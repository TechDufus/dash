#!/usr/bin/env bash
# Smoke test of examples/compose: start dash with the monitoring profile, set up the admin account
# through the setup token, enable Prometheus through the settings API, wait for the prometheus
# plugin to turn healthy and require dash's connection test against the compose Prometheus to
# pass. Everything it creates is removed on exit.
#
#   scripts/ci/compose-smoke.sh [image]     # default: build dash:smoke from the checkout
#
# Needs docker (with compose and buildx), curl, jq and openssl. PROMETHEUS_IMAGE and
# ALERTMANAGER_IMAGE may pin the monitoring images (CI pins them by digest).
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
compose_dir=$root/examples/compose
image=${1:-}
project=dash-smoke-$$-$RANDOM
work=$(mktemp -d)
built=

say() { printf '%s smoke: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { say "FAIL: $*"; exit 1; }

dc() { docker compose --project-name "$project" --project-directory "$compose_dir" -f "$compose_dir/compose.yaml" "$@"; }

cleanup() {
	status=$?
	if ((status != 0)); then
		say "dash logs (the setup token is redacted):"
		dc logs --no-color dash 2>&1 | sed -E 's/dash_setup_[A-Za-z0-9_-]+/dash_setup_[redacted]/g' | tail -n 40 || true
	fi
	dc --profile monitoring down -v --remove-orphans >/dev/null 2>&1 || true
	[[ -n $built ]] && docker image rm "$built" >/dev/null 2>&1 || true
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

if [[ -z $image ]]; then
	built=dash:smoke-$project
	image=$built
	say "building $image"
	docker buildx build --load --quiet --tag "$image" "$root" >/dev/null
fi

# A free loopback port: the runner is shared, so 8080 may be taken. A port nothing answers on.
for _ in {1..20}; do
	port=$((20000 + RANDOM % 40000))
	(exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null || break
done
base=http://localhost:$port

openssl rand -base64 32 >"$work/dash_secret_key"
chmod 0644 "$work/dash_secret_key" # read by the image's nonroot user through the bind mount
export DASH_IMAGE=$image DASH_PORT=$port DASH_SECRET_KEY_PATH=$work/dash_secret_key

say "starting project $project (dash $image on $base)"
dc --profile monitoring up -d --quiet-pull

# Polls run cmd every second until it succeeds or the bound passes.
poll() {
	local bound=$1 what=$2
	shift 2
	local deadline=$((SECONDS + bound))
	until "$@"; do
		((SECONDS < deadline)) || fail "$what not reached within ${bound}s"
		sleep 1
	done
	say "$what after $((SECONDS - start))s"
}

healthy() { [[ $(docker inspect -f '{{.State.Health.Status}}' "$(dc ps -q dash)") == healthy ]]; }
start=$SECONDS
poll 90 "dash healthy" healthy

token=$(dc logs --no-color dash 2>&1 | grep -o 'dash_setup_[A-Za-z0-9_-]*' | head -n 1 || true)
[[ -n $token ]] || fail "no setup token in the logs"
say "setup token found in the logs"

jar=$work/cookies
api() { # api METHOD PATH [curl args...]: the session cookie, the Origin and the CSRF header
	local method=$1 path=$2
	shift 2
	curl -sS --fail-with-body -b "$jar" -c "$jar" -X "$method" \
		-H "Origin: $base" -H 'X-Dash-Csrf: 1' "$@" "$base$path"
}

# The token and the password go to curl on stdin, never in an argv other processes can read; both
# are JSON-safe (base64url and hex), and printf is a shell builtin.
password=$(openssl rand -hex 16)
admin=$(printf '{"token":"%s","password":"%s"}' "$token" "$password" |
	api POST /setup -H 'Content-Type: application/json' --data-binary @- | jq -r .adminUser)
say "setup created the account $admin"
[[ $(api GET /api/v1/me | jq -r .role) == admin ]] || fail "the setup session is not an admin"

rev=$(api GET /api/v1/settings | jq -r .revision)
say "settings revision $rev; enabling prometheus"
api PUT /api/v1/settings/plugins/prometheus -H 'Content-Type: application/merge-patch+json' \
	-H "If-Match: \"$rev\"" --data '{"url":"http://prometheus:9090"}' | jq -c .

state() { api GET /api/v1/plugins | jq -r '.[] | select(.id == "prometheus") | .state'; }
start=$SECONDS
poll 60 "prometheus plugin healthy" eval '[[ $(state) == healthy ]]'
# The prometheus plugin is healthy once its client is built (it never polls), so ask dash to
# query the compose Prometheus through the saved settings.
checks=$(api POST /api/v1/settings/plugins/prometheus/test -H 'Content-Type: application/json' --data '{}')
jq -c '.checks[] | {name, ok, category, message}' <<<"$checks"
jq -e '(.checks | length) > 0 and ([.checks[].ok] | all)' <<<"$checks" >/dev/null || fail "dash cannot query the compose Prometheus"
say "PASS"
