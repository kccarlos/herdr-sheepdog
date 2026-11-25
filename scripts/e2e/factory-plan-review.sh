#!/bin/zsh
# 11.3: plan-review live-check script (mirrors scripts/e2e/factory-retro.sh).
# Usage: scripts/e2e/factory-plan-review.sh --mode delete <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo}; optional env: PORT, ENV_FILE, OP_TOKEN_FILE.
set -u

usage() {
  echo "Usage: $0 --mode delete <demo-dir>" >&2
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
  delete) ;;
  "") echo "Missing required --mode flag" >&2; usage; exit 1 ;;
  *) echo "Unknown mode: $MODE (expected delete)" >&2; usage; exit 1 ;;
esac

[ -n "$D" ] || { usage; exit 1; }

PORT="${PORT:-17795}"
R="$D/test_repo"; U="http://127.0.0.1:$PORT"; C="$D/config.toml"
ENV_FILE="${ENV_FILE:-}"; OP_TOKEN_FILE="${OP_TOKEN_FILE:-}"
DNAME="e2-planrev11-daemon"; J=""; L=""; PID=""; NEED_ID=""; FLAG_ID=""

source "${0:A:h}/factory-lib.sh"
extra_cleanup() { cleanup_repo_prs_branches; }
trap cleanup_trap EXIT

factory_preflight

# A fresh name per run: a name an earlier run already merged makes the reviewer delete the needed task too.
N=$(date +%H%M%S)
G="Add func PlanCalc$N(n int) int returning n + 1 in a new file hello/plan11_$N.go with unit test TestPlanCalc$N in hello/plan11_${N}_test.go (after: [], no gate, outcome: \"PlanCalc$N(n) returns n+1\" proved by test: TestPlanCalc$N; estimate_lines: 30). On the backlog, not asked for now: a --verbose flag. Plan it as its own task."

daemon
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2

# Leave models.plan_reviewer unset so Candidates falls back to models.reviewer
# (agy/gemini-4-neon-high, agy/gemini-4-carbon-medium), stepping past the
# coordinator's agy/gemini-4-neon-medium (plan.md 11.3, P7).
pin_agy_models "$J" '{"plan_review_rounds":1}' '{}'

"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "restart after settings edit refused"
echo "== started $PID (--mode $MODE) on $U at $(date +%T)"

step "plan_apply"
wait_py "plan apply in log.jsonl and tasks in state.json" 300 5 \
  'any(x.get("rule")=="plan apply" for x in log) and len(state.get("tasks") or [])>=1 and len(state.get("tasks") or [])'
TASKS_INFO=$(python3 - "$J" <<'PY'
import json, sys
ts = json.load(open(sys.argv[1])).get("tasks") or []
if len(ts) != 2:
    print(f"FORCE:coordinator planned {len(ts)} task(s) instead of 2 (needed + --verbose)")
    sys.exit(0)
flag = [t for t in ts if "verbose" in (str(t.get("title", "")) + " " + str(t.get("brief", "")) + " " + str(t.get("if_skipped", ""))).lower()]
need = [t for t in ts if t not in flag]
if len(flag) != 1 or len(need) != 1:
    print(f"FORCE:could not distinguish needed task from --verbose task in {[t.get('id')+':'+t.get('title','') for t in ts]}")
else:
    print(f"OK:{need[0]['id']}:{flag[0]['id']}")
PY
)
echo "   $TASKS_INFO"
[[ "$TASKS_INFO" == FORCE:* ]] && forcing_fail "${TASKS_INFO#FORCE:}"
NEED_ID=$(printf '%s' "$TASKS_INFO" | cut -d: -f2)
FLAG_ID=$(printf '%s' "$TASKS_INFO" | cut -d: -f3)

step "j16_launch"
wait_py "J16 start_plan_reviewer in log.jsonl" 240 2 \
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
    print(f"FAIL:J16 asked={asked} decision={dec!r}, expected start_plan_reviewer@1 and fallback agy/gemini-4-neon-high")
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

step "plan_review_verdict"
wait_py "plan-review verdict or J15 unavailable" 600 5 \
  '([x for x in log if x.get("rule")=="plan-review" or (x.get("rule")=="J15" and "unavailable" in str(x.get("decision","")).lower())] or [None])[0]'
VERDICT_CHK=$(python3 - "$J" "$FLAG_ID" <<'PY'
import json, sys
state, fid = json.load(open(sys.argv[1])), sys.argv[2]
prs = (state.get("project") or {}).get("plan_reviews") or []
if not prs:
    print("FAIL:missing plan_reviews[0] in state.json"); sys.exit(0)
pr = prs[0]
v = pr.get("verdict") or ""
if v == "unavailable":
    print(f"UNAVAIL:{pr.get('why', '')}")
elif v == "pass":
    print(f"FORCE:plan reviewer returned pass with optional task {fid} in plan")
elif v != "changes":
    print(f"FAIL:unexpected verdict={v!r} in {pr}")
else:
    fs = [f for f in (pr.get("findings") or []) if f.get("task") == fid and f.get("kind") == "delete"]
    if not fs:
        print(f"FORCE:plan reviewer returned changes without a delete finding on {fid}: {pr.get('findings')}")
    else:
        print(f"OK:findings={pr.get('findings')}")
PY
)
echo "   $VERDICT_CHK"
if [[ "$VERDICT_CHK" == UNAVAIL:* ]]; then
  echo "== the plan reviewer was unavailable: ${VERDICT_CHK#UNAVAIL:}"
  exit 3
fi
[[ "$VERDICT_CHK" == FORCE:* ]] && forcing_fail "${VERDICT_CHK#FORCE:}"
[[ "$VERDICT_CHK" == FAIL:* ]] && fail "${VERDICT_CHK#FAIL:}"

# Capture the plan-review wake facts from state.json before or while J12 delivers it.
WAKE_TEXT=$(python3 - "$J" <<'PY'
import json, sys
p = (json.load(open(sys.argv[1])).get("project") or {})
for w in reversed((p.get("coord_wakes_in_flight") or []) + (p.get("wakes") or [])):
    if str(w.get("key") or "").startswith("plan_review@"):
        print((w.get("why") or "") + " " + (w.get("facts") or ""))
        sys.exit(0)
PY
)

step "j12_wake_and_close_finding"
wait_py "J12 wake delivery" 180 2 \
  '([x for x in log if x.get("rule")=="J12" and any("plan_review@" in a for a in (x.get("asked") or []))] or [None])[0]'
if [ -z "$WAKE_TEXT" ]; then
  COORD_ID=$(python3 -c "import json;print((json.load(open('$J')).get('project') or {}).get('run_id') or '')")
  [ -n "$COORD_ID" ] && WAKE_TEXT=$(hc read "$COORD_ID" 2>/dev/null || true)
fi

wait_py "coordinator closes finding on $FLAG_ID" 300 5 \
  'any(x.get("rule") in ("task drop","plan reject","plan amend") for x in log) and all(f.get("closed") for f in (((state.get("project") or {}).get("plan_reviews") or [{}])[0].get("findings") or []) if f.get("task")==val) and "finding_closed"' "$FLAG_ID"
CLOSE_CHK=$(python3 - "$J" "$FLAG_ID" <<'PY'
import json, sys
s, fid = json.load(open(sys.argv[1])), sys.argv[2]
pr = ((s.get("project") or {}).get("plan_reviews") or [{}])[0]
fs = [f for f in (pr.get("findings") or []) if f.get("task") == fid]
ft = [t for t in (s.get("tasks") or []) if t.get("id") == fid][0]
if not ft.get("dropped") or any(f.get("closed") != "dropped" for f in fs):
    print(f"FORCE:coordinator closed {fid} via {[f.get('closed') for f in fs]} (dropped={ft.get('dropped')}) instead of task drop")
else:
    print(f"OK:{fid} dropped; findings={fs}")
PY
)
echo "   $CLOSE_CHK"
[[ "$CLOSE_CHK" == FORCE:* ]] && forcing_fail "${CLOSE_CHK#FORCE:}"

step "j16_accept_and_n11"
wait_py "J16 accept plan in log.jsonl" 240 5 \
  '([x for x in log if x.get("rule")=="J16" and any("accept_plan" in a for a in (x.get("asked") or []))] or [None])[0]'
wait_py "N11 task start on $NEED_ID" 240 5 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N11"] or [None])[0]' "$NEED_ID"

step "n42_and_n2_merged"
wait_py "N42 accept and merge on $NEED_ID" 1500 15 \
  '([x for x in log if x.get("task")==val and x.get("rule")=="N42" and any("merge@" in a for a in (x.get("asked") or []))] or [None])[0]' "$NEED_ID"
wait_py "N2 PR merged and task done on $NEED_ID" 300 10 \
  'any(x.get("task")==val and x.get("rule")=="N2" for x in log) and any(t.get("id")==val and t.get("done") for t in (state.get("tasks") or [])) and "N2_MERGED"' "$NEED_ID"

HC_JSON=$(hc ls --json 2>/dev/null || echo '{"terminals":[]}')

echo "== sheepdog factory log:"
"$D/sheepdog" factory log "$R" --config "$C"
echo "== assertions, rule summary & fault check:"
python3 - "$L" "$J" "$PID" "$NEED_ID" "$FLAG_ID" "$WAKE_TEXT" "$HC_JSON" <<'PY'
import collections, json, sys
log = [json.loads(ln) for ln in open(sys.argv[1]) if ln.strip()]
state, pid, need_id, flag_id, wake_text, hc_ls = json.load(open(sys.argv[2])), sys.argv[3], sys.argv[4], sys.argv[5], sys.argv[6], json.loads(sys.argv[7])
tasks = {t["id"]: t for t in (state.get("tasks") or [])}
prs = (state.get("project") or {}).get("plan_reviews") or []

applies = [x for x in log if x.get("rule") == "plan apply"]
j16_starts = [x for x in log if x.get("rule") == "J16" and "start_plan_reviewer@1" in (x.get("asked") or [])]
pr_lines = [x for x in log if x.get("rule") == "plan-review" and x.get("decision") == "changes"]
j12_lines = [x for x in log if x.get("rule") == "J12" and any("plan_review@" in a for a in (x.get("asked") or []))]
drop_lines = [x for x in log if x.get("rule") == "task drop" and x.get("task") == flag_id]
j16_accepts = [x for x in log if x.get("rule") == "J16" and any("accept_plan" in a for a in (x.get("asked") or []))]
n11_all = [x for x in log if x.get("rule") == "N11"]
n42_need = [x for x in log if x.get("task") == need_id and x.get("rule") == "N42" and any("merge@" in a for a in (x.get("asked") or []))]
n2_need = [x for x in log if x.get("task") == need_id and x.get("rule") == "N2"]
errs = []

if not (applies and j16_starts and pr_lines and j12_lines and drop_lines and j16_accepts and n11_all and n42_need and n2_need):
    errs.append(f"missing expected log lines: apply={len(applies)} j16_start={len(j16_starts)} plan_review={len(pr_lines)} j12={len(j12_lines)} drop={len(drop_lines)} j16_accept={len(j16_accepts)} n11={len(n11_all)} n42={len(n42_need)} n2={len(n2_need)}")
else:
    s_app, s_st, s_pr, s_j12, s_dr, s_acc, s_n11, s_n42, s_n2 = (
        applies[0]["seq"], j16_starts[0]["seq"], pr_lines[0]["seq"], j12_lines[0]["seq"],
        drop_lines[0]["seq"], j16_accepts[0]["seq"], min(x["seq"] for x in n11_all),
        n42_need[0]["seq"], n2_need[0]["seq"],
    )
    if not (s_app < s_st < s_pr < s_j12 < s_dr < s_acc < s_n11 < s_n42 < s_n2):
        errs.append(f"wrong log sequence order: apply={s_app} j16_start={s_st} plan_review={s_pr} j12={s_j12} drop={s_dr} j16_accept={s_acc} first_n11={s_n11} n42={s_n42} n2={s_n2}")

if flag_id not in wake_text or "delete" not in wake_text:
    errs.append(f"plan-review wake text missing delete finding for {flag_id}: {wake_text[:300]!r}")

if len(prs) < 1:
    errs.append(f"expected >=1 plan_reviews record, got {prs}")
else:
    pr = prs[0]
    acc = str(pr.get("accepted") or "").strip()
    if pr.get("verdict") != "changes" or not acc or acc.startswith("0001-01-01"):
        errs.append(f"invalid plan_reviews[0] verdict/accepted: {pr}")
    if not any(f.get("task") == flag_id and f.get("kind") == "delete" and f.get("closed") == "dropped" for f in (pr.get("findings") or [])):
        errs.append(f"plan_reviews[0] missing closed=dropped delete finding for {flag_id}: {pr.get('findings')}")
    rev = pr.get("run") or {}
    if rev.get("run_id") or rev.get("run_token_sha256"):
        errs.append(f"plan_reviews[0].run left run_id or token hash: {rev}")

ft, nt = tasks.get(flag_id, {}), tasks.get(need_id, {})
if not ft.get("dropped") or len(ft.get("attempts") or []) != 0:
    errs.append(f"flag task {flag_id} expected dropped with 0 attempts, got dropped={ft.get('dropped')} attempts={ft.get('attempts')}")
if not nt.get("done"):
    errs.append(f"needed task {need_id} expected done=true, got {nt}")
if [tid for tid, t in tasks.items() if t.get("unreviewed")]:
    errs.append(f"tasks still marked unreviewed at end: {[tid for tid, t in tasks.items() if t.get('unreviewed')]}")

pr_terms = [t.get("address") for t in (hc_ls.get("terminals") or []) if (t.get("labels") or {}).get("sheepdog.project") == pid and (t.get("labels") or {}).get("sheepdog.role") == "plan_reviewer"]
if pr_terms:
    errs.append(f"plan_reviewer terminal(s) left in hc: {pr_terms}")
if any("stray_terminal" in str(x.get("asked", "")) + str(x.get("why", "")) for x in log) or any("stray_terminal" in k for k in ((state.get("project") or {}).get("sweep_items") or {})):
    errs.append("stray_terminal wake or sweep item recorded")

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
echo "PLANREVIEW11_DELETE_DONE"
