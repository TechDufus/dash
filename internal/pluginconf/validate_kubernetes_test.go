package pluginconf

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The backup job rules: up to eight entries, each with a namespace and a cronjob, and a row name
// that is not repeated within a namespace. The paths and the words of the old rules are kept.
func TestKubernetesBackupJobRules(t *testing.T) {
	job := BackupJob{Namespace: "backup", CronJob: "nightly"}
	many := make([]BackupJob, MaxBackupJobs+1)
	for i := range many {
		many[i] = BackupJob{Namespace: "backup", CronJob: "job", Label: string(rune('a' + i))}
	}
	for _, tc := range []struct {
		name string
		k    Kubernetes
		want []string
		msg  string
	}{
		{"nothing set is fine", Kubernetes{}, nil, ""},
		{"one complete job", Kubernetes{BackupJobs: []BackupJob{job}}, nil, ""},
		{"two jobs are fine now", Kubernetes{BackupJobs: []BackupJob{job, {Namespace: "backup", CronJob: "weekly", Label: "weekly"}}}, nil, ""},
		{"eight jobs", Kubernetes{BackupJobs: many[:MaxBackupJobs]}, nil, ""},
		{"nine jobs", Kubernetes{BackupJobs: many}, []string{"backupJobs"}, "has 9 entries; at most 8 are supported"},
		{"no cronjob", Kubernetes{BackupJobs: []BackupJob{{Namespace: "backup"}}}, []string{"backupJobs"}, "entry 1 needs both namespace and cronjob"},
		{"no namespace", Kubernetes{BackupJobs: []BackupJob{{CronJob: "nightly"}}}, []string{"backupJobs"}, "entry 1 needs both namespace and cronjob"},
		{"two incomplete jobs report each rule", Kubernetes{BackupJobs: []BackupJob{{}, {}}}, []string{"backupJobs", "backupJobs"}, ""},
		{"a row name repeated in one namespace", Kubernetes{BackupJobs: []BackupJob{
			{Namespace: "backup", CronJob: "a", Label: "nas"}, {Namespace: "backup", CronJob: "b", Label: "nas"},
		}}, []string{"backupJobs"}, "entry 2 repeats the row backup/nas"},
		{"the same row name in two namespaces", Kubernetes{BackupJobs: []BackupJob{
			{Namespace: "a", CronJob: "x", Label: "nas"}, {Namespace: "b", CronJob: "x", Label: "nas"},
		}}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.k.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
			if tc.msg != "" && (err == nil || !strings.Contains(err.Error(), tc.msg)) {
				t.Errorf("error = %v, want %q", err, tc.msg)
			}
		})
	}
}

func TestKubernetesDefaultsAreValidAndEqualToday(t *testing.T) {
	d := DefaultKubernetes()
	if err := d.Validate(); err != nil {
		t.Fatalf("the defaults are invalid: %v", err)
	}
	if d.ConnectionMode() != KubernetesModeAuto || d.EffectiveQPS() != 20 || d.EffectiveBurst() != 40 || d.EtcdJob() != "etcd" {
		t.Errorf("defaults = %+v", d)
	}
	var zero Kubernetes
	if zero.ConnectionMode() != KubernetesModeAuto || zero.EffectiveQPS() != 20 || zero.EffectiveBurst() != 40 || zero.EtcdJob() != "etcd" {
		t.Errorf("an unset section reads as the defaults: %+v", zero)
	}
}

func TestKubernetesSettingRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		k    Kubernetes
		want []string
	}{
		{"mode auto", Kubernetes{Mode: "auto"}, nil},
		{"mode inCluster", Kubernetes{Mode: "inCluster"}, nil},
		{"mode kubeconfig", Kubernetes{Mode: "kubeconfig"}, nil},
		{"unknown mode", Kubernetes{Mode: "remote"}, []string{"mode"}},
		{"qps and burst", Kubernetes{QPS: 50, Burst: 50}, nil},
		{"zero qps and burst mean the defaults", Kubernetes{QPS: 0, Burst: 0}, nil},
		{"burst below qps", Kubernetes{QPS: 50, Burst: 10}, []string{"burst"}},
		{"only qps above the default burst", Kubernetes{QPS: 100}, []string{"burst"}},
		{"negative qps", Kubernetes{QPS: -1}, []string{"qps"}},
		{"negative burst", Kubernetes{Burst: -5}, []string{"burst"}},
		{"an etcd job", Kubernetes{Etcd: KubernetesEtcd{Job: "kube-etcd"}}, nil},
		{"an etcd job that breaks out of the query", Kubernetes{Etcd: KubernetesEtcd{Job: `etcd"}or{job=~".*`}}, []string{"etcd.job"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.k.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

// TestBurstBelowQPSIsInvalid names the rule of the settings table: burst is at least qps.
func TestBurstBelowQPSIsInvalid(t *testing.T) {
	err := Kubernetes{QPS: 40, Burst: 20}.Validate()
	if err == nil || !strings.Contains(err.Error(), "burst: is 20, below qps (40)") {
		t.Fatalf("err = %v", err)
	}
}

// The settings table says what the code does: 0 is not "no limit" but the default.
func TestKubernetesRateLimitDocsSayZeroIsTheDefault(t *testing.T) {
	for _, name := range []string{"QPS", "Burst"} {
		f, _ := reflect.TypeOf(Kubernetes{}).FieldByName(name)
		if doc := f.Tag.Get("doc"); !strings.Contains(doc, "0 or unset means the default") {
			t.Errorf("%s doc = %q", name, doc)
		}
	}
	if got := (Kubernetes{}).EffectiveQPS(); got != KubernetesQPSDefault {
		t.Errorf("EffectiveQPS of 0 = %d", got)
	}
	if got := (Kubernetes{}).EffectiveBurst(); got != KubernetesBurstDefault {
		t.Errorf("EffectiveBurst of 0 = %d", got)
	}
}

func TestKubernetesActionsConfigIsTheSection(t *testing.T) {
	k := Kubernetes{Actions: Actions{Enabled: true, DenyNamespaces: []string{"kube-system"}}}
	got := k.ActionsConfig()
	if !got.Enabled || !slices.Equal(got.DenyNamespaces, []string{"kube-system"}) {
		t.Errorf("ActionsConfig = %+v", got)
	}
}

func TestKubernetesAppsRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    KubernetesApps
		want []string
	}{
		{"nothing set", KubernetesApps{}, nil},
		{"a gateway", KubernetesApps{Gateways: []AppsGateway{{Namespace: "gateway", Name: "main"}}}, nil},
		{"a gateway without a namespace", KubernetesApps{Gateways: []AppsGateway{{Name: "main"}}}, []string{"apps.gateways"}},
		{"a gateway without a name", KubernetesApps{Gateways: []AppsGateway{{Namespace: "gateway"}}}, []string{"apps.gateways"}},
		{"an excluded namespace", KubernetesApps{ExcludeNamespaces: []string{"dash", "kube-system"}}, nil},
		{"a bad excluded namespace", KubernetesApps{ExcludeNamespaces: []string{"Dash!"}}, []string{"apps.excludeNamespaces"}},
		{"an empty excluded namespace", KubernetesApps{ExcludeNamespaces: []string{""}}, []string{"apps.excludeNamespaces"}},
		{"a prefix", KubernetesApps{AnnotationPrefix: "dash.example.com"}, nil},
		{"a prefix with a trailing slash", KubernetesApps{AnnotationPrefix: "gethomepage.dev/"}, nil},
		{"a prefix with a space", KubernetesApps{AnnotationPrefix: "my prefix"}, []string{"apps.annotationPrefix"}},
		{"a prefix with a scheme", KubernetesApps{AnnotationPrefix: "https://x.example.com"}, []string{"apps.annotationPrefix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Kubernetes{Apps: tc.a}.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

func TestKubernetesAppsPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"": "gethomepage.dev", "gethomepage.dev": "gethomepage.dev", "gethomepage.dev/": "gethomepage.dev",
		" dash.example.com/ ": "dash.example.com", "/": "gethomepage.dev",
	} {
		if got := (KubernetesApps{AnnotationPrefix: in}).Prefix(); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", in, got, want)
		}
	}
	if got := DefaultKubernetes().Apps; got.AnnotationPrefix != "gethomepage.dev" || got.Ingress || len(got.Gateways) != 0 || len(got.ExcludeNamespaces) != 0 {
		t.Errorf("default apps = %+v", got)
	}
}
