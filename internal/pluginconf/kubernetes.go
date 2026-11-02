package pluginconf

// Kubernetes is plugins.kubernetes: how dash reaches and reads the cluster.
type Kubernetes struct {
	PluginBase
	// Kubeconfig is used outside the cluster (dev); empty means in-cluster config.
	Kubeconfig  string                `json:"kubeconfig" doc:"Path of a kubeconfig file, used outside the cluster (development). Empty means the in-cluster configuration. The standard KUBECONFIG variable fills it too; this setting's own variable wins."`
	Conventions KubernetesConventions `json:"conventions" doc:"Labels dash reads from namespaces. None has a default: while one is empty the feature that depends on it is off."`
	// BackupJobs locate the NAS backup CronJob (name or name-*); at most one until G3.
	BackupJobs []BackupJob `json:"backupJobs" doc:"The CronJob behind the NAS backup age, as [{namespace, cronjob}]. One entry is supported for now. Empty hides the NAS backup row."`
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

// BackupJob is a CronJob whose last success is the NAS backup age.
type BackupJob struct {
	Namespace string `json:"namespace" doc:"Namespace of the backup CronJob."`
	CronJob   string `json:"cronjob" doc:"Name of the backup CronJob, or name-* to match every CronJob with that prefix."`
}

// Validate holds the rules that lived in internal/config before the section moved, with the
// same paths and messages: at most one backup job, each with both fields.
func (k Kubernetes) Validate() error {
	var p problems
	if len(k.BackupJobs) > 1 {
		p.add("backupJobs", "has %d entries; one job is supported until G3", len(k.BackupJobs))
	}
	for i, j := range k.BackupJobs {
		if j.Namespace == "" || j.CronJob == "" {
			p.add("backupJobs", "entry %d needs both namespace and cronjob", i+1)
		}
	}
	return p.err()
}
