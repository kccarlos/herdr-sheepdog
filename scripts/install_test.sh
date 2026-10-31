#!/bin/sh
# Tests scripts/install.sh against a fake release directory (no network).
# Run: sh scripts/install_test.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
fails=0

check() { # check <name> <condition exit status>
	if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails + 1)); fi
}

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}

home=$work/home
bin=$work/bin
rel=$work/rel
pkg=$work/pkg
mkdir -p "$home" "$rel" "$pkg"

# The stub logs setup, and reports a running daemon only when asked.
cat >"$pkg/sheepdog" <<EOF
#!/bin/sh
case "\${1:-}" in
version) echo "sheepdog v9.9.9" ;;
status) [ -z "\${SHEEPDOG_FAKE_STATUS_RUNNING:-}" ] || exit 0; exit 1 ;;
setup) echo setup >>"$work/setup-ran" ;;
*) exit 1 ;;
esac
EOF
chmod 755 "$pkg/sheepdog"
tar -C "$pkg" -czf "$rel/sheepdog_v9.9.9_linux_amd64.tar.gz" sheepdog
tar -C "$pkg" -czf "$rel/sheepdog_v9.9.9_darwin_arm64.tar.gz" sheepdog
(cd "$rel" && for f in *.tar.gz; do sum "$f"; done >checksums.txt)

cleanup_run() {
	rm -rf "$bin"
	rm -f "$work/setup-ran"
}

# A supported host is required for the end-to-end release cases. The
# unsupported-platform cases use fake uname below and run everywhere.
case "$(uname -s)_$(uname -m)" in
Darwin_arm64 | Linux_x86_64 | Linux_amd64)
	# 1. Good release, checksum, install, PATH hint, and next step.
	out=$(HOME=$home SHELL=/bin/zsh SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_RELEASE_URL="file://$rel" SHEEPDOG_NO_SETUP=1 \
		sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "release installs" "$rc"
	if [ -x "$bin/sheepdog" ] && [ "$("$bin/sheepdog" version)" = "sheepdog v9.9.9" ]; then rc=0; else rc=1; fi
	check "installed binary runs" "$rc"
	case "$out" in *"sha256 verified"*) rc=0 ;; *) rc=1 ;; esac
	check "says it verified the checksum" "$rc"
	# shellcheck disable=SC2016 # $PATH is the literal text the installer prints.
	case "$out" in *"Add this line to ~/.zshrc:"*'export PATH="'*"$bin"*'$PATH"'*) rc=0 ;; *) rc=1 ;; esac
	check "prints the shell PATH line" "$rc"
	case "$out" in *"Next: sheepdog setup"*) rc=0 ;; *) rc=1 ;; esac
	check "NO_SETUP prints the next step" "$rc"
	if [ ! -e "$work/setup-ran" ]; then rc=0; else rc=1; fi
	check "NO_SETUP runs no setup" "$rc"

	# A running old daemon gets the restart instruction.
	out=$(HOME=$home SHELL=/bin/zsh PATH="$bin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_FAKE_STATUS_RUNNING=1 SHEEPDOG_RELEASE_URL="file://$rel" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	case "$out" in *"Restart it to use the new version: sheepdog restart"*) rc=0 ;; *) rc=1 ;; esac
	check "running daemon gets the restart line" "$rc"

	# Another sheepdog earlier on PATH: said, and the new one still runs.
	other=$work/other
	mkdir -p "$other"
	cp "$pkg/sheepdog" "$other/sheepdog"
	out=$(HOME=$home SHELL=/bin/zsh PATH="$other:$bin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_RELEASE_URL="file://$rel" SHEEPDOG_NO_SETUP=1 \
		sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "installs with another sheepdog first on PATH" "$rc"
	case "$out" in *"Note: $other/sheepdog comes first on PATH"*) rc=0 ;; *) rc=1 ;; esac
	check "says another sheepdog comes first" "$rc"
	rm -rf "$other"

	# 2. A bad checksum fails before anything is installed.
	cleanup_run
	printf 'x' >>"$rel/sheepdog_v9.9.9_linux_amd64.tar.gz"
	printf 'x' >>"$rel/sheepdog_v9.9.9_darwin_arm64.tar.gz"
	HOME=$home SHEEPDOG_INSTALL_DIR=$bin SHEEPDOG_RELEASE_URL="file://$rel" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" >"$work/out" 2>&1 && rc=1 || rc=0
	check "bad checksum fails" "$rc"
	grep -q "does not match checksums.txt" "$work/out"
	check "bad checksum says so" "$?"
	if [ ! -e "$bin/sheepdog" ]; then rc=0; else rc=1; fi
	check "bad checksum installs nothing" "$rc"

	# Restore good assets for the symlink and version cases.
	rm -rf "$rel"
	mkdir -p "$rel"
	tar -C "$pkg" -czf "$rel/sheepdog_v9.9.9_linux_amd64.tar.gz" sheepdog
	tar -C "$pkg" -czf "$rel/sheepdog_v9.9.9_darwin_arm64.tar.gz" sheepdog
	(cd "$rel" && for f in *.tar.gz; do sum "$f"; done >checksums.txt)

	# 3. An existing symlink (the plugin's link) is left alone.
	cleanup_run
	mkdir -p "$bin"
	ln -s "$pkg/sheepdog" "$bin/sheepdog"
	out=$(HOME=$home SHEEPDOG_INSTALL_DIR=$bin SHEEPDOG_RELEASE_URL="file://$rel" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	if [ "$rc" -eq 0 ] && [ -L "$bin/sheepdog" ]; then rc=0; else rc=1; fi
	check "existing symlink exits successfully and stays a symlink" "$rc"
	case "$out" in *"herdr plugin update kccarlos.sheepdog"*) rc=0 ;; *) rc=1 ;; esac
	check "symlink says how the plugin upgrades" "$rc"

	# 4. A Homebrew Cellar binary is left alone.
	cleanup_run
	brewbin=$work/homebrew-bin
	cellar=$work/homebrew/Cellar/sheepdog/1.0.0/bin
	mkdir -p "$brewbin" "$cellar"
	cp "$pkg/sheepdog" "$cellar/sheepdog"
	ln -s "$cellar/sheepdog" "$brewbin/sheepdog"
	out=$(HOME=$home PATH="$brewbin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_RELEASE_URL="file://$rel" SHEEPDOG_NO_SETUP=1 \
		sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	if [ "$rc" -eq 0 ] && [ ! -e "$bin/sheepdog" ]; then rc=0; else rc=1; fi
	check "Homebrew install is left alone" "$rc"
	case "$out" in *"upgrade it with: brew upgrade sheepdog"*) rc=0 ;; *) rc=1 ;; esac
	check "Homebrew says how it upgrades" "$rc"

	# 5. SHEEPDOG_VERSION pins the release URL. A fake curl maps GitHub
	# releases to the local files and logs the requested URLs.
	cleanup_run
	fake=$work/fake
	mkdir -p "$fake" "$work/pinned"
	for f in "$rel"/*; do cp "$f" "$work/pinned/"; done
	cat >"$fake/curl" <<EOF
#!/bin/sh
url=
out=
while [ "\$#" -gt 0 ]; do
	case "\$1" in
	-o) out=\$2; shift 2 ;;
	http://* | https://* | file://*) url=\$1; shift ;;
	*) shift ;;
	esac
done
printf '%s\n' "\$url" >>"$work/curl-urls"
case "\$url" in
https://github.com/kccarlos/herdr-sheepdog/releases/download/v0.1.18/*)
	cp "$work/pinned/\${url##*/}" "\$out"
	;;
*) exit 22 ;;
esac
EOF
	chmod 755 "$fake/curl"
	out=$(HOME=$home SHELL=/bin/zsh PATH="$fake:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_VERSION=v0.1.18 SHEEPDOG_NO_SETUP=1 \
		sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "pinned release installs" "$rc"
	grep -q "https://github.com/kccarlos/herdr-sheepdog/releases/download/v0.1.18/checksums.txt" "$work/curl-urls"
	check "pinned release uses the tag URL" "$?"
	;;
*)
	echo "skip supported-release cases on $(uname -s) $(uname -m)"
	;;
esac

# 6. Unsupported platforms get one clear line before downloads. The fake
# uname is first on PATH, so neither curl nor tar is needed.
fake_uname() { # fake_uname <os> <arch> <name> <expected>
	dir=$work/uname-$3
	mkdir -p "$dir"
	cat >"$dir/uname" <<EOF
#!/bin/sh
case "\${1:-}" in
-s) echo "$1" ;;
-m) echo "$2" ;;
*) exit 1 ;;
esac
EOF
	chmod 755 "$dir/uname"
	HOME=$home PATH="$dir:/usr/bin:/bin" SHEEPDOG_INSTALL_DIR=$work/never \
		sh "$here/install.sh" >"$work/out" 2>&1 && rc=1 || rc=0
	check "$3 is rejected" "$rc"
	grep -q "$4" "$work/out"
	check "$3 has the expected message" "$?"
}

fake_uname MINGW64_NT x86_64 windows "Windows is not supported."
fake_uname Linux aarch64 linux-arm64 "No release build for Linux aarch64; build from source: https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/quick-start.md#other-ways-to-install"
[ ! -e "$work/never/sheepdog" ]
rc=$?
check "unsupported platforms install nothing" "$rc"

[ "$fails" -eq 0 ] || { echo "$fails failed"; exit 1; }
echo "all passed"
