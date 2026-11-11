#!/usr/bin/env bash
# Install the nightly regression as a LaunchAgent (03:00 every day).
#
#   scripts/e2e/nightly-install.sh [--ref origin/<branch>] [--uninstall]
#
# Makes a dedicated clone at ~/.sheepdog-nightly/herdr-sheepdog (so the job
# never runs from, or changes, a checkout you work in), writes
# ~/Library/LaunchAgents/io.github.kccarlos.sheepdog-nightly.plist from the
# template beside this script and loads it into your login session. Each run
# moves the clone to origin/main (or --ref while main has no nightly.sh yet)
# before it starts. Run it again to change --ref; --uninstall unloads and
# removes the plist (the clone and the reports stay).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABEL=io.github.kccarlos.sheepdog-nightly
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
CLONE="${SHEEPDOG_NIGHTLY_CLONE:-$HOME/.sheepdog-nightly/herdr-sheepdog}"
URL="https://github.com/kccarlos/herdr-sheepdog"
REF=origin/main
TART_HOME_V="${TART_HOME:-/Volumes/Ext/dev/vms/tart}"
DOMAIN="gui/$(id -u)"

while [ $# -gt 0 ]; do
  case "$1" in
    --ref) REF="${2:?--ref needs origin/<branch>}"; shift 2;;
    --uninstall)
      launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
      rm -f "$PLIST"
      echo "nightly: uninstalled ($PLIST removed; $CLONE and ~/Library/Logs/sheepdog-nightly kept)"
      exit 0
      ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0;;
    *) echo "nightly-install: unknown argument: $1" >&2; exit 2;;
  esac
done

case "$CLONE" in *" "*) echo "nightly-install: the clone path must not contain spaces: $CLONE" >&2; exit 2;; esac
if [ ! -d "$CLONE/.git" ]; then
  mkdir -p "$(dirname "$CLONE")"
  git clone -q "$URL" "$CLONE"
fi
git -C "$CLONE" fetch -q --prune origin
git -C "$CLONE" rev-parse -q --verify "$REF" >/dev/null || { echo "nightly-install: no $REF in $CLONE" >&2; exit 1; }
git -C "$CLONE" checkout -q --detach --force "$REF"
[ -x "$CLONE/scripts/e2e/nightly.sh" ] || { echo "nightly-install: $REF has no scripts/e2e/nightly.sh; pass --ref" >&2; exit 1; }

mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/sheepdog-nightly"
esc() { printf '%s' "$1" | sed -e 's/[&|\\]/\\&/g'; }
sed -e "s|@CLONE@|$(esc "$CLONE")|g" -e "s|@HOME@|$(esc "$HOME")|g" \
    -e "s|@REF@|$(esc "$REF")|g" -e "s|@TART_HOME@|$(esc "$TART_HOME_V")|g" \
    "$HERE/$LABEL.plist" >"$PLIST.new"
plutil -lint "$PLIST.new" >/dev/null
mv "$PLIST.new" "$PLIST"

launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
launchctl bootstrap "$DOMAIN" "$PLIST"
echo "nightly: installed $PLIST (03:00 daily, $CLONE at $REF)"
launchctl print "$DOMAIN/$LABEL" | grep -E '^\s*(state|path) =' || true
echo "run it now: launchctl kickstart $DOMAIN/$LABEL"
