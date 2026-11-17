"""Autopilot: a project's coordinator woken like an automation (on a schedule or on task events), each
wake bringing it the project's heartbeat and its own saved progress. No sandbox: it is the same
regular coordinator, with its usual permissions."""

import contextlib
import datetime
import fcntl
import json
import os

from .base import Fail, hc_json, HOME, now, PROJECT_LABEL, STORE, WHERE
from .settings import autopilot_layers, read_settings, write_autopilot
from .agents import agent_running, status_word
from .launch import caller_labels, coordinator_in
from .store import locate, open_lock, read_list, Store, store_files
from .output import say
from .news import news_send, typing
from .automations import EVENTS, events_in, next_run, parse_every, parse_on, trigger_words


# ---- autopilot ------------------------------------------------------------------------------
# How it wakes is kept in settings.json ("autopilot" = {"every", "events", "heartbeat", "direction"}
# in "everywhere" and per project in "projects"[folder]; resolve() is tested on core/autopilot.cases.json).
# Each project's runtime state lives in STORE/.autopilot.json under "projects"[folder] (on by default
# unless "on": False), with pending/inflight/last_wake/wakes/progress/completed/seen.

STATE = os.path.join(STORE, ".autopilot.json")
HEARTBEATS = os.environ.get("HERDR_AUTOPILOT_HOME") or os.path.join(WHERE.roots["config"], "autopilot")
DEFAULT_EVERY = "every 60 minutes"
PENDING_MAX_MS = 6 * 3600000     # a wake that found no Assistant this long: missed
TURN_MAX_MS = 3600000            # a wake's turn not seen ending this long: closed anyway
IDLE_AFTER_MS = 120000           # a wake this old whose Assistant sits idle: its turn ended
HEARTBEAT_MAX = 8000             # characters of the heartbeat in a wake (the rest: read the file)
DIRECTION_MAX = 300              # a Direction's one line (core/autopilot.js DIRECTION_MAX)
# The heartbeat Everywhere has until someone writes one: a real one, ready as it is in any project
# (core/autopilot.js STARTER, the same words). The person edits it, Everywhere's or a space's own, to steer.
STARTER = """Direction: push this project forward on your own, without wasting my time or money. Follow this space's Direction when it has one; with none, work out what matters most from the project itself.

You decide, I steer later: make every call yourself, taste and product choices too. Pick the best option, act on it, and tell me in one line what you chose and why, so I can redirect you. Never ask me a question on a wake; only a take-over (below) stops for me.

Each wake: first what woke you, then in order:
1. Needs you, failed or stalled: look why and unblock it yourself (nudge, resume, fix). A worker's question or choice: answer it yourself with the best option. Only a take-over waits for me.
2. Ready: check the work (diff, checks, proof). A clear defect: send it back saying what's wrong. Looks right: say so in one line; Accepting stays mine. Never park on a task waiting for my Accept: go on with work that doesn't need it (step 4), and each wake it still holds tasks back, say in one line what waits on my Accept.
3. To do: start the ones with a clear brief that nothing blocks; the queue limits how many run. Leave a task that's working fine alone.
4. Push the project forward: look at the project itself (README, docs and goals, recent commits and merged tasks, open GitHub issues and PRs, TODOs, failing or missing tests, your saved progress and what I asked for lately). Pick the most valuable next tasks yourself, each with a full brief (what, why, how to check it's done), and start them within the queue. At most 2 a wake; never one that repeats an open or recent task or an old ask I dropped. Tell me in one line what you started and why.
5. End by saving progress: facts and your next step, never a limit of your own. Only this heartbeat and I set limits.

Take over: the only time you stop for me. Something irreversible or outside this space that can't wait (spending money, deploying or publishing outside this space, pushes to main, emails or posts, deleting data, credentials, a security problem), or you're stuck on the same thing after retrying twice. Then don't do it: send one herdr-task alert "<what>" --body "<why>", say it in one line, and carry on with other work.

Limits:
- Never merge, Accept, mark done, delete or stop work: that's mine.
- Never do a take-over thing yourself: alert me instead.
- Work only through herdr-task tasks; never write the code yourself.
- 3 or more tasks waiting on me: add and start nothing new.
- Keep a wake short: a few minutes of your own work.

Reporting: one or two short plain lines on what you did and chose, never a question; never repeat what you told me before, except what still waits on me. When there's truly nothing worth doing (nothing to unblock, check or start, and no next task worth adding), end with <hc no-reply why="what you checked and what it waits on"/>, never a bare <hc no-reply/>.

Stopping goal: none. Runs until I switch it off.
"""
# Starters earlier versions wrote: one never edited, wherever it is kept, stands for today's.
OLD_STARTERS = ("""Direction: push this project forward on your own, without wasting my time or money. Follow this space's Direction when it has one; with none, work out what matters most from the project itself.

You decide, I steer later: make every call yourself, taste and product choices too. Pick the best option, act on it, and tell me in one line what you chose and why, so I can redirect you. Never ask me a question on a wake; only a take-over (below) stops for me.

Each wake: first what woke you, then in order:
1. Needs you, failed or stalled: look why and unblock it yourself (nudge, resume, fix). A worker's question or choice: answer it yourself with the best option. Only a take-over waits for me.
2. Ready: check the work (diff, checks, proof). A clear defect: send it back saying what's wrong. Looks right: say so in one line; Accepting stays mine.
3. To do: start the ones with a clear brief that nothing blocks; the queue limits how many run. Leave a task that's working fine alone.
4. Push the project forward: look at the project itself (README, docs and goals, recent commits and merged tasks, open GitHub issues and PRs, TODOs, failing or missing tests, your saved progress and what I asked for lately). Pick the most valuable next tasks yourself, each with a full brief (what, why, how to check it's done), and start them within the queue. At most 2 a wake; never one that repeats an open or recent task or an old ask I dropped. Tell me in one line what you started and why.
5. End by saving progress and your next step.

Take over: the only time you stop for me. Something irreversible or outside this space that can't wait (spending money, deploying or publishing outside this space, pushes to main, emails or posts, deleting data, credentials, a security problem), or you're stuck on the same thing after retrying twice. Then don't do it: send one herdr-task alert "<what>" --body "<why>", say it in one line, and carry on with other work.

Limits:
- Never merge, Accept, mark done, delete or stop work: that's mine.
- Never do a take-over thing yourself: alert me instead.
- Work only through herdr-task tasks; never write the code yourself.
- 3 or more tasks waiting on me: add and start nothing new.
- Keep a wake short: a few minutes of your own work.

Reporting: one or two short plain lines on what you did and chose, never a question; never repeat what you told me before. Reply only <hc no-reply/> when there's truly nothing worth doing: nothing to unblock, check or start, and no next task worth adding.

Stopping goal: none. Runs until I switch it off.
""", """Direction: push this project forward, without wasting my time or money. Follow this space's Direction when it has one; with none, work out what matters most from the project itself.

Each wake: first what woke you, then in order:
1. Needs you, failed or stalled: look why. Unblock what you can (nudge, resume, fix); what only I can answer, ask me once, in one clear question.
2. Ready: check the work (diff, checks, proof). Tell me in a line if it looks right to Accept; a clear defect, send it back saying what's wrong.
3. To do: start the ones with a clear brief that nothing blocks; the queue limits how many run. Leave a task that's working fine alone.
4. Push the project forward: look at the project itself (README, docs and goals, recent commits and merged tasks, open GitHub issues and PRs, TODOs, failing or missing tests, your saved progress and what I asked for lately). Add the most valuable next tasks, each with a full brief (what, why, how to check it's done), and start them within the queue. At most 2 a wake; never one that repeats an open or recent task or an old ask I dropped. Tell me in one line what you started and why.
5. End by saving progress and your next step.

Limits:
- Never merge, Accept, mark done, delete or stop work: that's mine.
- Ask before anything risky, costly or outside this space (deploys, pushes to main, emails, posts, payments, sign-ups).
- Work only through herdr-task tasks; never write the code yourself.
- Same thing failed twice: stop retrying and tell me.
- 3 or more tasks waiting on me: add and start nothing new.
- Keep a wake short: a few minutes of your own work.

Reporting: one or two short plain lines, only what's new that I must know or decide; never repeat what you told me before. Reply only <hc no-reply/> when there's truly nothing worth doing: nothing to unblock, check or start, and no next task worth adding. Alert me only for what can't wait (something down, work at risk).

Stopping goal: none. Runs until I switch it off.
""", """Direction: keep this space's tasks moving to done, without wasting my time or money. Don't invent work or redo old asks from past chats.

Each wake: first what woke you, then in order:
1. Needs you, failed or stalled: look why. Unblock what you can (nudge, resume, fix); what only I can answer, ask me once, in one clear question.
2. Ready: check the work (diff, checks, proof). Tell me in a line if it looks right to Accept; a clear defect, send it back saying what's wrong.
3. To do: start the ones with a clear brief that nothing blocks; the queue limits how many run. Leave a task that's working fine alone.
4. New tasks: only follow-ups I asked for or a finished task named. At most 2 a wake, each with a full brief.
5. End by saving progress and your next step.

Limits:
- Never merge, Accept, mark done, delete or stop work: that's mine.
- Ask before anything risky, costly or outside this space (deploys, pushes to main, emails, posts, payments, sign-ups).
- Work only through herdr-task tasks; never write the code yourself.
- Same thing failed twice: stop retrying and tell me.
- 3 or more tasks waiting on me: start nothing new.
- Keep a wake short: a few minutes of your own work.

Reporting: one or two short plain lines, only what's new that I must know or decide; never repeat what you told me before. Nothing new: reply only <hc no-reply/>, never with other words. Alert me only for what can't wait (something down, work at risk).

Stopping goal: none. Runs until I switch it off.
""", """Direction: what Autopilot works toward in this space.

Priorities:
-

Constraints: what it must not do (e.g. never merge; ask before anything risky).

Reporting: what to tell me after a wake, and when to alert me.

Stopping goal (optional): a finite goal; when it's proven, Autopilot turns itself off.
""",)


def heartbeat_path(project=None):
    """Everywhere's heartbeat file from before Settings kept it (read while Settings has none)."""
    return os.path.join(HEARTBEATS, "everywhere.md")


def heartbeat_words(path):
    try:
        with open(path) as f:
            return f.read()
    except OSError:
        return ""


def heartbeat_of(project=None, cfg=None):
    """(words, where) of the heartbeat a project works from (None: Everywhere's): its own in
    settings ("project"), else Everywhere's there ("everywhere"), else Everywhere's file ("file"; the
    starter while there is none)."""
    every, own = autopilot_layers(read_settings() if cfg is None else cfg, project)
    for layer, where in ((own, "project"), (every, "everywhere")):
        if isinstance(layer.get("heartbeat"), str):
            return current(layer["heartbeat"]), where
    p = heartbeat_path()
    return (current(heartbeat_words(p)) if os.path.exists(p) else STARTER), "file"


def direction_of(project=None, cfg=None):
    """The one line a project steers toward (settings' "direction"): its own, else Everywhere's;
    "" when none is set (it then works out what matters from the project)."""
    every, own = autopilot_layers(read_settings() if cfg is None else cfg, project)
    for layer in (own, every):
        d = one_line(layer.get("direction"))
        if d:
            return d
    return ""


def one_line(words):
    return " ".join(str(words or "").split())[:DIRECTION_MAX] if isinstance(words, str) else ""


def old_starter(words):
    return words.strip() in {x.strip() for x in OLD_STARTERS}


def current(words):
    """A heartbeat's words, an earlier version's starter (never edited) as today's."""
    return STARTER if old_starter(words) else words


def blank(words):
    """A heartbeat nobody wrote: empty (the starter is a real one)."""
    return not current(words).strip()


def ensure_heartbeat(make=True):
    """Everywhere's file: written with STARTER when missing (make), or when it still holds an earlier
    version's starter, never edited (that one kept beside it as everywhere.md.bak). The person's own
    words are never touched."""
    p = heartbeat_path()
    words = heartbeat_words(p) if os.path.exists(p) else None
    old = words is not None and old_starter(words) and words != STARTER
    if old or (make and words is None):
        os.makedirs(HEARTBEATS, exist_ok=True)
        if old:
            with open(f"{p}.bak", "w") as f:
                f.write(words)
        tmp = f"{p}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            f.write(STARTER)
        os.replace(tmp, p)
    return p


def everywhere_default():
    return {"every": parse_every(DEFAULT_EVERY), "events": []}


def migrate(d):
    """One setting for every project: legacy space records move to d["projects"] keyed by folder,
    and version 1's per-entry schedule/events (and Everywhere's switch) go."""
    projects = d.setdefault("projects", {})
    for k, a in (d.pop("spaces", None) or {}).items():
        if isinstance(a, dict) and (folder := a.get("folder") or (k if os.path.isabs(str(k)) else None)):
            projects.setdefault(folder, dict(a, folder=folder))
    for a in projects.values():
        a.pop("every", None)
        a.pop("events", None)
    (d.get("everywhere") or {}).pop("on", None)
    d["version"] = 2


@contextlib.contextmanager
def state(write=True):
    """The autopilot state, locked; written back (atomically) when write. A read (write=False)
    never needs a writable lock: where nothing can be written (a read-only sandbox, #900), it
    reads under the shared lock, or with none when the lock file doesn't exist yet."""
    try:
        os.makedirs(STORE, exist_ok=True)
    except OSError:
        pass
    lock, can = open_lock(os.path.join(STORE, ".autopilot.lock"))
    if not can:
        if write:
            if lock:
                lock.close()
            raise Fail(f"Can't change the autopilot here: {STORE} is read-only (a sandbox?). Run this outside it.")
        write = False
    if lock:
        fcntl.flock(lock, fcntl.LOCK_EX if write else fcntl.LOCK_SH)
    try:
        d = read_list(STATE) or {}
        d.setdefault("projects", {})
        migrate(d)
        e = d.setdefault("everywhere", {})
        for k, v in everywhere_default().items():
            e.setdefault(k, v)
        yield d
        if write:
            tmp = f"{STATE}.{os.getpid()}.tmp"
            with open(tmp, "w") as f:
                json.dump(d, f, indent=1)
            os.replace(tmp, STATE)
    finally:
        if lock:
            fcntl.flock(lock, fcntl.LOCK_UN)
            lock.close()


def folder_of(folder):
    if not folder:
        return None
    exp = os.path.expanduser(str(folder).strip())
    real = os.path.realpath(exp) if os.path.exists(exp) else os.path.abspath(exp)
    try:
        return locate(real)["folder"]
    except Fail:
        return real


def known_projects(d, cfg=None):
    cfg = read_settings() if cfg is None else cfg
    folders = dict.fromkeys([*d.get("projects", {}), *(k for k in (cfg.get("projects") or {}) if os.path.isabs(str(k))),
                             *(x["folder"] for p in store_files() if isinstance(x := read_list(p), dict) and x.get("folder"))])
    return [(f, d["projects"].get(f) or {"folder": f}) for f in folders]


def list_all(here_only=False):
    cfg = read_settings()
    with state(write=False) as d:
        return {"everywhere": everywhere_view(d, cfg), "projects": [view(d, p, a, cfg) for p, a in known_projects(d, cfg)]}


def my_project(args):
    """--folder, else the caller coordinator's project folder (or cwd when a repo). None: --everywhere."""
    if getattr(args, "everywhere", False):
        if getattr(args, "folder", None):
            raise Fail("Pass --folder or --everywhere, not both.")
        return None
    if getattr(args, "folder", None):
        return folder_of(args.folder)
    if proj := caller_labels().get(PROJECT_LABEL):
        if os.path.isabs(proj):
            return folder_of(proj)
        if isinstance(lst := read_list(os.path.join(STORE, f"{proj}.json")), dict) and lst.get("folder"):
            return folder_of(lst["folder"])
    if (tid := os.environ.get("HC_TERMINAL_ID")) and (cwd := (hc_json("get", tid) or {}).get("cwd")):
        return folder_of(cwd)
    with contextlib.suppress(Fail, OSError):
        if (loc := locate(os.getcwd())).get("kind") == "repo":
            return loc["folder"]
    raise Fail("Which project? Run this in the project folder, as its coordinator, or pass --folder <folder> (or --everywhere).")


def when(ms):
    return datetime.datetime.fromtimestamp(ms / 1000).strftime("%a %H:%M") if ms else None


def rule_of(x):
    """A schedule as Settings keeps it (its words; "" or off: none) as a rule; False: not one."""
    if x is None or isinstance(x, dict):
        return x
    t = str(x).strip()
    if not t or t.lower() in ("off", "none", "never"):
        return None
    try:
        return parse_every(t)
    except Fail:
        return False


def resolve(d, a, project=None, cfg=None):
    """What applies in a project (its record `a` and folder; None: Everywhere's own): {on, every,
    events, own}: on by default unless switched off; the schedule and events it sets in settings
    (cfg, read when not given), else Everywhere's there, else Everywhere's from before
    (.autopilot.json), else the defaults. own: the fields the project sets itself."""
    e = {**everywhere_default(), **{k: v for k, v in (d.get("everywhere") or {}).items() if k in ("every", "events")}}
    every, mine = autopilot_layers(read_settings() if cfg is None else cfg, project)
    own = []
    for layer, is_own in ((every, False), (mine, True)):
        if "every" in layer and rule_of(layer["every"]) is not False:
            e["every"] = rule_of(layer["every"])
            own += ["every"] if is_own else []
        if isinstance(layer.get("events"), list):
            e["events"] = layer["events"]
            own += ["events"] if is_own else []
    if isinstance(mine.get("heartbeat"), str):
        own.append("heartbeat")
    if one_line(mine.get("direction")):
        own.append("direction")
    return {"on": (a or {}).get("on") is not False, "every": e["every"], "events": [x for x in EVENTS if x in (e["events"] or [])], "own": own}


def trigger_of(eff):
    return trigger_words({"every": eff["every"], "on": eff["events"]}) if (eff["every"] or eff["events"]) else "Only Run now"


def sync(d, cfg=None):
    """Acts on what changed in what applies to each project (its switch, or its schedule and events,
    its own or Everywhere's): turned on (a fresh start: next wake, events from now on), off
    (nothing waits), a new schedule or events."""
    cfg = read_settings() if cfg is None else cfg
    for project, a in d["projects"].items():
        eff = resolve(d, a, project, cfg)
        sig = {"on": eff["on"], "every": (eff["every"] or {}).get("text"), "events": eff["events"]}
        was = a.get("applied")
        a["applied"] = sig
        if was is None:
            if sig["on"] and not a.get("next_run"):
                a["next_run"] = next_run(eff["every"], now())
            continue
        if sig["on"] and not was.get("on"):
            a.pop("completed", None)
            a.pop("seen", None)
            a["next_run"] = next_run(eff["every"], now())
        elif not sig["on"] and was.get("on"):
            a.pop("pending", None)
            a.pop("inflight", None)
            a["next_run"] = None
        elif sig["on"] and sig["every"] != was.get("every"):
            a["next_run"] = next_run(eff["every"], now())
        if sig["events"] != was.get("events"):
            a.pop("seen", None)


def view(d, project, a, cfg=None):
    """A project's Autopilot: its switch, the schedule, events and heartbeat that apply there
    (own: the ones it sets itself; heartbeat_from: "project", "everywhere" or "file"), and the rest
    of its record."""
    a = a or {}
    cfg = read_settings() if cfg is None else cfg
    eff = resolve(d, a, project, cfg)
    words, where = heartbeat_of(project, cfg)
    p_cfg = (cfg.get("projects") or {}).get(project) if isinstance(cfg.get("projects"), dict) else None
    name = a.get("name") or (p_cfg.get("name") if isinstance(p_cfg, dict) else None) or os.path.basename((project or "").rstrip("/"))
    v = {k: a.get(k) for k in ("next_run", "pending", "inflight", "last_wake", "wakes", "progress", "completed")}
    v.update(project=project, folder=a.get("folder") or project, name=name, on=eff["on"],
             every=(eff["every"] or {}).get("text"), events=eff["events"], own=eff["own"],
             heartbeat=heartbeat_path(), heartbeat_text=words, heartbeat_from=where, blank=blank(words),
             trigger=trigger_of(eff), direction=direction_of(project, cfg))
    return v


def everywhere_view(d, cfg=None):
    cfg = read_settings() if cfg is None else cfg
    e = resolve(d, None, None, cfg)
    words, where = heartbeat_of(None, cfg)
    return {"everywhere": True, "project": None, "every": (e["every"] or {}).get("text"), "events": e["events"],
            "heartbeat": heartbeat_path(), "heartbeat_text": words, "heartbeat_from": where, "blank": blank(words),
            "trigger": trigger_of(e)}


def shown_name(v):
    name = v.get("name")
    if not name or name == "~":
        folder = (v.get("folder") or v.get("project") or "").rstrip("/")
        name = os.path.basename(folder) or folder or "project"
    return name


def line_of(v):
    if v.get("everywhere"):
        return f"Everywhere: {v['trigger']}" + (", no heartbeat yet" if v.get("blank") else "")
    state_word = "on" if v["on"] else "off"
    nxt = f", next {when(v['next_run'])}" if v["on"] and v.get("next_run") else ""
    last = v.get("last_wake")
    last = f"; last {when(last['at'])} ({last['reason']}): {last['outcome']}" if last else ""
    own = f"; its own {', '.join(OWN_WORDS[k] for k in v.get('own') or [])}" if v.get("own") else ""
    return f"{shown_name(v)}: Autopilot {state_word} ({v['trigger']}{nxt}){own}{last}"


OWN_WORDS = {"every": "schedule", "events": "events", "heartbeat": "heartbeat", "direction": "direction"}


def log_wake(a, reason, outcome, at=None):
    w = {"at": at or now(), "reason": reason, "outcome": outcome}
    a["last_wake"] = w
    a["wakes"] = (a.get("wakes") or [])[-9:] + [w]


def queue(a, reason):
    """One wake at a time: a second reason joins the one waiting."""
    p = a.get("pending")
    if p:
        if reason not in p["reasons"]:
            p["reasons"] = (p["reasons"] + [reason])[:6]
    else:
        a["pending"] = {"reasons": [reason], "at": now()}


def waiting(project, a):
    """Ready tasks of this project that others wait behind (queued --after them, down the chain), in
    words: ["TG-2 is Ready, waiting on the person's Accept; TG-3 and TG-4 wait behind it"]. A fact
    for the wake, so a blocked chain is said, never parked on in silence."""
    folder = (a or {}).get("folder") or project
    out = []
    for p in store_files():
        d = read_list(p) or {}
        if d.get("folder") != folder:
            continue
        tasks = d.get("tasks") or []
        ref = lambda t: f"{d.get('prefix') or ''}-{t['id']}"
        for t in tasks:
            if t.get("state") != "ready":
                continue
            behind, look = [], [t["id"]]
            while look:
                i = look.pop(0)
                for x in tasks:
                    if i in (x.get("after") or []) and x.get("state") in ("queued", "todo") and x not in behind:
                        behind.append(x)
                        look.append(x["id"])
            if behind:
                names = [ref(x) for x in behind]
                names = names[0] if len(names) == 1 else ", ".join(names[:-1]) + " and " + names[-1]
                out.append(f"{ref(t)} is Ready, waiting on the person's Accept; {names} wait{'s' if len(behind) == 1 else ''} behind it")
    return out


def message(project, a, reasons):
    """The wake: one tagged message."""
    beat, where = heartbeat_of(project)
    beat = "" if blank(beat) else beat.strip()
    path = heartbeat_path()
    src = {"project": "this project's own, settings > Autopilot",
           "everywhere": "Everywhere's, settings > Autopilot"}.get(where, path.replace(HOME, "~", 1))
    if len(beat) > HEARTBEAT_MAX:
        beat = beat[:HEARTBEAT_MAX] + (f"\n… (cut here: read the rest in {path})" if where == "file" else "\n… (cut here)")
    p = a.get("progress")
    prog = f"Saved progress ({when(p['at'])}):\n{p['text']}" if p else "Saved progress: none yet (this is the first wake)."
    why = reasons[0] + (f" (also: {', '.join(reasons[1:])})" if len(reasons) > 1 else "")
    waits = "\n".join(f"- {x}" for x in waiting(project, a))
    aim = direction_of(project)
    aim = (f"Direction (set by the person, settings > Autopilot): {aim}" if aim else
           "Direction: none set. Work out what matters most from the project itself: its README, docs and goals, "
           "recent commits and merged tasks, open issues and PRs, and what the person asked for lately.")
    body = (f"Autopilot wake: {why}, {when(now())}.\n"
            "You are on Autopilot in this project: you drive it forward and decide on your own; the person steers "
            "later. Never ask the person a question on a wake: make every call yourself (product and taste choices "
            "too: pick the best option and say in one line what you chose and why), answer workers' questions "
            "yourself and unblock them. The one exception is a take-over: something irreversible or outside this "
            "project that can't wait (spending money, deploying or publishing, deleting data, credentials, a security "
            "problem) or being stuck after retrying twice: don't do it, send one herdr-task alert \"<what>\" "
            "--body \"<why>\", say it in one line and carry on with other work. Read the heartbeat, the "
            "direction and your saved progress below, look at the tasks (herdr-task list --json), and do the most "
            "useful work the heartbeat permits: unblock and check what's open, start what's ready to start, and, "
            "unless the heartbeat says otherwise, look at the project and add and start the next most valuable "
            "tasks to push it toward the direction. Evaluate what the evidence shows, then save progress and your "
            "next step with herdr-task progress \"...\". When the heartbeat's stopping goal is met and proven: "
            "herdr-task complete \"evidence\" (this turns Autopilot off). A Ready task that others wait behind "
            "never parks you: go on with work that doesn't need it (tasks outside its chain, within the heartbeat's "
            "limits), and each wake it still holds them back, say in one line what waits on the person's Accept. "
            "Saved progress records facts and your next step, never a limit or pace of your own: only the heartbeat "
            "and the person set limits, and one in your saved progress that neither set doesn't apply (leave it out "
            "when you save). Only when there's truly nothing worth doing (nothing to unblock, check or start, and no "
            "next task worth adding): save progress saying so, and end with <hc no-reply why=\"...\"/>, its why one "
            "short line of what you checked and what it waits on (the chat shows it on the wake's line), never a "
            "bare <hc no-reply/>.\n\n"
            f"Heartbeat ({src}):\n{beat or '(not written yet: tell the person Autopilot has no heartbeat yet, and do nothing else)'}\n\n"
            f"{aim}\n\n"
            + (f"Waiting on the person:\n{waits}\n\n" if waits else "")
            + f"{prog}")
    body = body.replace("</herdr-autopilot", "</ herdr-autopilot")
    reason = reasons[0].replace('"', "'")
    return f'<herdr-autopilot reason="{reason}" project="{project}">\n{body}\n</herdr-autopilot>'


def deliver(project, a):
    """Sends the waiting wake when the project's coordinator is live and idle. Returns what happened,
    or None when it still waits."""
    p = a.get("pending")
    if not p or a.get("inflight"):
        return None
    to = coordinator_in(a.get("folder") or project)
    k = hc_json("get", to) if to else None
    if not k or k.get("state") != "live" or not agent_running(k):
        if now() - p["at"] > PENDING_MAX_MS:
            a.pop("pending")
            log_wake(a, p["reasons"][0], "Missed: no Assistant was running")
            return "missed"
        p["note"] = "Waiting for its Assistant to run"
        return None
    if status_word(k) in ("working", "blocked"):
        p["note"] = "Waiting for its Assistant to be idle" if status_word(k) == "working" else "Waiting: its Assistant asks you something"
        return None
    if typing(to, k):
        p["note"] = "Waiting: you're writing to its Assistant"
        return None
    if not news_send(to, message(project, a, p["reasons"]), check=False):
        p["note"] = "Waiting: its Assistant couldn't take it yet"
        return None
    a.pop("pending")
    a["inflight"] = {"at": now(), "reason": p["reasons"][0], "to": to, "turn": k.get("turn") or 0}
    log_wake(a, p["reasons"][0], "Delivered")
    return "delivered"


def settle(a):
    """A delivered wake's turn ended: its outcome."""
    f = a.get("inflight")
    if not f:
        return
    k = hc_json("get", f["to"]) or {}
    gone = k.get("state") != "live" or not agent_running(k)
    # Its turn counted, or (should the count miss it) the Assistant idle a while after the wake:
    # closed either way, so the next wake can go.
    # A turn of a second (a quiet wake) can slip past the count: its status settling after the wake
    # (status_ms) says it ended too (SHEP-40: "Waking" held two minutes after a one-second turn).
    st = status_word(k)
    settled_since = st in ("idle", "done") and (k.get("status_ms") or 0) > f["at"]
    ended = st != "working" and ((k.get("turn") or 0) > f["turn"] or settled_since or (st in ("idle", "done") and now() - f["at"] > IDLE_AFTER_MS))
    if not (gone or ended or now() - f["at"] > TURN_MAX_MS):
        return
    a.pop("inflight")
    saved = (a.get("progress") or {}).get("at", 0) >= f["at"]
    outcome = ("Assistant stopped" if gone else "Turn ended" if ended else "No turn end seen") + \
              (", progress saved" if saved else ", no progress saved")
    w = a.get("last_wake")
    if w and w.get("at") and w["at"] >= f["at"] - 1000 and w["outcome"] == "Delivered":
        w["outcome"] = outcome
        if a.get("wakes"):
            a["wakes"][-1] = w
    else:
        log_wake(a, f["reason"], outcome, f["at"])


def events_for(project, a, events):
    """Task events since the last look that concern this project among `events` (what applies there):
    ["event:ready HAT-4", ...]. Records only, the first look."""
    seen_all = a.setdefault("seen", {})
    folder = a.get("folder") or project
    out = []
    for p in store_files():
        d = read_list(p)
        if not d or d.get("folder") != folder or not os.path.isdir(folder or ""):
            continue
        with Store(locate(folder), write=False) as s:
            for ev, t in events_in(s, seen_all.setdefault(s.slug, {})):
                # Added while a wake is at work: its own doing, not news.
                if ev in events and not (ev == "added" and a.get("inflight")):
                    out.append(f"event:{ev} {s.ref(t)}")
    return out


def autopilot_pass(report):
    """The scheduler's round: due schedules and heard events queue a wake; a waiting wake goes to
    its idle coordinator; a delivered one is settled when its turn ends."""
    cfg = read_settings()
    with state() as d:
        for folder, _ in known_projects(d, cfg):
            d["projects"].setdefault(folder, {"folder": folder})
        if any_on(d):
            ensure_heartbeat(make=False)  # an earlier version's starter: today's (the old one kept as .bak)
        sync(d, cfg)
        for project, a in d["projects"].items():
            eff = resolve(d, a, project, cfg)
            if not eff["on"]:
                continue
            settle(a)
            if eff["every"] and a.get("next_run") and now() >= a["next_run"]:
                a["next_run"] = next_run(eff["every"], now())
                queue(a, "schedule")
            if eff["events"]:
                for why in events_for(project, a, eff["events"]):
                    queue(a, why)
            if deliver(project, a) == "delivered":
                report.setdefault("autopilot", []).append({"project": project, "to": a["inflight"]["to"], "why": a["inflight"]["reason"]})


def any_on(d=None):
    """Whether Autopilot is on in any project here."""
    d = d if d is not None else (read_list(STATE) or {})
    return any((a or {}).get("on") is not False for a in (d.get("projects") or {}).values())


# ---- commands -------------------------------------------------------------------------------

PER_PROJECT = "Autopilot is switched on or off per project: pass --folder."


def set_values(args):
    """set's --every, --on and --heartbeat as Settings keeps them; "default" unsets one at that
    level, so it follows the level above again."""
    out = {}
    dflt = lambda x: x.strip().lower() == "default"
    if args.every is not None:
        x = args.every.strip()
        out["every"] = None if dflt(x) else "" if not x or x.lower() in ("off", "none", "never") else parse_every(x)["text"]
    if args.on is not None:
        out["events"] = None if dflt(args.on) else parse_on(args.on)
    if getattr(args, "heartbeat", None) is not None:
        out["heartbeat"] = None if dflt(args.heartbeat) else args.heartbeat[:HEARTBEAT_MAX]
    if getattr(args, "direction", None) is not None:
        out["direction"] = None if dflt(args.direction) or not one_line(args.direction) else one_line(args.direction)
    if not out:
        raise Fail("Give --every, --on, --heartbeat or --direction (\"default\": follow Everywhere's again).")
    return out


def cmd_autopilot(args):
    sub = args.autopilot
    ensure_heartbeat(make=False)  # an earlier version's starter in the file: today's, before anyone reads it
    if sub == "list":
        out = list_all(getattr(args, "here", False))
        if args.json:
            print(json.dumps(out))
        else:
            print("\n".join([line_of(out["everywhere"])] + [line_of(v) for v in out["projects"]]))
        return
    if sub == "check":
        # a schedule as Settings would keep it, checked without saving
        x = (args.every or "").strip()
        r = rule_of(x) if x.lower() not in ("off", "none", "never", "") else None
        if r is False:
            parse_every(x)  # says why
        v = {"every": (r or {}).get("text"), "trigger": trigger_of({"every": r, "events": []}) if r else "Off"}
        say(args, v, v["trigger"])
        return
    project = None if sub == "heartbeat" and not getattr(args, "folder", None) else my_project(args)
    if sub == "set":
        write_autopilot(project, set_values(args))
    if project is None and sub in ("on", "off", "run-now"):
        raise Fail(PER_PROJECT if sub != "run-now" else "Run now wakes one project's coordinator: pass --folder.")
    msg = ""
    with state(write=sub not in ("show", "heartbeat")) as d:
        a = None
        if project is not None:
            a = d["projects"].get(project)
            if a is None:
                a = {"folder": project} if sub in ("show", "heartbeat") else d["projects"].setdefault(project, {"folder": project})
            if sub not in ("show", "heartbeat"):
                a["folder"] = project
                if getattr(args, "name", None):
                    a["name"] = args.name
                else:
                    a.setdefault("name", os.path.basename(project.rstrip("/")))
        if sub == "on":
            was = bool(a.get("on"))
            a["on"] = True
            ensure_heartbeat()
            sync(d)
            msg = f"Autopilot is on: {trigger_of(resolve(d, a, project))}."
            if not was:
                # Switched on: its first wake goes now (queued until its coordinator is idle), not one
                # interval later, so the person sees it work at once.
                queue(a, "switched on")
                got = deliver(project, a)
                msg += " Woke its Assistant." if got == "delivered" else f" First wake queued: {(a.get('pending') or {}).get('note') or 'it goes when its Assistant is idle'}."
        elif sub == "off":
            a["on"] = False
            sync(d)
            msg = "Autopilot is off."
        elif sub == "set":
            sync(d)
            eff = resolve(d, a, project)
            how = trigger_of(eff) if (eff["every"] or eff["events"]) else "only on Run now"
            msg = f"Autopilot wakes every project that follows Everywhere: {how}." if project is None else \
                f"Autopilot wakes this project: {how}" + (f" (its own {', '.join(OWN_WORDS[k] for k in eff['own'])})." if eff["own"] else " (Everywhere's).")
        elif sub == "run-now":
            if not resolve(d, a, project)["on"]:
                raise Fail("Autopilot is off in this project: turn it on first.")
            sync(d)
            settle(a)  # a wake whose turn has ended never holds this one back
            queue(a, "run now")
            got = deliver(project, a)
            msg = "Woke its Assistant." if got == "delivered" else f"Queued: {(a.get('pending') or {}).get('note') or 'it goes when its Assistant is idle'}."
        elif sub == "heartbeat":
            ensure_heartbeat()
        v = everywhere_view(d) if project is None else view(d, project, a)
    say(args, v, msg or line_of(v))


def cmd_progress(args):
    """The coordinator saves where Autopilot is and its next step; the next wake brings it back."""
    project = my_project(args)
    text = " ".join(args.text.split()) if "\n" not in args.text else args.text.strip()
    if not text:
        raise Fail("Say what was done and what's next.")
    if project is None:
        raise Fail("Progress is a project's: run this in its folder, as its coordinator, or pass --folder.")
    with state() as d:
        a = d["projects"].setdefault(project, {"folder": project})
        a["progress"] = {"text": text[:4000], "at": now()}
        v = view(d, project, a)
    say(args, v, "Progress saved." + ("" if v["on"] else " (Autopilot is off here.)"))


def cmd_complete(args):
    """A finite goal met and proven: Autopilot turns off, with the evidence kept."""
    project = my_project(args)
    if not args.evidence.strip():
        raise Fail("Give the evidence that the goal is met.")
    if project is None:
        raise Fail("A goal is a project's: run this in its folder, as its coordinator, or pass --folder.")
    with state() as d:
        a = d["projects"].setdefault(project, {"folder": project})
        if not resolve(d, a, project)["on"]:
            raise Fail("Autopilot isn't on in this project.")
        a.update(on=False, next_run=None, completed={"text": args.evidence.strip()[:4000], "at": now()})
        a.pop("pending", None)
        a["progress"] = {"text": f"Goal complete: {args.evidence.strip()[:3900]}", "at": now()}
        f = a.pop("inflight", None)
        w = a.get("last_wake")
        if f and w and w.get("at") == f["at"] and w["outcome"] == "Delivered":
            w["outcome"] = "Goal complete: Autopilot turned off"  # the wake it came from
            a["wakes"][-1] = w
        else:
            log_wake(a, (f or {}).get("reason") or "complete", "Goal complete: Autopilot turned off")
        sync(d)
        v = view(d, project, a)
    say(args, v, "Goal complete: Autopilot is now off in this project. Tell the person, with the evidence.")
