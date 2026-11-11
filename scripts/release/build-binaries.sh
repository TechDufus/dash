#!/usr/bin/env bash
# The release binaries (docs/deploy/binary.md): dash cross-compiled with the Dockerfile's flags
# for every platform below, each packed as dash_<version>_<os>_<arch>.tar.gz with LICENSE and
# THIRD_PARTY_NOTICES.md, plus dash_<version>_checksums.txt in sha256sum format. CI's binaries
# job runs it on every push and pull request and attaches the files to the GitHub release of a
# tag; `make dist` runs it with the chart's version into dist/.
#
#   build-binaries.sh [--web] [--platform OS/ARCH]… VERSION OUTDIR
#
# The SPA is embedded at build time: web/dist is built first when web/dist/index.html is
# missing, or always with --web. --platform limits the build to the platforms named (the install
# smoke builds only its own); the default is all of them.
#
# The archives are reproducible: the same checkout and Go toolchain give the same bytes. Entries
# are in a fixed order with fixed modes, owner and group 0, and the mtime SOURCE_DATE_EPOCH (when
# unset, the commit time of HEAD); gzip stores no name or time.
#
# Needs bash 4.4, Go, npm (only when web/dist is built), GNU tar (gtar on macOS), gzip and
# sha256sum or shasum.
set -euo pipefail
# A failure inside $(…) stops the script too. That needs bash 4.4 or newer; macOS ships 3.2
# (brew install bash).
shopt -s inherit_errexit 2>/dev/null || {
	echo "build-binaries: needs bash 4.4 or newer (brew install bash)" >&2
	exit 2
}

# Every platform builds with CGO_ENABLED=0, so none needs a C toolchain.
all=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

usage() {
	echo "usage: build-binaries.sh [--web] [--platform OS/ARCH]... VERSION OUTDIR" >&2
	exit 2
}

web=
platforms=()
while (($#)); do
	case $1 in
	--web) web=1 ;;
	--platform)
		(($# >= 2)) || usage
		[[ " ${all[*]} " == *" $2 "* ]] || {
			echo "build-binaries: unknown platform '$2'; one of ${all[*]}" >&2
			exit 2
		}
		platforms+=("$2")
		shift
		;;
	--) shift && break ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
(($# == 2)) || usage
version=$1
out=$2
((${#platforms[@]})) || platforms=("${all[@]}")
# X.Y.Z with an optional pre-release or build suffix: it is part of every file name.
if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
	echo "build-binaries: VERSION must be X.Y.Z (got '$version')" >&2
	exit 2
fi

mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$(dirname "$0")/../.."

tar=tar
tar --version 2>/dev/null | grep -q 'GNU tar' || tar=gtar
"$tar" --version 2>/dev/null | grep -q 'GNU tar' || {
	echo "build-binaries: needs GNU tar (brew install gnu-tar on macOS)" >&2
	exit 2
}
if command -v sha256sum >/dev/null; then
	sha256=(sha256sum)
else
	sha256=(shasum -a 256)
fi

epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
[[ $epoch =~ ^[0-9]+$ ]] || {
	echo "build-binaries: SOURCE_DATE_EPOCH must be seconds since 1970 (got '$epoch')" >&2
	exit 2
}

if [[ -n $web || ! -f web/dist/index.html ]]; then
	echo "build-binaries: building the SPA (web/dist)"
	(cd web && npm ci --ignore-scripts && npm run build)
fi

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
umask 022

files=()
for platform in "${platforms[@]}"; do
	os=${platform%/*} arch=${platform#*/}
	name=dash_${version}_${os}_${arch}.tar.gz
	dir=$stage/$os-$arch
	mkdir -p "$dir"
	echo "build-binaries: $name"
	# The Dockerfile's build: no cgo, no local paths, no symbol tables, the version stamped in.
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
		go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/dash" ./cmd/dash
	cp LICENSE THIRD_PARTY_NOTICES.md "$dir/"
	chmod 0755 "$dir/dash"
	chmod 0644 "$dir/LICENSE" "$dir/THIRD_PARTY_NOTICES.md"
	# Names listed in byte order (LC_ALL=C), no directory entry, ustar headers: nothing in the
	# archive depends on this machine, the user or the time of the build.
	LC_ALL=C "$tar" --create --format=ustar --directory "$dir" \
		--owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
		LICENSE THIRD_PARTY_NOTICES.md dash | gzip -n -9 >"$out/$name"
	files+=("$name")
done

checksums=dash_${version}_checksums.txt
(cd "$out" && "${sha256[@]}" "${files[@]}") >"$out/$checksums"
echo "build-binaries: $out/$checksums"
cat "$out/$checksums"
