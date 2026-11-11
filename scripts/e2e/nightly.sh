#!/usr/bin/env bash
# Nightly regression (docs/dev/e2e.md, "Nightly regression").
#
#   scripts/e2e/nightly.sh [--only vm,linux,real,audit] [--no-fixer]
#
#   vm     reset the Tart VM sd-qa from sd-qa-base, check out this commit,
#          run selftest.sh and panel-auto.sh (real keys) inside, copy the
#          screenshots back, stop the VM
#   linux  on `ssh oci`: check out this commit, go test ./...,
#          selftest.sh --no-herdr-app, and the real-agent checks
#   real   on this Mac: real voice (Gemini Live, 2 turns) and real agents
#          (Claude Code on Haiku, Codex), within scripts/e2e/budget.sh
#
# Writes ~/Library/Logs/sheepdog-nightly/<YYYY-MM-DD>/report.md with every
# step's log and screenshots, and latest.json beside it. On a failure: a macOS
# notification, then (unless --no-fixer) a headless Claude Code session that
# tries a fix in a fresh worktree and opens a PR. Every step is time-limited.
# Exits non-zero when any step failed.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
export TART_HOME="${TART_HOME:-/Volumes/Ext/dev/vms/tart}"

LOGROOT="${SHEEPDOG_NIGHTLY_LOGS:-$HOME/Library/Logs/sheepdog-nightly}"
VM="${SHEEPDOG_NIGHTLY_VM:-sd-qa}"
VM_BASE="${SHEEPDOG_NIGHTLY_VM_BASE:-sd-qa-base}"
VM_KEY="${SHEEPDOG_NIGHTLY_VM_KEY:-$HOME/.ssh/tart_sdqa_ed25519}"
LINUX="${SHEEPDOG_NIGHTLY_LINUX:-oci}"
REPO_SLUG="kccarlos/herdr-sheepdog"
SSHOPT=(-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 -o ServerAliveCountMax=4)
VMSSH=("${SSHOPT[@]}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i "$VM_KEY")

# ---- internal subcommands (each one step, run under a time limit) ----------

vm_ip() { tart ip "$VM" 2>/dev/null; }
vm_ssh() { ssh "${VMSSH[@]}" "admin@$(vm_ip)" "$@"; }

# The commands a remote checkout runs first: PATH, then this exact commit.
remote_prelude() {
  cat <<EOF
set -uo pipefail
export PATH="\$HOME/sdk/go1.27.3/bin:\$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:\$PATH"
cd "\$HOME/herdr-sheepdog" || exit 1
EOF
}

case "${1:-}" in
  __vm-boot)
    # Under launchd, the first read of an external disk waits for a macOS
    # consent dialog ("bash" would like to access files on a removable
    # volume); without a click it never returns.
    if ! perl -e 'alarm 30; exec @ARGV' ls "$TART_HOME" >/dev/null 2>&1; then
      echo "cannot read $TART_HOME within 30 s: under launchd, allow /bin/bash to access removable volumes"
      echo "(System Settings > Privacy & Security > Files and Folders, or click Allow on the dialog), or mount the disk"
      exit 1
    fi
    tart list --quiet 2>/dev/null | grep -qx "$VM_BASE" || { echo "no VM $VM_BASE (the clean snapshot)"; exit 1; }
    tart stop "$VM" >/dev/null 2>&1
    tart delete "$VM" >/dev/null 2>&1
    tart clone "$VM_BASE" "$VM" || exit 1
    nohup tart run "$VM" --no-graphics >"$2/tart-run.log" 2>&1 &
    echo $! >"$2/tart.pid"
    for _ in $(seq 1 120); do
      if [ -n "$(vm_ip)" ] && vm_ssh true 2>/dev/null; then echo "VM $VM up at $(vm_ip)"; exit 0; fi
      sleep 2
    done
    echo "VM $VM never answered ssh"; exit 1
    ;;
  __vm-run) # __vm-run <ref> <command>
    { remote_prelude
      echo "GIT_TERMINAL_PROMPT=0 git fetch -q origin && git checkout -q --detach --force '$2' || { echo 'checkout of $2 failed'; exit 1; }"
      echo "echo \"VM at \$(git rev-parse --short HEAD)\""
      echo "( $3 ) </dev/null"
    } | vm_ssh 'bash -s'; exit $?
    ;;
  __vm-fetch) # __vm-fetch <remote dir> <local dir>
    mkdir -p "$3"
    vm_ssh "test -d $2" || { echo "no $2 in the VM (the step before failed early)"; exit 0; }
    scp -q -r "${VMSSH[@]}" "admin@$(vm_ip):$2/." "$3/"; exit $?
    ;;
  __vm-stop)
    tart stop "$VM" >/dev/null 2>&1
    [ -f "$2/tart.pid" ] && kill "$(cat "$2/tart.pid")" 2>/dev/null
    echo "VM $VM stopped"
    exit 0
    ;;
  __linux-run) # __linux-run <ref> <seconds> <command>
    { remote_prelude
      echo "GIT_TERMINAL_PROMPT=0 git fetch -q origin && git checkout -q --detach '$2' || { echo 'checkout of $2 failed'; exit 1; }"
      echo "echo \"$LINUX at \$(git rev-parse --short HEAD)\""
      echo "timeout --kill-after=30 $3 bash -c '$4' </dev/null"
    } | ssh "${SSHOPT[@]}" "$LINUX" 'bash -s'; exit $?
    ;;
esac
case "${1:-}" in __*) echo "nightly: unknown subcommand $1" >&2; exit 2;; esac

# ---- arguments ---------------------------------------------------------------

ONLY="vm,linux,real,audit"
FIXER=1
while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="${2:?--only needs vm, linux, real and/or audit}"; shift 2;;
    --no-fixer) FIXER=0; shift;;
    -h|--help) sed -n '2,19p' "$0"; exit 0;;
    *) echo "nightly: unknown argument: $1" >&2; exit 2;;
  esac
done
want() { case ",$ONLY," in *",$1,"*) return 0;; esac; return 1; }

mkdir -p "$LOGROOT"
LOCK="$LOGROOT/nightly.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  if [ -f "$LOCK/pid" ] && kill -0 "$(cat "$LOCK/pid")" 2>/dev/null; then
    echo "nightly: another run is going (pid $(cat "$LOCK/pid"))" >&2; exit 2
  fi
  rm -rf "$LOCK"; mkdir "$LOCK"
fi
echo $$ >"$LOCK/pid"

# ---- self-update (the scheduled job only) -----------------------------------
# The LaunchAgent runs a dedicated clone with SHEEPDOG_NIGHTLY_UPDATE=1: move
# it to origin/main (or SHEEPDOG_NIGHTLY_REF while main has no nightly.sh yet)
# and start again from the new copy.
if [ "${SHEEPDOG_NIGHTLY_UPDATE:-0}" = 1 ] && [ -z "${SHEEPDOG_NIGHTLY_UPDATED:-}" ]; then
  if GIT_TERMINAL_PROMPT=0 git -C "$ROOT" fetch -q --prune origin; then
    TARGET=origin/main
    git -C "$ROOT" cat-file -e "origin/main:scripts/e2e/nightly.sh" 2>/dev/null || TARGET="${SHEEPDOG_NIGHTLY_REF:-origin/main}"
    if [ "$(git -C "$ROOT" rev-parse HEAD)" != "$(git -C "$ROOT" rev-parse "$TARGET" 2>/dev/null)" ]; then
      git -C "$ROOT" checkout -q --detach --force "$TARGET" && {
        rm -rf "$LOCK"
        AGAIN=(--only "$ONLY"); [ "$FIXER" = 0 ] && AGAIN+=(--no-fixer)
        SHEEPDOG_NIGHTLY_UPDATED=1 exec "$ROOT/scripts/e2e/nightly.sh" "${AGAIN[@]}"
      }
    fi
  fi
fi

# Go's caches stay on this disk: under launchd a go build reading an external
# disk can wait for a consent dialog nobody answers, even when ls gets through.
export GOMODCACHE="$HOME/Library/Caches/sheepdog-nightly/GOMODCACHE"
export GOCACHE="$HOME/Library/Caches/sheepdog-nightly/GOCACHE"

DAY=$(date +%F)
OUT="$LOGROOT/$DAY"
[ -e "$OUT" ] && OUT="$LOGROOT/$DAY-$(date +%H%M%S)"
mkdir -p "$OUT/shots"
SHA=$(git -C "$ROOT" rev-parse HEAD)
SHORT=${SHA:0:8}
# The remote machines test the same commit when it is pushed, else origin/main.
REMOTE_REF="$SHA"
REMOTE_NOTE=""
if [ -z "$(git -C "$ROOT" branch -r --contains "$SHA" 2>/dev/null)" ]; then
  REMOTE_REF=origin/main
  REMOTE_NOTE="HEAD $SHORT is not pushed, so the VM and Linux tested origin/main."
fi

ROWS=()
FAILED=()
STARTED=$(date +%s)

# limited <seconds> <cmd...>: runs it in its own process group, and at the
# limit ends the whole group with exit 142 (a plain alarm dies with the first
# process and bash ignores it, so a hung child would run on for ever). The VM
# has no timeout(1), and neither has a stock Mac.
limited() {
  perl -e '
    my $t = shift;
    my $p = fork // die "fork: $!\n";
    if (!$p) { setpgrp(0, 0); exec @ARGV or die "exec: $!\n"; }
    $SIG{ALRM} = sub { kill "TERM", -$p; sleep 5; kill "KILL", -$p; exit 142 };
    alarm $t;
    waitpid $p, 0;
    exit($? >> 8);
  ' "$@"
}

# step <name> <seconds> <cmd...>: run it under a time limit into <name>.log.
# Exit 0 is PASS, 3 SKIP, anything else FAIL (a time-out: 124 or 142).
step() {
  local name="$1" limit="$2" t0 rc res
  shift 2
  t0=$(date +%s)
  echo "nightly: $name ..."
  limited "$limit" "$@" >"$OUT/$name.log" 2>&1
  rc=$?
  case "$rc" in
    0) res=PASS;;
    3) res=SKIP;;
    124|142) res="FAIL (timed out after ${limit}s)";;
    *) res="FAIL (exit $rc)";;
  esac
  case "$res" in FAIL*) FAILED+=("$name");; esac
  ROWS+=("| $name | $res | $(( $(date +%s) - t0 ))s | [$name.log]($name.log) |")
  echo "nightly: $name $res"
  return "$rc"
}

# skip_step <name> <reason>: a SKIP row without running anything.
skip_step() {
  echo "SKIP  $2" >"$OUT/$1.log"
  ROWS+=("| $1 | SKIP | 0s | [$1.log]($1.log) |")
  echo "nightly: $1 SKIP ($2)"
}

disk_ok() { [ "$(df -k / | awk 'NR==2{print $4}')" -gt 8388608 ]; }

# ---- vm ----------------------------------------------------------------------
if want vm; then
  if ! command -v tart >/dev/null 2>&1; then
    skip_step vm "tart is not installed"
  elif [ ! -d "$TART_HOME" ]; then
    FAILED+=(vm-boot); ROWS+=("| vm-boot | FAIL (no $TART_HOME: is the external disk mounted?) | 0s | |")
  else
    if step vm-boot 400 "$0" __vm-boot "$OUT"; then
      step vm-selftest 1500 "$0" __vm-run "$REMOTE_REF" 'scripts/e2e/selftest.sh'
      step vm-panel 900 "$0" __vm-run "$REMOTE_REF" 'rm -rf /tmp/nightly-shots; scripts/e2e/panel-auto.sh --shots /tmp/nightly-shots'
      step vm-shots 120 "$0" __vm-fetch /tmp/nightly-shots "$OUT/shots/vm"
    fi
    [ -f "$OUT/tart.pid" ] && step vm-stop 120 "$0" __vm-stop "$OUT"
  fi
fi

# ---- linux -------------------------------------------------------------------
if want linux; then
  if ! ssh "${SSHOPT[@]}" "$LINUX" true 2>/dev/null; then
    FAILED+=(linux-ssh); ROWS+=("| linux-ssh | FAIL (ssh $LINUX does not answer) | 0s | |")
  else
    step linux-go-test 2100 "$0" __linux-run "$REMOTE_REF" 1800 'go test ./...'
    step linux-selftest 1000 "$0" __linux-run "$REMOTE_REF" 900 'scripts/e2e/selftest.sh --no-herdr-app'
    for a in claude codex; do
      if "$HERE/budget.sh" reserve agent 1 >"$OUT/budget-linux-$a.txt" 2>&1; then
        step "linux-agent-$a" 700 "$0" __linux-run "$REMOTE_REF" 600 "scripts/e2e/real-agent.sh $a --budget-reserved --timeout 300"
      else
        skip_step "linux-agent-$a" "agent budget used up today ($(cat "$OUT/budget-linux-$a.txt"))"
      fi
    done
  fi
fi

# ---- real (this Mac) ---------------------------------------------------------
if want real; then
  if ! disk_ok; then
    FAILED+=(real-disk); ROWS+=("| real-disk | FAIL (less than 8 GB free on /) | 0s | |")
  else
    step real-voice 600 "$HERE/real-voice.sh" --out "$OUT"
    step real-agent-claude 600 env NIGHTLY_OUT="$OUT" "$HERE/real-agent.sh" claude --timeout 300
    step real-agent-codex 600 env NIGHTLY_OUT="$OUT" "$HERE/real-agent.sh" codex --timeout 300
  fi
fi

# ---- audit -------------------------------------------------------------------
if want audit; then
  # Findings and operational problems do not turn the regression red.
  step audit 8400 "$HERE/audit.sh" "$OUT" "$SHA" "$DAY" "$FIXER"
  audit_rc=$?
  if [ "$audit_rc" != 0 ] && [ "$audit_rc" != 3 ]; then
    unset 'FAILED[${#FAILED[@]}-1]'
    ROWS[${#ROWS[@]}-1]="| audit | SKIP | 8400s or less | [audit.log](audit.log) |"
    echo "SKIP audit did not complete (exit $audit_rc)" >>"$OUT/audit.log"
  fi
fi

# ---- report ------------------------------------------------------------------
STATUS=PASS
[ "${#FAILED[@]}" -gt 0 ] && STATUS=FAIL
REPORT="$OUT/report.md"
{
  echo "# Sheepdog nightly $DAY"
  echo
  echo "**$STATUS**$([ "$STATUS" = FAIL ] && echo ": ${FAILED[*]}") · commit \`$SHORT\` · $(( ($(date +%s) - STARTED) / 60 )) min · parts: $ONLY"
  [ -n "$REMOTE_NOTE" ] && { echo; echo "$REMOTE_NOTE"; }
  echo
  echo "| Step | Result | Time | Log |"
  echo "|---|---|---|---|"
  printf '%s\n' "${ROWS[@]}"
  echo
  [ -f "$OUT/audit.md" ] && { echo "[Code audit](audit.md)"; echo; }
  echo "## Checks"
  for f in "$OUT"/*.log; do
    [ -f "$f" ] || continue
    LINES=$(grep -E '^(PASS|FAIL|SKIP) |^\| |^selftest:|^panel-auto:' "$f" | grep -v '^| *---' | head -60)
    [ -n "$LINES" ] || continue
    echo
    echo "### $(basename "$f" .log)"
    echo
    echo '```'
    echo "$LINES"
    echo '```'
  done
  if ls "$OUT"/shots/vm/*.png >/dev/null 2>&1; then
    echo
    echo "## Screenshots (VM)"
    for p in "$OUT"/shots/vm/*.png; do
      echo
      echo "$(basename "$p" .png): ![$(basename "$p" .png)](shots/vm/$(basename "$p"))"
    done
  fi
  echo
  echo "## Budget today"
  echo
  echo '```'
  "$HERE/budget.sh" show
  echo '```'
} >"$REPORT"

write_latest() { # write_latest <fixer_pr>; full runs only (a partial run is not the night's state)
  [ "$ONLY" = "vm,linux,real,audit" ] || return 0
  jq -n --arg date "$DAY" --arg status "$STATUS" --arg report "$REPORT" --arg commit "$SHA" \
    --arg failed "${FAILED[*]:-}" --arg pr "$1" \
    '{date:$date, status:$status, report:$report, commit:$commit,
      failed:($failed | split(" ") | map(select(. != ""))), fixer_pr:(if $pr == "" then null else $pr end)}' \
    >"$LOGROOT/latest.json"
}
write_latest ""
rm -rf "$LOCK"
echo "nightly: $STATUS; report $REPORT"
[ "$STATUS" = PASS ] && exit 0

osascript -e "display notification \"Failed: ${FAILED[*]}. Report: $REPORT\" with title \"Sheepdog nightly failed\" sound name \"Basso\"" >/dev/null 2>&1

# ---- fixer -------------------------------------------------------------------
# A headless Claude Code session that reproduces the first failure, fixes it
# with tests, and opens a PR (never merges). Guarded: --no-fixer, one at a
# time, not when an open nightly PR already names that check, and not with
# 20 % or less of the 5-hour Claude quota left.
fixer_note() { echo "$1" >"$OUT/fixer.txt"; echo "nightly: fixer: $1" >&2; }
run_fixer() {
  [ "$FIXER" = 1 ] || { fixer_note "skipped (--no-fixer)"; return; }
  command -v claude >/dev/null 2>&1 || { fixer_note "skipped (no claude CLI)"; return; }
  local left first flock wt prompt pr
  left=$(codexbar usage --provider claude --no-color 2>/dev/null | sed -n 's/^Session: *\([0-9][0-9]*\)% left.*/\1/p' | head -1)
  [ -n "$left" ] || { fixer_note "skipped (Claude quota unknown: codexbar gave no Session line)"; return; }
  [ "$left" -gt 20 ] || { fixer_note "skipped (only $left% of the 5-hour Claude quota left)"; return; }
  # Machine failures (VM disk or boot, ssh, disk space) are not code to fix.
  first=""
  local f
  for f in "${FAILED[@]}"; do
    case "$f" in vm-boot|vm-stop|vm-shots|linux-ssh|real-disk) ;; *) first="$f"; break;; esac
  done
  [ -n "$first" ] || { fixer_note "skipped (only machine failures: ${FAILED[*]})"; return; }
  if command -v gh >/dev/null 2>&1; then
    local open
    open=$(gh pr list --repo "$REPO_SLUG" --state open --search "nightly in:title" --json title,url \
      -q ".[] | select(.title | contains(\"$first\")) | .url" 2>/dev/null | head -1)
    [ -n "$open" ] && { fixer_note "skipped (open PR for $first: $open)"; echo "$open"; return; }
  fi
  flock="$LOGROOT/fixer.lock"
  if ! mkdir "$flock" 2>/dev/null; then
    if [ -f "$flock/pid" ] && kill -0 "$(cat "$flock/pid")" 2>/dev/null; then fixer_note "skipped (another fixer is running)"; return; fi
    rm -rf "$flock"; mkdir "$flock" || return
  fi
  echo $$ >"$flock/pid"
  wt="/tmp/sheepdog-nightly-fix-$DAY-$$"
  if ! git -C "$ROOT" worktree add -q --detach "$wt" "$SHA" >"$OUT/fixer-worktree.log" 2>&1; then
    fixer_note "skipped (could not make the worktree $wt)"; rm -rf "$flock"; return
  fi
  prompt="The sheepdog nightly regression failed on $DAY (commit $SHORT; failed steps: ${FAILED[*]}).
The report is $REPORT; every step's log is beside it in $OUT. You are in a fresh git worktree of the repo at $wt.

Do this:
1. Read AGENTS.md and docs/dev/e2e.md, then the report and the failing logs.
2. Reproduce the first failure ($first) with scripts/e2e (up.sh, scenario.sh, selftest.sh, panel-auto.sh, real-agent.sh, real-voice.sh). Real voice and real agents cost money: they count in scripts/e2e/budget.sh and refuse past the daily cap; use at most 2 voice turns and 3 agent tasks.
3. Find the root cause. If it is not a sheepdog bug (a machine down, a CLI logged out, a flaky external service), do not change code: say so in FIX.md and stop.
4. Otherwise fix it with a test that fails before the fix, and verify: make test, make vet lint, scripts/e2e/selftest.sh, and the failing check again.
5. Push a branch and open a PR titled \"fix: nightly $DAY $first\". Never merge, never tag.
6. Write a short summary (cause, fix, evidence, PR link) to $OUT/FIX.md.

Rules: follow AGENTS.md (Conventional Commits; no Co-authored-by, session links or claude.ai URLs in commits or PRs). Never touch the live daemon on port 7777, ~/.config/sheepdog, ~/.local/share/sheepdog, ~/.config/herdr-app, the user's own hc core, Herdr window or Sheepdog.app. No pkill by pattern. Never print or store secrets. Never create Oracle Cloud disks, volumes, backups or instances. Before each heavy batch, check 'codexbar usage --provider claude --no-color'; at 5% session left or less, write what you have to FIX.md and stop."
  printf '%s\n' "$prompt" >"$OUT/fixer-prompt.txt"
  if [ "${SHEEPDOG_NIGHTLY_FIXER_DRYRUN:-0}" = 1 ]; then
    # Tests of the guards: everything but the Claude session.
    fixer_note "dry run: would run claude in $wt (prompt in fixer-prompt.txt)"
  else
    echo "nightly: fixer running in $wt (45 min limit)" >&2
    (cd "$wt" && perl -e '$t = shift; alarm $t; exec @ARGV or die "exec: $!\n"' 2700 \
      claude -p "$prompt" --model sonnet --permission-mode auto \
      --settings '{"includeCoAuthoredBy":false,"attribution":{"commit":"","pr":""}}') >"$OUT/fixer.log" 2>&1
    fixer_note "ran (exit $?); see fixer.log and FIX.md"
  fi
  pr=$(gh pr list --repo "$REPO_SLUG" --state open --search "nightly $DAY in:title" --json url -q '.[0].url' 2>/dev/null)
  if [ -n "$pr" ] || [ "${SHEEPDOG_NIGHTLY_FIXER_DRYRUN:-0}" = 1 ]; then
    git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1
  else
    echo "no PR; the fixer's worktree is kept at $wt" >>"$OUT/fixer.txt"
  fi
  rm -rf "$flock"
  echo "$pr"
}
PR=$(run_fixer | tail -1)
case "$PR" in https://*) ;; *) PR="";; esac
write_latest "$PR"
{ echo; echo "## Fixer"; echo; cat "$OUT/fixer.txt" 2>/dev/null; [ -n "$PR" ] && echo "PR: $PR"; } >>"$REPORT"
exit 1
