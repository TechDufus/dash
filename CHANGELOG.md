# Changelog

The release notes of dash, newest first. CI publishes the section of a version as its GitHub
release when the tag is pushed (docs/development.md, Release): the `## vX.Y.Z: headline` heading
is the title of the release and the text below it, up to the next version, its notes. Notes for
work that is not released yet can wait under a `## Unreleased` heading, which nothing publishes.

The releases before v0.16.1 have their notes on
[GitHub](https://github.com/TechDufus/dash/releases) only.

## v0.17.0: hosts over SSH, an installer and a live demo

Linux hosts over SSH with nothing installed on them, binaries and a one-line installer for servers without Docker, and a live demo that runs in the browser. Nothing changes for an existing installation until you add a host.

### Added

- **SSH hosts:** add any Linux machine by its address and a user in Settings → SSH hosts. dash signs in with its own key, runs one fixed read-only script and shows the machine on the new Hosts page: live CPU, memory, network and disk charts, temperatures and fans, file systems, disks and ZFS pools, containers (docker or podman) and systemd services. A full file system, a failed unit, a pool that is not online and a host that went away become attention items on the Overview. Nothing is installed on the host, dash holds no password and never writes there ([the SSH hosts page](https://github.com/TechDufus/dash/blob/main/docs/plugins/ssh.md)).
- **Pinned host keys:** dash connects to a host only after an administrator has trusted the fingerprint it shows, and refuses a host whose key changed. Hosts set in the config file carry their key as `hostKey`.
- **Release binaries:** every release now has `dash_<version>_<os>_<arch>.tar.gz` for Linux and macOS on amd64 and arm64, a checksum file and a build provenance attestation.
- **Installer:** `curl -fsSL https://dash.techdufus.com/install.sh | sudo sh` installs the binary as a hardened systemd service, creates its secret key and prints the setup link. Run it again to upgrade ([the binary guide](https://github.com/TechDufus/dash/blob/main/docs/deploy/binary.md)).
- **Live demo:** [the live demo](https://dash.techdufus.com/demo/) is the real web UI on recorded demo data, hosted as static files. It is read-only; the container's demo mode still answers actions.
- **Getting started:** an Overview with nothing connected yet points administrators at adding a host.

### Security

- **SSH hosts** need `DASH_SECRET_KEY` and a writable data directory: dash's SSH private key is sealed with it in `credentials.yaml`. Give dash a dedicated unprivileged user on each host and add its key with `restrict`. [The security model](https://github.com/TechDufus/dash/blob/main/docs/security.md#hosts-over-ssh) and [ADR 0003](https://github.com/TechDufus/dash/blob/main/docs/adr/0003-ssh-hosts-binaries-static-demo.md) say what dash runs, what it stores and what viewers see.

### Upgrade notes

- **No step needed.** The `ssh` plugin stays off until `plugins.ssh.hosts` names a host.
- **Rolling back** after adding hosts: remove `plugins.ssh` from the config file and unset every `DASH_PLUGINS_SSH_*` variable first, since an older release refuses a key or a variable it does not know. The older release drops dash's SSH key at its next write of `credentials.yaml`, so after upgrading again every host needs the new public key.

## v0.16.1: usage figures stay in their cards

The Hardware page's memory and root-disk figures stay inside their cards, and dash is built with Go 1.27.4 for a `net/http` security fix. A patch; nothing to change.

### Fixed

- **Hardware fleet cards:** in a narrow card, a machine's "used / total %" memory and root-disk figures ran past the card's edge. They now wrap (the percentage first, then the total) and never split a number from its unit. The same holds for every usage bar in dash.

### Security

- **Go 1.27.4:** fixes [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859), an HTTP/1 server crash from a header race in `net/http`'s `MaxBytesReader`, which dash's server and clients reach.

### Upgrade notes

- **No step needed.** Rolling back to v0.16.0 is safe, but brings the vulnerable Go back.

