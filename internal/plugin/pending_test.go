package plugin_test

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/secret"
)

// pendingCfg is a plugin section with secrets in the three places real ones have them: a
// field, a nested section and an embedded struct.
type pendingCfg struct {
	embeddedAuth
	URL   string       `json:"url"`
	Token secret.Value `json:"token"`
	Basic struct {
		Password secret.Value `json:"password"`
	} `json:"basic"`
	Skipped secret.Value `json:"-"`
}

type embeddedAuth struct {
	Bearer secret.Value `json:"bearerToken"`
}

func pendingCfgOf() any { return &pendingCfg{} }

const (
	refToken  = "k8s:media/app-credentials#api-token"
	refBasic  = "k8s:media/app-credentials#password"
	refBearer = "k8s:vault-ns/bearer-holder#tok"

	canaryValue = "CANARY-k8s-secret-0123456789"
)

// pendingConsumer needs its token to be set.
func pendingConsumer() *plugintest.Fake {
	f := plugintest.New("aa", plugintest.Config(pendingCfgOf))
	f.ConfiguredFn = func(c any) bool { return c.(*pendingCfg).Token.IsSet() }
	f.Hint = "plugins.aa.token"
	return f
}

func pendingEnv(t *testing.T, refs *secret.K8sRefs, ic func(*plugin.InstanceConfig), logw *lockedBuffer) *env {
	t.Helper()
	e := newEnv(t, func(o *plugin.Options) {
		if logw != nil {
			o.Log = slog.New(slog.NewTextHandler(logw, nil))
		}
	}, pendingConsumer().Factory())
	token, err := refs.Handle(refToken)
	if err != nil {
		t.Fatal(err)
	}
	i := plugin.InstanceConfig{Name: "aa", Config: &pendingCfg{Token: token}}
	if ic != nil {
		ic(&i)
	}
	e.set.Set(i)
	return e
}

func TestPendingMessageIsGenericForViewers(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	e := pendingEnv(t, refs, func(i *plugin.InstanceConfig) {
		i.Enabled = new(true)
		c := i.Config.(*pendingCfg)
		c.Basic.Password, _ = refs.Handle(refBasic)
		c.Bearer, _ = refs.Handle(refBearer)
		c.Skipped, _ = refs.Handle("k8s:skipped/x#y")
	}, nil)
	refs.SetReason(refToken, secret.ReasonForbidden)
	e.run(t)

	st, msg := e.state(t, "aa")
	if st != model.PluginUnconfigured {
		t.Fatalf("state = %s, want unconfigured with enabled: true", st)
	}
	want := "waiting for a Kubernetes Secret (see plugins.aa.bearerToken)" +
		"; waiting for a Kubernetes Secret (see plugins.aa.token)" +
		"; waiting for a Kubernetes Secret (see plugins.aa.basic.password)"
	if msg != want {
		t.Errorf("message =\n%q\nwant\n%q", msg, want)
	}

	// The row goes to every viewer: no namespace, Secret name, key or reason in it, in what
	// the API serves or in what the hub got.
	b, _ := json.Marshal(e.sup.Infos())
	for _, ev := range e.hub.Published() {
		j, _ := json.Marshal(ev)
		b = append(b, j...)
	}
	for _, leak := range []string{"media", "app-credentials", "api-token", "vault-ns", "forbidden", "k8s:", "skipped"} {
		if strings.Contains(strings.ToLower(string(b)), leak) {
			t.Errorf("the published rows leak %q", leak)
		}
	}
}

// Enabled unset gives the disabled row (auto-enable), true the unconfigured one; both carry the
// pending note.
func TestDisabledWithEnabledUnsetAndUnconfiguredWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled *bool
		want    model.PluginState
	}{
		{"unset", nil, model.PluginDisabled},
		{"true", new(true), model.PluginUnconfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := pendingEnv(t, secret.NewK8sRefs(nil), func(i *plugin.InstanceConfig) { i.Enabled = tc.enabled }, nil).run(t)
			st, msg := e.state(t, "aa")
			if st != tc.want || msg != "waiting for a Kubernetes Secret (see plugins.aa.token)" {
				t.Errorf("state %s, message %q", st, msg)
			}
		})
	}
}

// The first fill runs one coalesced Reload and the consumer starts; a rotation changes nothing
// about its lifecycle (consumers reveal per request).
func TestK8sRefFillStartsTheConsumer(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	f := pendingConsumer()
	e := newEnv(t, nil, f.Factory())
	refs.OnFill(e.sup.Reload)
	token, _ := refs.Handle(refToken)
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &pendingCfg{Token: token}})
	e.run(t)

	if st, _ := e.state(t, "aa"); st != model.PluginUnconfigured || f.Starts() != 0 {
		t.Fatalf("before the fill: %s, %d starts", st, f.Starts())
	}
	refs.Fill(refToken, canaryValue)
	eventually(t, "the consumer to start", func() bool { st, _ := e.state(t, "aa"); return st != model.PluginUnconfigured && f.Starts() == 1 })

	refs.Fill(refToken, canaryValue+"-rotated")
	if f.Starts() != 1 || f.Stops() != 0 {
		t.Errorf("a rotation restarted the consumer: %d starts, %d stops", f.Starts(), f.Stops())
	}
	if got := f.Config().(*pendingCfg).Token.Reveal(); got != canaryValue+"-rotated" {
		t.Errorf("the running consumer must see the rotated value, got %d bytes", len(got))
	}
}

// A change of the reason alone reloads (OnChange) and the log says the new reason once; the
// row itself stays generic.
func TestPendingReasonChangeRefreshesTheRow(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	var logw lockedBuffer
	e := pendingEnv(t, refs, func(i *plugin.InstanceConfig) { i.Enabled = new(true) }, &logw)
	refs.OnChange(e.sup.Reload)
	e.run(t)

	_, msg := e.state(t, "aa")
	if !strings.Contains(logw.String(), "reason=kubernetes_unavailable") {
		t.Fatalf("the log must name the first reason:\n%s", logw.String())
	}
	for _, reason := range []string{secret.ReasonNotFound, secret.ReasonForbidden} {
		refs.SetReason(refToken, reason)
		eventually(t, "the log to say "+reason, func() bool { return strings.Contains(logw.String(), "reason="+reason) })
	}
	if n := strings.Count(logw.String(), "reason=forbidden"); n != 1 {
		t.Errorf("a reason is logged once per change, got %d times", n)
	}
	if !strings.Contains(logw.String(), "reference="+refToken) || !strings.Contains(logw.String(), "field=plugins.aa.token") {
		t.Errorf("the log carries the field and the reference:\n%s", logw.String())
	}
	if _, now := e.state(t, "aa"); now != msg {
		t.Errorf("the row changed with the reason: %q -> %q", msg, now)
	}
}

// A plugin without a pending secret gets no note.
func TestNoNoteWithoutPendingSecrets(t *testing.T) {
	f := plugintest.New("aa", plugintest.Config(pendingCfgOf))
	f.ConfiguredFn = func(any) bool { return false }
	f.Hint = "plugins.aa.url"
	e := newEnv(t, nil, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &pendingCfg{Token: secret.New("literal", "")}})
	e.run(t)
	if _, msg := e.state(t, "aa"); msg != "not configured: set plugins.aa.url" {
		t.Errorf("message = %q", msg)
	}
}

// optionalCfg is a plugin whose URL is enough and whose credentials are optional, as alertmanager
// and gatus are; Tokens and Named hold secrets in a slice and in a map.
type optionalCfg struct {
	URL    string       `json:"url"`
	Token  secret.Value `json:"token"`
	Tokens []secret.Value
	Named  map[string]struct {
		Key secret.Value `json:"key"`
	} `json:"named"`
}

func optionalFake() *plugintest.Fake {
	f := plugintest.New("aa", plugintest.Config(func() any { return &optionalCfg{} }))
	f.ConfiguredFn = func(c any) bool { return c.(*optionalCfg).URL != "" }
	f.Hint = "plugins.aa.url"
	return f
}

// A pending secret anywhere in the section, an optional credential included, keeps a plugin that
// is otherwise configured from starting: it would have built its client without the value. The
// row says only what it waits for, and the plugin starts when the value arrives.
func TestPendingOptionalSecretKeepsAConfiguredPluginFromStarting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled *bool
	}{
		{"enabled unset", nil},
		{"enabled true", new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := secret.NewK8sRefs(nil)
			f := optionalFake()
			e := newEnv(t, nil, f.Factory())
			refs.OnFill(e.sup.Reload)
			token, _ := refs.Handle(refToken)
			e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: tc.enabled, Config: &optionalCfg{URL: "http://aa.example.com", Token: token}})
			e.run(t)

			st, msg := e.state(t, "aa")
			if st != model.PluginUnconfigured || msg != "waiting for a Kubernetes Secret (see plugins.aa.token)" || f.Starts() != 0 {
				t.Fatalf("while pending: state %s, message %q, %d starts", st, msg, f.Starts())
			}

			refs.Fill(refToken, canaryValue)
			eventually(t, "the plugin to start", func() bool { return f.Starts() == 1 })
			if st, msg := e.state(t, "aa"); st == model.PluginUnconfigured || strings.Contains(msg, "waiting") {
				t.Errorf("after the fill: state %s, message %q", st, msg)
			}
		})
	}
}

// Slices and maps are walked, so a secret in a list of instances or a map of named sections
// holds the plugin back too.
func TestPendingSecretsInSlicesAndMapsAreFound(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	f := optionalFake()
	e := newEnv(t, nil, f.Factory())
	list, _ := refs.Handle(refToken)
	named, _ := refs.Handle(refBasic)
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &optionalCfg{
		URL:    "http://aa.example.com",
		Tokens: []secret.Value{secret.New("literal", ""), list},
		Named: map[string]struct {
			Key secret.Value `json:"key"`
		}{"beta": {Key: named}},
	}})
	e.run(t)
	st, msg := e.state(t, "aa")
	want := "waiting for a Kubernetes Secret (see plugins.aa.Tokens[1])" +
		"; waiting for a Kubernetes Secret (see plugins.aa.named.beta.key)"
	if st != model.PluginUnconfigured || msg != want || f.Starts() != 0 {
		t.Errorf("state %s, message %q, %d starts; want %q", st, msg, f.Starts(), want)
	}
}

// A reload that adds a pending reference to a running plugin stops it until the fill, and a
// plugin that waits is settled as far as readiness goes.
func TestReloadAddingAPendingReferenceStopsARunningPlugin(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	f := optionalFake()
	e := newEnv(t, func(o *plugin.Options) { o.ReadyRequires = []plugin.ID{"aa"} }, f.Factory())
	refs.OnFill(e.sup.Reload)
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &optionalCfg{URL: "http://aa.example.com"}})
	e.run(t)
	eventually(t, "the plugin to start", func() bool { return f.Starts() == 1 })

	token, _ := refs.Handle(refToken)
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &optionalCfg{URL: "http://aa.example.com", Token: token}})
	e.reloadUntil(t, "the plugin to stop", func() bool { return f.Stops() == 1 })
	if st, msg := e.state(t, "aa"); st != model.PluginUnconfigured || !strings.Contains(msg, "waiting for a Kubernetes Secret") {
		t.Errorf("state %s, message %q", st, msg)
	}
	if !e.sup.Ready() {
		t.Error("a plugin that waits for a Secret must not hold readiness back")
	}

	refs.Fill(refToken, canaryValue)
	eventually(t, "the plugin to start again", func() bool { return f.Starts() == 2 })
}
