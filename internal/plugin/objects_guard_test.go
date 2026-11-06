package plugin_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// panickyObjects is a plugin whose every Objects call panics.
type panickyObjects struct{ *plugintest.Fake }

func (panickyObjects) Search(string, []string, int) []model.SearchHit { panic("search boom") }
func (panickyObjects) Get(context.Context, string) (any, error)       { panic("get boom") }
func (panickyObjects) Context(context.Context, string, model.ContextOptions) (model.ContextPart, error) {
	panic("context boom")
}

// syncBuffer is a log sink the supervisor's goroutines may write to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// A plugin's Objects calls are guarded: a panic is logged at ERROR with its stack and answers an
// empty search or an error, and dash goes on.
func TestObjectsPanicsAreRecovered(t *testing.T) {
	logs := &syncBuffer{}
	f := plugintest.New("aa")
	e := newEnv(t, func(o *plugin.Options) { o.Log = slog.New(slog.NewJSONHandler(logs, nil)) },
		func() plugin.Plugin { return panickyObjects{f} }).run(t)
	var o plugin.Objects
	eventually(t, "the objects of aa", func() bool {
		var ok bool
		o, ok = e.sup.Objects("aa")
		return ok
	})
	if hits := o.Search("", nil, 10); hits == nil || len(hits) != 0 {
		t.Errorf("search after a panic = %v", hits)
	}
	if _, err := o.Get(context.Background(), "aa:Item/1"); err == nil {
		t.Error("get after a panic answered no error")
	}
	if _, err := o.Context(context.Background(), "aa:Item/1", model.ContextOptions{}); err == nil {
		t.Error("context after a panic answered no error")
	}
	for _, want := range []string{"panic in Search", "panic in Get", "panic in Context", "search boom", `"stack":"goroutine `} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %s:\n%s", want, logs.String())
		}
	}
}
