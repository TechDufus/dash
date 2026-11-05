#!/usr/bin/env bash
# The link check of the docs, with lychee (pinned in tools.sh). Every tracked Markdown file is an
# input except docs/design (the planning record, which quotes links as they were) and the
# hand-written sources of generated pages (cmd/dash/plugindocs, tools/notices), whose relative
# links resolve only in the page they end up in.
#
#   links.sh offline   make check: relative links and their #anchors; no network
#   links.sh online    make scan: the http(s) links, with retries; needs the hosts the docs link
#
# scripts/ci/lychee.toml lists what is never checked, each with its reason.
set -euo pipefail
# A failure inside $(…), such as a download in fetch, stops the script too. That needs bash 4.4
# or newer; macOS ships 3.2 (brew install bash).
shopt -s inherit_errexit 2>/dev/null || {
	echo "links: needs bash 4.4 or newer (brew install bash)" >&2
	exit 2
}

cd "$(git rev-parse --show-toplevel)"

# shellcheck source=scripts/ci/tools.sh
. scripts/ci/tools.sh

lychee=$(fetch lychee "$LYCHEE_VERSION" \
	"https://github.com/lycheeverse/lychee/releases/download/lychee-v$LYCHEE_VERSION/lychee-$lychee_asset.tar.gz" \
	"$lychee_sha256" "lychee-$lychee_asset/lychee")

inputs() {
	git ls-files -- '*.md' ':!:docs/design/**' ':!:cmd/dash/plugindocs/**' ':!:tools/notices/**'
}

run=("$lychee" --config scripts/ci/lychee.toml --no-progress --mode plain --files-from -)
case "${1:-}" in
offline)
	inputs | "${run[@]}" --offline --include-fragments
	;;
online)
	# Only the web links: offline already checks the files. Anchors on other sites are left
	# alone (many are drawn by scripts), and a host gets five tries, ten seconds apart.
	inputs | "${run[@]}" --scheme https --scheme http --max-retries 5 --retry-wait-time 10 --timeout 30
	;;
*)
	echo "usage: $0 offline|online" >&2
	exit 2
	;;
esac
