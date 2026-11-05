package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/model"
)

const (
	maxLogText = 200

	// DefaultLogLimit is the most lines a query returns when NewVLogs gets no positive limit.
	DefaultLogLimit = 500

	maxLogRecord   = 256 << 10 // bytes; longer records are skipped
	maxLogMsg      = 8192      // runes kept per message
	maxLogResponse = 16 << 20  // bytes read per query
)

var (
	k8sNameRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	logSinces  = []string{"5m", "15m", "1h", "6h", "24h"}
	logEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

// LogFields names the fields of the log records that identify the pod a line came from. The
// names are concatenated into the LogsQL query, so they must be valid field names (letters,
// digits, _ and ., not starting with a digit or a dot): pluginconf.VictoriaLogs.Validate
// enforces that before a plugin builds a client.
type LogFields struct{ Namespace, Pod, Container string }

// DefaultLogFields is the mapping of the Kubernetes log shippers (vlagent, vector).
func DefaultLogFields() LogFields {
	return LogFields{
		Namespace: "kubernetes.pod_namespace",
		Pod:       "kubernetes.pod_name",
		Container: "kubernetes.container_name",
	}
}

// hidden reports whether record field k is left out of a line's message: the internal fields
// (_time, _msg, _stream…), "stream", and the family of the pod fields. The family is every key
// that starts like a configured field up to and including its first dot (the default mapping
// gives "kubernetes.", which hides kubernetes.pod_ip and the labels too); a configured field
// with no dot is hidden by name, since its value already is the Pod or Container of the line.
func (f LogFields) hidden(k string) bool {
	if strings.HasPrefix(k, "_") || k == "stream" {
		return true
	}
	for _, name := range [...]string{f.Namespace, f.Pod, f.Container} {
		if k == name {
			return true
		}
		if i := strings.IndexByte(name, '.'); i >= 0 && strings.HasPrefix(k, name[:i+1]) {
			return true
		}
	}
	return false
}

// LogQuery selects container logs. Namespace is required. StartOffset applies to tails only:
// VictoriaLogs replays the last 5s by default (v1.52 start_offset default), and a positive
// StartOffset widens that replay window to this far back; BuildQuery ignores it.
type LogQuery struct {
	Namespace, Pod, Container, Text, Since string
	StartOffset                            time.Duration
}

// BuildQuery renders q as a LogsQL query over the record fields f names. tail=true omits the
// time filter and the sort, as /select/logsql/tail wants.
func BuildQuery(q LogQuery, f LogFields, tail bool) (string, error) {
	if !k8sNameRe.MatchString(q.Namespace) {
		return "", fmt.Errorf("%w: bad namespace", ErrInvalid)
	}
	if q.Pod != "" && !k8sNameRe.MatchString(q.Pod) {
		return "", fmt.Errorf("%w: bad pod", ErrInvalid)
	}
	if q.Container != "" && !k8sNameRe.MatchString(q.Container) {
		return "", fmt.Errorf("%w: bad container", ErrInvalid)
	}
	since := q.Since
	if since == "" {
		since = "15m"
	}
	if !slices.Contains(logSinces, since) {
		return "", fmt.Errorf("%w: bad since", ErrInvalid)
	}

	var b strings.Builder
	if !tail {
		b.WriteString("_time:" + since + " ")
	}
	b.WriteString(f.Namespace + `:="` + q.Namespace + `"`)
	if q.Pod != "" {
		b.WriteString(` ` + f.Pod + `:="` + q.Pod + `"`)
	}
	if q.Container != "" {
		b.WriteString(` ` + f.Container + `:="` + q.Container + `"`)
	}
	t, err := cleanLogText(q.Text)
	if err != nil {
		return "", err
	}
	if t != "" {
		b.WriteString(` "` + logEscaper.Replace(t) + `"`)
	}
	if !tail {
		b.WriteString(" | sort by (_time desc)")
	}
	return b.String(), nil
}

// errLogTextTooLong refuses a search over maxLogText runes. It never repeats the text: a search
// goes into the query string, and a cut one would put half of a secret pasted into it where the
// URL is logged (a transport error names it).
var errLogTextTooLong = fmt.Errorf("%w: q: at most %d characters", ErrInvalid, maxLogText)

// cleanLogText drops NUL and line breaks, then refuses what is longer than maxLogText runes.
func cleanLogText(s string) (string, error) {
	s = strings.Map(func(r rune) rune {
		if r == 0 || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > maxLogText {
		return "", errLogTextTooLong
	}
	return s, nil
}

// VLogs queries VictoriaLogs.
type VLogs struct {
	short, stream client
	limit         int
	fields        LogFields
}

// NewVLogs builds the client for the VictoriaLogs at baseURL. limit is the most lines a query
// returns (DefaultLogLimit when it is not positive). It reads the default record fields; use
// WithFields for another mapping.
func NewVLogs(baseURL string, rt http.RoundTripper, limit int, opts ...Option) *VLogs {
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	return &VLogs{
		short:  newClient(baseURL, rt, 10*time.Second, opts...),
		stream: newStreamClient(baseURL, rt, opts...),
		limit:  limit,
		fields: DefaultLogFields(),
	}
}

// WithFields returns a client that reads the record fields f names, for queries and for the pod
// and container of every line.
func (v *VLogs) WithFields(f LogFields) *VLogs {
	c := *v
	c.fields = f
	return &c
}

// recordLine turns one VictoriaLogs record into a LogLine, reading the pod and the container
// through f. vlagent moves a JSON log line's "msg" into _msg and every other key into its own
// field, so the remaining fields are appended after the message as " k=v" (sorted by key) to keep
// structured lines such as audit entries readable; the fields f.hidden names are not. A message
// over maxLogMsg runes is cut by cut, which redacts a secret that straddles the cut whole.
func recordLine(rec map[string]string, f LogFields, cut cutFunc) (model.LogLine, bool) {
	t, err := time.Parse(time.RFC3339Nano, rec["_time"])
	if err != nil {
		return model.LogLine{}, false
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if f.hidden(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var msg strings.Builder
	msg.WriteString(rec["_msg"])
	for _, k := range keys {
		v := rec[k]
		if v == "" || strings.ContainsAny(v, " \t\"") {
			v = strconv.Quote(v)
		}
		msg.WriteString(" " + k + "=" + v)
	}
	m := msg.String()
	if out, ok := cut(m, maxLogMsg); ok {
		m = out + "…"
	}
	return model.LogLine{Time: t, Pod: rec[f.Pod], Container: rec[f.Container], Msg: m}, true
}

// Query returns up to the configured limit of the newest matching lines, oldest first.
func (v *VLogs) Query(ctx context.Context, q LogQuery) ([]model.LogLine, error) {
	expr, err := BuildQuery(q, v.fields, false)
	if err != nil {
		return nil, err
	}
	resp, err := v.short.open(ctx, http.MethodGet, "/select/logsql/query", url.Values{"query": {expr}, "limit": {strconv.Itoa(v.limit)}}, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := []model.LogLine{}
	err = readLogLines(io.LimitReader(resp.Body, maxLogResponse), v.fields, v.short.cut, func(l model.LogLine) error {
		out = append(out, l)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The query sorts newest first and VictoriaLogs honours the limit; an upstream that does not
	// still cannot make dash return more than the configured number of lines.
	out = out[:min(len(out), v.limit)]
	// The UI shows chronological order.
	slices.Reverse(out)
	return out, nil
}

// Tail streams new matching lines to emit until ctx ends (returns nil), the
// stream ends, or emit fails.
func (v *VLogs) Tail(ctx context.Context, q LogQuery, emit func(model.LogLine) error) error {
	expr, err := BuildQuery(q, v.fields, true)
	if err != nil {
		return err
	}
	params := url.Values{"query": {expr}, "offset": {"1s"}}
	if q.StartOffset > 0 {
		// Whole seconds, rounded up, in the plain duration form LogsQL accepts.
		params.Set("start_offset", strconv.FormatInt(int64((q.StartOffset+time.Second-1)/time.Second), 10)+"s")
	}
	resp, err := v.stream.open(ctx, http.MethodGet, "/select/logsql/tail", params, nil, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	var emitErr error
	err = readLogLines(resp.Body, v.fields, v.stream.cut, func(l model.LogLine) error {
		emitErr = emit(l)
		return emitErr
	})
	if emitErr != nil {
		return emitErr
	}
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// readLogLines decodes JSON lines from r and passes each to emit, its message cut by cut;
// malformed and oversized lines are skipped. An emit error is returned as is.
func readLogLines(r io.Reader, f LogFields, cut cutFunc, emit func(model.LogLine) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	skip := false // the current record exceeds maxLogRecord
	for {
		chunk, err := br.ReadSlice('\n')
		if !skip {
			if len(buf)+len(chunk) > maxLogRecord {
				skip, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue // the record goes on
		}
		if !skip && len(bytes.TrimSpace(buf)) > 0 {
			var rec map[string]string
			if json.Unmarshal(buf, &rec) == nil {
				if l, ok := recordLine(rec, f, cut); ok {
					if eerr := emit(l); eerr != nil {
						return eerr
					}
				}
			}
		}
		buf, skip = buf[:0], false
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
