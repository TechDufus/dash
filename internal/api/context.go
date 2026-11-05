package api

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	dashmcp "github.com/TechDufus/dash/internal/mcp"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// logsInstance is the plugin whose log route a bundle reads its log lines through.
const logsInstance plugin.ID = "victorialogs"

// The reasons a bundle has no log lines although its part names logs (ContextBundle.LogsNote).
const (
	logsNotRequested  = "not_requested"
	logsScopeRequired = "logs_scope_required"
	logsDenied        = "namespace_denied"
	logsUnavailable   = "logs_unavailable"
)

// contextDoc is the description of GET /api/v1/context in the OpenAPI document.
const contextDoc = "The context bundle of one object (design N.4): the owning plugin's summary, facts, conditions, " +
	"at most 20 events, related refs, links, series and logs; the overview's attention items for the ref; a summary " +
	"of each series over 1h; and at most 100 of the newest log lines, only for a caller with the logs scope and " +
	"never for a namespace victorialogs denies. format=markdown answers text/markdown in which every lab-sourced " +
	"text sits in a fenced block after the untrusted-data sentence. 400 for a malformed ref, format or include, " +
	"404 for an object no running plugin knows."

// contextBundle serves GET /api/v1/context.
func (s *Server) contextBundle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := cmp.Or(q.Get("format"), "json")
	if format != "json" && format != "markdown" {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "format must be json or markdown")
		return
	}
	opts, ok := includeOf(q.Get("include"))
	if !ok {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "include must list events, related, attention, series or logs")
		return
	}
	b, f := s.buildContext(r, q.Get("ref"), opts)
	if f != nil {
		s.writeFailure(w, f)
		return
	}
	if format == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bundle.Markdown(b)))
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// includeOf parses a comma-separated include list; empty means every part.
func includeOf(s string) (model.ContextOptions, bool) {
	var o model.ContextOptions
	if s == "" {
		return o, true
	}
	for p := range strings.SplitSeq(s, ",") {
		inc := model.ContextInclude(strings.TrimSpace(p))
		if !slices.Contains(model.ContextIncludes(), inc) {
			return o, false
		}
		if !slices.Contains(o.Include, inc) {
			o.Include = append(o.Include, inc)
		}
	}
	return o, true
}

// buildContext composes the context bundle of ref for the principal of r: the owning plugin's
// part, the attention items, the series summaries and the log lines, every string scrubbed. The
// log lines are read through the victorialogs route under the core's read policy (the logs
// scope, the anonymous viewer's exclusion and its denyNamespaces), as r's principal.
func (s *Server) buildContext(r *http.Request, ref string, o model.ContextOptions) (model.ContextBundle, *failure) {
	id, _, _, ok := model.ParseRef(ref)
	if !ok {
		return model.ContextBundle{}, &failure{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "invalid ref"}
	}
	unknown := &failure{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "unknown object"}
	if s.d.Plugins == nil {
		return model.ContextBundle{}, unknown
	}
	objs, ok := s.d.Plugins.Objects(plugin.ID(id))
	if !ok {
		return model.ContextBundle{}, unknown
	}
	part, err := objs.Context(r.Context(), ref, o)
	switch {
	case errors.Is(err, plugin.ErrNotFound):
		return model.ContextBundle{}, unknown
	case err != nil:
		return model.ContextBundle{}, &failure{Message: id, Upstream: err}
	}
	part.Events = plugin.NewestEvents(part.Events)
	b := model.ContextBundle{
		Ref: ref, GeneratedAt: s.bundleNow(), Part: part,
		Attention: []model.AttentionItem{}, Series: []model.SeriesSummary{}, Logs: []model.LogLine{},
	}
	if o.Wants(model.ContextIncludeAttention) {
		b.Attention = s.attentionFor(ref)
	}
	if o.Wants(model.ContextIncludeSeries) {
		for _, sr := range part.Series {
			b.Series = append(b.Series, s.seriesSummary(r, sr))
		}
	}
	if part.Logs != nil {
		switch p := principalFrom(r.Context()); {
		case !o.Wants(model.ContextIncludeLogs):
			b.LogsNote = logsNotRequested
		case !mayReadLogs(p):
			b.LogsNote = logsScopeRequired
		default:
			b.Logs, b.LogsNote = s.readLogs(r, *part.Logs, "1h", "", model.ContextMaxLogLines)
		}
	}
	scrubbed, err := bundle.ScrubTyped(b, s.d.Secrets.ScrubLogLine)
	if err != nil {
		return model.ContextBundle{}, &failure{Status: http.StatusInternalServerError, Code: model.ErrCodeInternal, Message: "internal error"}
	}
	return scrubbed, nil
}

// bundleNow is the bundle's time: demo mode's fixed clock when there is one.
func (s *Server) bundleNow() time.Time {
	if c := s.d.Cfg.DemoClock; !c.IsZero() {
		return c.UTC()
	}
	return s.now().UTC()
}

// mayReadLogs reports whether p may read log lines: the logs scope (every person has it), and
// never the anonymous viewer.
func mayReadLogs(p auth.Principal) bool {
	return p.Kind != auth.KindAnonymous && p.HasScope("logs")
}

// attentionFor is the overview's attention items whose ref is ref, never nil.
func (s *Server) attentionFor(ref string) []model.AttentionItem {
	out := []model.AttentionItem{}
	ov, ok := s.overview()
	if !ok {
		return out
	}
	for _, a := range ov.Attention {
		if a.Ref == ref {
			out = append(out, a)
		}
	}
	return out
}

// overview is the current core.overview payload.
func (s *Server) overview() (model.OverviewData, bool) {
	var d model.OverviewData
	if s.d.Hub == nil {
		return d, false
	}
	ev, ok := s.d.Hub.Latest("", model.TopicOverview)
	if !ok {
		return d, false
	}
	var env model.Envelope
	if json.Unmarshal(ev.Data, &env) != nil || json.Unmarshal(env.Data, &d) != nil {
		return d, false
	}
	return d, true
}

// seriesSummary reads a series a part names over the bundle's range and sums up each line.
func (s *Server) seriesSummary(r *http.Request, sr model.SeriesRef) model.SeriesSummary {
	out := model.SeriesSummary{Name: sr.Name, Args: sr.Args, Range: model.ContextSeriesRange, Lines: []model.SeriesLineSummary{}}
	if out.Args == nil {
		out.Args = map[string]string{}
	}
	args := queries.Args{VMID: sr.Args["vmid"], Namespace: sr.Args["namespace"], Pod: sr.Args["pod"], Instance: sr.Args["node"]}
	lines, _, f := s.readSeries(r.Context(), sr.Name, model.ContextSeriesRange, args)
	switch {
	case f == nil:
	case f.Upstream != nil:
		out.Error = "metrics_unavailable"
		return out
	case f.Status == http.StatusNotFound:
		out.Error = "not_found"
		return out
	default:
		out.Error = "invalid_input"
		return out
	}
	for _, l := range lines {
		if sum, ok := dashmcp.Summarize(l); ok {
			out.Lines = append(out.Lines, sum)
		}
	}
	return out
}

// readLogs reads at most limit of the newest log lines of ref, oldest first, through the
// victorialogs route as r's principal, so the core's read policy for that route applies: its
// scope, and its denyNamespaces (refused, and audited as a denial). note is "" when the lines
// were read, else namespace_denied or logs_unavailable.
func (s *Server) readLogs(r *http.Request, ref model.LogsRef, since, text string, limit int) (lines []model.LogLine, note string) {
	lines = []model.LogLine{}
	if s.d.Plugins == nil {
		return lines, logsUnavailable
	}
	h, ok := s.d.Plugins.Router(logsInstance, s.routeGuard())
	if !ok {
		return lines, logsUnavailable
	}
	q := url.Values{"ns": {ref.Namespace}, "pod": {ref.Pod}, "since": {since}}
	if ref.Container != "" {
		q.Set("container", ref.Container)
	}
	if text != "" {
		q.Set("q", text)
	}
	sub := r.Clone(r.Context())
	sub.Method, sub.Body, sub.ContentLength = http.MethodGet, http.NoBody, 0
	sub.URL = &url.URL{Path: "/api/v1/plugins/" + string(logsInstance) + "/logs", RawQuery: q.Encode()}
	sub.RequestURI = sub.URL.RequestURI()
	rec := &bufferWriter{header: http.Header{}}
	h.ServeHTTP(rec, sub)
	switch {
	case rec.status == http.StatusForbidden:
		return lines, logsDenied
	case rec.status != http.StatusOK:
		return lines, logsUnavailable
	}
	if err := json.Unmarshal(rec.body.Bytes(), &lines); err != nil || lines == nil {
		return []model.LogLine{}, logsUnavailable
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, ""
}

// bufferWriter is the answer of an in-process request.
type bufferWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferWriter) Header() http.Header { return b.header }

func (b *bufferWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferWriter) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}
