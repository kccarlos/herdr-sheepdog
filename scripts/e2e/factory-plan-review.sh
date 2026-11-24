#!/bin/zsh
# 11.1–11.2: plan-review live-check script (mirrors scripts/e2e/factory-retro.sh).
# Usage: scripts/e2e/factory-plan-review.sh --mode <unavailable|launch> <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo}; optional env: PORT, ENV_FILE, OP_TOKEN_FILE.
set -u

usage() {
  echo "Usage: $0 --mode <unavailable|launch> <demo-dir>" >&2
  echo "Optional env: PORT (default 17795), ENV_FILE, OP_TOKEN_FILE" >&2
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
  unavailable|launch) ;;
  "") echo "Missing required --mode flag" >&2; usage; exit 1 ;;
  *) echo "Unknown mode: $MODE (expected unavailable, launch)" >&2; usage; exit 1 ;;
esac

[ -n "$D" ] || { usage; exit 1; }

PORT="${PORT:-17795}"
R="$D/test_repo"; U="http://127.0.0.1:$PORT"; C="$D/config.toml"
ENV_FILE="${ENV_FILE:-}"; OP_TOKEN_FILE="${OP_TOKEN_FILE:-}"
DNAME="e2-planrev11-daemon"; J=""; L=""; PID=""; TID=""

source "${0:A:h}/factory-lib.sh"
extra_cleanup() { cleanup_repo_prs_branches; }
trap cleanup_trap EXIT

factory_preflight

if [ "$MODE" = "unavailable" ]; then
  G='Slice 11.1 plan-review unavailable test. Plan EXACTLY ONE task with id T1 (after: [], no gate, estimate_lines: 30):
- T1: add func PlanCalc(n int) int returning n + 1 in a new file hello/plan11.go with unit test TestPlanCalc in hello/plan11_test.go; outcome: "PlanCalc(n) returns n+1" proved by test: TestPlanCalc.
Keep the implementation and test minimal so go test ./... passes on the first commit. After applying the plan, wait and do not amend, retry, or drop T1.'
  EXTRA_MODELS='{}'
else
  G='Slice 11.2 plan-review launch test. Add func PlanCalc(n int) int returning n + 1 in hello/plan11.go with unit test TestPlanCalc in hello/plan11_test.go (outcome: "PlanCalc(n) returns n+1" proved by test: TestPlanCalc; estimate_lines: 30). On the backlog, not asked for now: a --verbose flag. Plan it as its own task. After applying the plan, wait and do not amend, retry, or drop tasks.'
  EXTRA_MODELS='{"plan_reviewer":[{"cli":"agy","model":"gemini-4-neon-medium","effort":"medium"},{"cli":"agy","model":"gemini-4-neon-high","effort":"medium"}]}'
fi

daemon
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2

pin_agy_models "$J" '{"plan_review_rounds":1}' "$EXTRA_MODELS"

"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "restart after settings edit refused"
echo "== started $PID (--mode $MODE) on $U at $(date +%T)"

if [ "$MODE" = "unavailable" ]; then
  step "plan_apply"
  wait_py "plan apply in log.jsonl and task in state.json" 300 10 \
    'any(x.get("rule")=="plan apply" for x in log) and len(state.get("tasks") or [])>=1 and state["tasks"][0]["id"]'
  TID=$(python3 -c "import json;ts=json.load(open('$J')).get('tasks') or [];print(ts[0]['id'] if len(ts)==1 else f'COUNT:{len(ts)}')")
  [[ "$TID" == COUNT:* ]] && forcing_fail "coordinator planned ${TID#COUNT:} tasks instead of 1"

  step "j16_unavailable"
  wait_py "J16 accept unreviewed in log.jsonl" 240 5 \
    '([x for x in log if x.get("rule")=="J16" and "unreviewed" in str(x.get("decision","")).lower()] or [None])[0]'

  step "n11_start"
  wait_py "N11 task start on $TID" 240 5 \
    '([x for x in log if x.get("task")==val and x.get("rule")=="N11"] or [None])[0]' "$TID"

  step "n42_merge"
  wait_py "N42 accept and merge on $TID" 1500 15 \
    '([x for x in log if x.get("task")==val and x.get("rule")=="N42" and any("merge@" in a for a in (x.get("asked") or []))] or [None])[0]' "$TID"

  step "n2_done"
  wait_py "N2 PR merged and task done on $TID" 300 10 \
    'any(x.get("task")==val and x.get("rule")=="N2" for x in log) and any(t.get("id")==val and t.get("done") for t in (state.get("tasks") or [])) and "N2_MERGED"' "$TID"

  echo "== sheepdog factory log:"
  "$D/sheepdog" factory log "$R" --config "$C"
  echo "== assertions, rule summary & fault check:"
  python3 - "$L" "$J" "$TID" <<'PY'
import collections, json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state, tid = json.load(open(sys.argv[2])), sys.argv[3]
tasks = state.get("tasks") or []
t = [x for x in tasks if x.get("id") == tid][0]
prs = (state.get("project") or {}).get("plan_reviews") or []

applies = [x for x in log if x.get("rule") == "plan apply"]
j16s = [x for x in log if x.get("rule") == "J16" and "unreviewed" in str(x.get("decision", "")).lower()]
n11s = [x for x in log if x.get("rule") == "N11"]
n2s = [x for x in log if x.get("task") == tid and x.get("rule") == "N2"]
errs = []

if not applies:
    errs.append("missing plan apply line in log.jsonl")
if not j16s:
    errs.append("missing J16 line accepting plan unreviewed in log.jsonl")
if not n11s:
    errs.append("missing N11 line in log.jsonl")
if applies and j16s and j16s[0].get("seq", 0) <= applies[0].get("seq", 0):
    errs.append(f"J16 seq={j16s[0].get('seq')} not after plan apply seq={applies[0].get('seq')}")
if j16s and n11s and min(x.get("seq", 0) for x in n11s) <= j16s[0].get("seq", 0):
    errs.append(f"first N11 seq={min(x.get('seq', 0) for x in n11s)} not after J16 seq={j16s[0].get('seq')}")

if len(prs) != 1:
    errs.append(f"expected 1 plan_reviews record in state.json, got {prs}")
else:
    pr = prs[0]
    if pr.get("verdict") != "unavailable":
        errs.append(f"plan_reviews[0].verdict={pr.get('verdict')!r}, want 'unavailable'")
    if not str(pr.get("why") or "").strip():
        errs.append(f"plan_reviews[0].why is empty: {pr}")
    acc = str(pr.get("accepted") or "").strip()
    if not acc or acc.startswith("0001-01-01"):
        errs.append(f"plan_reviews[0].accepted is unset: {pr}")

unrev = [x.get("id") for x in tasks if x.get("unreviewed")]
if unrev:
    errs.append(f"tasks still marked unreviewed at end: {unrev}")
if not n2s:
    errs.append(f"missing N2 merged line for {tid}")
if not t.get("done"):
    errs.append(f"expected task {tid} done=true after merge")

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
  echo "PLANREVIEW11_UNAVAILABLE_DONE"
  exit 0
fi

# --mode launch (PR 11.2):
step "plan_apply"
wait_py "plan apply in log.jsonl and tasks in state.json" 300 5 \
  'any(x.get("rule")=="plan apply" for x in log) and len(state.get("tasks") or [])>=1 and len(state.get("tasks") or [])'
TCOUNT=$(python3 -c "import json;print(len(json.load(open('$J')).get('tasks') or []))")
[ "$TCOUNT" -eq 2 ] || forcing_fail "coordinator planned $TCOUNT task(s) instead of 2 (needed + --verbose)"

step "j16_launch"
wait_py "J16 line in log.jsonl" 240 2 \
  '([x for x in log if x.get("rule")=="J16"] or [None])[0]'
J16_CHK=$(python3 - "$L" "$J" <<'PY'
import json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state = json.load(open(sys.argv[2]))
j16 = [x for x in log if x.get("rule") == "J16"][0]
prs = (state.get("project") or {}).get("plan_reviews") or []
dec = str(j16.get("decision", ""))
asked = j16.get("asked") or []
if "unavailable" in dec.lower() or (prs and prs[0].get("verdict") == "unavailable" and not any("start_plan_reviewer@1" in a for a in asked)):
    why = prs[0].get("why", dec) if prs else j16.get("why", dec)
    print(f"UNAVAIL:{why}")
elif "start_plan_reviewer@1" not in asked or "agy/gemini-4-neon-high" not in dec:
    print(f"FAIL:J16 asked={asked} decision={dec!r}, expected start_plan_reviewer@1 and agy/gemini-4-neon-high")
else:
    print(f"OK:seq={j16.get('seq')} decision={dec}")
PY
)
echo "   $J16_CHK"
if [[ "$J16_CHK" == UNAVAIL:* ]]; then
  echo "== the plan reviewer was unavailable: ${J16_CHK#UNAVAIL:}"
  exit 3
fi
[[ "$J16_CHK" == FAIL:* ]] && fail "${J16_CHK#FAIL:}"

STATUS_SEEN=0
for _ in $(seq 1 15); do
  ST=$("$D/sheepdog" factory status "$R" --config "$C" 2>&1 || true)
  PV=$(curl -sf --max-time 2 "$U/api/factory/projects" 2>/dev/null || true)
  HOLD_OK=$(python3 - "$J" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
prs = (s.get("project") or {}).get("plan_reviews") or []
live = bool(prs and not prs[0].get("verdict") and (prs[0].get("run") or {}).get("run_id"))
atts = sum(len(t.get("attempts") or []) for t in (s.get("tasks") or []))
if live and atts > 0:
    print(f"FAIL:attempts={atts} started while plan reviewer run lives")
elif live:
    print("LIVE")
else:
    print("ENDED")
PY
  )
  [[ "$HOLD_OK" == FAIL:* ]] && fail "${HOLD_OK#FAIL:}"
  WT_CNT=$(printf '%s\n' "$ST" | grep -c "Waiting on the plan review" || true)
  if [ "$WT_CNT" -ge 2 ] && printf '%s' "$PV" | grep -q '"Plan review"'; then
    STATUS_SEEN=1
    echo "   status while live:"
    printf '%s\n' "$ST"
    break
  fi
  [ "$HOLD_OK" = "ENDED" ] && break
  sleep 1
done
[ "$STATUS_SEEN" -eq 1 ] || fail "did not observe 'Plan review' project status and both tasks 'Waiting on the plan review' while run lived (status: ${ST:-<empty>})"

step "j15_unavailable"
wait_py "J15 accept unavailable in log.jsonl" 600 5 \
  '([x for x in log if x.get("rule")=="J15" and "unavailable" in str(x.get("decision","")).lower()] or [None])[0]'

step "n11_after_j15"
wait_py "first N11 task start in log.jsonl" 240 5 \
  '([x for x in log if x.get("rule")=="N11"] or [None])[0]'

echo "== sheepdog factory log:"
"$D/sheepdog" factory log "$R" --config "$C"
echo "== assertions, rule summary & fault check:"
python3 - "$L" "$J" <<'PY'
import collections, json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state = json.load(open(sys.argv[2]))
tasks = state.get("tasks") or []
prs = (state.get("project") or {}).get("plan_reviews") or []

applies = [x for x in log if x.get("rule") == "plan apply"]
j16s = [x for x in log if x.get("rule") == "J16" and "start_plan_reviewer@1" in (x.get("asked") or [])]
j15s = [x for x in log if x.get("rule") == "J15" and "unavailable" in str(x.get("decision", "")).lower()]
n11s = [x for x in log if x.get("rule") == "N11"]
errs = []

if not applies or not j16s or not j15s or not n11s:
    errs.append(f"missing expected log lines: apply={len(applies)} j16={len(j16s)} j15={len(j15s)} n11={len(n11s)}")
else:
    s_app, s_j16, s_j15, s_n11 = applies[0]["seq"], j16s[0]["seq"], j15s[0]["seq"], min(x["seq"] for x in n11s)
    if not (s_app < s_j16 < s_j15 < s_n11):
        errs.append(f"wrong log sequence order: apply={s_app} J16={s_j16} J15={s_j15} first_N11={s_n11}")
    if "agy/gemini-4-neon-high" not in str(j16s[0].get("decision", "")):
        errs.append(f"J16 decision did not name agy/gemini-4-neon-high: {j16s[0]}")

if len(prs) != 1:
    errs.append(f"expected 1 plan_reviews record in state.json, got {prs}")
else:
    pr = prs[0]
    acc = str(pr.get("accepted") or "").strip()
    if pr.get("verdict") != "unavailable" or not str(pr.get("why") or "").strip() or not acc or acc.startswith("0001-01-01"):
        errs.append(f"invalid plan_reviews[0] record: {pr}")
    rev = pr.get("run") or {}
    if rev.get("run_id") or rev.get("run_token_hash"):
        errs.append(f"plan_reviews[0].run left run_id or token hash: {rev}")

if [x.get("id") for x in tasks if x.get("unreviewed")]:
    errs.append(f"tasks still marked unreviewed after J15 accept: {tasks}")

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
echo "PLANREVIEW11_LAUNCH_DONE"
