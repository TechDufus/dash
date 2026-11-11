#!/bin/sh
# dash installer for Linux with systemd: https://dash.techdufus.com/install.sh
#
#   curl -fsSL https://dash.techdufus.com/install.sh | sudo sh
#   curl -fsSL https://dash.techdufus.com/install.sh | sudo sh -s -- --help
#
# It downloads a release archive of dash from GitHub, checks its SHA-256 against the release's
# checksums file, installs /usr/local/bin/dash and a hardened systemd unit, writes /etc/dash
# once, starts dash and prints the address to open. Running it again upgrades in place. The
# guide is docs/deploy/binary.md: https://github.com/TechDufus/dash/blob/main/docs/deploy/binary.md
#
# Everything runs inside main, called on the last line: a download cut short defines functions
# at most and runs nothing.
set -eu

releases=https://github.com/TechDufus/dash/releases
guide=https://github.com/TechDufus/dash/blob/main/docs/deploy/binary.md
# The last release without binaries: it and every older version have no archive to install.
last_release_without_binaries=0.16.1
bin=/usr/local/bin/dash
etc=/etc/dash
unit=/etc/systemd/system/dash.service
# The unit's StateDirectory=dash: with DynamicUser=yes systemd keeps it in /var/lib/private/dash
# and links /var/lib/dash to it.
data=/var/lib/dash
# LoadCredential (247) and the %d specifier (248) are the oldest features the unit uses.
min_systemd=248

usage() {
	cat <<'EOF'
dash installer: installs dash as a systemd service on Linux.

Usage:
  curl -fsSL https://dash.techdufus.com/install.sh | sudo sh
  curl -fsSL https://dash.techdufus.com/install.sh | sudo sh -s -- [options]
  sudo sh install.sh [options]

Options:
  --version X.Y.Z   install this release (default: DASH_VERSION, else the latest release)
  --public-url URL  the URL people open dash at, written to /etc/dash/dash.env on the first
                    install only (default: http://<this machine's name>:8080). Behind a
                    reverse proxy, give the proxy's https:// URL.
  --no-start        install and enable the service, but do not start or restart it
  --uninstall       stop and remove the service and the binary; /etc/dash and the data stay
  -h, --help        show this help

What it does:
  - downloads dash_<version>_linux_<amd64|arm64>.tar.gz and dash_<version>_checksums.txt
    from the GitHub release and refuses to go on unless the SHA-256 matches;
  - installs /usr/local/bin/dash (replaced atomically on an upgrade);
  - writes, only when missing, /etc/dash/dash.env (0640: DASH_SERVER_PUBLIC_URL and
    DASH_DATA_DIR) and /etc/dash/secret_key (0600: 32 random bytes, base64), the key that
    encrypts the secrets saved from the settings page. Back it up: dash cannot read those
    secrets without it.
  - installs /etc/systemd/system/dash.service (a dynamic user, the data in /var/lib/dash, the
    key handed over with LoadCredential, a read-only system), enables and starts it, waits
    until dash is ready and prints the address and the one-time setup link.

Running it again upgrades dash to the requested version and restarts it; /etc/dash and the
data are kept.

Environment:
  DASH_VERSION            the release to install, as --version
  DASH_INSTALL_BASE_URL   where the archives are, instead of the GitHub releases: the files
                          of version X.Y.Z are read from <base>/vX.Y.Z/. Needs a version.

Needs root, Linux on amd64 or arm64, systemd 248 or newer, curl or wget, tar, gzip,
sha256sum (or shasum) and base64. The guide: docs/deploy/binary.md in the repository,
https://github.com/TechDufus/dash/blob/main/docs/deploy/binary.md
EOF
}

# Colour only on a terminal.
setup_colour() {
	if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
		bold=$(printf '\033[1m') red=$(printf '\033[31m') green=$(printf '\033[32m') reset=$(printf '\033[0m')
	else
		bold='' red='' green='' reset=''
	fi
}

say() { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s\n' "$green" "$reset" "$*"; }
die() {
	printf '%sdash install: %s%s\n' "$red" "$*" "$reset" >&2
	exit 1
}

valid_version() {
	printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'
}

# fetch URL FILE: downloads URL into FILE with curl or wget; https stays https on redirects.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		case $1 in
		https://*) curl -fsSL --retry 3 --proto '=https' --proto-redir '=https' -o "$2" "$1" ;;
		*) curl -fsSL --retry 3 -o "$2" "$1" ;;
		esac
	else
		wget -q -O "$2" "$1"
	fi
}

# latest_version: the newest release's X.Y.Z, from where github.com's releases/latest redirects
# (.../releases/tag/vX.Y.Z); no API call and no token.
latest_version() {
	if command -v curl >/dev/null 2>&1; then
		url=$(curl -fsSLI --retry 3 --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "$releases/latest") ||
			die "could not reach $releases/latest; give a version with --version X.Y.Z"
	else
		url=$(wget -S --spider "$releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r') ||
			die "could not reach $releases/latest; give a version with --version X.Y.Z"
	fi
	case $url in
	*/releases/tag/v*) printf '%s\n' "${url##*/releases/tag/v}" ;;
	*) die "could not find the latest release at $releases/latest; give a version with --version X.Y.Z" ;;
	esac
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

need() {
	for cmd in "$@"; do
		command -v "$cmd" >/dev/null 2>&1 || die "needs $cmd, which is not installed"
	done
}

check_system() {
	[ "$(uname -s)" = Linux ] ||
		die "this installer is for Linux with systemd. On $(uname -s), run the container image or a release binary: $guide"
	{ [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; } ||
		die "systemd is not running here; this installer needs it. Run the container image or the binary by hand: $guide"
	systemd=$(systemctl --version | sed -n '1s/^systemd \([0-9]*\).*/\1/p')
	[ -n "$systemd" ] && [ "$systemd" -ge "$min_systemd" ] ||
		die "needs systemd $min_systemd or newer (this is ${systemd:-unknown}). Run the container image instead: $guide"
	[ "$(id -u)" -eq 0 ] ||
		die "needs root. Run it again with sudo: curl -fsSL https://dash.techdufus.com/install.sh | sudo sh"
}

detect_arch() {
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "no dash build for $(uname -m); releases have linux/amd64 and linux/arm64" ;;
	esac
}

# install_binary VERSION: downloads, verifies and installs the binary.
install_binary() {
	version=$1
	archive=dash_${version}_linux_${arch}.tar.gz
	sums=dash_${version}_checksums.txt
	base=${DASH_INSTALL_BASE_URL:-$releases/download}
	base=${base%/}/v$version
	step "downloading dash $version for linux/$arch"
	fetch "$base/$sums" "$tmp/$sums" ||
		die "could not download $base/$sums. Release binaries exist for releases after v$last_release_without_binaries: an older version has none; install a newer one or run the container image ($guide)"
	want=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "$tmp/$sums")
	[ -n "$want" ] ||
		die "release v$version has no archive for linux/$arch. Release binaries exist for releases after v$last_release_without_binaries; see $guide"
	fetch "$base/$archive" "$tmp/$archive" || die "could not download $base/$archive"
	got=$(sha256_of "$tmp/$archive")
	[ "$got" = "$want" ] ||
		die "the SHA-256 of $archive does not match $sums (got $got, want $want); nothing was installed"
	say "    sha256 $got verified"
	mkdir "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x" dash || die "could not unpack $archive"
	# Copied next to the target and renamed over it: the swap is atomic, and a running dash keeps
	# its old file until it restarts.
	install -d -m 0755 "$(dirname "$bin")"
	cp "$tmp/x/dash" "$bin.new.$$"
	chmod 0755 "$bin.new.$$"
	mv -f "$bin.new.$$" "$bin"
	step "installed $bin ($version)"
}

default_public_url() {
	name=$(hostname -f 2>/dev/null || true)
	case $name in
	"" | localhost | localhost.*) name=$(hostname 2>/dev/null || uname -n) ;;
	esac
	printf 'http://%s:8080\n' "$name"
}

write_config() {
	install -d -m 0755 "$etc"
	if [ -e "$etc/dash.env" ]; then
		say "    kept $etc/dash.env"
		[ -z "$public_url" ] || say "    (--public-url is only written on the first install; edit $etc/dash.env to change it)"
	else
		url=${public_url:-$(default_public_url)}
		(
			umask 027
			cat >"$etc/dash.env.new" <<EOF
# dash's settings for dash.service (systemd EnvironmentFile syntax). The installer wrote this
# file once and never rewrites it. Edit it, then run: systemctl restart dash
# Every variable is listed in docs/configuration.md. The secret key is not set here: the unit
# hands $etc/secret_key over as DASH_SECRET_KEY_FILE.

# The URL people type, with scheme and host; dash answers only that host. Behind a reverse
# proxy, the proxy's https:// URL.
DASH_SERVER_PUBLIC_URL=$url
# The unit's state directory: settings, accounts, the audit log.
DASH_DATA_DIR=$data
EOF
		)
		chmod 0640 "$etc/dash.env.new"
		mv "$etc/dash.env.new" "$etc/dash.env"
		say "    wrote $etc/dash.env (DASH_SERVER_PUBLIC_URL=$url)"
	fi
	if [ -e "$etc/secret_key" ]; then
		say "    kept $etc/secret_key"
	else
		(
			umask 077
			head -c 32 /dev/urandom | base64 | tr -d '\n' >"$etc/secret_key.new"
			printf '\n' >>"$etc/secret_key.new"
		)
		chmod 0600 "$etc/secret_key.new"
		mv "$etc/secret_key.new" "$etc/secret_key"
		say "    wrote $etc/secret_key: back it up, dash cannot read the secrets it encrypts without it"
	fi
}

write_unit() {
	cat >"$unit.new" <<EOF
# Installed by the dash installer (https://dash.techdufus.com/install.sh), which rewrites it
# on every run. Put your changes in a drop-in: systemctl edit dash
[Unit]
Description=dash, a dashboard for a homelab
Documentation=$guide
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
ExecStart=$bin
EnvironmentFile=$etc/dash.env
# The key reaches dash as a credential: a copy only this service can read, in %d.
LoadCredential=secret_key:$etc/secret_key
Environment=DASH_SECRET_KEY_FILE=%d/secret_key
Restart=on-failure
RestartSec=5s

# A user that exists while dash runs; its state directory is $data.
DynamicUser=yes
StateDirectory=dash
StateDirectoryMode=0700
UMask=0077

# dash needs its state directory, the network and nothing else of the system.
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
PrivateUsers=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
RemoveIPC=yes
KeyringMode=private
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
EOF
	chmod 0644 "$unit.new"
	mv "$unit.new" "$unit"
	systemctl daemon-reload
}

# healthcheck: `dash healthcheck` with the DASH_* lines of dash.env as its only environment, so
# that it asks the health listener the service uses.
healthcheck() {
	set --
	while IFS= read -r line || [ -n "$line" ]; do
		case $line in
		DASH_*=*)
			value=${line#*=}
			case $value in \"*\") value=${value#\"} value=${value%\"} ;; esac
			set -- "$@" "${line%%=*}=$value"
			;;
		esac
	done <"$etc/dash.env"
	env -i "$@" "$bin" healthcheck >/dev/null 2>&1
}

# setup_url: the one-time setup link from this start's log, if dash printed one (it does while
# no account exists). Only that line is read from the journal.
setup_url() {
	id=$(systemctl show -p InvocationID --value dash.service)
	[ -n "$id" ] || return 0
	journalctl --no-pager -o cat "_SYSTEMD_INVOCATION_ID=$id" 2>/dev/null |
		sed -n 's/.*"msg":"setup token required[^"]*".*"url":"\([^"]*\)".*/\1/p' | tail -n 1
}

start_service() {
	if systemctl is-active --quiet dash.service; then
		step "restarting dash"
		systemctl restart dash.service
	else
		step "starting dash"
		systemctl enable --now --quiet dash.service
	fi
	# /readyz answers once the plugins dash waits for have started (server.readyTimeout, 60 s).
	i=0
	until healthcheck; do
		if systemctl is-failed --quiet dash.service || [ "$i" -ge 90 ]; then
			die "dash did not become ready. See why with: journalctl -u dash -n 50 --no-pager"
		fi
		i=$((i + 1))
		sleep 1
	done
	url=''
	i=0
	while [ -z "$url" ] && [ "$i" -lt 5 ]; do
		url=$(setup_url)
		[ -n "$url" ] || { i=$((i + 1)) && sleep 1; }
	done
	public=$(sed -n 's/^DASH_SERVER_PUBLIC_URL=//p' "$etc/dash.env" | tail -n 1 | tr -d '"')
	say ""
	say "${bold}dash $version is running.${reset}"
	if [ -n "$url" ]; then
		say "Open this link within an hour to choose the administrator's password:"
		say "  ${bold}$url${reset}"
	else
		say "Open ${bold}$public${reset} and sign in."
	fi
}

uninstall() {
	step "removing dash"
	if [ -e "$unit" ]; then
		systemctl disable --now dash.service >/dev/null 2>&1 || true
		rm -f "$unit"
		systemctl daemon-reload
		systemctl reset-failed dash.service >/dev/null 2>&1 || true
		say "    removed $unit"
	fi
	if [ -e "$bin" ]; then
		rm -f "$bin"
		say "    removed $bin"
	fi
	say "Left in place: $etc (dash.env and secret_key) and the data in /var/lib/private/dash."
	say "Remove them too to forget every setting and account: rm -rf $etc /var/lib/private/dash"
}

main() {
	setup_colour
	version=${DASH_VERSION:-}
	public_url=''
	start=1
	action=install
	while [ $# -gt 0 ]; do
		case $1 in
		--version)
			[ $# -ge 2 ] || die "--version needs X.Y.Z"
			version=$2
			shift
			;;
		--version=*) version=${1#*=} ;;
		--public-url)
			[ $# -ge 2 ] || die "--public-url needs a URL"
			public_url=$2
			shift
			;;
		--public-url=*) public_url=${1#*=} ;;
		--no-start) start='' ;;
		--uninstall) action=uninstall ;;
		-h | --help)
			usage
			return 0
			;;
		*) die "unknown option $1 (see --help)" ;;
		esac
		shift
	done

	check_system
	if [ "$action" = uninstall ]; then
		uninstall
		return 0
	fi

	need tar gzip base64 head install awk sed
	command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || die "needs curl or wget"
	command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || die "needs sha256sum or shasum"
	detect_arch
	case $public_url in
	'' | http://?* | https://?*) ;;
	*) die "--public-url must start with http:// or https://" ;;
	esac
	version=${version#v}
	if [ -z "$version" ]; then
		[ -z "${DASH_INSTALL_BASE_URL:-}" ] || die "DASH_INSTALL_BASE_URL needs a version: --version X.Y.Z or DASH_VERSION"
		version=$(latest_version)
	fi
	valid_version "$version" || die "not a version: $version (want X.Y.Z)"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	install_binary "$version"
	write_config
	write_unit
	step "installed $unit"
	if [ -z "$start" ]; then
		systemctl enable --quiet dash.service
		say ""
		say "dash $version is installed and enabled but not started (--no-start)."
		say "Check $etc/dash.env, then run: systemctl restart dash"
		return 0
	fi
	start_service
}

main "$@"
