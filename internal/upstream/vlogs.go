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

	"github.com/TechDufus/dash/internal/model"
)

const (
	maxLogText = 200
	logLimit   = "500"

	maxLogRecord   = 256 << 10 // bytes; longer records are skipped
	maxLogMsg      = 8192      // runes kept per message
	maxLogResponse = 16 << 20  // bytes read per query
)

var (
	k8sNameRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	logSinces  = []string{"5m", "15m", "1h", "6h", "24h"}
	logEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

// LogQuery selects container logs. Namespace is required. StartOffset applies to tails only:
// VictoriaLogs replays the last 5s by default (v1.52 start_offset default), and a positive
// StartOffset widens that replay window to this far back; BuildQuery ignores it.
type LogQuery struct {
	Namespace, Pod, Container, Text, Since string
	StartOffset                            time.Duration
}

// BuildQuery renders q as a LogsQL query. tail=true omits the time filter and
// the sort, as /select/logsql/tail wants.
func BuildQuery(q LogQuery, tail bool) (string, error) {
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
	b.WriteString(`kubernetes.pod_namespace:="` + q.Namespace + `"`)
	if q.Pod != "" {
		b.WriteString(` kubernetes.pod_name:="` + q.Pod + `"`)
	}
	if q.Container != "" {
		b.WriteString(` kubernetes.container_name:="` + q.Container + `"`)
	}
	if t := cleanLogText(q.Text); t != "" {
		b.WriteString(` "` + logEscaper.Replace(t) + `"`)
	}
	if !tail {
		b.WriteString(" | sort by (_time desc)")
	}
	return b.String(), nil
}

// cleanLogText drops NUL and line breaks, then keeps at most 200 runes.
func cleanLogText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == 0 || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
	return truncate(s, maxLogText)
}

// VLogs queries VictoriaLogs.
type VLogs struct{ short, stream client }

func NewVLogs(baseURL string, rt http.RoundTripper) *VLogs {
	return &VLogs{
		short:  newClient(baseURL, rt, 10*time.Second),
		stream: newClient(baseURL, rt, 0),
	}
}

// recordLine turns one VictoriaLogs record into a LogLine. vlagent moves a JSON log line's "msg" into
// _msg and every other key into its own field, so the remaining fields are appended after the
// message as " k=v" (sorted by key) to keep structured lines such as audit entries readable.
func recordLine(rec map[string]string) (model.LogLine, bool) {
	t, err := time.Parse(time.RFC3339Nano, rec["_time"])
	if err != nil {
		return model.LogLine{}, false
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if strings.HasPrefix(k, "_") || strings.HasPrefix(k, "kubernetes.") || k == "stream" {
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
	if t := truncate(m, maxLogMsg); len(t) < len(m) {
		m = t + "…"
	}
	return model.LogLine{Time: t, Pod: rec["kubernetes.pod_name"], Container: rec["kubernetes.container_name"], Msg: m}, true
}

// Query returns up to 500 of the newest matching lines, oldest first.
func (v *VLogs) Query(ctx context.Context, q LogQuery) ([]model.LogLine, error) {
	expr, err := BuildQuery(q, false)
	if err != nil {
		return nil, err
	}
	resp, err := v.short.open(ctx, http.MethodGet, "/select/logsql/query", url.Values{"query": {expr}, "limit": {logLimit}}, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := []model.LogLine{}
	err = readLogLines(io.LimitReader(resp.Body, maxLogResponse), func(l model.LogLine) error {
		out = append(out, l)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The query sorts newest first; the UI shows chronological order.
	slices.Reverse(out)
	return out, nil
}

// Tail streams new matching lines to emit until ctx ends (returns nil), the
// stream ends, or emit fails.
func (v *VLogs) Tail(ctx context.Context, q LogQuery, emit func(model.LogLine) error) error {
	expr, err := BuildQuery(q, true)
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
	err = readLogLines(resp.Body, func(l model.LogLine) error {
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

// readLogLines decodes JSON lines from r and passes each to emit; malformed
// and oversized lines are skipped. An emit error is returned as is.
func readLogLines(r io.Reader, emit func(model.LogLine) error) error {
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
				if l, ok := recordLine(rec); ok {
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
