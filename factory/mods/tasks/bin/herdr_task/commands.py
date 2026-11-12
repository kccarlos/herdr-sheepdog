"""The task commands: add, start, status, ready, accept, merge and the rest."""

import argparse
import hashlib
import json
import os
import re
import secrets
import shlex
import subprocess
import sys
import time

from .checks import checks_line
from .base import Fail, git, guard, hc_json, now, run, STORE, VERSION
from .settings import (
    agent_argv, agent_key, argv_for, bare, gate_min, merge_policy, MUST_WORDS, review_limits, set_opts, settings_block, settings_for, space_base,
    task_settings, worker_argv, worktrees_on,
)
from .agents import (
    agent_running, busy_again, detail, fit, LINE_MAX, live_agents, ready_turns, STATUSES, status_word, SUMMARY_MAX, WORKING_AGAIN,
)
from .sessions import refresh_sessions
from .github import already_merged, finish, gh, github, merged_here, own_work, pr_adopt, pr_open, pr_pending, pr_problem, pr_view, update_main, watch_main
from .launch import (
    base_ref, brief, caller_labels, caller_space, current_base, default_space, folder_for, handoff, launch, name_tabs,
    own_path, parent_for, resume_argv, REVIEW_READS, review_reads, retag_tabs, sandboxes, set_branch, tab_title, task_tabs,
)
from .store import is_task_list, locate, open_store, prefixes_in_use, read_list, Store, store_files
from .queues import after_refs, parse_after, queue_reason, set_after
from .archive import unarchive
from .output import line, minutes, say, view
from .scheduler import ensure_scheduler
from .proof import evidence, find_proof
from .when import when_from
from .templates import instructions
from .budget import budget_lines, budget_pass, recap_refusal, record_ci


# ---- commands -------------------------------------------------------------------------------

def cmd_where(args):
    loc = locate(args.folder)
    with Store(loc) as st:  # makes ~/.herdr/tasks and moves old lists in
        pass
    slug, path = st.slug, st.path
    say(args, {**loc, "slug": slug, "store": path, "prefix": st.prefix, "version": VERSION}, f"{loc['folder']}\n{path}")


def cmd_list(args):
    if args.all:
        folders = [d["folder"] for d in map(read_list, store_files()) if is_task_list(d)]
    else:
        folders = [locate(args.folder)["folder"]]
    groups = []
    for folder in folders:
        if not os.path.isdir(folder):
            continue
        with Store(locate(folder)) as s:
            out = []
            archived = sum(1 for t in s.data["tasks"] if t.get("archived"))
            for t in s.data["tasks"]:
                if bool(t.get("archived")) != args.archived:
                    continue
                mine = live_agents(s.key(t)) if t["state"] in ("running", "ready") else []
                if mine:
                    refresh_sessions(s, t, mine)
                v = view(s, t, [detail(k) for k in mine] if mine else [])
                if t["state"] == "queued":
                    v["reason"] = queue_reason(s, t)
                out.append(v)
            if args.archived:
                out.sort(key=lambda v: -(v.get("archived") or 0))
            groups.append({"folder": s.loc["folder"], "name": s.loc["name"], "kind": s.loc["kind"], "tasks": out,
                           **({} if args.archived else {"archived": archived})})
    if args.json:
        print(json.dumps(groups if args.all else groups[0]))
        return
    for g in groups:
        if args.all:
            print(f"{g['name']}  {g['folder']}")
        for v in g["tasks"]:
            if v["state"] != "done" or args.all or args.archived:
                print(("  " if args.all else "") + line(v))
        hidden = sum(1 for v in g["tasks"] if v["state"] == "done") if not args.archived else 0
        more = [f"{hidden} done"] if hidden and not args.all else []
        if g.get("archived") and not args.all:
            more.append(f"{g['archived']} archived: --archived lists them")
        if more:
            print(f"({'; '.join(more)})")
        elif not g["tasks"] and not args.all:
            print("No archived tasks." if args.archived else "No tasks.")


def cmd_add(args):
    prompt = args.prompt or ""
    if args.prompt_file:
        with open(args.prompt_file) as f:
            prompt = f.read()
    at = scheduled_at(args)  # refused before anything is added
    with open_store(args) as s:
        repo = s.loc["kind"] == "repo"
        if args.worktree and not repo:
            raise Fail("Worktrees need a git repo; this folder isn't one.")
        # The person's setting wins: worktrees off for this repo means in the folder, --worktree or not.
        mode = "here" if args.here or not worktrees_on(s) else "worktree"
        t = s.new(args.title.strip(), prompt, mode)
        set_opts(t, args)
        space = bare(args.space) if args.space else caller_space()
        set_base(s, t, args.base, space, new=True)
        set_branch(s, t, getattr(args, "branch", None))
        set_notes(t, args)
        if args.after:
            set_after(s, t, *parse_after(s, args.after))
        if at:
            # Started later by the scheduler, with no caller to ask: its space is kept now.
            schedule(s, t, at, {"space": space or default_space()})
        v = view(s, t)
    if at:
        ensure_scheduler_quietly()
    say(args, v, s.ref(t) + (f": {v['why']}" if at else ""))


# ---- scheduled (SHEP-38) -----------------------------------------------------------------------
# A task that starts by itself at a time (--at "fri 9:00", --in 6h): state "scheduled" with
# scheduled_for (ms) and what start would use (queue: argv, space, parent). The scheduler's tick
# (tick.py's due_pass) queues it once that time comes, also a time passed while the machine was
# off, and the queue starts it as any queued task (after its --after tasks, when there's room).

def scheduled_at(args):
    """--at / --in as a moment (ms), or None when neither was given."""
    at, within = getattr(args, "at", None), getattr(args, "in_", None)
    if at is None and within is None:
        return None
    return when_from(at, within)


def schedule(s, t, at, plan=None):
    if t["state"] not in ("todo", "scheduled", "queued"):
        raise Fail(f"{s.ref(t)} has already started ({STATUSES.get(t['state'], t['state'])}); only a task not started yet can be scheduled.")
    t["state"], t["scheduled_for"] = "scheduled", at
    if plan is not None:
        t["queue"] = plan
    for k in ("queued_at", "held"):
        t.pop(k, None)
    s.dirty = True


def unschedule(t):
    """Back to To do, as if never scheduled (a task that isn't scheduled is left as it is)."""
    if t["state"] != "scheduled":
        return
    t["state"] = "todo"
    for k in ("scheduled_for", "queue", "queued_at", "held"):
        t.pop(k, None)


def ensure_scheduler_quietly():
    try:
        ensure_scheduler(quiet=True)
    except Exception:
        pass


def set_base(s, t, base, space=None, new=False):
    """--base: the branch a worktree task branches from and merges into (its pull request's base).
    Without it a new task takes its space's base branch (Shepherdr settings > Tasks > Base branch,
    as Shepherdr's space.base: captured when added, so a later change only touches new tasks);
    unset, the folder's current branch, kept unmarked. "" goes back to the folder's."""
    if base is None:
        own = space_base(s, space) if new and t.get("mode") == "worktree" else ""
        main = s.loc["folder"]
        if own and own != current_base(main):
            try:
                base_ref(main, own)
            except Fail as e:
                raise Fail(f"This space's base branch is {own} (Shepherdr settings > Tasks > Base branch), but: {e} "
                           "Change the setting, or pass --base.")
            t["base"], t["base_set"], t["base_by"] = own, True, "space"
        return
    t.pop("base_by", None)
    base = base.strip()
    if t.get("worktree"):
        raise Fail(f"{s.ref(t)} already has its worktree from {t.get('base')}; its base can't change now.")
    if not base:
        t.pop("base", None)
        t.pop("base_set", None)
        return
    if t.get("mode") != "worktree":
        raise Fail("--base is for tasks in a worktree; this one works in the folder itself.")
    main = s.loc["folder"]
    base_ref(main, base)
    if base == current_base(main):
        t.pop("base", None)
        t.pop("base_set", None)
    else:
        t["base"], t["base_set"] = base, True


def set_notes(t, args):
    """--start-notes / --finish-notes: this task's own, replacing its folder's ("" goes back to the folder's)."""
    for which in ("start", "finish"):
        v = getattr(args, f"{which}_notes", None)
        if v is not None and v.strip():
            t[f"{which}_notes"] = v.strip()
        elif v is not None:
            t.pop(f"{which}_notes", None)


def cmd_show(args):
    with open_store(args) as s:
        t = s.find(args.id)
        live = [detail(k) for k in live_agents(s.key(t))]
        refresh_sessions(s, t, live)
        # Back at work since it said ready: saved at once, so the Accept card (which reads this)
        # says it's Working, not Ready.
        if busy_again(t, live):
            back_to_work(s, t, "its agent is at work again")
        v = view(s, t, live)
        if t["state"] == "queued":
            v["reason"] = queue_reason(s, t)
        v["settings"] = task_settings(s, t)
        on, rounds = review_limits(s, t)
        if on:
            v["review_rounds"] = {"used": rounds_used(t, bool(gate_min(s, t))), "of": rounds, "added": int(t.get("review_rounds_added") or 0)}
        v["after_refs"] = after_refs(s, t)
        block = settings_block(s, t)
        if getattr(args, "evidence", False):
            # What the Accept card shows: commits, files, checks and the proof (proof.py).
            v["evidence"] = evidence(s, t)
    text = [line(v), f"folder: {v['folder']}"]
    if v.get("worktree"):
        text.append(f"worktree: {v['worktree']} (branch {v['branch']}, base {v.get('base')})")
    elif v.get("branch") and v.get("mode") == "worktree":
        text.append(f"branch: {v['branch']} (set for this task)")
    if v.get("base_set"):
        text.append(f"merges into: {v['base']} (" + ("its space's base branch" if v.get("base_by") == "space" else "set for this task") + ")")
    if v.get("after_refs"):
        text.append("comes after: " + ", ".join(v["after_refs"]))
    if v.get("review_rounds"):
        r = v["review_rounds"]
        text.append(f"review rounds: {r['used']} of {r['of']} used" + (f" ({r['added']} added by the assistant)" if r["added"] else "")
                    + (" (ready refused: out of rounds)" if v.get("review_out") else ""))
    if v.get("agents_md"):
        text.append(f"for AGENTS.md: {v['agents_md']}")
    text += budget_lines(t)
    if checks_line(t):
        text.append(checks_line(t))
    if v.get("pr"):
        text.append(f"pull request: #{v['pr']['number']} {v['pr']['url']}")
    if v.get("why") and v["status"] == "needs":
        text.append(f"needs the person: {v['why']}")
    if v.get("proof"):
        text.append(f"proof: {v['proof']['title']} ({v['proof']['id']})")
    if v.get("archived"):
        text.append(f"archived: {time.strftime('%Y-%m-%d %H:%M', time.localtime(v['archived'] / 1000))} (still kept; writing to it brings it back)")
    for k in v["live"]:
        text.append(f"agent: {k['address']} {k['status'] or ''}")
    if v.get("prompt"):
        text.append("\n" + v["prompt"].strip())
    text.append("\n" + block)
    say(args, v, "\n".join(text))


def begin(s, t, argv, space, parent=None):
    """Starts the task's first agent. Returns its record."""
    t["state"], t["started"] = "running", now()
    # Its brief teaches `reviewed`: ready waits for a review of its final commit (review_refusal).
    t["review_gate"] = True
    for k in ("queue", "queued_at", "attention", "held", "scheduled_for"):
        t.pop(k, None)
    if t.get("mode") == "worktree" and not t.get("worktree") and not worktrees_on(s, space):
        t["mode"] = "here"
    folder_for(s, t)
    parent = parent_for(parent, space)
    return launch(s, t, argv, space, brief(s, t, parent, space, argv), parent=parent)


def cmd_start(args):
    with open_store(args) as s:
        t = s.find(args.id)
        live = [k for k in map(detail, live_agents(s.key(t))) if agent_running(k)]
        if live:
            raise Fail(f"{s.ref(t)} is already running ({live[0]['address']}). Use switch or resume.")
        if t["state"] == "done":
            raise Fail(f"{s.ref(t)} is done. Reopen it first.")
        if args.here:
            t["mode"] = "here"
        elif args.worktree and worktrees_on(s):
            t["mode"] = "worktree"
        set_branch(s, t, getattr(args, "branch", None))
        q = t.get("queue") or {}
        space = bare(args.space) if args.space else (q.get("space") or default_space())
        argv = agent_argv(None, args.argv) if args.argv else worker_argv(s, t, space, args.agent) if args.agent else \
            (q.get("argv") or worker_argv(s, t, space))
        parent = args.parent or q.get("parent")
        plan = {"argv": argv, "space": space, "parent": parent, **({"custom": True} if args.argv else {})}
        at = scheduled_at(args)
        if at:
            # start --at / --in: it starts by itself then, as this start would now.
            schedule(s, t, at, plan)
            v = view(s, t)
            reason = "scheduled"
        # A queued task started again goes now; otherwise it waits its turn. A scheduled one
        # started now waits only for the queue (start --now: not even that).
        elif (reason := None if (t["state"] == "queued" or args.now) else queue_reason(s, t)):
            t["state"], t["queued_at"] = "queued", now()
            t.pop("scheduled_for", None)
            t["queue"] = plan
            s.dirty = True
            v = view(s, t)
        else:
            a = begin(s, t, argv, space, parent)
            v = view(s, t)
    # The scheduler starts queued tasks and tells the Assistant its tasks' news.
    try:
        ensure_scheduler(quiet=True)
    except Exception:
        pass
    if reason == "scheduled":
        say(args, v, f"{s.ref(t)} is scheduled: {v['why']}. It starts by itself then.")
        return
    if reason:
        say(args, {**v, "queued": True, "reason": reason}, f"{s.ref(t)} is queued ({reason}). It starts by itself when it can.")
        return
    out = {**v, "terminal": a["address"], "space": space, "parent": a.get("parent")}
    say(args, out, f"{s.ref(t)} started: {a['address']} in {folder_desc(t, s)}")


def folder_desc(t, s):
    return f"{t['worktree']} ({t['branch']})" if t.get("worktree") else s.loc["folder"]


def ready_words(args):
    """The ready summary in one line, refused past SUMMARY_MAX (as Shepherdr's) before anything
    changes; --details, any length, as written."""
    said = " ".join(str(args.summary or "").split())
    if len(said) > SUMMARY_MAX:
        raise Fail(f"That summary is {len(said)} characters: write a shorter one, {SUMMARY_MAX} at most, in plain words on "
                   f"what the person now sees or can do (no file names, flags or jargon). Put the detail in --details \"…\" "
                   f"(a few short bullets in plain words, folded in the Accept card) or in your proof, then run ready again.")
    return said, str(args.details or "").strip()


def cmd_ready(args):
    said, details = ready_words(args)
    with open_store(args) as s:
        t = s.find(args.id)
        me = os.environ.get("HC_TERMINAL_ID")
        if me and any(a["address"] == me for a in t["agents"]):
            refresh_sessions(s, t, [k for k in live_agents(s.key(t)) if k["address"] == me])
        if t["state"] == "done":
            raise Fail(f"{s.ref(t)} is already done.")
        wt = t.get("worktree") if t.get("mode") == "worktree" else None
        if wt and os.path.isdir(wt):
            if git(wt, "status", "--porcelain", "--untracked-files=no")[1]:
                raise Fail(f"{wt} has uncommitted changes. Commit them, then run ready again.")
            t["head"] = git(wt, "rev-parse", "HEAD")[1]
            own_work(s, t)
        refused = recap_refusal(s, t) or review_refusal(s, t, wt)
        if refused:
            # Kept (the task's line says why it waits); refused after the store is saved.
            s.dirty = True
        else:
            if said:
                t["summary"] = said
                # A new summary brings its own details (or none): an older one's would be stale.
                if details:
                    t["details"] = details
                else:
                    t.pop("details", None)
            elif details:
                t["details"] = details
            # Its proof artifact (--proof <id>, else its newest one titled "... · Proof"), linked from
            # the person's Accept card.
            proof = find_proof(s, t, getattr(args, "proof", None))
            if proof:
                t["proof"] = proof
            else:
                t.pop("proof", None)
            asked = next((x["value"] for x in task_settings(s, t) if x["label"] == "Proof"), False)
            note = " ".join(str(args.agents_md or "").split())
            if note:
                # "For AGENTS.md": what the next agent would need about setting up or testing this
                # repo; the Assistant decides whether it goes into the repo's AGENTS.md.
                t["agents_md"] = note[:300]
            for k in ("attention", "attention_at", "handed_at", "fix_requested", "progress", "retry", "review_out", "back"):
                t.pop(k, None)
            # Its agents' turns now: a later turn of theirs takes it back to work (busy_again()).
            t["ready_turns"] = ready_turns(live_agents(s.key(t)))
            s.dirty = True
    if refused:
        raise Fail(refused)
    with open_store(args) as s:
        t = s.find(args.id)
        # Its review is the agent's own, as its brief's Settings block says (herdr-task starts no
        # reviewers). A worktree branch with nothing beyond its base (none of its own, or only its
        # base caught up with: a research or artifact-only task) still waits in Ready for the
        # person's Accept, its summary, details and artifacts on the card (SHEP-96); no pull request,
        # and Accept just marks it done and cleans up its worktree.
        if wt and t.get("start") and in_base(s, t) and not merged_here(s, t):
            t["nothing_to_merge"] = True
        else:
            t.pop("nothing_to_merge", None)
        # Its work already in its base (SHEP-59: its pull request squash-merged, or nothing left to
        # merge): done at once, never a second pull request.
        gone = already_merged(s, t) if wt and t.get("start") and not t.get("nothing_to_merge") else None
        if gone is not None:
            t["merged"] = True
            if gone:
                watch_main(s, t, (gone.get("mergeCommit") or {}).get("oid"))
                update_main(s, t)
            finish(s, t, even_if_busy=True)
            v = view(s, t)
            how = f"pull request #{gone['number']} is merged" if gone else f"its work is already in {t.get('base') or 'main'}"
            say(args, v, f"{s.ref(t)}: {how}; done.")
            return
        msg = (None if t.get("nothing_to_merge") else gate_step(s, t)) or finalize(s, t)
        if proof:
            msg += f" Proof: {proof['title']}."
        elif asked:
            msg += (" No proof artifact found, and your Settings block asks for one: publish it (herdr-artifact), "
                    "then run ready again with --proof <its id>.")
        v = view(s, t)
    say(args, v, msg)


REVIEWED = ("report", "pass", "changes")  # a recorded review round (report: the agent's own reviewed)


def rounds_used(t, gate=False):
    """Review rounds used: in a gate folder, the reviewers herdr-task started (review_runs)."""
    if gate:
        return len(t.get("review_runs") or [])
    return sum(1 for r in t.get("reviews") or [] if r.get("result") in REVIEWED)


def gate_passes(t, head):
    return sum(1 for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task" and r["head"] == head and r["verdict"] == "pass")


def fork_of(wt, base, tip):
    """Where tip left its base: the merge-base with the local base or the remote's (new worktrees branch from the
    remote's, which can be ahead of the local one); the later of the two."""
    from .github import remote_of
    mbs = []
    for ref in (base, f"refs/remotes/{remote_of(wt)}/{base}"):
        ok, out, _ = git(wt, "merge-base", ref, tip)
        if ok and out and out not in mbs:
            mbs.append(out)
    return mbs[1] if len(mbs) > 1 and git(wt, "merge-base", "--is-ancestor", mbs[0], mbs[1])[0] else (mbs[0] if mbs else "")


def review_diff(wt, base, head, mine):
    """(what the diff is, the diff) for a gate round (FAC-53). Round 1 and a re-review of the same head: the whole
    change from the merge-base (never the stored start, stale after a rebase). Later: only what changed since the last
    reviewed head, and a line of the earlier findings. If the base moved (a rebase, or the base merged in), the
    difference between the task's own change then and now, not the base's commits."""
    mb = fork_of(wt, base, head) or base
    whole = (f"Its diff (git diff {mb}..{head})", git(wt, "diff", f"{mb}..{head}")[1])
    if not mine:
        return whole
    prev = next((r["head"] for r in reversed(mine) if r["head"] != head), None)  # the last reviewed head that isn't this one
    earlier = "; ".join(f"round {r['round']} ({r['head'][:7]}) {r['verdict']}: {' '.join((r.get('report') or 'no findings').split())}" for r in mine)
    old = fork_of(wt, base, prev) if prev and git(wt, "cat-file", "-e", prev)[0] else ""
    if not old:  # nothing reviewed before this head (another pass of it), or it's gone: the whole change, with the earlier rounds
        return (f"{whole[0]}. Earlier rounds, to know what was fixed: {earlier}", whole[1])
    if old == mb:
        what, out = f"Only what changed since the last reviewed head {prev[:7]} (git diff {prev}..{head})", git(wt, "diff", f"{prev}..{head}")[1]
    else:
        import difflib
        def files(text):
            out = {}
            for chunk in ("\n" + text).split("\ndiff --git ")[1:]:
                lines = [re.sub(r"^@@ -[0-9,]+ \+[0-9,]+ @@", "@@", x) for x in chunk.splitlines() if not x.startswith("index ")]
                out[lines[0].split(" b/")[-1]] = lines[1:]
            return out
        then, now_ = (files(git(wt, "diff", f"{x}..{y}")[1]) for x, y in ((old, prev), (mb, head)))
        what = (f"The branch's base moved since the last reviewed head {prev[:7]}; only how the task's own change differs now "
                f"(its diff {old[:7]}..{prev[:7]} against {mb[:7]}..{head[:7]})")
        out = "\n".join(l for f in sorted({*then, *now_}) for l in difflib.unified_diff(
            then.get(f, []), now_.get(f, []), f"reviewed/{f}", f"now/{f}", lineterm="", n=2)) or "(none)"
    return (f"{what}. Earlier rounds, to know what was fixed: {earlier}. The whole change is `git diff {mb}..{head}`", out)


def gate_step(s, t):
    """A gate folder's ready (FAC-13): None once its head has review.min passing rounds from
    herdr-task's own reviewer; else starts the next one for that head (or says why it waits)."""
    need, head = gate_min(s, t), t.get("head")
    if not need or gate_passes(t, head) >= need:
        return None
    if not head or not os.path.isdir(t.get("worktree") or ""):
        raise Fail(f"{s.ref(t)} has no worktree to review: start it first.")
    back_to_work(s, t, "a new commit to review")  # a Ready task's unreviewed commit never stays mergeable
    t.pop("auto_merge", None)
    mine = [r for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task"]
    runs, done = t.get("review_runs") or [], {r["round"] for r in mine}
    if mine and mine[-1]["head"] == head and mine[-1]["verdict"] == "changes":
        return f"{s.ref(t)}: Herdr's reviewer asked for changes to {head[:7]}: fix them, commit, then run ready again."
    # Never two reviewers at once (FAC-53): a live one on any head, even an older one, holds the next round back.
    def running(r):
        k = hc_json("get", r["address"]) or {}
        if r["round"] in done or k.get("state") != "live" or not agent_running(k):
            return False
        if r["head"] != head and status_word(k) in ("idle", "done"):
            end_reviewer(r["address"])  # its turn ended on an older head: its verdict can never apply, it mustn't block this one
            return False
        return True
    busy = next((r for r in runs if running(r)), None)
    if busy:
        return (f"{s.ref(t)}: Herdr's reviewer ({busy['address']}) is still reviewing {busy['head'][:7]}; no second one starts. "
                + ("Its findings come to you as a message." if busy["head"] == head else "Run ready again once it finishes."))
    on, rounds = review_limits(s, t)
    if len(runs) >= rounds:
        t["review_out"] = now()
        return (f"{s.ref(t)}: all {rounds} review rounds are used and {head[:7]} has {gate_passes(t, head)} of {need} passing "
                f"rounds. Say why in your chat and end your turn: only your Assistant can add a round (herdr-task review-more).")
    rows = {x["label"]: x["value"] for x in task_settings(s, t)}
    rv, wt, ref, ht, folder = rows["Reviewer"], t["worktree"], s.ref(t), own_path(), shlex.quote(s.loc["folder"])
    sand = sandboxes([rv["agent"]])  # agy on a Mac: sandboxed, nothing prompts, its verdict read from its terminal
    # Claude Code's plan mode blocks every command, the verdict's `herdr-task review` too, so a Claude reviewer
    # runs in auto mode: its allow list (the verdict, its reads) runs, the rest goes past auto mode's checks.
    perm = "read-only" if rv["agent"] == "codex" else "full" if sand else "auto" if rv["agent"] == "claude" else "plan"
    argv = argv_for({**rv, "perm": perm})
    at = now()
    code = round_code({"round": len(runs) + 1, "head": head, "at": at}) if sand else None
    shown, diff = review_diff(wt, t.get("base") or "main", head, mine)
    cmd = f"{ht} review {ref} --folder {folder}"
    check = (settings_for(s, t.get("space"))["tasks"].get("check") or "").strip()
    prompt = "\n\n".join([
        instructions("reviewer"), f"The task, {ref}:\n" + ((t.get("prompt") or "").strip() or t["title"]),
        f"Review commit {head} in {wt}. {shown}" + (", cut: run it for the rest" if len(diff) > 20000 else "")
        + f":\n{diff[:20000]}",
        f"Must fix: {MUST_WORDS[rows['Must fix']]}. Herdr started you, not the worker. "
        + (f"You run in a sandbox: you can read only this worktree, its git history and the system's tools, write only in "
           f"$TMPDIR, and can't reach Herdr. When done, end your last message with your findings, each with its level and "
           f"proof, between two lines of their own: first one holding only this round's code and the word begin, last one "
           f"holding only the code and your verdict: pass, or changes and how many findings are at or above Must fix. Herdr "
           f"reads it from this terminal while {head[:7]} is the worktree's HEAD with nothing uncommitted, and runs this "
           f"folder's check itself, outside the sandbox: don't run it here. This round's code is {code}." if sand else
        f"When done, record your verdict with one of "
        f"`{cmd} --pass` or `{cmd} --changes \"<each finding with its level and proof>\" --must-fix <how many are at or above "
        f"Must fix>`. It is taken only from this terminal while {head[:7]} is the worktree's HEAD with nothing uncommitted. "
        f"Then end with your findings as your last message."
        + (f" Your shell runs without asking only {', '.join(REVIEW_READS)}" + (f" and this folder's check, `{check}`" if check else "")
           + ": anything else waits on a prompt nobody here can answer." if rv["agent"] in ("agy", "claude") else "")
        + (" Codex's sandbox can't reach Herdr: give that command sandbox_permissions \"require_escalated\"." if rv["agent"] == "codex" else ""))])
    live = workers(s, t)
    a = launch(s, t, argv, t.get("space"), prompt, parent=live[-1]["address"] if live else None, role="reviewer",
               reads=review_reads(check))
    t.setdefault("review_runs", []).append({"round": len(runs) + 1, "head": head, "address": a["address"], "cli": agent_key(argv),
                                            "model": rv.get("model") or None, "at": at})
    t["progress"] = {"text": fit("Herdr's reviewer is on it"), "at": now(), "set": now(), "by": "herdr"}
    return f"{s.ref(t)}: Herdr started review round {len(runs) + 1} of {head[:7]} ({a['address']}); its findings come to you as a message."


def review_refusal(s, t, wt):
    """Why ready is refused for review (as Shepherdr's: `ready` waits for a review of the final
    commit), or None. Only tasks whose brief taught `reviewed` (review_gate) and whose Settings
    block says Review: on. A worktree task's final commit must be one a round was recorded on; a
    task in the folder itself needs a round recorded since it started. Out of rounds, only the
    Assistant adds one (review-more): the task then waits, saying so (review_out)."""
    on, rounds = review_limits(s, t)
    if not on or not t.get("review_gate") or gate_min(s, t):
        return None  # a gate folder: herdr-task reviews after ready (gate_step)
    if wt and t.get("start") and t.get("head") == t["start"]:
        return None  # no commits of its own: nothing to review (nothing to merge)
    revs = [r for r in t.get("reviews") or [] if r.get("result") in REVIEWED and (r.get("at") or 0) >= (t.get("started") or 0)]
    head = t.get("head") if wt else None
    if (any(r.get("head") == head for r in revs) if wt else bool(revs)):
        return None
    ref, ht, folder = s.ref(t), own_path(), shlex.quote(s.loc["folder"])
    what = f"your final commit {head[:7]}" if head else "this task's work"
    if rounds_used(t) >= rounds:
        t["review_out"] = now()
        return (f"All {rounds} review rounds are used and {what} isn't reviewed. Say why in your chat, set your status line to "
                f"it, and end your turn: only your Assistant can add a round (herdr-task review-more {ref}).")
    return (f"Review is on and {what} isn't reviewed. Have it reviewed as your brief says, fix what must be fixed, record the "
            f"round with `{ht} reviewed {ref} --folder {folder} --report \"<findings, a line>\"` (on the commit you'd say "
            f"ready at), then run ready again. {rounds - rounds_used(t)} of {rounds} rounds left.")


def cmd_reviewed(args):
    """A task's agent records one review round of its current commit (its own review: a helper agent
    it started as its hc child, as its brief says). Uses one of its Review rounds; `ready` then accepts that
    commit. Out of rounds it is refused: only the Assistant adds one (review-more)."""
    with open_store(args) as s:
        t = s.find(args.id)
        if t["state"] not in ("running", "ready"):
            raise Fail(f"{s.ref(t)} isn't being worked on ({STATUSES.get(t['state'], t['state'])}).")
        on, rounds = review_limits(s, t)
        if not on:
            raise Fail(f"Review is off for {s.ref(t)}: there is no round to record.")
        if gate_min(s, t):
            raise Fail(f"{s.ref(t)}'s review is herdr-task's own: say ready, and it starts the reviewer for your final commit.")
        used = rounds_used(t)
        if used >= rounds:
            raise Fail(f"All {rounds} review rounds of {s.ref(t)} are used. Say why in your chat and end your turn: only your "
                       f"Assistant can add a round (herdr-task review-more {s.ref(t)}).")
        wt = t.get("worktree") if t.get("mode") == "worktree" else None
        head = None
        if wt and os.path.isdir(wt):
            if git(wt, "status", "--porcelain", "--untracked-files=no")[1]:
                raise Fail(f"{wt} has uncommitted changes: a review is of a commit. Commit first, then review it.")
            head = git(wt, "rev-parse", "HEAD")[1]
        elif s.loc["kind"] == "repo":
            head = git(s.loc["folder"], "rev-parse", "HEAD")[1] or None
        text = " ".join(str(args.report or "").split())[:2000]
        t.setdefault("reviews", []).append({"round": used + 1, "result": "report", "text": text, "at": now(), "head": head,
                                            "by": os.environ.get("HC_TERMINAL_ID")})
        t.pop("review_out", None)
        s.dirty = True
        v = view(s, t)
    on_what = f" for commit {head[:7]}" if head else ""
    say(args, {**v, "round": used + 1, "rounds": rounds}, f"{s.ref(t)}: review round {used + 1} of {rounds} recorded{on_what}.")


def cmd_review_more(args):
    """The Assistant gives a task exactly one more review round (as Shepherdr's `task review-more`):
    only once all its rounds are used. The limit stops an agent reviewing for ever, so the task's
    own agent can never raise it. Its agent is told."""
    labels = caller_labels()
    with open_store(args) as s:
        t = s.find(args.id)
        if labels.get("herdr.task") == s.key(t) or labels.get("herdr.role") == "reviewer":
            raise Fail("A task's own agent can't add review rounds: say why in your chat and end your turn; your Assistant decides.")
        on, rounds = review_limits(s, t)
        if not on:
            raise Fail(f"Review is off for {s.ref(t)}.")
        if t["state"] == "done":
            raise Fail(f"{s.ref(t)} is already done; it needs no more review.")
        used = rounds_used(t, bool(gate_min(s, t)))
        if used < rounds:
            raise Fail(f"{s.ref(t)} has used {used} of {rounds} review rounds; add one only when they are all used.")
        t["review_rounds_added"] = int(t.get("review_rounds_added") or 0) + 1
        t.pop("review_out", None)
        s.dirty = True
        live = workers(s, t)
        told = None
        if live:
            ht, folder = own_path(), shlex.quote(s.loc["folder"])
            from .news import send_or_hold
            r = send_or_hold(live[-1]["address"],
                     f"Your Assistant added one review round to {s.ref(t)}: {rounds + 1} in all. " + (
                         f"Run `{ht} ready {s.ref(t)} --folder {folder}` again: Herdr starts its reviewer." if gate_min(s, t) else
                         f"Have your final commit reviewed, record it with `{ht} reviewed {s.ref(t)} --folder {folder} --report "
                         f"\"<findings, a line>\"`, then run ready again."))
            told = live[-1]["address"] if r else None
        v = view(s, t)
    say(args, {**v, "rounds": rounds + 1, "told": told},
        f"{s.ref(t)} may now use {rounds + 1} review rounds" + (f"; told {told}." if told else "; no agent is running to tell."))


def parse_left(text):
    """Time left as an agent writes it: 5m, 90s, 1h, 1h30m, 2 (minutes); in ms."""
    t = str(text).strip().lower().replace(" ", "")
    if re.fullmatch(r"\d+(?:\.\d+)?", t):
        return int(float(t) * 60000)
    parts = re.findall(r"(\d+(?:\.\d+)?)(h|hr|hrs|hours?|m|min|mins|minutes?|s|sec|secs|seconds?)", t)
    if not parts or "".join(a + b for a, b in parts) != t:
        raise Fail(f"--left takes a time like 5m, 90s or 1h30m, not \"{text}\".")
    return int(sum(float(n) * {"h": 3600000, "m": 60000, "s": 1000}[u[0]] for n, u in parts))


def cmd_status(args):
    """A task agent says what it's doing, how far along it is and the time left, at the start and
    at each natural step (as Shepherdr's `shepherdr status`). Herdr guesses nothing: it shows
    what the agent said and counts the minutes down while it works."""
    text = " ".join(str(args.text or "").split())
    if not text:
        raise Fail("Say what you're doing: herdr-task status <id> \"Fixing the two failing tests\" --done 40 --left 5m")
    if args.done is not None and not 0 <= args.done <= 100:
        raise Fail("--done is a percentage, 0 to 100.")
    left = parse_left(args.left) if args.left else None
    with open_store(args) as s:
        t = s.find(args.id)
        if t["state"] not in ("running", "ready"):
            raise Fail(f"{s.ref(t)} isn't being worked on ({STATUSES.get(t['state'], t['state'])}); nothing to report.")
        shown = fit(text)
        # set: when it was said ("at" moves on while it waits, for the minutes left).
        t["progress"] = {"text": shown, "at": now(), "set": now(), **({"done": args.done} if args.done is not None else {}),
                         **({"left": left} if left is not None else {})}
        s.dirty = True
        v = view(s, t, [detail(k) for k in live_agents(s.key(t))])
    note = f" Cut to {LINE_MAX} characters: \"{shown}\". Keep it shorter next time." if shown != text else ""
    # Every status gives the time left, a rough guess beats none (SHEP-13): one without it asks for it.
    if left is None:
        note += " Add --left with your best guess of the time left (like 20m) to every status."
    say(args, {**v, "cut": shown != text}, f"{s.ref(t)}: {shown}" + (f" · {args.done}%" if args.done is not None else "")
        + (f" · ~{minutes(round(left / 60000))} left" if left is not None else "") + "." + note)


ALERT = "herdr.alert"


def cmd_alert(args):
    """An agent tells the person something that can't wait (as Shepherdr's `shepherdr alert`): a
    toast and a sound in every Herdr window. Never asks permission. A task's agent (or an alert
    --task names) always reaches the person; an Assistant's follows its space's Notify (Off:
    nothing). It sets the core label herdr.alert on the caller's own terminal, which every Herdr
    client sees at once, here or on another machine; Herdr shows each new one once (inbox.js)."""
    title, body = " ".join(str(args.title or "").split()), " ".join(str(args.body or "").split())
    if not title:
        raise Fail("Say what can't wait: herdr-task alert \"Deploy is down\" --body \"Checks fail on main since 12:40\"")
    if len(title) > 240 or len(body) > 4000:
        raise Fail("Keep an alert short: a title and a line or two of why.")
    me = os.environ.get("HC_TERMINAL_ID")
    if not me:
        raise Fail("Run it from an agent's own terminal (there's no HC_TERMINAL_ID here).")
    labels = caller_labels()
    ref = None
    if args.task:
        with open_store(argparse.Namespace(folder=args.folder, id=args.task), write=False) as s:
            ref = s.ref(s.find(args.task))
    elif labels.get("herdr.task"):
        # Its own task: "<slug>#<n>" -> HERD-44, from that list's prefix.
        slug, _, n = labels["herdr.task"].rpartition("#")
        try:
            with open(os.path.join(STORE, f"{slug}.json")) as f:
                pre = json.load(f).get("prefix")
        except (OSError, ValueError):
            pre = None
        ref = f"{pre}-{n}" if pre else None
    # The label holds what a toast shows (inbox.js cuts it again to fit).
    alert = {"id": secrets.token_hex(4), "at": now(), "title": title[:120], "body": body[:300],
             **({"task": ref} if ref else {}), **({"about": True} if args.task else {})}
    r = run(["hc", "label", me, f"{ALERT}={json.dumps(alert, ensure_ascii=False, separators=(',', ':'))}"])
    if r.returncode != 0:
        raise Fail((r.stderr or r.stdout).strip() or "hc label failed")
    say(args, alert, "Alert sent.")


def back_to_work(s, t, why, said=WORKING_AGAIN):
    """A Ready task taken back to work (herdr-task fix or resume, or its agent at work again):
    Working, its status line saying so until its agent reports, and no Accept until its next
    ready. why: what took it back, for its Assistant's news ("SHEP-7 is working again: why")."""
    if t["state"] != "ready":
        return False
    t["state"] = "running"
    for k in ("ready_turns", "merge_requested"):
        t.pop(k, None)
    t["back"] = {"at": now(), "why": why}
    # by: Herdr's words, not its agent's (on_it() counts only an agent's own line).
    t["progress"] = {"text": fit(said), "at": now(), "set": now(), "by": "herdr"}
    s.dirty = True
    return True


def finalize(s, t):
    """Ready: on GitHub its pull request opens; then the merge policy in Settings decides who
    merges: auto (now), checks (once its checks pass) or ask (the person). Returns what to say."""
    t["state"], t["ready_at"] = "ready", now()
    for k in ("attention", "merge_requested", "review"):
        t.pop(k, None)
    s.dirty = True
    wt = t.get("worktree")
    msg = f"{s.ref(t)} is ready."
    if t.get("mode") != "worktree" or not wt or t.get("nothing_to_merge"):
        # Nothing to merge (in the folder itself, or no commits beyond its base): it waits in Ready
        # for the person's Accept, whatever the merge setting; no pull request.
        t.pop("auto_merge", None)
        return msg + (" Nothing to merge (no commits beyond its base)." if t.get("nothing_to_merge") else "") + \
            " The person accepts it in the Tasks panel."
    repo = github(s) if wt and os.path.isdir(wt) else None
    if repo:
        try:
            pr = pr_open(s, t, repo, t.get("summary"))
            pr["checked"] = 0
            msg = f"{s.ref(t)} is ready: pull request #{pr['number']}, {pr['url']}."
        except Fail as e:
            t["attention"] = str(e)
            return f"{s.ref(t)} is ready, but: {e} Tell the person."
    policy = merge_policy(s, t)
    if policy in ("auto", "checks"):
        t["auto_merge"] = policy
        if t.get("pr"):
            if policy == "auto":
                err = merge_now(s, t)
                return msg + (f" Merging it waits: {err}" if err else " Merged; done.")
            return msg + " It merges by itself once its checks pass."
        try:
            sent, started = tell(s, t, local_merge_msg(s, t), None)
            t["merge_requested"] = now()
            return msg + " Settings say merge it right away: the next message says how."
        except Fail as e:
            t["attention"] = f"Couldn't ask for the merge: {e}"
    else:
        t.pop("auto_merge", None)
    return msg + " The person accepts it in the Tasks panel, which merges it."


def cmd_review(args):
    """A reviewer's verdict: --pass (on to merging) or --changes (back to the worker). Only for
    reviewers herdr-task started before task agents ran their own review: kept so they finish."""
    if bool(args.passed) == bool(args.changes or args.changes_file):
        raise Fail("Say --pass, or --changes with what must be fixed.")
    text = args.changes or ""
    if args.changes_file:
        with open(args.changes_file) as f:
            text = f.read()
    with open_store(args) as s:
        t = s.find(args.id)
        if gate_min(s, t):  # herdr-task's own reviewer in a gate folder (FAC-13)
            return gate_verdict(s, t, args, text)
        rv = t.pop("review", None) or {}
        cfg = settings_for(s, t.get("space"))["tasks"]
        of = rv.get("of") or int(cfg.get("review_rounds") or 1)
        n = rv.get("round") or len(t.get("reviews") or []) + 1
        rec = {"round": n, "result": "pass" if args.passed else "changes", "text": (text or args.notes or "").strip(),
               "at": now(), "head": t.get("head"), "by": os.environ.get("HC_TERMINAL_ID")}
        t.setdefault("reviews", []).append(rec)
        s.dirty = True
        if args.passed:
            msg = f"{s.ref(t)}: review round {n} passed. " + finalize(s, t)
        else:
            last = n >= of
            if t["state"] == "ready":
                t["state"] = "running"
            sent, _ = tell(s, t, (f"Review round {n} of {of} for {s.ref(t)} found what must be fixed before it can merge:\n{text.strip()}\n\n"
                                     f"Fix these, commit, then run `herdr-task ready {s.ref(t)} --folder {shlex.quote(s.loc['folder'])}` again"
                                     + (" (that was the last review round)." if last else " for the next round.")), None)
            msg = f"{s.ref(t)}: findings sent to {sent}."
        v = view(s, t)
    # This reviewer is finished: it ends a moment after this command returns.
    me = os.environ.get("HC_TERMINAL_ID")
    if me and rv.get("reviewer") == me:
        end_reviewer(me)
    say(args, v, msg)


END_REVIEWER_SH = ('sleep 4; hc rm "$1" >/dev/null 2>&1 || { '
                   'if [ -n "$2" ]; then kill -TERM "$2" 2>/dev/null; '
                   'for c in $(pgrep -P "$2" 2>/dev/null); do kill -TERM "$c" 2>/dev/null; kill -TERM -"$c" 2>/dev/null; done; '
                   'kill -TERM -"$2" 2>/dev/null; sleep 2; hc rm "$1" >/dev/null 2>&1 && exit 0; fi; '
                   'hc kill "$1" >/dev/null 2>&1; sleep 2; hc rm "$1" >/dev/null 2>&1; }')


def end_reviewer(me):
    """Stops a finished reviewer so its shell traps TERM and exits 0 (hc: reason 'exited', exit_code 0),
    with HC_TERMINAL_ID set to its parent so any fallback hc kill never types an exit note into the worker."""
    guard(["hc"])
    k = hc_json("get", me) or {}
    pid = str(k["pid"]) if "/" not in str(me) and type(k.get("pid")) is int and k["pid"] > 1 else ""
    env = {x: v for x, v in os.environ.items() if x != "HC_TERMINAL_ID"}
    if k.get("parent"):
        env["HC_TERMINAL_ID"] = k["parent"]
    subprocess.Popen(["sh", "-c", END_REVIEWER_SH, "x", me, pid], env=env,
                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)


def tell_pass(s, t, round_n, line):
    """Tells a live worker a passing gate round once (never starting a worker just for a pass)."""
    told = t.setdefault("verdict_told", [])
    if round_n in told:
        return None
    turns = dict(ready_turns(live_agents(s.key(t)))) if t.get("state") == "ready" else None
    try:
        sent, _ = tell(s, t, " ".join(line.split()), None, False)
    except Fail:
        return None
    if sent:
        told.append(round_n)
        if turns and t.get("state") == "ready":
            if sent in turns:
                turns[sent] += 1
            t["ready_turns"] = turns
    return sent


def gate_verdict(s, t, args, text):
    """A verdict from herdr-task's own reviewer (FAC-13): only from its open round's terminal, while
    the worktree's HEAD is still that round's head with nothing uncommitted. That reviewer then ends."""
    me = os.environ.get("HC_TERMINAL_ID")
    if t["state"] == "done":
        raise Fail(f"{s.ref(t)} is already done.")
    done = {r["round"] for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task"}
    rn = next((r for r in reversed(t.get("review_runs") or []) if me and r["address"] == me and r["round"] not in done), None)
    if not rn:
        raise Fail(f"Only the reviewer herdr-task started for {s.ref(t)}'s open review round records it, from its own terminal.")
    must = args.must_fix if args.must_fix is not None else (0 if args.passed else None)
    if must is None or must < 0 or bool(args.passed) != (must == 0):  # findings below Must fix never block
        raise Fail("Say --pass (nothing at Must fix; --notes for the rest), or --changes \"…\" --must-fix N: N (1 or more) findings at or above Must fix.")
    msg = record_verdict(s, t, rn, bool(args.passed), must, text or args.notes or "")
    end_reviewer(me)
    say(args, view(s, t), msg)


def record_verdict(s, t, rn, passed, must, text):
    """Records review round rn's verdict (from its own terminal: gate_verdict, gate_reads) while the
    worktree's HEAD is still its head with nothing uncommitted; returns what happened."""
    wt = t.get("worktree") or ""
    if git(wt, "rev-parse", "HEAD")[1] != rn["head"] or git(wt, "status", "--porcelain", "--untracked-files=no")[1]:
        raise Fail(f"{wt} has moved on from {rn['head'][:7]} or has uncommitted changes: this round's verdict no longer applies.")
    t.setdefault("reviews", []).append({"round": rn["round"], "head": rn["head"], "by": rn["address"], "started_by": "herdr-task",
                                        "verdict": "pass" if passed else "changes", "must_fix": must,
                                        "report": text.strip()[:2000], "at": now()})
    for k in ("review_out", "progress"):
        t.pop(k, None)
    s.dirty = True
    if t.get("mode") == "worktree" and budget_pass(s, t):  # a recap first: no next round, no merge, no fixing
        msg = f"{s.ref(t)}: review round {rn['round']} {'passed' if passed else 'asked for changes'}; {t['recap']['title']}."
    elif passed:
        ref, need, k = s.ref(t), gate_min(s, t), gate_passes(t, rn["head"])
        before = len(t.get("review_runs") or [])
        step = gate_step(s, t)
        if step:
            runs = t.get("review_runs") or []
            rest = (f"Round {runs[-1]['round']} has started; its findings come to you as a message, nothing to do meanwhile."
                    if len(runs) > before else step)
            line = f"Herdr's review round {rn['round']} of {ref} passed ({k} of {need} passing). {rest}"
            msg = f"{ref}: review round {rn['round']} passed. {step}"
        else:
            fin = finalize(s, t)
            line = f"Herdr's review round {rn['round']} of {ref} passed: {ref} goes on to merging ({k} of {need} passing). {fin}"
            msg = f"{ref}: review round {rn['round']} passed. {fin}"
        tell_pass(s, t, rn["round"], line)
    else:
        told = t.setdefault("verdict_told", [])
        sent = None
        if rn["round"] not in told:
            sent, _ = tell(s, t, (f"Herdr's review round {rn['round']} of {s.ref(t)} ({rn['head'][:7]}) found what must be fixed:\n"
                                  f"{text.strip()}\n\nFix these, commit, then run `{own_path()} ready {s.ref(t)} --folder "
                                  f"{shlex.quote(s.loc['folder'])}` again."), None)
            if sent:
                told.append(rn["round"])
        msg = f"{s.ref(t)}: findings sent to {sent}." if sent else f"{s.ref(t)}: review round {rn['round']} asked for changes."
    return msg


def round_code(rn):
    """A sandboxed round's code, which its verdict lines start with: from its round, head and start, so it
    is never in the diff or the store's other words, and no new record field."""
    return "R" + hashlib.sha256(f"{rn['round']}:{rn['head']}:{rn['at']}".encode()).hexdigest()[:8]


def read_verdict(screen, code):
    """(passed, must_fix, findings) from a sandboxed reviewer's screen: its last line holding only
    `code pass` or `code changes N` (0: a pass), the findings above it up to its `code begin` line
    (else its last 80 lines); else None."""
    lines = screen.split("\n")
    mark = re.compile(rf"^\W*{code}\s+(?:(begin)|(pass)|changes\s+(\d+))\W*$")
    ends = [(i, m) for i, m in enumerate(map(mark.match, lines)) if m and not m.group(1)]
    if not ends:
        return None
    end, m = ends[-1]
    begin = max((i for i, x in enumerate(lines[:end]) if (y := mark.match(x)) and y.group(1)), default=max(-1, end - 81))
    must = 0 if m.group(2) else int(m.group(3))
    return not must, must, "\n".join(x.strip() for x in lines[begin + 1:end]).strip()


def gate_reads(s, t):
    """Verdicts of sandboxed reviewers (FAC-49), which can't run herdr-task: read with hc read from
    each open round's own terminal (G2: only that run's terminal counts), then that reviewer ends."""
    done = {r["round"] for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task"}
    for rn in list(t.get("review_runs") or []):
        k = (hc_json("get", rn["address"]) or {}) if sandboxes([rn["cli"]]) and rn["round"] not in done else {}
        if k.get("state") != "live" or status_word(k) in ("working", "blocked"):  # read only once its turn is over
            continue
        got = read_verdict(run(["hc", "read", rn["address"], "--lines", "400"]).stdout, round_code(rn))
        if not got:
            continue
        before, dirty = json.loads(json.dumps(t)), s.dirty
        try:
            record_verdict(s, t, rn, *got)
        except Exception:  # nothing half recorded: a later tick tries again, its reviewer still there
            t.clear()
            t.update(before)
            s.dirty = dirty
            if git(t.get("worktree") or "", "rev-parse", "HEAD")[1] in ("", rn["head"]):
                raise
        end_reviewer(rn["address"])  # recorded, or its head moved on: its verdict never applies


def cmd_state(args, state):
    with open_store(args) as s:
        t = s.find(args.id)
        if state == "reopen":
            state = "running" if t.get("started") and t["state"] not in ("queued", "scheduled") else "todo"
            for k in ("reviews", "review", "review_runs", "review_out", "verdict_told", "ci_runs", "ci_seen", "ci_pending", "auto_merge", "merged", "nothing_to_merge", "accepted_at"):
                t.pop(k, None)
        if state == "done" and t.get("worktree") and merged_here(s, t):
            t["merged"] = True
        t["state"] = state
        t[{"done": "done_at", "running": "reopened_at", "todo": "reopened_at"}[state]] = now()
        if state != "done":
            unarchive(s, t)
        for k in ("merge_requested", "attention", "queue", "queued_at", "progress", "retry", "scheduled_for", "held") if state != "running" else ("attention", "scheduled_for"):
            t.pop(k, None)
        s.dirty = True
        v = view(s, t)
    say(args, v, f"{s.ref(t)} is done." if state == "done" else f"{s.ref(t)} is {state}.")


def cmd_unarchive(args):
    with open_store(args) as s:
        t = s.find(args.id)
        was = bool(t.get("archived"))
        unarchive(s, t)
        v = view(s, t)
    say(args, v, f"{s.ref(t)} is back in its list." if was else f"{s.ref(t)} isn't archived.")


def cmd_edit(args):
    with open_store(args) as s:
        t = s.find(args.id)
        old = t["title"]
        if args.title is not None:
            t["title"] = args.title.strip() or t["title"]
        if args.prompt is not None:
            t["prompt"] = args.prompt
        if args.prompt_file:
            with open(args.prompt_file) as f:
                t["prompt"] = f.read()
        pick = [t.get(k) for k in ("agent", "model", "effort")]
        set_opts(t, args)
        if [t.get(k) for k in ("agent", "model", "effort")] != pick:
            # The next agent started for it runs on the new choice (start_again), a queued one too.
            t["pick_at"] = now()
            q = t.get("queue") or {}
            if q.get("argv") and not q.get("custom"):
                q["argv"] = worker_argv(s, t, q.get("space"))
        set_base(s, t, args.base)
        set_notes(t, args)
        if args.after is not None:
            set_after(s, t, *parse_after(s, args.after))
        at = scheduled_at(args) if (args.at or "").strip() or args.in_ is not None else None
        if at:
            schedule(s, t, at)
        elif args.at is not None:
            unschedule(t)  # --at "": back to To do
        if args.here or args.worktree:
            if t["state"] not in ("todo", "scheduled", "queued"):
                raise Fail("Where a task works can only change before it starts.")
            if args.worktree and s.loc["kind"] != "repo":
                raise Fail("Worktrees need a git repo.")
            t["mode"] = "here" if args.here else "worktree"
        if t["mode"] != "worktree" and not t.get("worktree"):
            t.pop("branch", None)
        set_branch(s, t, getattr(args, "branch", None))
        s.dirty = True
        tabs = task_tabs(s, t) if t["title"] != old else []
        v = view(s, t)
    left = name_tabs([(tab, tab_title(s, t)) for tab in tabs])
    if at:
        ensure_scheduler_quietly()
    say(args, {**v, "rename": left}, f"{s.ref(t)} saved" + (f": {v['why']}." if v["status"] == "scheduled" else "."))


def cmd_config(args):
    with open_store(args) as s:
        cfg = s.data.setdefault("config", {})
        tabs = False
        if args.prefix is not None:
            new = args.prefix.strip().upper()
            if not re.fullmatch(r"[A-Z]{2,5}", new):
                raise Fail("A prefix is 2 to 5 letters, like HAT.")
            owner = prefixes_in_use(s.path).get(new)
            if owner:
                raise Fail(f"{new} is taken by {owner}.")
            if new != s.prefix:
                s.data["prefix"] = new
                s.dirty = tabs = True
        out = {"folder": s.loc["folder"], "prefix": s.prefix, "max_running": s.max_running(), "settings": settings_for(s),
               "worktrees": worktrees_on(s)}
    out["rename"] = retag_tabs(s) if tabs else []
    n = out["max_running"]
    where = "a worktree each" if out["worktrees"] else "in the folder itself"
    say(args, out, f"{out['folder']}: tasks {out['prefix']}-n, at most {n} at once, {where}.")


def cmd_rm(args):
    with open_store(args) as s:
        t = s.find(args.id)
        s.data["tasks"].remove(t)
        for o in s.data["tasks"]:
            if t["id"] in (o.get("after") or []):
                o["after"] = [i for i in o["after"] if i != t["id"]]
        s.dirty = True
    note = f" Its worktree {t['worktree']} is still there." if t.get("worktree") and os.path.isdir(t["worktree"]) else ""
    say(args, {"id": t["id"], "removed": True}, f"{s.ref(t)} removed.{note}")


def last_agent(t):
    return t["agents"][-1] if t["agents"] else None


def continue_line(s, t):
    done = "finished and committed" if t.get("mode") == "worktree" else "finished"
    folder = (getattr(s, "loc", None) or {}).get("folder")
    where = f" --folder {shlex.quote(folder)}" if folder else ""
    return (f"Herdr resumed you: carry on with {s.ref(t)} where you left off; run "
            f"`{own_path()} ready {s.ref(t)}{where}` when it's {done}.")


def start_again(s, t, space, key=None, argv_json=None, message=None, parent=None, resuming=False):
    """Starts an agent for the task: the last agent's own session when it's the same kind of agent
    and the session is known; else a fresh one with a handoff. `message` goes after either; `resuming`
    (`herdr-task resume`) rebuilds argv from the current settings and wakes a resumed session."""
    unarchive(s, t)
    prev = last_agent(t)
    parent = parent_for(parent or (prev or {}).get("parent"), space)
    # The last agent's argv again (switch, tell), unless `resuming` or edit changed its pick.
    keep = not resuming and prev and prev.get("argv") and (t.get("pick_at") or 0) <= (prev.get("at") or 0)
    argv = agent_argv(None, argv_json) if argv_json else worker_argv(s, t, space, key) if key else \
        (prev["argv"] if keep else worker_argv(s, t, space))
    same = prev and agent_key(argv) == prev.get("agent")
    res = resume_argv(argv, prev["session"]) if same and prev.get("session") else None
    if res:
        msg = message or (continue_line(s, t) if resuming else None)
        a = launch(s, t, argv, space, msg, parent=parent, resume=res)
        a["session"] = prev["session"]  # the same conversation from the start (agent_log, before a pass sees its pid)
        return a, True
    text = handoff(s, t, prev, parent, argv, space)
    extra = message or (continue_line(s, t) if resuming and f"ready {s.ref(t)}" not in text else None)
    if extra:
        text += "\n\n" + extra
    t["review_gate"] = True
    return launch(s, t, argv, space, text, parent=parent), False


def cmd_resume(args):
    with open_store(args) as s:
        t = s.find(args.id)
        live = [k for k in map(detail, live_agents(s.key(t))) if agent_running(k)]
        if live:
            raise Fail(f"{s.ref(t)} still has an agent ({live[0]['address']}). Use switch to replace it.")
        if t["state"] in ("todo", "scheduled", "queued"):
            raise Fail(f"{s.ref(t)} hasn't started. Use start.")
        space = bare(args.space) if args.space else (t.get("space") or default_space())
        if t["state"] == "done":
            t["state"] = "running"
        back_to_work(s, t, "resumed")
        a, resumed = start_again(s, t, space, args.agent, args.argv, parent=args.parent, resuming=True)
        v = view(s, t)
    say(args, {**v, "terminal": a["address"], "space": space, "parent": a.get("parent"), "resumed": resumed},
        f"{s.ref(t)} {'resumed its session' if resumed else 'started with a handoff'}: {a['address']}")


def cmd_switch(args):
    with open_store(args) as s:
        t = s.find(args.id)
        live = [k for k in map(detail, live_agents(s.key(t))) if agent_running(k)]
        refresh_sessions(s, t, live)
        old = live[-1] if live else None
        labels = (old or {}).get("labels") or {}
        space = (labels.get("herdr.task.space") or labels.get("herdr.space") or (bare(args.space) if args.space else None)
                 or t.get("space") or default_space())
        if t["state"] in ("todo", "scheduled", "queued", "done"):
            t["state"] = "running"
            t.setdefault("started", now())
            t.pop("queue", None)
            t.pop("scheduled_for", None)
        a, resumed = start_again(s, t, space, args.agent, args.argv, parent=args.parent)
        v = view(s, t)
    ended = []
    if old and not args.keep:
        # The new agent took over: end the old one (its terminal, shell and all).
        run(["hc", "kill", old["address"]])
        ended.append(old["address"])
    say(args, {**v, "terminal": a["address"], "space": space, "parent": a.get("parent"), "resumed": resumed, "ended": ended},
        f"{s.ref(t)}: {a['agent']} {a['address']} {'resumed its session' if resumed else 'took over'}" + (f", {old['address']} ended" if ended else ""))


def workers(s, t):
    """The task's agents at work, reviewers left out (hc get's records, oldest first)."""
    return [k for k in map(detail, live_agents(s.key(t)))
            if agent_running(k) and (k.get("labels") or {}).get("herdr.role") != "reviewer"]


def tell(s, t, msg, args, start=True):
    """Sends the task's worker a message, or starts one with it when none is left (when `start`).
    Returns (terminal, started)."""
    unarchive(s, t)
    live = workers(s, t)
    if live:
        # While the person writes to it, the message waits for them (news.py's held_pass).
        from .news import send_or_hold
        if not send_or_hold(live[-1]["address"], msg):
            raise Fail(f"hc send to {live[-1]['address']} failed")
        return live[-1]["address"], False
    if not start:
        return None, False
    space = bare(args.space) if getattr(args, "space", None) else (t.get("space") or default_space())
    a, _ = start_again(s, t, space, getattr(args, "agent", None), getattr(args, "argv", None), message=msg)
    return a["address"], True


def local_merge_msg(s, t):
    main, wt, br, base = s.loc["folder"], t["worktree"], t["branch"], t.get("base") or "main"
    q = shlex.quote
    return (f"Merge task {s.ref(t)} \"{t['title']}\" now:\n"
            f"1. In {wt}: commit any remaining work, then rebase {br} onto {base} (git rebase {base}) and resolve conflicts.\n"
            f"2. Run the project's checks (tests, build, lint) and fix what fails.\n"
            f"3. Merge into {base}: git -C {q(main)} merge --ff-only {br} when {main} has {base} checked out, else git -C {q(main)} fetch . {br}:{base}. If {main} has uncommitted changes in the way, or another worktree has {base} checked out, stop and tell the person; don't stash or reset their work.\n"
            f"4. Clean up: git -C {q(main)} worktree remove {q(wt)} && git -C {q(main)} branch -d {br}\n"
            f"5. Run: herdr-task done {s.ref(t)} --folder {q(main)}")


MERGE_FAILED = "GitHub didn't merge it"  # a merge that failed: never in the way of the next try


def merge_now(s, t):
    """Merges the task's pull request on GitHub (squash) and tidies up here. Returns why it
    couldn't, or None when it's merged (and the task done)."""
    n, repo = str(t["pr"]["number"]), t["pr"]["repo"]
    v = pr_view(t)
    s.dirty = True
    if not v:
        return f"Couldn't reach pull request #{n} on GitHub."
    head = t.get("head")
    # A commit made here after ready was never pushed: merging would delete it with the branch.
    if head and branch_tip(s, t) != head:
        return f"{t.get('branch')} has moved on since it said ready: its agent says ready again first."
    if v.get("state") != "MERGED":
        record_ci(s, t, v, now())
        if t.get("mode") == "worktree" and budget_pass(s, t):
            return t["recap"]["title"]
        problem = pr_problem(v)
        if problem:
            t["attention"], t["attention_at"] = problem, now()
            return problem
        if pr_pending(v):
            return "Checks are still running."
        ok, _, err = gh("pr", "merge", n, "--repo", repo, "--squash", "--delete-branch",
                        "--match-head-commit", head or v.get("headRefOid") or "")
        # A one-off GitHub error can come after the merge went through: asked again.
        again = None if ok else pr_view(t)
        if not ok and (again or {}).get("state") == "MERGED":
            v = again
        elif not ok:
            t["attention"], t["attention_at"] = f"{MERGE_FAILED}: {err}", now()
            # The next tick reads the pull request at once (merged by hand meanwhile: done).
            t["pr"]["checked"] = 0
            return t["attention"]
    watch_main(s, t, ((v.get("mergeCommit") or {}).get("oid")) if v.get("state") == "MERGED" else None)
    update_main(s, t)
    t["merged"] = True
    finish(s, t, even_if_busy=True)
    return None


def cmd_merge(args):
    error = None
    with open_store(args) as s:
        t = s.find(args.id)
        if t.get("mode") != "worktree" or not t.get("worktree"):
            raise Fail(f"{s.ref(t)} works in the folder itself; there is nothing to merge.")
        sent, started, how = None, False, "agent"
        if t.get("pr"):
            # On GitHub: Herdr merges the pull request itself (squash), then tidies up here.
            how = "github"
            error = merge_now(s, t)
            if error == "Checks are still running.":
                error += " Merge again when they pass."
        else:
            t["merge_requested"] = now()
            s.dirty = True
            sent, started = tell(s, t, local_merge_msg(s, t), args)
        v = view(s, t)
    if error:
        say(args, {**v, "error": error}, None) if args.json else print(f"herdr-task: {error}", file=sys.stderr)
        sys.exit(1)
    text = f"{s.ref(t)}: pull request #{t['pr']['number']} merged; done." if how == "github" else \
        f"{s.ref(t)}: merge asked of {sent}" + (" (a new agent)" if started else "")
    say(args, {**v, "terminal": sent, "new_agent": started, "merged": how == "github"}, text)


def branch_tip(s, t):
    """The task branch's last commit here (else the one it said ready at), or None."""
    ok, tip, _ = git(s.loc["folder"], "rev-parse", "--verify", "--quiet", f"refs/heads/{t.get('branch')}")
    return (tip if ok and tip else None) or t.get("head") or None


def in_base(s, t):
    """Its branch holds nothing its base branch doesn't: no commits of its own, or it only caught
    up with its base (`git merge --ff-only main`, SHEP-23). Nothing to merge, and never merged."""
    tip = branch_tip(s, t)
    return bool(tip) and (tip == t.get("start") or git(s.loc["folder"], "merge-base", "--is-ancestor", tip, t.get("base") or "main")[0])


def ff_merge(s, t):
    """Lands a task's branch in its base branch here when that is a fast-forward (the base is
    checked out in the main folder and the branch already holds it: exactly the tested work, no
    new combination). Returns whether it did; git refuses to touch uncommitted files."""
    main, br, base = s.loc["folder"], t.get("branch"), t.get("base") or "main"
    ok, cur, _ = git(main, "symbolic-ref", "--short", "HEAD")
    if not (br and git(main, "merge-base", "--is-ancestor", base, br)[0]):
        return False
    if ok and cur == base:
        return git(main, "merge", "--ff-only", br)[0]
    # A base not checked out here (a task's own --base): its branch moves forward to the task's
    # (git refuses unless it's a fast-forward, or when another worktree has it checked out).
    return git(main, "fetch", ".", f"{br}:{base}")[0]


def cmd_accept(args):
    """The person's Accept on a Ready task (the one thing they do in the Tasks panel): Done at
    once when there is nothing to merge, else merged first, as the merge settings say how: a pull
    request is squash-merged on GitHub (or merges once its checks pass); a local branch is
    fast-forwarded when it can be, else its agent merges it (rebase, checks) and marks it done."""
    error, how, sent, started, back = None, "done", None, False, False
    with open_store(args) as s:
        t = s.find(args.id)
        if t["state"] != "ready":
            raise Fail(f"{s.ref(t)} isn't ready ({t['state']}); only a Ready task can be accepted.")
        # Its agent at work again since it said ready: never accepted half done. Working again
        # (saved), and refused after the store is.
        if busy_again(t, [detail(k) for k in live_agents(s.key(t))]):
            back = back_to_work(s, t, "its agent is at work again")
        # One its agent opened itself, which ready couldn't open again ("already exists"): adopted.
        elif (not t.get("pr") and "already exists" in (t.get("attention") or "")
              and (repo := github(s)) and pr_adopt(t, repo)):
            t.pop("attention", None)
            s.dirty = True
        # A pull request's problems are read fresh (merge_now()): one fixed since, or a merge that
        # failed once, doesn't stand in the way. Without one, only a failed try doesn't.
        elif t.get("attention") and not t.get("pr") and not t["attention"].startswith(("Couldn't ask for the merge", MERGE_FAILED)):
            raise Fail(f"{s.ref(t)} needs attention first: {t['attention']}")
    if back:
        raise Fail(f"{s.ref(t)} is working again: its agent took up more work after it said ready. Accept it once it says ready again.")
    with open_store(args) as s:
        t = s.find(args.id)
        if t["state"] != "ready":
            raise Fail(f"{s.ref(t)} isn't ready ({t['state']}); only a Ready task can be accepted.")
        t.pop("attention", None)
        wt = t.get("worktree") if t.get("mode") == "worktree" else None
        t["accepted_at"] = now()
        s.dirty = True
        if not wt:
            # Worked in the folder itself: nothing to merge.
            finish(s, t)
        elif t.get("pr"):
            how = "github"
            error = merge_now(s, t)
            if error == "Checks are still running.":
                # Accepted: it merges by itself once they pass (Merging).
                t["auto_merge"], how, error = "checks", "checks", None
        elif merged_here(s, t) or in_base(s, t):
            # Its own work already in the base branch, or none of its own: nothing to merge.
            t["merged"] = merged_here(s, t)
            if not t["merged"]:
                t["nothing_to_merge"] = True
            finish(s, t, even_if_busy=True)
        elif ff_merge(s, t):
            how = "merged"
            t["merged"] = True
            finish(s, t, even_if_busy=True)
        else:
            how = "agent"
            t["merge_requested"] = now()
            sent, started = tell(s, t, local_merge_msg(s, t), args)
        v = view(s, t)
    if error:
        say(args, {**v, "error": error}, None) if args.json else print(f"herdr-task: {error}", file=sys.stderr)
        sys.exit(1)
    text = {"done": f"{s.ref(t)} accepted: done.",
            "merged": f"{s.ref(t)} accepted: merged into {t.get('base') or 'main'}; done.",
            "github": f"{s.ref(t)} accepted: pull request #{(t.get('pr') or {}).get('number')} merged; done.",
            "checks": f"{s.ref(t)} accepted: it merges once its checks pass.",
            "agent": f"{s.ref(t)} accepted: merge asked of {sent}" + (" (a new agent)" if started else "")}[how]
    say(args, {**v, "accepted": how, "terminal": sent, "new_agent": started}, text)


def cmd_fix(args):
    with open_store(args) as s:
        t = s.find(args.id)
        if t.get("mode") != "worktree" or not t.get("worktree"):
            raise Fail(f"{s.ref(t)} works in the folder itself; tell its agent what to fix.")
        problem = args.reason or t.get("attention") or "The pull request can't merge yet."
        main, wt, br, base = s.loc["folder"], t["worktree"], t["branch"], t.get("base") or "main"
        msg = (f"Task {s.ref(t)} \"{t['title']}\" can't merge yet: {problem}\n"
               f"Fix it in {wt}: for conflicts, rebase {br} onto {base}; for failing checks, find out why and fix them. "
               f"Run the checks, commit, then run `herdr-task ready {t['id']} --folder {shlex.quote(main)}` to push the fix.")
        # Handed the problem: someone is on it while its agent starts (on_it()).
        t["fix_requested"] = t["handed_at"] = now()
        s.dirty = True
        sent, started = tell(s, t, msg, args)
        back_to_work(s, t, f"asked to fix: {problem}", f"Fixing: {problem}")
        v = view(s, t)
    say(args, {**v, "terminal": sent, "new_agent": started},
        f"{s.ref(t)}: asked {sent} to fix it" + (" (a new agent)" if started else ""))
