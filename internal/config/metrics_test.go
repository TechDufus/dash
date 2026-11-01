package config

import (
	"strings"
	"testing"
)

func TestMetricsAreOffByDefault(t *testing.T) {
	if Defaults().Metrics {
		t.Error("server.metrics defaults to on")
	}
}

func TestMetricsFromFileAndEnv(t *testing.T) {
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true"},
		File:    &File{Path: "c.yaml", Data: []byte("server:\n  metrics: true\n")},
	})
	if err != nil || !res.Config.Metrics {
		t.Fatalf("file: metrics = %v, err %v", res.Config.Metrics, err)
	}
	res, err = Resolve(Sources{
		Environ: []string{"DASH_DEMO=true", "DASH_SERVER_METRICS=false"},
		File:    &File{Path: "c.yaml", Data: []byte("server:\n  metrics: true\n")},
	})
	if err != nil || res.Config.Metrics {
		t.Fatalf("the environment overrides the file: metrics = %v, err %v", res.Config.Metrics, err)
	}
	res, err = Resolve(Sources{Environ: []string{"DASH_DEMO=true", "DASH_SERVER_METRICS=true"}})
	if err != nil || !res.Config.Metrics {
		t.Fatalf("env: metrics = %v, err %v", res.Config.Metrics, err)
	}
}

func TestMetricsMustBeABoolean(t *testing.T) {
	_, err := Resolve(Sources{Environ: []string{"DASH_DEMO=true", "DASH_SERVER_METRICS=maybe"}})
	if err == nil || !strings.Contains(err.Error(), "DASH_SERVER_METRICS") {
		t.Errorf("err = %v, want one naming DASH_SERVER_METRICS", err)
	}
}

func TestMetricsIsARestartSettingInTheEnvTable(t *testing.T) {
	for _, v := range EnvVars() {
		if v.Path == "server.metrics" {
			if v.Name != "DASH_SERVER_METRICS" || !v.Restart || v.Type != "boolean" {
				t.Errorf("%+v, want DASH_SERVER_METRICS, restart-only, boolean", v)
			}
			return
		}
	}
	t.Error("server.metrics is not in the env table")
}

func TestUIMayNotSetMetrics(t *testing.T) {
	log, buf := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true"}, Log: log,
		UI: map[string]any{"server": map[string]any{"metrics": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Metrics || !strings.Contains(buf.String(), "server.metrics") {
		t.Errorf("metrics = %v, log %q: the UI layer must not switch on an unauthenticated endpoint", res.Config.Metrics, buf)
	}
}
