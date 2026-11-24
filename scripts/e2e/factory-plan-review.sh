#!/bin/zsh
# 11.1: plan-review live-check script (mirrors scripts/e2e/factory-retro.sh).
# Usage: scripts/e2e/factory-plan-review.sh --mode unavailable <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo}; optional env: PORT, ENV_FILE, OP_TOKEN_FILE.
set -u

usage() {
  echo "Usage: $0 --mode unavailable <demo-dir>" >&2
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
  unavailable) ;;
  "") echo "Missing required --mode flag" >&2; usage; exit 1 ;;
  *) echo "Unknown mode: $MODE (expected unavailable)" >&2; usage; exit 1 ;;
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

G='Slice 11.1 plan-review unavailable test. Plan EXACTLY ONE task with id T1 (after: [], no gate, estimate_lines: 30):
- T1: add func PlanCalc(n int) int returning n + 1 in a new file hello/plan11.go with unit test TestPlanCalc in hello/plan11_test.go; outcome: "PlanCalc(n) returns n+1" proved by test: TestPlanCalc.
Keep the implementation and test minimal so go test ./... passes on the first commit. After applying the plan, wait and do not amend, retry, or drop T1.'

daemon
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2

# Set plan_review_rounds=1 and pin agy coordinator/worker/reviewer with no models.plan_reviewer.
pin_agy_models "$J" '{"plan_review_rounds":1}' '{}'

"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "restart after settings edit refused"
echo "== started $PID (--mode $MODE) on $U at $(date +%T)"

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
