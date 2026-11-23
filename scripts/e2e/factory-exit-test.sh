#!/bin/zsh
# 8.2: version 1 exit test forcing script.
# Usage: OWNER_ANS=/path/to/ownerans VOICE_ENV=/path/to/voice.env.tpl GH_TOKEN=... \
#   scripts/e2e/factory-exit-test.sh <demo-dir>
# Expects <demo-dir>/{sheepdog,test_repo,scenarios.json}; optional: REPO (owner/repo), OP_TOKEN_FILE.
set -u
if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ] || [ $# -lt 1 ]; then
  echo "Usage: OWNER_ANS=<ownerans-bin> VOICE_ENV=<voice.env.tpl> GH_TOKEN=<token> $0 <demo-dir>" >&2
  exit 1
fi
SCRIPT_DIR="${0:A:h}"
D="${1%/}"; R="$D/test_repo"; U="http://127.0.0.1:17793"; C="$D/config.toml"; VDONE="$D/voice.done"
OWNER_ANS="${OWNER_ANS:-}"; VOICE_ENV="${VOICE_ENV:-$D/voice.env.tpl}"; OP_TOKEN_FILE="${OP_TOKEN_FILE:-}"
DNAME="e2-exit82-daemon"; MAIN_RED_PUSHED=0; J=""; L=""; PID=""

fail() { echo "== FAIL: $1"; exit 1; }
evt()  { echo "== EVENT $1 $2 forced at $(date +%T)"; }
git_gh() { git -C "$R" -c credential.helper= -c credential.helper='!f(){ echo username=x-access-token; echo "password=$GH_TOKEN"; }; f' "$@"; }

revert_main_red() {
  [ "$MAIN_RED_PUSHED" = "1" ] || return 0
  echo "== reverting temporary ci/main-fail on main $(date +%T)"
  git_gh revert --no-edit HEAD >/dev/null 2>&1 && git_gh push -q origin HEAD:main && MAIN_RED_PUSHED=0 && echo "== reverted: $(git -C "$R" log --oneline -1)"
}
stopd() { hc kill "$DNAME" >/dev/null 2>&1; hc rm "$DNAME" >/dev/null 2>&1; sleep 3; }
cleanup_trap() {
  revert_main_red || true
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
  if [ -n "$OP_TOKEN_FILE" ] && [ -f "$OP_TOKEN_FILE" ]; then
    hc run --bg --json --name "$DNAME" --cwd "$D" -- zsh -c "OP_SERVICE_ACCOUNT_TOKEN=\"\$(<\"$OP_TOKEN_FILE\")\" exec op run --env-file=\"$VOICE_ENV\" -- env -u OP_SERVICE_ACCOUNT_TOKEN \"$D/sheepdog\" serve --config \"$C\" --voice=gemini --brain=off --cli=off" >/dev/null || fail "hc run daemon"
  else
    hc run --bg --json --name "$DNAME" --cwd "$D" -- op run --env-file="$VOICE_ENV" -- "$D/sheepdog" serve --config "$C" --voice=gemini --brain=off --cli=off >/dev/null || fail "hc run daemon"
  fi
  for _ in $(seq 1 20); do curl -sf --max-time 2 "$U/api/state" >/dev/null && return 0; sleep 1; done
  fail "daemon did not answer on $U within 20s"
}

# wait_py <label> <timeout_sec> <poll_sec> <python_expr> [val]
# Expr sees: log (list of dicts from $L), state (dict from $J), val (optional arg).
wait_py() {
  local label=$1 lim=$2 step=$3 expr=$4 val=${5:-} elapsed=0 out=""
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
    sleep "$step"; elapsed=$((elapsed + step))
  done
  fail "TIMEOUT (${lim}s) waiting for $label"
}

# Preflight: expect pre-populated $D/sheepdog, $D/test_repo, $D/scenarios.json, $VOICE_ENV, $OWNER_ANS, $GH_TOKEN.
[ -x "$D/sheepdog" ] || fail "missing executable $D/sheepdog"
[ -d "$R/.git" ] || fail "missing repo $R"
[ -f "$D/scenarios.json" ] || fail "missing $D/scenarios.json"
[ -n "$VOICE_ENV" ] && [ -f "$VOICE_ENV" ] || fail "missing VOICE_ENV file (needed for --voice=gemini)"
[ -n "$OWNER_ANS" ] && [ -x "$OWNER_ANS" ] || fail "missing executable OWNER_ANS helper"
[ -n "${GH_TOKEN:-}" ] || fail "GH_TOKEN not set"
[ ! -e "$VDONE" ] || fail "stale $VDONE exists; remove it before running"
[ ! -e "$R/ci/main-fail" ] || fail "$R/ci/main-fail already exists on main"
REPO="${REPO:-$(git -C "$R" remote get-url origin 2>/dev/null | sed -E 's#^(https?://[^/]+/|git@[^:]+:)##; s#\.git$##')}"
[ -n "$REPO" ] || fail "could not determine REPO (set REPO=owner/repo)"
mkdir -p "$D/data"
if [ ! -f "$C" ]; then
  cat > "$C" <<EOF
data_dir = "$D/data"
[web]
bind = "127.0.0.1"
port = 17793
[panel]
auto_update = false
[update]
check = false
EOF
fi

G='Version 1 exit test. Plan EXACTLY three tasks with ids T1, T2, T3:
- T1 (after: [], no gate): add func Exit82Base(n int) int returning 37*n in a new file hello/exit82_a.go with unit test TestExit82Base in hello/exit82_a_test.go, commit, and report ready.
- T2 (after: [], no gate): add func Exit82Peer(n int) int returning 38*n in a new file hello/exit82_b.go with unit test TestExit82Peer in hello/exit82_b_test.go, commit, and report ready.
- T3 (after: ["T1"], "gate": "owner"): add func Exit82Dep(n int) int calling Exit82Base(n)+n (returning 38*n for all n, including negative n) in a new file hello/exit82_c.go with unit test TestExit82Dep in hello/exit82_c_test.go; outcome: "Exit82Dep(n) calls Exit82Base(n)+n and returns 38*n for all n, including negative n" proved by test: TestExit82Dep. Put these EXACT worker steps in T3 brief verbatim:
  1) If notes/exit82-t3-a.txt does not exist: mkdir -p notes, write "a" to notes/exit82-t3-a.txt (uncommitted), then run in foreground: for i in $(seq 1 18); do echo tick-a $i; sleep 10; done
  2) If notes/exit82-t3-b.txt does not exist: mkdir -p notes, write "b" to notes/exit82-t3-b.txt (uncommitted), then run in foreground: for i in $(seq 1 18); do echo tick-b $i; sleep 10; done
  3) Remove notes/exit82-t3-a.txt and notes/exit82-t3-b.txt before committing.
  4) Exit-test review-loop hook: on your FIRST commit ONLY (when git log -1 does not mention "review fix"), implement Exit82Dep to return 0 for n < 0 and test only n=0,1,2 in TestExit82Dep so ready proofs pass; do NOT fix n < 0 until a reviewer reports changes. Note to reviewer: if Exit82Dep returns 0 for n < 0, you MUST submit changes with a major finding. After receiving review findings, fix Exit82Dep for all n including negative n, add a n=-2 test case, commit with "review fix" in the message, and report ready.
After applying the plan, check sheepdog factory status every 30 seconds; as soon as BOTH T1 and T2 have an open PR (stage review or later), run exactly: sheepdog factory ask --kind override --task T1 --task T2 and then wait.'

daemon
echo "== voice: $(curl -s --max-time 3 "$U/api/state" | python3 -c "import sys,json;print(json.load(sys.stdin).get('voice'))")"
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "initial start refused"
for _ in $(seq 1 30); do J=$(ls "$D"/data/factory/projects/*/state.json(N) 2>/dev/null | head -1); [ -n "$J" ] && break; sleep 1; done
[ -n "$J" ] || fail "TIMEOUT waiting for state.json"
L=$(dirname "$J")/log.jsonl; PID=$(python3 -c "import json;print(json.load(open('$J'))['project']['id'])")
"$D/sheepdog" factory stop "$R" --config "$C" >/dev/null; sleep 2
python3 - "$J" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p))
d["settings"]["models"] = {
  "coordinator": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"}],
  "worker": [{"cli": "agy", "model": "gemini-4-neon-medium", "effort": "medium"},
             {"cli": "agy", "model": "gemini-4-neon-high", "effort": "medium"}],
  "reviewer": [{"cli": "agy", "model": "gemini-4-neon-high", "effort": "medium"},
               {"cli": "agy", "model": "gemini-4-carbon-medium", "effort": "medium"}],
}
d["settings"]["pool_floor_percent"] = 0
d["settings"]["handoff_minutes"] = 2
json.dump(d, open(p, "w"), indent=2)
PY
"$D/sheepdog" factory start "$R" --goal "$G" --config "$C" >/dev/null || fail "restart after settings edit refused"
echo "== started $PID on $U at $(date +%T)"

# EVENT 1: Dependency hold (T3 waits on T1 at N9)
evt 1 dependency_hold
wait_py "EVENT 1 (N9 on T3)" 600 10 '[x for x in log if x.get("task")=="T3" and x.get("rule")=="N9"][-1:] and [x for x in log if x.get("task")=="T3" and x.get("rule")=="N9"][-1]'

# EVENT 2: Override of two independent open PRs (T1 & T2, slice 7.2)
wait_py "EVENT 2 (override ask A2)" 1200 15 '([a["id"] for a in (state.get("asks") or []) if a.get("kind")=="override" and a.get("status")=="open"] or [""])[0]'
OV_AID=$(python3 -c "import json;a=[x['id'] for x in json.load(open('$J')).get('asks') or [] if x.get('kind')=='override' and x.get('status')=='open'];print(a[0] if a else '')")
[ -n "$OV_AID" ] || fail "EVENT 2 missing open override ask id"
evt 2 override_two_prs
"$OWNER_ANS" -url "$U" -ask "$OV_AID" -answer "Yes, merge now" 2>&1 | grep -E 'answer ->|device_id|ok' | cut -c1-160 || fail "EVENT 2 override answer failed"
wait_py "EVENT 2 (N19+N2 on T1&T2 + OF2)" 600 15 '([x for x in log if x.get("rule")=="OF2"][-1:] and any(x.get("task")=="T1" and x.get("rule")=="N19" for x in log) and any(x.get("task")=="T2" and x.get("rule")=="N19" for x in log) and any(x.get("task")=="T1" and x.get("rule")=="N2" for x in log) and any(x.get("task")=="T2" and x.get("rule")=="N2" for x in log)) and [x for x in log if x.get("rule")=="OF2"][-1]'

# EVENT 3: Daemon restart mid-task (adopt live coordinator + T3 worker without duplicating)
wait_py "T3 worker started (N23)" 300 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N23" and "started worker" in str(x.get("decision",""))] or [None])[-1]'
T3_WT=$(python3 -c "import json;t=[x for x in json.load(open('$J'))['tasks'] if x['id']=='T3'][0];print(t['attempts'][-1]['worktree'])")
for i in $(seq 1 36); do [ -s "$T3_WT/notes/exit82-t3-a.txt" ] && break; [ "$i" -eq 36 ] && fail "TIMEOUT waiting for T3 marker a"; sleep 5; done
read -r PRE_CID PRE_WID PRE_STARTS <<< "$(python3 -c "import json;s=json.load(open('$J'));t=[x for x in s['tasks'] if x['id']=='T3'][0];c=sum(1 for l in open('$L') if str(json.loads(l).get('decision','')).startswith(('started worker ','started coordinator ')));print(s['project'].get('run_id',''), t['attempts'][-1].get('run_id',''), c)")"
evt 3 daemon_restart_adopt
stopd; daemon; sleep 20
POST=$(python3 -c "import json;s=json.load(open('$J'));t=[x for x in s['tasks'] if x['id']=='T3'][0];c=sum(1 for l in open('$L') if str(json.loads(l).get('decision','')).startswith(('started worker ','started coordinator ')));ok=(s['project'].get('run_id')=='$PRE_CID' and t['attempts'][-1].get('run_id')=='$PRE_WID' and c==$PRE_STARTS);print('ADOPT_OK' if ok else f'ADOPT_MISMATCH {s[\"project\"].get(\"run_id\")} {t[\"attempts\"][-1].get(\"run_id\")} {c}!=$PRE_STARTS')")
echo "   adopted: $POST (coord=$PRE_CID worker=$PRE_WID)"; [[ "$POST" == ADOPT_OK* ]] || fail "EVENT 3 daemon did not adopt live runs: $POST"

# EVENT 4: Worker killed by hand (R33 -> N23 restarts after backoff)
evt 4 worker_killed
hc kill "$PRE_WID" >/dev/null 2>&1 || fail "EVENT 4 hc kill $PRE_WID failed"
wait_py "EVENT 4 (R33 worker ended)" 120 5 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="R33" and "worker run" in str(x.get("why",""))] or [None])[-1]'
wait_py "EVENT 4 (N23 restarted worker)" 300 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N23" and "started worker" in str(x.get("decision","")) and val not in str(x.get("decision",""))] or [None])[-1]' "$PRE_WID"

# EVENT 5: Quota-floor handoff (R31 -> ended(quota) -> replacement worker with handoff)
for i in $(seq 1 48); do [ -s "$T3_WT/notes/exit82-t3-b.txt" ] && break; [ "$i" -eq 48 ] && fail "TIMEOUT waiting for T3 marker b"; sleep 5; done
evt 5 quota_floor_handoff
stopd
python3 - "$J" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p)); d["settings"]["pool_floor_percent"] = 100; json.dump(d, open(p, "w"), indent=2)
PY
daemon
wait_py "EVENT 5 (R31 ended(quota))" 300 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="R31" and x.get("decision")=="ended(quota)"] or [None])[-1]'
stopd
python3 - "$J" <<'PY'
import json, sys
p = sys.argv[1]; d = json.load(open(p)); d["settings"]["pool_floor_percent"] = 0; json.dump(d, open(p, "w"), indent=2)
PY
daemon
wait_py "EVENT 5 (N23 handoff worker after R31)" 300 10 '([x for x in log if x.get("seq",0) > max([y.get("seq",0) for y in log if y.get("task")=="T3" and y.get("rule")=="R31" and y.get("decision")=="ended(quota)"] or [10**9]) and x.get("task")=="T3" and x.get("rule")=="N23" and any("start_worker@handoff" in a for a in (x.get("asked") or [])) and "started worker" in str(x.get("decision",""))] or [None])[-1]'

# EVENT 6: Reviewer exits with no report (hc kill reviewer -> R33 -> N29 retries next candidate)
wait_py "T3 round 1 reviewer started (N29)" 900 15 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N29" and "started reviewer" in str(x.get("decision",""))] or [None])[-1]'
sleep 5
REV_ID=$(python3 -c "import json;t=[x for x in json.load(open('$J'))['tasks'] if x['id']=='T3'][0];print(((t['attempts'][-1].get('review') or {}).get('run_id')) or '')")
[ -n "$REV_ID" ] || fail "EVENT 6 could not find live T3 reviewer run_id"
evt 6 reviewer_no_report
hc kill "$REV_ID" >/dev/null 2>&1 || fail "EVENT 6 hc kill $REV_ID failed"
wait_py "EVENT 6 (R33 reviewer ended)" 120 5 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="R33" and "reviewer run" in str(x.get("why",""))] or [None])[-1]'
wait_py "EVENT 6 (N29 reviewer retry)" 240 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N29" and "started reviewer" in str(x.get("decision","")) and any("@1@1@" in a for a in (x.get("asked") or []))] or [None])[-1]'

# EVENT 7: Forced review change (verdict changes -> N22 -> worker fixes -> 2nd ready)
evt 7 forced_review_change
wait_py "EVENT 7 (verdict changes on T3)" 600 15 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="verdict" and x.get("decision")=="changes"] or [None])[-1]'
wait_py "EVENT 7 (N22 told worker findings)" 120 5 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N22"] or [None])[-1]'
wait_py "EVENT 7 (2nd ready on T3)" 600 15 '(len([x for x in log if x.get("task")=="T3" and x.get("rule")=="ready"])>=2) and [x for x in log if x.get("task")=="T3" and x.get("rule")=="ready"][-1]'

# EVENT 8: Owner gate (N31) + O25 live voice answer (with red base pre-armed before VOICE_STEP so N38 fires immediately after)
wait_py "EVENT 8 (N31 owner gate on T3)" 900 15 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N31" and "opened owner gate ask" in str(x.get("decision",""))] or [None])[-1]'
GATE_AID=$(python3 -c "import json;a=[x for x in json.load(open('$J')).get('asks') or [] if x.get('status')=='open' and x.get('kind')=='gate' and x.get('task')=='T3'];print(a[0]['id'] if a else '')")
[ -n "$GATE_AID" ] || fail "EVENT 8 no open gate ask for T3"

# Pre-arm EVENT 9 (push temporary ci/main-fail to main while T3 is held at N31)
git_gh fetch -q origin main && git -C "$R" checkout -q main && git -C "$R" merge -q --ff-only origin/main || fail "EVENT 9 fast-forward main failed"
mkdir -p "$R/ci"; echo "exit82 red base" > "$R/ci/main-fail"
git -C "$R" add ci/main-fail && git -C "$R" -c user.name=exit-test -c user.email=account@example.invalid commit -qm "temporary: red base for exit82 N38 test (reverted right after)" && git_gh push -q origin HEAD:main || fail "EVENT 9 push ci/main-fail failed"
MAIN_RED_PUSHED=1
wait_py "EVENT 9 (base_observer base turned red)" 300 10 '([x for x in log if x.get("rule")=="base_observer" and x.get("decision")=="base turned red"] or [None])[-1]'

# === VOICE_STEP ===
evt 8 owner_gate_and_voice_o25
echo "== VOICE_STEP: answer gate ask $GATE_AID (T3) by live voice on $U (speak 'Merge' after read-out), then: touch $VDONE"
if [ -x "$SCRIPT_DIR/budget.sh" ]; then (cd "$SCRIPT_DIR/../.." && "$SCRIPT_DIR/budget.sh" reserve voice 1 >/dev/null) || fail "EVENT 8 voice budget refused"; fi
echo "== audio-e2e speaks 'Merge' after the read-out:"; perl -e 'alarm 120; exec @ARGV' "$D/sheepdog" audio-e2e --url "$U" --scenarios "$D/scenarios.json" --only factory-gate-merge --cache "${TMPDIR:-/tmp}/sheepdog-audio-e2e" --json "$D/audio.json" 2>&1 | tail -6 | cut -c1-200
touch "$VDONE"
wait_py "EVENT 8 (A3 voice answer)" 120 5 '([x for x in log if x.get("rule")=="A3" and val in str(x.get("decision","")) and (x.get("input") or {}).get("device_id")=="voice-session"] or [None])[-1]' "$GATE_AID"

# EVENT 9: Red base holds T3 at N38, then revert ci/main-fail and wait for base green
evt 9 red_base_n38
wait_py "EVENT 9 (N38 holds T3)" 180 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N38"] or [None])[-1]'
revert_main_red || fail "EVENT 9 revert ci/main-fail failed"
wait_py "EVENT 9 (base_observer base turned green)" 300 10 '([x for x in log if x.get("rule")=="base_observer" and x.get("decision")=="base turned green"] or [None])[-1]'

# EVENT 10: PR merged by hand (gh pr merge after N40 updates branch -> N2 merged_outside_bar)
wait_py "EVENT 10 (N40 update branch on T3)" 240 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N40"] or [None])[-1]'
T3_PR=$(python3 -c "import json;t=[x for x in json.load(open('$J'))['tasks'] if x['id']=='T3'][0];print(t['attempts'][-1].get('pr') or '')")
[ -n "$T3_PR" ] || fail "EVENT 10 missing T3 PR number"
evt 10 pr_merged_by_hand
gh pr merge "$T3_PR" --repo "$REPO" --squash || fail "EVENT 10 gh pr merge #$T3_PR failed"
wait_py "EVENT 10 (N2 merged_outside_bar on T3)" 180 10 '([x for x in log if x.get("task")=="T3" and x.get("rule")=="N2" and any("merged_outside_bar" in a for a in (x.get("asked") or []))] or [None])[-1]'
wait_py "all tasks done + N1 cleanup" 240 10 '(all(t.get("done") for t in state.get("tasks",[])) and len(state.get("tasks",[]))==3 and any(x.get("task")=="T3" and x.get("rule")=="N1" for x in log)) and "ALL_3_DONE"'

echo "== sheepdog factory log:"
"$D/sheepdog" factory log "$R" --config "$C"
echo "== rule summary & fault check:"
python3 - "$L" <<'PY'
import collections, json, sys
cnt, faults = collections.Counter(), []
for line in open(sys.argv[1]):
    d = json.loads(line); r = d.get("rule") or ""
    cnt[r] += 1
    if r in ("", "N43", "R37", "P4", "A5") or "no rule matched" in (str(d.get("why","")) + str(d.get("decision",""))).lower():
        faults.append(f"FAULT seq={d.get('seq')} task={d.get('task','-')} rule={r or '(empty)'} why={d.get('why','')}")
for r, n in sorted(cnt.items()): print(f"  {r:18s} {n}")
for f in faults: print(f)
sys.exit(1 if faults else 0)
PY
echo "EXIT82_DONE"
