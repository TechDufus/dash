A panel on the Storage page lists the CloudNativePG clusters with their state and the age of their
last available backup. The clusters come from the cluster through the kubernetes plugin; the
backup age comes from `plugins.cnpg.backupMetric` through the prometheus plugin, and without it
the clusters are listed with no age. The plugin is `unavailable` on a cluster without the
CloudNativePG CRD.

## Settings

The cnpg plugin reads the newest sample of `plugins.cnpg.backupMetric` per cluster with
`role="primary"` for the age shown on the Storage page. Without the prometheus plugin the
clusters are listed with a `prometheus: not_configured` note and no age.

## Kubernetes RBAC

The cnpg plugin reads the CloudNativePG clusters with the same ServiceAccount as the kubernetes
plugin; a refusal shows on the cnpg row, not on the kubernetes one.
