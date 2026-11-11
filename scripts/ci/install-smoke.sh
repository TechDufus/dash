#!/usr/bin/env bash
# Smoke test of the installer (site/install.sh, docs/deploy/binary.md) on a machine with systemd:
# it serves release archives of two made-up versions over loopback HTTP and points the installer
# at them with DASH_INSTALL_BASE_URL. It checks that the first install starts an active
# dash.service that answers its health endpoint and prints the setup link; that a second run
# upgrades the binary and restarts dash while /etc/dash/secret_key stays as it was; that a
# checksum mismatch and a release without this platform's archive are refused and leave the
# installed binary alone; and that --uninstall leaves no unit and no binary. It also runs
# `systemd-analyze verify` on the unit and prints its `systemd-analyze security` exposure.
#
#   scripts/ci/install-smoke.sh               # CI: builds, then installs on this machine (sudo)
#   scripts/ci/install-smoke.sh --dist DIR    # installs the archives build-binaries.sh put in DIR
#   scripts/ci/install-smoke.sh --docker      # locally: builds here, installs in a systemd container
#
# Without --dist it builds both versions for this machine's platform with
# scripts/release/build-binaries.sh as the user that runs it (Go, and npm when web/dist is
# missing) and runs the installer and the checks as root, through sudo unless it is root already.
# The machine must have no dash installed: it refuses to start when /etc/dash or the unit exists,
# and on exit it uninstalls and removes /etc/dash and dash's data. --docker never installs
# anything on this machine: it starts a privileged Debian container with systemd as PID 1, the
# installer, this script and the archives mounted read-only, runs this script there with --dist,
# and removes the container and its image on exit. Needs bash, curl and python3 (the HTTP
# server); --docker needs docker.
set -euo pipefail
# A failure inside $(…) stops the script too. That needs bash 4.4 or newer.
shopt -s inherit_errexit

root=$(cd "$(dirname "$0")/../.." && pwd)
# Made-up versions, never a release: the first install, the upgrade, and two refused ones.
v1=0.0.0-smoke.1
v2=0.0.0-smoke.2
v3=0.0.0-smoke.3
v4=0.0.0-smoke.4
# Debian 13 with systemd for --docker, pinned by digest.
base_image=debian:trixie@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6

say() { printf '%s install-smoke: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() {
	say "FAIL: $*"
	exit 1
}

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no dash build for $(uname -m)" ;;
esac

build() {
	local out=$1
	say "building $v1 and $v2 for linux/$arch"
	"$root/scripts/release/build-binaries.sh" --platform "linux/$arch" "$v1" "$out"
	"$root/scripts/release/build-binaries.sh" --platform "linux/$arch" "$v2" "$out"
}

# --docker: the whole test inside a throwaway systemd container.
in_docker() {
	local name=dash-install-smoke-$$-$RANDOM work
	local image=$name
	work=$(mktemp -d)
	# shellcheck disable=SC2064 # the names are fixed now
	trap "docker rm -f $name >/dev/null 2>&1 || true; docker image rm $image >/dev/null 2>&1 || true; rm -rf '$work'" EXIT
	build "$work/dist"
	say "building the systemd image $image"
	docker build --quiet --tag "$image" - >/dev/null <<EOF
FROM $base_image
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates python3 \
 && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
# systemd makes / shared on a host but not in a container, and without that the credentials it
# mounts for LoadCredential= never reach the service's mount namespace.
CMD ["/bin/sh", "-c", "mount --make-rshared / && exec /sbin/init"]
EOF
	# The container gets a read-only copy of the two scripts it runs, in the checkout's layout:
	# a checkout on a FUSE file system (or a remote Docker host) cannot be bind-mounted.
	mkdir -p "$work/src/site" "$work/src/scripts/ci"
	cp "$root/site/install.sh" "$work/src/site/"
	cp "$root/scripts/ci/install-smoke.sh" "$work/src/scripts/ci/"
	docker run -d --name "$name" --privileged --cgroupns=private \
		--tmpfs /run --tmpfs /run/lock \
		-v "$work/src:/src:ro" -v "$work/dist:/dist:ro" "$image" >/dev/null
	# The bus appears a moment after the container starts; then --wait blocks until boot ends.
	local state=''
	for _ in {1..30}; do
		state=$(docker exec "$name" systemctl is-system-running --wait 2>/dev/null || true)
		[[ -z $state ]] || break
		sleep 1
	done
	[[ -n $state ]] || fail "systemd did not boot in the container"
	say "container $name booted systemd ($state)"
	docker exec "$name" /src/scripts/ci/install-smoke.sh --dist /dist
}

dist=
case ${1:-} in
--docker)
	in_docker
	exit
	;;
--dist)
	[[ -n ${2:-} ]] || fail "--dist needs a directory"
	dist=$(cd "$2" && pwd)
	;;
'') ;;
*) fail "usage: install-smoke.sh [--dist DIR | --docker]" ;;
esac

as_root() {
	if ((EUID == 0)); then "$@"; else sudo "$@"; fi
}

[[ -d /run/systemd/system ]] || fail "systemd is not running here; use --docker"
if as_root test -e /etc/dash || as_root test -e /etc/systemd/system/dash.service; then
	fail "dash is installed on this machine already; this test would replace it (use --docker)"
fi

work=$(mktemp -d)
server=
installed=
cleanup() {
	local status=$?
	[[ -z $server ]] || kill "$server" 2>/dev/null || true
	if [[ -n $installed ]]; then
		((status == 0)) || {
			say "dash's journal:"
			as_root journalctl -u dash.service -n 60 --no-pager -o cat || true
		}
		as_root sh "$root/site/install.sh" --uninstall >/dev/null 2>&1 || true
		as_root rm -rf /etc/dash /var/lib/private/dash /var/lib/dash
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

if [[ -z $dist ]]; then
	dist=$work/dist
	build "$dist"
fi

# The release layout the installer reads: <base>/v<version>/<file>.
serve=$work/serve
release() {
	local version=$1
	mkdir -p "$serve/v$version"
	cp "$dist/dash_${version}_linux_${arch}.tar.gz" "$dist/dash_${version}_checksums.txt" "$serve/v$version/"
}
release "$v1"
release "$v2"
# v3: the archive of v2 under another name with a checksum that does not match it.
mkdir -p "$serve/v$v3"
cp "$dist/dash_${v2}_linux_${arch}.tar.gz" "$serve/v$v3/dash_${v3}_linux_${arch}.tar.gz"
printf '%064d  %s\n' 0 "dash_${v3}_linux_${arch}.tar.gz" >"$serve/v$v3/dash_${v3}_checksums.txt"
# v4: a release whose checksums list no archive for this platform.
mkdir -p "$serve/v$v4"
printf '%064d  %s\n' 0 "dash_${v4}_darwin_arm64.tar.gz" >"$serve/v$v4/dash_${v4}_checksums.txt"

for _ in {1..20}; do
	port=$((20000 + RANDOM % 40000))
	(exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null || break
done
python3 -m http.server --bind 127.0.0.1 --directory "$serve" "$port" >"$work/http.log" 2>&1 &
server=$!
for _ in {1..50}; do
	curl -fsS -o /dev/null "http://127.0.0.1:$port/v$v1/dash_${v1}_checksums.txt" 2>/dev/null && break
	sleep 0.1
done
say "serving the releases on http://127.0.0.1:$port"

public=http://localhost:8080
installer() {
	as_root env DASH_INSTALL_BASE_URL="http://127.0.0.1:$port" sh "$root/site/install.sh" "$@"
}
binary_sha() { tar -xzOf "$dist/dash_${1}_linux_${arch}.tar.gz" dash | sha256sum | cut -d' ' -f1; }
installed_sha() { as_root sha256sum /usr/local/bin/dash | cut -d' ' -f1; }
check_running() {
	local version=$1 out=$2
	as_root systemctl is-active --quiet dash.service || fail "dash.service is not active after installing $version"
	curl -fsS http://127.0.0.1:8081/readyz >/dev/null || fail "the health endpoint does not answer after installing $version"
	curl -fsS -o /dev/null -H "Host: localhost:8080" http://127.0.0.1:8080/ || fail "dash does not serve its UI at $public"
	[[ $(installed_sha) == "$(binary_sha "$version")" ]] || fail "/usr/local/bin/dash is not the binary of $version"
	grep -q "dash $version is running" <<<"$out" || fail "the installer did not report $version running"
	grep -Eq "^  $public/setup#token=[A-Za-z0-9_-]+\$" <<<"$out" || fail "the installer printed no setup link"
}

say "first install: $v1"
installed=1
out=$(installer --version "$v1" --public-url "$public")
printf '%s\n' "$out"
check_running "$v1" "$out"
[[ $(as_root stat -c '%a %U' /etc/dash/secret_key) == "600 root" ]] || fail "/etc/dash/secret_key is not 0600 root"
[[ $(as_root stat -c '%a %U' /etc/dash/dash.env) == "640 root" ]] || fail "/etc/dash/dash.env is not 0640 root"
as_root grep -qx "DASH_SERVER_PUBLIC_URL=$public" /etc/dash/dash.env || fail "dash.env does not hold the public URL"
[[ $(as_root base64 -d /etc/dash/secret_key | wc -c) -eq 32 ]] || fail "the secret key is not 32 bytes of base64"
key=$(as_root sha256sum /etc/dash/secret_key)
env_sha=$(as_root sha256sum /etc/dash/dash.env)
invocation=$(as_root systemctl show -p InvocationID --value dash.service)

say "the unit: systemd-analyze verify and security"
verify=$(as_root systemd-analyze verify /etc/systemd/system/dash.service 2>&1) || fail "systemd-analyze verify: $verify"
[[ -z $verify ]] || say "systemd-analyze verify said: $verify"
exposure=$(as_root systemd-analyze security --no-pager dash.service | tail -n 1)
say "$exposure"

say "upgrade: $v2"
out=$(installer --version "$v2" --public-url http://ignored.example.com:8080)
printf '%s\n' "$out"
check_running "$v2" "$out"
[[ $(as_root systemctl show -p InvocationID --value dash.service) != "$invocation" ]] || fail "the upgrade did not restart dash"
[[ $(as_root sha256sum /etc/dash/secret_key) == "$key" ]] || fail "the upgrade changed /etc/dash/secret_key"
[[ $(as_root sha256sum /etc/dash/dash.env) == "$env_sha" ]] || fail "the upgrade changed /etc/dash/dash.env"
as_root test -d /var/lib/private/dash || fail "the data directory is gone"

say "refused: a checksum that does not match ($v3)"
if out=$(installer --version "$v3" 2>&1); then fail "the installer accepted a checksum mismatch: $out"; fi
printf '%s\n' "$out"
grep -q 'does not match' <<<"$out" || fail "no checksum message"
[[ $(installed_sha) == "$(binary_sha "$v2")" ]] || fail "a refused download replaced the binary"

say "refused: a release without this platform's archive ($v4)"
if out=$(installer --version "$v4" 2>&1); then fail "the installer accepted a release without the archive: $out"; fi
printf '%s\n' "$out"
grep -q "has no archive for linux/$arch" <<<"$out" || fail "no missing-archive message"
as_root systemctl is-active --quiet dash.service || fail "a refused install stopped dash"

say "--uninstall"
out=$(installer --uninstall)
printf '%s\n' "$out"
as_root test ! -e /etc/systemd/system/dash.service || fail "the unit file is still there"
as_root test ! -e /usr/local/bin/dash || fail "the binary is still there"
[[ -z $(as_root systemctl list-unit-files --no-legend dash.service) ]] || fail "systemd still knows dash.service"
as_root systemctl is-active --quiet dash.service && fail "dash.service is still active"
as_root test -e /etc/dash/secret_key || fail "--uninstall removed /etc/dash/secret_key"
say "PASS ($exposure)"
