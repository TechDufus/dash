package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// toolsBackend has n objects of kind Pod, knows no object by ref and one topic, and serves two
// series; it records the kinds a search asked for.
type toolsBackend struct {
	Backend
	n     int
	kinds *[]string
}

func (b toolsBackend) Search(_ string, kinds []string, limit int) []model.SearchHit {
	*b.kinds = kinds
	out := []model.SearchHit{}
	for i := range min(b.n, limit) {
		out = append(out, model.SearchHit{Ref: fmt.Sprintf("kubernetes:Pod/a/p%d", i), Kind: "Pod"})
	}
	return out
}

func (toolsBackend) Get(context.Context, string) (any, error) {
	return nil, &Error{Code: model.ErrCodeNotFound, Message: "unknown object"}
}

func (toolsBackend) Context(*http.Request, string, model.ContextOptions) (model.ContextBundle, error) {
	return model.ContextBundle{}, &Error{Code: model.ErrCodeNotFound, Message: "unknown object"}
}

func (toolsBackend) Topic(_, topic string) (model.Envelope, bool) {
	return model.Envelope{Topic: topic, Data: json.RawMessage(`{}`)}, topic == "core.overview"
}

func (toolsBackend) SeriesNames() []string { return []string{"pod.cpu", "host.power"} }

func (toolsBackend) Changes(context.Context) <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

// The input schemas say what is required, and a limit is at least 1.
func TestToolSchemasNameTheRequiredFields(t *testing.T) {
	s := New(Options{Backend: toolsBackend{kinds: new([]string)}, Context: canceled()})
	for name, want := range map[string][]string{
		"dash_get": {"ref"}, "dash_topic": {"topic"}, "dash_series": {"name", "range"},
		"dash_logs": {"namespace", "pod"}, "dash_context": {"ref"}, "dash_search": nil, "dash_overview": nil,
	} {
		tl, _ := s.toolNamed(name)
		raw, _ := json.Marshal(tl.sdkTool().InputSchema)
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Minimum *int `json:"minimum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		slices.Sort(schema.Required)
		slices.Sort(want)
		if !slices.Equal(schema.Required, want) {
			t.Errorf("%s requires %v, want %v", name, schema.Required, want)
		}
		if l, ok := schema.Properties["limit"]; ok && (l.Minimum == nil || *l.Minimum != 1) {
			t.Errorf("%s: limit minimum %v, want 1", name, l.Minimum)
		}
	}
}

// A refusal tells the agent what to do next: the ref format, dash_search, resources/list, the
// series names.
func TestToolErrorsAreActionable(t *testing.T) {
	url := serveHTTP(t, New(Options{Backend: toolsBackend{kinds: new([]string)}, Context: canceled()}))
	for _, tc := range []struct {
		tool       string
		args       map[string]any
		code, want string
	}{
		{"dash_get", map[string]any{"ref": "nope"}, model.ErrCodeInvalidInput, "a ref is <plugin>:<Kind>/<id>, e.g. kubernetes:Pod/ns/name"},
		{"dash_context", map[string]any{"ref": "pod/a"}, model.ErrCodeInvalidInput, "a ref is <plugin>:<Kind>/<id>"},
		{"dash_get", map[string]any{"ref": "kubernetes:Pod/a/gone"}, model.ErrCodeNotFound, "unknown object; find refs with dash_search"},
		{"dash_context", map[string]any{"ref": "kubernetes:Pod/a/gone"}, model.ErrCodeNotFound, "unknown object; find refs with dash_search"},
		{"dash_topic", map[string]any{"topic": "Not A Topic"}, model.ErrCodeInvalidInput, "invalid topic: resources/list lists"},
		{"dash_topic", map[string]any{"topic": "core.nothing"}, model.ErrCodeNotFound, "unknown topic: resources/list lists"},
		{"dash_series", map[string]any{"name": "pod.cpuu", "range": "1h"}, model.ErrCodeInvalidInput, `unknown series "pod.cpuu"; the series are: pod.cpu, host.power`},
		{"dash_search", map[string]any{"kinds": []string{"pod", "Pods"}}, model.ErrCodeInvalidInput,
			`unknown kind "Pods"; the kinds are Pod, Workload, Node, Namespace, PVC, App, BackupJob, Certificate, ExternalSecret (kubernetes); VM, Node, Storage (proxmox); `},
	} {
		res := callTool(t, url, tc.tool, tc.args)
		if !res.IsError || res.StructuredContent["code"] != tc.code || !strings.Contains(fmt.Sprint(res.StructuredContent["error"]), tc.want) {
			t.Errorf("%s %v: %v, want %s %q", tc.tool, tc.args, res.StructuredContent, tc.code, tc.want)
		}
	}
	for args, want := range map[string]string{
		`{"name":"dash_investigate","arguments":{}}`:                             "ref is required, e.g. kubernetes:Pod/ns/name",
		`{"name":"dash_investigate","arguments":{"ref":"x"}}`:                    "ref must be <plugin>:<Kind>/<id>, e.g. kubernetes:Pod/ns/name",
		`{"name":"dash_explain_alert","arguments":{"ref":"kubernetes:Pod/a/b"}}`: "ref must be the ref of an Alert, e.g. alertmanager:Alert/<fingerprint>",
	} {
		var params any
		_ = json.Unmarshal([]byte(args), &params)
		if rep := rpc(t, url, "prompts/get", params); rep.Error == nil || !strings.Contains(rep.Error.Message, want) {
			t.Errorf("prompts/get %s: %+v", args, rep.Error)
		}
	}
}

// dash_search hands the kinds to the plugins, so their limit counts only matching objects, and
// says when there are more hits than it returns.
func TestSearchPassesKindsAndSaysWhenCut(t *testing.T) {
	var kinds []string
	for _, tc := range []struct {
		n, limit int
		hits     int
		more     bool
	}{{11, 0, 10, true}, {10, 0, 10, false}, {3, 2, 2, true}, {2, 2, 2, false}} {
		url := serveHTTP(t, New(Options{Backend: toolsBackend{n: tc.n, kinds: &kinds}, Context: canceled()}))
		args := map[string]any{"kinds": []string{"Pod"}}
		if tc.limit > 0 {
			args["limit"] = tc.limit
		}
		res := callTool(t, url, "dash_search", args)
		hits, _ := res.StructuredContent["hits"].([]any)
		if res.IsError || len(hits) != tc.hits || res.StructuredContent["more"] != tc.more || !slices.Equal(kinds, []string{"Pod"}) {
			t.Errorf("%d objects, limit %d: %d hits, more %v, kinds %v", tc.n, tc.limit, len(hits), res.StructuredContent["more"], kinds)
		}
	}
}
