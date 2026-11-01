package victorialogs

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/upstream"
)

// In demo mode the twin runs with no configuration at all, healthy, and its routes answer from
// the fake world with the same validation as the real ones.
func TestDemoTwinServesTheWorldWithNoConfiguration(t *testing.T) {
	h := hub.New()
	set := &plugintest.Settings{} // nothing configured: no URL, no section
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.DiscardHandler), Demo: true,
	})
	if err := sup.Add(New); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })

	deadline := time.Now().Add(3 * time.Second)
	for !sup.Running("victorialogs") {
		if time.Now().After(deadline) {
			t.Fatal("the twin did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rt, ok := sup.Router("victorialogs", plugin.RouteGuard{})
	if !ok {
		t.Fatal("no routes")
	}

	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/victorialogs/logs?ns=media&pod=plex-0&container=plex", nil))
	var lines []model.LogLine
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lines) != nil || len(lines) != 60 {
		t.Fatalf("GET logs = %d, %d lines: %.200s", w.Code, len(lines), w.Body)
	}
	if lines[0].Pod != "plex-0" || lines[0].Container != "plex" || !lines[0].Time.Before(lines[59].Time) {
		t.Errorf("lines = %+v ... %+v", lines[0], lines[59])
	}

	// A text filter narrows the demo too.
	w = httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/victorialogs/logs?ns=media&q=slow+upstream", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "slow upstream") || strings.Contains(w.Body.String(), "request served") {
		t.Errorf("filtered = %d %.200s", w.Code, w.Body)
	}

	// The same validation as the real plugin: a bad namespace is an input error.
	if _, err := (demoSource{fields: upstream.DefaultLogFields(), now: time.Now}).Query(ctx, upstream.LogQuery{Namespace: "Bad"}); err == nil {
		t.Error("the demo accepted an invalid namespace")
	}
}

func TestDemoTailEmitsLinesUntilTheContextEnds(t *testing.T) {
	d := demoSource{fields: upstream.DefaultLogFields(), now: func() time.Time { return testNow }, every: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan model.LogLine, 1)
	done := make(chan error, 1)
	go func() {
		done <- d.Tail(ctx, upstream.LogQuery{Namespace: "media", Pod: "plex-0"}, func(l model.LogLine) error {
			select {
			case got <- l:
			default:
			}
			return nil
		})
	}()
	select {
	case l := <-got:
		if l.Pod != "plex-0" || !l.Time.Equal(testNow) {
			t.Errorf("line = %+v", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no demo line arrived")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Tail after cancel = %v, want nil", err)
	}
}
