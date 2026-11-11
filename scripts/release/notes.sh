#!/usr/bin/env bash
# The release notes of one version, from its section of CHANGELOG.md: the `## vX.Y.Z: headline`
# heading is the title of the GitHub release and the text up to the next `## ` heading its notes.
# CI's github-release job publishes them, and `make check` asks for those of the chart's version
# (TestChartVersionHasReleaseNotes), so a version without notes fails before it can be tagged.
#
#   notes.sh title [X.Y.Z [FILE]]   the heading without its marker: vX.Y.Z: headline
#   notes.sh body  [X.Y.Z [FILE]]   the notes, without the blank lines at either end
#
# X.Y.Z defaults to the version of charts/dash/Chart.yaml and FILE to CHANGELOG.md. Both modes
# refuse a version with no section, with more than one, or with a section that has no notes. A
# `## ` line inside a fenced code block belongs to the notes.
set -euo pipefail
# A failure inside $(…) stops the script too. That needs bash 4.4 or newer; macOS ships 3.2
# (brew install bash).
shopt -s inherit_errexit 2>/dev/null || {
	echo "notes: needs bash 4.4 or newer (brew install bash)" >&2
	exit 2
}

cd "$(git rev-parse --show-toplevel)"

mode=${1:-}
version=${2:-$(awk '$1 == "version:" { print $2 }' charts/dash/Chart.yaml)}
file=${3:-CHANGELOG.md}
if [[ $# -gt 3 || ($mode != title && $mode != body) || ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "usage: notes.sh title|body [X.Y.Z [FILE]] (got '${*}')" >&2
	exit 2
fi

status=0
awk -v mode="$mode" -v head="## v$version" '
	/^[ \t]*(```|~~~)/ { fenced = !fenced }
	!fenced && /^## / {
		inside = 0
		if ($0 == head || index($0, head ": ") == 1) {
			found++
			title = substr($0, 4)
			inside = 1
			next
		}
	}
	inside { line[++n] = $0 }
	END {
		if (found != 1) exit (found ? 4 : 3)
		first = 1
		while (first <= n && line[first] ~ /^[ \t]*$/) first++
		while (n >= first && line[n] ~ /^[ \t]*$/) n--
		if (n < first) exit 5
		if (mode == "title") print title
		else for (i = first; i <= n; i++) print line[i]
	}
' "$file" || status=$?
case $status in
0) ;;
3) echo "notes: $file has no section '## v$version: headline'; write the release notes of $version there" >&2 ;;
4) echo "notes: $file has more than one section for v$version" >&2 ;;
5) echo "notes: the section of v$version in $file has no notes" >&2 ;;
*) echo "notes: could not read $file" >&2 ;;
esac
exit "$status"
