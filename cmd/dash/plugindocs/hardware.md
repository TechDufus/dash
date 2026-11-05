The Hardware page shows the physical host behind the cluster from its node-exporter and IPMI
series, read through the prometheus plugin: power draw, temperatures, fans and power supplies,
ZFS pools with their scrubs, and the SMART health of each drive bay. It also lists the machines
outside the cluster you name as up or down, by their node-exporter or a Gatus check. A ZFS pool
that is not online or whose last scrub found errors, a disk that fails SMART and a CPU at or
above the warning temperature become attention items on the Overview.

## Settings

- The Hardware plugin reads the host's node from the running Proxmox plugin, so without
  `hosts[0].nodename` the Hardware row stays `unavailable` ("waiting for the Proxmox node name")
  until Proxmox runs (URL, token ID and token secret set).
- `hosts[].name` is the host's name in the web UI (default: the Proxmox node, then `nodename`).
- `hosts[].nodename` is the label of the host's node-exporter and IPMI series (default: the node
  of the running Proxmox plugin, that is `plugins.proxmox.node` or the discovered node; set it to
  run without a Proxmox token).
- `hosts[].model` is shown next to the name, e.g. `Example 2U`.
- `hosts[].bays` is the number of drive slots the Disks grid draws (0 to 128; 0 numbers the slots
  from 1, or from 0 when a disk sits in slot 0, up to the highest slot that holds a disk, at most
  128 slots, and above that the page lists the disks sorted).
- `hosts[].fanMaxRPM` is the top of the fan gauges (0 scales them to the fastest fan, and a gauge
  never ends below the speed a fan reports; the hosts payload calls it `fanMaxRpm`).
- `hosts[].sensorAliases` is `{inlet, exhaust}`, the text the names of the Inlet and Exhaust
  temperature sensors contain, ignoring case (`inlet` and `exhaust` when empty).
- In the environment:
  `DASH_PLUGINS_HARDWARE_HOSTS='[{"model":"Example 2U","bays":16,"fanMaxRPM":18000}]'`.
- A machine with `nodeExporterNodename` is up while the node-exporter with that `nodename` label
  is scraped. A `gatusEndpoint` is compared without regard to case; a check that does not exist,
  or no gatus plugin, counts as down. Each entry of `machines` has exactly one of the two, and the
  names are unique ignoring case and at most 64 bytes.
