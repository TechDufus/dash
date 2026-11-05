#!/usr/bin/env bash
# make scan: secrets in the whole git history (gitleaks and trufflehog over every ref), known
# vulnerabilities in the Go code dash builds (govulncheck) and the workflows (actionlint, zizmor,
# and a trigger check). Any finding fails the run.
#
# gitleaks, trufflehog and zizmor are release archives pinned by version and sha256 below. Each is
# downloaded once into SCAN_TOOLS_DIR (default: the user cache; CI uses a fresh temp dir), and on
# every run its sha256 is checked again before the binary is unpacked from it into a directory of
# this run. govulncheck and actionlint are Go tools in tools/go.mod. Run from anywhere in the
# checkout; CI checks out with fetch-depth 0 so the history is complete.
#
# The secret scanners never verify a candidate against its service: trufflehog runs with
# --no-verification, so a fixture never leaves this machine and every candidate fails the run
# until it is removed or, for a made-up value only, allowlisted by its exact value with a reason
# (.gitleaks.toml, scripts/ci/trufflehog-allow.txt). trufflehog also reads commit messages, which
# a path exclusion cannot cover, so its allowlist names values.
set -euo pipefail
# A failure inside $(…), such as a download in fetch, stops the script too. That needs bash 4.4
# or newer; macOS ships 3.2 (brew install bash).
shopt -s inherit_errexit 2>/dev/null || {
	echo "scan: needs bash 4.4 or newer (brew install bash)" >&2
	exit 2
}

cd "$(git rev-parse --show-toplevel)"

GITLEAKS_VERSION=8.30.1
TRUFFLEHOG_VERSION=3.99.8
ZIZMOR_VERSION=1.32.0

case "$(uname -s)/$(uname -m)" in
Linux/x86_64)
	gitleaks_asset=linux_x64 gitleaks_sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
	trufflehog_asset=linux_amd64 trufflehog_sha256=7c3b02f160f28929a467e29e20fba98746e0729f6e37c3c25761e4f8789abe08
	zizmor_asset=x86_64-unknown-linux-gnu zizmor_sha256=bb110cc765eca9cd7efb2e284a956be5974c60027b0970f16397d86208a9957b
	;;
Linux/aarch64 | Linux/arm64)
	gitleaks_asset=linux_arm64 gitleaks_sha256=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080
	trufflehog_asset=linux_arm64 trufflehog_sha256=a0382d83dd58c967af5dd0175cc732c62c836ab1d15dc9ae71b01a2d3f0911b2
	zizmor_asset=aarch64-unknown-linux-gnu zizmor_sha256=cb152b207f815116d7fa1c1ee4614f61578803351395052908095e79adfd9fd0
	;;
Darwin/x86_64)
	gitleaks_asset=darwin_x64 gitleaks_sha256=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709
	trufflehog_asset=darwin_amd64 trufflehog_sha256=f77c01a484eb8ba7ce5a17ec08ef72d3c6398ff0513b4c7b7dc480c50d8e5176
	zizmor_asset=x86_64-apple-darwin zizmor_sha256=6983143c719ada6845a69cc4dd2bacc215f971662fecd8bbd2678388e5ce5969
	;;
Darwin/arm64)
	gitleaks_asset=darwin_arm64 gitleaks_sha256=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5
	trufflehog_asset=darwin_arm64 trufflehog_sha256=85724f221bc390f6e0bdf65df245468c8426350de85aa9290ea5491c6d4ec35a
	zizmor_asset=aarch64-apple-darwin zizmor_sha256=6efed18c4539a663fd3c851a2be9533fd8456c4e646d9416206da059154a0d3c
	;;
*)
	echo "scan: no pinned scanner binaries for $(uname -s)/$(uname -m)" >&2
	exit 2
	;;
esac

tools_dir=${SCAN_TOOLS_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/dash-scan-tools}

# sha256 [FILE] prints the hex sha256 of FILE, or of stdin without one.
sha256() {
	if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi | cut -d' ' -f1
}

# The unpacked binaries of this run, removed on exit.
bin_dir=$(mktemp -d)
trap 'rm -rf "$bin_dir"' EXIT

# fetch NAME VERSION URL SHA256 prints the path of NAME's binary. The release archive is kept in
# the cache, downloaded when it is missing; its sha256 is checked on every run and the binary is
# unpacked from it into bin_dir, so a cached file that changed never runs. An archive with the
# wrong sha256 is removed and fails the run; the next run downloads it again.
fetch() {
	local name=$1 version=$2 url=$3 want=$4
	local archive=$tools_dir/$name-$version.tar.gz
	if [[ ! -f $archive ]]; then
		mkdir -p "$tools_dir"
		curl -fsSL --retry 3 -o "$archive.part" "$url"
		mv "$archive.part" "$archive"
	fi
	local got
	got=$(sha256 "$archive")
	if [[ $got != "$want" ]]; then
		echo "scan: $archive (from $url) has sha256 $got, want $want; removed it" >&2
		rm -f "$archive"
		exit 1
	fi
	mkdir -p "$bin_dir/$name"
	tar -xzf "$archive" -C "$bin_dir/$name" "$name"
	echo "$bin_dir/$name/$name"
}

gitleaks=$(fetch gitleaks "$GITLEAKS_VERSION" \
	"https://github.com/gitleaks/gitleaks/releases/download/v$GITLEAKS_VERSION/gitleaks_${GITLEAKS_VERSION}_$gitleaks_asset.tar.gz" \
	"$gitleaks_sha256")
trufflehog=$(fetch trufflehog "$TRUFFLEHOG_VERSION" \
	"https://github.com/trufflesecurity/trufflehog/releases/download/v$TRUFFLEHOG_VERSION/trufflehog_${TRUFFLEHOG_VERSION}_$trufflehog_asset.tar.gz" \
	"$trufflehog_sha256")
zizmor=$(fetch zizmor "$ZIZMOR_VERSION" \
	"https://github.com/zizmorcore/zizmor/releases/download/v$ZIZMOR_VERSION/zizmor-$zizmor_asset.tar.gz" \
	"$zizmor_sha256")

echo "== gitleaks $GITLEAKS_VERSION: every commit of every ref"
"$gitleaks" git --no-banner --redact --config .gitleaks.toml --log-opts="--all --full-history" .

echo "== trufflehog $TRUFFLEHOG_VERSION: every commit of every ref, unverified"
# One JSON result per line; a result is accepted only when its detector and exact Raw value are a
# line of the allowlist. Another result is reported by detector, commit, file and line and a
# sha256 prefix of its value, never the value itself.
found=$("$trufflehog" git "file://$PWD" --no-update --no-verification --fail-on-scan-errors --json)
unexpected=$(jq -r --rawfile allow scripts/ci/trufflehog-allow.txt '
	($allow | split("\n") | map(select(test("^[^#[:space:]]")) | [splits("[[:space:]]+")] | .[0] + " " + .[1])) as $ok
	| select((.DetectorName + " " + .Raw) as $key | $ok | index([$key]) | not)
	| .SourceMetadata.Data.Git as $g
	| [.DetectorName, $g.commit[0:12], ($g.file // "(commit message)"), ($g.line | tostring), (.Raw | @base64)]
	| @tsv' <<<"$found")
if [[ -n $unexpected ]]; then
	while IFS=$'\t' read -r detector commit file line raw; do
		echo "$detector in commit $commit $file:$line: value sha256 $(base64 -d <<<"$raw" | sha256 | cut -c1-12)…"
	done <<<"$unexpected"
	echo "scan: trufflehog found the candidates above; remove a real secret from the history and rotate it" >&2
	exit 1
fi
echo "trufflehog: $(jq -s length <<<"$found") results, all in scripts/ci/trufflehog-allow.txt"

echo "== govulncheck: the packages dash builds"
go tool -modfile=tools/go.mod govulncheck ./...

# shellcheck and pyflakes are left out: they are not pinned here.
echo "== actionlint"
go tool -modfile=tools/go.mod actionlint -shellcheck= -pyflakes=

echo "== zizmor $ZIZMOR_VERSION"
"$zizmor" --offline --config .github/zizmor.yml .github

# zizmor's dangerous-triggers audit reports these three today; this check holds the rule whatever
# a later zizmor counts as dangerous or a config suppresses. Each runs a workflow with the base
# repository's token on input an outsider controls, and none has a use here, so any mention of
# one in a workflow fails.
echo "== workflow triggers"
if grep -nE '(^|[^[:alnum:]_])(pull_request_target|workflow_run|issue_comment)([^[:alnum:]_]|$)' .github/workflows/*.y*ml; then
	echo "scan: the workflows above use a trigger dash does not allow (pull_request_target, workflow_run, issue_comment)" >&2
	exit 1
fi

echo "scan: clean"
