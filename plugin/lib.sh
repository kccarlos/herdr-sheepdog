# Sourced by install.sh/uninstall.sh. run CMD... executes it, unless
# SHEEPDOG_DRY_RUN=1, in which case it only prints the command.
run() {
  if [ -n "${SHEEPDOG_DRY_RUN:-}" ]; then
    printf '[dry-run]'
    for a in "$@"; do printf ' %s' "$a"; done
    printf '\n'
  else
    "$@"
  fi
}
