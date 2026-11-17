"""Shared ground: paths, Fail, the clock, this herdr-task's own files, and running programs
(hc, herdr-app, git) only in the real environment."""

import io
import json
import os
import pwd
import signal
import subprocess
import time
import zipfile


from . import layout

VERSION = "1"
HOME = os.path.expanduser("~")
# Shepherdr's own folders (layout.py): ~/.local/share/shepherdr/tasks once this machine's data moved
# there, else the old ~/.herdr/tasks (the entry moves it first, so a run sees one or the other).
WHERE = layout.Where()
# HERDR_TASK_STORE: another task list (a test's own), as HERDR_TASK_SETTINGS for settings.
STORE = os.environ.get("HERDR_TASK_STORE") or WHERE.path("tasks")
# Where new task worktrees go (a task keeps the one it has; Settings > Tasks > Worktree folder picks another).
WORKTREES = WHERE.worktrees()


def beside_store(kind, name):
    """A herdr-task file kept beside the task lists: its own place in Shepherdr's folders, or beside a
    test's own HERDR_TASK_STORE."""
    if os.environ.get("HERDR_TASK_STORE"):
        return os.path.join(os.path.dirname(STORE.rstrip("/")), name)
    return WHERE.path(kind)
LEGACY = os.path.join(HOME, ".config", "herdr-app", "data", "tasks")
SHELLS = {"bash", "zsh", "fish", "sh", "dash", "ksh", "tcsh", "csh", "nu", "xonsh", "pwsh", "powershell", "elvish"}
RANK = {"blocked": 4, "done": 3, "working": 2, "idle": 1}
MAX_RUNNING = 10
GH = os.environ.get("HERDR_TASK_GH") or "gh"
# How often tick asks GitHub about a pull request (ms).
PR_EVERY = 60000


class Fail(Exception):
    pass


# Shepherdr's installer (bin/shepherdr) puts this file here while it replaces helpers or templates,
# and removes it once `shepherdr doctor` finds the install whole. Until then nothing copies a helper
# to another machine: not the sync, not a command's self-copy (cli.remote, herdr-artifact's,
# herdr-secret's install --on), so a half-done or wrong copy never reaches every machine.
# It is in Shepherdr's state folder; an older installer's is at ~/.herdr/sync-hold: either holds.
SYNC_HOLD = os.path.join(WHERE.roots["state"], "sync-hold")
SYNC_HOLDS = (SYNC_HOLD, os.path.join(HOME, ".herdr", "sync-hold"))


def sync_held():
    """Why copies to other machines wait now (the hold's first line), or None."""
    for p in SYNC_HOLDS:
        try:
            with open(p) as f:
                return f.readline().strip() or "held by Shepherdr's installer"
        except OSError:
            continue
    return None


# Shepherdr's add-ons (lib/addons.json): one the person switched off in Shepherdr settings >
# Window is in Everywhere's "off" (the settings file, or HERDR_TASK_SETTINGS), and its
# background work here stops (autopilot, browsers, the sync to other machines).
def addon_off(name):
    path = os.environ.get("HERDR_TASK_SETTINGS") or WHERE.path("settings")
    try:
        with open(path) as f:
            off = (json.load(f).get("everywhere") or {}).get("off")
    except (OSError, ValueError, AttributeError):
        return False
    return isinstance(off, list) and name in off


def now():
    return int(time.time() * 1000)


# ---- the stop log (SHEP-189) ----------------------------------------------------------------------
# Every time an agent goes (Shepherdr ends it, or its terminal is gone while its task is running),
# one line here: when, what, which terminal, and what Herdr knew then. Kept small: the last
# STOP_LOG_LINES lines. Never raises: logging must not stop the work it records.
STOP_LOG = os.path.join(STORE, ".stops.jsonl")
STOP_LOG_LINES = 500


def stop_note(kind, **fields):
    """Appends {at, kind, ...fields} to the stop log (a JSON line)."""
    try:
        os.makedirs(os.path.dirname(STOP_LOG), exist_ok=True)
        try:
            with open(STOP_LOG) as f:
                lines = f.readlines()
        except OSError:
            lines = []
        lines = lines[-(STOP_LOG_LINES - 1):] + [json.dumps({"at": now(), "kind": kind, **fields}, ensure_ascii=False) + "\n"]
        tmp = f"{STOP_LOG}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            f.writelines(lines)
        os.replace(tmp, STOP_LOG)
    except Exception:  # noqa: BLE001 - a log line never stops the agent work it records
        pass


# ---- the real environment -------------------------------------------------------------------
# With a changed HOME, HERDR_APP_HOME or another herdr-core session (a test's), herdr-app and hc
# could still reach the person's real window and core and overwrite every machine's spaces. So
# herdr-task then refuses to run them at all, with why (HERD-55).
GUARDED = {"hc", "herdr-core", "herdr-app"}


def real_home():
    try:
        return pwd.getpwuid(os.getuid()).pw_dir
    except KeyError:
        return os.path.expanduser("~")


def not_real():
    """Why this environment isn't the account's own, or None when it is."""
    env, home = os.environ, real_home()
    same = lambda a, b: os.path.realpath(a) == os.path.realpath(b)
    if env.get("HOME") and not same(env["HOME"], home):
        return f"HOME is {env['HOME']}, not {home}"
    app = env.get("HERDR_APP_HOME")
    if app and not same(app, os.path.join(home, ".config", "herdr-app")):
        return f"HERDR_APP_HOME is {app}"
    if env.get("HC_SESSION") not in (None, "", "default"):
        return f"HC_SESSION is {env['HC_SESSION']}"
    sock = env.get("HC_SOCKET")
    if sock and not any(same(sock, s) for s in (env.get("HC_TERMINAL_SOCKET"), f"/run/user/{os.getuid()}/herdr-core/default.sock") if s):
        return f"HC_SOCKET is {sock}"
    return None


def guard(args):
    """Refuses herdr-app and hc outside the real environment (see not_real)."""
    name = os.path.basename(args[0])
    why = not_real() if name in GUARDED else None
    if why:
        raise Fail(f"refusing to run {name}: {why}. Run herdr-task with the real HOME, HERDR_APP_HOME, "
                   "HC_SESSION and HC_SOCKET: changed ones can reach the real window and wipe its spaces.")


def run(args, cwd=None, check=False, env=None, timeout=None):
    guard(args)
    r = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, timeout=timeout)
    if check and r.returncode != 0:
        raise Fail((r.stderr or r.stdout).strip() or f"{args[0]} failed")
    return r


def volume_of(path):
    """'/Volumes/Ext' for a path on an external volume, else None."""
    parts = os.path.abspath(path or "").split(os.sep)
    return os.sep.join(parts[:3]) if len(parts) > 2 and parts[1] == "Volumes" and parts[2] else None


def _answers(folder, timeout):
    """Does a quick `git status` (or `ls`) in `folder` answer in time? A stuck child may not die on kill (uninterruptible I/O), so it's never waited for."""
    try:
        p = subprocess.Popen(["sh", "-c", 'git -C "$1" status --porcelain >/dev/null 2>&1 || ls "$1" >/dev/null 2>&1; exit 0', "sh", folder],
                             stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except OSError:
        return True  # can't even start it: not a stalled disk
    try:
        p.wait(timeout=timeout)
        return True
    except subprocess.TimeoutExpired:
        try:
            p.kill()
        except OSError:
            pass
        return False


def probe_volume(repo, timeout=10):
    """None when the repo's folder answers in time, else a short reason (a stalled disk or a waiting macOS permission prompt, FAC-83)."""
    if _answers(repo, timeout):
        return None
    where = volume_of(repo) or repo
    if _answers(os.environ.get("TMPDIR") or "/tmp", timeout):
        return f"{where} did not answer in {timeout}s while $TMPDIR did: the disk is stalling, or a macOS permission prompt may be waiting for a new process to reach it"
    return f"{where} and $TMPDIR did not answer in {timeout}s: this machine is too busy or stuck to start new processes"


def git(cwd, *args, timeout=None):
    r = run(["git", "-C", cwd, *args], timeout=timeout)
    return r.returncode == 0, r.stdout.strip(), r.stderr.strip()


def hc_json(*args):
    r = run(["hc", *args, "--json"])
    if r.returncode != 0:
        return None
    try:
        return json.loads(r.stdout)
    except ValueError:
        return None
GROUP_GRACE_S = 3  # SIGTERM to an agent's process group, then SIGKILL this long after


def agent_pid(address):
    """The pid of the agent in terminal `address` (hc get's foreground), None if unknown."""
    if "/" in address:  # another machine's agent: its pid means nothing here
        return None
    try:
        pid = ((hc_json("get", address) or {}).get("foreground") or {}).get("pid")
    except Fail:
        return None
    return pid if isinstance(pid, int) and pid > 1 else None


def end_group(pid):
    """Best effort: ends the process group `pid` leads (agy's children too), SIGTERM then SIGKILL. Only a group whose leader is `pid` itself, never our own."""
    try:
        if not pid or pid <= 1:
            return
        try:
            led = os.getpgid(pid)
        except ProcessLookupError:
            led = pid  # the leader is already gone; its children may still hold the group
        if led != pid or pid == os.getpgrp():
            return
        os.killpg(pid, signal.SIGTERM)
        for _ in range(GROUP_GRACE_S * 5):
            time.sleep(0.2)
            os.killpg(pid, 0)  # raises once the group is gone
        os.killpg(pid, signal.SIGKILL)
    except (OSError, ValueError):
        pass


def kill_agent(address, runner=None):
    """`hc kill address` (through `runner`, default run), then its process group too, so an agent's children don't orphan."""
    pid = agent_pid(address)
    r = (runner or run)(["hc", "kill", address])
    end_group(pid)
    return r


APP_HOME = os.path.join(HOME, ".config", "herdr-app")
# Where a machine that only runs tasks keeps the chat's question-card hook (mods/chat/ask-hook.sh
# and ask-end.py), synced there beside herdr-task (machines.manifest; launch.ask_hook runs it).
ASK_HOME = os.path.join(".local", "share", "herdr", "ask")
# ---- this herdr-task ------------------------------------------------------------------------
# herdr-task is a thin entry beside this package; on another machine it is one zipapp holding
# both (remote installs it as ~/.local/bin/herdr-task). ENTRY is what runs it again (agents' own
# commands, _track, the scheduler); bundle() is all of it as that one file.
PACKAGE = os.path.dirname(os.path.realpath(__file__))
_OUTER = os.path.dirname(PACKAGE)
ENTRY = _OUTER if os.path.isfile(_OUTER) else os.path.join(_OUTER, "herdr-task")


def bundle():
    """This herdr-task as one runnable file (a zipapp), the same bytes for the same code."""
    if zipfile.is_zipfile(ENTRY):
        with open(ENTRY, "rb") as f:
            return f.read()
    with open(ENTRY, "rb") as f:
        entry = f.read()
    buf = io.BytesIO()
    buf.write(b"#!/usr/bin/env python3\n")
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr(zipfile.ZipInfo("__main__.py", (1980, 1, 1, 0, 0, 0)), entry, zipfile.ZIP_DEFLATED)
        for name in sorted(os.listdir(PACKAGE)):
            if name.endswith(".py") or name == "reviewer.sb":  # its sandbox profile (launch.sandboxed)
                with open(os.path.join(PACKAGE, name), "rb") as f:
                    z.writestr(zipfile.ZipInfo(f"herdr_task/{name}", (1980, 1, 1, 0, 0, 0)), f.read(), zipfile.ZIP_DEFLATED)
    return buf.getvalue()
