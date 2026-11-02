package pluginconf

// CNPGBackupMetric is the default of plugins.cnpg.backupMetric: the series the CloudNativePG
// Barman Cloud plugin exports for the time of the last backup a cluster has available.
const CNPGBackupMetric = "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp"

// CNPG is plugins.cnpg: how dash reads CloudNativePG clusters. The plugin needs the kubernetes
// plugin for the clusters themselves and reads the age of their last backup from Prometheus.
type CNPG struct {
	PluginBase
	// BackupMetric is the name of the series whose newest sample, per primary, is the time of the
	// last backup.
	BackupMetric string `json:"backupMetric" doc:"Name of the Prometheus series that holds the Unix time of a cluster's last available backup, labelled namespace and cluster (role=\"primary\" selects the primary's). The default is the Barman Cloud plugin's barman_cloud_cloudnative_pg_io_last_available_backup_timestamp. Must be a valid metric name."`
}

// DefaultCNPG is the section with nothing set.
func DefaultCNPG() CNPG { return CNPG{BackupMetric: CNPGBackupMetric} }

// BackupMetricName is the backup metric, the default when none is set.
func (c CNPG) BackupMetricName() string {
	if c.BackupMetric == "" {
		return CNPGBackupMetric
	}
	return c.BackupMetric
}

// Validate checks that the backup metric is a metric name: it is put into a PromQL expression,
// so anything else would be injected into it.
func (c CNPG) Validate() error {
	var p problems
	if m := c.BackupMetric; m != "" && !metricNameRE.MatchString(m) {
		p.add("backupMetric", "%q is not a metric name", m)
	}
	return p.err()
}
