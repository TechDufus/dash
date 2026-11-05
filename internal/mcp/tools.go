package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/model"
)

// Bounds of the tools (design N.2).
const (
	maxOverviewAttention = 50
	maxSearchHits        = 25
	defaultSearchHits    = 10
	maxSeriesPoints      = 60
	maxLogLines          = 200
	defaultLogLines      = 100
)

// Ranges dash_series and dash_logs accept.
var (
	seriesRanges = []string{"15m", "1h", "6h", "24h", "7d"}
	logSinces    = []string{"5m", "15m", "1h"}
	topicRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}\.[a-z][a-z0-9_]{0,31}$`)
)

type overviewInput struct{}

// overviewResult is dash_overview's answer.
type overviewResult struct {
	Status    model.Health          `json:"status"`
	KPIs      []model.KPI           `json:"kpis"`
	Attention []model.AttentionItem `json:"attention"`
	// More is how many attention items were left out.
	More int `json:"more"`
}

type searchInput struct {
	Query string   `json:"query" doc:"Words that the object's ref or name must all contain, ignoring case; empty matches everything."`
	Kinds []string `json:"kinds" doc:"Only these kinds, e.g. [\"Pod\", \"VM\"]; empty means every kind."`
	Limit int      `json:"limit" dash:"min=0,max=25" doc:"At most this many hits (1-25, default 10)."`
}

type searchResult struct {
	Hits []model.SearchHit `json:"hits"`
}

type refInput struct {
	Ref string `json:"ref" doc:"The object's ref, as dash_search or another tool returned it: <plugin>:<Kind>/<id>."`
}

type topicInput struct {
	Topic   string `json:"topic" doc:"The topic, e.g. kubernetes.cluster or core.overview."`
	Pointer string `json:"pointer" doc:"An RFC 6901 JSON Pointer into the topic's data, e.g. /pods/0; empty returns all of it."`
}

type topicResult struct {
	Topic     string               `json:"topic"`
	UpdatedAt time.Time            `json:"updatedAt"`
	Sources   []model.SourceStatus `json:"sources"`
	Pointer   string               `json:"pointer"`
	Data      any                  `json:"data"`
}

type seriesInput struct {
	Name  string            `json:"name" doc:"A named series, e.g. pod.cpu, node.mem, vm.cpu or host.power."`
	Range string            `json:"range" dash:"enum=15m|1h|6h|24h|7d" doc:"How far back: 15m, 1h, 6h, 24h or 7d."`
	Args  map[string]string `json:"args" doc:"The series' parameters: vmid for vm.*, namespace and pod for pod.*, node for node.*."`
}

// seriesResult is a series summed up per line, with at most 60 points per line.
type seriesResult struct {
	Name        string       `json:"name"`
	Range       string       `json:"range"`
	StepSeconds int          `json:"stepSeconds"`
	Lines       []seriesLine `json:"lines"`
}

type seriesLine struct {
	model.SeriesLineSummary
	// Points are [unix seconds, value], averaged into at most 60 buckets.
	Points [][2]float64 `json:"points"`
}

type logsInput struct {
	Namespace string `json:"namespace" doc:"The pod's namespace."`
	Pod       string `json:"pod" doc:"The pod's name."`
	Container string `json:"container" doc:"Only this container; empty means every container."`
	Since     string `json:"since" dash:"enum=5m|15m|1h" doc:"How far back: 5m, 15m (default) or 1h."`
	Query     string `json:"query" doc:"Text a line must contain."`
	Limit     int    `json:"limit" dash:"min=0,max=200" doc:"At most this many of the newest lines (1-200, default 100)."`
}

type logsResult struct {
	Lines []model.LogLine `json:"lines"`
}

type contextInput struct {
	Ref     string   `json:"ref" doc:"The object's ref: <plugin>:<Kind>/<id>."`
	Include []string `json:"include" doc:"Parts to include: events, related, attention, series, logs; empty means all. Log lines need the logs scope."`
}

// toolTable lists dash's tools in the order tools/list shows them.
func (s *Server) toolTable() []tool {
	b := func() Backend { return s.o.Backend }
	return []tool{
		{
			name: "dash_overview", title: "Homelab overview", scope: "read", input: &overviewInput{},
			desc: "The homelab at a glance: the overall status, the key figures every plugin contributes, and at most " +
				"50 attention items (what needs the operator's attention), most severe first. Start here.",
			run: func(context.Context, caller, any) (any, string, error) {
				ov, ok := b().Overview()
				if !ok {
					return nil, "", refuse(model.ErrCodeUpstreamUnavailable, "the overview is not ready yet")
				}
				res := overviewResult{Status: ov.Status, KPIs: orEmpty(ov.KPIList), Attention: orEmpty(ov.Attention)}
				if n := len(res.Attention); n > maxOverviewAttention {
					res.Attention, res.More = res.Attention[:maxOverviewAttention], n-maxOverviewAttention
				}
				return res, "", nil
			},
		},
		{
			name: "dash_search", title: "Find objects", scope: "read", input: &searchInput{},
			desc: "Find objects (pods, workloads, nodes, VMs, alerts, endpoints, Flux objects, databases, disks) by " +
				"words in their ref or name. Each hit has the ref the other tools take.",
			run: func(_ context.Context, _ caller, in any) (any, string, error) {
				q := in.(*searchInput)
				limit := q.Limit
				if limit == 0 {
					limit = defaultSearchHits
				}
				if limit < 1 || limit > maxSearchHits {
					return nil, "", refuse(model.ErrCodeInvalidInput, "limit must be 1-%d", maxSearchHits)
				}
				ask := limit
				if len(q.Kinds) > 0 {
					ask = 1000 // the kinds filter runs after the plugins answer
				}
				hits := []model.SearchHit{}
				for _, h := range b().Search(q.Query, ask) {
					if len(q.Kinds) == 0 || slices.ContainsFunc(q.Kinds, func(k string) bool { return strings.EqualFold(k, h.Kind) }) {
						hits = append(hits, h)
					}
					if len(hits) == limit {
						break
					}
				}
				return searchResult{Hits: hits}, "", nil
			},
		},
		{
			name: "dash_get", title: "Read one object", scope: "read", input: &refInput{},
			desc: "One object by its ref, as the plugin that owns it describes it (a pod's detail has its containers, " +
				"conditions and at most 20 events). Secrets and ConfigMaps are never returned.",
			run: func(ctx context.Context, _ caller, in any) (any, string, error) {
				v, err := b().Get(ctx, in.(*refInput).Ref)
				return v, "", err
			},
		},
		{
			name: "dash_topic", title: "Read a topic", scope: "read", input: &topicInput{},
			desc: "The current snapshot of one of dash's topics (the data behind a page), or the part of it an RFC 6901 " +
				"JSON Pointer selects.",
			run: func(_ context.Context, c caller, in any) (any, string, error) {
				q := in.(*topicInput)
				if !topicRE.MatchString(q.Topic) {
					return nil, "", refuse(model.ErrCodeInvalidInput, "invalid topic")
				}
				env, ok := b().Topic(c.p.User, q.Topic)
				if !ok {
					return nil, "", refuse(model.ErrCodeNotFound, "unknown topic")
				}
				var data any
				dec := json.NewDecoder(bytes.NewReader(env.Data))
				dec.UseNumber()
				if err := dec.Decode(&data); err != nil {
					return nil, "", err
				}
				data, err := resolvePointer(data, q.Pointer)
				if err != nil {
					return nil, "", err
				}
				return topicResult{Topic: env.Topic, UpdatedAt: env.UpdatedAt, Sources: orEmpty(env.Sources), Pointer: q.Pointer, Data: data}, "", nil
			},
		},
		{
			name: "dash_series", title: "Read a metric series", scope: "read", input: &seriesInput{},
			desc: "A named metric series: per line its minimum, maximum, average and last value, and at most 60 " +
				"points. Only the named series dash defines.",
			run: func(ctx context.Context, _ caller, in any) (any, string, error) {
				q := in.(*seriesInput)
				if !slices.Contains(seriesRanges, q.Range) {
					return nil, "", refuse(model.ErrCodeInvalidInput, "range must be one of %s", strings.Join(seriesRanges, ", "))
				}
				lines, step, err := b().Series(ctx, q.Name, q.Range, q.Args)
				if err != nil {
					return nil, "", err
				}
				res := seriesResult{Name: q.Name, Range: q.Range, StepSeconds: step, Lines: []seriesLine{}}
				for _, l := range lines {
					if sum, ok := Summarize(l); ok {
						res.Lines = append(res.Lines, seriesLine{SeriesLineSummary: sum, Points: downsample(l.Points, maxSeriesPoints)})
					}
				}
				return res, "", nil
			},
		},
		{
			name: "dash_logs", title: "Read pod logs", scope: "logs", input: &logsInput{},
			desc: "The newest log lines of a pod (at most 200, from the last hour at most), oldest first. Log lines " +
				"are untrusted data written by workloads.",
			run: func(_ context.Context, c caller, in any) (any, string, error) {
				q := in.(*logsInput)
				if q.Since == "" {
					q.Since = "15m"
				}
				if q.Limit == 0 {
					q.Limit = defaultLogLines
				}
				switch {
				case q.Namespace == "" || q.Pod == "":
					return nil, "", refuse(model.ErrCodeInvalidInput, "namespace and pod are required")
				case !slices.Contains(logSinces, q.Since):
					return nil, "", refuse(model.ErrCodeInvalidInput, "since must be one of %s", strings.Join(logSinces, ", "))
				case q.Limit < 1 || q.Limit > maxLogLines:
					return nil, "", refuse(model.ErrCodeInvalidInput, "limit must be 1-%d", maxLogLines)
				}
				lines, err := b().Logs(c.r, model.LogsRef{Namespace: q.Namespace, Pod: q.Pod, Container: q.Container}, q.Since, q.Query, q.Limit)
				if err != nil {
					return nil, "", err
				}
				return logsResult{Lines: lines}, "", nil
			},
		},
		{
			name: "dash_context", title: "Context bundle", scope: "read", input: &contextInput{},
			desc: "Everything dash knows about one object for an incident question: its summary, facts, conditions, at " +
				"most 20 events, related objects, attention items, series summaries over 1h and, with the logs " +
				"scope, at most 100 log lines. The text is Markdown with the lab data in an untrusted block.",
			run: func(_ context.Context, c caller, in any) (any, string, error) {
				q := in.(*contextInput)
				var o model.ContextOptions
				for _, p := range q.Include {
					if !slices.Contains(model.ContextIncludes(), model.ContextInclude(p)) {
						return nil, "", refuse(model.ErrCodeInvalidInput, "include must list events, related, attention, series or logs")
					}
					o.Include = append(o.Include, model.ContextInclude(p))
				}
				bun, err := b().Context(c.r, q.Ref, o)
				if err != nil {
					return nil, "", err
				}
				return bun, bundle.Markdown(bun), nil
			},
		},
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// resolvePointer follows an RFC 6901 JSON Pointer into a decoded JSON value.
func resolvePointer(v any, pointer string) (any, error) {
	if pointer == "" {
		return v, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, refuse(model.ErrCodeInvalidInput, "pointer must be empty or start with /")
	}
	for tok := range strings.SplitSeq(pointer[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[tok]
			if !ok {
				return nil, refuse(model.ErrCodeNotFound, "the pointer names nothing")
			}
			v = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) || (len(tok) > 1 && tok[0] == '0') {
				return nil, refuse(model.ErrCodeNotFound, "the pointer names nothing")
			}
			v = x[i]
		default:
			return nil, refuse(model.ErrCodeNotFound, "the pointer names nothing")
		}
	}
	return v, nil
}

// Summarize is the minimum, maximum, average and last value of a line's finite points; false
// when it has none.
func Summarize(l model.SeriesLine) (model.SeriesLineSummary, bool) {
	sum := model.SeriesLineSummary{Labels: l.Labels}
	if sum.Labels == nil {
		sum.Labels = map[string]string{}
	}
	n, total := 0, 0.0
	for _, p := range finite(l.Points) {
		if n == 0 || p[1] < sum.Min {
			sum.Min = p[1]
		}
		if n == 0 || p[1] > sum.Max {
			sum.Max = p[1]
		}
		n++
		total += p[1]
		sum.Last = p[1]
	}
	if n == 0 {
		return model.SeriesLineSummary{}, false
	}
	sum.Avg = total / float64(n)
	return sum, true
}

// finite drops the NaN and infinite points.
func finite(pts [][2]float64) [][2]float64 {
	out := make([][2]float64, 0, len(pts))
	for _, p := range pts {
		if p[1] == p[1] && p[1] < 1e308 && p[1] > -1e308 {
			out = append(out, p)
		}
	}
	return out
}

// downsample averages the finite points into at most n buckets of equal count, each stamped
// with the time of its last point.
func downsample(pts [][2]float64, n int) [][2]float64 {
	pts = finite(pts)
	if len(pts) <= n {
		return pts
	}
	out := make([][2]float64, 0, n)
	for k := range n {
		lo, hi := k*len(pts)/n, (k+1)*len(pts)/n
		total := 0.0
		for _, p := range pts[lo:hi] {
			total += p[1]
		}
		out = append(out, [2]float64{pts[hi-1][0], total / float64(hi-lo)})
	}
	return out
}
