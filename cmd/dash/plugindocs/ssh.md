The Hosts page shows the Linux machines you add by address and user: what they are (OS, kernel,
CPU, board, disks, file systems, ZFS pools), what runs on them (docker or podman containers,
systemd services) and live CPU, memory, network, disk, temperature and fan figures, with an hour of
history. dash reads them over SSH with a key pair of its own. It installs nothing on a host, writes
nothing there, never asks for sudo and holds no password.

A host that cannot be reached, refuses dash's key or presents another host key than the trusted
one, a file system at 90 % (warning) or 95 % (critical), failed systemd units and a ZFS pool that
is not online become attention items on the Overview.

The plugin needs `DASH_SECRET_KEY` and a writable data directory (`DASH_DATA_DIR`): its private
key is sealed with the secret key in `credentials.yaml`, made at the first start that has a host
and the same at every later one. Without either the plugin does not start, and the dash log says
which to set. dash never makes the secret key itself. A `DASH_SECRET_KEY` rotated without
`DASH_SECRET_KEY_PREVIOUS` loses the key pair: dash makes a new one, logs a warning, and every
host must be given the new public key.

## Adding a host

1. On the host, make a dedicated unprivileged user for dash, for example
   `sudo useradd --system --create-home --shell /bin/sh dash-reader`. It needs no group, no sudo
   rule and no password: everything dash reads is readable by any user, except the container
   list (see below).
2. In dash, open Settings → SSH hosts and add the host (or name it in `plugins.ssh.hosts`) with
   its address and that user, and leave `hostKey` empty. dash connects, reads the key the host
   presents and ends the connection before it sends any credential. The host is `unverified` and
   the settings page shows the key's fingerprint.
3. Compare the fingerprint with the host's own, read on the host or from its console:
   `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`. When they match, trust the key: the page
   saves it as `hostKey` through the settings API (audited as `settings.set`). A host in the
   config file or the environment needs its `hostKey` written there; the page shows the line.
4. The host is `unauthorized` until it accepts dash's public key, which the settings page shows
   with the one line to run as the user on the host:

   ```sh
   mkdir -p -m 700 ~/.ssh && echo 'restrict ssh-ed25519 AAAA... dash' >> ~/.ssh/authorized_keys
   ```

   `restrict` turns off port, agent and X11 forwarding and the pty for this key: dash needs none
   of them, only one command without a terminal. dash tries again every few seconds at first, so
   the host turns `up` moments later.

Until a host is up, its own log shows dash's attempts: a key read every two minutes while it is
`unverified` (OpenSSH logs a connection that did not try to authenticate), and a refused key at
most once a minute while it is `unauthorized`. A host that bans addresses for failed sign-ins
(fail2ban in its aggressive mode, for example) may ban dash's: authorize the key soon after
adding the host, or remove the host from the settings until you can.

In the environment the list is JSON:
`DASH_PLUGINS_SSH_HOSTS='[{"name":"nas","address":"192.0.2.10","user":"dash-reader","hostKey":"ssh-ed25519 AAAA..."}]'`.

An address saved from the settings page may not reach a loopback address (dash's own listeners);
one in the config file or the environment may. Link-local addresses and the cloud metadata
address are refused whatever the source. Private LAN addresses are allowed: that is where the
hosts are.

## States

| State | What it means | What dash does |
|---|---|---|
| `connecting` | dash has not finished a first attempt. | |
| `unverified` | The host has no `hostKey`. The page shows the key it offers. | Sends no credential; reads the key again every two minutes. |
| `key_changed` | The host presents another key than `hostKey` (a reinstall, or someone in between). | Refuses the connection and sends no credential; tries again every minute. Check the host, then trust the new key. Critical attention item. |
| `unauthorized` | The key matches, and the host refuses dash's public key for the user. | Tries again after a few seconds, backing off to once a minute. Warning attention item. |
| `unreachable` | The dial, the handshake or the session failed, the host fell silent, or it broke the protocol. | Tries again with a backoff from 2 seconds to a minute, with jitter. Warning attention item. |
| `unsupported` | The host answered, and it is not Linux, or its `/bin/sh` cannot run the script. | Tries again every five minutes. |
| `up` | Samples arrive every `intervalSeconds`. | |

A host's health is the worst of its state and, while it is up, what it reports; `unknown` while it
is connecting, unverified or unsupported. Viewers see the state and a fixed message; the address,
the user and the error the network returned are in the dash log only, written once for each change
of state.

## What dash reads

dash opens one SSH connection per host, authenticates with its key only, and starts one command,
`/bin/sh`, without a pty. It sends a fixed POSIX `sh` script on that command's standard input; the
script is a constant in dash, and the only values put into it are the interval, how often to read
the inventory and a random marker. No setting and nothing a host sent ever reaches a shell. The
script loops: it prints raw files and command outputs in marked sections and sleeps. Every figure
is computed in dash. When dash closes the connection, the script ends at its next write.

- Every interval: the CPU lines of `/proc/stat`, `/proc/loadavg`, `/proc/meminfo`,
  `/proc/uptime`, `/proc/net/dev`, `/proc/diskstats`, the temperatures and fans of
  `/sys/class/hwmon` and the thermal zones of `/sys/class/thermal`.
- On the first sample and then about once a minute: `uname`, `/etc/os-release`, the CPU model and
  count from `/proc/cpuinfo`, the DMI vendor and product (or the device-tree model of a board),
  `systemd-detect-virt`, the whole disks of `/sys/block` with their size, model and rotation, which
  network interfaces are devices, `df -Pk` and `/proc/self/mounts`, `zpool list`, `docker ps -a` or
  `podman ps -a`, and the running and failed services of `systemctl list-units`.
- A tool the host lacks, or a file it does not let the user read, leaves its figure out; it is
  never an error. A command that could hang (`df` on a stale network mount, `zpool`, the container
  runtimes, `systemctl`) runs under `timeout` when the host has it.
- CPU, network and disk rates are counter differences between two samples over the host's own
  uptime clock; the first sample of a connection has none. A counter that goes backwards (a
  reboot, a wrap) or jumps beyond any link gives zero for that sample. The network rates sum the
  interfaces that are devices (or, where none is, every interface but loopback, bridges, tunnels
  and container veths); the disk rates sum the whole disks.
- The CPU temperature is the hottest reading of a CPU hwmon chip (`coretemp`, `k10temp`,
  `cpu_thermal`, ...), else of a CPU or SoC thermal zone. Readings outside -40 to 125 °C and fans
  at 0 RPM are left out.

The containers are optional. Listing them needs the docker socket, and membership of the `docker`
group is equivalent to root on the host: grant it only if you accept that. Without it the Hosts
page says the runtime refused the user, and everything else works. Rootless podman lists the
user's own containers.

## Limits

- At most 64 hosts; names unique ignoring case, at most 64 bytes, without `/` or control
  characters. An empty name is the host part of the address.
- `intervalSeconds` is 2 to 60 (5 by default). A frame may take the interval plus 20 seconds, or
  plus two minutes when it carries the inventory; a silent host is dropped and tried again. An idle
  connection is asked for a reply every 15 seconds.
- The handshake, with the authentication, has 15 seconds. A line may be 64 KiB and a frame 4 MiB;
  a host past either breaks the protocol and its connection is dropped.
- dash keeps at most 64 disks, 64 file systems, 32 pools, 200 containers, 100 failed units, 64
  temperature sensors and 32 fans of a host, and 256 bytes of any name.
- The history route (`GET /api/v1/plugins/ssh/hosts/{name}/history`) answers the samples of the
  last hour, oldest first, from memory: a restart of dash starts it again, a change of the
  settings keeps it, and a host removed from the settings loses it.
- The host key algorithms dash asks for are those of the trusted key's type, so a host with keys
  of several types is never taken for one whose key changed. Without a `hostKey` dash prefers the
  host's ed25519 key.
