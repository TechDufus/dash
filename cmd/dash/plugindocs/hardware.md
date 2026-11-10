The Hardware page shows the physical hosts behind the cluster from their node-exporter and IPMI
series, read through the prometheus plugin: power draw, temperatures, fans and power supplies,
ZFS pools with their scrubs, and the SMART health of each drive bay. It also shows the machines
outside the cluster you name: up or down, by their node-exporter or a Gatus check, and for a
node-exporter machine its CPU, memory, root disk, load, uptime and temperature. A ZFS pool that is
not online or whose last scrub found errors, a disk that fails SMART and a CPU at or above the
warning temperature become attention items on the Overview.

## Hosts

- `hosts` takes any number of hosts, shown in their order. Names and nodenames are unique
  ignoring case, a name is not another host's nodename, names are at most 64 bytes, and at most
  one host leaves `nodename` empty.
- `hosts[].nodename` is the label of the host's node-exporter and IPMI series. The host without a
  nodename takes the node the running Proxmox plugin names (`plugins.proxmox.node`, or the only
  node of a one-node cluster), so its row stays `unavailable` ("waiting for the Proxmox node
  name") until Proxmox runs (URL, token ID and token secret set); in a cluster of several nodes
  the row names the nodes and says what to set. Hosts with a nodename run regardless.
- A host's node (its Proxmox facts, shell link and place in the Overview's topology) is the
  cluster's node of its nodename (or of the nodename's first DNS label, `pve1.example.com` being
  `pve1`), else the node the Proxmox plugin names for the host without a nodename or the only
  host, else the nodename. While the Proxmox plugin has not read the cluster yet a host keeps the
  node it last resolved.
- `hosts[].name` is the host's name in the web UI, its refs (`hardware:Host/<name>`), attention
  ids and page (`/hardware/<name>`). Empty uses the nodename, or for the host without one the
  Proxmox node. Two hosts that resolve to the same nodename or name are never merged: the later
  one is not read and the row says which.
- `hosts[].model` is shown next to the name, e.g. `Example 2U`.
- `hosts[].bays` is the number of drive slots the Disks grid draws (0 to 128; 0 numbers the slots
  from 1, or from 0 when a disk sits in slot 0, up to the highest slot that holds a disk, at most
  128 slots, and above that the page lists the disks sorted).
- `hosts[].fanMaxRPM` is the top of the fan gauges (0 scales them to the fastest fan, and a gauge
  never ends below the speed a fan reports; the hosts payload calls it `fanMaxRpm`).
- `hosts[].sensorAliases` is `{inlet, exhaust}`, the text the names of the Inlet and Exhaust
  temperature sensors contain, ignoring case (`inlet` and `exhaust` when empty).
- In the environment:
  `DASH_PLUGINS_HARDWARE_HOSTS='[{"nodename":"pve-1","model":"Example 2U","bays":16,"fanMaxRPM":18000},{"nodename":"pve-2"}]'`.
- The attention ids name the host: `zfs:pool:<name>/<pool>`, `zfs:scrub:<name>/<pool>`,
  `smart:<name>/<bay>` and `host:cpu-temp:<name>`. The `power` KPI is the sum over the hosts and
  `cpuTemp` the hottest package of all of them with the worst health. A failing host poll fails
  the one `prometheus` source, and the row names the host (`prometheus: pve-2: timeout`).

## Series

- `host.power`, `host.cpuTemp`, `host.cpu`, `host.mem`, `host.temps` and `host.fans` take the host
  as `node`: its Proxmox node, else its nodename, else its name, ignoring case; a `node` that
  names no host answers 404. The API accepts only lowercase DNS names as `node`, so the web and
  the context bundles ask by the host's node lowercased. A host whose node has characters outside
  a lowercase DNS label (e.g. `_`) cannot be charted.
- Without `node`, one host is that host. With several, `host.power` is the sum of all of them and
  `host.cpuTemp` the hottest package (the Overview tiles' sparks), and the other series draw one
  line per host, each with its `nodename` label.

## Machines

- A machine with `nodeExporterNodename` is up while the node-exporter with that `nodename` label
  is scraped. A `gatusEndpoint` is compared without regard to case; a check that does not exist,
  or no gatus plugin, counts as down. Each entry of `machines` has exactly one of the two, and the
  names are unique ignoring case and at most 64 bytes.
- A node-exporter machine that is up has stats, once its `node_uname_info` or CPU series is
  found: OS, architecture and kernel (`node_uname_info`), logical CPUs and their use
  (`node_cpu_seconds_total`), memory, the root file system (`node_filesystem_size_bytes` and
  `_avail_bytes` on `/`), load (`node_load1`, `5`, `15`), uptime (`node_time_seconds` minus
  `node_boot_time_seconds`) and the hottest CPU temperature (`node_hwmon_temp_celsius` with
  `selectors.hwmonChip`, which hosts and machines share, else a CPU `node_thermal_zone_temp`).
  Memory is `MemTotal - MemAvailable` on Linux; on macOS, which has neither, the total is
  `node_memory_total_bytes` and the used figure is Activity Monitor's Memory Used (internal minus
  purgeable, plus wired and compressed). macOS reports no temperature. CPUs, memory, disk and
  uptime that are not reported are 0 and the page leaves them out, and a missing temperature is
  absent; load is always shown (node-exporter reports it on Linux and macOS). Every figure is one
  query for all machines, so the query count does not grow with them. A Gatus machine has no
  stats.
