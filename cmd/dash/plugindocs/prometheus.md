dash asks a Prometheus-compatible server (VictoriaMetrics or Prometheus) for the series behind
its charts and KPIs. The plugin has no page of its own: the plugins that use it read their
metrics through it, and the charts on `/api/v1/series` query it.
