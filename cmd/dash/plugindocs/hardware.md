The Hardware page shows the physical host behind the cluster from its node-exporter and IPMI
series, read through the prometheus plugin: power draw, temperatures, fans and power supplies,
ZFS pools with their scrubs, and the SMART health of each drive bay. It also lists the machines
outside the cluster you name as up or down, by their node-exporter or a Gatus check. A ZFS pool
that is not online or whose last scrub found errors, a disk that fails SMART and a CPU at or
above the warning temperature become attention items on the Overview.
