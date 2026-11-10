package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

var categoryRE = regexp.MustCompile(`^(unreachable|timeout|tls|auth|invalid_response|not_configured|http_[0-9]{3})$`)

// Category is exercised with the errors real clients produce, not hand-built ones.
func TestCategory(t *testing.T) {
	status := func(code int) *httptest.Server {
		return serve(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", code) })
	}
	closedURL := refusedURL(t) // a bound port that never listens: connection refused, and nobody else can get it

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "{}") }))
	t.Cleanup(tlsSrv.Close)

	notJSON := serve(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html>login</html>") })
	badStatus := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
	})
	expired, cancel := context.WithDeadline(ctx, time.Unix(1, 0))
	defer cancel()

	tests := []struct {
		name string
		err  func() error
		want string
	}{
		{"nil", func() error { return nil }, ""},
		{"unconfigured client", func() error { _, err := NewProm("", nil).Instant(ctx, "x"); return err }, "not_configured"},
		{"401", func() error { _, err := NewProm(status(401).URL, nil).Instant(ctx, "x"); return err }, "auth"},
		{"403", func() error { _, err := NewProm(status(403).URL, nil).Instant(ctx, "x"); return err }, "auth"},
		{"503", func() error { _, err := NewProm(status(503).URL, nil).Instant(ctx, "x"); return err }, "http_503"},
		{"404", func() error { _, err := NewProm(status(404).URL, nil).Instant(ctx, "x"); return err }, "http_404"},
		{"connection refused", func() error { _, err := NewProm(closedURL, nil).Instant(ctx, "x"); return err }, "unreachable"},
		{"untrusted certificate", func() error { _, err := NewProm(tlsSrv.URL, nil).Instant(ctx, "x"); return err }, "tls"},
		{"deadline", func() error { _, err := NewProm(notJSON.URL, nil).Instant(expired, "x"); return err }, "timeout"},
		{"body is not JSON", func() error { _, err := NewProm(notJSON.URL, nil).Instant(ctx, "x"); return err }, "invalid_response"},
		{"query error status", func() error { _, err := NewProm(badStatus.URL, nil).Instant(ctx, "x"); return err }, "invalid_response"},
		{"proxmox data that does not decode", func() error {
			s := serve(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"data":"x"}`) })
			_, err := NewProxmox(s.URL, "u@pve!t", secret.New("s", ""), nil).VMs(ctx)
			return err
		}, "invalid_response"},
		{"wrapped", func() error { return fmt.Errorf("poll: %w", &StatusError{Code: 502}) }, "http_502"},
		{"anything else", func() error { return errors.New("boom") }, "unreachable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err()
			got := Category(err)
			if got != tc.want {
				t.Fatalf("Category = %q, want %q (error: %v)", got, tc.want, err)
			}
			if got != "" && !categoryRE.MatchString(got) {
				t.Fatalf("category %q is not one of the documented values", got)
			}
		})
	}
}
