package api

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// streamDocPlugin is a fake plugin with one GET route and one stream, the first manifest of the
// G3 migration that has a stream (victorialogs, P3.5) in miniature.
func streamDocPlugin(withStream bool) *plugintest.Fake {
	f := plugintest.New("aa")
	f.M.Name, f.M.Description = "Alpha", "the alpha plugin"
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/items", func(http.ResponseWriter, *http.Request) {}, plugin.RouteSpec{
			Summary: "List items", Scope: "read", Response: []string{},
			Query: []plugin.Param{{Name: "ns", Doc: "Namespace."}, {Name: "q", Doc: "Text to look for."}},
		})
		if !withStream {
			return
		}
		r.Stream("/tail", func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { return nil }, plugin.StreamSpec{
			Summary: "Tail the items", Scope: "logs",
			Query:  []plugin.Param{{Name: "ns", Doc: "Namespace of the pod.", Required: true}, {Name: "pod", Doc: "Pod to tail.", Required: true}},
			Target: podTarget, Namespace: nsQuery,
		})
	}
	return f
}

func streamDoc(t *testing.T, withStream bool) (doc []byte, parsed map[string]any) {
	t.Helper()
	f := streamDocPlugin(withStream)
	doc, err := OpenAPI([]plugin.Manifest{f.M}, func(id plugin.ID) []plugin.RouteInfo {
		if id == "aa" {
			return plugin.CollectRoutes(f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc, openAPIDoc(t, []plugin.Manifest{f.M}, func(plugin.ID) []plugin.RouteInfo { return plugin.CollectRoutes(f) })
}

func TestOpenAPIDocumentsAPluginStreamAsAnSSEOperation(t *testing.T) {
	_, doc := streamDoc(t, true)

	tail := dig(t, doc, "paths", "/api/v1/plugins/aa/tail", "get")
	if dig(t, tail, "x-dash-scope") != "logs" || dig(t, tail, "x-dash-min-role") != "viewer" || dig(t, tail, "summary") != "Tail the items" {
		t.Errorf("tail = %v", tail)
	}
	ok := dig(t, tail, "responses", "200")
	content := dig(t, ok, "content").(map[string]any)
	if len(content) != 1 {
		t.Errorf("a stream's 200 has content %v, want only text/event-stream", keysOf(content))
	}
	dig(t, content, "text/event-stream", "schema")
	if desc := dig(t, ok, "description").(string); !strings.Contains(desc, "server-sent events") || !strings.Contains(desc, "429") {
		t.Errorf("200 description = %q, want the SSE documentation with the refusals", desc)
	}
	if ref := dig(t, tail, "responses", "default", "$ref"); ref != "#/components/responses/Error" {
		t.Errorf("default response = %v, want the Error envelope", ref)
	}
	var names []string
	for _, p := range dig(t, tail, "parameters").([]any) {
		names = append(names, dig(t, p, "name").(string)+":"+dig(t, p, "in").(string))
	}
	if !slices.Equal(names, []string{"ns:query", "pod:query"}) {
		t.Errorf("parameters = %v, want the stream's query parameters", names)
	}

	// The plain route next to it is an ordinary JSON operation.
	items := dig(t, doc, "paths", "/api/v1/plugins/aa/items", "get")
	if _, sse := dig(t, items, "responses", "200", "content").(map[string]any)["text/event-stream"]; sse {
		t.Error("a GET route is documented as a stream")
	}
	dig(t, items, "responses", "200", "content", "application/json")
	if dig(t, items, "x-dash-scope") != "read" {
		t.Errorf("items scope = %v", dig(t, items, "x-dash-scope"))
	}
}

var vacuumSummary = regexp.MustCompile(`(?:Passed|Failed) with (\d+) errors?, (\d+) warnings? and (\d+) informs?`)

// vacuumLint runs the lint of `make check`, from the repository root, on doc, and returns its
// exit error and the counts of its summary line.
func vacuumLint(t *testing.T, root string, doc []byte) (err error, out string, errs, warns, infos int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "openapi.json")
	if werr := os.WriteFile(file, doc, 0o600); werr != nil {
		t.Fatal(werr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "-modfile=tools/go.mod", "vacuum", "lint",
		"-r", "docs/api/ruleset.yaml", "-n", "warn", "-q", "-b", "--no-update-check", file)
	cmd.Dir = root
	b, err := cmd.CombinedOutput()
	out = string(b)
	m := vacuumSummary.FindStringSubmatch(out)
	if m == nil {
		return err, out, -1, -1, -1
	}
	errs, _ = strconv.Atoi(m[1])
	warns, _ = strconv.Atoi(m[2])
	infos, _ = strconv.Atoi(m[3])
	return err, out, errs, warns, infos
}

// A plugin stream passes the lint `make check` applies to docs/api/openapi.json: the same
// invocation (the ruleset of docs/api, warnings fail), with the document of a fake plugin that
// registers a GET and a stream. vacuum also reports "informs" (types the reflector leaves open,
// a response described by its own summary), which `make check` accepts and every GET route adds
// one of: the stream must add none to the count of the same document without it.
func TestOpenAPIWithAPluginStreamPassesVacuum(t *testing.T) {
	if testing.Short() {
		t.Skip("runs vacuum from the module cache")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "tools", "go.mod")); err != nil {
		t.Fatalf("repository root %s: %v", root, err)
	}

	without, _ := streamDoc(t, false)
	lintErr, out, errs, warns, baseInfos := vacuumLint(t, root, without)
	if lintErr != nil || errs != 0 || warns != 0 {
		t.Fatalf("vacuum on the document with a plugin that has a GET route: %v, %d errors, %d warnings:\n%s", lintErr, errs, warns, out)
	}

	with, _ := streamDoc(t, true)
	lintErr, out, errs, warns, infos := vacuumLint(t, root, with)
	if lintErr != nil || errs != 0 || warns != 0 || infos != baseInfos {
		t.Errorf("vacuum on the document with a plugin stream: %v, %d errors, %d warnings, %d informs (without the stream: %d):\n%s",
			lintErr, errs, warns, infos, baseInfos, out)
	}
}
