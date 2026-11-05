#!/usr/bin/env bash
# Smoke test of a PaaS-style run (docs/deploy/paas.md): the image with --read-only and no volume,
# auth.mode=local with DASH_ADMIN_PASSWORD_HASH, a plugin URL from the environment and a setting
# from a mounted config file. It checks that dash turns healthy, that the password signs in and
# that the settings API reports the store read-only for want of a data directory, with each
# value's source. Everything it creates is removed on exit.
#
#   scripts/ci/readonly-smoke.sh [image]     # default: build dash:readonly-… from the checkout
#
# Needs docker (with buildx), curl, jq and openssl.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
image=${1:-}
name=dash-readonly-$$-$RANDOM
work=$(mktemp -d)
built=
started=

say() { printf '%s readonly: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { say "FAIL: $*"; exit 1; }

cleanup() {
	status=$?
	if [[ -n $started ]]; then
		((status == 0)) || { say "dash logs:"; docker logs "$name" 2>&1 | tail -n 40 || true; }
		docker rm -f "$name" >/dev/null 2>&1 || true
	fi
	[[ -n $built ]] && docker image rm "$built" >/dev/null 2>&1 || true
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

if [[ -z $image ]]; then
	built=dash:$name
	image=$built
	say "building $image"
	docker buildx build --load --quiet --tag "$image" "$root" >/dev/null
fi

for _ in {1..20}; do
	port=$((20000 + RANDOM % 40000))
	(exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null || break
done
base=http://localhost:$port

password=$(openssl rand -hex 16)
hash=$(docker run --rm -i --network none "$image" hash-password <<<"$password")
[[ $hash == '$argon2id$'* ]] || fail "dash hash-password printed no argon2id hash"
say "password hash generated"

icons=https://icons.example.com/png
prom=http://prometheus.example.internal:9090
printf 'ui:\n  iconBaseURL: %s\n' "$icons" >"$work/config.yaml"
chmod 0644 "$work/config.yaml"

started=1
docker run -d --name "$name" --read-only --network bridge \
	-p "127.0.0.1:$port:8080" \
	-v "$work/config.yaml:/etc/dash/config.yaml:ro" \
	-e DASH_CONFIG=/etc/dash/config.yaml \
	-e DASH_SERVER_PUBLIC_URL="$base" \
	-e DASH_AUTH_MODE=local \
	-e DASH_ADMIN_PASSWORD_HASH="$hash" \
	-e DASH_PLUGINS_PROMETHEUS_URL="$prom" \
	"$image" >/dev/null
say "started $name (--read-only, no volume) on $base"

start=$SECONDS
until [[ $(docker inspect -f '{{.State.Health.Status}}' "$name") == healthy ]]; do
	[[ $(docker inspect -f '{{.State.Running}}' "$name") == true ]] || fail "dash exited"
	((SECONDS - start < 90)) || fail "not healthy within 90s"
	sleep 1
done
say "healthy after $((SECONDS - start))s"

jar=$work/cookies
api() {
	local method=$1 path=$2
	shift 2
	curl -sS --fail-with-body -b "$jar" -c "$jar" -X "$method" \
		-H "Origin: $base" -H 'X-Dash-Csrf: 1' "$@" "$base$path"
}

code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/api/v1/settings")
[[ $code == 401 ]] || fail "GET /api/v1/settings without a session = $code, want 401"
# The password goes to curl on stdin, never in an argv other processes can read (hex is JSON-safe;
# printf is a shell builtin).
printf '{"username":"admin","password":"%s"}' "$password" |
	api POST /login -H 'Content-Type: application/json' --data-binary @- >/dev/null
[[ $(api GET /api/v1/me | jq -r .role) == admin ]] || fail "the password did not sign in as admin"
say "signed in with the password"

settings=$(api GET /api/v1/settings)
jq -c '.store' <<<"$settings"
jq -e '.store.readonly == true and .store.reason == "data_dir"' <<<"$settings" >/dev/null ||
	fail "the store is not read-only for want of a data directory"
jq -c '.general[] | select(.path == "ui.iconBaseURL") | {path, value, source, ref}' <<<"$settings"
jq -e --arg v "$icons" '.general[] | select(.path == "ui.iconBaseURL") | .value == $v and .source == "file"' <<<"$settings" >/dev/null ||
	fail "ui.iconBaseURL does not come from the file"
plugin=$(api GET /api/v1/settings/plugins/prometheus)
jq -c '.fields[] | select(.path == "plugins.prometheus.url") | {path, value, source, ref}' <<<"$plugin"
jq -e --arg v "$prom" '.fields[] | select(.path == "plugins.prometheus.url") | .value == $v and .source == "env"' <<<"$plugin" >/dev/null ||
	fail "plugins.prometheus.url does not come from the environment"
say "PASS"
