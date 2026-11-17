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
# An upgrade: sheepdog was installed and set up, so setup is not offered.
configured=0
if { [ -n "$installed" ] || [ -x "$link" ]; } &&
	{ [ -f "$HOME/.config/sheepdog/config.toml" ] || [ -f "$HOME/.config/sheepdog/.env" ]; }; then
	configured=1
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

managed_python_ok() {
	_py_root=$1
	_py_want=$2
	_py_real_root=$(CDPATH='' cd -P "$_py_root" 2>/dev/null && pwd -P) || return 1
	for _py_cand in "$_py_root/cpython-$_py_want" "$_py_root/cpython-$_py_want"-*; do
		[ -d "$_py_cand" ] || continue
		[ ! -L "$_py_cand" ] || continue
		_py_bin=$_py_cand/bin/python3
		[ -x "$_py_bin" ] || continue
		_py_resolved=$(resolve_command "$_py_bin")
		_py_real_dir=$(CDPATH='' cd -P "$(dirname "$_py_resolved")" 2>/dev/null && pwd -P) || continue
		case "$_py_real_dir/" in
		"$_py_real_root"/*) ;;
		*) continue ;;
		esac
		_py_got=$("$_py_bin" -I -B -c 'import encodings, json, sqlite3, ssl, sys; print("%d.%d.%d" % sys.version_info[:3])' </dev/null 2>/dev/null || true)
		if [ "$_py_got" = "$_py_want" ]; then
			return 0
		fi
	done
	return 1
}

# Fetch herdr-core (hc) if missing; a fresh install does not start a core.
if ! command -v hc >/dev/null 2>&1 && [ ! -x "$install_dir/hc" ] && [ ! -x "$HOME/.local/bin/hc" ]; then
	hc_install_url=${SHEEPDOG_HC_INSTALL_URL:-https://releases.herdr.dev/core/install.sh}
	if curl -fsSL "$hc_install_url" -o "$tmp/hc-install.sh" </dev/null &&
		HC_INSTALL_DIR="$install_dir" sh "$tmp/hc-install.sh" </dev/null; then
		:
	else
		echo "Warning: could not install herdr-core (hc); install it with: curl -fsSL https://releases.herdr.dev/core/install.sh | sh" >&2
	fi
fi

# Install the pinned uv-managed CPython into sheepdog's data folder (never
# system python3). The factory itself stays off until `sheepdog factory start`.
py_version=$("$link" factory python-version </dev/null 2>/dev/null || true)
if [ -n "$py_version" ]; then
	py_dir=$("$link" factory python-dir </dev/null 2>/dev/null || true)
	[ -n "$py_dir" ] || py_dir="${SHEEPDOG_DATA_DIR:-$HOME/.local/share/sheepdog}/factory/python"
	uv_cmd=$(command -v uv 2>/dev/null || true)
	if [ -z "$uv_cmd" ] && [ -x "$install_dir/uv" ]; then
		uv_cmd=$install_dir/uv
	elif [ -z "$uv_cmd" ] && [ -x "$HOME/.local/bin/uv" ]; then
		uv_cmd=$HOME/.local/bin/uv
	fi
	if [ -z "$uv_cmd" ]; then
		uv_install_url=${SHEEPDOG_UV_INSTALL_URL:-https://astral.sh/uv/install.sh}
		if curl -fsSL "$uv_install_url" -o "$tmp/uv-install.sh" </dev/null &&
			UV_INSTALL_DIR="$install_dir" UV_NO_MODIFY_PATH=1 UV_PRINT_QUIET=1 sh "$tmp/uv-install.sh" </dev/null; then
			if [ -x "$install_dir/uv" ]; then
				uv_cmd=$install_dir/uv
			else
				uv_cmd=$(command -v uv 2>/dev/null || true)
			fi
		fi
	fi
	py_installed=0
	if [ -n "$uv_cmd" ] && mkdir -p "$py_dir"; then
		if "$uv_cmd" python install --install-dir "$py_dir" --no-bin --managed-python --no-config "$py_version" </dev/null &&
			managed_python_ok "$py_dir" "$py_version"; then
			py_installed=1
		elif "$uv_cmd" python install --reinstall --install-dir "$py_dir" --no-bin --managed-python --no-config "$py_version" </dev/null &&
			managed_python_ok "$py_dir" "$py_version"; then
			py_installed=1
		fi
	fi
	if [ "$py_installed" -eq 1 ]; then
		printf 'Installed managed Python %s into %s.\n' "$py_version" "$py_dir"
	else
		printf 'Warning: could not install managed Python %s with uv; run "sheepdog factory doctor" to repair it.\n' "$py_version" >&2
	fi
fi

# A running daemon moves to the new version now.
if [ "$daemon_running" -eq 1 ] && ! "$link" restart </dev/null; then
	echo "Restart it to use the new version: sheepdog restart"
fi

# Ask on the terminal even when the script itself arrives on stdin
# (curl | sh); without a terminal, never wait for input.
if [ "$configured" -eq 1 ]; then
	:
elif [ "${SHEEPDOG_NO_SETUP:-}" != "1" ] && tty_available; then
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

if [ "$os" = Darwin ] && [ ! -d /Applications/Sheepdog.app ] && [ ! -d "$HOME/Applications/Sheepdog.app" ]; then
	echo "For voice on this Mac with herdr-app: brew install --cask kccarlos/tap/sheepdog-app"
fi
