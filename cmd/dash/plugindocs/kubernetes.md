The kubernetes plugin reads the cluster through informers: nodes, namespaces, workloads, pods,
events, certificates and External Secrets on the Cluster page (a pod's logs come from the
victorialogs plugin and its metrics from the prometheus plugin); the volumes, their fill and
growth with days-to-full, the etcd size and the age of the backup CronJobs you name on the
Storage page; and the apps behind HTTP routes on the Apps page. Without
a kubeconfig or an in-cluster ServiceAccount it is `unavailable`: its pages are hidden and dash
serves everything else. The flux and cnpg plugins read the cluster through it.

## Actions

- `restart` is a rollout restart of a Deployment, StatefulSet or DaemonSet: it sets the pod
  template's `kubectl.kubernetes.io/restartedAt` annotation. `kind` is `Deployment`,
  `StatefulSet` or `DaemonSet`; `namespace` and `name` are DNS names.
- In a namespace that `plugins.kubernetes.conventions.protectedLabel` marks, the caller also
  types the workload's name (the `confirm` field).

## Demo twin

The restart action answers 202 and changes nothing.
