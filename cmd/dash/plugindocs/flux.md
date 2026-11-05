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

## Settings

- `plugins.flux.suspendReasonAnnotation`: the reason shows in the suspended warning's detail and
  on the GitOps page; empty shows "suspended" alone.
- The check that a `plugins.flux.verify.sources` entry exists is skipped while the list of its
  kind cannot be read, so a refused list never raises a false critical. An entry with an unknown
  field, another kind, no namespace or name, or a duplicate is refused when the configuration
  loads. In the environment:
  `DASH_PLUGINS_FLUX_VERIFY_SOURCES='[{"kind":"GitRepository","namespace":"gitops","name":"fleet"}]'`.

## Kubernetes RBAC

The reconcile write goes through the kubernetes plugin's client.
