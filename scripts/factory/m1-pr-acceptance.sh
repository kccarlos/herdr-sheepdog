#!/usr/bin/env bash
# Run steps 1-5 of docs/dev/factory/m1-acceptance.md against a private throwaway
# GitHub repo and verify the resulting PR is merged on GitHub via `gh pr view`.
# Reads GH_TOKEN from the environment and never prints or writes it to disk.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: m1-pr-acceptance.sh <owner/repo> [--agent CLI] [--model MODEL] [--prefix PREFIX] [--keep]

Environment:
  GH_TOKEN   Fine-grained GitHub token scoped only to <owner/repo>
             (Contents: read/write, Pull requests: read/write). Required.
EOF
}

REPO=""
AGENT=""
MODEL=""
PREFIX="DEMO"
KEEP=0

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
    env -u GH_TOKEN -u GITHUB_TOKEN hc server restart ${HC_RESTART_FRESH:+$HC_RESTART_FRESH} </dev/null >/dev/null 2>&1 || true
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
  hc server restart ${HC_RESTART_FRESH:+$HC_RESTART_FRESH} </dev/null >/dev/null 2>&1
  HC_RESTARTED=1
fi

echo "==> Cloning $REPO into $REPO_DIR"
git -c credential.https://github.com.helper='!gh auth git-credential' \
  clone "https://github.com/${REPO}.git" "$REPO_DIR"
git -C "$REPO_DIR" config credential.https://github.com.helper '!gh auth git-credential'
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
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  if ! SHOW_JSON="$(herdr-task show "$TASK_ID" --folder "$REPO_DIR" --json 2>/dev/null)"; then
    sleep 3
    continue
  fi
  STATE="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("state",""))' <<<"$SHOW_JSON")"
  if [ "$STATE" = "ready" ]; then
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

if [ "$STATE" != "ready" ] && [ "$STATE" != "done" ]; then
  echo "Error: timed out waiting for $TASK_ID to reach ready (state=$STATE)" >&2
  herdr-task show "$TASK_ID" --folder "$REPO_DIR" >&2 || true
  exit 1
fi

PR_NUM="$(python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("pr") or {}).get("number") or "")' <<<"$SHOW_JSON")"
echo "==> Task $TASK_ID is $STATE (PR #${PR_NUM:-none}); running accept"
herdr-task accept "$TASK_ID" --folder "$REPO_DIR"

MERGE_DEADLINE=$(( $(date +%s) + 120 ))
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
