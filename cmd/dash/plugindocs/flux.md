The GitOps page lists every Flux source, Kustomization and HelmRelease with its readiness, its
revision and, for sources, its signature verification. A failing or suspended object, and a
source that must be verified and is not, become attention items on the Overview. The plugin
reads the cluster through the kubernetes plugin, and is `unavailable` on a cluster that serves
none of the Flux kinds.

## Actions

- `reconcile` sets the `reconcile.fluxcd.io/requestedAt` annotation of a source, Kustomization
  or HelmRelease, which asks Flux to reconcile it now. Until the actions are on, the GitOps page
  shows no Reconcile button. `kind` is one of the five Flux kinds; `namespace` and `name` are DNS
  names.

## Demo twin

The reconcile action answers 202 and changes nothing.
