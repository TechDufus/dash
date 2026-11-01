package pluginconf

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

// paths lists the settings err complains about, sorted, relative to the section.
func paths(err error) []string {
	var out []string
	for _, fe := range Problems(err) {
		out = append(out, fe.Path)
	}
	slices.Sort(out)
	return out
}

func TestProblems(t *testing.T) {
	if Problems(nil) != nil {
		t.Error("nil has problems")
	}
	one := FieldError{Path: "a", Msg: "bad"}
	if got := Problems(one); len(got) != 1 || got[0] != one {
		t.Errorf("one = %+v", got)
	}
	joined := errors.Join(one, errors.Join(FieldError{Path: "b", Msg: "worse"}, errors.New("plain")))
	got := Problems(joined)
	if len(got) != 3 || got[0].Path != "a" || got[1].Path != "b" || got[2].Path != "" || got[2].Msg != "plain" {
		t.Errorf("joined = %+v", got)
	}
	if one.Error() != "a: bad" {
		t.Errorf("Error() = %q", one.Error())
	}
}

func TestAuthRules(t *testing.T) {
	pw, tok := secret.New("pw", ""), secret.New("tok", "")
	for name, tc := range map[string]struct {
		auth Auth
		want []string
	}{
		"nothing":            {Auth{}, nil},
		"basic":              {Auth{Basic: BasicAuth{Username: "u", Password: pw}}, nil},
		"bearer":             {Auth{BearerToken: tok}, nil},
		"username only":      {Auth{Basic: BasicAuth{Username: "u"}}, []string{"basic.password"}},
		"password only":      {Auth{Basic: BasicAuth{Password: pw}}, []string{"basic.username"}},
		"basic and bearer":   {Auth{Basic: BasicAuth{Username: "u", Password: pw}, BearerToken: tok}, []string{"bearerToken"}},
		"half basic, bearer": {Auth{Basic: BasicAuth{Username: "u"}, BearerToken: tok}, []string{"basic.password", "bearerToken"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := paths(tc.auth.validate()); !slices.Equal(got, tc.want) {
				t.Errorf("problems at %v, want %v", got, tc.want)
			}
		})
	}
	// Every section that has auth applies the rules under auth.
	bad := Auth{Basic: BasicAuth{Username: "u"}}
	for name, err := range map[string]error{
		"prometheus":   Prometheus{Auth: bad}.Validate(),
		"alertmanager": Alertmanager{Auth: bad}.Validate(),
		"gatus":        Gatus{Auth: bad, PageSize: 1}.Validate(),
		"victorialogs": VictoriaLogs{Auth: bad, Fields: DefaultVictoriaLogs().Fields, Limit: 1}.Validate(),
	} {
		if got := paths(err); !slices.Equal(got, []string{"auth.basic.password"}) {
			t.Errorf("%s: problems at %v, want auth.basic.password", name, got)
		}
	}
}

func TestPrometheusHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		headers map[string]string
		bad     int
	}{
		"none":                 {nil, 0},
		"plain":                {map[string]string{"X-Scope-OrgID": "tenant-1", "X-Custom_1.2": "v"}, 0},
		"not a token":          {map[string]string{"X Bad": "v"}, 1},
		"colon":                {map[string]string{"X-A:": "v"}, 1},
		"empty name":           {map[string]string{"": "v"}, 1},
		"authorization":        {map[string]string{"Authorization": "Bearer x"}, 1},
		"case does not matter": {map[string]string{"authorization": "x", "COOKIE": "x", "hOsT": "x", "Proxy-Authorization": "x"}, 4},
	} {
		t.Run(name, func(t *testing.T) {
			err := Prometheus{Headers: tc.headers}.Validate()
			if got := paths(err); len(got) != tc.bad || !slices.Equal(got, slices.Repeat([]string{"headers"}, tc.bad)) {
				t.Errorf("problems at %v, want %d at headers (%v)", got, tc.bad, err)
			}
		})
	}
	err := Prometheus{Headers: map[string]string{"Authorization": "x"}}.Validate()
	if err == nil || !strings.Contains(err.Error(), `"Authorization"`) {
		t.Errorf("the message must name the header: %v", err)
	}
}

func TestAlertmanagerSilenceDurations(t *testing.T) {
	if err := DefaultAlertmanager().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for _, ok := range []string{"1h", "4h", "24h", "7d"} {
		if err := (Alertmanager{SilenceDurations: []string{ok}}).Validate(); err != nil {
			t.Errorf("%s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"2h", "1d", "168h", "1H", "", "forever"} {
		err := Alertmanager{SilenceDurations: []string{"1h", bad}}.Validate()
		if got := paths(err); !slices.Equal(got, []string{"silenceDurations"}) {
			t.Errorf("%q: problems at %v", bad, got)
		}
	}
}

func TestAlertmanagerSeverityMap(t *testing.T) {
	for _, ok := range []map[string]string{nil, {}, {"critical": "crit", "warning": "warn", "info": "warn"}} {
		if err := (Alertmanager{SeverityMap: ok}).Validate(); err != nil {
			t.Errorf("%v rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"fatal", "critical", "warning", "info", "", "CRIT"} {
		err := Alertmanager{SeverityMap: map[string]string{"critical": "crit", "x": bad}}.Validate()
		if got := paths(err); !slices.Equal(got, []string{"severityMap"}) {
			t.Errorf("value %q: problems at %v", bad, got)
		}
		if err != nil && !strings.Contains(err.Error(), `"x"`) {
			t.Errorf("the message must name the label: %v", err)
		}
	}
}

func TestGatusPageSize(t *testing.T) {
	if err := DefaultGatus().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for _, n := range []int{0, -1} {
		if got := paths(Gatus{PageSize: n}.Validate()); !slices.Equal(got, []string{"pageSize"}) {
			t.Errorf("pageSize %d: problems at %v", n, got)
		}
	}
	if err := (Gatus{PageSize: 1, CriticalGroups: []string{"infrastructure"}}).Validate(); err != nil {
		t.Errorf("pageSize 1: %v", err)
	}
}

func TestVictoriaLogsFieldsAndLimit(t *testing.T) {
	if err := DefaultVictoriaLogs().Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	with := func(f LogFields, limit int) error { return VictoriaLogs{Fields: f, Limit: limit}.Validate() }
	good := DefaultVictoriaLogs().Fields
	for _, ok := range []string{"pod", "_x", "a.b.c", "Pod_Name9", "kubernetes.pod_name"} {
		if err := with(LogFields{Namespace: ok, Pod: ok, Container: ok}, 1); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "9lives", ".pod", "a b", `a" OR "1"="1`, "a:b", "a-b", "a|b", "pod\n", "a{b}", "é"} {
		for path, f := range map[string]LogFields{
			"fields.namespace": {Namespace: bad, Pod: good.Pod, Container: good.Container},
			"fields.pod":       {Namespace: good.Namespace, Pod: bad, Container: good.Container},
			"fields.container": {Namespace: good.Namespace, Pod: good.Pod, Container: bad},
		} {
			if got := paths(with(f, 1)); !slices.Equal(got, []string{path}) {
				t.Errorf("%q at %s: problems at %v", bad, path, got)
			}
		}
	}
	for _, n := range []int{0, -5} {
		if got := paths(with(good, n)); !slices.Equal(got, []string{"limit"}) {
			t.Errorf("limit %d: problems at %v", n, got)
		}
	}
}

func TestUIIconBaseURL(t *testing.T) {
	for _, ok := range []string{"", "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png", "http://icons.lan:8080/png", "https://icons.example.com"} {
		if err := (UI{IconBaseURL: ok}).Validate(); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"ftp://x.example.com", "javascript:alert(1)", "/icons", "icons.example.com", "https://", "https:///x",
		"https://x.example.com/png?v=1", "https://x.example.com/png#top", "https://x.example.com/png?", "https://x.example.com/png#", "https://exa mple.com",
	} {
		if got := paths(UI{IconBaseURL: bad}.Validate()); !slices.Equal(got, []string{"iconBaseURL"}) {
			t.Errorf("%q: problems at %v", bad, got)
		}
	}
}

func TestDefaults(t *testing.T) {
	if got := DefaultPrometheus(); got.Timeout != Duration(5*time.Second) || got.URL != "" {
		t.Errorf("prometheus = %+v", got)
	}
	am := DefaultAlertmanager()
	if !slices.Equal(am.IgnoreAlerts, []string{"Watchdog", "InfoInhibitor"}) ||
		!reflect.DeepEqual(am.SeverityMap, map[string]string{"critical": "crit", "warning": "warn"}) ||
		!slices.Equal(am.SilenceDurations, []string{"1h", "4h", "24h", "7d"}) ||
		!slices.Equal(am.ProtectedAlerts, []string{"Watchdog"}) || am.Actions.Enabled {
		t.Errorf("alertmanager = %+v", am)
	}
	if g := DefaultGatus(); g.PageSize != 30 || len(g.CriticalGroups) != 0 {
		t.Errorf("gatus = %+v", g)
	}
	vl := DefaultVictoriaLogs()
	if vl.Fields != (LogFields{Namespace: "kubernetes.pod_namespace", Pod: "kubernetes.pod_name", Container: "kubernetes.container_name"}) ||
		vl.Limit != 500 || len(vl.DenyNamespaces) != 0 {
		t.Errorf("victorialogs = %+v", vl)
	}
	if h := DefaultHeadlamp(); h.Cluster != "main" {
		t.Errorf("headlamp = %+v", h)
	}

	// Every default section passes its own Validate, and a default is a fresh value: changing one
	// never leaks into the next.
	for name, err := range map[string]error{
		"prometheus": DefaultPrometheus().Validate(), "alertmanager": DefaultAlertmanager().Validate(),
		"gatus": DefaultGatus().Validate(), "victorialogs": DefaultVictoriaLogs().Validate(),
		"grafana": nil, "launcher": Launcher{}.Validate(), "ui": UI{}.Validate(),
	} {
		if err != nil {
			t.Errorf("%s defaults: %v", name, err)
		}
	}
	a := DefaultAlertmanager()
	a.SeverityMap["critical"] = "warn"
	a.IgnoreAlerts[0] = "x"
	a.SilenceDurations[0] = "x"
	if b := DefaultAlertmanager(); b.SeverityMap["critical"] != "crit" || b.IgnoreAlerts[0] != "Watchdog" || b.SilenceDurations[0] != "1h" {
		t.Errorf("a default aliases the previous one: %+v", b)
	}
}

// The hooks cmd/dash reads: alertmanager has an action toggle and no deny list, victorialogs a
// read deny list and no actions, and the rest neither.
func TestPolicyHooks(t *testing.T) {
	am := Alertmanager{Actions: ActionsToggle{Enabled: true}}
	if got := am.ActionsConfig(); !got.Enabled || got.DenyNamespaces != nil {
		t.Errorf("alertmanager ActionsConfig = %+v", got)
	}
	if got := (Alertmanager{}).ActionsConfig(); got.Enabled {
		t.Errorf("default alertmanager actions on: %+v", got)
	}
	vl := VictoriaLogs{DenyNamespaces: []string{"kube-system"}}
	if got := vl.ReadDenyConfig(); !slices.Equal(got, []string{"kube-system"}) {
		t.Errorf("victorialogs ReadDenyConfig = %v", got)
	}
	for name, v := range map[string]any{
		"prometheus": Prometheus{}, "gatus": Gatus{}, "grafana": Grafana{}, "headlamp": Headlamp{}, "launcher": Launcher{}, "victorialogs": VictoriaLogs{},
	} {
		if _, ok := v.(interface{ ActionsConfig() Actions }); ok {
			t.Errorf("%s has an action policy", name)
		}
	}
	for name, v := range map[string]any{
		"prometheus": Prometheus{}, "alertmanager": Alertmanager{}, "gatus": Gatus{}, "grafana": Grafana{}, "headlamp": Headlamp{}, "launcher": Launcher{},
	} {
		if _, ok := v.(interface{ ReadDenyConfig() []string }); ok {
			t.Errorf("%s has a read deny list", name)
		}
	}
	// A section is usable through a pointer too, which is how the supervisor holds it.
	var section any = &Alertmanager{Actions: ActionsToggle{Enabled: true}}
	if a, ok := section.(interface{ ActionsConfig() Actions }); !ok || !a.ActionsConfig().Enabled {
		t.Error("*Alertmanager lost its ActionsConfig")
	}
	if _, ok := section.(interface{ Validate() error }); !ok {
		t.Error("*Alertmanager has no Validate")
	}
}

func TestSecretsRedactInSections(t *testing.T) {
	a := Auth{Basic: BasicAuth{Username: "u", Password: secret.New("hunter2", "")}, BearerToken: secret.New("tok3n", "")}
	s := fmt.Sprintf("%v %+v %#v", a, a, a)
	if strings.Contains(s, "hunter2") || strings.Contains(s, "tok3n") {
		t.Errorf("a secret leaked through fmt: %s", s)
	}
}
