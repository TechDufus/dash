package plugin_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// hostGet sends one GET of url through a client the running fake's Host builds.
func hostGet(t *testing.T, f *plugintest.Fake, url string) error {
	t.Helper()
	c, err := f.Host().HTTPClient(plugin.HTTPConfig{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// A URL from the UI (InstanceConfig.FromUI) makes every client the instance builds refuse
// loopback; the same URL from the file or the environment dials. A change of FromUI restarts the
// instance, so its next client follows it either way.
func TestFromUIURLRefusesLoopbackAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	f := plugintest.New("aa")
	e := start(t, f.Factory())
	eventually(t, "aa to run", func() bool { return e.sup.Running("aa") })

	if err := hostGet(t, f, srv.URL); err != nil {
		t.Fatalf("a loopback URL from the file or env: %v", err)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", FromUI: true})
	e.reloadUntil(t, "a restart with FromUI", func() bool { return f.Starts() == 2 && e.sup.Running("aa") })
	if err := hostGet(t, f, srv.URL); !errors.Is(err, plugin.ErrBlockedAddress) {
		t.Fatalf("a loopback URL from the UI: err = %v, want ErrBlockedAddress", err)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa"})
	e.reloadUntil(t, "a restart without FromUI", func() bool { return f.Starts() == 3 && e.sup.Running("aa") })
	if err := hostGet(t, f, srv.URL); err != nil {
		t.Fatalf("the URL back in the file or env: %v", err)
	}
}

// The link-local and metadata addresses are refused whatever the URL's source.
func TestMetadataAddressRefusedForEverySource(t *testing.T) {
	for _, fromUI := range []bool{false, true} {
		f := plugintest.New("aa")
		e := newEnv(t, nil, f.Factory())
		e.set.Set(plugin.InstanceConfig{Name: "aa", FromUI: fromUI})
		e.run(t)
		eventually(t, "aa to run", func() bool { return e.sup.Running("aa") })
		for _, u := range []string{"http://169.254.169.254/latest/meta-data", "http://[fd00:ec2::254]/"} {
			if err := hostGet(t, f, u); !errors.Is(err, plugin.ErrBlockedAddress) {
				t.Errorf("fromUI=%v: GET %s err = %v, want ErrBlockedAddress", fromUI, u, err)
			}
		}
	}
}

// hostDial opens one connection to address through the running fake's Host.Dial.
func hostDial(t *testing.T, f *plugintest.Fake, address string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := f.Host().Dial(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return c.Close()
}

// Host.Dial applies the HTTP client's guard to a raw connection: loopback is refused only for an
// instance whose address came from the UI, and link-local and the metadata address always. It
// dials nothing but TCP.
func TestDialFollowsTheGuard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	f := plugintest.New("aa")
	e := start(t, f.Factory())
	eventually(t, "aa to run", func() bool { return e.sup.Running("aa") })
	if err := hostDial(t, f, ln.Addr().String()); err != nil {
		t.Fatalf("a loopback address from the file or env: %v", err)
	}
	for _, a := range []string{"169.254.169.254:22", "[fe80::1]:22", "[fd00:ec2::254]:22"} {
		if err := hostDial(t, f, a); !errors.Is(err, plugin.ErrBlockedAddress) {
			t.Errorf("dial %s from the file or env: err = %v, want ErrBlockedAddress", a, err)
		}
	}
	if _, err := f.Host().Dial(context.Background(), "unix", "/run/x.sock"); err == nil {
		t.Error("the unix network was accepted")
	}

	e.set.Set(plugin.InstanceConfig{Name: "aa", FromUI: true})
	e.reloadUntil(t, "a restart with FromUI", func() bool { return f.Starts() == 2 && e.sup.Running("aa") })
	for _, a := range []string{ln.Addr().String(), "169.254.169.254:22", "[::1]:22", "0.0.0.0:22"} {
		if err := hostDial(t, f, a); !errors.Is(err, plugin.ErrBlockedAddress) {
			t.Errorf("dial %s from the UI: err = %v, want ErrBlockedAddress", a, err)
		}
	}
}
