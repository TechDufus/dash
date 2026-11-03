package plugintest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
)

func TestCheckEnvAppliesAuthorizeAndRecordsConfigs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	env := &CheckEnv{Clock: func() time.Time { return at }}
	c, err := env.HTTPClient(plugin.HTTPConfig{
		Timeout:   plugin.CheckTimeout,
		Authorize: func(r *http.Request) { r.Header.Set("Authorization", "Bearer t") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != plugin.CheckTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, plugin.CheckTimeout)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != "Bearer t" {
		t.Errorf("upstream saw Authorization %q, want %q", got, "Bearer t")
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("Authorize changed the caller's request instead of a copy")
	}
	if cfgs := env.Configs(); len(cfgs) != 1 || cfgs[0].Timeout != plugin.CheckTimeout {
		t.Errorf("Configs() = %+v, want the one config asked for", cfgs)
	}
	if !env.Now().Equal(at) {
		t.Errorf("Now() = %v, want the clock's %v", env.Now(), at)
	}

	plain, _ := (&CheckEnv{}).HTTPClient(plugin.HTTPConfig{})
	if plain.Timeout != plugin.DefaultHTTPTimeout {
		t.Errorf("zero Timeout = %v, want %v", plain.Timeout, plugin.DefaultHTTPTimeout)
	}
}
