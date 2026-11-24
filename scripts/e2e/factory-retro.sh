#!/bin/zsh
# 12.1: retro-round forcing script (mirrors scripts/e2e/factory-exit-test.sh).
# Usage: scripts/e2e/factory-retro.sh --mode <regression|launch|recap|ship> <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo}; optional env: PORT, ENV_FILE, OP_TOKEN_FILE.
set -u

usage() {
  echo "Usage: $0 --mode <regression|launch|recap|ship> <demo-dir>" >&2
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
  regression) ;;
  launch)
    # TODO(12.2): implement --mode launch (models.retro=[agy/gemini-4-carbon-high], N28 start_retro +
    # "In retro" status, retries exhaust to N25+N6 with facts.retro_unavailable==true and no RoundRetro).
    echo "TODO(12.2): --mode launch is not implemented yet" >&2; exit 2 ;;
  recap|ship)
    # TODO(12.3): implement --mode recap (FIX -> N22 size_cap=lines+60 -> c4 -> N28 delta -> 2nd FIX ->
    # N25 open_recap@review [split,new,drop] -> N6 retro wake line) and --mode ship (SHIP -> N42 -> N2),
    # and delete --mode regression and --mode launch.
    echo "TODO(12.3): --mode $MODE is not implemented yet" >&2; exit 2 ;;
  "") echo "Missing required --mode flag" >&2; usage; exit 1 ;;
  *) echo "Unknown mode: $MODE (expected regression, launch, recap, ship)" >&2; usage; exit 1 ;;
esac

[ -n "$D" ] || { usage; exit 1; }

PORT="${PORT:-17794}"
R="$D/test_repo"; U="http://127.0.0.1:$PORT"; C="$D/config.toml"
ENV_FILE="${ENV_FILE:-}"; OP_TOKEN_FILE="${OP_TOKEN_FILE:-}"
DNAME="e2-retro12-daemon"; J=""; L=""; PID=""; TID=""

fail()         { echo "== FAIL: $1"; exit 1; }
forcing_fail() { echo "== forcing failed: $1, run again"; exit 2; }
step()         { echo "== STEP $1 at $(date +%T)"; }
stopd()        { hc kill "$DNAME" >/dev/null 2>&1; hc rm "$DNAME" >/dev/null 2>&1; sleep 3; }

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
      hc run --bg --json --name "$DNAME" --cwd "$D" -- zsh -c "OP_SERVICE_ACCOUNT_TOKEN=\"\$(<\"$OP_TOKEN_FILE\")\" exec op run --env-file=\"$ENV_FILE\" -- env -u OP_SERVICE_ACCOUNT_TOKEN \"$D/sheepdog\" serve --config \"$C\" --voice=off --brain=off --cli=off" >/dev/null || fail "hc run daemon"
    else
      hc run --bg --json --name "$DNAME" --cwd "$D" -- op run --env-file="$ENV_FILE" -- "$D/sheepdog" serve --config "$C" --voice=off --brain=off --cli=off >/dev/null || fail "hc run daemon"
    fi
  else
    hc run --bg --json --name "$DNAME" --cwd "$D" -- "$D/sheepdog" serve --config "$C" --voice=off --brain=off --cli=off >/dev/null || fail "hc run daemon"
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

G='Slice 12 retro regression test. Plan EXACTLY ONE task with id T1 (after: [], no gate, estimate_lines: 60):
- T1: add func RetroCalc(n int) int returning 2*n + 1 for all integers n (positive, zero, and negative) in a new file hello/retro12.go with unit test TestRetroCalc in hello/retro12_test.go; outcome: "RetroCalc(n) returns 2*n+1 for all integers n, including n == 0 and negative n" proved by test: TestRetroCalc.
Put these EXACT hooks in T1 brief verbatim (keyed on the code currently in hello/retro12.go):
  1) Commit 1 (when hello/retro12.go does not exist): implement RetroCalc(n int) int to return 2*n + 1 for n > 0 and 0 for n <= 0. Test ONLY n = 1, 2 in TestRetroCalc so go test ./... passes. Commit and report ready. Do NOT fix n <= 0 yet.
  2) Commit 2 (after review round 1 findings, when hello/retro12.go does not handle n == -1): patch-fix ONLY n == -1 (return -1) and add n = -1 to TestRetroCalc, leaving n <= -2 and n == 0 returning 0. Commit and report ready.
  3) Commit 3 (after review round 2 findings, when hello/retro12.go handles n == -1 but not n == -2): patch-fix ONLY n == -2 (return -3) and add n = -2 to TestRetroCalc, leaving n <= -3 and n == 0 returning 0. Commit and report ready.
  4) Note to reviewer (every full and delta round): check RetroCalc(n) against 2*n + 1 for all n. If ANY negative n (such as n == -1, n == -2, or n == -3) returns the wrong value, you MUST submit changes with a major: line naming a failing negative input.
After applying the plan, wait and do not amend, retry, or drop T1 until woken for a recap.'

daemon
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2
python3 - "$J" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p))
# Explicitly pin agy models; models.retro stays unset in --mode regression (12.2/12.3 add agy/gemini-4-carbon-high).
d["settings"]["models"] = {
  "coordinator": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "worker": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "reviewer": [{"cli": "agy", "model": "gemini-4-neon-high", "effort": "medium"},
               {"cli": "agy", "model": "gemini-4-carbon-medium", "effort": "medium"}],
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
    'any(t.get("id")==val and (len([r for r in ((t.get("attempts") or [{}])[-1].get("rounds") or []) if r.get("kind") in ("full","delta")])>='"$rn"' or any(t.get(k) for k in ("done","closed","dropped","split")) or any(rc.get("trigger") in ("size","ci","time") for rc in (t.get("recaps") or []))) for t in (state.get("tasks") or [])) and "round_'"$rn"'_ready"' "$TID"
  CHK=$(python3 - "$J" "$L" "$TID" "$rn" <<'PY'
import json, sys
s, lp, tid, rn = json.load(open(sys.argv[1])), sys.argv[2], sys.argv[3], int(sys.argv[4])
t = [x for x in s.get("tasks") or [] if x.get("id") == tid][0]
if any(x.get("rule") == "N28" for x in (json.loads(ln) for ln in open(lp) if ln.strip())):
    print("FAIL:unexpected N28 line in log.jsonl"); sys.exit(0)
for rc in t.get("recaps") or []:
    if rc.get("trigger") in ("size", "ci", "time"):
        print(f"FORCE:hit {rc['trigger']} recap before round {rn}"); sys.exit(0)
if any(t.get(k) for k in ("done", "closed", "dropped", "split")):
    print(f"FORCE:task {tid} ended early before round {rn}"); sys.exit(0)
rr = [r for r in ((t.get("attempts") or [{}])[-1].get("rounds") or []) if r.get("kind") in ("full", "delta")]
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

step "n25_recap_and_n6_wake"
wait_py "N25 open_recap@review (or unexpected N28)" 240 5 \
  '([x for x in log if x.get("rule")=="N28" or (x.get("task")==val and x.get("rule")=="N25" and any("open_recap@review" in a for a in (x.get("asked") or [])))] or [None])[0]' "$TID"
grep -q '"rule":"N28"' "$L" && fail "unexpected N28 line in log.jsonl"
wait_py "N6 coordinator wake on $TID" 180 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N6" and any("wake_coordinator@recap" in a for a in (x.get("asked") or []))] or [None])[0]' "$TID"

echo "== sheepdog factory log:"
"$D/sheepdog" factory log "$R" --config "$C"
echo "== assertions, rule summary & fault check:"
python3 - "$L" "$J" "$TID" <<'PY'
import collections, json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state, tid = json.load(open(sys.argv[2])), sys.argv[3]
t = [x for x in state.get("tasks") or [] if x["id"] == tid][0]
a = (t.get("attempts") or [{}])[-1]
rr = [r for r in a.get("rounds") or [] if r.get("kind") in ("full", "delta")]
n29 = [x for x in log if x.get("task") == tid and x.get("rule") == "N29" and "started reviewer" in str(x.get("decision", ""))]
n28 = [x for x in log if x.get("rule") == "N28"]
n25 = [x for x in log if x.get("task") == tid and x.get("rule") == "N25" and any("open_recap@review" in k for k in (x.get("asked") or []))]
n6 = [x for x in log if x.get("task") == tid and x.get("rule") == "N6" and any("wake_coordinator@recap" in k for k in (x.get("asked") or []))]
errs = []
if len(rr) < 3 or any(r.get("verdict") != "changes" or int(r.get("majors") or 0) <= 0 for r in rr[:3]):
    errs.append(f"expected 3 reviewer rounds with majors>0 in state.json, got {rr}")
if len(n29) < 3:
    errs.append(f"expected >=3 N29 reviewer starts in log.jsonl, got {len(n29)}")
if n28 or any(r.get("kind") == "retro" for r in a.get("rounds") or []) or (t.get("retros") or []):
    errs.append(f"unexpected retro activity: n28={len(n28)} retros={t.get('retros')}")
if not n25:
    errs.append("missing N25 log line with open_recap@review")
else:
    if (n25[0].get("facts") or {}).get("retro_unavailable") is not True:
        errs.append(f"N25 facts.retro_unavailable != true: {n25[0].get('facts')}")
    if len(n29) >= 3 and n25[0].get("seq", 0) <= n29[2].get("seq", 0):
        errs.append(f"N25 seq={n25[0].get('seq')} not after 3rd N29 seq={n29[2].get('seq')}")
rev_recaps = [rc for rc in t.get("recaps") or [] if rc.get("trigger") == "review"]
if not rev_recaps or rev_recaps[0].get("allowed") != ["split", "new", "drop"]:
    errs.append(f"expected review recap with allowed [split,new,drop], got {t.get('recaps')}")
if not n6 or (n25 and n6[0].get("seq", 0) <= n25[0].get("seq", 0)):
    errs.append(f"missing N6 coordinator wake after N25 (n6={len(n6)})")
cnt, faults = collections.Counter(), []
for d in log:
    r = d.get("rule") or ""; cnt[r] += 1
    if r in ("", "N43", "R37", "P4", "A5") or "no rule matched" in (str(d.get("why", "")) + str(d.get("decision", ""))).lower():
        faults.append(f"FAULT seq={d.get('seq')} task={d.get('task', '-')} rule={r or '(empty)'} why={d.get('why', '')}")
for r, n in sorted(cnt.items()): print(f"  {r:18s} {n}")
for e in errs + faults: print(f"ERROR: {e}")
sys.exit(1 if (errs or faults) else 0)
PY
[ $? -eq 0 ] || fail "regression assertions or fault check failed"
echo "RETRO12_REGRESSION_DONE"
