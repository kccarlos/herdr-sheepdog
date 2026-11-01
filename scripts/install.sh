#!/bin/sh
# Installs the released sheepdog binary for Apple silicon macOS or x86-64
# Linux. The default is the latest release and ~/.local/bin.
#
#   curl -fsSL https://raw.githubusercontent.com/kccarlos/herdr-sheepdog/main/scripts/install.sh | sh
#
# SHEEPDOG_VERSION pins a release, SHEEPDOG_INSTALL_DIR changes the target,
# and SHEEPDOG_RELEASE_URL replaces the download directory (tests use it).
set -eu

quickstart=https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/quick-start.md#other-ways-to-install

die() {
	printf 'sheepdog installer: %s\n' "$*" >&2
	exit 1
}

os=$(uname -s)
arch=$(uname -m)
case "$os" in
MINGW* | MSYS* | CYGWIN* | Windows_NT)
	die "Windows is not supported."
	;;
Darwin)
	[ "$arch" = arm64 ] || die "No release build for $os $arch; build from source: $quickstart"
	target=darwin_arm64
	;;
Linux)
	case "$arch" in
	x86_64 | amd64) target=linux_amd64 ;;
	*) die "No release build for $os $arch; build from source: $quickstart" ;;
	esac
	;;
*)
	die "No release build for $os $arch; build from source: $quickstart"
	;;
esac

for command in curl tar; do
	command -v "$command" >/dev/null 2>&1 || die "$command is required."
done

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		die "sha256sum or shasum is required."
	fi
}

# resolve_command follows symlinks so a Homebrew shim such as
# /opt/homebrew/bin/sheepdog is recognised as the Cellar binary.
resolve_command() {
	_resolve_path=$1
	_resolve_hops=0
	while [ -L "$_resolve_path" ] && command -v readlink >/dev/null 2>&1; do
		_resolve_hops=$((_resolve_hops + 1))
		[ "$_resolve_hops" -le 40 ] || break
		_resolve_target=$(readlink "$_resolve_path") || break
		case "$_resolve_target" in
		/*) _resolve_path=$_resolve_target ;;
		*) _resolve_path=$(dirname "$_resolve_path")/$_resolve_target ;;
		esac
	done
	printf '%s\n' "$_resolve_path"
}

[ -n "${HOME:-}" ] || die "HOME is not set."
install_dir=${SHEEPDOG_INSTALL_DIR:-$HOME/.local/bin}
link=$install_dir/sheepdog
mkdir -p "$install_dir" || die "cannot create $install_dir"

if [ -L "$link" ]; then
	echo "sheepdog at $link is a symlink; upgrade it with: herdr plugin update kccarlos.sheepdog" >&2
	exit 0
fi

installed=$(command -v sheepdog 2>/dev/null || true)
if [ -n "$installed" ]; then
	resolved=$(resolve_command "$installed")
	case "$resolved" in
	*/Cellar/*)
		echo "sheepdog is installed with Homebrew; upgrade it with: brew upgrade sheepdog" >&2
		exit 0
		;;
	esac
fi

daemon_running=0
if [ -n "$installed" ] && "$installed" status >/dev/null 2>&1; then
	daemon_running=1
fi

if [ -n "${SHEEPDOG_RELEASE_URL:-}" ]; then
	base=$SHEEPDOG_RELEASE_URL
elif [ -n "${SHEEPDOG_VERSION:-}" ]; then
	base=https://github.com/kccarlos/herdr-sheepdog/releases/download/${SHEEPDOG_VERSION}
else
	base=https://github.com/kccarlos/herdr-sheepdog/releases/latest/download
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/sheepdog-install.XXXXXX")
staged=
cleanup() {
	rm -rf "$tmp"
	[ -z "$staged" ] || rm -f "$staged"
}
trap cleanup EXIT HUP INT TERM

curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" ||
	die "could not download $base/checksums.txt"

# Lines look like "<sha256>  sheepdog_v0.1.0_linux_amd64.tar.gz".
asset=$(awk -v t="_$target.tar.gz" '{
	f = $2
	sub(/^\*/, "", f)
}
f ~ /^sheepdog_/ && substr(f, length(f) - length(t) + 1) == t {
	print f
	exit
}' "$tmp/checksums.txt")
[ -n "$asset" ] || die "the release has no $target asset"
want=$(awk -v a="$asset" '{
	f = $2
	sub(/^\*/, "", f)
}
f == a {
	print $1
	exit
}' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no checksum for $asset"

curl -fsSL "$base/$asset" -o "$tmp/$asset" ||
	die "could not download $base/$asset"
[ "$(sha256 "$tmp/$asset")" = "$want" ] ||
	die "$asset does not match checksums.txt; not installing"
tar -xzf "$tmp/$asset" -C "$tmp" sheepdog ||
	die "could not unpack $asset"

staged=$(mktemp "$install_dir/.sheepdog.XXXXXX") ||
	die "cannot create a temporary file in $install_dir"
cp "$tmp/sheepdog" "$staged" || die "cannot copy sheepdog into $install_dir"
chmod 755 "$staged" || die "cannot make sheepdog executable"
mv -f "$staged" "$link" || die "cannot replace $link"
staged=
printf 'Installed sheepdog to %s (sha256 verified).\n' "$link"

case ":$PATH:" in
*":$install_dir:"*) cli=sheepdog ;;
*)
	case "${SHELL:-}" in
	*/zsh) profile='~'/.zshrc ;;
	*/bash)
		if [ "$os" = Darwin ]; then profile='~'/.bash_profile; else profile='~'/.bashrc; fi
		;;
	*) profile='~'/.profile ;;
	esac
	# shellcheck disable=SC2016 # $PATH is the useful line to add, not this shell's PATH.
	printf 'Add this line to %s:\nexport PATH="%s:$PATH"\n' "$profile" "$install_dir"
	cli=$link
	;;
esac

"$cli" version || die "the installed sheepdog did not run"

tty_available() {
	(true </dev/tty) >/dev/null 2>&1
}

# Another sheepdog earlier on PATH would keep running instead of this one.
first=$(command -v sheepdog 2>/dev/null || true)
if [ -n "$first" ] && [ "$first" != "$link" ]; then
	printf 'Note: %s comes first on PATH, so the sheepdog command still runs that one. Remove it, or put %s before it.\n' "$first" "$install_dir"
	cli=$link
fi

# Ask on the terminal even when the script itself arrives on stdin
# (curl | sh); without a terminal, never wait for input.
if [ "${SHEEPDOG_NO_SETUP:-}" != "1" ] && tty_available; then
	printf 'Run sheepdog setup now? [Y/n] ' >/dev/tty
	if ! IFS= read -r answer </dev/tty; then
		answer=n
	fi
	case "$answer" in
	"" | y | Y | yes | YES | Yes) "$cli" setup </dev/tty ;;
	*) echo "Next: sheepdog setup" ;;
	esac
else
	echo "Next: sheepdog setup"
fi

if [ "$daemon_running" -eq 1 ]; then
	echo "Restart it to use the new version: sheepdog restart"
fi
if [ "$os" = Darwin ] && [ ! -d /Applications/Sheepdog.app ] && [ ! -d "$HOME/Applications/Sheepdog.app" ]; then
	echo "For voice on this Mac with herdr-app: brew install --cask kccarlos/tap/sheepdog-app"
fi
