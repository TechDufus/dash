The pod sheet on the Cluster page has a Logs tab while this plugin runs: the latest lines of a pod
and a live tail, read from VictoriaLogs. `plugins.victorialogs.denyNamespaces`
hides the logs of namespaces from everyone (403, audited), and `server.limits.tailsPerUser`,
`server.limits.tails` and `server.limits.tailMaxDuration` bound the tails.

## Settings

The `plugins.victorialogs.fields.*` fields are used for the query and for the pod and container
shown with each line. The fields that share a name's prefix up to its first dot (`kubernetes.` by
default) are left out of the message text.
