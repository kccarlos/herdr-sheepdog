"""Tick: one pass over the tasks (start queued ones, finish merged ones, retry cut turns, hand a
task whose CLI can't go on to its backup)."""

import argparse
import contextlib
import fcntl
import io
import json
import os
import secrets
import subprocess
import time

from .base import Fail, git, guard, hc_json, label_of, now, PR_EVERY, PROJECT_LABEL, ROLE_LABEL, run, SHELLS, STORE, stop_note
from .settings import machine_name, merge_hold, remove_build_dir, worker_argv
from .agents import (
    agent_log, agent_running, back_at_work, BG_ENDED_MSG, BG_NUDGE_MS, busy_again, detail, ended_jobs, fit, last_question, live_agents,
    mid_turn_agents, open_question, own_work, stray_since, ready_turns, REVIEWER_FAILED, status_word, task_status, WAITING_BG,
)
from .provider import NUDGE, provider_cut, provider_error, PROVIDER_WAITS
from .sessions import find_lost_logs, refresh_sessions, session_of
from .github import (already_merged, BEHIND, CHECKS_NEVER, CHECKS_RUNNING, checks_missing, checks_since, ci_awaited, ci_cap_hold, CI_CAP_WAIT,
                     ci_dispatch, ci_runs_on, drop_branch, expected_checks, finish, github, main_red_hold, merged_here, needs_approval, NO_CI_RUN,
                     note_checks, pr_head_problem, pr_merged, pr_open, pr_pending, pr_problem, pr_repo, pr_view,
                     pr_wait, CI_STOPPED, queue_entry, queue_merge, task_problem, task_github, tidy_done, tidy_due, update_main, watch_main,
                     UNKNOWN_CHECKS_MS)
from .launch import parent_for, sweep_sandboxes
from .store import locate, read_list, Store, store_files
from .queues import cap_for, machine_hold, refresh_ext, running_count, set_hold, urgent, waiting_ext, waiting_for
from .archive import archive_pass
from .simulators import simulator_pass
from .devices import device_pass
from .output import progress_clock, say
from .commands import (advance_merge_only, back_to_work, begin, CHECKS_RUNNING as MERGE_CHECKS_RUNNING, gate_reads, IN_QUEUE,
                       merge_now, MERGE_FAILED, start_nudge, START_WINDOW_MS, WAITS_APPROVAL)
from .news import news_pass, send_or_hold, typing
from .takeover import cant_go_on, takeover
from .ghnews import main_pass, pr_deliver, pr_news
from .scheduler import SCHEDULER
from .revive import revive_pass
from .watchdog import watchdog_pass
from .checks import checks_pass
from .budget import budget_pass, record_ci


PR_WAIT_EVERY = 20000  # how often a pull request whose checks still run is read again (ms)


def at_work(s, t):
    """Its agent is mid-turn (working, or asking): a task is never marked merged under it (SHEP-23);
    a later tick looks again."""
    return bool(mid_turn_agents(live_agents(s.key(t)), detail))


def holds_open(s, t):
    """A merge GitHub reports never closes the task under it (#802, #844): reopened after its pull
    request merged (until its agent says ready), or Working with its agent still there (its brief
    may go on after the merge: it says ready, or ends, and is done then)."""
    return bool(t.get("kept_open")) or (t["state"] == "running" and any(agent_running(k) for k in live_agents(s.key(t))))


QUEUE_GRACE = 2 * 60000  # a pull request just queued may take a moment to show in GitHub's queue


QUEUE_REMOVED = "GitHub's merge queue removed it"


def queue_pass(s, t, v, report):
    """A pull request Herdr put in GitHub's merge queue that is still open: still in the queue (or set
    to join it), it stays Merging; the queue removed it (a check failed there, a conflict), it
    needs attention, with the queue's reason (#742, #802). Removed only because its checks there
    timed out (busy runners: #1041), it is queued again once by itself, at the same commit."""
    if v.get("state") != "OPEN":
        t.pop("merge_queued", None)
        return
    if now() - t["merge_queued"] < QUEUE_GRACE:
        return
    q = queue_entry(t)
    if q and (q["in"] or q["auto"]):  # FAC-98: its base has a merge queue (commands.uses_queue())
        s.data.setdefault("merge_queue", {})[t.get("base") or "main"] = now()
        s.dirty = True
    if not q or q["in"] or q["auto"] or q.get("state") == "MERGED":
        return
    t.pop("merge_queued")
    why = (q.get("reason") or "").strip().rstrip(".")
    head = v.get("headRefOid")
    if "timed_out" in why.lower().replace(" ", "_") and head and t.get("requeued") != head and not pr_problem(v):
        ok, err = queue_merge(t, head)
        if ok:
            t["requeued"], t["merge_queued"] = head, now()
            report.setdefault("requeued", []).append(s.ref(t))
            return
        why += f"; queuing it again failed: {err}"
    t["attention"], t["attention_at"] = QUEUE_REMOVED + (f": {why}." if why else "."), now()
    report["attention"].append(t["id"])


def back_in_queue(s, t, report):
    """#1041: a pull request the merge queue removed, queued again by hand (gh pr merge --auto, GitHub's
    button): Merging again, its "removed" attention gone, once GitHub shows it in the queue."""
    if t.get("merge_queued") or not (t.get("attention") or "").startswith(QUEUE_REMOVED):
        return
    q = queue_entry(t)
    if q and (q["in"] or q["auto"]):
        t.pop("attention", None)
        t.pop("attention_at", None)
        t["merge_queued"] = now()
        s.dirty = True
        report.setdefault("requeued", []).append(s.ref(t))


def facts(s, t, report):
    """A worktree task whose own work merged anywhere is done (once its agent is between turns, and
    a Working one once its agent is gone or says ready); a pull request's problems need the person."""
    if t.get("pr"):
        pr = t["pr"]
        if now() - pr.get("checked", 0) < PR_EVERY:
            return
        pr["checked"] = now()
        s.dirty = True
        repo = pr_repo(t, s, github)
        v = pr_view(t)
        if not v:
            return
        note_checks(s, t, v)
        if v.get("state") == "MERGED":
            if at_work(s, t) or holds_open(s, t):
                return
            t["merged"] = True
            watch_main(s, t, (v.get("mergeCommit") or {}).get("oid"))
            if t.get("merge_queued"):
                # Herdr queued it: its base here catches up and its branch on GitHub goes, as a merge
                # Herdr made itself would (merge_now()).
                update_main(s, t)
                drop_branch(s, t)
            finish(s, t)
            report["done"].append(t["id"])
            return
        record_ci(s, t, v, now())
        problem = task_problem(t, v, repo)
        if t.get("merge_queued"):
            queue_pass(s, t, v, report)
        else:
            back_in_queue(s, t, report)
        if needs_approval(v):
            pr["needs_approval"] = True
        else:
            pr.pop("needs_approval", None)
        since, expect = checks_since(s, t, v), expected_checks(s, t, v.get("headRefOid"))
        # #1044, #1018: no checks yet (at its newest commit) while some will come is never "all passed".
        missing = checks_missing(v, since, expect)
        t["_pr_fresh"] = not problem and not pr_pending(v) and not needs_approval(v) and not missing
        # Ready only once its checks are done and GitHub knows it can merge (SHEP-175): Working till then.
        ci = False
        if t["state"] == "ready":
            ci = v.get("state") == "OPEN" and ci_awaited(s, t, v)
            # FAC-105: timed from its newest head Herdr made (FAC-98's update-branch, SDF-39's merge-only move), else from ready.
            since_ci = max(t.get("ready_at") or 0, t.get("head_moved_at") or 0, (pr.get("synced") or {}).get("at") or 0)
            wait = pr_wait(v, since_ci if ci else since, expect, ci)
            if ci and not wait and not pr.get("no_ci"):
                # FAC-105: its base's workflows run on pull requests, yet none ran on it: Ready for the person, its
                # pending merge set aside (kept in no_ci), the reason told; an Actions run for its head not shown yet waits.
                if ci_runs_on(t, v) and now() - since_ci < UNKNOWN_CHECKS_MS:
                    wait = CHECKS_RUNNING
                elif started := ci_dispatch(s, t, v):  # FAC-115: none ran, so Herdr started its workflow once for this head
                    wait = started
                else:
                    pr["no_ci"], s.dirty = t.pop("auto_merge", None) or True, True
            elif not ci and (held := pr.pop("no_ci", None)):
                s.dirty = True
                if held is not True and not t.get("accepted_by"):
                    t.setdefault("auto_merge", held)  # its checks showed after all: merges by itself as it would have
            if wait != pr.get("wait"):
                if wait:
                    pr["wait"] = wait
                else:
                    pr.pop("wait", None)
            if wait:
                pr["checked"] = now() - PR_EVERY + PR_WAIT_EVERY
        elif pr.get("wait") in (CHECKS_RUNNING, STARTED_CI, MERGE_CHECKS_RUNNING) and not pr_pending(v) and not missing:
            pr.pop("wait", None)
            s.dirty = True
        try:
            pr_news(s, t, v, report)
        except Exception as e:  # never stops the tick
            report.setdefault("github_error", []).append(f"{s.ref(t)}: {e}")
        # Kept up to date while it is back at work too (a fix pushed: the problem goes away), so its
        # agent going quiet with the problem still there reads Needs you (on_it()).
        if (t["state"] in ("ready", "running") and problem != t.get("attention") and not t.get("recap")
                and not (t.get("attention") or "").startswith(REVIEWER_FAILED)):  # a recap or gate failure first
            if problem:
                t["attention"], t["attention_at"] = problem, now()
                t.pop("merge_queued", None)
                report["attention"].append(t["id"])
            elif t.get("attention", "").startswith(("Checks failing", CI_STOPPED, "It conflicts", "The pull request was closed", MERGE_FAILED, BEHIND, "Couldn't reach pull request")) \
                    or (t.get("attention") == CHECKS_NEVER and missing != "never"):
                t.pop("attention")
        if t["state"] == "ready" and t.get("auto_merge") and missing == "never" and not t.get("attention") and not (ci and pr.get("wait")):
            # Checks that should have started never did: it doesn't merge by itself (the person's Accept still can).
            t["attention"], t["attention_at"] = CHECKS_NEVER, now()
            report["attention"].append(t["id"])
    elif merged_here(s, t) and not at_work(s, t) and not t.get("kept_open"):
        # #989: reopened after its work merged (kept_open, until its agent says ready): its fix goes on.
        t["merged"] = True
        finish(s, t)
        report["done"].append(t["id"])
    elif t.get("own") and not at_work(s, t) and not holds_open(s, t) and now() - t.get("gh_checked", 0) >= PR_EVERY:
        # SHEP-59: its pull request squash-merged without the task knowing (opened by its agent), or a
        # Ready one whose work is already in its base: done, as ready would.
        t["gh_checked"] = now()
        s.dirty = True
        gone = already_merged(s, t) if t["state"] == "ready" else squash_merged(s, t)
        if gone is not None:
            t["merged"] = True
            if gone:
                watch_main(s, t, (gone.get("mergeCommit") or {}).get("oid"))
            finish(s, t)
            report["done"].append(t["id"])


def squash_merged(s, t):
    """A Working task's branch whose pull request (opened by its agent) merged at its tip: that
    pull request, recorded on the task, or None."""
    repo = task_github(github, s, t)
    found = pr_merged(t, repo) if repo else None
    if not found or found.get("headRefOid") != (t.get("own") or t.get("head")):
        return None
    t["pr"] = {"number": found["number"], "url": found["url"], "repo": repo}
    return {**t["pr"], "mergeCommit": found.get("mergeCommit")}


def back_pass(s, t, report):
    """A Ready task whose agent took up work again (a message from anyone in its chat) is Working
    again until its next ready (busy_again()); its Assistant hears it (news: working-again)."""
    live = live_agents(s.key(t))
    # Cheap while nothing works: hc ls alone; its records in full (roles) only then.
    if any(status_word(k) == "working" for k in live):
        mid = mid_turn_agents(live, detail)
        if back_at_work(t, mid):
            back_to_work(s, t, "its agent is at work again", auto=True)
            report.setdefault("working_again", []).append(s.ref(t))
            return
        # FAC-107: a turn that is only an idle compaction, kept for the window's busyAgain()
        stray = {**(t.get("stray_since") or {}), **stray_since(t, mid)}
        if stray and stray != t.get("stray_since"):
            t["stray_since"] = stray
            s.dirty = True
    # #1000: a whole turn between two passes (GitHub news woke it, it listed options and asked "pick
    # one" in its own terminal): never seen working, so the task stayed Ready and nobody heard. A
    # turn that asked or changed its branch takes it back to work: Needs you once it stays quiet
    # (settle_back() keeps it there). One that left nothing behind only moves its turns on.
    seen = t.get("ready_turns")
    if not isinstance(seen, dict) or t.get("merge_requested"):
        return
    turned = [k for k in live if status_word(k) in ("idle", "done") and k.get("address") in seen
              and label_of(k.get("labels"), "role") != "reviewer" and (k.get("turn") or 0) > seen[k["address"]]]
    if not turned:
        return
    wt = t.get("worktree") if t.get("mode") == "worktree" else None
    dirty = bool(wt and os.path.isdir(wt) and git(wt, "status", "--porcelain", "--untracked-files=no")[1])
    asked = any(last_question(t, k["address"]) for k in turned)
    moved = dirty or bool(wt and t.get("head") and os.path.isdir(wt)
                          and git(wt, "rev-parse", "HEAD")[1] != t["head"]
                          and (asked or not advance_merge_only(s, t)))
    if moved or asked:
        back_to_work(s, t, "its agent asked something" if not moved else "its agent changed its work", auto=True)
        report.setdefault("working_again", []).append(s.ref(t))
        return
    t["ready_turns"] = {**seen, **{k["address"]: k.get("turn") or 0 for k in turned}}
    s.dirty = True


def settle_back(s, t, report):
    """A Ready task its agent's own turn took back to work (back_pass(): a message in its chat,
    a background job's notice waking it) is Ready again once that turn ended with nothing new: its
    branch where it said ready, nothing uncommitted, no question asked, no work of its own left
    running (SHEP-133). Else it stays Working, and Needs you once its agent stays quiet.
    Its pending auto-merge stays meanwhile and goes in the same tick once Ready (FAC-96); a new
    commit, uncommitted changes or a question drop it until its next ready."""
    b = t.get("back") or {}
    if t["state"] != "running" or not b.get("auto") or t.get("attention") or t.get("retry"):
        return False
    wt = t.get("worktree") if t.get("mode") == "worktree" else None
    if not wt or not t.get("head") or not os.path.isdir(wt):
        return False
    live = [k for k in map(detail, live_agents(s.key(t))) if agent_running(k)]
    if not live or any(status_word(k) not in ("idle", "done") for k in live):
        return False
    ok, head, _ = git(wt, "rev-parse", "HEAD")
    if not ok:
        return False
    dirty = git(wt, "status", "--porcelain", "--untracked-files=no")[1]
    if dirty or any(last_question(t, k.get("address")) for k in live) or (head != t["head"] and not advance_merge_only(s, t)):
        # FAC-96: new work since ready: its merge waits for its next ready.
        if t.pop("auto_merge", None) is not None:
            s.dirty = True
        return False
    # Its hard signs only: Herdr's own line stands in for its status line, and a job already
    # listed while it sat Ready (an expired Monitor whose notice woke it) is no new work (FAC-96).
    if own_work(t, live, ready=True, since=b.get("at") or 0):
        return False
    t["state"], t["ready_turns"] = "ready", ready_turns(live)
    if b.get("progress"):
        t["progress"] = b["progress"]
    else:
        t.pop("progress", None)
    t.pop("back", None)
    # Jobs listed before the wake-up stay no work of its own while it's Ready (own_work()), and its
    # pull request is read again in this tick, so a pending merge can go now (FAC-96).
    t["settled_at"] = b.get("at") or 0
    if t.get("pr"):
        t["pr"]["checked"] = 0
    s.dirty = True
    report.setdefault("ready_again", []).append(s.ref(t))
    return True


def due_pass(s, report):
    """Scheduled tasks whose time has come (SHEP-38), a time passed while the machine was off too, join
    the queue in the order they were due: the queue below starts them (after their --after tasks,
    when there's room), each once."""
    for t in s.data["tasks"]:
        if t["state"] == "scheduled" and (t.get("scheduled_for") or 0) <= now():
            t["state"], t["queued_at"] = "queued", t.get("scheduled_for") or now()
            t.pop("scheduled_for", None)
            s.dirty = True
            report.setdefault("due", []).append(s.ref(t))


def tick_store(s, report):
    """One folder's turn: Ready tasks back at work are Working again, merged tasks become done,
    auto-merges go when they can, and queued tasks start while there's room."""
    for t in s.data["tasks"]:
        if t["state"] == "ready":
            back_pass(s, t, report)
        elif t["state"] == "running" and (t.get("back") or {}).get("auto"):
            settle_back(s, t, report)
        if t["state"] == "running" and t.get("agents"):
            try:
                start_nudge(s, t)  # FAC-100: an agy worker idle at turn 0 gets one nudge
            except Exception as e:  # never stops the tick
                report.setdefault("start_nudge_error", []).append(f"{s.ref(t)}: {e}")
        if t.get("worktree") and t["state"] in ("running", "ready"):
            if t["state"] == "ready" and t.get("push_held") and not t["push_held"].get("gate"):
                repo = task_github(github, s, t)
                if repo:
                    if hold := ci_cap_hold(s, t, repo):
                        if t["push_held"].get("reason") != hold:
                            t["push_held"]["reason"] = hold
                            s.dirty = True
                    else:
                        try:
                            pr = pr_open(s, t, repo, t.get("summary"))
                            if why := pr_head_problem(s, t, repo, t.get("head")):
                                raise Fail(why[0].upper() + why[1:] + ".")
                            t.pop("push_held", None)
                            t["head_moved_at"] = now()  # CI grace window starts when the held push actually reaches GitHub
                            pr["head_at"] = now()
                            pr["checked"] = 0
                            pr["wait"] = CHECKS_RUNNING
                            s.dirty = True
                        except Fail as e:
                            t.pop("push_held", None)
                            t["attention"], t["attention_at"] = str(e), now()
                            s.dirty = True
                            report["attention"].append(t["id"])
                else:
                    t.pop("push_held", None)
                    s.dirty = True
            facts(s, t, report)
            try:
                budget_pass(s, t)  # FAC-54: not converging, or far over its estimate: recap first
            except Exception as e:  # never stops the tick
                report.setdefault("budget_error", []).append(f"{s.ref(t)}: {e}")
            if t["state"] == "ready" and (t.get("pr") or {}).get("news"):
                try:
                    pr_deliver(s, t, report)
                except Exception as e:  # never stops the tick
                    report.setdefault("github_error", []).append(f"{s.ref(t)}: {e}")
            if t.get("auto_merge") and not t.get("accepted_by") and merge_hold(s, t):
                t.pop("auto_merge", None)
                s.dirty = True
            if t["state"] == "ready" and t.get("auto_merge") and t.get("pr") and not t.get("push_held") and not t.get("attention") and t.get("_pr_fresh") \
                    and not busy_again(t, [detail(k) for k in live_agents(s.key(t))]):  # FAC-107: not under a turn just begun
                if err := merge_now(s, t):
                    if (not t.get("attention") and t["state"] == "ready"
                            and err not in (MERGE_CHECKS_RUNNING, WAITS_APPROVAL, IN_QUEUE, NO_CI_RUN, t.get("accept_wait"))
                            and not str(err).startswith(CI_CAP_WAIT)
                            and err != main_red_hold(s, t.get("base") or "main")):
                        t["attention"], t["attention_at"] = err, now()
                        s.dirty = True
                    if t.get("attention"):
                        report.setdefault("attention", []).append(t["id"])
                else:
                    report["done"].append(t["id"])
            t.pop("_pr_fresh", None)
    for t in s.data["tasks"]:
        if t.get("worktree") or t.get("check_run"):
            try:
                checks_pass(s, t)
            except Exception as e:  # never stops the tick
                report.setdefault("checks_error", []).append(f"{s.ref(t)}: {e}")
        if (t.get("review_runs") or t.get("review_quota_wait") or (t.get("push_held") or {}).get("gate")) and t["state"] in ("running", "ready"):  # FAC-101: a quota wait too
            had = t.get("attention")
            try:
                gate_reads(s, t)  # a sandboxed reviewer's verdict, or a dead reviewer's fallback
            except Exception as e:  # never stops the tick
                report.setdefault("review_error", []).append(f"{s.ref(t)}: {e}")
            if t.get("attention") and t.get("attention") != had:
                report.setdefault("attention", []).append(t["id"])
    due_pass(s, report)
    # Urgent ones first (FAC-103), then oldest first.
    queued = sorted((t for t in s.data["tasks"] if t["state"] == "queued"), key=lambda t: (not urgent(t), t.get("queued_at") or 0))
    n = running_count(s) if queued else 0
    budget = [3]  # other machines asked this pass, at most
    for t in queued:
        waits = bool(waiting_for(s, t))
        if not waits:
            refresh_ext(s, t, budget=budget)
            waits = bool(waiting_ext(t))
        if waits or n >= cap_for(s, t):
            if t.pop("held", None):  # waits for something else now
                s.dirty = True
            continue
        # Its machine too busy (HERD-40): it, and every one after it, waits with why.
        held = machine_hold()
        if (t.get("held") or None) != held:
            t["held"] = held
            if not held:
                t.pop("held")
            s.dirty = True
        if held:
            continue
        q = t.get("queue") or {}
        try:
            a = begin(s, t, q.get("argv") or worker_argv(s, t), parent=q.get("parent"))
        except Fail as e:
            why = f"Couldn't start: {e}"
            if t.get("attention") != why:
                t["attention"] = why
                t.pop("attention_at", None)
                s.dirty = True
                report["attention"].append(t["id"])
            continue
        n += 1
        report["started"].append({"folder": s.loc["folder"], "id": t["id"], "ref": s.ref(t), "terminal": a["address"]})
    if s.data.get("main_watch"):
        try:
            main_pass(s, report)
        except Exception as e:  # never stops the tick
            report["main_error"] = str(e)


def starts_now(t):
    """A running task whose agy worker started within START_WINDOW_MS and wasn't nudged yet (start_nudge)."""
    a = (t.get("agents") or [{}])[-1]
    return (t.get("state") == "running" and a.get("agent") == "agy" and not a.get("start_nudged") and not a.get("ended")
            and now() - (a.get("at") or 0) < START_WINDOW_MS)


def lost_log(t, a):
    """An agent of task t that ended with no log recorded, not looked for yet (find_lost_logs())."""
    gone = a.get("ended") or (t.get("state") == "done" and (t.get("ended_at") or 0) > (t.get("done_at") or 0))
    return bool(gone and not a.get("log") and not a.get("log_looked"))


def lost_logs_pass(report, folder=None):
    """Finds the log of every ended task agent here whose session no pass saw (a short run), so its
    chat reads back after its terminal is gone (SHEP-26). Cheap once each was looked for."""
    for p in store_files(folder):
        d = read_list(p)
        tasks = (d or {}).get("tasks") or []
        if not d or not os.path.isdir(d.get("folder") or "") or not any(lost_log(t, a) for t in tasks for a in t.get("agents") or []):
            continue
        with Store(locate(d["folder"])) as s:
            for t in s.data["tasks"]:
                if any(lost_log(t, a) for a in t.get("agents") or []):
                    find_lost_logs(s, t)
                    report.setdefault("logs_found", []).append(s.ref(t))


# ---- Done tasks end their agents (as Shepherdr) -----------------------------------------------
# A Done task's worker and reviewer terminals end (their shells too) once the final news went
# and its agent is quiet; their sessions stay recorded on the task, so `resume` brings one back
# (which makes it running again). Ready tasks keep theirs. Only terminals labelled with that
# task's herdr.task key are touched. "ended_at" says a pass found nothing left of it.

DONE_GRACE_MS = 60000          # the done news and the agent's last words go first
DONE_WORKING_MS = 15 * 60000   # an agent still working this long after Done ends anyway
GUARD_MEMORY = 85              # memory in use (%) past which the guard acts (memory_guard())
GUARD_FILE = os.path.join(STORE, ".memory-guard.json")
GUARD_ALERT_EVERY_MS = 30 * 60000
GUARD_BACK_UNDER = 75          # memory (%) the guard's alert time is forgotten under: dips around GUARD_MEMORY keep it (#1047)


def rescue_question(s, t, k, q, report):
    """Copies an open question `q` from Done task `t`'s worker `k` into `herdr-task ask` before `k`
    closes (FAC-70). True once the ask exists; False if it couldn't be made (logs why and keeps `k`)."""
    from . import ask as ask_mod
    ref, addr = s.ref(t), k["address"]
    if any(a.get("from") == addr and a.get("question") == q["question"] for a in t.get("asks") or []):
        return True
    parent = k.get("parent") or next((a.get("parent") for a in reversed(t.get("agents") or []) if a.get("parent")), None)
    try:
        to = parent_for(parent, s.loc["folder"]) or parent
    except Fail:
        to = parent
    question = q["question"] if ref in q["question"] else fit(f"{ref}: {q['question']}", ask_mod.MSG_MAX)
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            rec = ask_mod.cmd_ask(argparse.Namespace(
                show=None, question=question, option=q["options"], recommend=q["recommend"],
                task=ref, to=to, json=True, origin={"terminal": addr, "folder": s.loc["folder"]}))
    except (Fail, OSError) as e:
        why = str(e)
        line = f"Kept {addr} open (open question: {q['question']}): {why}"
        logs = t.setdefault("log", [])
        if not any((x.get("text") if isinstance(x, dict) else x) == line for x in logs):
            logs.append({"at": now(), "terminal": addr, "question": q["question"], "why": why, "text": line})
            s.dirty = True
            stop_note("kept-open", task=ref, terminal=addr, question=q["question"], why=why)
        report.setdefault("kept_open", []).append({"task": ref, "terminal": addr, "why": why})
        return False
    t.setdefault("asks", []).append({"id": rec["id"], "terminal": rec["terminal"], "from": addr,
                                     "question": q["question"], "at": now()})
    t.setdefault("log", []).append({"at": now(), "terminal": addr, "ask": rec["id"], "question": q["question"],
                                    "text": f"Moved open question from {addr} to ask {rec['id']}: {q['question']}"})
    s.dirty = True
    stop_note("end-done-ask", task=ref, terminal=addr, ask=rec["id"], question=q["question"])
    report.setdefault("asked", []).append({"task": ref, "id": rec["id"], "terminal": rec["terminal"], "from": addr})
    return True


def end_done(s, t, report, pressed=False):
    """Ends what is left of one Done task's agents now, or what may end: a shell at once, an agent
    once it's idle (or working past DONE_WORKING_MS); under memory pressure (pressed) every idle
    one at once. An open question on a worker moves into `herdr-task ask` first, or keeps the worker
    alive when the ask can't be made (FAC-70). Returns the addresses it ended."""
    done_at = t.get("done_at") or 0
    if not pressed and now() - done_at < DONE_GRACE_MS:
        return []
    me = os.environ.get("HC_TERMINAL_ID")
    live = [detail(k) for k in live_agents(s.key(t))]
    live = [k for k in live if k.get("address") != me and label_of(k.get("labels"), "role") != "coordinator"]
    if not live:
        remove_build_dir(s, t)
        t["ended_at"] = now()
        s.dirty = True
        return []
    refresh_sessions(s, t, live)
    ended = []
    for k in live:
        q = open_question(t, k)
        if not q and agent_running(k) and status_word(k) == "working" and now() - done_at < DONE_WORKING_MS:
            continue
        if q and not rescue_question(s, t, k, q, report):
            continue
        if quiet_hc("kill", k).returncode == 0:
            stop_note("end-done", task=s.ref(t), terminal=k["address"], status=status_word(k), pressed=pressed)
            ended.append(k)
    if ended:
        report.setdefault("ended", []).append({"task": s.ref(t), "terminals": [k["address"] for k in ended]})
    return ended


def end_done_pass(report, folder=None, pressed=False):
    """Every Done task here with agents left ends them (end_done()), and once none is left its
    worktree goes (tidy_done(), SHEP-161). Cheap when nothing's left: a task is looked at once after
    each time it became Done, and again only while its worktree is still on disk."""
    ending = lambda t: t.get("state") == "done" and (t.get("ended_at") or 0) <= (t.get("done_at") or 1)
    ended = []
    for p in store_files(folder):
        d = read_list(p)
        tasks = (d or {}).get("tasks") or []
        if not d or not os.path.isdir(d.get("folder") or "") or not any(ending(t) or tidy_due(t) for t in tasks):
            continue
        with Store(locate(d["folder"])) as s:
            for t in s.data["tasks"]:
                if ending(t):
                    ended += end_done(s, t, report, pressed)
                if not ending(t) and tidy_due(t):
                    try:
                        tidy_done(s, t, report)
                    except Exception as e:  # never stops ending the others
                        report.setdefault("tidy_error", []).append(f"{s.ref(t)}: {e}")
    if ended:
        # Ended terminals leave hc's list too (a moment after: hc rm takes finished ones only).
        time.sleep(1)
        for k in ended:
            quiet_hc("rm", k)
    return [k["address"] for k in ended]


def quiet_hc(verb, k):
    """hc kill / rm of a Done task's terminal, asked as its parent: a parent that ends its own
    child has seen the news, so no "exited" note is typed into the Assistant (hc's caller)."""
    guard(["hc"])
    env = dict(os.environ)
    env.pop("HC_TERMINAL_ID", None)
    if k.get("parent"):
        env["HC_TERMINAL_ID"] = k["parent"]
    return subprocess.run(["hc", verb, k["address"]], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)


def memory_guard(report):
    """Past GUARD_MEMORY% memory in use, before oomd ends everything: idle agents of Done tasks
    end first (whatever their grace), then the person is told (an alert from the scheduler's
    terminal, once per 30 min while it stays high). HERD-40's hold keeps new tasks waiting."""
    from .machine import sample
    h = sample()
    try:
        with open(GUARD_FILE) as f:
            g = json.load(f)
    except (OSError, ValueError):
        g = {}
    if h["memory"] <= GUARD_MEMORY:
        # #1047: removed at every dip to 85%, memory hovering at 85-86% alerted each tick (every 15 s); the alert
        # time stays until memory is well under, so one toast per GUARD_ALERT_EVERY_MS while it stays near the limit
        if g and h["memory"] <= GUARD_BACK_UNDER:
            try:
                os.remove(GUARD_FILE)
            except OSError:
                pass
        return
    ended = end_done_pass(report, pressed=True)
    if ended:
        h = sample()
    report["memory_guard"] = {"memory": h["memory"], "ended": ended}
    if ended:
        stop_note("memory-guard", memory=h["memory"], terminals=ended)
    if h["memory"] <= GUARD_MEMORY or now() - (g.get("alerted") or 0) < GUARD_ALERT_EVERY_MS:
        return
    k = hc_json("get", SCHEDULER)
    if not k or k.get("state") != "live":
        return
    agents = sum(1 for x in (hc_json("ls") or {}).get("terminals", []) if "/" not in x.get("address", "/")
                 and x.get("agent") and x.get("agent") not in SHELLS)
    alert = {"id": secrets.token_hex(4), "at": now(), "title": f"Memory at {h['memory']}%",
             "body": (f"Ended {len(ended)} finished agent{'s' if len(ended) != 1 else ''}. " if ended else "")
             + f"{agents} agents still run here. New tasks wait. Close apps or finish tasks before the system ends them all.",
             "about": True, "task": machine_name() or "This computer"}
    if run(["hc", "label", SCHEDULER, "herdr.alert=" + json.dumps(alert, ensure_ascii=False, separators=(",", ":"))]).returncode == 0:
        g["alerted"] = now()
        report["memory_guard"]["alert"] = alert["title"]
    tmp = f"{GUARD_FILE}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump(g, f)
    os.replace(tmp, GUARD_FILE)


RETRIES = os.path.join(STORE, ".retries.json")
STALE_CUT_MS = 30 * 60000


def retry_one(row, k, log, report, project=None):
    """One agent's provider retry: the row it has now (None: no cut). The row is
    {address, role, project, log, cut, tries, reason, due | over, nudged}."""
    addr = k["address"]
    cut = provider_cut(log) if log else None
    if not cut:
        return None
    if not row or row.get("cut") != cut["id"]:
        # One that ended long before Herdr first saw it is left to the person (old news).
        if (not row or not row.get("cut")) and cut.get("at") and now() - cut["at"] > STALE_CUT_MS:
            return {**(row or {}), "address": addr, "log": log, "cut": cut["id"], "stale": True}
        f = provider_error(cut["text"], cut["code"], cut["status"])
        wait = f["passing"] and cut["tries"] <= len(PROVIDER_WAITS) and PROVIDER_WAITS[cut["tries"] - 1]
        labels = k.get("labels") or {}
        row = {"address": addr, "role": label_of(labels, "role") or "worker",
               "project": project or labels.get(PROJECT_LABEL) or k.get("cwd"),
               "log": log, "cut": cut["id"], "tries": cut["tries"],
               "reason": f"{f['reason']}, still after {cut['tries']} tries" if f["passing"] and not wait else f["reason"],
               "at": now(), **({"due": now() + wait * 60000} if wait else {"over": True})}
        report["retries"].append({"address": addr, "reason": row["reason"], "tries": row["tries"], "over": bool(row.get("over"))})
    # Not into words the person is writing to it (HERD-71): the next round tries again.
    if row.get("due") and row["due"] <= now() and status_word(k) != "working" and not typing(addr, k):
        r = run(["hc", "send", addr, NUDGE.format(row["reason"].lower())])
        if r.returncode == 0:
            row = {**row, "nudged": now()}
            row.pop("due")
    return row


def bg_ended_pass(s, t, live):
    """FAC-106: a worker Waiting on a background job whose jobs all ended, quiet for BG_NUDGE_MS since, is told
    once per job (agents.ended_jobs()); task_status() shows Needs you if no turn follows."""
    if not live or task_status(t, live) != ("working", None, WAITING_BG):
        return
    told = (t.get("bg_told") or {}).get("ids") or []
    for k in live:
        if label_of(k.get("labels"), "role") == "reviewer" or status_word(k) not in ("idle", "done"):
            continue
        quiet = k.get("status_ms") or 0
        jobs = {i: j for i, j in ended_jobs(t, k).items() if i not in told}
        if not jobs or not quiet or now() - quiet < BG_NUDGE_MS:
            continue
        msg = BG_ENDED_MSG[len(jobs) > 1].format(", ".join(jobs.values()))
        if send_or_hold(k["address"], msg):
            t["bg_told"] = {"ids": told + list(jobs), "at": now(), "address": k["address"], "turn": k.get("turn") or 0}
            s.dirty = True
        return


def retry_pass(report):
    """Provider retries for every live coordinator and task worker here: a turn its provider cut
    off (its own log says so) gets "carry on" after 1, 2, 4 and 8 minutes; the fifth cut, or an
    error waiting can't fix, needs the person. Rows live in .retries.json; a task's row is also on
    the task (its "retry"), which its status reads. Reviewers are left alone, as are agents that
    work, ask or were written to since (their log no longer ends on the cut)."""
    report.setdefault("retries", [])
    try:
        with open(RETRIES) as f:
            old = json.load(f)
    except (OSError, ValueError):
        old = {}
    rows = {}
    coords = (hc_json("ls", "--label", f"{ROLE_LABEL}=coordinator") or {}).get("terminals", [])
    coords += (hc_json("ls", "--label", "herdr.role=coordinator") or {}).get("terminals", [])
    seen_coords = set()
    for k in coords:
        if k.get("state") != "live" or "/" in k["address"] or k["address"] in seen_coords:
            continue
        seen_coords.add(k["address"])
        k = detail(k)
        k["labels"] = dict(k.get("labels") or {}, **{ROLE_LABEL: "coordinator", "herdr.role": "coordinator"})
        if not agent_running(k) or status_word(k) == "blocked":
            continue
        pid = (k.get("foreground") or {}).get("pid")
        was = old.get(k["address"]) or {}
        log = was.get("log") if was.get("pid") == pid and was.get("log") else (session_of(pid)[1] if pid else None)
        row = retry_one(was or None, k, log, report)
        if row:
            rows[k["address"]] = {**row, "pid": pid}
        elif pid and log:
            rows[k["address"]] = {"address": k["address"], "pid": pid, "log": log}
    for path in store_files():
        d = read_list(path)
        if not d or not os.path.isdir(d.get("folder") or ""):
            continue
        tasks = [t for t in d.get("tasks") or [] if t.get("state") == "running" or t.get("retry")]
        if not tasks:
            continue
        with Store(locate(d["folder"])) as s:
            for t in s.data["tasks"]:
                if t["state"] != "running":
                    if t.pop("retry", None):
                        s.dirty = True
                    continue
                mine = [detail(k) for k in live_agents(s.key(t))]
                want = None
                for k in mine:
                    if label_of(k.get("labels"), "role") == "reviewer" or not agent_running(k):
                        continue
                    if not agent_log(t, k["address"]):
                        refresh_sessions(s, t, [k])
                    log = agent_log(t, k["address"])
                    # Blocked waits for the person, unless its log ends on what only the backup gets past.
                    if status_word(k) == "blocked" and not cant_go_on(log):
                        continue
                    b = t.get("backup") or {}
                    if b.get("state") == "started" and b.get("from_address") == k["address"]:
                        continue  # the agent its backup took over from, still ending
                    row = retry_one(old.get(k["address"]), k, log, report, project=s.loc["folder"])
                    if row and row.get("over") and not row.get("stale"):
                        row = takeover(s, t, k, row, log, report)
                    if row:
                        row["task"] = f"{s.loc['folder']}#{t['id']}"
                        rows[k["address"]] = row
                    if row and not row.get("stale"):
                        want = {x: row[x] for x in ("address", "reason", "tries", "due", "over") if x in row}
                # SHEP-189: a running task whose agents were all running and now are not has gone
                # "stopped". Logged once at the moment it happens: which terminals, what Herdr saw.
                up = any(agent_running(k) and label_of(k.get("labels"), "role") != "reviewer" for k in mine)
                if t.get("alive") and not up:
                    from .machine import sample
                    h = sample()
                    stop_note("gone", task=s.ref(t), memory=h["memory"], load=h["load"], terminals=[
                        {"address": k["address"], "state": k.get("state"), "status": status_word(k),
                         "foreground": (k.get("foreground") or {}).get("name"), "role": label_of(k.get("labels"), "role")}
                        for k in mine if label_of(k.get("labels"), "role") != "reviewer"])
                if bool(t.get("alive")) != up:
                    t["alive"] = up
                    s.dirty = True
                if (t.get("retry") or None) != want:
                    if want:
                        t["retry"] = want
                    else:
                        t.pop("retry", None)
                    s.dirty = True
                bg_ended_pass(s, t, [k for k in mine if agent_running(k)])
                progress_clock(s, t, task_status(t, [k for k in mine if agent_running(k)])[0])
    if rows != old:
        tmp = f"{RETRIES}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            json.dump(rows, f)
        os.replace(tmp, RETRIES)


def cmd_tick(args):
    """Starts queued tasks that can go now and finds merged ones. Cheap when nothing waits; one
    at a time (a second tick while one runs does nothing)."""
    os.makedirs(STORE, exist_ok=True)
    lock = open(os.path.join(STORE, ".tick.lock"), "a")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        say(args, {"busy": True}, "Another tick is running.")
        return
    report = {"started": [], "done": [], "attention": [], "fired": [], "retries": []}
    if getattr(args, "machine", None) or getattr(args, "hold", None):
        try:
            set_hold(args.machine, json.loads(args.hold) if args.hold else None)
        except ValueError:
            pass
    try:
        # First: an Assistant or worker the core gave a plain shell (a crash, a reboot) gets its agent back.
        revive_pass(report)
    except Exception as e:  # never stops the queue
        report["revive_error"] = str(e)
    try:
        retry_pass(report)
    except Exception as e:  # never stops the queue
        report["retry_error"] = str(e)
    try:
        archive_pass(report, args.folder)
    except Exception as e:  # never stops the queue
        report["archive_error"] = str(e)
    for p in store_files(args.folder):
        d = read_list(p)
        tasks = (d or {}).get("tasks") or []
        if not d or not os.path.isdir(d.get("folder") or "") or not (d.get("main_watch") or any(
                t.get("state") in ("queued", "scheduled", "ready") or t.get("check_run") or (t.get("worktree") and t.get("state") == "running") or starts_now(t) for t in tasks)):
            continue
        try:
            with Store(locate(d["folder"])) as s:
                tick_store(s, report)
        except Fail as e:  # one folder never stops the rest
            report.setdefault("folder_errors", []).append(f"{d['folder']}: {e}")
    try:
        news_pass(report, args.folder)
    except Exception as e:  # never stops the queue
        report["news_error"] = str(e)
    try:
        watchdog_pass(report)
    except Exception as e:  # never stops the queue
        report["watchdog_error"] = str(e)
    try:
        end_done_pass(report, args.folder)
        memory_guard(report)
    except Exception as e:  # never stops the queue
        report["ended_error"] = str(e)
    try:
        lost_logs_pass(report, args.folder)
    except Exception as e:  # never stops the queue
        report["logs_error"] = str(e)
    try:
        sweep_sandboxes()  # temp dirs of sandboxed reviewers that ended without their trap
        simulator_pass(report, args.folder)
        device_pass(report, args.folder)
    except Exception as e:  # never stops the queue
        report["browser_error"] = str(e)
    say(args, report, "; ".join(f"{k}: {v}" for k, v in report.items() if v) or "Nothing to do.")
