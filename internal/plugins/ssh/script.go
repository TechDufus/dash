package ssh

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
)

// remoteCommand is the one command dash asks a host to run. The host's login shell runs it,
// whatever that shell is (bash, zsh, fish, csh all start a program by its path), and /bin/sh then
// reads the collector script from standard input. So the script never goes through the login
// shell, and nothing but this constant is ever part of a command line.
const remoteCommand = "/bin/sh"

// collectorScript is the program dash runs on a host: POSIX sh, for busybox ash, dash and bash.
// It prints raw /proc, /sys and a few read-only command outputs in sections and sleeps; every
// fact is parsed in Go. A frame is
//
//	@@<nonce> frame <n>
//	@@<nonce> <section>
//	<raw lines>
//	...
//	@@<nonce> end
//
// The fast sections come every frame, the inventory on the first frame and then every
// __EVERY__ frames. Every reader is optional on the host: a missing tool or an unreadable file
// leaves its section empty, and stderr goes nowhere. A command that could hang (df on a stale
// network mount, zpool, a container runtime) runs under timeout when the host has it.
//
// The whole program is one function definition and one call, so sh has read all of it before
// anything runs, and the call's standard input is /dev/null: no command can read the rest of the
// script as its input. When dash closes the connection the next write fails and sh dies of
// SIGPIPE.
//
// The only values ever substituted are the three placeholders: two integers dash computes and a
// random hex nonce (buildScript; TestScriptTakesNoConfiguredString).
const collectorScript = `dash_collect() {
M='@@__NONCE__'
I=__INTERVAL__
K=__EVERY__
dash_cat() {
	while IFS= read -r l || [ -n "$l" ]; do printf '%s\n' "$l"; done < "$1"
}
dash_run() {
	if command -v timeout >/dev/null; then timeout 20 "$@"; else "$@"; fi
}
dash_fast() {
	echo "$M stat"
	while IFS= read -r l; do case $l in cpu*) printf '%s\n' "$l";; esac; done < /proc/stat
	echo "$M loadavg"
	dash_cat /proc/loadavg
	echo "$M meminfo"
	dash_cat /proc/meminfo
	echo "$M uptime"
	dash_cat /proc/uptime
	echo "$M netdev"
	dash_cat /proc/net/dev
	echo "$M diskstats"
	dash_cat /proc/diskstats
	echo "$M hwmon"
	for d in /sys/class/hwmon/hwmon*; do
		[ -r "$d/name" ] || continue
		c=
		IFS= read -r c < "$d/name"
		[ -n "$c" ] || continue
		printf 'chip %s %s\n' "${d##*/}" "$c"
		for f in "$d"/temp*_input "$d"/fan*_input; do
			[ -r "$f" ] || continue
			v=
			IFS= read -r v < "$f"
			[ -n "$v" ] || continue
			b=${f%_input}
			lb=
			[ -r "${b}_label" ] && IFS= read -r lb < "${b}_label"
			printf '%s %s %s\n' "${b##*/}" "$v" "$lb"
		done
	done
	echo "$M thermal"
	for z in /sys/class/thermal/thermal_zone*; do
		[ -r "$z/temp" ] || continue
		v=
		IFS= read -r v < "$z/temp"
		[ -n "$v" ] || continue
		t=
		[ -r "$z/type" ] && IFS= read -r t < "$z/type"
		printf '%s %s %s\n' "${z##*/}" "$v" "$t"
	done
}
dash_containers() {
	command -v "$1" >/dev/null || return 0
	echo "$M $1"
	{ e=$(dash_run "$1" ps -a --format '{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}' 2>&1 >&3); } 3>&1
	case $e in *ermission*denied*) echo "$M $1-denied";; esac
}
dash_inventory() {
	echo "$M uname"
	uname -s
	uname -r
	uname -m
	uname -n
	echo "$M osrelease"
	if [ -r /etc/os-release ]; then dash_cat /etc/os-release; elif [ -r /usr/lib/os-release ]; then dash_cat /usr/lib/os-release; fi
	echo "$M cpuinfo"
	while IFS= read -r l; do case $l in processor*|'model name'*|Hardware*|'cpu model'*|Model*) printf '%s\n' "$l";; esac; done < /proc/cpuinfo
	echo "$M dmi"
	for f in sys_vendor product_name; do
		v=
		[ -r /sys/class/dmi/id/$f ] && IFS= read -r v < /sys/class/dmi/id/$f
		printf '%s %s\n' "$f" "$v"
	done
	echo "$M dtmodel"
	[ -r /proc/device-tree/model ] && cat /proc/device-tree/model && echo
	echo "$M virt"
	command -v systemd-detect-virt >/dev/null && systemd-detect-virt
	echo "$M block"
	for d in /sys/block/*; do
		[ -e "$d/device" ] || continue
		s=
		r=
		m=
		[ -r "$d/size" ] && IFS= read -r s < "$d/size"
		[ -r "$d/queue/rotational" ] && IFS= read -r r < "$d/queue/rotational"
		[ -r "$d/device/model" ] && IFS= read -r m < "$d/device/model"
		printf '%s %s %s %s\n' "${d##*/}" "${s:-0}" "${r:-x}" "$m"
	done
	echo "$M nic"
	for d in /sys/class/net/*; do
		[ -e "$d/device" ] && printf '%s\n' "${d##*/}"
	done
	echo "$M mounts"
	dash_cat /proc/self/mounts
	echo "$M df"
	dash_run df -Pk
	if command -v zpool >/dev/null; then
		echo "$M zpool"
		dash_run zpool list -Hp -o name,health,allocated,size
	fi
	dash_containers docker
	dash_containers podman
	if [ -d /run/systemd/system ] && command -v systemctl >/dev/null; then
		echo "$M running"
		dash_run systemctl list-units --type=service --state=running --no-legend --no-pager --plain
		echo "$M failed"
		dash_run systemctl list-units --type=service --state=failed --no-legend --no-pager --plain
	fi
}
n=0
while :; do
	echo "$M frame $n"
	dash_fast
	if [ $((n % K)) -eq 0 ]; then dash_inventory; fi
	echo "$M end"
	n=$((n + 1))
	sleep "$I"
done
}
dash_collect </dev/null 2>/dev/null
`

// nonceBytes is the size of the section marker's random part: 128 bits, so no line a host prints
// is taken for a marker by chance.
const nonceBytes = 16

// newNonce is a random hex nonce for one connection's section markers.
func newNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// buildScript is the collector script for one connection. Its parameters are integers and a
// nonce newNonce made; it has no way to take a setting, a host name or anything a host sent.
// A nonce that is not lower-case hex is replaced by zeros rather than reaching the shell.
func buildScript(intervalSeconds, inventoryEvery int, nonce string) string {
	if len(nonce) != 2*nonceBytes || strings.Trim(nonce, "0123456789abcdef") != "" {
		nonce = strings.Repeat("0", 2*nonceBytes)
	}
	return strings.NewReplacer(
		"__NONCE__", nonce,
		"__INTERVAL__", strconv.Itoa(max(1, intervalSeconds)),
		"__EVERY__", strconv.Itoa(max(1, inventoryEvery)),
	).Replace(collectorScript)
}

// inventoryEvery is how many frames apart the inventory comes: about once a minute.
func inventoryEvery(intervalSeconds int) int {
	return max(1, (60+intervalSeconds-1)/max(1, intervalSeconds))
}
