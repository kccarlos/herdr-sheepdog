#!/usr/bin/env bash
# Run steps 1-5 of docs/dev/factory/m1-acceptance.md against a private throwaway
# GitHub repo and verify the resulting PR is merged on GitHub via `gh pr view`.
# Reads GH_TOKEN from the environment and never prints or writes it to disk.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: m1-pr-acceptance.sh <owner/repo> [--agent CLI] [--model MODEL] [--prefix PREFIX] [--keep]
       m1-pr-acceptance.sh <owner/repo> --vm <name> [--vm-base <snapshot>] [--vm-user USER] [--vm-key PATH] [--sheepdog BIN] [--agent CLI] [--model MODEL] [--prefix PREFIX] [--keep]

Environment:
  GH_TOKEN   Fine-grained GitHub token scoped only to <owner/repo>
             (Contents: read/write, Pull requests: read/write). Required.
  TART_HOME  Tart VM directory when --vm is used (else machine.tart_home in settings.json).
EOF
}

REPO=""
AGENT=""
MODEL=""
PREFIX="DEMO"
KEEP=0
VM=""
VM_BASE=""
VM_USER="admin"
VM_KEY="${SHEEPDOG_NIGHTLY_VM_KEY:-$HOME/.ssh/tart_sdqa_ed25519}"
SHEEPDOG_BIN=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --agent)
      AGENT="${2:?--agent requires a value}"
      shift 2
      ;;
    --model)
      MODEL="${2:?--model requires a value}"
      shift 2
      ;;
    --prefix)
      PREFIX="${2:?--prefix requires a value}"
      shift 2
      ;;
    --vm)
      VM="${2:?--vm requires a value}"
      shift 2
      ;;
    --vm-base)
      VM_BASE="${2:?--vm-base requires a value}"
      shift 2
      ;;
    --vm-user)
      VM_USER="${2:?--vm-user requires a value}"
      shift 2
      ;;
    --vm-key)
      VM_KEY="${2:?--vm-key requires a value}"
      shift 2
      ;;
    --sheepdog)
      SHEEPDOG_BIN="${2:?--sheepdog requires a value}"
      shift 2
      ;;
    --keep)
      KEEP=1
      shift
      ;;
    -*)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      if [ -n "$REPO" ]; then
        echo "Unexpected argument: $1" >&2
        usage >&2
        exit 2
      fi
      REPO="$1"
      shift
      ;;
  esac
done

if [ -z "$REPO" ] || [[ "$REPO" != */* ]]; then
  echo "Error: <owner/repo> is required." >&2
  usage >&2
  exit 2
fi

if [ -z "${GH_TOKEN:-}" ]; then
  echo "Error: GH_TOKEN environment variable is required." >&2
  exit 2
fi

export PATH="$HOME/.local/bin:/opt/homebrew/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_SH="$SCRIPT_DIR/../e2e/lib.sh"

if [ -n "$VM" ]; then
  case "$VM" in
    sd-qa-base|factory-vm|factory-base-*)
      echo "Error: refusing to run against protected base VM '$VM'; pass a clone name." >&2
      exit 2
      ;;
  esac
  if [ -n "$VM_BASE" ] && [ "$VM" = "$VM_BASE" ]; then
    echo "Error: --vm and --vm-base cannot name the same VM ('$VM')." >&2
    exit 2
  fi
  if [ -f "$LIB_SH" ]; then
    # shellcheck source=scripts/e2e/lib.sh
    . "$LIB_SH"
    RESOLVED_TART_HOME="$(e2e_resolve_tart_home)"
    if [ -n "$RESOLVED_TART_HOME" ]; then
      export TART_HOME="$RESOLVED_TART_HOME"
    fi
  fi
  for cmd in tart ssh scp python3; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "Error: missing required command: $cmd" >&2
      exit 1
    fi
  done
  TART_VMS="$(tart list --quiet 2>/dev/null || true)"
  if [ -z "$TART_VMS" ]; then
    echo "Error: tart list is empty. VMs live under TART_HOME; set it to the folder that holds them." >&2
    exit 1
  fi
  VM_SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=30 -o ServerAliveCountMax=4 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
  if [ -n "$VM_KEY" ] && [ -f "$VM_KEY" ]; then
    VM_SSH_OPTS+=(-i "$VM_KEY")
  fi
  VM_STARTED_HERE=0
  TART_LOG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/m1-pr-vm.XXXXXX")"
  vm_cleanup() {
    local rc=$?
    if [ "$KEEP" -eq 0 ] && [ "$VM_STARTED_HERE" -eq 1 ]; then
      tart stop "$VM" >/dev/null 2>&1 || true
      if [ -f "$TART_LOG_DIR/tart.pid" ]; then
        kill "$(cat "$TART_LOG_DIR/tart.pid")" >/dev/null 2>&1 || true
      fi
    fi
    rm -rf "$TART_LOG_DIR"
    exit "$rc"
  }
  trap vm_cleanup EXIT

  if [ -n "$VM_BASE" ]; then
    printf '%s\n' "$TART_VMS" | grep -qx "$VM_BASE" || { echo "Error: base VM $VM_BASE not found (TART_HOME=${TART_HOME:-default})" >&2; exit 1; }
    echo "==> Resetting Tart VM $VM from $VM_BASE"
    tart stop "$VM" >/dev/null 2>&1 || true
    tart delete "$VM" >/dev/null 2>&1 || true
    tart clone "$VM_BASE" "$VM"
  fi
  VM_IP="$(tart ip "$VM" 2>/dev/null || true)"
  if [ -z "$VM_IP" ] || ! ssh "${VM_SSH_OPTS[@]}" "$VM_USER@$VM_IP" true 2>/dev/null; then
    echo "==> Booting Tart VM $VM"
    nohup tart run "$VM" --no-graphics >"$TART_LOG_DIR/tart-run.log" 2>&1 </dev/null &
    echo $! >"$TART_LOG_DIR/tart.pid"
    VM_STARTED_HERE=1
    for _ in $(seq 1 60); do
      VM_IP="$(tart ip "$VM" 2>/dev/null || true)"
      if [ -n "$VM_IP" ] && ssh "${VM_SSH_OPTS[@]}" "$VM_USER@$VM_IP" true 2>/dev/null; then
        break
      fi
      sleep 2
    done
  fi
  [ -n "$VM_IP" ] && ssh "${VM_SSH_OPTS[@]}" "$VM_USER@$VM_IP" true 2>/dev/null || { echo "Error: Tart VM $VM did not answer SSH" >&2; exit 1; }
  echo "==> Tart VM $VM up at $VM_IP"
  if [ -n "$SHEEPDOG_BIN" ]; then
    [ -x "$SHEEPDOG_BIN" ] || { echo "Error: --sheepdog binary not executable: $SHEEPDOG_BIN" >&2; exit 1; }
    ssh "${VM_SSH_OPTS[@]}" "$VM_USER@$VM_IP" 'mkdir -p "$HOME/.local/bin" && rm -f "$HOME/.local/bin/sheepdog"'
    scp -q "${VM_SSH_OPTS[@]}" "$SHEEPDOG_BIN" "$VM_USER@$VM_IP:.local/bin/sheepdog"
  fi
  REMOTE_SCRIPT="/tmp/m1-pr-acceptance.$$.sh"
  scp -q "${VM_SSH_OPTS[@]}" "$0" "$VM_USER@$VM_IP:$REMOTE_SCRIPT"
  REMOTE_ARGS=("$REPO" --prefix "$PREFIX")
  [ -n "$AGENT" ] && REMOTE_ARGS+=(--agent "$AGENT")
  [ -n "$MODEL" ] && REMOTE_ARGS+=(--model "$MODEL")
  [ "$KEEP" -eq 1 ] && REMOTE_ARGS+=(--keep)
  REMOTE_CMD=""
  for a in "${REMOTE_ARGS[@]}"; do
    REMOTE_CMD+=" $(printf '%q' "$a")"
  done
  printf '%s\n' "$GH_TOKEN" | ssh "${VM_SSH_OPTS[@]}" "$VM_USER@$VM_IP" \
    "IFS= read -r GH_TOKEN && export GH_TOKEN && trap 'rm -f $REMOTE_SCRIPT' EXIT && bash $REMOTE_SCRIPT$REMOTE_CMD"
  exit $?
fi

for cmd in git gh hc sheepdog python3; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Error: missing required command: $cmd" >&2
    exit 1
  fi
done

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/m1-pr-acceptance.XXXXXX")"
REPO_DIR="$WORKDIR/repo"
BIN_DIR="$WORKDIR/bin"
mkdir -p "$BIN_DIR"
export PATH="$BIN_DIR:$PATH"
HC_RESTARTED=0
HC_RESTART_FRESH=""
if hc server restart --help 2>&1 | grep -q -- '--fresh'; then
  HC_RESTART_FRESH="--fresh"
fi
TASK_STARTED=0
TASK_DONE=0
TASK_ID="${PREFIX}-1"

cleanup() {
  local rc=$?
  if [ "$KEEP" -eq 0 ] && [ "$TASK_STARTED" -eq 1 ] && [ "$TASK_DONE" -eq 0 ] && [ -d "$REPO_DIR" ]; then
    herdr-task done "$TASK_ID" --folder "$REPO_DIR" </dev/null >/dev/null 2>&1 || true
  fi
  if [ "$HC_RESTARTED" -eq 1 ]; then
    if [ -n "$HC_RESTART_FRESH" ]; then
      env -u GH_TOKEN -u GITHUB_TOKEN hc server restart --fresh </dev/null >/dev/null 2>&1 || true
    else
      env -u GH_TOKEN -u GITHUB_TOKEN sh -c 'hc kill herdr-automations >/dev/null 2>&1 || true; hc server stop >/dev/null 2>&1 || true; hc server start >/dev/null 2>&1 || true' </dev/null >/dev/null 2>&1 || true
    fi
  fi
  if [ "$KEEP" -eq 0 ]; then
    rm -rf "$WORKDIR"
  else
    echo "Kept workdir: $WORKDIR" >&2
  fi
  exit "$rc"
}
trap cleanup EXIT

# Verify token access to the target repo without printing the token.
gh repo view "$REPO" --json nameWithOwner,isPrivate -q '"Repo: \(.nameWithOwner) (private=\(.isPrivate))"'

# Ensure hc worker terminals inherit GH_TOKEN when gh is not otherwise logged in.
if ! env -u GH_TOKEN -u GITHUB_TOKEN gh auth status >/dev/null 2>&1; then
  if [ -n "$HC_RESTART_FRESH" ]; then
    hc server restart --fresh </dev/null >/dev/null 2>&1
  else
    hc kill herdr-automations >/dev/null 2>&1 || true
    sleep 1
    hc rm herdr-automations >/dev/null 2>&1 || true
    hc server stop </dev/null >/dev/null 2>&1 || true
    hc server start </dev/null >/dev/null 2>&1
  fi
  HC_RESTARTED=1
fi

echo "==> Cloning $REPO into $REPO_DIR"
GIT_TERMINAL_PROMPT=0 GCM_INTERACTIVE=never git -c credential.helper= -c credential.https://github.com.helper='!gh auth git-credential' \
  clone "https://github.com/${REPO}.git" "$REPO_DIR"
git -C "$REPO_DIR" config --unset-all credential.helper >/dev/null 2>&1 || true
git -C "$REPO_DIR" config --unset-all credential.https://github.com.helper >/dev/null 2>&1 || true
git -C "$REPO_DIR" config --add credential.helper ''
git -C "$REPO_DIR" config --add credential.https://github.com.helper ''
git -C "$REPO_DIR" config --add credential.https://github.com.helper '!gh auth git-credential'
git -C "$REPO_DIR" config user.name "${GIT_AUTHOR_NAME:-M1 Acceptance}"
git -C "$REPO_DIR" config user.email "${GIT_AUTHOR_EMAIL:-m1-acceptance@example.com}"

# Bootstrap main if the throwaway repo was created empty.
if ! git -C "$REPO_DIR" rev-parse --verify HEAD >/dev/null 2>&1; then
  echo "==> Initializing empty repo with main branch"
  git -C "$REPO_DIR" checkout -b main
  printf '# M1 PR acceptance test\n' > "$REPO_DIR/README.md"
  git -C "$REPO_DIR" add README.md
  git -C "$REPO_DIR" commit -m "chore: initial commit"
  git -C "$REPO_DIR" push -u origin main
fi

echo "==> Step 1: sheepdog factory doctor"
sheepdog factory doctor --folder "$REPO_DIR" || true

echo "==> Step 2: ensure herdr-task wrapper on PATH"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/sheepdog"
PY_BIN="$(ls -1 "$DATA_DIR"/factory/python/cpython-3.14.*/bin/python3 2>/dev/null | head -n 1 || true)"
ENG_DIR="$(ls -1dt "$DATA_DIR"/factory/engine/* 2>/dev/null | head -n 1 || true)"
if [ -z "$PY_BIN" ] || [ -z "$ENG_DIR" ] || [ ! -f "$ENG_DIR/mods/tasks/bin/herdr-task" ]; then
  echo "Error: unpacked factory engine or managed python3.14 not found under $DATA_DIR/factory" >&2
  exit 1
fi
cat > "$BIN_DIR/herdr-task" <<EOF
#!/bin/sh
exec "$PY_BIN" "$ENG_DIR/mods/tasks/bin/herdr-task" "\$@"
EOF
chmod +x "$BIN_DIR/herdr-task"

echo "==> Step 3: configure $PREFIX with worker.perm=full"
herdr-task config --folder "$REPO_DIR" --prefix "$PREFIX" --set worker.perm=full

echo "==> Step 4: add $TASK_ID"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
ADD_ARGS=(
  add "Update HELLO.md ${RUN_ID}"
  --folder "$REPO_DIR"
  --prompt "Write 'hello (${RUN_ID})' to HELLO.md and note it in docs/proof/${TASK_ID}.md, commit both, then run ${BIN_DIR}/herdr-task ready ${TASK_ID} --folder ${REPO_DIR} --proof docs/proof/${TASK_ID}.md --summary \"Update HELLO.md (${RUN_ID})\""
)
if [ -n "$AGENT" ]; then
  ADD_ARGS+=(--agent "$AGENT")
fi
if [ -n "$MODEL" ]; then
  ADD_ARGS+=(--model "$MODEL")
fi
herdr-task "${ADD_ARGS[@]}"

echo "==> Step 5: start $TASK_ID and wait for Ready"
herdr-task start "$TASK_ID" --folder "$REPO_DIR"
TASK_STARTED=1

DEADLINE=$(( $(date +%s) + 300 ))
STATE=""
STATUS=""
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  if ! SHOW_JSON="$(herdr-task show "$TASK_ID" --folder "$REPO_DIR" --json 2>/dev/null)"; then
    sleep 3
    continue
  fi
  STATE="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("state",""))' <<<"$SHOW_JSON")"
  STATUS="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",""))' <<<"$SHOW_JSON")"
  if [ "$STATE" = "ready" ] && [ "$STATUS" = "ready" ]; then
    break
  fi
  if [ "$STATE" = "done" ]; then
    TASK_DONE=1
    break
  fi
  NEEDS_YOU="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("attention") or d.get("why") or d.get("needs") or "needs attention") if (d.get("attention") or d.get("status") == "needs") else "")' <<<"$SHOW_JSON")"
  if [ -n "$NEEDS_YOU" ]; then
    echo "Task $TASK_ID needs attention: $NEEDS_YOU" >&2
    exit 1
  fi
  sleep 3
done

if [ "$STATE" != "ready" ] || [ "$STATUS" != "ready" ]; then
  if [ "$STATE" != "done" ]; then
    echo "Error: timed out waiting for $TASK_ID to reach ready (state=$STATE status=$STATUS)" >&2
    herdr-task show "$TASK_ID" --folder "$REPO_DIR" >&2 || true
    exit 1
  fi
fi

PR_NUM="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("pr") or {}).get("number") or "")' <<<"$SHOW_JSON")"
echo "==> Task $TASK_ID is $STATE (PR #${PR_NUM:-none}); running accept"
herdr-task accept "$TASK_ID" --folder "$REPO_DIR"

MERGE_DEADLINE=$(( $(date +%s) + 180 ))
while [ "$(date +%s)" -lt "$MERGE_DEADLINE" ]; do
  if ! SHOW_JSON="$(herdr-task show "$TASK_ID" --folder "$REPO_DIR" --json 2>/dev/null)"; then
    sleep 3
    continue
  fi
  STATE="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("state",""))' <<<"$SHOW_JSON")"
  if [ "$STATE" = "done" ]; then
    TASK_DONE=1
    break
  fi
  NEEDS_YOU="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("attention") or d.get("why") or d.get("needs") or "needs attention") if (d.get("attention") or d.get("status") == "needs") else "")' <<<"$SHOW_JSON")"
  if [ -n "$NEEDS_YOU" ]; then
    echo "Task $TASK_ID stalled during merge: $NEEDS_YOU" >&2
    exit 1
  fi
  sleep 3
done

if [ "$STATE" != "done" ]; then
  echo "Error: timed out waiting for $TASK_ID to finish merging (state=$STATE)" >&2
  herdr-task show "$TASK_ID" --folder "$REPO_DIR" >&2 || true
  exit 1
fi

if [ -z "$PR_NUM" ]; then
  SHOW_JSON="$(herdr-task show "$TASK_ID" --folder "$REPO_DIR" --json)"
  PR_NUM="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("pr") or {}).get("number") or "")' <<<"$SHOW_JSON")"
fi

if [ -z "$PR_NUM" ]; then
  echo "Error: no pull request number recorded on $TASK_ID" >&2
  exit 1
fi

echo "==> Verifying PR #$PR_NUM on GitHub"
PR_VIEW="$(gh pr view "$PR_NUM" --repo "$REPO" --json number,url,state,mergedAt,mergeCommit)"
printf '%s\n' "$PR_VIEW"

python3 -c '
import json, sys
d = json.load(sys.stdin)
state = d.get("state")
num = d.get("number")
merged_at = d.get("mergedAt")
assert state == "MERGED", f"expected state=MERGED, got {state!r}"
assert merged_at, "expected non-empty mergedAt"
print(f"OK: PR #{num} is MERGED at {merged_at}")
' <<<"$PR_VIEW"
