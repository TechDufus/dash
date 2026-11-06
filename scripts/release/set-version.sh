#!/usr/bin/env bash
# Moves the release version, X.Y.Z, from the one charts/dash/Chart.yaml names to the one given:
# the chart's version and appVersion (CI's release job refuses a tag that differs from them) and
# the version pins of the docs, the Compose example and the bug report form, then re-renders
# examples/kubernetes with `make generate`. Only the pins change: each pattern is anchored to the
# text around its pin (an image tag, a build argument, a kustomize ref), so prose that names a
# version ("since v0.14.0") stays as it is.
#
#   set-version.sh X.Y.Z   (make set-version VERSION=X.Y.Z)
#
# It refuses a malformed version, a version equal to the current one, and a file in which its
# patterns find no pin; nothing is written until every file has been rewritten in a scratch copy.
set -euo pipefail
# A failure inside $(…) stops the script too. That needs bash 4.4 or newer; macOS ships 3.2
# (brew install bash).
shopt -s inherit_errexit 2>/dev/null || {
	echo "set-version: needs bash 4.4 or newer (brew install bash)" >&2
	exit 2
}

cd "$(git rev-parse --show-toplevel)"

semver='^[0-9]+\.[0-9]+\.[0-9]+$'
new=${1:-}
if [[ $# -ne 1 || ! $new =~ $semver ]]; then
	echo "usage: set-version.sh X.Y.Z (got '${*}')" >&2
	exit 2
fi

chart=charts/dash/Chart.yaml
old=$(awk '$1 == "version:" { print $2 }' "$chart")
app=$(awk '$1 == "appVersion:" { gsub(/"/, "", $2); print $2 }' "$chart")
if [[ ! $old =~ $semver || $app != "$old" ]]; then
	echo "set-version: $chart has version '$old' and appVersion '$app'; they must be one X.Y.Z" >&2
	exit 1
fi
if [[ $new == "$old" ]]; then
	echo "set-version: the version is already $old; nothing to change" >&2
	exit 1
fi

o=${old//./\\.} # the old version as an ERE: its digits are literal, its dots escaped
end='([^0-9.]|$)' # the pin ends there: 0.14.4 is not the start of 0.14.40

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# pin FILE SED-EXPRESSION…: rewrites FILE into the scratch directory; a file with no pin is an
# error, since the list below names only files that hold one.
files=()
pin() {
	local file=$1 args=() expr
	shift
	for expr in "$@"; do args+=(-e "$expr"); done
	mkdir -p "$tmp/$(dirname "$file")"
	sed -E "${args[@]}" "$file" >"$tmp/$file"
	if cmp -s "$file" "$tmp/$file"; then
		echo "set-version: no $old pin found in $file; update scripts/release/set-version.sh" >&2
		exit 1
	fi
	files+=("$file")
}

build="s#(VERSION=)$o$end#\\1$new\\2#g"  # docker build --build-arg VERSION=X.Y.Z
image="s#(/dash:)$o$end#\\1$new\\2#g"     # …/dash:X.Y.Z
helm="s#(--version )$o$end#\\1$new\\2#g"  # helm install … --version X.Y.Z

pin "$chart" \
	"s#^version: $o\$#version: $new#" \
	"s#^appVersion: \"$o\"\$#appVersion: \"$new\"#"
pin README.md "$build" "$image" "$helm"
pin docs/deploy/docker.md "$build" "$image"
pin docs/deploy/paas.md "$build" "$image"
pin docs/deploy/kubernetes.md "$helm" \
	"s#(\\?ref=v)$o$end#\\1$new\\2#g" \
	"s#^([[:space:]]*newTag: \")$o\"#\\1$new\"#"
pin examples/compose/compose.yaml "s#(\\\$\\{DASH_IMAGE:-[^}]*:)$o\\}#\\1$new}#"
pin .github/ISSUE_TEMPLATE/bug_report.yml "s#^([[:space:]]*placeholder: \")$o\"\$#\\1$new\"#"

# cat keeps each file's mode and links; mv would replace them with the scratch copy's.
for file in "${files[@]}"; do
	cat "$tmp/$file" >"$file"
done
echo "set-version: $old -> $new in ${files[*]}"

make generate
