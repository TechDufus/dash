The VMs page lists the virtual machines and containers of one Proxmox VE node with their state,
their Kubernetes node, configuration, snapshots and charts, with a link to each console. A
stopped VM is an attention item on the Overview: critical when `plugins.proxmox.criticalVMs`
names it, a warning otherwise. The topology on the Overview runs from the host through its VMs
to the cluster. The plugin runs once `url`, `tokenID` and `tokenSecret` are all set; with
`storage.source: api` (the default) the token needs the `Datastore.Audit` privilege on
`/storage` for the storage list. The client is tested against API answers of Proxmox VE 8 and 9
(`internal/upstream/testdata/proxmox`).

## Settings

- With a `k8s:` token still being read, the row says it waits for a Kubernetes Secret.
- `plugins.proxmox.tls.insecureSkipVerify` makes dash log a startup WARN and list an `ok` source
  named `tls: verification disabled` in the Overview's sources (not on the Proxmox row, and drawn
  as no chip: chips show failing sources only).
- In the environment: `DASH_PLUGINS_PROXMOX_CRITICAL_VMS_IDS='[101,102]'`.
