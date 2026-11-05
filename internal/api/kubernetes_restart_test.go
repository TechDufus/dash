package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/secret"
)

// factoryFunc registers a plugin.Factory with a pluginEnv.
type factoryFunc plugin.Factory

func (f factoryFunc) Factory() plugin.Factory { return plugin.Factory(f) }

const (
	restartPath     = "/api/v1/plugins/kubernetes/actions/restart"
	protectedLabel  = "dash.example.com/protected"
	deniedNamespace = "kube-system"
)

// kubeEnv is the real kubernetes plugin over a fake clientset, behind the core's action chain: the
// supervisor, the audit log, the metrics and the redaction a deployment has.
type kubeEnv struct {
	*pluginEnv
	client *kubefake.Clientset
	logs   *syncBuf
	reg    *secret.Registry
}

func deployment(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func namespaceOf(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// newKubeEnv starts the plugin with its actions enabled and kube-system denied; objs are what the
// cluster holds. The canary secret is registered, so the plugin's scrubbing and the log redact it.
func newKubeEnv(t *testing.T, objs ...runtime.Object) *kubeEnv {
	t.Helper()
	return newKubeEnvWith(t, nil, objs...)
}

// newKubeEnvWith is newKubeEnv with prep run on the fake client before the plugin starts: the fake's
// reactor chain is not safe to change while the plugin's informers read it.
func newKubeEnvWith(t *testing.T, prep func(*kubefake.Clientset), objs ...runtime.Object) *kubeEnv {
	t.Helper()
	e := &kubeEnv{client: kubefake.NewClientset(objs...), logs: &syncBuf{}, reg: canaryRegistry()}
	if prep != nil {
		prep(e.client)
	}
	cfg := pluginconf.DefaultKubernetes()
	cfg.Conventions.ProtectedLabel = protectedLabel
	connect := func(pluginconf.Kubernetes) (*kubernetes.Connection, error) { return connectionWith(e.client), nil }
	e.pluginEnv = newPluginEnvOpt(t, func(o *plugin.Options) {
		o.ScrubLog = e.reg.Scrub
		o.Log = slog.New(secret.NewHandler(slog.NewTextHandler(e.logs, nil), e.reg))
	}, []plugin.InstanceConfig{{
		Name: "kubernetes", Type: "kubernetes", Config: &cfg, ActionsEnabled: true, DenyNamespaces: []string{deniedNamespace},
	}}, factoryFunc(kubernetes.FactoryWithConnect(nil, connect)))
	e.Server.d.Metrics = obs.New("test", RoutePatterns(), e.sup.Infos)
	e.eventually(t, "the restart action to be mounted", func() bool {
		_, ok := e.sup.Action("kubernetes", "restart")
		return ok
	})
	return e
}

func (e *kubeEnv) restart(in model.RestartRequest) *httptest.ResponseRecorder {
	return req{method: http.MethodPost, path: restartPath, body: in}.do(e.Handler())
}

// patches are the patch calls the plugin made on workloads.
func (e *kubeEnv) patches() []k8stesting.PatchAction {
	var out []k8stesting.PatchAction
	for _, a := range e.client.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok {
			out = append(out, p)
		}
	}
	return out
}

func (e *kubeEnv) records(t *testing.T, kind audit.Kind) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, r := range auditRecords(t, e.dir) {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func rolloutOf(ns, name string) model.RestartRequest {
	return model.RestartRequest{Kind: model.WorkloadKindDeployment, Namespace: ns, Name: name}
}

// The restart of the real kubernetes plugin, through the core's chain (role, rate limit, input,
// deny list, audit intent, Act), as the tests of the fixture plugin check it for the framework.
func TestKubernetesRestartRunsTheCoreChain(t *testing.T) {
	t.Run("an admin restarts, audited and counted", func(t *testing.T) {
		e := newKubeEnv(t, namespaceOf("media", nil), deployment("media", "app"))
		if w := e.restart(rolloutOf("media", "app")); w.Code != http.StatusAccepted {
			t.Fatalf("restart = %d %s", w.Code, w.Body)
		}
		ps := e.patches()
		if len(ps) != 1 || ps[0].GetResource().Resource != "deployments" || ps[0].GetNamespace() != "media" || ps[0].GetName() != "app" {
			t.Fatalf("patches = %+v", ps)
		}
		if !strings.Contains(string(ps[0].GetPatch()), "kubectl.kubernetes.io/restartedAt") {
			t.Errorf("patch = %s", ps[0].GetPatch())
		}

		recs := auditRecords(t, e.dir)
		if len(recs) != 2 || recs[0].Kind != audit.KindIntent || recs[1].Kind != audit.KindOutcome || recs[1].Result != audit.ResultOK {
			t.Fatalf("records = %+v, want intent then an ok outcome", recs)
		}
		for _, r := range recs {
			if r.Plugin != "kubernetes" || r.Action != "restart" || r.Actor.User != "alice" ||
				r.Target != (audit.Target{Kind: "Deployment", Namespace: "media", Name: "app"}) {
				t.Errorf("record = %+v", r)
			}
		}
		// dash_actions_total carries the plugin and the action under their own labels.
		ss := scrapeMetrics(t, e.Server)
		if v, ok := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "kubernetes", "action": "restart", "outcome": obs.ActionOK}); !ok || v != 1 {
			t.Errorf(`dash_actions_total{kubernetes,restart,ok} = %v (present %v), want 1`, v, ok)
		}
	})

	t.Run("a viewer is refused before anything runs", func(t *testing.T) {
		e := newKubeEnv(t, namespaceOf("media", nil), deployment("media", "app"))
		// No role of the chain is a viewer until P4.2 maps roles, so the principal is injected as
		// auth would have stored it (as meAs does for the me handler).
		body, _ := json.Marshal(rolloutOf("media", "app"))
		r := httptest.NewRequest(http.MethodPost, restartPath, strings.NewReader(string(body)))
		r.SetPathValue("id", "kubernetes")
		r.SetPathValue("name", "restart")
		p := auth.Principal{Kind: auth.KindProxy, User: "vera", Role: auth.RoleViewer}
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p))
		w := httptest.NewRecorder()
		e.Server.runAction(w, r)

		wantError(t, w, http.StatusForbidden, model.ErrCodeForbidden)
		if n := len(e.patches()); n != 0 {
			t.Errorf("%d patches for a viewer", n)
		}
		recs := auditRecords(t, e.dir)
		if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "forbidden" || recs[0].Actor.User != "vera" {
			t.Errorf("records = %+v, want one forbidden denial of vera", recs)
		}
	})

	t.Run("the eleventh call in a minute is 429", func(t *testing.T) {
		e := newKubeEnv(t, namespaceOf("media", nil), deployment("media", "app"))
		for i := 1; i <= actionRateLimit; i++ {
			if w := e.restart(rolloutOf("media", "app")); w.Code != http.StatusAccepted {
				t.Fatalf("restart %d = %d %s", i, w.Code, w.Body)
			}
		}
		w := e.restart(rolloutOf("media", "app"))
		wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
		if w.Header().Get("Retry-After") == "" {
			t.Error("no Retry-After")
		}
		if n := len(e.patches()); n != actionRateLimit {
			t.Errorf("%d patches, want %d", n, actionRateLimit)
		}
		if d := e.records(t, audit.KindDenial); len(d) != 1 || d[0].Reason != "rate_limit" {
			t.Errorf("denials = %+v, want one rate_limit", d)
		}
	})

	t.Run("a denied namespace is 403 and audited, and the cluster is never read", func(t *testing.T) {
		e := newKubeEnv(t, namespaceOf(deniedNamespace, nil), deployment(deniedNamespace, "coredns"))
		wantError(t, e.restart(rolloutOf(deniedNamespace, "coredns")), http.StatusForbidden, model.ErrCodeForbidden)
		if n := len(e.patches()); n != 0 {
			t.Errorf("%d patches in a denied namespace", n)
		}
		for _, a := range e.client.Actions() {
			if a.GetVerb() == "get" && a.GetResource().Resource == "namespaces" {
				t.Errorf("the namespace was read after the refusal: %+v", a)
			}
		}
		recs := auditRecords(t, e.dir)
		if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "namespace_denied" {
			t.Fatalf("records = %+v, want one namespace_denied denial", recs)
		}
	})

	t.Run("a protected namespace needs the typed name", func(t *testing.T) {
		e := newKubeEnv(t, namespaceOf("prod", map[string]string{protectedLabel: "true"}), deployment("prod", "api"))
		wantError(t, e.restart(rolloutOf("prod", "api")), http.StatusBadRequest, model.ErrCodeInvalidInput)
		if n := len(e.patches()); n != 0 {
			t.Errorf("%d patches without the typed name", n)
		}
		in := rolloutOf("prod", "api")
		in.Confirm = "api"
		if w := e.restart(in); w.Code != http.StatusAccepted {
			t.Errorf("with the name typed = %d %s", w.Code, w.Body)
		}
		var results []string
		for _, r := range e.records(t, audit.KindOutcome) {
			results = append(results, r.Result)
		}
		if strings.Join(results, ",") != audit.ResultError+","+audit.ResultOK {
			t.Errorf("outcomes = %v, want error then ok", results)
		}
	})
}

// The four codes whose message reaches the user are the cluster's own policy; a credential an
// admission webhook echoes in one is scrubbed on the way out, from the body, the audit log and the
// log, through the real plugin (the plugin's own scrubbing, with no core fallback to hide it).
func TestKubernetesAdmissionMessageIsScrubbed(t *testing.T) {
	const pat = "dash_pat_abcdefghij0123456789ABCDEFGHIJ0123456789abc"
	msg := "admission policy: denied; webhook echoed " + strings.Join(canaryForms(), " ") + " and " + pat
	for _, tc := range []struct {
		code    int32
		want    string
		outcome string
	}{
		{http.StatusForbidden, model.ErrCodeForbidden, obs.ActionDenied},
		{http.StatusUnprocessableEntity, model.ErrCodeInvalidInput, obs.ActionInvalid},
	} {
		t.Run(http.StatusText(int(tc.code)), func(t *testing.T) {
			e := newKubeEnvWith(t, func(c *kubefake.Clientset) {
				c.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: tc.code, Message: msg}}
				})
			}, namespaceOf("media", nil), deployment("media", "app"))
			w := e.restart(rolloutOf("media", "app"))
			wantError(t, w, int(tc.code), tc.want)
			if !strings.Contains(w.Body.String(), "admission policy: denied") {
				t.Errorf("the policy message must still reach the user: %s", w.Body)
			}
			everywhere := w.Body.String() + readAuditFile(t, e.dir) + e.logs.String()
			for _, leak := range append(canaryForms(), pat) {
				if strings.Contains(everywhere, leak) {
					t.Errorf("%q leaked:\n%s", leak, everywhere)
				}
			}
			ss := scrapeMetrics(t, e.Server)
			if v, _ := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "kubernetes", "action": "restart", "outcome": tc.outcome}); v != 1 {
				t.Errorf(`dash_actions_total{kubernetes,restart,%s} = %v, want 1`, tc.outcome, v)
			}
		})
	}
}

// The credential an upstream echoes back in an error page reaches neither the topics a viewer
// reads nor the log: the kubernetes plugin polls a Prometheus that answers 500 with the
// Authorization header it was sent, through the real prometheus plugin (the P1.4 canary, as it was
// before the polls moved into the plugin).
func TestPrometheusCredentialEchoIsNeverPublishedOrLogged(t *testing.T) {
	const credential = "CANARYSECRET123456"
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error for "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(prom.Close)

	promCfg := pluginconf.DefaultPrometheus()
	promCfg.URL = prom.URL
	promCfg.Auth.BearerToken = secret.New(credential, "")
	kubeCfg := pluginconf.DefaultKubernetes()

	reg := secret.NewRegistry()
	reg.Add(promCfg.Auth.BearerToken)
	logs := &syncBuf{}
	e := newPluginEnvOpt(t, func(o *plugin.Options) {
		o.ScrubLog = reg.Scrub
		// A plain handler: only the plugin's own ScrubLog keeps the credential out of the log.
		o.Log = slog.New(slog.NewTextHandler(logs, nil))
	}, []plugin.InstanceConfig{
		{Name: "prometheus", Type: "prometheus", Config: &promCfg},
		{Name: "kubernetes", Type: "kubernetes", Config: &kubeCfg},
	}, factoryFunc(prometheus.New), factoryFunc(kubernetes.FactoryWithConnect(nil, fakeConnection)))

	// The usage poll publishes kubernetes.cluster with the category of its failure.
	var topic string
	e.eventually(t, "kubernetes.cluster to name the failed poll", func() bool {
		ev, ok := e.hub.Latest("", model.TopicCluster)
		if !ok {
			return false
		}
		topic = string(ev.Data)
		return strings.Contains(topic, `"http_500"`)
	})
	e.eventually(t, "the failure to be logged", func() bool { return strings.Contains(logs.String(), "upstream source failed") })

	out := logs.String()
	if strings.Contains(out, credential) || strings.Contains(topic, credential) {
		t.Errorf("the credential leaked:\nlog: %s\ntopic: %s", out, topic)
	}
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "category=http_500") {
		t.Errorf("want the scrubbed detail and the category in the log:\n%s", out)
	}
	for _, leak := range []string{prom.URL, "internal error"} {
		if strings.Contains(topic, leak) {
			t.Errorf("the topic carries %q: %s", leak, topic)
		}
	}
}

// readAuditFile is the audit log as written, for a search of every byte of it.
func readAuditFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}
