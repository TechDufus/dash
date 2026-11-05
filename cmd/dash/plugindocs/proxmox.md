The VMs page lists the virtual machines and containers of one Proxmox VE node with their state,
their Kubernetes node, configuration, snapshots and charts, with a link to each console. A
stopped VM is an attention item on the Overview: critical when `plugins.proxmox.criticalVMs`
names it, a warning otherwise. The topology on the Overview runs from the host through its VMs
to the cluster. The plugin runs once `url`, `tokenID` and `tokenSecret` are all set; with
`storage.source: api` (the default) the token needs the `Datastore.Audit` privilege on
`/storage` for the storage list.
