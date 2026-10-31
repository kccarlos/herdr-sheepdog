#!/usr/bin/env bash
# Tests packaging/homebrew/render.sh and tap-update.sh against a scratch tap
# repository (no network, no push). Run: bash packaging/homebrew/tap-update_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0
check() { # check <name> <command...>
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails + 1)); fi
}

d=0ea8aa3c6aa50aea770527a7b3fae1a3ba7af08fe0850216aebffc022c4be777
l=49df9e7c629f410762200cd67d63382c0b7034884f57c098651f13ca399c4ce0
a=5d8f0c1e7b2a49d3a6c4e8f1b0d7a3c9e2f4b6a8d0c1e3f5a7b9d1c3e5f7a9b1
printf '%s  sheepdog_v9.9.9_darwin_arm64.tar.gz\n%s  sheepdog_v9.9.9_linux_amd64.tar.gz\n%s  Sheepdog-9.9.9-macos.zip\n' "$d" "$l" "$a" > "$work/checksums.txt"

CHECKSUMS_FILE="$work/checksums.txt" "$here/render.sh" v9.9.9 > "$work/sheepdog.rb"
check "render fills the darwin sum" grep -q "sha256 \"$d\"" "$work/sheepdog.rb"
check "render fills the linux sum" grep -q "sha256 \"$l\"" "$work/sheepdog.rb"
check "render fills the tag" grep -q 'download/v9.9.9/sheepdog_v9.9.9_darwin_arm64.tar.gz' "$work/sheepdog.rb"
check "render leaves no placeholder" bash -c "! grep -q @ '$work/sheepdog.rb'"
check "caveats say sheepdog setup" grep -q 'sheepdog setup ' "$work/sheepdog.rb"
if command -v ruby >/dev/null; then check "formula is valid ruby" ruby -c "$work/sheepdog.rb"; fi
printf 'sheepdog_v9.9.9_linux_amd64.tar.gz\n' > "$work/bad.txt"
check "render fails without a darwin sum" bash -c "! CHECKSUMS_FILE='$work/bad.txt' '$here/render.sh' v9.9.9"

CHECKSUMS_FILE="$work/checksums.txt" "$here/render.sh" --cask v9.9.9 > "$work/sheepdog-app.rb"
check "cask fills the app sum" grep -q "sha256 \"$a\"" "$work/sheepdog-app.rb"
check "cask fills the version" grep -q '^  version "9.9.9"$' "$work/sheepdog-app.rb"
check "cask fills the tag" grep -q 'download/v9.9.9/Sheepdog-#{version}-macos.zip' "$work/sheepdog-app.rb"
check "cask leaves no placeholder" bash -c "! grep -q @ '$work/sheepdog-app.rb'"
check "cask installs Sheepdog.app" grep -q '^  app "Sheepdog.app"$' "$work/sheepdog-app.rb"
if command -v ruby >/dev/null; then check "cask is valid ruby" ruby -c "$work/sheepdog-app.rb"; fi
# A pre-release tag keeps the zip's name (no suffix) and the tag in the URL.
sed 's/9\.9\.9/9.9.9-rc1/' "$work/checksums.txt" | sed 's/Sheepdog-9.9.9-rc1-macos/Sheepdog-9.9.9-macos/' > "$work/rc.txt"
CHECKSUMS_FILE="$work/rc.txt" "$here/render.sh" --cask v9.9.9-rc1 > "$work/rc.rb"
check "cask for a pre-release" grep -q 'download/v9.9.9-rc1/Sheepdog-#{version}-macos.zip' "$work/rc.rb"
check "cask fails without an app sum" bash -c "! CHECKSUMS_FILE='$work/bad.txt' '$here/render.sh' --cask v9.9.9"

# A scratch tap shaped like kccarlos/homebrew-tap.
tap="$work/tap"
mkdir -p "$tap/Casks"
cat > "$tap/README.md" <<'EOF'
# Homebrew tap

| App | What it does | Install (macOS) |
| --- | --- | --- |
| [A](https://example.com/a) | Does a. | `brew install --cask kccarlos/tap/a` |
| [B](https://example.com/b) | Does b. | `brew install --cask kccarlos/tap/b` |

Text after the table.
EOF
touch "$tap/Casks/a.rb"
git -C "$tap" init -q -b main
git -C "$tap" -c user.name=t -c user.email=t@example.com add -A
git -C "$tap" -c user.name=t -c user.email=t@example.com commit -qm init

out="$("$here/tap-update.sh" "$tap" "$work/sheepdog.rb" "$work/sheepdog-app.rb" v9.9.9)"
check "first run reports a change" grep -q '^changed=true$' <<<"$out"
check "formula written" cmp -s "$work/sheepdog.rb" "$tap/Formula/sheepdog.rb"
check "cask written" cmp -s "$work/sheepdog-app.rb" "$tap/Casks/sheepdog-app.rb"
check "one commit for both" test "$(git -C "$tap" show --name-only --format= HEAD | grep -c '\.rb$')" = 2
check "commit subject" test "$(git -C "$tap" log -1 --format=%s)" = "sheepdog 9.9.9"
check "commit author" test "$(git -C "$tap" log -1 --format='%an <%ae>')" = "KC <110118511+kccarlos@users.noreply.github.com>"
check "formula row added once" test "$(grep -c 'kccarlos/tap/sheepdog`' "$tap/README.md")" = 1
check "cask row added once" test "$(grep -c 'kccarlos/tap/sheepdog-app' "$tap/README.md")" = 1
check "formula row follows the table" test "$(sed -n 7p "$tap/README.md" | cut -c1-12)" = "| [sheepdog]"
check "cask row is the table's last row" test "$(sed -n 8p "$tap/README.md" | cut -c1-16)" = "| [Sheepdog.app]"
check "text after the table kept" grep -q '^Text after the table.$' "$tap/README.md"

out="$("$here/tap-update.sh" "$tap" "$work/sheepdog.rb" "$work/sheepdog-app.rb" v9.9.9)"
check "second run is a no-op" grep -q '^changed=false$' <<<"$out"
check "no second commit" test "$(git -C "$tap" rev-list --count HEAD)" = 2

# Only one of the two differs: fail and write nothing.
{ cat "$work/sheepdog-app.rb"; echo '# changed'; } > "$work/cask2.rb"
check "fails when only the cask would change" bash -c "! '$here/tap-update.sh' '$tap' '$work/sheepdog.rb' '$work/cask2.rb' v9.9.9"
check "the tap is untouched by that" test -z "$(git -C "$tap" status --porcelain)"
{ cat "$work/sheepdog.rb"; echo '# changed'; } > "$work/formula2.rb"
check "fails when only the formula would change" bash -c "! '$here/tap-update.sh' '$tap' '$work/formula2.rb' '$work/sheepdog-app.rb' v9.9.9"
check "fails on a cask for another tag" bash -c "! '$here/tap-update.sh' '$tap' '$work/sheepdog.rb' '$work/rc.rb' v9.9.9"

check "rejects a bad tag" bash -c "! '$here/tap-update.sh' '$tap' '$work/sheepdog.rb' '$work/sheepdog-app.rb' 9.9.9"

if (( fails )); then echo "$fails failed"; exit 1; fi
echo "all passed"
