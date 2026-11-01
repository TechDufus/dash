package config

import (
	"strings"
	"testing"
	"time"
)

// defaultLimits are the stream and request limits the hardening change (fix/api-hardening)
// shipped as constants; the defaults must stay equal to them.
var defaultLimits = Limits{
	StreamsPerUser: 16, Streams: 256, TailsPerUser: 4, Tails: 32,
	TailMaxDuration: 30 * time.Minute, SSEWriteTimeout: 10 * time.Second, BodyReadTimeout: 10 * time.Second,
	MaxHeaderBytes: 64 << 10,
}

func TestLimitDefaults(t *testing.T) {
	if got := Defaults().Limits; got != defaultLimits {
		t.Errorf("default limits = %+v, want %+v", got, defaultLimits)
	}
}

func TestLimitsFromFileAndEnv(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte("server:\n  limits:\n    streamsPerUser: 1\n    tailMaxDuration: 90s\n    maxHeaderBytes: 16384\n")}
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true", "DASH_SERVER_LIMITS_SSE_WRITE_TIMEOUT=2m", "DASH_SERVER_LIMITS_TAILS=40"},
		File:    file,
	})
	if err != nil {
		t.Fatal(err)
	}
	l := res.Config.Limits
	if l.StreamsPerUser != 1 || l.TailMaxDuration != 90*time.Second || l.MaxHeaderBytes != 16384 {
		t.Errorf("file values not applied: %+v", l)
	}
	if l.SSEWriteTimeout != 2*time.Minute || l.Tails != 40 {
		t.Errorf("env values not applied: %+v", l)
	}
	if l.Streams != 256 {
		t.Errorf("untouched limit = %d, want its default 256", l.Streams)
	}
	if p := res.Provenance["server.limits.tailMaxDuration"]; p.Source != SourceFile {
		t.Errorf("tailMaxDuration provenance = %+v", p)
	}
	if p := res.Provenance["server.limits.sseWriteTimeout"]; p.Source != SourceEnv || p.Ref != "DASH_SERVER_LIMITS_SSE_WRITE_TIMEOUT" {
		t.Errorf("sseWriteTimeout provenance = %+v", p)
	}
}

func TestInvalidLimitsNameTheirPath(t *testing.T) {
	tests := []struct {
		name, file string
		env        []string
		want       []string
	}{
		{"zero count", "server: {limits: {streams: 0}}", nil, []string{"server.limits.streams", "(file c.yaml)", "greater than zero"}},
		{"negative size", "server: {limits: {maxHeaderBytes: -1}}", nil, []string{"server.limits.maxHeaderBytes", "greater than zero"}},
		{"zero duration", "server: {limits: {bodyReadTimeout: 0s}}", nil, []string{"server.limits.bodyReadTimeout", "greater than zero"}},
		{"per user above total", "server: {limits: {tailsPerUser: 33}}", nil, []string{"server.limits.tailsPerUser", "server.limits.tails (32)"}},
		{"per user above lowered total", "", []string{"DASH_SERVER_LIMITS_STREAMS=8"}, []string{"server.limits.streamsPerUser", "server.limits.streams (8)"}},
		{"duration needs a unit", "server: {limits: {tailMaxDuration: 30}}", nil, []string{"server.limits.tailMaxDuration", "duration"}},
		{"duration is not a number in yaml", "server: {limits: {sseWriteTimeout: 5s5}}", nil, []string{"server.limits.sseWriteTimeout", "5s5"}},
		{"count is not text", "server: {limits: {tails: many}}", nil, []string{"server.limits.tails", "whole number"}},
		{"env duration", "", []string{"DASH_SERVER_LIMITS_TAIL_MAX_DURATION=soon"}, []string{"DASH_SERVER_LIMITS_TAIL_MAX_DURATION", "server.limits.tailMaxDuration", `"soon"`}},
		{"env count", "", []string{"DASH_SERVER_LIMITS_TAILS=1.5"}, []string{"DASH_SERVER_LIMITS_TAILS", "server.limits.tails"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Sources{
				Environ: append([]string{"DASH_DEMO=true"}, tc.env...),
				File:    &File{Path: "c.yaml", Data: []byte(tc.file)},
			})
			if err == nil {
				t.Fatal("resolved without error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestLimitsAreRestartSettingsInTheEnvTable(t *testing.T) {
	byPath := map[string]EnvVar{}
	for _, v := range EnvVars() {
		byPath[v.Path] = v
	}
	for path, env := range map[string]string{
		"server.limits.streamsPerUser":  "DASH_SERVER_LIMITS_STREAMS_PER_USER",
		"server.limits.streams":         "DASH_SERVER_LIMITS_STREAMS",
		"server.limits.tailsPerUser":    "DASH_SERVER_LIMITS_TAILS_PER_USER",
		"server.limits.tails":           "DASH_SERVER_LIMITS_TAILS",
		"server.limits.tailMaxDuration": "DASH_SERVER_LIMITS_TAIL_MAX_DURATION",
		"server.limits.sseWriteTimeout": "DASH_SERVER_LIMITS_SSE_WRITE_TIMEOUT",
		"server.limits.bodyReadTimeout": "DASH_SERVER_LIMITS_BODY_READ_TIMEOUT",
		"server.limits.maxHeaderBytes":  "DASH_SERVER_LIMITS_MAX_HEADER_BYTES",
	} {
		v, ok := byPath[path]
		if !ok || v.Name != env || !v.Restart {
			t.Errorf("%s: %+v, want env %s and restart", path, v, env)
		}
	}
	if byPath["server.limits.tailMaxDuration"].Type != "duration (e.g. 30s)" || byPath["server.limits.streams"].Type != "integer" {
		t.Errorf("types: %+v, %+v", byPath["server.limits.tailMaxDuration"], byPath["server.limits.streams"])
	}
}

func TestUIMayNotSetLimits(t *testing.T) {
	log, buf := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true"}, Log: log,
		UI: map[string]any{"server": map[string]any{"limits": map[string]any{"streams": 1000000}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Limits.Streams != 256 || !strings.Contains(buf.String(), "server.limits.streams") {
		t.Errorf("streams = %d, log %q: the UI layer must not reach server.limits", res.Config.Limits.Streams, buf)
	}
}
