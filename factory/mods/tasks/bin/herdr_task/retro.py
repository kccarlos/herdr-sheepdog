"""SDF-49: the retro round. When review does not converge (two rounds in a row with new majors), the
engine starts one fresh read-only Fable session per task from RETRO_MD; its <retro>{...}</retro> tail
becomes task.retro (one record, never edited) and the engine acts on the verdict: SHIP counts as the
passing round, FIX is one bounded fix checked by a Fable round, REDESIGN (or FIX twice) leaves only
split, new or drop. Each outcome wakes the coordinator with event:retro."""
import json
import re
from types import SimpleNamespace

from .base import Fail, hc_json, now
from .budget import NON_CONVERGENCE, SMALL_LINES, SMALL_ROUNDS, change_lines, open_recap, review_nonconverging, rounds_since, size_estimate, token_sum
from .settings import FABLE, FABLE_CHANGES, fable_runs

# The bundled factory/templates/retro.md, word for word (test_retro keeps them equal).
RETRO_MD = """You are the retro reviewer for {task_ref} "{task_title}" in {project}. Read only: never edit, commit or push.
Normal review did not converge: {rounds_used} rounds, majors in the last {rounds_with_majors}. You give one
verdict on the code, then say what went wrong upstream so it is not repeated. Be plain; no praise.

Inputs
- Brief and acceptance: at the end of this prompt   - Design refs: the ones the brief names
- Diff: {base_sha}..{head_sha} in {worktree} ({changed_lines} lines; estimate {estimate_lines}; tokens {tokens_used})
- Worker: {worker}        - Reviewer: {reviewer}
- Rounds (round, head, minutes since the last, verdict, majors, note):
{rounds_table}
- Recap decisions so far: {recaps}           - Commits per round: git log {base_sha}..{head_sha}

Part 1: the code. Read the diff and run the project's checks ({checks_cmd}).
- Report majors only: wrong, unsafe, or loses data. Each needs a run or a reproduced state with real output.
- Do not look for the next single case. Ask what class the earlier findings belong to and whether the
  current head closes the class. If it does not, name the structural change.
- Verdict: SHIP (no majors), FIX (majors that a change under {small_lines} lines closes for the whole
  class), or REDESIGN (the approach or the task's shape is wrong; more fixes will not converge).

Part 2: the retro. Pick the one cause the evidence supports best, and at most one second cause:
  task-too-big | wrong-seam | vague-brief | design-needed-first | patch-shaped-fix |
  worker-model | reviewer-narrow | none
- Tell them apart by evidence. Same major returning, or fixes breaking what worked: worker-model. Every
  round a new instance of one class that the reviewer never named: reviewer-narrow or patch-shaped-fix.
  Majors across unrelated modules or size far over the estimate: task-too-big or wrong-seam. Majors that
  are questions about intended behaviour: vague-brief. One mechanism patched case by case: design-needed-first.
- Quote the round numbers or commits behind your choice. If nothing was wrong upstream, say none.
- Give exactly one change for the coordinator (planning, splitting or the brief) and one for the worker.
  Each is one sentence that could be acted on in the next task, not advice in general.
- Say whether the remaining work should be re-planned, and whether the models should change for it.
  Suggest only; the coordinator picks models. Do not blame a model without the evidence above.

End with your findings as plain text, then exactly one JSON object in this tag, nothing after it:
<retro>{"verdict":"SHIP|FIX|REDESIGN","majors":[{"file":"","line":0,"what":"","repro":""}],
"cause":"","second_cause":null,"evidence":["round 3: ..."],"coordinator_change":"","worker_change":"",
"replan":"none|split|spike-first|drop","replan_note":"","worker_model":"keep|raise|lower",
"reviewer":"keep|other-model|class-pass","confidence":"low|medium|high"}</retro>
"""

CAUSES = ("task-too-big", "wrong-seam", "vague-brief", "design-needed-first", "patch-shaped-fix", "worker-model",
          "reviewer-narrow", "none")
VERDICTS, CONFIDENCE = ("SHIP", "FIX", "REDESIGN"), ("low", "medium", "high")
RETRO_TRIES = 3  # failed retro rounds (no tail, a bad tail) before the coordinator is told instead
RETRO_REDESIGN = "retro: REDESIGN"
RETRO_CAP = f"retro: FIX asked {FABLE_CHANGES} times and it still does not converge"


def retro_due(t):
    """Review is on, not converging, and this task has not had its one retro (failed rounds don't use it up)."""
    sets = t.get("set") or {}
    return not t.get("retro") and sets.get("review") != "off" and sets.get("review_rounds") != 0 and review_nonconverging(t)


def fixing(t):
    """A worktree task after a retro FIX, before the FIX cap: its fix is checked by a Fable round alone."""
    r = t.get("retro") or {}
    return (t.get("mode") == "worktree" and r.get("verdict") == "FIX" and r.get("confidence") != "low" and not t.get("retro_capped")
            and r.get("acted") is not False)


def gate_rounds(t):
    """1 when the retro decides this head's review (its SHIP on it, or a FIX being checked by Fable), else None."""
    r = t.get("retro") or {}
    return 1 if fixing(t) or (r.get("verdict") == "SHIP" and r.get("confidence") != "low" and r.get("acted") is not False
                              and r.get("head") == t.get("head")) else None


def blocks_continue(t):
    """Why recap --decide continue is refused after a retro, or None."""
    r = t.get("retro") or {}
    if t.get("retro_capped"):
        return RETRO_CAP
    return RETRO_REDESIGN if r.get("verdict") == "REDESIGN" and r.get("confidence") != "low" else None


def cap(s, t):
    """The retro's FIX plus a Fable round asking for changes again is the FIX cap (FABLE_CHANGES): a recap
    without continue, opened before anyone tells the worker to fix again. True when it opened one."""
    r = t.get("retro") or {}
    if r.get("verdict") != "FIX" or r.get("confidence") == "low" or t.get("retro_capped") or r.get("acted") is False:
        return False
    fable, worktree = fable_runs(t), t.get("mode") == "worktree"  # a folder task: its own next round checks the fix
    asked = sum(1 for x in t.get("reviews") or [] if x.get("verdict") == "changes" and (x.get("at") or 0) > r["at"]
                and (x.get("by") in fable if worktree else not x.get("retro")))
    if 1 + asked < FABLE_CHANGES:
        return False
    t["retro_capped"] = now()
    if t.get("recap"):  # another stop is open: it keeps its reasons, and gains this one
        t["recap"]["reasons"].append(RETRO_CAP)
        s.dirty = True
        return True
    return open_recap(s, t, [RETRO_CAP])


def fix_words(t):
    r = t["retro"]
    return ("This round checks the one bounded fix after the retro's FIX verdict, as a delta: does the head close these "
            f"majors for their whole class? {json.dumps(r.get('majors') or [])}. The worker was asked: {r.get('worker_change')}")


def parse_tail(screen):
    """(record, None) from the last <retro>{...}</retro> on a screen, or (None, why). A terminal wraps long lines,
    at a space or inside a word: the lines are joined with a space, else with nothing, whichever gives a valid record."""
    i, j = (screen or "").rfind("<retro>"), (screen or "").rfind("</retro>")
    if i < 0 or j < i:
        return None, "no <retro> tail"
    lines, why = [x.strip().lstrip("⏺│ ").rstrip("│ ") for x in screen[i + 7:j].splitlines()], None
    for sep in (" ", ""):
        try:
            rec = json.loads(sep.join(lines))
        except ValueError:
            why = why or "the tail is not one JSON object"
            continue
        why = check_tail(rec)
        if not why:
            return rec, None
    return None, why


def check_tail(rec):
    if not isinstance(rec, dict):
        return "the tail is not one JSON object"
    if rec.get("verdict") not in VERDICTS:
        return f"verdict {rec.get('verdict')!r} is not SHIP, FIX or REDESIGN"
    if rec.get("cause") not in CAUSES or rec.get("second_cause") not in (*CAUSES, None):
        return f"cause {rec.get('cause')!r} / {rec.get('second_cause')!r} is outside the set"
    if rec.get("confidence") not in CONFIDENCE:
        return f"confidence {rec.get('confidence')!r} is not low, medium or high"
    if not isinstance(rec.get("majors", []), list) or not all(isinstance(m, dict) for m in rec.get("majors") or []):
        return "majors is not a list of findings"
    if rec["verdict"] == "SHIP" and rec.get("majors"):
        return "a SHIP verdict names majors"
    if rec["verdict"] == "FIX" and (not rec.get("majors") or not all(m.get("file") and m.get("what") for m in rec["majors"])):
        return "a FIX verdict needs majors, each with its file and what is wrong"
    return None


def rounds_table(t):
    rows, last = [], None
    for r in rounds_since(t, all_rounds=True):
        gap = f"{round(((r.get('at') or 0) - last) / 60000)}" if last else "-"
        last = r.get("at") or last
        note = " ".join(str(r.get("report") or r.get("text") or "").split())[:300]
        rows.append(f"  {r.get('round')} | {str(r.get('head') or '')[:9]} | {gap} | {r.get('verdict') or r.get('result')} | "
                    f"{r.get('majors')} | {note}")
    return "\n".join(rows) or "  (none)"


def who(v):
    return " ".join(str(x) for x in (v.get("agent"), v.get("model")) if x) if isinstance(v, dict) else str(v or "unknown")


def retro_prompt(s, t, base_sha, head):
    from .launch import task_text
    from .settings import settings_for, task_settings
    rows = {x["label"]: x["value"] for x in task_settings(s, t)}
    revs = [r for r in rounds_since(t, all_rounds=True) if r.get("by") not in fable_runs(t)]
    with_majors = 0
    for r in reversed(revs):
        if r.get("verdict") != "changes" or not r.get("majors"):
            break
        with_majors += 1
    recaps = "; ".join(f"{d.get('decision')} ({'; '.join(d.get('reasons') or [])}: {d.get('why')})" for d in t.get("recaps") or [])
    values = {"task_ref": s.ref(t), "task_title": t.get("title") or "", "project": s.loc.get("folder", "").rstrip("/").split("/")[-1],
              "rounds_used": len(revs), "rounds_with_majors": with_majors, "base_sha": base_sha[:12], "head_sha": (head or "")[:12],
              "worktree": t.get("worktree") or s.loc.get("folder", ""), "changed_lines": change_lines(t) if t.get("worktree") else "unknown",
              "estimate_lines": size_estimate(t) or "none", "tokens_used": token_sum(s, t) or "unknown",
              "worker": who(rows.get("Agent")), "reviewer": who(rows.get("Reviewer")), "rounds_table": rounds_table(t),
              "recaps": recaps or "none", "checks_cmd": (settings_for(s)["tasks"].get("check") or "").strip() or "the repo's own tests",
              "small_lines": SMALL_LINES}
    return fill(values, task_text(t))


def fill(values, brief):
    """RETRO_MD with every {slot} filled (JSON braces in its tail left alone), then the brief."""
    text = re.sub(r"\{([a-z_]+)\}", lambda m: str(values[m.group(1)]) if m.group(1) in values else m.group(0), RETRO_MD)
    return text.strip() + f"\n\nThe brief of {values['task_ref']}:\n" + brief


def start(s, t):
    """Starts the retro session: Fable, fresh, read-only (the reviewer's allow list), as Fable's review round starts."""
    from .base import git
    from .commands import workers
    from .launch import launch, review_reads
    from .settings import argv_for, settings_for
    wt = t.get("worktree") or s.loc["folder"]
    head = git(wt, "rev-parse", "HEAD")[1]  # the commit on disk: the one it reads
    if not head:
        t["retro_failures"] = (t.get("retro_failures") or []) + [{"at": now(), "head": None, "why": "no commit to read"}]
        s.dirty = True
        return
    base_sha = git(wt, "merge-base", t.get("base") or "main", head)[1] if t.get("worktree") else t.get("folder_start") or ""
    check = (settings_for(s)["tasks"].get("check") or "").strip()
    live = workers(s, t)
    # dontAsk: only the reviewer's allow list runs; edits and every other command are refused, nothing prompts.
    a = launch(s, t, argv_for({**FABLE, "perm": "dontAsk"}), retro_prompt(s, t, base_sha or (t.get("start") or ""), head),
               parent=live[-1]["address"] if live else None, role="reviewer", reads=review_reads(check))
    t["retro_run"] = {"address": a["address"], "head": head, "at": now(), "recap_at": (t.get("recap") or {}).get("at")}
    t["progress"] = {"text": "Herdr's retro round is on it", "at": now(), "set": now(), "by": "herdr"}
    s.dirty = True


def finished_screen(t, rn):
    """The retro session's screen once its turn has ended (None while it works or starts)."""
    from .agents import STARTING_MS, status_word
    from .commands import screen_of
    k = hc_json("get", rn["address"]) or {}
    if k.get("state") == "live" and (status_word(k) not in ("idle", "done")
                                     or (k.get("turn") == 0 and now() - rn["at"] < STARTING_MS)):
        return None
    return screen_of(rn["address"], 400)


def close_recap(s, t, decision, rec, small):
    """The retro decided in the coordinator's place (SHIP, FIX): the open recap closes as a continue."""
    rest = [w for w in (t.get("recap") or {}).get("reasons") or [] if w != NON_CONVERGENCE]
    if rest:  # a size, time or CI stop as well: that recap stays for the coordinator, without the review reason
        t["recap"]["reasons"] = rest
        if small:
            t["retro_fix_pending"] = True  # its continue carries the FIX's bound, and last_findings the majors
        return False
    cur = change_lines(t) if t.get("worktree") else None
    t.setdefault("recaps", []).append({"decision": "continue", "why": f"retro: {decision}", "by": "retro", "at": now(),
                                       "reasons": [NON_CONVERGENCE], "tokens": token_sum(s, t),
                                       **({"small": True, "max_rounds": SMALL_ROUNDS,
                                           "max_lines": cur + SMALL_LINES if cur is not None else None} if small else {})})
    t.pop("recap", None)
    if (t.get("attention") or "").startswith("Recap required"):
        t.pop("attention", None)
        t.pop("attention_at", None)
    return True


def act(s, t, rn, rec):
    """Stores the retro and acts on its verdict; returns the line told to the worker (or None)."""
    from .commands import tell
    from .launch import own_path
    t["retro"] = {**rec, "head": rn["head"], "by": rn["address"], "at": now()}
    t.pop("progress", None)
    ref, ready = s.ref(t), f"`{own_path()} ready {s.ref(t)} --folder {s.loc['folder']}`"
    if rec["confidence"] == "low":  # stored, not acted on: the recap stays open for the coordinator
        return None
    if rec["verdict"] == "SHIP":
        t.setdefault("reviews", []).append({"round": len(t.get("reviews") or []) + 1, "head": rn["head"], "by": rn["address"],
                                            "started_by": "herdr-task", "result": "pass", "verdict": "pass", "must_fix": 0,
                                            "majors": 0, "report": "retro: SHIP", "cli": "claude", "model": FABLE["model"],
                                            "retro": True, "at": now()})
        close_recap(s, t, "SHIP", rec, False)
        msg = f"Herdr's retro round passed {ref} on {rn['head'][:7]}: no majors. Run {ready} again."
    elif rec["verdict"] == "FIX":
        found = "\n".join(f"- {m.get('file')}:{m.get('line')}: {m.get('what')} (repro: {m.get('repro')})" for m in rec["majors"])
        t.setdefault("reviews", []).append({"round": len(t.get("reviews") or []) + 1, "head": rn["head"], "by": rn["address"],
                                            "started_by": "herdr-task", "result": "changes", "verdict": "changes",
                                            "must_fix": len(rec["majors"]), "majors": len(rec["majors"]),
                                            "report": f"retro: FIX\n{found}\nYour one change: {rec.get('worker_change')}",
                                            "cli": "claude", "model": FABLE["model"], "retro": True, "at": now()})
        if any(d.get("small") for d in t.get("recaps") or []):  # its bounded continue is spent: FIX is the cap
            t["retro_capped"] = now()
            if t.get("recap"):
                t["recap"]["reasons"].append(RETRO_CAP)
            s.dirty = True
            return None
        closed = close_recap(s, t, "FIX", rec, True)
        folder = t.get("mode") != "worktree"
        if folder:  # no worktree for a Fable round: the worker's own next round checks the fix
            t["review_rounds_added"] = int(t.get("review_rounds_added") or 0) + 1
        if not closed:  # another stop stays open: the coordinator's continue tells the worker, with these majors
            s.dirty = True
            return None
        msg = (f"Herdr's retro round on {ref} found majors to fix:\n{found}\nYour one change: {rec.get('worker_change')}\n"
               f"Make one bounded fix (at most {SMALL_LINES} more lines), commit, then " + (
                   "have it reviewed and record the round (one more round is added), then run " if folder else "run ")
               + f"{ready} again" + ("." if folder else ": a Fable round checks it."))
    else:
        rc = t.get("recap")
        if rc and RETRO_REDESIGN not in rc["reasons"]:
            rc["reasons"].append(RETRO_REDESIGN)
        return None
    s.dirty = True
    try:
        return tell(s, t, msg, None)[0]
    except Fail:
        return None


def child_ask(s, t):
    """B1 step 7: a split child of a retro'd task draws a retro too: one owner ask, kind decision, instead of a retro."""
    from .ask import cmd_ask
    parent = t["retro_parent"]["task"]
    import contextlib
    import io
    with contextlib.redirect_stdout(io.StringIO()):  # the tick's own --json stays one document
        rec = cmd_ask(SimpleNamespace(
            show=None, question=f"{s.ref(t)} failed review twice in a row, also after {parent} was split after its retro. "
                                "Drop it, or tell me how to go on?",
            option=["Drop it", "Tell me how to go on"], recommend="Drop it", task=s.ref(t), kind="decision",
            origin={"folder": s.loc["folder"]}, checklist=None, to=None, json=True))
    t["retro_ask"] = rec["id"]
    s.dirty = True


def retro_pass(s, t):
    """The tick's retro step for a running or ready task with review on, any mode (worktree or folder)."""
    rn = t.get("retro_run")
    if rn:
        screen = finished_screen(t, rn)
        if screen is None:
            return
        from .commands import end_reviewer
        from .base import git
        rec, why = parse_tail(screen)
        where = t.get("worktree") or s.loc["folder"]
        if git(where, "rev-parse", "HEAD")[1] != rn["head"] or git(where, "status", "--porcelain", "--untracked-files=no")[1]:
            rec, why = None, f"the checkout moved on from {rn['head'][:7]} or has uncommitted changes"
        t.pop("retro_run")
        s.dirty = True
        end_reviewer(rn["address"])
        if rec and (t.get("recap") or {}).get("at") != rn.get("recap_at"):  # the coordinator decided meanwhile
            t["retro"] = {**rec, "head": rn["head"], "by": rn["address"], "at": now(), "acted": False}
            return
        if rec:
            act(s, t, rn, rec)
            return
        t["retro_failures"] = (t.get("retro_failures") or []) + [{"at": now(), "head": rn["head"], "why": why}]
    if cap(s, t) or t.get("retro"):
        return
    from .settings import review_limits
    if not retro_due(t) or not review_limits(s, t)[0]:
        return
    if not t.get("recap"):
        open_recap(s, t, [NON_CONVERGENCE])  # folder tasks too: no next round, no merge, no fixing
    if t.get("retro_parent"):
        if not t.get("retro_ask"):
            child_ask(s, t)
        return
    fails = t.get("retro_failures") or []
    if len(fails) >= RETRO_TRIES:
        why = f"Retro round failed {len(fails)} times ({fails[-1]['why']}): the coordinator decides the recap"
        if t.get("attention") != why:
            t["attention"], t["attention_at"] = why, now()
            s.dirty = True
        return
    start(s, t)
