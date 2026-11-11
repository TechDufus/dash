# ADR 0003: Hosts over SSH, release binaries and the static demo

- Status: Accepted
- Date: 2026-11-11
- Deciders: the owner, by merging the pull request that adds the `ssh` plugin

## Context

Until 0.16 dash could show a machine only through other systems: a Proxmox API, a Kubernetes API, or node-exporter and IPMI series in a Prometheus-compatible store (the hardware plugin). A homelab that has none of them, or a box that sits outside them, had no way in. Running dash itself needed Docker or a cluster, and seeing it at all needed a clone and a build.

Three additions answer that: a plugin that reads Linux hosts over SSH, binaries and an installer for machines without Docker, and a demo that runs in a browser from static files. Each crosses a line the earlier ADRs drew (what dash connects to, what it stores, what the project hosts), so the decisions are recorded here. They continue ADR 0001 (plugins, configuration, release) and ADR 0002 (S.17: no project infrastructure), and change none of their decisions. The decisions are numbered T.1 to T.9.

## Decisions

### T.1 Hosts are read over SSH, and nothing is installed on them

**Decision.** The `ssh` plugin opens one SSH connection per configured host and runs one command in one session without a terminal: a POSIX `sh` loop that prints the raw text of `/proc`, `/sys` and a few read-only commands (`df`, `zpool list`, `docker ps`, `systemctl list-units`) in marked sections, then sleeps. dash parses that text and computes every rate itself. Nothing is copied to the host, and when dash closes the connection the loop ends at its next write. Only Linux is read; another kernel is reported as unsupported.

**Alternatives considered.**
- An agent on each host that dash talks to: it has to be installed, upgraded and secured on every machine, it needs a port or a registration channel, and it is a second program to build, sign and trust. That is the opposite of "one Go binary".
- node-exporter and a Prometheus-compatible store, as the hardware plugin's machines use: right for a lab that already runs them, and it stays. It is a monitoring stack to run before the first number appears.
- Copying dash's own binary to the host and running it over the session: one build per architecture to carry, and a write to the host on every upgrade.
- Parsing on the host with `awk` and friends: the tools differ between busybox, GNU and BSD userlands, and their failures would surface on someone's host instead of in a test.

**Consequences.** Adding a host needs an address, a user and one line in that user's `authorized_keys`. What dash discovers is bounded by what that user may read: containers need access to the runtime, SMART data and power draw need root and are not read. Parsing is ordinary Go with fixtures of real hosts. A host costs one idle TCP connection and a shell that wakes every few seconds.

### T.2 The remote command is a constant

**Decision.** The script is a constant in dash's source. The only values put into it are numbers dash computes (the sampling interval and the inventory period) and a random hexadecimal marker dash generates for each session. No setting, no host name and nothing a host sends ever becomes part of a command. dash has no way to run anything else on a host: there is no action, no route and no setting that takes a command.

**Alternatives considered.**
- Configurable extra commands or checks: every such setting is remote code execution by configuration, and a settings write would then be worth a shell on every host.
- A terminal in the browser: useful, and a different product. Proxmox and Headlamp links already lead to the consoles that exist.

**Consequences.** A new fact to read means a change to dash and a release. The plugin has no actions, so `plugins.ssh` has no `actions` section and nothing for an agent to call.

### T.3 dash only reads, holds no password and never asks for more privilege

**Decision.** dash signs in with a public key only. It never stores or prompts for a password, never runs `sudo`, and never writes to a host, including to install its own key: the administrator adds that line. The documented setup is a dedicated unprivileged user and the `restrict` option on the key, which turns off forwarding and terminals.

**Alternatives considered.**
- Take a password once and install the key with it: one step fewer for the user, at the price of a password passing through dash and of dash writing to `authorized_keys` on a machine it does not own.
- Password sign-in as a stored secret: a reusable credential for every host in one file.

**Consequences.** The security model keeps its sentence that dash writes in three places only. The first connection to a host needs someone to paste one line there.

### T.4 One identity, sealed with the secret key

**Decision.** dash has one ed25519 key pair for all hosts. It is generated at first need and its private half is stored in `credentials.yaml` sealed with `DASH_SECRET_KEY`, as the VAPID key of ADR 0002 is. Without the secret key or without a writable data directory the plugin does not start and says what is missing. dash still never generates the secret key itself.

**Alternatives considered.**
- A private key the administrator uploads: secrets cannot live inside a list setting, and the path of least effort would be a personal key that opens far more than dash should.
- A key per host: the keys would sit side by side in the same file, so one leak is all of them either way.
- An unsealed key file in the data directory, as an SSH client keeps: the one credential that opens a shell on every host would be the least protected thing in the volume.
- Generating the secret key when there is none: rejected in the design of ADR 0001 and unchanged here. A key that lives beside what it protects protects nothing.

**Consequences.** Losing the secret key means authorizing a new public key on every host. Rotating it (`DASH_SECRET_KEY_PREVIOUS`) re-seals the identity like the other stored secrets. The installer and the Compose example create the key, so the two shortest ways to run dash have it.

### T.5 Host keys are pinned, and trusting one is a recorded act

**Decision.** Each host's public key is part of its settings (`plugins.ssh.hosts[].hostKey`). For a host without one dash reads the key the host presents, ends the handshake before it authenticates, and shows the fingerprint; an administrator trusts it, which is a settings change with its audit record. A host that presents another key than the pinned one is refused until an administrator replaces the pin.

**Alternatives considered.**
- Accept the first key silently: whoever answers first is believed, and nobody is asked.
- A `known_hosts` file in the data directory: no view of it in the UI, no audit record, and a second place where configuration lives.
- Host certificates: a fine addition for labs that run an SSH CA; nothing here prevents it later.

**Consequences.** Hosts set in a config file carry their key there (`ssh-keyscan` prints it); the settings page shows the line to add. Reinstalling a host takes one confirmation.

### T.6 The same dial guard as the HTTP plugins

**Decision.** The plugin dials through the guard the HTTP plugins use: link-local addresses and the cloud metadata address are always refused, and an address saved from the settings page may not reach loopback. Private addresses are allowed, since that is where homelab hosts are. The environment's proxy is not used.

**Alternatives considered.**
- No guard, as an SSH client has none: the settings page would become a way to make dash connect to dash's own node.
- The global-addresses-only guard of the OAuth and push clients: it would refuse every host in a homelab.

**Consequences.** `plugin.Host` gains a guarded dial for plugins that do not speak HTTP.

### T.7 Viewers see no connection details, and history stays in memory

**Decision.** The `ssh.hosts` topic carries names, states, fixed messages, fingerprints, inventory and metrics. Addresses, users and the text of network errors stay in the settings API and in dash's log. dash keeps an hour of samples per host in memory for the page's charts and writes none of it to disk.

**Alternatives considered.**
- A time-series store in the data directory: retention, compaction and disk use would become dash's problems. A lab that wants weeks of history runs a Prometheus-compatible store, and the hardware plugin reads it.
- Showing the address to viewers: it helps nobody who cannot change it.

**Consequences.** Charts start empty after a restart of dash. The data directory holds no measurement data.

### T.8 Release binaries and an installer; the installer makes the key

**Decision.** Every release carries archives of the `dash` binary for Linux and macOS on amd64 and arm64 with a checksum file and a build provenance attestation. `install.sh`, published with the landing page, installs the Linux binary as a hardened systemd service. The installer generates `/etc/dash/secret_key`; dash is handed it as a systemd credential.

**Alternatives considered.**
- Distribution packages and repositories: a signing key and a repository per family to run, for one binary.
- Homebrew and similar: nothing prevents them later; they are not where a homelab server gets its software.
- No installer, only documented steps: the steps stay in the docs for people who will not pipe a script into a shell.

**Consequences.** A release is not complete without its binaries: the image is not pushed for a version whose binaries do not build. The installer is tested in CI against a real systemd.

### T.9 The public demo is a recording, played by the real web UI

**Decision.** The demo on the project's Pages site is the production SPA built for a sub-path, with its two network entry points replaced by a player of recorded data: every read answers from a file recorded from the real binary in demo mode, the event stream replays recorded frames in a loop, and every write answers that the demo is read-only. The recording is made by crawling the real UI, so it holds what the UI asks for, and a second crawl of the static build fails on anything missing.

**Alternatives considered.**
- A demo server the project runs: project infrastructure, which ADR 0002 S.17 rules out, and a public service that accepts writes.
- The Go backend compiled to WebAssembly: the Kubernetes client alone makes it tens of megabytes, slow to start on a phone.
- Hand-written fixtures in the web code: a second fake homelab that drifts from `internal/demo/world`.

**Consequences.** The demo shows exactly the recorded moments: a filter or a time range nobody recorded answers "not part of the recording". The page's clock follows the recording. The container (`DASH_DEMO=true`) remains the demo in which actions answer.

## References

- [../security.md](../security.md) (Hosts over SSH) and [../plugins/ssh.md](../plugins/ssh.md).
- [../deploy/binary.md](../deploy/binary.md).
- [../development.md](../development.md) (Static demo).
