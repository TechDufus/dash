package config

import (
	"slices"
	"strings"
	"testing"
)

// server.corsOrigins takes origins only, normalized to what a browser sends; "*", a path or
// another scheme is a load error that names the entry, never its text.
func TestCORSOriginsAreOrigins(t *testing.T) {
	c := mustLoad(t, map[string]string{"DASH_DEV": "true", "DASH_SERVER_CORS_ORIGINS": `[" HTTPS://Inspector.Example.com/ ", "", "http://127.0.0.1:6274", "http://[::1]:8080"]`})
	if want := []string{"https://inspector.example.com", "http://127.0.0.1:6274", "http://[::1]:8080"}; !slices.Equal(c.CORSOrigins, want) {
		t.Errorf("CORSOrigins = %q, want %q", c.CORSOrigins, want)
	}
	for _, bad := range []string{`["*"]`, `["https://a.example.com/path"]`, `["ftp://a.example.com"]`, `["a.example.com"]`, `["https://user:pw@a.example.com"]`} {
		_, err := load(map[string]string{"DASH_DEV": "true", "DASH_SERVER_CORS_ORIGINS": bad})
		if err == nil || !strings.Contains(err.Error(), "server.corsOrigins") || !strings.Contains(err.Error(), "entry 1") || strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
