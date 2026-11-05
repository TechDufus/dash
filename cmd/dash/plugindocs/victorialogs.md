The pod sheet on the Cluster page has a Logs tab while this plugin runs: the latest lines of a pod
and a live tail, read from VictoriaLogs. `plugins.victorialogs.denyNamespaces`
hides the logs of namespaces from everyone (403, audited), and `server.limits.tailsPerUser`,
`server.limits.tails` and `server.limits.tailMaxDuration` bound the tails.
