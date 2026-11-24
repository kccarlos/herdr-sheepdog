#!/bin/zsh
# Shared helpers for factory e2e live-check scripts (factory-retro.sh, factory-plan-review.sh).
# Expects caller to set: D, R, U, C, DNAME, ENV_FILE, OP_TOKEN_FILE, J, L, PID.

fail()         { echo "== FAIL: $1"; exit 1; }
forcing_fail() { echo "== forcing failed: $1, run again"; exit 2; }
step()         { echo "== STEP $1 at $(date +%T)"; }

# Only stop a daemon this run started: tests run the script on an empty dir, and a
# fixed terminal name must never take down a live run started elsewhere.
STARTED=0
stopd() {
  [ "$STARTED" = 1 ] || return 0
  hc kill "$DNAME" >/dev/null 2>&1
  hc rm "$DNAME" >/dev/null 2>&1
  sleep 3
}

clear_project_id() {
  git -C "$R" config --local --unset sheepdog.projectId >/dev/null 2>&1 || true
}

cleanup_repo_prs_branches() {
  [ -n "${J:-}" ] && [ -f "$J" ] && [ -d "${R:-}/.git" ] || return 0
  local prs branches pr br
  prs=$(python3 - "$J" <<'PY' 2>/dev/null || true
import json, sys
s = json.load(open(sys.argv[1]))
out = sorted({int(a["pr"]) for t in (s.get("tasks") or []) for a in (t.get("attempts") or []) if int(a.get("pr") or 0) > 0})
print(" ".join(str(x) for x in out))
PY
  )
  branches=$(python3 - "$J" <<'PY' 2>/dev/null || true
import json, sys
s = json.load(open(sys.argv[1]))
out = sorted({str(a["branch"]) for t in (s.get("tasks") or []) for a in (t.get("attempts") or []) if a.get("branch")})
print(" ".join(out))
PY
  )
  for pr in ${=prs}; do
    (cd "$R" && gh pr close "$pr" --delete-branch) >/dev/null 2>&1 || true
  done
  for br in ${=branches}; do
    git -C "$R" push origin --delete "$br" >/dev/null 2>&1 || true
  done
}

cleanup_trap() {
  [ -n "${J:-}" ] && "$D/sheepdog" factory stop "$R" --config "$C" >/dev/null 2>&1 || true
  stopd
  if [ -n "${PID:-}" ]; then
    for t in $(hc ls --json 2>/dev/null | python3 -c "import sys,json;print(' '.join(t['address'] for t in json.load(sys.stdin).get('terminals',[]) if (t.get('labels') or {}).get('sheepdog.project')=='$PID'))" 2>/dev/null); do
      hc kill "$t" >/dev/null 2>&1; hc rm "$t" >/dev/null 2>&1
    done
  fi
  if typeset -f extra_cleanup >/dev/null 2>&1; then
    extra_cleanup || true
  fi
}

daemon() {
  if [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ]; then
    if [ -n "$OP_TOKEN_FILE" ] && [ -f "$OP_TOKEN_FILE" ]; then
      hc run --bg --json --name "$DNAME" --cwd "$D" -- zsh -c "OP_SERVICE_ACCOUNT_TOKEN=\"\$(<\"$OP_TOKEN_FILE\")\" exec op run --env-file=\"$ENV_FILE\" -- env -u OP_SERVICE_ACCOUNT_TOKEN \"$D/sheepdog\" serve --config \"$C\" --voice=off --brain=off --cli=off" >/dev/null || fail "hc run daemon"; STARTED=1
    else
      hc run --bg --json --name "$DNAME" --cwd "$D" -- op run --env-file="$ENV_FILE" -- "$D/sheepdog" serve --config "$C" --voice=off --brain=off --cli=off >/dev/null || fail "hc run daemon"; STARTED=1
    fi
  else
    hc run --bg --json --name "$DNAME" --cwd "$D" -- "$D/sheepdog" serve --config "$C" --voice=off --brain=off --cli=off >/dev/null || fail "hc run daemon"; STARTED=1
  fi
  for _ in $(seq 1 20); do curl -sf --max-time 2 "$U/api/state" >/dev/null && return 0; sleep 1; done
  fail "daemon did not answer on $U within 20s"
}

# wait_py <label> <timeout_sec> <poll_sec> <python_expr> [val]
# Expr sees: log (list of dicts from $L), state (dict from $J), val (optional arg).
wait_py() {
  local label=$1 lim=$2 step_s=$3 expr=$4 val=${5:-} elapsed=0 out=""
  while [ "$elapsed" -lt "$lim" ]; do
    out=$(python3 - "$L" "$J" "$expr" "$val" <<'PY'
import json, sys
lp, jp, expr, val = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
log = []
try:
    for line in open(lp):
        line = line.strip()
        if line:
            try: log.append(json.loads(line))
            except Exception: pass
except FileNotFoundError: pass
state = json.load(open(jp)) if jp else {}
res = eval(expr, {"log": log, "state": state, "val": val, "any": any, "all": all, "len": len, "max": max, "str": str})
if res:
    print(json.dumps(res) if isinstance(res, dict) else res)
    sys.exit(0)
sys.exit(1)
PY
    ) && { echo "   matched: ${out:0:220}"; return 0; }
    sleep "$step_s"; elapsed=$((elapsed + step_s))
  done
  fail "TIMEOUT (${lim}s) waiting for $label"
}

factory_preflight() {
  [ -x "$D/sheepdog" ] || fail "missing executable $D/sheepdog"
  [ -d "$R/.git" ] || fail "missing repo $R"
  clear_project_id
  mkdir -p "$D/data"
  if [ ! -f "$C" ]; then
    cat > "$C" <<EOF
data_dir = "$D/data"
[web]
bind = "127.0.0.1"
port = $PORT
[panel]
auto_update = false
[update]
check = false
EOF
  fi
}

# pin_agy_models <state_json> [extra_settings_json] [extra_models_json]
# Pins coordinator/worker/reviewer to agy models and sets pool_floor_percent = 0.
pin_agy_models() {
  local state_path=$1 extra_settings=${2:-"{}"} extra_models=${3:-"{}"}
  python3 - "$state_path" "$extra_settings" "$extra_models" <<'PY'
import json, sys
p, extra_s, extra_m = sys.argv[1], json.loads(sys.argv[2]), json.loads(sys.argv[3])
d = json.load(open(p))
models = {
  "coordinator": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "worker": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "reviewer": [{"cli": "agy", "model": "gemini-4-neon-high", "effort": "medium"},
               {"cli": "agy", "model": "gemini-4-carbon-medium", "effort": "medium"}],
}
models.update(extra_m)
d["settings"]["models"] = models
d["settings"]["pool_floor_percent"] = 0
d["settings"].update(extra_s)
json.dump(d, open(p, "w"), indent=2)
PY
}
