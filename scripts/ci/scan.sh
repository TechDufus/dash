#!/usr/bin/env bash
# make scan: secrets in the whole git history (gitleaks and trufflehog over every ref), known
# vulnerabilities in the Go code dash builds (govulncheck) and the workflows (actionlint, zizmor,
# and a trigger check). Any finding fails the run.
#
# gitleaks, trufflehog and zizmor are release archives pinned by version and sha256 in tools.sh,
# downloaded once into SCAN_TOOLS_DIR (default: the user cache; CI uses a fresh temp dir) and
# checked against their sha256 again on every run before the binary is unpacked into a directory
# of this run. govulncheck and actionlint are Go tools in tools/go.mod. Run from anywhere in the
# checkout; CI checks out with fetch-depth 0 so the history is complete. The external links of
# the docs are checked last (links.sh online).
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

# shellcheck source=scripts/ci/tools.sh
. scripts/ci/tools.sh

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

# actionlint runs without its shellcheck and pyflakes integrations: those are not pinned here.
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

echo "== lychee $LYCHEE_VERSION: the web links of the docs"
scripts/ci/links.sh online

echo "scan: clean"
