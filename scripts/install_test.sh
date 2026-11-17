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
pre=$work/preinstalled
mkdir -p "$home" "$rel" "$pkg" "$pre"

# Preinstall a stub hc for the base release cases so Linux CI runners without
# hc never touch the network; dedicated cases below test fetching missing hc.
cat >"$pre/hc" <<'EOF'
#!/bin/sh
echo "hc 0.2.0"
EOF
chmod 755 "$pre/hc"
PATH="$pre:$PATH"
export PATH

# The stub logs setup, and reports a running daemon only when asked.
cat >"$pkg/sheepdog" <<EOF
#!/bin/sh
case "\${1:-}" in
version) echo "sheepdog v9.9.9" ;;
status) [ -z "\${SHEEPDOG_FAKE_STATUS_RUNNING:-}" ] || exit 0; exit 1 ;;
setup) echo setup >>"$work/setup-ran" ;;
restart) echo restart >>"$work/restart-ran"; [ -z "\${SHEEPDOG_FAKE_RESTART_FAILS:-}" ] || exit 1 ;;
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

	# A running old daemon is restarted by the new binary; without a
	# config, setup is still the next step.
	out=$(HOME=$home SHELL=/bin/zsh PATH="$bin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_FAKE_STATUS_RUNNING=1 SHEEPDOG_RELEASE_URL="file://$rel" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	if [ -e "$work/restart-ran" ]; then rc=0; else rc=1; fi
	check "running daemon is restarted" "$rc"
	case "$out" in *"Next: sheepdog setup"*) rc=0 ;; *) rc=1 ;; esac
	check "unconfigured install still offers setup" "$rc"
	rm -f "$work/restart-ran"

	# A failed restart falls back to the instruction.
	out=$(HOME=$home SHELL=/bin/zsh PATH="$bin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_FAKE_STATUS_RUNNING=1 SHEEPDOG_FAKE_RESTART_FAILS=1 SHEEPDOG_RELEASE_URL="file://$rel" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	case "$out" in *"Restart it to use the new version: sheepdog restart"*) rc=0 ;; *) rc=1 ;; esac
	check "failed restart gets the restart line" "$rc"
	rm -f "$work/restart-ran"

	# An upgrade of a configured install: no setup step, the daemon is
	# restarted, no restart instruction.
	mkdir -p "$home/.config/sheepdog"
	: >"$home/.config/sheepdog/config.toml"
	out=$(HOME=$home SHELL=/bin/zsh PATH="$bin:$PATH" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_FAKE_STATUS_RUNNING=1 SHEEPDOG_RELEASE_URL="file://$rel" \
		sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "configured upgrade installs" "$rc"
	if [ -e "$work/restart-ran" ] && [ ! -e "$work/setup-ran" ]; then rc=0; else rc=1; fi
	check "configured upgrade restarts and runs no setup" "$rc"
	case "$out" in *"sheepdog setup"* | *"Restart it to use"*) rc=1 ;; *) rc=0 ;; esac
	check "configured upgrade prints no setup or restart step" "$rc"
	rm -rf "$home/.config" "$work/restart-ran"

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

	# 5b. Factory release: fetches hc if missing, fetches uv if missing,
	# runs uv python install for the pinned version into sheepdog's data
	# folder, repairs a broken install with --reinstall, and never calls
	# system python3.
	cleanup_run
	sysbin=$work/sysbin
	mkdir -p "$sysbin"
	for tool in sh env uname curl tar gzip gunzip bzip2 xz awk grep mktemp cp mv rm ln mkdir chmod dirname basename readlink sha256sum shasum printf cat tr sed cut head tail; do
		tp=$(command -v "$tool" 2>/dev/null || true)
		case "$tp" in
		/*) ;;
		*)
			tp=
			for d in /usr/bin /bin /sbin /usr/sbin; do
				if [ -x "$d/$tool" ]; then tp=$d/$tool; break; fi
			done
			;;
		esac
		[ -z "$tp" ] || ln -sf "$tp" "$sysbin/$tool"
	done
	# GNU tar on Linux spawns gzip via PATH for -z; enforce that on BSD tar too
	# so macOS runs catch a missing compressor in sysbin.
	real_tar=$(command -v tar)
	rm -f "$sysbin/tar"
	cat >"$sysbin/tar" <<EOF
#!/bin/sh
case "\$*" in *z*) command -v gzip >/dev/null 2>&1 || { echo "tar: gzip: not on PATH" >&2; exit 2; } ;; esac
exec "$real_tar" "\$@"
EOF
	chmod 755 "$sysbin/tar"
	cat >"$sysbin/python3" <<EOF
#!/bin/sh
echo "poison-system-python3" >>"$work/poison-python3"
exit 99
EOF
	chmod 755 "$sysbin/python3"

	fpkg=$work/fpkg
	frel=$work/frel
	mkdir -p "$fpkg" "$frel"
	cat >"$fpkg/sheepdog" <<EOF
#!/bin/sh
case "\${1:-} \${2:-}" in
"version ") echo "sheepdog v9.9.9" ;;
"status ") exit 1 ;;
"factory python-version") echo "3.14.8" ;;
"factory python-dir") echo "\${SHEEPDOG_DATA_DIR:-\$HOME/.local/share/sheepdog}/factory/python" ;;
*) exit 1 ;;
esac
EOF
	chmod 755 "$fpkg/sheepdog"
	tar -C "$fpkg" -czf "$frel/sheepdog_v9.9.9_linux_amd64.tar.gz" sheepdog
	tar -C "$fpkg" -czf "$frel/sheepdog_v9.9.9_darwin_arm64.tar.gz" sheepdog
	(cd "$frel" && for f in *.tar.gz; do sum "$f"; done >checksums.txt)

	cat >"$work/hc-install.sh" <<EOF
#!/bin/sh
set -eu
echo "\${HC_INSTALL_DIR:-}" >>"$work/hc-fetched"
mkdir -p "\$HC_INSTALL_DIR"
cat >"\$HC_INSTALL_DIR/hc" <<'HCEOF'
#!/bin/sh
echo "hc 0.9.0"
HCEOF
chmod 755 "\$HC_INSTALL_DIR/hc"
EOF

	cat >"$work/fake-uv" <<EOF
#!/bin/sh
set -eu
printf '%s\n' "\$*" >>"$work/uv-calls"
install_dir=
reinstall=0
ver=
while [ "\$#" -gt 0 ]; do
	case "\$1" in
	python | install | --no-bin | --managed-python | --no-config) shift ;;
	--reinstall) reinstall=1; shift ;;
	--install-dir) install_dir=\$2; shift 2 ;;
	*) ver=\$1; shift ;;
	esac
done
if [ -n "\${SHEEPDOG_TEST_NEED_REINSTALL:-}" ] && [ "\$reinstall" -eq 0 ]; then
	exit 0
fi
mkdir -p "\$install_dir/cpython-\$ver-test/bin"
printf '#!/bin/sh\necho "%s"\n' "\$ver" >"\$install_dir/cpython-\$ver-test/bin/python3.14"
chmod 755 "\$install_dir/cpython-\$ver-test/bin/python3.14"
ln -sf python3.14 "\$install_dir/cpython-\$ver-test/bin/python3"
EOF
	chmod 755 "$work/fake-uv"

	cat >"$work/uv-install.sh" <<EOF
#!/bin/sh
set -eu
echo "\${UV_INSTALL_DIR:-}:\${UV_NO_MODIFY_PATH:-}" >>"$work/uv-fetched"
mkdir -p "\$UV_INSTALL_DIR"
cp "$work/fake-uv" "\$UV_INSTALL_DIR/uv"
chmod 755 "\$UV_INSTALL_DIR/uv"
EOF

	fhome=$work/fhome
	mkdir -p "$fhome"
	out=$(HOME=$fhome SHELL=/bin/zsh PATH="$sysbin" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_RELEASE_URL="file://$frel" \
		SHEEPDOG_HC_INSTALL_URL="file://$work/hc-install.sh" \
		SHEEPDOG_UV_INSTALL_URL="file://$work/uv-install.sh" \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "factory install succeeds on bare PATH" "$rc"
	[ "$rc" -eq 0 ] || printf '  install output: %s\n' "$out" >&2
	if [ -x "$bin/hc" ] && [ -f "$work/hc-fetched" ]; then rc=0; else rc=1; fi
	check "fetches hc into install_dir when missing" "$rc"
	if [ -f "$work/uv-fetched" ] && grep -qx "$bin:1" "$work/uv-fetched" && [ -x "$bin/uv" ]; then rc=0; else rc=1; fi
	check "fetches uv into install_dir with UV_NO_MODIFY_PATH=1" "$rc"
	if [ -f "$work/uv-calls" ] && grep -q "python install --install-dir $fhome/.local/share/sheepdog/factory/python --no-bin --managed-python --no-config 3.14.8" "$work/uv-calls"; then rc=0; else rc=1; fi
	check "runs uv python install into sheepdog data folder" "$rc"
	if [ -x "$fhome/.local/share/sheepdog/factory/python/cpython-3.14.8-test/bin/python3" ]; then rc=0; else rc=1; fi
	check "pinned managed python binary exists in data folder" "$rc"
	if [ ! -e "$work/poison-python3" ]; then rc=0; else rc=1; fi
	check "never invokes system python3" "$rc"
	case "$out" in *"Installed managed Python 3.14.8 into $fhome/.local/share/sheepdog/factory/python."*) rc=0 ;; *) rc=1 ;; esac
	check "prints managed Python install confirmation" "$rc"

	# Second run with hc and uv already in install_dir fetches neither again,
	# and repairs a damaged Python installation with --reinstall.
	rm -f "$work/hc-fetched" "$work/uv-fetched" "$work/uv-calls"
	rm -f "$fhome/.local/share/sheepdog/factory/python/cpython-3.14.8-test/bin/python3.14"
	out=$(HOME=$fhome SHELL=/bin/zsh PATH="$sysbin" SHEEPDOG_INSTALL_DIR=$bin \
		SHEEPDOG_RELEASE_URL="file://$frel" \
		SHEEPDOG_HC_INSTALL_URL="file://$work/hc-install.sh" \
		SHEEPDOG_UV_INSTALL_URL="file://$work/uv-install.sh" \
		SHEEPDOG_TEST_NEED_REINSTALL=1 \
		SHEEPDOG_NO_SETUP=1 sh "$here/install.sh" 2>&1) && rc=0 || rc=$?
	check "rerun with damaged python succeeds" "$rc"
	[ "$rc" -eq 0 ] || printf '  rerun output: %s\n' "$out" >&2
	if [ ! -e "$work/hc-fetched" ] && [ ! -e "$work/uv-fetched" ]; then rc=0; else rc=1; fi
	check "skips fetching hc and uv when already present" "$rc"
	if [ -f "$work/uv-calls" ] && grep -q "python install --reinstall --install-dir $fhome/.local/share/sheepdog/factory/python --no-bin --managed-python --no-config 3.14.8" "$work/uv-calls"; then rc=0; else rc=1; fi
	check "repairs damaged python with --reinstall" "$rc"
	if [ ! -e "$work/poison-python3" ]; then rc=0; else rc=1; fi
	check "rerun still never invokes system python3" "$rc"
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
