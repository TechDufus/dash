# Sourced by scan.sh and links.sh: the release archives of the binaries they run, pinned by version
# and sha256, and fetch. Each archive is downloaded once into SCAN_TOOLS_DIR (default: the user
# cache; CI uses a fresh temp dir); on every run its sha256 is checked again before the binary is
# unpacked from it into a directory of this run. The first run of each version needs github.com.

GITLEAKS_VERSION=8.30.1
TRUFFLEHOG_VERSION=3.99.8
ZIZMOR_VERSION=1.32.0
LYCHEE_VERSION=0.24.2

case "$(uname -s)/$(uname -m)" in
Linux/x86_64)
	gitleaks_asset=linux_x64 gitleaks_sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
	trufflehog_asset=linux_amd64 trufflehog_sha256=7c3b02f160f28929a467e29e20fba98746e0729f6e37c3c25761e4f8789abe08
	zizmor_asset=x86_64-unknown-linux-gnu zizmor_sha256=bb110cc765eca9cd7efb2e284a956be5974c60027b0970f16397d86208a9957b
	lychee_asset=x86_64-unknown-linux-musl lychee_sha256=73657a111819a30c47c08352896796f23d64e4eb2b3ed39b6d32149241566fc5
	;;
Linux/aarch64 | Linux/arm64)
	gitleaks_asset=linux_arm64 gitleaks_sha256=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080
	trufflehog_asset=linux_arm64 trufflehog_sha256=a0382d83dd58c967af5dd0175cc732c62c836ab1d15dc9ae71b01a2d3f0911b2
	zizmor_asset=aarch64-unknown-linux-gnu zizmor_sha256=cb152b207f815116d7fa1c1ee4614f61578803351395052908095e79adfd9fd0
	lychee_asset=aarch64-unknown-linux-musl lychee_sha256=5d0b0e3aeab240f41920c633a6eaf97599be6eedda034b36e858ede7dba5e535
	;;
Darwin/x86_64)
	gitleaks_asset=darwin_x64 gitleaks_sha256=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709
	trufflehog_asset=darwin_amd64 trufflehog_sha256=f77c01a484eb8ba7ce5a17ec08ef72d3c6398ff0513b4c7b7dc480c50d8e5176
	zizmor_asset=x86_64-apple-darwin zizmor_sha256=6983143c719ada6845a69cc4dd2bacc215f971662fecd8bbd2678388e5ce5969
	lychee_asset=x86_64-apple-darwin lychee_sha256=887503a9cff667d322b8d0892b40bf49976eb9507af8483220a3706cdad55978
	;;
Darwin/arm64)
	gitleaks_asset=darwin_arm64 gitleaks_sha256=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5
	trufflehog_asset=darwin_arm64 trufflehog_sha256=85724f221bc390f6e0bdf65df245468c8426350de85aa9290ea5491c6d4ec35a
	zizmor_asset=aarch64-apple-darwin zizmor_sha256=6efed18c4539a663fd3c851a2be9533fd8456c4e646d9416206da059154a0d3c
	lychee_asset=aarch64-apple-darwin lychee_sha256=c9d3740ea2d891854d37116c9fba840f37b6e7c89d330e7db84ac333631c4977
	;;
*)
	echo "${0##*/}: no pinned tool binaries for $(uname -s)/$(uname -m)" >&2
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

# fetch NAME VERSION URL SHA256 [MEMBER] prints the path of NAME's binary. MEMBER is the binary's
# path in the archive (default: NAME). The release archive is kept in the cache, downloaded when it
# is missing; its sha256 is checked on every run and the binary is unpacked from it into bin_dir,
# so a cached file that changed never runs. An archive with the wrong sha256 is removed and fails
# the run; the next run downloads it again.
fetch() {
	local name=$1 version=$2 url=$3 want=$4 member=${5:-$1}
	local archive=$tools_dir/$name-$version.tar.gz
	if [[ ! -f $archive ]]; then
		mkdir -p "$tools_dir"
		curl -fsSL --retry 3 -o "$archive.part" "$url"
		mv "$archive.part" "$archive"
	fi
	local got
	got=$(sha256 "$archive")
	if [[ $got != "$want" ]]; then
		echo "${0##*/}: $archive (from $url) has sha256 $got, want $want; removed it" >&2
		rm -f "$archive"
		exit 1
	fi
	mkdir -p "$bin_dir/$name"
	tar -xzf "$archive" -C "$bin_dir/$name" "$member"
	echo "$bin_dir/$name/$member"
}
