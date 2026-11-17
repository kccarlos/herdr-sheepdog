"""The command line, and running herdr-task on another machine (--on)."""

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

from . import link
from .base import bundle, Fail, guard, hc_json, run, STORE, VERSION
from .agents import LINE_MAX, SUMMARY_MAX
from .launch import cmd_allow, is_coordinator
from .commands import (
    cmd_accept, cmd_add, cmd_alert, cmd_config, cmd_edit, cmd_fix, cmd_list, cmd_merge, cmd_ready,
    cmd_resume, cmd_review, cmd_review_more, cmd_reviewed, cmd_rm, cmd_show, cmd_start, cmd_state, cmd_status,
    cmd_switch, cmd_unarchive, cmd_where,
)
from .tick import cmd_tick
from .automations import auto_list, cmd_auto, cmd_scheduler, cmd_track, EVENTS, HOW
from .machines import cmd_assistants, cmd_machines, cmd_sync_auto, cmd_tell
from .autopilot import cmd_autopilot, cmd_complete, cmd_progress
from .ask import cmd_ask, cmd_ask_pane
from .budget import cmd_recap, DECISIONS
from .project import cmd_project
from .simulators import cmd_simulator
from .devices import cmd_device_wait
from .sandbox import cmd_sandbox


# ---- another machine ------------------------------------------------------------------------

def install_secret(machine):
    """herdr-secret goes along to another machine (its `install --on`: no values, only the
    command), so agents there can ask for and use secrets. Best effort, again when it changed."""
    path = shutil.which("herdr-secret")
    if not path:
        return
    with open(os.path.realpath(path), "rb") as f:
        digest = hashlib.sha1(f.read()).hexdigest()
    mark = os.path.join(STORE, ".remote", f"{machine}.secret")
    try:
        with open(mark) as f:
            if f.read().strip() == digest:
                return
    except OSError:
        pass
    r = subprocess.run([path, "install", "--on", machine], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode == 0:
        os.makedirs(os.path.dirname(mark), exist_ok=True)
        with open(mark, "w") as f:
            f.write(digest)


def remote_run(machine, argv, timeout=None):
    """herdr-task on another machine (installed there first when it isn't this one): its run,
    output kept. timeout: seconds before herdr-core gives up on it."""
    body = bundle()
    digest = hashlib.sha1(body).hexdigest()
    mark = os.path.join(STORE, ".remote", machine)
    target = ".local/bin/herdr-task"
    try:
        with open(mark) as f:
            # "source": it holds herdr-task's source, and runs its own
            installed = f.read().strip() in (digest, "source")
    except OSError:
        installed = False

    def install():
        guard(["hc"])
        # A machine whose ~/.local/bin/herdr-task is a symlink (its own unpacked or checkout install)
        # runs its own copy and is never overwritten (machines.py INSTALL, the same rule).
        there = run(["hc", "exec", "--on", machine, "--", "test", "-L", target])
        if link.lost(there):
            raise Fail(f"Can't reach {machine}: its link is down, and nothing was run there. Try again once it's back.")
        if there.returncode == 0:
            os.makedirs(os.path.dirname(mark), exist_ok=True)
            with open(mark, "w") as f:
                f.write("source")
            return
        r = subprocess.run(["hc", "file", "write", "--on", machine, target], input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if r.returncode != 0:
            raise Fail(f"Couldn't install herdr-task on {machine}: {r.stderr.decode().strip()}")
        run(["hc", "exec", "--on", machine, "--", "chmod", "+x", target], check=True)
        os.makedirs(os.path.dirname(mark), exist_ok=True)
        with open(mark, "w") as f:
            f.write(digest)

    if not installed:
        install()
    install_secret(machine)
    if argv[:1] == ["_job"]:
        # an action that was still running there (link.PENDING): its result, waiting again
        return link.follow(machine, argv[1] if len(argv) > 1 else "", argv, time.monotonic() + link.wait_s())
    if argv[:1] and argv[0] in link.ACTIONS:
        # the person's action: runs there whole even when the link drops, its result read back (SHEP-227, #1055)
        return link.action(machine, argv, install)
    wait = ["--timeout", str(timeout)] if timeout else []
    by = "HERDR_TASK_BY=assistant " if argv[:1] == ["accept"] and (me := os.environ.get("HC_TERMINAL_ID")) and is_coordinator(hc_json("get", me) or {}) else ""
    call = ["hc", "exec", "--on", machine, *wait, "--", "sh", "-c", f'{by}exec "$HOME/.local/bin/herdr-task" "$@"', "herdr-task", *argv]
    r = link.again(machine, argv, lambda: run(call)) if argv[:1] and argv[0] in link.READS else run(call)
    if r.returncode == 127 or "not found" in r.stderr and not r.stdout:
        install()
        r = run(call)
    return r


def remote(machine, argv):
    """Runs herdr-task on the machine that holds the folder (installing it there first)."""
    if argv[:1] == ["tick"]:
        # Its start limits live in Settings here, where the window is (HERD-40).
        from .settings import machine_limits
        argv = [*argv, "--machine", machine, "--hold", json.dumps(machine_limits(machine))]
    r = remote_run(machine, argv)
    sys.stderr.write(r.stderr)
    out = r.stdout
    if r.returncode == 0 and "--json" in argv:
        try:
            d = json.loads(out)
        except ValueError:
            d = None
        if isinstance(d, dict):
            if isinstance(d.get("terminal"), str) and "/" not in d["terminal"]:
                d["terminal"] = f"{machine}/{d['terminal']}"
            out = json.dumps(d) + "\n"
        # An automation's view (auto list, show, add...) is that machine's: it says so (SHEP-43).
        if argv[:1] == ["auto"] and argv[1:2] != ["feed"]:
            views = d if isinstance(d, list) else [d] if isinstance(d, dict) else []
            for v in views:
                if isinstance(v, dict) and "ref" in v and v.get("machine") is None:
                    v["machine"] = machine
            if views:
                out = json.dumps(d) + "\n"
    sys.stdout.write(out)
    return r.returncode


# ---- cli ------------------------------------------------------------------------------------

def main():
    argv = sys.argv[1:]
    if argv[:1] == ["--on"] and len(argv) > 1:
        try:
            return remote(argv[1], argv[2:])
        except Fail as e:
            if "--json" in argv:
                print(json.dumps({"error": str(e)}))
            else:
                print(f"herdr-task: {e}", file=sys.stderr)
            return 1
    if argv[:1] == ["--version"]:
        print(VERSION)
        return 0

    p = argparse.ArgumentParser(prog="herdr-task", description="Herdr's tasks: add, start and finish work for a folder.",
                                epilog="Run on another machine's folder: herdr-task --on <machine> <command> ...")
    sub = p.add_subparsers(dest="cmd")

    def cmd(name, fn, help, id=True):
        c = sub.add_parser(name, help=help)
        if id:
            c.add_argument("id", help="task id (4, #4 or its key)")
        c.add_argument("--folder", help="the task folder (default: here)")
        c.add_argument("--json", action="store_true")
        c.set_defaults(fn=fn)
        return c

    def notes_opts(c):
        c.add_argument("--start-notes", help="this task's own \"When a task starts\" notes, replacing the folder's (\"\": the folder's)")
        c.add_argument("--finish-notes", help="this task's own \"When a task finishes\" notes, replacing the folder's (\"\": the folder's)")

    def task_opts(c):
        c.add_argument("--agent", help="the CLI its agent runs (claude, codex, ...; default: Settings' Workers)")
        c.add_argument("--model", help="its agent's model, an id or alias (default: Settings'; \"default\": the CLI's own)")
        c.add_argument("--effort", help="its agent's effort: low, medium, high, xhigh, max... (default: Settings')")
        c.add_argument("--reviewer", help="the CLI its agent's review uses (default: Settings' Reviewer, else the other CLI)")
        c.add_argument("--reviewer-model", help="the reviewer's model")
        c.add_argument("--reviewer-effort", help="the reviewer's effort")
        c.add_argument("--set", action="append", metavar="KEY=VALUE",
                       help="one task's setting, a line of its brief's Settings block: review=on|off, review.kind=design|doc, "
                            "review.rounds=1..10, review.mustFix=blocker|major|minor, proof=on|off, priority=urgent|normal, "
                            "merge=\"on its own\"|\"after checks\"|\"ask me first\" (\"\": Settings' again)")
        c.add_argument("--base", help="the branch its worktree branches from and merges into (default: this project's Base branch "
                                      "setting, else the folder's current branch)")
        c.add_argument("--branch", help="its branch's exact name, e.g. a ticket's ABC-2137 (default: the folder's Branch names "
                                        "setting, else task/<n>-<title words>); refused when that branch exists already (\"\": the pattern's)")

    def agent_opts(c):
        c.add_argument("--agent", help="agent key (claude, codex, ...)")
        c.add_argument("--argv", help="how to start it, as a JSON list (default from --agent)")

    def when_opts(c, edit=False):
        # SHEP-38: Scheduled, a one-off start at a time (a repeating one is an automation: auto add --every).
        g = c.add_mutually_exclusive_group()
        g.add_argument("--at", help="start it by itself at this time, this machine's time zone: \"2026-11-06 09:00\", "
                                    "\"tomorrow 9:00\", \"fri 8:15\", \"18:00\"" + (" (\"\" unschedules: back to To do)" if edit else ""))
        g.add_argument("--in", dest="in_", metavar="SPAN", help="start it by itself this long from now: \"6h\", \"90m\", \"2 days\"")

    cmd("where", cmd_where, "the task folder and store for a folder", id=False)
    c = cmd("list", cmd_list, "list tasks", id=False)
    c.add_argument("--all", action="store_true", help="every folder's tasks on this machine")
    c.add_argument("--archived", action="store_true", help="the archived tasks instead (Done ones archive themselves after a week), latest first")
    c = cmd("add", cmd_add, "add a task; prints its id", id=False)
    c.add_argument("title")
    c.add_argument("--prompt")
    c.add_argument("--prompt-file")
    task_opts(c)
    notes_opts(c)
    g = c.add_mutually_exclusive_group()
    g.add_argument("--here", action="store_true", help="work in the folder itself")
    g.add_argument("--worktree", action="store_true", help="work in a git worktree (default in repos)")
    c.add_argument("--after", help="start only once these tasks are done: 3,5; another folder's by its id (WEB-2), another "
                                   "machine's with the machine first (vm-b:HAT-12)")
    when_opts(c)
    c = cmd("show", cmd_show, "one task")
    c.add_argument("--evidence", action="store_true", help="with what its Accept card shows: commits, changed files, checks and its proof")
    c = cmd("start", cmd_start, "start a task's agent (an hc child of the project's coordinator)")
    agent_opts(c)
    c.add_argument("--parent", help="a coordinator's terminal: the agent's hc parent (default: the project's coordinator)")
    c.add_argument("--now", action="store_true", help="skip the queue (a scheduled task: start it now)")
    c.add_argument("--branch", help="its branch's exact name (as add --branch), before its worktree is made")
    g = c.add_mutually_exclusive_group()
    g.add_argument("--here", action="store_true")
    g.add_argument("--worktree", action="store_true")
    when_opts(c)
    c = cmd("ready", cmd_ready, "a worker's task is finished (worktree tasks: committed): it waits in Ready for the person's Accept (opens a pull request on GitHub)")
    c.add_argument("--summary", help=f"what the person now sees or can do, in plain words, at most {SUMMARY_MAX} characters "
                                     "(no file names, flags or jargon); longer is refused")
    c.add_argument("--details", help="optional: a few short bullets in plain words (one \"- \" per line): what changed for the "
                                     "person first, then how it was checked; file names only where they help; never one dense "
                                     "paragraph. Folded under Details in the Accept card")
    c.add_argument("--proof", help="its proof file or folder (Markdown, image, or recipe.json; committed on the branch); default: docs/proof/<REF>.md or proof/<REF>/")
    c.add_argument("--agents-md", help="\"For AGENTS.md\": one line about setting up or testing this repo that the next agent "
                                       "would need and AGENTS.md lacks; the Assistant decides whether to add it")
    c = cmd("reviewed", cmd_reviewed, "a task's agent records one review round of its current commit (Review: on): ready then "
                                      "accepts that commit; uses one of its Review rounds")
    c.add_argument("--report", help="the round's findings in a line (levels, what was fixed)")
    c.add_argument("--commit", help="the commit the round reviewed, when fixes were committed since (default: HEAD)")
    c = cmd("recap", cmd_recap, "the Assistant decides a task's open recap (Recap required): no flags prints it and its RECAP.md")
    c.add_argument("--decide", choices=DECISIONS, help="continue (once per task), split, new (approach) or drop: the last three only record")
    c.add_argument("--small", action="store_true", help="with continue, on a second recap: once, for a small follow-up fix "
                                                        "(at most 1 more review round and 60 more changed lines)")
    c.add_argument("--why", help="why, in one line")
    cmd("review-more", cmd_review_more, "the Assistant gives a task one more review round, once all are used (never its own agent)")
    c = cmd("status", cmd_status, "a task's agent says what it's doing now, at the start, each natural step and at least every 5 minutes: status 4 \"Fixing the two failing tests\" --done 40 --left 5m")
    c.add_argument("text", help=f"what it is doing, at most {LINE_MAX} characters (longer is cut at a word)")
    c.add_argument("--done", type=int, help="how far through the whole task, 0-100 (%%)")
    c.add_argument("--left", help="your best guess of how long is left: 5m, 90s, 1h30m; on every status, the first too (a rough guess beats none)")
    c = cmd("alert", cmd_alert, "tell the person something that can't wait: alert \"Deploy is down\" --body \"why\"", id=False)
    c.add_argument("title", help="what can't wait, a few words")
    c.add_argument("--body", help="why, a line or two")
    c.add_argument("--task", help="the task it's about (an Assistant's alert about one of its tasks always reaches the person)")
    cmd("done", lambda a: cmd_state(a, "done"), "mark done (the person's call: only when they ask; agents finish with ready)")
    c = cmd("accept", cmd_accept, "the person's Accept on a Ready task: merges first when there is something to merge, then done")
    agent_opts(c)
    c.add_argument("--owner-approved", metavar="NOTE", help="Assistant: a task out of review rounds, after the owner's explicit approval (who/where, one line)")
    cmd("reopen", lambda a: cmd_state(a, "reopen"), "undo done or ready; a queued or scheduled task goes back to to do")
    cmd("unarchive", cmd_unarchive, "bring an archived task back into its list (only when the person asks; writing to it does too)")
    c = cmd("edit", cmd_edit, "change a task")
    c.add_argument("--title")
    c.add_argument("--prompt")
    c.add_argument("--prompt-file")
    c.add_argument("--amend", help="add a dated change to the brief (kept beside it; the reviewer judges by both); "
                                   "a running task's agent is told at once")
    task_opts(c)
    notes_opts(c)
    c.add_argument("--after", help="start only once these tasks are done: 3,5, WEB-2, vm-b:HAT-12 (\"\" clears)")
    g = c.add_mutually_exclusive_group()
    g.add_argument("--here", action="store_true")
    g.add_argument("--worktree", action="store_true")
    when_opts(c, edit=True)
    cmd("rm", cmd_rm, "remove a task")
    c = cmd("config", cmd_config, "this folder's settings", id=False)
    c.add_argument("--prefix", help="task ids' prefix, 2 to 5 letters (HAT -> HAT-4); unique on this machine")
    c.add_argument("--build-dir", help="per-task build output folder (<path>/<task-key>/, cleaned on done; \"\" clears)")
    c.add_argument("--set", action="append", metavar="KEY=VALUE",
                   help="tasks.max_running=N, tasks.check=CMD, worker.agent=claude|codex|agy or worker.perm=full|auto|plan for this project folder (\"default\" clears)")
    si = sub.add_parser("simulator", help="this folder's fixed iOS simulator pool: lease one per task, release it, or list holders")
    si.set_defaults(fn=cmd_simulator, simulator="list", folder=None, json=False, id=None, task_opt=None)
    sp = si.add_subparsers(dest="simulator")
    for name, help in (("lease", "lease a free simulator from this folder's pool for a task"),
                       ("release", "release a task's simulator lease and shut the simulator down"),
                       ("list", "show this folder's simulator pool and which task holds each")):
        c = sp.add_parser(name, help=help)
        if name != "list":
            c.add_argument("id", nargs="?", help="task id (default: this worker's own task)")
            c.add_argument("--task", dest="task_opt", help="task id (same as positional id)")
        c.add_argument("--folder", help="the task folder (default: here)")
        c.add_argument("--json", action="store_true")
        c.set_defaults(fn=cmd_simulator)
    c = sub.add_parser("device-wait", help="this task waits on the physical device (locked, untrusted, not connected): Needs you until it's available")
    c.add_argument("why", nargs="?", help='what the person must do, like "unlock and trust the iPhone"')
    c.add_argument("--clear", action="store_true", help="clear the wait by hand")
    c.add_argument("--task", dest="task_opt", help="task id (default: this worker's own task)")
    c.add_argument("--folder", help="the task folder (default: here)")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_device_wait, id=None)
    pr = sub.add_parser("project", help="start a factory project in one command (repo, settings, requirements, coordinator)").add_subparsers(dest="project")
    c = pr.add_parser("start", help="make sure the folder is a git repo, set factory defaults, import requirements, create or find its coordinator")
    c.add_argument("folder", nargs="?", help="the project's folder (default: here)")
    c.add_argument("--folder", dest="folder_opt", help=argparse.SUPPRESS)
    c.add_argument("--name", help="the project name (default: the folder's)")
    c.add_argument("--requirements", help="a file (copied) or URL (saved as a link line; a claude.ai URL is marked to export first) imported into docs/requirements.md")
    c.add_argument("--check", help="the check command the gate runs (default: detected from package.json, Makefile, unittest or xcodebuild)")
    c.add_argument("--autopilot", action="store_true", help="turn Autopilot on for the project, pointing its heartbeat at docs/requirements.md")
    c.add_argument("--dry-run", action="store_true", help="print every step without doing it")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_project)
    c = pr.add_parser("checklist", help="check what an unattended night needs, ask the rest as cards, and save answers with the project")
    c.add_argument("folder", nargs="?", help="the project's folder (default: here)")
    c.add_argument("--folder", dest="folder_opt", help=argparse.SUPPRESS)
    c.add_argument("--dry-run", action="store_true", help="check and print without opening ask cards or writing settings")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_project)
    c = cmd("tick", cmd_tick, "start queued tasks that can go; mark merged ones done (Herdr runs this)", id=False)
    c.add_argument("--machine", help=argparse.SUPPRESS)  # this machine's name, from Herdr's window (HERD-40)
    c.add_argument("--hold", help=argparse.SUPPRESS)  # its start limits as JSON, from Settings > Machines
    c = sub.add_parser("review")  # a verdict from a reviewer herdr-task started (a gate folder's, or an old one)
    c.add_argument("id")
    c.add_argument("--folder")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_review)
    c.add_argument("--pass", dest="passed", action="store_true", help="nothing that must be fixed")
    c.add_argument("--changes", help="what must be fixed, for the worker")
    c.add_argument("--changes-file")
    c.add_argument("--notes", help="suggestions with a pass")
    c.add_argument("--must-fix", type=int, help="findings at or above Must fix (herdr-task's own reviewer, FAC-13)")
    c = cmd("scheduler", cmd_scheduler, "run automations and the queue (herdr-core keeps one as herdr-automations)", id=False)
    c.add_argument("--ensure", action="store_true", help="start it if automations need it and it isn't running")
    c.add_argument("--once", action="store_true", help="one round, then exit")
    au = sub.add_parser("auto", help="automations: a saved task that a time or an event adds again").add_subparsers(dest="auto")

    def auto_cmd(name, help, id=True, fields=False):
        c = au.add_parser(name, help=help)
        if id:
            c.add_argument("id", help="automation id (HAT-A1, A1 or 1)")
        c.add_argument("--folder")
        c.add_argument("--json", action="store_true")
        c.set_defaults(fn=cmd_auto)
        if fields:
            c.add_argument("--prompt")
            c.add_argument("--prompt-file")
            c.add_argument("--every", help=HOW)
            c.add_argument("--on", help="events, comma-separated: " + ", ".join(EVENTS))
            c.add_argument("--match", help="only tasks whose title has these words, or branch:<glob>")
            c.add_argument("--from", dest="from_", help="hear another folder's task events instead of this one's: "
                           "vm-b:~/dev/app (another machine linked to this one) or a folder here (\"\" clears)")
            c.add_argument("--agent")
            c.add_argument("--parent", help="its tasks' agents' parent (default: you)")
            c.add_argument("--set", action="append", metavar="KEY=VALUE",
                           help="a setting for each task it adds, as add --set: review=on|off, review.kind=design|doc, "
                                "review.rounds=1..10, proof=on|off, priority=urgent|normal, merge=..., ... (\"\": Settings' again)")
            g = c.add_mutually_exclusive_group()
            g.add_argument("--here", action="store_true")
            g.add_argument("--worktree", action="store_true")
        return c
    c = auto_cmd("add", "add an automation", id=False, fields=True)
    c.add_argument("title")
    c = auto_cmd("edit", "change an automation", fields=True)
    c.add_argument("--title")
    c = auto_cmd("list", "this folder's automations", id=False)
    c.add_argument("--all", action="store_true", help="every folder's on this machine")
    c.set_defaults(fn=auto_list)
    auto_cmd("show", "one automation")
    auto_cmd("feed", "this folder's tasks as events read them, as JSON (asked by another machine's --from)", id=False)
    auto_cmd("pause", "stop it firing")
    auto_cmd("resume", "let it fire again")
    ap = sub.add_parser("autopilot", help="a project's coordinator woken on a schedule or by events, with the heartbeat "
                        "(Everywhere's schedule, events and heartbeat, unless a project sets its own)").add_subparsers(dest="autopilot")

    def ap_cmd(name, help, everywhere=False):
        c = ap.add_parser(name, help=help)
        c.add_argument("--folder", help="the project folder (default: here)")
        if everywhere:
            c.add_argument("--everywhere", action="store_true", help="Everywhere's: what every project works from unless it sets its own")
        c.add_argument("--name", help=argparse.SUPPRESS)
        c.add_argument("--json", action="store_true")
        c.set_defaults(fn=cmd_autopilot)
        return c
    ap_cmd("show", "this project's Autopilot (or --everywhere): when it wakes and from what heartbeat, its own or Everywhere's, next and last wake, progress", everywhere=True)
    c = ap.add_parser("list", help="Everywhere's schedule and events, and each project's Autopilot")
    c.add_argument("--here", action="store_true", help="only this machine's projects")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_autopilot)
    ap_cmd("on", "turn it on in this project (makes the heartbeat file)")
    ap_cmd("off", "turn it off in this project")
    c = ap_cmd("set", "when this project's Autopilot wakes and from what (--everywhere: every project that doesn't set its own); "
               "kept in settings", everywhere=True)
    c.add_argument("--every", help=HOW + ' "" or off: no schedule; default: follow Everywhere\'s.')
    c.add_argument("--on", help="task events that wake it, comma-separated (\"\": none; default: Everywhere's): " + ", ".join(EVENTS))
    c.add_argument("--heartbeat", help="its heartbeat's words (default: Everywhere's)")
    c.add_argument("--direction", help="one line it steers toward (\"\" or default: none, it works out what matters from the project)")
    c = ap.add_parser("check", help="check a schedule's words without saving them")
    c.add_argument("--every", required=True)
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_autopilot)
    ap_cmd("run-now", "wake its coordinator now (queued until it is idle)")
    ap_cmd("heartbeat", "the heartbeat this project works from (--everywhere: Everywhere's), and Everywhere's file from before (made with a starter when missing)", everywhere=True)
    c = sub.add_parser("progress", help="a coordinator on Autopilot saves its progress and next step: progress \"Started HAT-4; next: check its review\"")
    c.add_argument("text")
    c.add_argument("--folder", help="the project folder (default: here)")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_progress)
    c = sub.add_parser("complete", help="a coordinator on Autopilot proved its heartbeat's stopping goal: turns Autopilot off")
    c.add_argument("evidence")
    c.add_argument("--folder", help="the project folder (default: here)")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_complete)
    auto_cmd("rm", "remove it")
    auto_cmd("run-now", "add and start its task now")
    ma = sub.add_parser("machines", help="other machines: keep the task tools in step there (sync), and what each machine reaches (reach)").add_subparsers(dest="machines")
    c = ma.add_parser("sync", help="install or update herdr-task, its helpers and the role templates on every connected machine (current ones are skipped)")
    c.add_argument("machine", nargs="*", help="only these machines (default: every connected one)")
    c.add_argument("--check", action="store_true", help="only report each machine: current, out of date (and what), or can't reach; exits 1 unless all are current")
    c.add_argument("--force", action="store_true", help="also replace a template someone changed there, or a newer copy")
    c.add_argument("--auto", action="store_true", help=argparse.SUPPRESS)
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=lambda a: cmd_sync_auto(a.machine) if a.auto else cmd_machines(a))
    c = ma.add_parser("reach", help="the reach map: the machines this one links to over herdr-core, and through each, what that one links to (contacts nothing else)")
    c.add_argument("--refresh", action="store_true", help="check every link now (default: the scheduler's map, at most 10 minutes old)")
    c.add_argument("--quiet", action="store_true", help=argparse.SUPPRESS)
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_machines)
    c = ma.add_parser("find", help="Find machines: list the ssh config's hosts and online Tailscale peers not linked yet (contacts none); --go tries each once")
    c.add_argument("--go", action="store_true", help="try each listed host once now (ssh, batch mode); at most once every 10 minutes")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_machines)
    c = sub.add_parser("assistants", help="every live coordinator on every machine reachable from here: its address (machine/terminal) and project folder")
    c.add_argument("--refresh", action="store_true", help="look again now")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_assistants)
    c = sub.add_parser("tell-assistant", help="message another coordinator, here or on a machine reachable from here: tell-assistant vm-demo/t3 \"...\" (or its project folder or name)")
    c.add_argument("target", help="its address as `assistants` lists it (t3, vm-demo/t3, laptop>vm-x/t3), or its project folder or name")
    c.add_argument("text")
    c.add_argument("--json", action="store_true")
    c.set_defaults(fn=cmd_tell)
    c = cmd("fix", cmd_fix, "ask the task's agent to fix what keeps its pull request from merging")
    agent_opts(c)
    c.add_argument("--reason", help="what to fix (default: what Herdr found)")
    c = cmd("merge", cmd_merge, "ask the task's agent to merge it (the Merge button)")
    agent_opts(c)
    c = cmd("resume", cmd_resume, "continue a stopped task")
    agent_opts(c)
    c.add_argument("--parent")
    c = cmd("switch", cmd_switch, "replace the task's agent (no --agent: restart it); --all --from claude --agent agy: "
            "every task of the folder on that agent", id=False)
    c.add_argument("id", nargs="?", help="task id (4, #4 or its key)")
    agent_opts(c)
    c.add_argument("--model", help="the model it runs (saved on the task, as edit --model)")
    c.add_argument("--all", action="store_true", help="every task of the folder not done whose agent matches --from")
    c.add_argument("--from", dest="from_", metavar="CLI[:MODEL]", help="with --all: the agent (and model) to move off")
    c.add_argument("--dry-run", action="store_true", help="with --all: list what would change, change nothing")
    c.add_argument("--reviewer", action="store_true", help="with --all: tasks whose reviewer matches --from move it too")
    c.add_argument("--parent")
    c.add_argument("--keep", action="store_true", help="add the agent beside the old one")
    c = cmd("ask", cmd_ask, "ask the person without blocking: ask \"<question>\" --option A --option B --recommend A starts an "
            "ask pane; the answer comes to you as news (--show <id>: an ask and its answer)", id=False)
    c.add_argument("question", nargs="?")
    c.add_argument("--option", action="append", help="an answer to pick by number (repeatable)")
    c.add_argument("--recommend", help="the option you recommend")
    c.add_argument("--task", help="the task it is about (FAC-3)")
    c.add_argument("--kind", choices=("release", "paid-key", "delete", "install", "reset", "owner"),
                   help="an ask only the owner answers (FAC-99): its task never auto-accepts while it is open")
    c.add_argument("--show", metavar="ID")
    c = sub.add_parser("_ask-pane")  # the ask pane's program (ask.py)
    c.add_argument("id")
    c.set_defaults(fn=cmd_ask_pane, json=False)
    c = sub.add_parser("_allow")  # Codex's PermissionRequest hook (own_argv)
    c.add_argument("--command", required=True)
    c.add_argument("--sandboxed", action="store_true")  # Codex's: its proof and commits run escalated (#866)
    c.set_defaults(fn=cmd_allow, json=False)
    c = cmd("sandbox", cmd_sandbox, "whether Codex's sandbox (bwrap) can start on this machine, and the fix for it when it can't "
            "(asked once and kept; --check asks again)", id=False)
    c.add_argument("--check", action="store_true", help="ask again now")
    c = sub.add_parser("_track")
    c.add_argument("id")
    c.add_argument("address")
    c.add_argument("--folder")
    c.set_defaults(fn=cmd_track, json=False)

    args = p.parse_args(argv)
    if not getattr(args, "fn", None):
        p.print_help()
        return 1
    try:
        rc = args.fn(args)
    except Fail as e:
        return _refused(args, str(e))
    except OSError as e:
        # #1057: a program it runs (hc, git, gh) that couldn't be started (the system refusing a new process
        # under memory pressure, hc missing or broken) ended Accept with a traceback at its first hc call
        # (busy_again's). Said plainly instead (a store open when it failed is left as it was).
        what = os.path.basename(str(e.filename)) if e.filename else "a program it needs"
        return _refused(args, f"couldn't start {what} ({e.strerror or e}). Try again in a moment.")
    return rc if type(rc) is int else 0


def _refused(args, why):
    if getattr(args, "json", False):
        print(json.dumps({"error": why}))
    else:
        print(f"herdr-task: {why}", file=sys.stderr)
    return 1
