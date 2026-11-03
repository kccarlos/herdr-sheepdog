#!/usr/bin/env bash
# Save the private window's last frame: scripts/e2e/shot.sh <dir> <name>
# Writes <dir>/shots/<name>.png and prints `herdr-app status`.
set -euo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

e2e_load "${1:-}"
NAME="${2:?usage: shot.sh <dir> <name>}"
[ "${E2E_WITH_APP:-0}" = 1 ] || e2e_die "this environment has no Herdr window (--no-herdr-app)"
mkdir -p "$E2E_DIR/shots"
OUT="$E2E_DIR/shots/$NAME.png"
e2e_app shot "$OUT" >/dev/null
echo "shot: $OUT"
echo "--- herdr-app status ---"
e2e_app status
