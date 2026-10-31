#!/bin/sh
# Tests scripts/plugin-fetch.sh against a fake release directory (no
# network): good download, tampered download, no release with and without
# go. Run: sh scripts/plugin-fetch_test.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
fails=0
# Never touch the real ~/.local/bin: the script links sheepdog there.
export SHEEPDOG_LINK_DIR="$work/linkbin"

check() { # check <name> <condition exit status>
	if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails + 1)); fi
}

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}

# A copy of the plugin's scripts, so bin/ lands in a scratch root.
root=$work/root
mkdir -p "$root/scripts" "$work/rel" "$work/pkg"
cp "$here/plugin-fetch.sh" "$root/scripts/"

# Fake release: the tarballs of the two targets, and checksums.txt.
printf '#!/bin/sh\necho "sheepdog fake v9.9.9"\n' >"$work/pkg/sheepdog"
chmod 755 "$work/pkg/sheepdog"
tar -C "$work/pkg" -czf "$work/rel/sheepdog_v9.9.9_linux_amd64.tar.gz" sheepdog
tar -C "$work/pkg" -czf "$work/rel/sheepdog_v9.9.9_darwin_arm64.tar.gz" sheepdog
(cd "$work/rel" && for f in *.tar.gz; do sum "$f"; done >checksums.txt)

# 1. good release. Needs a target this script knows; skip elsewhere.
case "$(uname -s)_$(uname -m)" in
Darwin_arm64 | Linux_x86_64 | Linux_amd64)
	out=$(SHEEPDOG_RELEASE_URL="file://$work/rel" sh "$root/scripts/plugin-fetch.sh" 2>&1) && rc=0 || rc=$?
	check "release installs" "$rc"
	[ -x "$root/bin/sheepdog" ] && [ "$("$root/bin/sheepdog")" = "sheepdog fake v9.9.9" ]
	check "installed binary runs" "$?"
	case "$out" in *"sha256 verified"*) rc=0 ;; *) rc=1 ;; esac
	check "says it verified" "$rc"
	[ -L "$SHEEPDOG_LINK_DIR/sheepdog" ] && [ "$("$SHEEPDOG_LINK_DIR/sheepdog")" = "sheepdog fake v9.9.9" ]
	check "links sheepdog into the bin dir" "$?"
	case "$out" in *"is not on your PATH"*) rc=0 ;; *) rc=1 ;; esac
	check "warns the bin dir is not on PATH" "$rc"
	case "$out" in *'Next: run `sheepdog setup`'*) rc=0 ;; *) rc=1 ;; esac
	check "ends with the next step" "$rc"
	# Reinstall with the dir on PATH: no warning, the link replaced.
	out=$(PATH="$SHEEPDOG_LINK_DIR:$PATH" SHEEPDOG_RELEASE_URL="file://$work/rel" sh "$root/scripts/plugin-fetch.sh" 2>&1) && rc=0 || rc=$?
	case "$out" in *"is not on your PATH"*) rc=1 ;; esac
	check "no PATH warning when it is on PATH" "$rc"
	# A real file there is never replaced.
	rm "$SHEEPDOG_LINK_DIR/sheepdog"
	echo "mine" >"$SHEEPDOG_LINK_DIR/sheepdog"
	out=$(SHEEPDOG_RELEASE_URL="file://$work/rel" sh "$root/scripts/plugin-fetch.sh" 2>&1) && rc=0 || rc=$?
	[ "$rc" -eq 0 ] && [ "$(cat "$SHEEPDOG_LINK_DIR/sheepdog")" = "mine" ]
	check "keeps a file that is not a link" "$?"
	case "$out" in *"is not a link; left it alone"*) rc=0 ;; *) rc=1 ;; esac
	check "says it left the file alone" "$rc"
	rm -f "$SHEEPDOG_LINK_DIR/sheepdog"

	# 2. tampered tarball: must fail and must not touch bin/.
	rm -rf "${root:?}/bin"
	printf 'x' >>"$work/rel/sheepdog_v9.9.9_linux_amd64.tar.gz"
	printf 'x' >>"$work/rel/sheepdog_v9.9.9_darwin_arm64.tar.gz"
	SHEEPDOG_RELEASE_URL="file://$work/rel" sh "$root/scripts/plugin-fetch.sh" >"$work/out" 2>&1 && rc=1 || rc=0
	check "bad checksum fails" "$rc"
	grep -q "does not match checksums.txt" "$work/out"
	check "bad checksum says so" "$?"
	[ ! -e "$root/bin/sheepdog" ]
	check "bad checksum installs nothing" "$?"
	;;
*) echo "skip release cases on $(uname -s) $(uname -m)" ;;
esac

# 3. no release at all, no go on PATH: clear message. (curl and the
# coreutils stay reachable through a PATH of symlinks.)
nogo=$work/nogo
mkdir "$nogo"
for t in sh curl uname mktemp rm awk tar cp chmod mkdir dirname cat sha256sum shasum; do
	p=$(command -v "$t" 2>/dev/null || true)
	[ -z "$p" ] || ln -s "$p" "$nogo/$t"
done
PATH=$nogo SHEEPDOG_RELEASE_URL="file://$work/none" sh "$root/scripts/plugin-fetch.sh" >"$work/out" 2>&1 && rc=1 || rc=0
check "no release and no go fails" "$rc"
grep -q "go is not on PATH" "$work/out"
check "names the way out" "$?"

# 4. no release, go present: builds from source (the real module, from the
# repo this script lives in).
if command -v go >/dev/null 2>&1; then
	repo=$(cd "$here/.." && pwd)
	cp "$here/plugin-fetch.sh" "$work/pf.sh"
	mkdir -p "$work/repo/scripts"
	cp "$work/pf.sh" "$work/repo/scripts/plugin-fetch.sh"
	cp -R "$repo/cmd" "$repo/internal" "$repo/go.mod" "$repo/go.sum" "$work/repo/"
	for d in prompts web assets mods; do [ ! -d "$repo/$d" ] || cp -R "$repo/$d" "$work/repo/"; done
	# The root package (panel.go embeds mods/sheepdog for sheepdog setup).
	cp "$repo"/*.go "$work/repo/"
	SHEEPDOG_RELEASE_URL="file://$work/none" sh "$work/repo/scripts/plugin-fetch.sh" >"$work/out" 2>&1 && rc=0 || rc=$?
	check "no release, go builds" "$rc"
	[ -x "$work/repo/bin/sheepdog" ]
	check "built binary exists" "$?"
else
	echo "skip go fallback: go not on PATH"
fi

[ "$fails" -eq 0 ] || { echo "$fails failed"; exit 1; }
echo "all passed"
