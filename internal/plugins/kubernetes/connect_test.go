package kubernetes

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// kubeconfigFile writes a kubeconfig with the contexts a (192.0.2.10) and b (192.0.2.11).
func kubeconfigFile(t *testing.T) string {
	t.Helper()
	body := `apiVersion: v1
kind: Config
current-context: a
clusters:
- {name: ca, cluster: {server: "https://192.0.2.10:6443"}}
- {name: cb, cluster: {server: "https://192.0.2.11:6443"}}
users:
- {name: u, user: {token: "x"}}
contexts:
- {name: a, context: {cluster: ca, user: u}}
- {name: b, context: {cluster: cb, user: u}}
`
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noClusterEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("KUBECONFIG", "")
}

// auto: the kubeconfig when kubeconfig or KUBECONFIG is set, else in-cluster, else none.
func TestModeAutoPicksTheKubeconfigThenInClusterThenNone(t *testing.T) {
	noClusterEnv(t)
	path := kubeconfigFile(t)

	cfg := pluginconf.DefaultKubernetes()
	if _, err := connectCluster(cfg); !errors.Is(err, kube.ErrNoCluster) {
		t.Fatalf("nothing configured: %v, want ErrNoCluster", err)
	}

	cfg.Kubeconfig = path
	conn, err := connectCluster(cfg)
	if err != nil || conn.REST.Host != "https://192.0.2.10:6443" {
		t.Fatalf("kubeconfig setting: %v, %+v", err, conn)
	}

	cfg.Kubeconfig = ""
	t.Setenv("KUBECONFIG", path)
	conn, err = connectCluster(cfg)
	if err != nil || conn.REST.Host != "https://192.0.2.10:6443" {
		t.Fatalf("KUBECONFIG: %v, %+v", err, conn)
	}
	// The setting wins over the variable.
	other := filepath.Join(t.TempDir(), "missing")
	cfg.Kubeconfig = other
	if _, err := connectCluster(cfg); err == nil || errors.Is(err, kube.ErrNoCluster) {
		t.Errorf("a kubeconfig that cannot be read: %v, want an error that is not ErrNoCluster", err)
	}
}

func TestKubeconfigIsLoaded(t *testing.T) {
	noClusterEnv(t)
	cfg := pluginconf.DefaultKubernetes()
	cfg.Mode, cfg.Kubeconfig = pluginconf.KubernetesModeKubeconfig, kubeconfigFile(t)
	conn, err := connectCluster(cfg)
	if err != nil || conn.Cluster == nil || conn.REST.Host != "https://192.0.2.10:6443" {
		t.Fatalf("connect: %v, %+v", err, conn)
	}
}

func TestContextSelectsTheKubeconfigContext(t *testing.T) {
	noClusterEnv(t)
	cfg := pluginconf.DefaultKubernetes()
	cfg.Kubeconfig, cfg.Context = kubeconfigFile(t), "b"
	conn, err := connectCluster(cfg)
	if err != nil || conn.REST.Host != "https://192.0.2.11:6443" {
		t.Fatalf("context b: %v, %+v", err, conn)
	}
	cfg.Context = "nope"
	if _, err := connectCluster(cfg); err == nil {
		t.Error("an unknown context connected")
	}
}

func TestQPSAndBurstReachTheClient(t *testing.T) {
	noClusterEnv(t)
	cfg := pluginconf.DefaultKubernetes()
	cfg.Kubeconfig = kubeconfigFile(t)
	conn, err := connectCluster(cfg)
	if err != nil || conn.REST.QPS != 20 || conn.REST.Burst != 40 {
		t.Fatalf("defaults: %v, qps %v burst %v", err, conn.REST.QPS, conn.REST.Burst)
	}
	cfg.QPS, cfg.Burst = 5, 9
	conn, err = connectCluster(cfg)
	if err != nil || conn.REST.QPS != 5 || conn.REST.Burst != 9 {
		t.Fatalf("set: %v, qps %v burst %v", err, conn.REST.QPS, conn.REST.Burst)
	}
}

// An explicit mode that cannot run is a row in error, never a fatal Start: the process serves
// everything else.
func TestExplicitModesWithoutTheirSourceFail(t *testing.T) {
	noClusterEnv(t)
	for _, mode := range []string{pluginconf.KubernetesModeInCluster, pluginconf.KubernetesModeKubeconfig} {
		cfg := pluginconf.DefaultKubernetes()
		cfg.Mode = mode
		_, err := connectCluster(cfg)
		if err == nil || errors.Is(err, kube.ErrNoCluster) {
			t.Errorf("mode %s without its source: %v", mode, err)
		}
	}
}

// mode inCluster ignores KUBECONFIG: it is the in-cluster service account or nothing.
func TestInClusterModeIgnoresTheKubeconfigVariable(t *testing.T) {
	noClusterEnv(t)
	t.Setenv("KUBECONFIG", kubeconfigFile(t))
	cfg := pluginconf.DefaultKubernetes()
	cfg.Mode = pluginconf.KubernetesModeInCluster
	if _, err := connectCluster(cfg); err == nil {
		t.Error("inCluster mode read KUBECONFIG")
	}
}

// Start turns a Connect failure into the row, with the detail in the log and none in the row.
func TestStartTurnsConnectFailuresIntoTheRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state plugin.State
	}{
		{"no cluster", kube.ErrNoCluster, plugin.StateUnavailable},
		{"explicit mode", errors.New("boom /secret/path"), plugin.StateError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := FactoryWithConnect(nil, func(pluginconf.Kubernetes) (*Connection, error) { return nil, tc.err })().(*Plugin)
			cfg := pluginconf.DefaultKubernetes()
			h := &connHost{}
			if err := p.Start(context.Background(), h, &cfg); err != nil {
				t.Fatalf("Start: %v", err)
			}
			st := p.Status()
			if st.State != tc.state || strings.Contains(st.Message, "/secret") {
				t.Errorf("status = %+v", st)
			}
			if p.Service() != nil {
				t.Error("a plugin without a cluster exports a service")
			}
			if err := p.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	}
}

// connHost is a plugin.Host that does nothing, for a Start that never gets as far as a goroutine.
type connHost struct{}

func (connHost) Publish(string, any, []model.SourceStatus)          {}
func (connHost) Logger() *slog.Logger                               { return slog.New(slog.DiscardHandler) }
func (connHost) HTTPClient(plugin.HTTPConfig) (*http.Client, error) { return http.DefaultClient, nil }
func (connHost) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("connHost dials nothing")
}
func (connHost) ScrubLog(s string) string { return s }
func (connHost) CutLog(s string, n int) (string, bool) {
	return (*secret.Registry)(nil).CutRunes(s, n)
}
func (connHost) Links() plugin.LinkResolver    { return plugin.NoLinks }
func (connHost) IconBaseURL() string           { return "" }
func (connHost) Service(plugin.ID) (any, bool) { return nil, false }
func (connHost) Now() time.Time                { return time.Now() }
func (connHost) Go(_ string, fn func())        { go fn() }
