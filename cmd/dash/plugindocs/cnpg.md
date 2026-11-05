A panel on the Storage page lists the CloudNativePG clusters with their state and the age of their
last available backup. The clusters come from the cluster through the kubernetes plugin; the
backup age comes from `plugins.cnpg.backupMetric` through the prometheus plugin, and without it
the clusters are listed with no age. The plugin is `unavailable` on a cluster without the
CloudNativePG CRD.
