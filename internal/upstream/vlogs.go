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
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

const (
	maxLogText = 200
	logLimit   = "500"
)

var (
	k8sNameRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	logSinces  = []string{"5m", "15m", "1h", "6h", "24h"}
	logEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

// LogQuery selects container logs. Namespace is required.
type LogQuery struct{ Namespace, Pod, Container, Text, Since string }

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

type vlRecord struct {
	Msg       string `json:"_msg"`
	Time      string `json:"_time"`
	Pod       string `json:"kubernetes.pod_name"`
	Container string `json:"kubernetes.container_name"`
}

func (r vlRecord) line() (model.LogLine, bool) {
	t, err := time.Parse(time.RFC3339Nano, r.Time)
	if err != nil {
		return model.LogLine{}, false
	}
	return model.LogLine{Time: t, Pod: r.Pod, Container: r.Container, Msg: r.Msg}, true
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
	err = readLogLines(resp.Body, func(l model.LogLine) error {
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
	resp, err := v.stream.open(ctx, http.MethodGet, "/select/logsql/tail", url.Values{"query": {expr}}, nil, nil)
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
// lines are skipped. An emit error is returned as is .
func readLogLines(r io.Reader, emit func(model.LogLine) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		b, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			var rec vlRecord
			if json.Unmarshal(b, &rec) == nil {
				if l, ok := rec.line(); ok {
					if eerr := emit(l); eerr != nil {
						return eerr
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
