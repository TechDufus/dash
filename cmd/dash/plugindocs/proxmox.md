The VMs page shows a Proxmox VE cluster through one connection: the cluster's name and quorum,
every node with its usage and facts, the totals of the online nodes, and the virtual machines and
containers of every node with their state, their node, their Kubernetes node, configuration,
snapshots and charts, with a link to each console. `url` may point at any node of the cluster: it
answers for all of them, and a VM's detail and console go to the node the VM runs on. A
standalone server is a cluster of one. A stopped VM is an attention item on the Overview:
critical when `plugins.proxmox.criticalVMs` names it, a warning otherwise. A node the cluster
reports offline and a cluster that has lost quorum are critical items. The topology on the
Overview has a host per node, each with its VMs, down to the Kubernetes cluster. The plugin runs
once `url`, `tokenID` and `tokenSecret` are all set. The client is tested against API answers of
Proxmox VE 8 and 9 (`internal/upstream/testdata/proxmox`).

## What it reads

- `GET /cluster/resources?type=node` every 30 s: the node list, each node's state and usage. A
  node's status in `proxmox.cluster` is `online`, `offline` (the cluster lost it), or `unknown`
  (a member whose status daemon has not reported for a while, and any state Proxmox does not
  name). Only an online node has usage and counts in the totals; only an offline one is an item.
- `GET /nodes/{node}/status` for every online node, with each cluster poll: version, kernel, CPU
  model, sockets and cores. A node that does not answer keeps the facts it last reported.
- `GET /cluster/status` with each cluster poll: the cluster's name and quorum. It needs
  `Sys.Audit` on `/`; without it everything else is read, the name stays empty, `quorumKnown` is
  false (no quorum is claimed and no quorum item is raised), and the row is degraded with
  `cluster status: auth`. A failed call once it has answered keeps the last name and quorum, so a
  lost quorum stays raised.
- `GET /cluster/resources?type=vm` and `GET /cluster/tasks` every 15 s: the guests and the recent
  tasks, each with its node.
- `GET /cluster/resources?type=storage` every 60 s with `storage.source: api` (the default).
- A VM's detail reads `/nodes/{node}/qemu/{vmid}/config`, `/snapshot` and
  `/agent/network-get-interfaces` on the VM's own node. The detail is for qemu guests: a
  container's answers 404.

The `PVEAuditor` role on `/` holds every privilege the plugin needs (`Sys.Audit`, `VM.Audit`,
`Datastore.Audit`).

## Nodes and storages

- An offline node stays on the page with no usage and the facts it last reported. Its guests show
  health `unknown` and raise no item of their own (the node's item covers them); after a few
  minutes the cluster forgets their names and sizes, and dash shows the ones it last saw. Their
  detail answers 503 until the node is back.
- A shared storage (NFS, Ceph, anything Proxmox marks `shared`) is listed and counted once,
  without a node; every other storage is listed per node (`proxmox:Storage/<node>/<name>`). An
  offline node's storages leave the list, because the cluster no longer knows their sizes.
- With `storage.source: prometheus` one pve-exporter covers every node. A storage is shared when
  `pve_storage_shared` says so; an exporter without that series lists every storage once per
  node, a shared one included.

## Settings

- `node` no longer selects what dash reads: dash reads every node. It names the node that is the
  Hardware page's host (its facts, its shell link and its place in the topology), whatever
  `plugins.hardware.hosts[0].nodename` says; empty takes the only node of a one-node cluster.
  `hosts[0].nodename` is the label of the host's series, and the Proxmox node only while this
  plugin names none. A `node` that is not a node of the cluster leaves the row degraded, saying
  which nodes there are.
- With a `k8s:` token still being read, the row says it waits for a Kubernetes Secret.
- `plugins.proxmox.tls.insecureSkipVerify` makes dash log a startup WARN and list an `ok` source
  named `tls: verification disabled` in the Overview's sources (not on the Proxmox row, and drawn
  as no chip: chips show failing sources only).
- In the environment: `DASH_PLUGINS_PROXMOX_CRITICAL_VMS_IDS='[101,102]'`.
