#!/bin/sh
# Build step of the herdr plugin (herdr-plugin.toml): puts a sheepdog
# binary in <plugin root>/bin/.
#
#   1. Downloads the release asset for this OS and CPU from GitHub Releases
#      and checks its sha256 against the release's checksums.txt. A
#      mismatch is an error; it never falls back to building.
#   2. No release (or none for this machine) and `go` on PATH: go build.
#   3. Neither: stops and says what to do.
#
# SHEEPDOG_RELEASE_URL replaces the download directory (for tests; curl
# also takes file:// URLs). The default is the latest release.
set -eu

base=${SHEEPDOG_RELEASE_URL:-https://github.com/kccarlos/herdr-sheepdog/releases/latest/download}
root=$(cd "$(dirname "$0")/.." && pwd)
bin=$root/bin/sheepdog
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

die() {
	echo "plugin-fetch: $*" >&2
	exit 1
}

# sha256 prints the hex digest of file $1.
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# fetch_release installs the released binary. It returns 1 when there is
# nothing to download for this machine, and exits on a bad checksum.
fetch_release() {
	case "$(uname -s)_$(uname -m)" in
	Darwin_arm64) target=darwin_arm64 ;;
	Linux_x86_64 | Linux_amd64) target=linux_amd64 ;;
	*)
		echo "plugin-fetch: no release build for $(uname -s) $(uname -m)" >&2
		return 1
		;;
	esac
	command -v curl >/dev/null 2>&1 || return 1
	if ! curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"; then
		echo "plugin-fetch: no release found at $base" >&2
		return 1
	fi
	# Lines look like "<sha256>  sheepdog_v0.1.0_linux_amd64.tar.gz".
	asset=$(awk -v t="_$target.tar.gz" '{ f = $2; sub(/^\*/, "", f) } f ~ /^sheepdog_/ && substr(f, length(f) - length(t) + 1) == t { print f; exit }' "$tmp/checksums.txt")
	[ -n "$asset" ] || {
		echo "plugin-fetch: the release has no $target asset" >&2
		return 1
	}
	want=$(awk -v a="$asset" '{ f = $2; sub(/^\*/, "", f) } f == a { print $1; exit }' "$tmp/checksums.txt")
	curl -fsSL "$base/$asset" -o "$tmp/$asset" || return 1
	[ "$(sha256 "$tmp/$asset")" = "$want" ] || die "$asset does not match checksums.txt; not installing"
	tar -xzf "$tmp/$asset" -C "$tmp" sheepdog || die "cannot unpack $asset"
	{ mkdir -p "$root/bin" && cp "$tmp/sheepdog" "$bin" && chmod 755 "$bin"; } || die "cannot write $bin"
	echo "plugin-fetch: installed $asset (sha256 verified)"
}

build_from_source() {
	command -v go >/dev/null 2>&1 || die "no release to download and go is not on PATH. Install Go (https://go.dev/dl/) and reinstall, or install a release by hand: https://github.com/kccarlos/herdr-sheepdog/releases"
	echo "plugin-fetch: building from source with go"
	mkdir -p "$root/bin"
	(cd "$root" && go build -o "$bin" ./cmd/sheepdog)
}

# link_cli puts `sheepdog` on the user's PATH: a symlink in ~/.local/bin
# (SHEEPDOG_LINK_DIR for tests) to the plugin's binary. An existing file
# that is not a symlink is never replaced; a directory missing from PATH
# gets a warning with the line to add.
link_cli() {
	dir=${SHEEPDOG_LINK_DIR:-$HOME/.local/bin}
	link=$dir/sheepdog
	if [ -e "$link" ] && [ ! -L "$link" ]; then
		echo "plugin-fetch: $link exists and is not a link; left it alone (run $bin directly, or remove it and reinstall)" >&2
		return 0
	fi
	mkdir -p "$dir" && ln -sf "$bin" "$link" || {
		echo "plugin-fetch: could not link $link; run $bin directly" >&2
		return 0
	}
	echo "plugin-fetch: linked $link -> $bin"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "plugin-fetch: warning: $dir is not on your PATH; add it (e.g. echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc) or run $link" >&2 ;;
	esac
}

fetch_release || build_from_source
"$bin" version
link_cli
echo ""
echo "Next: run \`sheepdog setup\` (or open the \"Set up sheepdog\" action in herdr)."
