package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("body %s: %v", w.Body, err)
	}
}

// docManifests is a plugin with a typed topic, an action with an input, and a GET route.
func docManifests() ([]plugin.Manifest, func(plugin.ID) []plugin.RouteInfo) {
	f := plugintest.New("aa", plugintest.Typed("items", itemsData{}))
	f.M.Name, f.M.Description = "Alpha", "the alpha plugin"
	f.M.Actions = []plugin.ActionSpec{
		{Name: "restart", Title: "Restart", Destructive: true, Confirm: plugin.ConfirmTypeName, Input: func() any { return &restartIn{} }},
		{Name: "silence.create", Title: "Silence", Idempotent: true},
	}
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/vms/{vmid}", func(http.ResponseWriter, *http.Request) {}, plugin.RouteSpec{
			Summary: "One VM", Scope: "read", Query: []plugin.Param{{Name: "fields", Doc: "Fields to return."}}, Response: model.VMDetail{},
		})
		r.Get("/{$}", func(http.ResponseWriter, *http.Request) {}, plugin.RouteSpec{Summary: "The index"})
	}
	routes := func(id plugin.ID) []plugin.RouteInfo {
		if id == "aa" {
			return plugin.CollectRoutes(f)
		}
		return nil
	}
	return []plugin.Manifest{f.M}, routes
}

func TestOpenAPIIsDeterministic(t *testing.T) {
	ms, routes := docManifests()
	first, err := OpenAPI(ms, routes)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, err := OpenAPI(ms, routes)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("two runs gave different documents")
		}
	}
	if !bytes.HasSuffix(first, []byte("}\n")) || !json.Valid(first) {
		t.Error("the document is not indented JSON with a final newline")
	}
}

func openAPIDoc(t *testing.T, ms []plugin.Manifest, routes func(plugin.ID) []plugin.RouteInfo) map[string]any {
	t.Helper()
	b, err := OpenAPI(ms, routes)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// dig follows keys through nested objects.
func dig(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %q: %T is not an object", k, v)
		}
		if v, ok = m[k]; !ok {
			t.Fatalf("no %q (have %v)", k, keysOf(m))
		}
	}
	return v
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestOpenAPIDocumentsErrorEnvelopeAndEnvelopesPerTopic(t *testing.T) {
	ms, routes := docManifests()
	doc := openAPIDoc(t, ms, routes)
	if doc["openapi"] != "3.1.0" || dig(t, doc, "info", "version") != "v1" {
		t.Errorf("openapi %v, info.version %v", doc["openapi"], dig(t, doc, "info", "version"))
	}

	// Every operation answers the error body by default, and the body lists the codes.
	paths := dig(t, doc, "paths").(map[string]any)
	for path, ops := range paths {
		for method, op := range ops.(map[string]any) {
			if dig(t, op, "responses", "default").(map[string]any)["$ref"] != "#/components/responses/Error" {
				t.Errorf("%s %s has no default error response", method, path)
			}
		}
	}
	codes := dig(t, doc, "components", "schemas", "APIError", "properties", "code", "enum").([]any)
	var got []string
	for _, c := range codes {
		got = append(got, c.(string))
	}
	if !slices.Equal(got, model.ErrCodes()) {
		t.Errorf("code enum = %v, want %v", got, model.ErrCodes())
	}
	if req := dig(t, doc, "components", "schemas", "APIError", "required").([]any); len(req) != 3 {
		t.Errorf("APIError required = %v", req)
	}

	// One envelope per topic: the core topics, and the plugin's own with its type.
	schemas := dig(t, doc, "components", "schemas").(map[string]any)
	for _, name := range []string{"core.plugins", "core.overview", "vms", "host", "aa.items"} {
		env := dig(t, schemas, "Envelope."+name)
		if c := dig(t, env, "properties", "topic", "const"); c != name {
			t.Errorf("Envelope.%s topic const = %v", name, c)
		}
	}
	if ref := dig(t, schemas, "Envelope.core.plugins", "properties", "data", "$ref"); ref != "#/components/schemas/PluginsData" {
		t.Errorf("core.plugins data = %v", ref)
	}
	if ref := dig(t, schemas, "Envelope.aa.items", "properties", "data", "$ref"); ref != "#/components/schemas/itemsData" {
		t.Errorf("aa.items data = %v", ref)
	}
	oneOf := dig(t, doc, "paths", "/api/v1/topics/{topic}", "get", "responses", "200", "content", "application/json", "schema", "oneOf").([]any)
	if len(oneOf) != len(schemas)-countNonEnvelopes(schemas) {
		t.Errorf("snapshot oneOf has %d entries for %d envelopes", len(oneOf), len(schemas)-countNonEnvelopes(schemas))
	}
	dig(t, doc, "paths", "/api/v1/topics/{topic}", "get", "responses", "304")
}

func countNonEnvelopes(schemas map[string]any) int {
	n := 0
	for k := range schemas {
		if len(k) < 9 || k[:9] != "Envelope." {
			n++
		}
	}
	return n
}

func TestOpenAPIDocumentsPluginActionsAndRoutes(t *testing.T) {
	ms, routes := docManifests()
	doc := openAPIDoc(t, ms, routes)

	restart := dig(t, doc, "paths", "/api/v1/plugins/aa/actions/restart", "post")
	if dig(t, restart, "x-dash-destructive") != true || dig(t, restart, "x-dash-confirm") != "typeName" || dig(t, restart, "x-dash-scope") != "actions:aa" {
		t.Errorf("restart = %v", restart)
	}
	if ref := dig(t, restart, "requestBody", "content", "application/json", "schema", "$ref"); ref != "#/components/schemas/restartIn" {
		t.Errorf("restart body = %v", ref)
	}
	if req := dig(t, doc, "components", "schemas", "restartIn", "required").([]any); len(req) != 2 {
		t.Errorf("restartIn required = %v (the dash tags must reach the document)", req)
	}
	silence := dig(t, doc, "paths", "/api/v1/plugins/aa/actions/silence.create", "post")
	if dig(t, silence, "operationId") != "aa_silence_create" || dig(t, silence, "x-dash-idempotent") != true {
		t.Errorf("silence = %v", silence)
	}
	if _, has := silence.(map[string]any)["requestBody"]; has {
		t.Error("an action without an input has a request body")
	}

	vm := dig(t, doc, "paths", "/api/v1/plugins/aa/vms/{vmid}", "get")
	params := dig(t, vm, "parameters").([]any)
	if len(params) != 2 || dig(t, params[0], "name") != "vmid" || dig(t, params[0], "in") != "path" || dig(t, params[1], "name") != "fields" {
		t.Errorf("parameters = %v", params)
	}
	dig(t, doc, "paths", "/api/v1/plugins/aa/", "get")

	// The generic plugin paths are not themselves operations.
	for p := range dig(t, doc, "paths").(map[string]any) {
		if p == "/api/v1/plugins/{id}" || p == "/api/v1/plugins/{id}/{rest...}" || p == "/api/v1/plugins/{id}/actions/{name}" {
			t.Errorf("the document lists the generic path %s", p)
		}
	}
}

func TestOpenAPIRejectsConflictingTopicTypes(t *testing.T) {
	a := plugintest.New("aa", plugintest.Typed("items", itemsData{}))
	b := plugintest.New("bb", plugintest.Typed("items", itemsData{}))
	b.M.Topics[0].Legacy, a.M.Topics[0].Legacy = "same", "same"
	b.M.Topics[0].Type = model.Series{}
	if _, err := OpenAPI([]plugin.Manifest{a.M, b.M}, nil); err == nil {
		t.Fatal("two payload types for one topic produced a document")
	}
}

func TestActionInputSchemaInPluginsList(t *testing.T) {
	e := newActionEnv(t, nil)
	var list []model.PluginInfo
	decodeJSON(t, req{path: "/api/v1/plugins"}.do(e.Handler()), &list)
	if len(list) != 1 || len(list[0].Actions) != 3 {
		t.Fatalf("list = %+v", list)
	}
	byName := map[string]model.ActionInfo{}
	for _, a := range list[0].Actions {
		byName[a.Name] = a
	}
	var schema struct {
		Type       string                    `json:"type"`
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(byName["restart"].InputSchema, &schema); err != nil {
		t.Fatalf("restart schema %s: %v", byName["restart"].InputSchema, err)
	}
	if schema.Type != "object" || !slices.Equal(schema.Required, []string{"name", "namespace"}) || len(schema.Properties["kind"]["enum"].([]any)) != 2 {
		t.Errorf("restart schema = %s", byName["restart"].InputSchema)
	}
	if string(byName["ping"].InputSchema) != "null" && len(byName["ping"].InputSchema) != 0 {
		t.Errorf("ping has no input but a schema: %s", byName["ping"].InputSchema)
	}
	// core.plugins keeps the rows small: the schema is a /api/v1/plugins extra.
	if ev, ok := e.hub.Latest("", model.TopicPlugins); ok && bytes.Contains(ev.Data, []byte(`"required"`)) {
		t.Errorf("core.plugins carries input schemas: %s", ev.Data)
	}
}
