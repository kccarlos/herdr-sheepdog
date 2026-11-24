#!/bin/zsh
# 12.3: retro-round live-check script (mirrors scripts/e2e/factory-exit-test.sh).
# Usage: scripts/e2e/factory-retro.sh --mode <recap|ship> <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo}; optional env: PORT, ENV_FILE, OP_TOKEN_FILE.
set -u

usage() {
  echo "Usage: $0 --mode <recap|ship> <demo-dir>" >&2
  echo "Optional env: PORT (default 17794), ENV_FILE, OP_TOKEN_FILE" >&2
}

MODE=""; D=""
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --mode) [ $# -ge 2 ] || { usage; exit 1; }; MODE="$2"; shift 2 ;;
    --mode=*) MODE="${1#--mode=}"; shift ;;
    -*) echo "Unknown flag: $1" >&2; usage; exit 1 ;;
    *) [ -z "$D" ] || { usage; exit 1; }; D="${1%/}"; shift ;;
  esac
done

case "$MODE" in
  recap|ship) ;;
  "") echo "Missing required --mode flag" >&2; usage; exit 1 ;;
  *) echo "Unknown mode: $MODE (expected recap, ship)" >&2; usage; exit 1 ;;
esac

[ -n "$D" ] || { usage; exit 1; }

PORT="${PORT:-17794}"
R="$D/test_repo"; U="http://127.0.0.1:$PORT"; C="$D/config.toml"
ENV_FILE="${ENV_FILE:-}"; OP_TOKEN_FILE="${OP_TOKEN_FILE:-}"
DNAME="e2-retro12-daemon"; J=""; L=""; PID=""; TID=""

fail()         { echo "== FAIL: $1"; exit 1; }
forcing_fail() { echo "== forcing failed: $1, run again"; exit 2; }
step()         { echo "== STEP $1 at $(date +%T)"; }
# Only stop a daemon this run started: tests run the script on an empty dir, and a
# fixed terminal name must never take down a live run started elsewhere.
STARTED=0
stopd()        { [ "$STARTED" = 1 ] || return 0; hc kill "$DNAME" >/dev/null 2>&1; hc rm "$DNAME" >/dev/null 2>&1; sleep 3; }

cleanup_trap() {
  [ -n "$J" ] && "$D/sheepdog" factory stop "$R" --config "$C" >/dev/null 2>&1 || true
  stopd
  if [ -n "$PID" ]; then
    for t in $(hc ls --json 2>/dev/null | python3 -c "import sys,json;print(' '.join(t['address'] for t in json.load(sys.stdin).get('terminals',[]) if (t.get('labels') or {}).get('sheepdog.project')=='$PID'))" 2>/dev/null); do
      hc kill "$t" >/dev/null 2>&1; hc rm "$t" >/dev/null 2>&1
    done
  fi
}
trap cleanup_trap EXIT

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

# Preflight: expect pre-populated $D/sheepdog and $D/test_repo.
[ -x "$D/sheepdog" ] || fail "missing executable $D/sheepdog"
[ -d "$R/.git" ] || fail "missing repo $R"
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

if [ "$MODE" = "recap" ]; then
  C4_HOOK='  4) Commit 4 (after retro round 4 findings, when hello/retro12.go handles n == -2 but not all negative n): fix ALL negative n (n < 0 returns 2*n + 1) and add negative test cases to TestRetroCalc, but leave n == 0 returning 0 (still wrong; do NOT test n == 0). Commit and report ready.'
else
  C4_HOOK='  4) Commit 4 (after retro round 4 findings, when hello/retro12.go handles n == -2 but not all negative n): fix BOTH all negative n AND n == 0 so RetroCalc(n) returns 2*n + 1 for every integer n, and test positive, zero, and negative inputs in TestRetroCalc. Commit and report ready.'
fi

G="Slice 12 retro ${MODE} test. Plan EXACTLY ONE task with id T1 (after: [], no gate, estimate_lines: 60):
- T1: add func RetroCalc(n int) int returning 2*n + 1 for all integers n (positive, zero, and negative) in a new file hello/retro12.go with unit test TestRetroCalc in hello/retro12_test.go; outcome: \"RetroCalc(n) returns 2*n+1 for all integers n, including n == 0 and negative n\" proved by test: TestRetroCalc.
Put these EXACT hooks in T1 brief verbatim (keyed on the code currently in hello/retro12.go):
  1) Commit 1 (when hello/retro12.go does not exist): implement RetroCalc(n int) int to return 2*n + 1 for n > 0 and 0 for n <= 0. Test ONLY n = 1, 2 in TestRetroCalc so go test ./... passes. Commit and report ready. Do NOT fix n <= 0 yet.
  2) Commit 2 (after review round 1 findings, when hello/retro12.go does not handle n == -1): patch-fix ONLY n == -1 (return -1) and add n = -1 to TestRetroCalc, leaving n <= -2 and n == 0 returning 0. Commit and report ready.
  3) Commit 3 (after review round 2 findings, when hello/retro12.go handles n == -1 but not n == -2): patch-fix ONLY n == -2 (return -3) and add n = -2 to TestRetroCalc, leaving n <= -3 and n == 0 returning 0. Commit and report ready.
${C4_HOOK}
  5) Note to reviewer (every full and delta round): check RetroCalc(n) against 2*n + 1 for all n. If ANY negative n (such as n == -1, n == -2, or n == -3) returns the wrong value, you MUST submit changes with a major: line naming a failing negative input.
  6) Note to retro reviewer (initial retro and delta check): RetroCalc(n) = 2*n + 1 is a single-function bounded fix (not a redesign). Whenever any input (negative n or n == 0) returns the wrong value, submit verdict fix (not redesign) with a major: line naming the failing input; when every integer n (positive, zero, and negative) returns 2*n + 1 and TestRetroCalc covers them, submit verdict ship with 0 major: lines.
After applying the plan, wait and do not amend, retry, or drop T1; if woken for a recap, inspect it and wait without deciding it."

daemon
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2
python3 - "$J" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
# Explicitly pin agy models (agy supports RoleRetro / report retro per plan R1;
# DefaultModels[RoleRetro] is claude/opus/high when models.retro is unset).
d["settings"]["models"] = {
  "coordinator": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "worker": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "reviewer": [{"cli": "agy", "model": "gemini-4-neon-high", "effort": "medium"},
               {"cli": "agy", "model": "gemini-4-carbon-medium", "effort": "medium"}],
  "retro": [{"cli": "agy", "model": "gemini-4-carbon-high", "effort": "high"}],
}
d["settings"]["pool_floor_percent"] = 0
json.dump(d, open(p, "w"), indent=2)
PY
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "restart after settings edit refused"
echo "== started $PID (--mode $MODE) on $U at $(date +%T)"

step "plan_task"
wait_py "planned task in state.json" 300 10 'len(state.get("tasks") or []) >= 1 and state["tasks"][0]["id"]'
TID=$(python3 -c "import json;ts=json.load(open('$J')).get('tasks') or [];print(ts[0]['id'] if len(ts)==1 else f'COUNT:{len(ts)}')")
[[ "$TID" == COUNT:* ]] && forcing_fail "coordinator planned ${TID#COUNT:} tasks instead of 1"

# Watch three N29 reviewer rounds produce changes with majors > 0 (exit 2 if forcing misfires).
for rn in 1 2 3; do
  step "round_${rn}_majors"
  wait_py "round $rn verdict on $TID" 900 15 \
    'any(t.get("id")==val and (len([r for r in ((t.get("attempts") or [{}])[0].get("rounds") or []) if r.get("kind") in ("full","delta")])>='"$rn"' or any(t.get(k) for k in ("done","closed","dropped","split")) or any(rc.get("trigger") in ("size","ci","time") for rc in (t.get("recaps") or []))) for t in (state.get("tasks") or [])) and "round_'"$rn"'_ready"' "$TID"
  CHK=$(python3 - "$J" "$L" "$TID" "$rn" <<'PY'
import json, sys
s, lp, tid, rn = json.load(open(sys.argv[1])), sys.argv[2], sys.argv[3], int(sys.argv[4])
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
if rn < 3 and any(x.get("rule") == "N28" for x in (json.loads(ln) for ln in open(lp) if ln.strip())):
    print("FAIL:unexpected N28 line in log.jsonl"); sys.exit(0)
for rc in t.get("recaps") or []:
    if rc.get("trigger") in ("size", "ci", "time"):
        print(f"FORCE:hit {rc['trigger']} recap before round {rn}"); sys.exit(0)
if any(t.get(k) for k in ("done", "closed", "dropped", "split")):
    print(f"FORCE:task {tid} ended early before round {rn}"); sys.exit(0)
rr = [r for r in ((t.get("attempts") or [{}])[0].get("rounds") or []) if r.get("kind") in ("full", "delta")]
r = rr[rn - 1]
if r.get("verdict") != "changes" or int(r.get("majors") or 0) <= 0:
    print(f"FORCE:round {rn} returned verdict={r.get('verdict')} majors={r.get('majors')} (expected changes with majors>0)")
else:
    print(f"OK:round {rn} kind={r.get('kind')} verdict={r.get('verdict')} majors={r.get('majors')}")
PY
  )
  echo "   $CHK"
  [[ "$CHK" == FAIL:* ]] && fail "${CHK#FAIL:}"
  [[ "$CHK" == FORCE:* ]] && forcing_fail "${CHK#FORCE:}"
done

# Initial retro (L11 -> N28 start_retro -> status "In retro" -> Round kind retro verdict fix + 1 t.Retros entry).
IN_RETRO_SEEN=0
step "n28_retro_1"
wait_py "N28 initial start_retro on $TID" 240 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N28" and any("start_retro" in a for a in (x.get("asked") or [])) and "started retro" in str(x.get("decision",""))] or [None])[0]' "$TID"
for _ in $(seq 1 10); do
  ST=$("$D/sheepdog" factory status "$R" --config "$C" 2>&1 || true)
  if printf '%s\n' "$ST" | grep -q "In retro"; then
    IN_RETRO_SEEN=1
    echo "   status: $ST"
    break
  fi
  sleep 1
done
[ "$IN_RETRO_SEEN" -eq 1 ] || fail "sheepdog factory status did not show \"In retro\" after N28 start_retro (got: ${ST:-<empty>})"

step "retro_1_fix"
wait_py "first retro round on $TID" 900 15 \
  'any(t.get("id")==val and (len([r for r in ((t.get("attempts") or [{}])[0].get("rounds") or []) if r.get("kind")=="retro"])>=1 or any(rc.get("trigger")=="review" for rc in (t.get("recaps") or [])) or any(t.get(k) for k in ("done","closed","dropped","split"))) for t in (state.get("tasks") or [])) and "retro_1_ready"' "$TID"
CHK=$(python3 - "$J" "$TID" <<'PY'
import json, sys
s, tid = json.load(open(sys.argv[1])), sys.argv[2]
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
a = (t.get("attempts") or [{}])[0]
rr = [r for r in (a.get("rounds") or []) if r.get("kind") == "retro"]
rets = t.get("retros") or []
if not rr:
    print(f"FAIL:no retro round recorded in state.json (recaps={t.get('recaps')})")
elif rr[0].get("verdict") != "fix":
    print(f"FORCE:first retro round returned verdict={rr[0].get('verdict')} (expected fix)")
elif len(rets) != 1 or rets[0].get("round") != rr[0].get("n"):
    print(f"FAIL:expected 1 t.Retros entry matching round {rr[0].get('n')}, got {rets}")
else:
    print(f"OK:retro1 round={rr[0].get('n')} verdict={rr[0].get('verdict')} majors={rr[0].get('majors')} cause={rets[0].get('cause')}")
PY
)
echo "   $CHK"
[[ "$CHK" == FAIL:* ]] && fail "${CHK#FAIL:}"
[[ "$CHK" == FORCE:* ]] && forcing_fail "${CHK#FORCE:}"

# N22 bounded FIX with a.SizeCap == lines + 60 in state.json (L4).
step "n22_bounded_fix"
wait_py "N22 bounded retro fix on $TID" 240 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N22" and "bounded retro fix" in str(x.get("decision",""))] or [None])[0]' "$TID"
N22_INFO=$(python3 - "$L" "$J" "$TID" <<'PY'
import json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
s, tid = json.load(open(sys.argv[2])), sys.argv[3]
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
a = (t.get("attempts") or [{}])[0]
n22 = [x for x in log if x.get("task") == tid and x.get("rule") == "N22" and "bounded retro fix" in str(x.get("decision", ""))][-1]
lines = int((n22.get("facts") or {}).get("lines", -1))
cont = int((s.get("settings") or {}).get("continue_lines") or 60)
cap = int(a.get("size_cap") or 0)
if lines < 0 or cont != 60 or cap != lines + 60:
    print(f"FAIL:N22 size_cap={cap}, expected lines({lines})+60={lines+60} (continue_lines={cont})")
else:
    print(f"OK:{n22.get('seq', 0)}:lines={lines}:size_cap={cap}")
PY
)
echo "   $N22_INFO"
[[ "$N22_INFO" == FAIL:* ]] && fail "${N22_INFO#FAIL:}"
N22_SEQ=$(printf '%s' "$N22_INFO" | cut -d: -f2)

# Worker commits c4 and reports ready -> N26 pushes c4 -> N28 delta check (L5).
step "c4_ready_and_n26"
wait_py "worker ready (c4) after N22 on $TID" 600 10 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="ready" and x.get("seq",0)>'"$N22_SEQ"'] or [None])[0]' "$TID"
wait_py "N26 push c4 after N22 on $TID" 240 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N26" and any("push_and_open_pr" in a for a in (x.get("asked") or [])) and x.get("seq",0)>'"$N22_SEQ"'] or [None])[0]' "$TID"
N26_SEQ=$(python3 -c "import json;log=[json.loads(ln) for ln in open('$L') if ln.strip()];xs=[x for x in log if x.get('task')=='$TID' and x.get('rule')=='N26' and any('push_and_open_pr' in a for a in (x.get('asked') or [])) and x.get('seq',0)>$N22_SEQ];print(xs[0]['seq'])")

step "n28_retro_2"
wait_py "N28 delta-check start_retro on $TID" 300 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N28" and any("start_retro" in a for a in (x.get("asked") or [])) and "started retro" in str(x.get("decision","")) and x.get("seq",0)>'"$N26_SEQ"'] or [None])[0]' "$TID"

WAKE_TEXT=""
if [ "$MODE" = "recap" ]; then
  step "retro_2_fix"
  wait_py "second retro round on $TID" 900 15 \
    'any(t.get("id")==val and (len([r for r in ((t.get("attempts") or [{}])[0].get("rounds") or []) if r.get("kind")=="retro"])>=2 or any(rc.get("trigger")=="review" for rc in (t.get("recaps") or [])) or any(t.get(k) for k in ("done","closed","dropped","split"))) for t in (state.get("tasks") or [])) and "retro_2_ready"' "$TID"
  CHK=$(python3 - "$J" "$TID" <<'PY'
import json, sys
s, tid = json.load(open(sys.argv[1])), sys.argv[2]
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
a = (t.get("attempts") or [{}])[0]
rr = [r for r in (a.get("rounds") or []) if r.get("kind") == "retro"]
rets = t.get("retros") or []
if len(rr) < 2:
    print(f"FAIL:expected 2nd retro round in state.json, got {rr}")
elif rr[1].get("verdict") != "fix":
    print(f"FORCE:second retro round returned verdict={rr[1].get('verdict')} (expected fix)")
elif len(rets) != 2 or rets[1].get("round") != rr[1].get("n"):
    print(f"FAIL:expected 2 t.Retros entries with 2nd matching round {rr[1].get('n')}, got {rets}")
else:
    print(f"OK:retro2 round={rr[1].get('n')} verdict={rr[1].get('verdict')} majors={rr[1].get('majors')} cause={rets[1].get('cause')}")
PY
  )
  echo "   $CHK"
  [[ "$CHK" == FAIL:* ]] && fail "${CHK#FAIL:}"
  [[ "$CHK" == FORCE:* ]] && forcing_fail "${CHK#FORCE:}"

  step "n25_recap_and_n6_wake"
  wait_py "N25 open_recap@review on $TID" 240 5 \
    '([x for x in log if x.get("task")==val and x.get("rule")=="N25" and any("open_recap@review" in a for a in (x.get("asked") or []))] or [None])[0]' "$TID"
  wait_py "N6 coordinator wake on $TID" 180 2 \
    '([x for x in log if x.get("task")==val and x.get("rule")=="N6" and any("wake_coordinator@recap" in a for a in (x.get("asked") or []))] or [None])[0]' "$TID"
  # Capture the N6 wake facts from state.json (wakes or coord_wakes_in_flight),
  # falling back to reading the coordinator terminal via hc if already cleared.
  WAKE_TEXT=$(python3 - "$J" "$TID" <<'PY'
import json, sys
s, tid = json.load(open(sys.argv[1])), sys.argv[2]
p = s.get("project") or {}
for w in reversed((p.get("coord_wakes_in_flight") or []) + (p.get("wakes") or [])):
    if w.get("task") == tid and str(w.get("key") or "").split("@", 1)[0] == "recap":
        print(w.get("facts") or "")
        sys.exit(0)
PY
  )
  if [ -z "$WAKE_TEXT" ]; then
    COORD_ID=$(python3 -c "import json;print((json.load(open('$J')).get('project') or {}).get('run_id') or '')")
    [ -n "$COORD_ID" ] && WAKE_TEXT=$(hc read "$COORD_ID" 2>/dev/null || true)
  fi
else
  step "retro_2_ship"
  wait_py "second retro round on $TID" 900 15 \
    'any(t.get("id")==val and (len([r for r in ((t.get("attempts") or [{}])[0].get("rounds") or []) if r.get("kind")=="retro"])>=2 or any(rc.get("trigger")=="review" for rc in (t.get("recaps") or [])) or any(t.get(k) for k in ("done","closed","dropped","split"))) for t in (state.get("tasks") or [])) and "retro_2_ready"' "$TID"
  CHK=$(python3 - "$J" "$TID" <<'PY'
import json, sys
s, tid = json.load(open(sys.argv[1])), sys.argv[2]
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
a = (t.get("attempts") or [{}])[0]
rr = [r for r in (a.get("rounds") or []) if r.get("kind") == "retro"]
rets = t.get("retros") or []
if len(rr) < 2:
    print(f"FAIL:expected 2nd retro round in state.json, got {rr}")
elif rr[1].get("verdict") != "ship":
    print(f"FORCE:second retro round returned verdict={rr[1].get('verdict')} (expected ship)")
elif len(rets) != 2 or rets[1].get("round") != rr[1].get("n"):
    print(f"FAIL:expected 2 t.Retros entries with 2nd matching round {rr[1].get('n')}, got {rets}")
else:
    print(f"OK:retro2 round={rr[1].get('n')} verdict={rr[1].get('verdict')} majors={rr[1].get('majors')} cause={rets[1].get('cause')}")
PY
  )
  echo "   $CHK"
  [[ "$CHK" == FAIL:* ]] && fail "${CHK#FAIL:}"
  [[ "$CHK" == FORCE:* ]] && forcing_fail "${CHK#FORCE:}"

  step "n42_and_n2_merged"
  wait_py "N42 accept and merge on $TID" 300 10 \
    '([x for x in log if x.get("task")==val and x.get("rule")=="N42" and any("merge@" in a for a in (x.get("asked") or []))] or [None])[0]' "$TID"
  wait_py "N2 PR merged and task done on $TID" 300 10 \
    'any(x.get("task")==val and x.get("rule")=="N2" for x in log) and any(t.get("id")==val and t.get("done") for t in (state.get("tasks") or [])) and "N2_MERGED"' "$TID"
fi

echo "== sheepdog factory log:"
"$D/sheepdog" factory log "$R" --config "$C"
echo "== assertions, rule summary & fault check:"
python3 - "$L" "$J" "$TID" "$MODE" "$IN_RETRO_SEEN" "$WAKE_TEXT" <<'PY'
import collections, json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state, tid, mode, in_retro_seen, wake_text = json.load(open(sys.argv[2])), sys.argv[3], sys.argv[4], sys.argv[5], sys.argv[6]
t = [x for x in state.get("tasks") or [] if x["id"] == tid][0]
a = (t.get("attempts") or [{}])[0]
rr_rev = [r for r in a.get("rounds") or [] if r.get("kind") in ("full", "delta")]
rr_ret = [r for r in a.get("rounds") or [] if r.get("kind") == "retro"]
rets = t.get("retros") or []
n29 = [x for x in log if x.get("task") == tid and x.get("rule") == "N29" and "started reviewer" in str(x.get("decision", ""))]
n28_starts = [x for x in log if x.get("task") == tid and x.get("rule") == "N28" and any("start_retro" in k for k in (x.get("asked") or [])) and "started retro" in str(x.get("decision", ""))]
retro_reports = [x for x in log if x.get("task") == tid and x.get("rule") == "retro"]
n22_retro = [x for x in log if x.get("task") == tid and x.get("rule") == "N22" and "bounded retro fix" in str(x.get("decision", ""))]
n26_all = [x for x in log if x.get("task") == tid and x.get("rule") == "N26" and any("push_and_open_pr" in k for k in (x.get("asked") or []))]
n25 = [x for x in log if x.get("task") == tid and x.get("rule") == "N25" and any("open_recap@review" in k for k in (x.get("asked") or []))]
n6 = [x for x in log if x.get("task") == tid and x.get("rule") == "N6" and any("wake_coordinator@recap" in k for k in (x.get("asked") or []))]
n42 = [x for x in log if x.get("task") == tid and x.get("rule") == "N42" and any("merge@" in k for k in (x.get("asked") or []))]
n2 = [x for x in log if x.get("task") == tid and x.get("rule") == "N2"]
errs = []

if len(rr_rev) < 3 or any(r.get("verdict") != "changes" or int(r.get("majors") or 0) <= 0 for r in rr_rev[:3]):
    errs.append(f"expected 3 reviewer rounds with majors>0 in state.json, got {rr_rev}")
if len(n29) < 3:
    errs.append(f"expected >=3 N29 reviewer starts in log.jsonl, got {len(n29)}")
if len(n28_starts) < 2:
    errs.append(f"expected >=2 N28 start_retro launches (initial + delta check), got {len(n28_starts)}")
if n28_starts and len(n29) >= 3 and n28_starts[0].get("seq", 0) <= n29[2].get("seq", 0):
    errs.append(f"first N28 seq={n28_starts[0].get('seq')} not after 3rd N29 seq={n29[2].get('seq')}")
if in_retro_seen != "1":
    errs.append('sheepdog factory status did not show "In retro" during initial N28 retro run')

if len(rr_ret) != 2:
    errs.append(f"expected 2 retro rounds in state.json, got {rr_ret}")
if len(rets) != 2:
    errs.append(f"expected 2 entries in t.Retros, got {rets}")
if len(rr_ret) >= 1 and (rr_ret[0].get("verdict") != "fix" or int(rr_ret[0].get("majors") or 0) < 1):
    errs.append(f"expected 1st retro round verdict=fix majors>=1, got {rr_ret[0]}")
if len(rr_ret) >= 1 and len(rets) >= 1 and rets[0].get("round") != rr_ret[0].get("n"):
    errs.append(f"1st t.Retros round={rets[0].get('round')} != retro round n={rr_ret[0].get('n')}")

if not n22_retro:
    errs.append("missing N22 bounded retro fix log line")
else:
    n22_seq = n22_retro[0].get("seq", 0)
    n22_lines = int((n22_retro[0].get("facts") or {}).get("lines", -1))
    cont = int((state.get("settings") or {}).get("continue_lines") or 60)
    if n22_lines < 0 or cont != 60 or int(a.get("size_cap") or 0) != n22_lines + 60:
        errs.append(f"a.SizeCap={a.get('size_cap')} != N22 lines({n22_lines}) + 60 (continue_lines={cont})")
    ready_c4 = [x for x in log if x.get("task") == tid and x.get("rule") == "ready" and x.get("seq", 0) > n22_seq]
    n26_c4 = [x for x in n26_all if x.get("seq", 0) > n22_seq]
    if not ready_c4:
        errs.append("missing worker ready (c4) after N22")
    if not n26_c4:
        errs.append("missing N26 push_and_open_pr after N22")
    else:
        n28_delta = [x for x in n28_starts if x.get("seq", 0) > n26_c4[0].get("seq", 0)]
        if not n28_delta:
            errs.append(f"missing delta-check N28 start_retro after N26 seq={n26_c4[0].get('seq')}")

if len(rr_ret) >= 2 and len(rets) >= 2:
    if rets[1].get("round") != rr_ret[1].get("n"):
        errs.append(f"2nd t.Retros round={rets[1].get('round')} != retro round n={rr_ret[1].get('n')}")
    if rr_ret[1].get("head") == rr_ret[0].get("head"):
        errs.append(f"2nd retro head={rr_ret[1].get('head')} same as 1st retro head (expected c4)")

rev_recaps = [rc for rc in t.get("recaps") or [] if rc.get("trigger") == "review"]
if mode == "recap":
    if len(rr_ret) >= 2 and (rr_ret[1].get("verdict") != "fix" or int(rr_ret[1].get("majors") or 0) < 1):
        errs.append(f"expected 2nd retro round verdict=fix majors>=1, got {rr_ret[1]}")
    if not n25:
        errs.append("missing N25 log line with open_recap@review")
    elif len(retro_reports) >= 2 and n25[0].get("seq", 0) <= retro_reports[1].get("seq", 0):
        errs.append(f"N25 seq={n25[0].get('seq')} not after 2nd retro report seq={retro_reports[1].get('seq')}")
    if not rev_recaps or rev_recaps[0].get("allowed") != ["split", "new", "drop"]:
        errs.append(f"expected review recap with allowed [split,new,drop], got {t.get('recaps')}")
    if not n6 or (n25 and n6[0].get("seq", 0) <= n25[0].get("seq", 0)):
        errs.append(f"missing N6 coordinator wake after N25 (n6={len(n6)})")
    if len(rr_ret) >= 2 and len(rets) >= 2:
        norm_wake = " ".join(wake_text.split())
        ret2, r2 = rets[1], rr_ret[1]
        for label, needle in [
            ("retro header", f"retro r{ret2.get('round')} {r2.get('verdict')}"),
            ("cause", f"cause {ret2.get('cause')}"),
            ("coordinator change kind", f"coordinator {ret2.get('coordinator_change_kind')}:"),
            ("coordinator change", " ".join(str(ret2.get("coordinator_change") or "").split())),
            ("report path", str(r2.get("report_path") or "")),
        ]:
            if not needle or needle not in norm_wake:
                errs.append(f"N6 wake text missing retro line {label} ({needle!r}); got: {norm_wake[:300]!r}")
else:
    if len(rr_ret) >= 2 and (rr_ret[1].get("verdict") != "ship" or int(rr_ret[1].get("majors") or 0) != 0):
        errs.append(f"expected 2nd retro round verdict=ship majors=0, got {rr_ret[1]}")
    if not n42:
        errs.append("missing N42 accept/merge log line after retro SHIP")
    elif len(retro_reports) >= 2 and n42[0].get("seq", 0) <= retro_reports[1].get("seq", 0):
        errs.append(f"N42 seq={n42[0].get('seq')} not after 2nd retro report seq={retro_reports[1].get('seq')}")
    if len(rr_ret) >= 2:
        ac = [x for x in (a.get("accepts") or []) if x.get("head") == rr_ret[1].get("head") and rr_ret[1].get("n") in (x.get("rounds") or [])]
        if not ac:
            errs.append(f"expected accept covering retro round {rr_ret[1].get('n')} at {rr_ret[1].get('head')}, got {a.get('accepts')}")
    if not n2 or (n42 and n2[0].get("seq", 0) <= n42[0].get("seq", 0)):
        errs.append(f"missing N2 merged log line after N42 (n2={len(n2)})")
    if not t.get("done"):
        errs.append(f"expected task {tid} done=true after N2")
    if rev_recaps:
        errs.append(f"unexpected review recap in ship mode: {rev_recaps}")

cnt, faults = collections.Counter(), []
for d in log:
    r = d.get("rule") or ""; cnt[r] += 1
    if r in ("", "N43", "R37", "P4", "A5") or "no rule matched" in (str(d.get("why", "")) + str(d.get("decision", ""))).lower():
        faults.append(f"FAULT seq={d.get('seq')} task={d.get('task', '-')} rule={r or '(empty)'} why={d.get('why', '')}")
for r, n in sorted(cnt.items()): print(f"  {r:18s} {n}")
for e in errs + faults: print(f"ERROR: {e}")
sys.exit(1 if (errs or faults) else 0)
PY
[ $? -eq 0 ] || fail "$MODE assertions or fault check failed"
if [ "$MODE" = "recap" ]; then
  echo "RETRO12_RECAP_DONE"
else
  echo "RETRO12_SHIP_DONE"
fi
