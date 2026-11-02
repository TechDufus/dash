package pluginconf

import (
	"regexp"
	"strings"
)

// Connection modes of plugins.kubernetes.mode.
const (
	// KubernetesModeAuto reads the kubeconfig when kubeconfig (or KUBECONFIG) is set, else the
	// in-cluster configuration, else runs without a cluster.
	KubernetesModeAuto       = "auto"
	KubernetesModeInCluster  = "inCluster"
	KubernetesModeKubeconfig = "kubeconfig"
)

// Defaults of the client's rate limits and of the etcd job label: today's values.
const (
	KubernetesQPSDefault   = 20
	KubernetesBurstDefault = 40
	KubernetesEtcdJob      = "etcd"
)

// MaxBackupJobs is how many backupJobs entries a section may hold.
const MaxBackupJobs = 8

// Kubernetes is plugins.kubernetes: how dash reaches and reads the cluster.
type Kubernetes struct {
	PluginBase
	Mode string `json:"mode" dash:"enum=auto|inCluster|kubeconfig" doc:"How dash reaches the cluster: auto uses the kubeconfig when kubeconfig (or the standard KUBECONFIG variable) is set, else the in-cluster configuration, else runs without a cluster (the plugin is unavailable and the pages that need it are hidden); inCluster and kubeconfig insist on that one source and report an error when it is missing."`
	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig  string                `json:"kubeconfig" doc:"Path of a kubeconfig file, used outside the cluster (development). Empty means the in-cluster configuration. The standard KUBECONFIG variable fills it too; this setting's own variable wins."`
	Context     string                `json:"context" doc:"The kubeconfig context to use. Empty uses the kubeconfig's current context. Only read with a kubeconfig."`
	QPS         int                   `json:"qps" doc:"Requests per second dash's Kubernetes client may send. 0 or unset means the default, 20; a negative value is refused."`
	Burst       int                   `json:"burst" doc:"Burst of requests the client may send at once, at least qps. 0 or unset means the default, 40; a negative value is refused."`
	Etcd        KubernetesEtcd        `json:"etcd" doc:"How dash finds etcd's metrics."`
	Optional    KubernetesOptional    `json:"optional" doc:"CRDs that a cluster may not have. A missing CRD of the kinds the plugin reads shows as a note and makes the plugin degraded, unless it is marked optional here."`
	Conventions KubernetesConventions `json:"conventions" doc:"Labels dash reads from namespaces. None has a default: while one is empty the feature that depends on it is off."`
	Apps        KubernetesApps        `json:"apps" doc:"Which routes make the apps on the Apps page and how their annotations are read."`
	// UpstreamViaAPIServer sends the plugins' requests to in-cluster service URLs through the
	// API server's service proxy.
	UpstreamViaAPIServer bool    `json:"upstreamViaAPIServer" doc:"Send the requests of the other plugins to in-cluster service URLs (http://<service>.<namespace>.svc) through the API server's service proxy, with the Kubernetes credentials. For running dash on a laptop against a remote cluster (development); off by default. Applies to requests made after the plugin starts."`
	Actions              Actions `json:"actions" doc:"The restart action."`
	// BackupJobs locate the backup CronJobs whose last success is shown as the age of a backup.
	BackupJobs []BackupJob `json:"backupJobs" doc:"The CronJobs behind the backup ages, as [{namespace, cronjob, label}] (up to 8). cronjob also matches every CronJob named cronjob-<suffix>. label names the row; empty names each row after the CronJob. Empty hides the backup rows. A JSON list in the environment."`
}

// AppsAnnotationPrefix is the default plugins.kubernetes.apps.annotationPrefix.
const AppsAnnotationPrefix = "gethomepage.dev"

// AppsGateway names a Gateway API gateway whose routes are apps.
type AppsGateway struct {
	Namespace string `json:"namespace" doc:"Namespace of the gateway."`
	Name      string `json:"name" doc:"Name of the gateway."`
}

// KubernetesApps is how the Apps page is discovered.
type KubernetesApps struct {
	Gateways          []AppsGateway `json:"gateways" doc:"Only routes whose spec.parentRefs name one of these gateways are apps, as [{namespace, name}]. A parent reference without a namespace means the route's own namespace. Empty accepts every HTTPRoute. A JSON list in the environment."`
	ExcludeNamespaces []string      `json:"excludeNamespaces" doc:"Namespaces whose routes and ingresses are never apps (typically dash's own). Empty skips nothing. A JSON list in the environment."`
	AnnotationPrefix  string        `json:"annotationPrefix" doc:"The annotation prefix an app's name, group, description and icon are read from: <prefix>/name, <prefix>/group, <prefix>/description and <prefix>/icon. A trailing / is ignored. The default is gethomepage.dev."`
	Ingress           bool          `json:"ingress" doc:"Also read networking.k8s.io Ingresses: one app per rule host. Needs get, list and watch on ingresses in the cluster role; off by default."`
}

// KubernetesEtcd is where etcd's metrics are.
type KubernetesEtcd struct {
	Job string `json:"job" doc:"The job label of etcd's metrics in Prometheus. The default is etcd."`
}

// KubernetesOptional marks the CRD kinds the plugin reads that the cluster may lack.
type KubernetesOptional struct {
	Certificates    bool `json:"certificates" doc:"The cluster may lack the cert-manager CRDs: their absence is not a note."`
	ExternalSecrets bool `json:"externalSecrets" doc:"The cluster may lack the external-secrets CRDs: their absence is not a note."`
	HTTPRoutes      bool `json:"httpRoutes" doc:"The cluster may lack the Gateway API CRDs: their absence is not a note."`
}

// KubernetesConventions are the namespace labels dash reads.
type KubernetesConventions struct {
	// ProtectedLabel marks a namespace whose restarts must be confirmed by typing the name.
	ProtectedLabel string `json:"protectedLabel" doc:"Namespace label (value \"true\") that makes a restart ask you to type the namespace name. Empty protects no namespace."`
	// TenantLabel puts a namespace in the Tenants group; its value is the tenant.
	TenantLabel string `json:"tenantLabel" doc:"Namespace label that puts the namespace under Tenants; its value is the tenant. Empty turns the grouping off."`
	// AppsFluxPrefix puts namespaces applied by a Flux Kustomization with this name prefix in Apps.
	AppsFluxPrefix string `json:"appsFluxPrefix" doc:"Namespaces applied by a Flux Kustomization whose name starts with this prefix are listed under Apps. Empty turns the grouping off."`
}

// BackupJob is a CronJob whose last success is the age of a backup.
type BackupJob struct {
	Namespace string `json:"namespace" doc:"Namespace of the backup CronJob."`
	CronJob   string `json:"cronjob" doc:"Name of the backup CronJob. CronJobs named <cronjob>-<suffix> match too."`
	Label     string `json:"label" doc:"The row's name. Empty names each row after its CronJob."`
}

// DefaultKubernetes is the section with nothing set.
func DefaultKubernetes() Kubernetes {
	return Kubernetes{
		Mode: KubernetesModeAuto, QPS: KubernetesQPSDefault, Burst: KubernetesBurstDefault,
		Etcd: KubernetesEtcd{Job: KubernetesEtcdJob},
		Apps: KubernetesApps{AnnotationPrefix: AppsAnnotationPrefix},
	}
}

// ConnectionMode is the configured mode, auto when none is set.
func (k Kubernetes) ConnectionMode() string {
	if k.Mode == "" {
		return KubernetesModeAuto
	}
	return k.Mode
}

// EffectiveQPS is the client's rate limit, the default when none is set.
func (k Kubernetes) EffectiveQPS() int {
	if k.QPS == 0 {
		return KubernetesQPSDefault
	}
	return k.QPS
}

// EffectiveBurst is the client's burst, the default when none is set.
func (k Kubernetes) EffectiveBurst() int {
	if k.Burst == 0 {
		return KubernetesBurstDefault
	}
	return k.Burst
}

// EtcdJob is the job label of etcd's metrics, etcd when none is set.
func (k Kubernetes) EtcdJob() string {
	if k.Etcd.Job == "" {
		return KubernetesEtcdJob
	}
	return k.Etcd.Job
}

// Prefix is the annotation prefix without a trailing slash, the default when none is set.
func (a KubernetesApps) Prefix() string {
	p := strings.TrimSuffix(strings.TrimSpace(a.AnnotationPrefix), "/")
	if p == "" {
		return AppsAnnotationPrefix
	}
	return p
}

// RowName is the name of the backup row: the label, or empty when each row is named after its
// CronJob.
func (b BackupJob) RowName() string { return strings.TrimSpace(b.Label) }

// jobLabelRE bounds the etcd job label: it is put into a PromQL string, so it may hold no quote,
// backslash or brace.
var jobLabelRE = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

// Validate holds the rules of the section the schema cannot express.
func (k Kubernetes) Validate() error {
	var p problems
	switch k.Mode {
	case "", KubernetesModeAuto, KubernetesModeInCluster, KubernetesModeKubeconfig:
	default:
		p.add("mode", "%q is not %s, %s or %s", k.Mode, KubernetesModeAuto, KubernetesModeInCluster, KubernetesModeKubeconfig)
	}
	if k.QPS < 0 {
		p.add("qps", "must not be negative (0 means the default, %d)", KubernetesQPSDefault)
	}
	if k.Burst < 0 {
		p.add("burst", "must not be negative (0 means the default, %d)", KubernetesBurstDefault)
	}
	if k.QPS >= 0 && k.Burst >= 0 && k.EffectiveBurst() < k.EffectiveQPS() {
		p.add("burst", "is %d, below qps (%d)", k.EffectiveBurst(), k.EffectiveQPS())
	}
	if k.Etcd.Job != "" && !jobLabelRE.MatchString(k.Etcd.Job) {
		p.add("etcd.job", "%q is not a job label (letters, digits and _ . : / - only)", k.Etcd.Job)
	}
	if len(k.BackupJobs) > MaxBackupJobs {
		p.add("backupJobs", "has %d entries; at most %d are supported", len(k.BackupJobs), MaxBackupJobs)
	}
	rows := map[[2]string]bool{}
	for i, j := range k.BackupJobs {
		if j.Namespace == "" || j.CronJob == "" {
			p.add("backupJobs", "entry %d needs both namespace and cronjob", i+1)
			continue
		}
		if name := j.RowName(); name != "" {
			key := [2]string{j.Namespace, name}
			if rows[key] {
				p.add("backupJobs", "entry %d repeats the row %s/%s", i+1, j.Namespace, name)
			}
			rows[key] = true
		}
	}
	p.addAll("apps", k.Apps.validate())
	return p.err()
}

var (
	// annotationPrefixRE is a DNS-style prefix: labels of letters, digits and dashes joined by dots.
	annotationPrefixRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	k8sNameRE          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

func (a KubernetesApps) validate() error {
	var p problems
	for i, g := range a.Gateways {
		if g.Namespace == "" || g.Name == "" {
			p.add("gateways", "entry %d needs both namespace and name", i+1)
		}
	}
	for i, ns := range a.ExcludeNamespaces {
		if !k8sNameRE.MatchString(ns) {
			p.add("excludeNamespaces", "entry %d, %q, is not a namespace name", i+1, ns)
		}
	}
	if a.AnnotationPrefix != "" {
		if pre := a.Prefix(); len(pre) > 253 || !annotationPrefixRE.MatchString(pre) {
			p.add("annotationPrefix", "%q is not an annotation prefix such as gethomepage.dev", a.AnnotationPrefix)
		}
	}
	return p.err()
}

// ActionsConfig implements the action policy hook: restart is the plugin's one action, and its
// target has a namespace, so the deny list applies.
func (k Kubernetes) ActionsConfig() Actions { return k.Actions }
