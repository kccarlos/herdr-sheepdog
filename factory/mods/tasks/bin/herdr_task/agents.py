"""Live agents: a task's terminals, its status and what its agent last said or asks."""

import datetime
import json
import os
import re
import time

from .base import Fail, git, hc_json, HOME, label_of, now, RANK, run, SHELLS, TASK_LABEL


# ---- live agents ----------------------------------------------------------------------------

# A worker's own helper agents (SHEP-54): hc children of its terminal it starts itself (`hc run`), labelled
# herdr.task (its task) and herdr.role reviewer or helper. Never one of the task's agents: no second worker, no
# status of their own; a working one keeps its quiet worker Working (own_work).
HELPER_ROLES = ("reviewer", "helper")


def parent_of(k):
    """Its parent's address as hc ls writes this terminal's: another machine's names its parent there ("t5" for "vm/t5")."""
    p, a = str(k.get("parent") or ""), str(k.get("address") or "")
    return f"{a.split('/')[0]}/{p}" if p and "/" in a and "/" not in p else p or None


def is_helper(k, mine=()):
    """hc ls gives no labels: a helper is also told by its parent, another terminal of the same task
    (a worker's parent is its Assistant, never the task's)."""
    return bool(k.get("parent")) and (label_of(k.get("labels"), "role") in HELPER_ROLES or parent_of(k) in mine)


def _live(key):
    found = (hc_json("ls", "--label", f"{TASK_LABEL}={key}") or {}).get("terminals") or []
    found += (hc_json("ls", "--label", f"herdr.task={key}") or {}).get("terminals") or []
    by_addr = {}
    for k in found:
        if k.get("state") == "live" and k.get("address") not in by_addr:
            by_addr[k["address"]] = dict(k, labels=dict(k.get("labels") or {}, **{TASK_LABEL: key, "herdr.task": key}))
    mine = set(by_addr)
    return [(k, is_helper(k, mine)) for k in by_addr.values()]


def live_agents(key):
    """The task's live terminals, with labels (hc ls leaves labels out unless filtering by them),
    its worker's helpers left out (live_helpers()). Agents start inside the person's shell, which
    stays when the agent exits: agent_running()."""
    return [k for k, helper in _live(key) if not helper]


def live_helpers(key):
    """The helper agents its worker started (a reviewer, a second opinion), still live."""
    return [k for k, helper in _live(key) if helper]


def detail(k):
    """hc ls gives no foreground; hc get does."""
    return hc_json("get", k["address"]) or k


AGENT_BINS = ("claude", "claude-code", "codex", "agy", "antigravity")
AGENT_VER = re.compile(r"^\d+(?:\.\d+)+$")  # Claude Code's native binary is named by its version


def cli_of(k):
    lb = label_of(k.get("labels"), "agent")
    if lb:
        return lb
    fg, st = k.get("foreground") or {}, k.get("status") if isinstance(k.get("status"), dict) else {}
    raw = " ".join(str(x or "") for x in (st.get("app"), k.get("agent"), fg.get("name"), *(fg.get("argv") or [])[:1], *(k.get("argv") or [])[:1])).lower()
    return "agy" if "agy" in raw or "antigravity" in raw else "codex" if "codex" in raw else "claude"


def _cmd_shell(fg):
    """A shell running a `-c` command rather than waiting interactively after an agent exited."""
    argv = (fg or {}).get("argv") or []
    return any(a in ("-c", "-lc", "-ic", "-lic") for a in argv[1:3])


def agent_running(k):
    fg = k.get("foreground") or {}
    if "herdr-sandbox" in (fg.get("argv") or [])[:4]:
        return True  # a sandboxed reviewer's launcher (launch.SANDBOX_SH), alive while its agy runs
    name = os.path.basename(fg.get("name") or "").lstrip("-")
    if name:
        if name not in SHELLS:
            return True
        # fish keeps the agent its child, in its own process group (#780): the terminal's foreground
        # stays the shell running its launch command (fish -ic "claude …; exec fish -i") the whole
        # time. Only once it exec'ed its plain interactive shell has the agent exited.
        return runs_command(fg.get("argv"))
    return bool(k.get("agent")) and k.get("agent") not in SHELLS


def runs_command(argv):
    """A shell's argv that runs a command (-c, -ic, -lic, --command), not a plain shell."""
    for a in (argv or [])[1:]:
        if a == "--command" or a.startswith("--command="):
            return True
        if not a.startswith("-") or a.startswith("--"):
            break
        if "c" in a[1:]:
            return True
    return False


def mid_turn_agents(records, look=None):
    """Those of these hc records (hc ls is enough) mid-turn: working or asking, their agent still
    there. #1022: an agent that died mid-turn (crashed, killed) leaves its plain shell, and hc keeps
    the status it last reported ("working" for good, its ls even naming the dead agent), holding its
    task in Working. So a record that says mid-turn is read in full (look: hc get) for what is in
    front: a plain shell is no agent; fish running its agent (#780) still is."""
    out = []
    for k in records:
        if status_word(k) not in ("working", "blocked"):
            continue
        full = k if (k.get("foreground") or {}).get("name") else (look or detail)(k)
        if (full.get("foreground") or {}).get("name") and not agent_running(full):
            continue
        out.append(full)
    return out


def status_of(task, live):
    """A task's status now: its stored state, or for running tasks the most urgent agent status
    (blocked > done > working > idle), or "stopped" when no agent is left."""
    if task["state"] not in ("running", "ready"):
        return task["state"]
    best = None
    for k in live:
        st = k.get("status")
        st = st.get("state") if isinstance(st, dict) else st
        st = st if st in RANK else "idle"
        if best is None or RANK[st] > RANK[best]:
            best = st
    if task["state"] == "ready":
        return "ready"
    return best or ("working" if review_running(task) else "stopped")


# What people see a task as: one of eight, derived, never stored. From the task's record (todo,
# scheduled (SHEP-38: starts by itself at a time), queued, ready, done, attention, a merge asked for or waiting on checks) and its agents' own
# Herdr status (working, blocked, idle/done), which agents report as always.
IN_QUEUE_LINE = "In GitHub's merge queue"
APPROVAL_LINE = "Merges once it's approved"
STATUSES = {"todo": "To do", "scheduled": "Scheduled", "queued": "Queued", "working": "Working", "needs": "Needs you",
            "ready": "Ready", "merging": "Merging", "done": "Done"}


def _startup_auth(task, k):
    """agy briefly reports blocked (auth) at turn 0 while booting; ignore during STARTING_MS."""
    st = k.get("status") if isinstance(k.get("status"), dict) else {}
    return (status_word(k) == "blocked" and st.get("kind") == "auth"
            and cli_of(k) == "agy" and not k.get("turn") and starting(task))


def task_status(task, live):
    """(status, needs, why): a STATUSES key; for Needs you what kind ("attention", "asks",
    "reply", "stopped") and a one-line reason. live = its running agents (hc records)."""
    st = task["state"]
    if st == "scheduled":
        from .when import starts_words
        return "scheduled", None, starts_words(task.get("scheduled_for") or now(), now())
    if st not in ("running", "ready"):
        return (st if st in STATUSES else "todo"), None, None
    states = ["working" if _startup_auth(task, k) else "blocked" if asking(k) else status_word(k) for k in live]
    # A problem Herdr just found (SHEP-133): not Needs you before GRACE_MS have passed, while its
    # agent is told and takes it up; until then the task reads as it would without it. Recaps and
    # repeated reviewer failures wait on the Assistant right away (not the worker).
    if (task.get("attention") and not task.get("recap")
            and not (task.get("attention") or "").startswith(REVIEWER_FAILED)
            and now() - (task.get("attention_at") or 0) < GRACE_MS):
        return task_status({**task, "attention": None}, live)
    # What Herdr found that stops it (failing checks, a conflict): Needs you, unless its agent is
    # already on it (on_it()); then it is Working, with its agent's own status line.
    if task.get("attention") and not on_it(task, live):
        return "needs", "attention", task["attention"]
    # Cut off by its provider (retry_pass): still Working while Herdr waits to tell it to carry
    # on; Needs you once the tries are used up, or at once for what waiting can't fix.
    r = task.get("retry") if st == "running" else None
    if r and any(k.get("address") == r.get("address") for k in live) and ("blocked" not in states or r.get("over")):
        return ("needs", "provider", r.get("reason")) if r.get("over") else ("working", None, retry_line(r))
    if "blocked" in states:
        return "needs", "asks", REASONS["question"]
    if trust_asked(live):
        return "needs", "asks", REASONS["trust"]
    # Out of review rounds (FAC-95, note 49): it waits on its Assistant (review-more, accept
    # --owner-approved, reopen) whatever its agent does meanwhile: busy, a background job, its own line.
    if st == "running" and task.get("review_out"):
        return "needs", "reply", out_of_rounds_why(task)
    # Waits on the physical device (FAC-110, herdr-task device-wait): locked, untrusted or not connected.
    if st == "running" and live and (task.get("device_wait") or {}).get("why"):
        return "needs", "reply", device_why(task)
    if back_at_work(task, live):
        return "working", None, WORKING_AGAIN
    if task.get("attention"):
        return "working", None, WORKING_AGAIN if st == "ready" else None
    if st == "ready":
        # Nothing for the person yet (SHEP-175): its merge under way (asked, in GitHub's merge queue, or
        # by itself), or its pull request's checks still running. Working, with what it waits for.
        wait = ready_wait(task, live)
        if wait:
            return "working", None, wait
        # Said ready but not at rest yet (SHEP-148): Working until it is, and only then Ready.
        if live and not_at_rest(task, live):
            return "working", None, None
        return "ready", None, None
    # An agent that hasn't reported yet is starting: working.
    if any(x not in ("idle", "done", "blocked") for x in states):
        return "working", None, None
    # Just started: its terminal still runs the shell that starts the agent, or the agent is idle
    # before its first message lands. Working, until it ends or STARTING_MS pass.
    if starting(task):
        return "working", None, None
    if review_running(task):
        return "working", None, None if (task.get("progress") or {}).get("text") else REVIEWER_ON_IT
    if (task.get("review_quota_wait") or {}).get("head") == task.get("head") and task.get("head"):  # FAC-101: waits for a reset
        return "working", None, task["review_quota_wait"]["text"]
    # Quiet without saying ready, but on its own work (SHEP-53, FAC-60): a helper reviewer at work
    # keeps it Working; a background job of its own (hc get's foreground or status msg, its log's
    # background shells/Monitors, or its fresh status line) shows Waiting on a background job,
    # unless its last message asks the person something.
    if live and helper_working(task, live):
        return "working", None, None
    # Told its background job ended (FAC-106, bg_ended_pass): Waiting on it a while longer, then Needs you
    # if it still took no turn since.
    told = task.get("bg_told") if st == "running" else None
    if told and told.get("at") and any(k.get("address") == told.get("address") for k in live) and not turn_since(told, live):
        return ("working", None, WAITING_BG) if now() - told["at"] < BG_GRACE_MS else ("needs", "reply", BG_ENDED_WHY)
    if live and own_work(task, live):
        return "working", None, WAITING_BG
    # Just gone quiet (SHEP-133): still Working for GRACE_MS, as its agent often ends a turn a
    # moment before it says ready or takes up the next thing on its own.
    if live and settling(live):
        return "working", None, None
    # Quiet without saying ready: alive, it ended its turn (view() says on what: its question or
    # its last sentence); its process gone, the agent stopped.
    if live:
        return "needs", "reply", REASONS["reply"]
    return "needs", "stopped", STOPPED


STOPPED = "Agent stopped"
device_why = lambda task: f"Device not available: {(task.get('device_wait') or {}).get('why')}"
OUT_OF_ROUNDS = "Out of review rounds: wants one more"


def out_of_rounds_why(task):
    """Why a task out of review rounds waits: a design or doc task goes to the person, others want a round."""
    from .settings import review_kind
    return "Out of review rounds: for the person" if review_kind(task) else OUT_OF_ROUNDS


# ---- Ready only when the person must act (SHEP-175) ----------------------------------------------
# A task said ready is Ready (and its news and alerts) only once the person's Accept is what's
# left: not while its pull request's checks still run or GitHub works out whether it can merge
# (pr["wait"], kept by the tick), nor while it merges by itself. Then it is Working, its row saying
# in small words what it waits for. As core/tasks.js readyWait().
MERGING, MERGES_ITSELF = "Merging", "Merges once its checks pass"


def ready_wait(task, live):
    """What a Ready task waits on that needs nobody, in a few words; None when it's the person's."""
    if task.get("merge_requested") and live:
        return MERGING
    if task.get("merge_queued") and task.get("pr"):
        return IN_QUEUE_LINE
    if task.get("auto_merge") and task.get("pr") and not task.get("merged"):
        # #743: waits calmly for a review GitHub requires
        return APPROVAL_LINE if task["pr"].get("needs_approval") else MERGES_ITSELF
    return (task.get("pr") or {}).get("wait") or None


# ---- a short grace before Needs you (SHEP-133) ------------------------------------------------
# A task flips to Needs you for a few seconds and moves on by itself: its agent ends a turn a
# moment before it runs herdr-task ready or status, a background job's notice wakes it, a problem
# is found just before its agent is handed it. So a quiet agent (Needs you: reply) and a problem
# Herdr found (attention) need the person only once they've lasted GRACE_MS: the agent's own
# status change (hc's status_ms) or the problem's attention_at says since when. What really asks
# (a question tool, a permission: blocked) needs the person at once. As core/tasks.js GRACE_MS.
GRACE_MS = 20_000


def quiet_since(live):
    """When its agents last changed status (ms; hc's status_ms), or 0 when none says."""
    return max([k.get("status_ms") or 0 for k in live if isinstance(k.get("status_ms"), (int, float))] or [0])


def settling(live):
    """Its agents went quiet under GRACE_MS ago."""
    since = quiet_since(live)
    return bool(since) and now() - since < GRACE_MS


# ---- a Ready task back at work ----------------------------------------------------------------
# A Ready task whose agent takes up work again (a message in its chat from anyone, herdr-task fix
# or resume) is Working again until its next ready (as core/tasks.js busyAgain()). Herdr counts
# an agent's finished turns (hc's "turn"): ready keeps, per agent, the turn it is on once the one
# it said ready in has ended (ready_turns); working on that turn or a later one is new work.
WORKING_AGAIN = "Working again"


def ready_turns(live):
    """At ready: {address: the first turn that is new work} for its agents now."""
    return {k["address"]: (k.get("turn") or 0) + (1 if status_word(k) == "working" else 0)
            for k in live if k.get("address")}


def busy_again(task, live):
    """A Ready task whose agent is at work again since it said ready. Not while it merges as
    asked (the merge is its work), nor a reviewer's turn. A ready said before turns were kept:
    any work once STARTING_MS have passed."""
    if task["state"] != "ready" or task.get("merge_requested"):
        return False
    seen = task.get("ready_turns")
    if seen is None and now() - (task.get("ready_at") or 0) < STARTING_MS:
        return False
    return any(status_word(k) == "working" and label_of(k.get("labels"), "role") != "reviewer"
               and (k.get("turn") or 0) >= (seen or {}).get(k.get("address"), 0) and not stray(task, k) for k in live)


# ---- a stray wake isn't work (FAC-107) --------------------------------------------------------
# A Ready task's agent wakes with nothing to do: Claude Code's idle compaction (it reads files and
# rewrites its session), a review round's news, a background notice. That turn is no new work: the
# task stays Ready, without a flip nor its news, unless the turn shows a real sign: a new commit,
# changes in its worktree, its own status line since ready, or a minute at work (hc's status_ms)
# that isn't an idle compaction. A turn of an idle compaction only (its log since its last turn: no
# message, or its "Compacted while idle" notice) is kept as task["stray_since"] for the window
# (core/tasks.js busyAgain()), which also counts a later turn only once it has run REAL_TURN_MS.
# Keyed by when that stint at work began (hc's status_ms), so the next wake is judged afresh.
REAL_TURN_MS = 60000
IDLE_COMPACTED = "Compacted while idle"


def compaction_only(task, address):
    """Its Claude Code log since its last finished turn holds no message, or only an idle compaction's
    records (the boundary, its summary, the notice); False when its log doesn't say."""
    # newest first: the compaction re-reads files into big attachments, so its turn's end can lie
    # beyond the tail read; the notice says it alone
    for d in reversed(log_tail(agent_log(task, address), 131072)):
        if d.get("type") == "system" and (d.get("subtype") == "turn_duration" or str(d.get("content") or "").startswith(IDLE_COMPACTED)):
            return True
        if d.get("type") == "assistant" or (from_person(d) is not None and not d.get("isCompactSummary")):
            return False
    return False


def real_turn(task, k):
    """A Ready task's agent k, at work on a turn since ready: a real sign of new work in it."""
    p, since = task.get("progress") or {}, k.get("status_ms")
    if not p.get("by") and (p.get("set") or 0) > max(task.get("ready_at") or 0, since if isinstance(since, (int, float)) else 0):
        return True  # its own status line in this stint at work
    wt = task.get("worktree") if task.get("mode") == "worktree" else None
    if wt and task.get("head") and os.path.isdir(wt):
        ok, head, _ = git(wt, "rev-parse", "HEAD")
        if ok and (head != task["head"] or git(wt, "status", "--porcelain", "--untracked-files=no")[1]):
            return True
    if not isinstance(since, (int, float)) or not since:
        return True  # hc doesn't say since when: as before FAC-107
    if stray(task, k):
        return False  # found an idle compaction already (stray_since())
    return now() - since >= REAL_TURN_MS and not compaction_only(task, k.get("address"))


def later_turn(task, k):
    """k at work on a turn since its task said ready (ready_turns), not the turn it said it in."""
    seen = task.get("ready_turns")
    return seen is not None and (k.get("turn") or 0) >= seen.get(k.get("address"), 0)


def back_at_work(task, live):
    """busy_again() with a real sign of new work (real_turn()): what flips a Ready task to Working."""
    if not busy_again({**task, "stray_since": None}, live):  # a real sign counts in a stray turn too
        return False
    if task.get("ready_turns") is None:
        return True
    return any(status_word(k) == "working" and label_of(k.get("labels"), "role") != "reviewer" and later_turn(task, k)
               and real_turn(task, k) for k in live)


def stray(task, k):
    """k's stint at work is one found to be only an idle compaction (stray_since)."""
    return bool(k.get("status_ms")) and (task.get("stray_since") or {}).get(k.get("address")) == k["status_ms"]


def stray_since(task, live):
    """{address: status_ms} of its agents on a later turn that has run REAL_TURN_MS as an idle compaction
    only and shows no real sign (real_turn()), for the window."""
    return {k["address"]: k["status_ms"] for k in live if k.get("address") and status_word(k) == "working"
            and later_turn(task, k) and now() - (k.get("status_ms") or now()) >= REAL_TURN_MS and not real_turn(task, k)}


# ---- Ready only at rest (SHEP-148) --------------------------------------------------------------
# An agent often says ready and carries on in the same turn (a last check, its final message), or
# leaves its own work running: a background shell, a Monitor, a subagent, a helper of its own. That
# is no Ready yet: the person would be told to accept work that is still moving. Working until its
# agents are at rest, then Ready (and its news). A later turn is busy_again()'s.


def not_at_rest(task, live):
    """A Ready task's agent still at it: at work in the turn it said ready in, or its own work
    still running (own_work(), its hard signs only: no status line, a question asked doesn't end it)."""
    if any(status_word(k) == "working" and label_of(k.get("labels"), "role") != "reviewer"
           and not later_turn(task, k) for k in live):
        return True  # a later turn is back_at_work()'s (FAC-107)
    return own_work(task, live, ready=True)


# ---- someone is on what needs the person -------------------------------------------------------
# A task's attention (failing checks, a conflict, a failed merge) needs the person only while
# nobody is on it (SHEP-8). On it: one of its agents is at work (working, or asking: then the
# question shows), it was just handed the problem (herdr-task fix, GitHub news: handed_at) and is
# starting, or its agent said what it's doing (herdr-task status) since the problem came up and
# that is still fresh: an agent that set its line and waits on CI in the background is idle to
# Herdr. Nobody on it and the problem still there: Needs you. As core/tasks.js onIt().
ON_IT_MS = 30 * 60000  # how long an agent's own status line counts, or its own time left if longer


REVIEWER_FAILED = "Reviewer failed twice:"


def on_it(task, live):
    """live: its running agents (hc records). Cheap: the task's record and their status only."""
    if task.get("recap") or (task.get("attention") or "").startswith(REVIEWER_FAILED):  # waits for the Assistant (FAC-54, FAC-59)
        return False
    if any(status_word(k) in ("working", "blocked") and label_of(k.get("labels"), "role") != "reviewer"
           for k in live):
        return True
    if now() - (task.get("handed_at") or 0) < STARTING_MS:
        return True
    p = task.get("progress") or {}
    said = p.get("set") or 0
    if p.get("by") or not live or not said or said < (task.get("attention_at") or 0):
        return False
    left = p.get("left") if isinstance(p.get("left"), (int, float)) else 0
    return now() - said < max(ON_IT_MS, left)


def status_word(k):
    x = k.get("status")
    return x.get("state") if isinstance(x, dict) else x


def retry_line(r):
    """A provider retry's status line: "Provider overloaded · retry in 2m"."""
    if r.get("due"):
        return f"{r['reason']} · retry in {max(1, -(-(r['due'] - now()) // 60000))}m"
    return f"{r['reason']} · retrying"


# A Needs-you reason or an agent's status line: at most this many characters (as Shepherdr's
# STATUS_MAX), never the agent's message pasted, never a model's words.
LINE_MAX = 40
# A ready summary: at most this many characters of plain words on what the person now sees or can
# do (as Shepherdr's SUMMARY_MAX); the rest goes in --details or the proof.
SUMMARY_MAX = 160
REASONS = {"plan": "Plan to approve", "permission": "Asks permission", "question": "Question for you",
           "reply": "Waits for your reply", "signed_out": "Signed out", "trust": "Asks to trust this folder"}

# Claude Code's folder-trust dialog (#755): shown before it reports any status, so to Herdr the
# agent looked like it was starting, then Working, until it stalled. Read off its screen.
TRUST = re.compile(r"Quick safety check|Do you trust the files in this folder|Yes, I trust this folder", re.I)
_trust_seen = {}


def trust_asked(live):
    """The address of a live agent showing Claude Code's folder-trust dialog, else None. Only an
    agent that never reported a status nor took a turn: its screen is read at most every 5 s."""
    from .base import run
    for k in live:
        if (status_word(k) or k.get("turn")
                or (label_of(k.get("labels"), "agent") or "claude") != "claude"
                or label_of(k.get("labels"), "role") == "reviewer"):
            continue
        address = k.get("address")
        seen = _trust_seen.get(address)
        if not seen or now() - seen[0] > 5000:
            screen = run(["hc", "read", address, "--lines", "40"]).stdout or ""
            seen = _trust_seen[address] = (now(), bool(TRUST.search(screen)))
        if seen[1]:
            return address
    return None


def fit(text, n=LINE_MAX):
    """One line of at most n characters, cut at a word ("…" at the end) when longer."""
    text = " ".join(str(text or "").split())
    if len(text) <= n:
        return text
    cut = text[:n - 1]
    if " " in cut and text[n - 1] != " ":
        cut = cut[:cut.rindex(" ")]
    return cut.rstrip(" ,;:.-") + "…"


def log_lines(log, size=262144):
    """The last lines of an agent's session log, as text; [] when it can't be read. A first line
    cut in half is left out."""
    if not log:
        return []
    try:
        with open(log, "rb") as f:
            f.seek(0, 2)
            at = max(0, f.tell() - size)
            f.seek(at)
            lines = f.read().decode("utf-8", "replace").split("\n")
    except OSError:
        return []
    return lines[1:] if at else lines


def log_tail(log, size=262144):
    """The last lines of an agent's session log (Claude Code's or Codex's JSONL), parsed; [] when
    it can't be read. A first line cut in half is left out."""
    out = []
    for raw in log_lines(log, size):
        try:
            d = json.loads(raw)
        except ValueError:
            continue
        if isinstance(d, dict):
            out.append(d)
    return out


def agent_log(task, address=None):
    """The log of the task's newest agent (at `address`), else of an earlier one with the same session
    (a resumed agent's own log isn't found yet); never another conversation's."""
    a = task.get("agents") or []
    at = [i for i, x in enumerate(a) if address is None or x.get("address") == address]
    if not at:
        return None
    last = a[at[-1]]
    return next((x["log"] for x in [last] + a[:at[-1]][::-1] if x.get("log") and (x is last or last.get("session") and x.get("session") == last["session"])), None)


def said(d):
    """An assistant line's words (Claude Code's, or Codex's response item), or None for anything else."""
    msg = d.get("message") if d.get("type") == "assistant" and not d.get("isSidechain") and not d.get("isApiErrorMessage") else None
    if d.get("type") == "response_item" and (d.get("payload") or {}).get("role") == "assistant":
        msg = d["payload"]
    if msg is None:
        return None
    parts = [c.get("text") for c in msg.get("content") or [] if isinstance(c, dict) and c.get("type") in ("text", "output_text")]
    return "\n".join(x for x in parts if x).strip()


def from_person(d):
    """A line that is someone writing to the agent (not a tool's result): its words, else None."""
    if d.get("type") == "user" and not d.get("isSidechain"):
        c = (d.get("message") or {}).get("content")
        if isinstance(c, str):
            return c
        if isinstance(c, list) and not any(isinstance(x, dict) and x.get("type") == "tool_result" for x in c):
            return "\n".join(x.get("text") or "" for x in c if isinstance(x, dict) and x.get("type") == "text")
    if d.get("type") == "event_msg" and (d.get("payload") or {}).get("type") == "user_message":
        return str(d["payload"].get("message") or "")
    return None


SHORT_ASK = 25
# Herdr's chat tags in an agent's words (mods/chat/log.js): reactions and <hc no-reply/>, a message
# from another terminal, task and GitHub news, <no-reply>. None of it is the agent speaking.
CHAT_TAGS = re.compile(r"<(hc|herdr-news|herdr-github)\b[^>]*>[\s\S]*?</\1\s*>|<hc\b[^>]*/>|</?no-reply\b[^>]*>|<hc\b[^\n]*")


def spoken(text):
    """An agent's words without Herdr's chat tags: "" when nothing else is left."""
    return CHAT_TAGS.sub("", str(text or "")).strip()


def asking_sentence(text, limit=LINE_MAX):
    """The last sentence of a message that asks (ends in "?"), or "" when it asks without one. A
    short one ("Go?", "Shall I?") says nothing alone: the sentence before it makes it a question
    ("I'll take the window 30s to test the menu. Go?" -> "I'll take the window 30s to test the menu?")."""
    lines = [x.strip(" *_`>-#") for x in text.split("\n") if x.strip(" *_`>-#")]
    sentences = [x.strip(" *_`") for ln in lines for x in re.split(r"(?<=[.!?:])\s+", ln) if x.strip(" *_`")]
    for i in range(len(sentences) - 1, -1, -1):
        ask = sentences[i]
        if not ask.rstrip(" *_`\"')]").endswith("?"):
            continue
        before = sentences[i - 1] if i else ""
        if len(ask) >= SHORT_ASK or not before:
            return ask
        if len(f"{before} {ask}") <= limit:
            return f"{before} {ask}"
        before = before.rstrip(" .:;!,")
        return before + "?" if len(before) < limit else fit(before, limit - 1).rstrip("…") + "…?"
    return ""


def last_question(task, address=None):
    """Why an agent that ended its turn without saying ready waits: the question its last message
    asks, cut to LINE_MAX (Shepherdr: a "?" anywhere in its last words), "Waits for your reply" when
    no sentence can be told, or None when it didn't ask. Only the log's tail is read."""
    for d in reversed(log_tail(agent_log(task, address), 65536)):
        if from_person(d) is not None:
            return None  # the person wrote after it: no open question
        text = spoken(said(d))
        if not text:
            continue
        if not re.search(r"\?(?=[\s*_`\"')\]]|$)", text):
            return None
        q = asking_sentence(text)
        return fit(q) if q else REASONS["reply"]
    return None


def turn_began(task, address=None):
    """When its agent's current turn began: the last time someone wrote to it (its log's
    timestamp, ms), or None when its log doesn't say."""
    for d in reversed(log_tail(agent_log(task, address), 131072)):
        if from_person(d) is not None:
            try:
                return int(datetime.datetime.fromisoformat(str(d.get("timestamp")).replace("Z", "+00:00")).timestamp() * 1000)
            except ValueError:
                return None
    return None


def own_line(task, address=None):
    """The agent's own words for why it waits: its latest status line (herdr-task status), when it
    set one since its turn began. Agents set it before they ask or stop to wait; what Herdr
    works out (its question, its last sentence) is only for when it didn't."""
    p = task.get("progress") or {}
    # Herdr's own words ("Working again": back_to_work()) are never the agent's reason (SHEP-133).
    if not p.get("text") or p.get("by"):
        return None
    began = turn_began(task, address)
    if began and (p.get("set") or p.get("at") or 0) < began:
        return None
    text = fit(p["text"])
    return text[:1].upper() + text[1:]


def last_words(task, address=None):
    """Why an agent that ended its turn without saying ready waits: the question its last message
    asks, else that message's last sentence, cut to LINE_MAX at a word ("Waiting for your go on
    the screenshot…"), else "Waits for your reply" (nothing said since the person last wrote)."""
    for d in reversed(log_tail(agent_log(task, address), 65536)):
        if from_person(d) is not None:
            break
        text = spoken(said(d))
        if not text:
            continue
        q = last_question(task, address)
        if q:
            return q
        text = re.sub(r"```.*?(```|$)", "", text, flags=re.S)
        lines = [x.strip(" *_`>-#|") for x in text.split("\n") if x.strip(" *_`>-#|")]
        for ln in reversed(lines):
            parts = [x.strip(" *_`") for x in re.split(r"(?<=[.!?])\s+", ln) if x.strip(" *_`")]
            if parts:
                return fit(parts[-1])
        break
    return REASONS["reply"]


def permission_words(tool, value, cwd=None):
    """What a permission is for, as the chat's card says it (mods/chat/asks.js words()): "Run:
    npm test", "Edit mods/chat/main.js", "Fetch github.com"; None for a tool it can't name."""
    def rel(f):
        f = str(f or "")
        r = f[len(cwd) + 1:] if cwd and f.startswith(cwd + "/") else "~" + f[len(HOME):] if f.startswith(HOME + "/") else f
        return r if len(r) <= 32 else os.path.basename(f)
    first = lambda x: str(x or "").split("\n")[0]
    if tool == "Bash" or (tool in ("shell", "exec_command", "local_shell") and value.get("command")):
        cmd = value.get("command") or value.get("cmd")
        return f"Run: {first(' '.join(cmd) if isinstance(cmd, list) else cmd)}" if cmd else None
    if tool in ("Write", "Read"):
        return f"{tool} {rel(value.get('file_path'))}" if value.get("file_path") else None
    if tool in ("Edit", "MultiEdit", "NotebookEdit"):
        f = value.get("file_path") or value.get("notebook_path")
        return f"Edit {rel(f)}" if f else None
    if tool in ("Glob", "Grep") and value.get("pattern"):
        return f"Search for {value['pattern']}"
    if tool == "WebFetch" and value.get("url"):
        return f"Fetch {re.sub(r'^[a-z]+://', '', str(value['url'])).split('/')[0]}"
    if tool == "WebSearch" and value.get("query"):
        return f"Search the web: {value['query']}"
    m = re.match(r"^mcp__(.+?)__(.+)$", str(tool or ""))
    return f"Use {m.group(1)}: {m.group(2)}" if m else (f"Use {tool}" if tool else None)


QUESTION_TOOLS = ("AskUserQuestion", "request_user_input", "ask_question")
ASK_NEGATIVE = {"no", "nope", "nah", "don't", "dont", "stop", "cancel", "never", "deny", "decline", "reject", "wait", "hold", "negative", "do not"}


def pending_call(task, address):
    """The tool call an agent's session log left open: (name, input dict), or (None, {})."""
    done, pending = set(), None
    for d in log_tail(agent_log(task, address), 131072):
        c = (d.get("message") or {}).get("content") if d.get("type") in ("assistant", "user") and not d.get("isSidechain") else None
        for x in c if isinstance(c, list) else []:
            if isinstance(x, dict) and x.get("type") == "tool_result":
                done.add(x.get("tool_use_id"))
            elif isinstance(x, dict) and x.get("type") == "tool_use":
                pending = x
        p = d.get("payload") or {}
        if d.get("type") == "response_item" and p.get("type") in ("function_call", "custom_tool_call"):
            try:
                args = json.loads(p.get("arguments") or "{}")
            except (TypeError, ValueError):
                args = {}
            pending = {"id": p.get("call_id"), "name": p.get("name"), "input": args if isinstance(args, dict) else {}}
        elif d.get("type") == "response_item" and p.get("type") in ("function_call_output", "custom_tool_call_output"):
            done.add(p.get("call_id"))
    if pending and pending.get("id") not in done:
        return pending.get("name"), pending.get("input") if isinstance(pending.get("input"), dict) else {}
    return None, {}


# A worker blocked in its CLI's own question dialog (field note 51): every question it holds goes in
# its Needs-you why and news, so the Assistant can relay them all; this many characters at most,
# shared out so a long first question never hides the rest (WUKO-77's ran 300+ characters each).
DIALOG_MAX = 480


def dialog_why(tool, value):
    """"Asked in a blocking dialog (4 questions): q1 | q2 …" for a question tool's call, else None."""
    if tool not in QUESTION_TOOLS or not isinstance(value, dict):
        return None
    qs = [" ".join(str(q.get("question") or "").split()) for q in value.get("questions") or [] if isinstance(q, dict)]
    qs = [q for q in qs if q] or ([" ".join(str(value.get("question") or "").split())] if value.get("question") else [])
    if not qs:
        return None
    head = f"Asked in a blocking dialog ({len(qs)} questions): " if len(qs) > 1 else "Asked in a blocking dialog: "
    share = max(LINE_MAX, (DIALOG_MAX - len(head) - 3 * (len(qs) - 1)) // len(qs))
    return fit(head + " | ".join(fit(q, share) for q in qs), DIALOG_MAX)


def blocking_dialog(task, address, req=None):
    """dialog_why() of the question dialog an agent's pane is blocked on: the ask Herdr's hook holds
    (req), else the tool call its log left open. None when it isn't one (a permission, a plan)."""
    if isinstance(req, dict) and req.get("tool_name"):
        return dialog_why(req.get("tool_name"), req.get("tool_input"))
    return dialog_why(*pending_call(task, address)) if address else None


def ask_reason(task, address, req=None, kind=None, msg=None):
    """Why a blocked agent waits, in at most LINE_MAX characters: signed out when its own report
    says so (OSC 7501 kind auth); else from the ask Herdr's hook holds (req: Claude's
    PermissionRequest), else the tool call its log left open: a plan, its question tool (the
    question itself, cut to fit), any other tool (what the permission is for: "Run: npm test");
    else what its report says (msg: Claude's "approve Bash: npm test")."""
    if kind == "auth":
        return REASONS["signed_out"]
    tool, value = None, {}
    if isinstance(req, dict):
        tool, value = req.get("tool_name"), req.get("tool_input") if isinstance(req.get("tool_input"), dict) else {}
    if not tool:
        tool, value = pending_call(task, address)
    if tool == "ExitPlanMode":
        return REASONS["plan"]
    if tool in QUESTION_TOOLS:
        return dialog_why(tool, value) or REASONS["question"]
    if tool:
        words = permission_words(tool, value, req.get("cwd") if isinstance(req, dict) else (task.get("worktree") or task.get("folder")))
        return fit(words) if words else REASONS["permission"]
    if kind == "permission":
        m = re.match(r"^approve\s+(\S+?):\s*(.+)$", str(msg or "").strip(), re.I)
        if m:
            return fit({"Bash": "Run: ", "WebFetch": "Fetch "}.get(m.group(1), m.group(1) + " ") + m.group(2))
        return REASONS["permission"]
    return last_question(task, address) or REASONS["question"]


def _ask_shape(question, options=(), recommend=None, defaults=("Yes, go ahead", "No, skip")):
    """Shapes a worker's question and options for `herdr-task ask` (FAC-70)."""
    q = fit(" ".join(str(question or "").split()), 260)
    if not q:
        return None
    strip = lambda s: re.sub(r"\s\(([a-z0-9]{1,6})\)$", r" — \1",
                             re.sub(r"^\(recommended\)\s*|\s*\(recommended\)$", "", s, flags=re.I).strip(), flags=re.I)
    cleaned, rec = [], strip(" ".join(str(recommend).split())) if recommend else None
    for raw in options or []:
        raw_s = " ".join(str(raw.get("label") if isinstance(raw, dict) else raw or "").split())
        s = strip(raw_s)
        if s and s not in cleaned:
            cleaned.append(s)
            if not rec and re.search(r"^\(recommended\)\s*|\s*\(recommended\)$", raw_s, re.I):
                rec = s
    cleaned = (cleaned + [d for d in defaults if d not in cleaned])[:max(2, min(9, len(cleaned)))]
    rec = rec if rec in cleaned else cleaned[0]
    words = [w.strip(".!?,;:") for w in rec.lower().split()]
    first = " ".join(words[:2]) if words[:2] == ["do", "not"] else (words or [""])[0]
    if first in ASK_NEGATIVE and not any(o.lower().startswith("yes") for o in cleaned):
        idx = next((i for i, o in enumerate(cleaned) if o != rec and (o.lower().split() or [""])[0].strip(".!?,;:") not in ASK_NEGATIVE), None)
        cleaned = ([*cleaned[:idx], f"Yes — {cleaned[idx]}", *cleaned[idx + 1:]] if idx is not None and f"Yes — {cleaned[idx]}" not in cleaned
                   else ["Yes, go ahead", *[o for o in cleaned if o != "Yes, go ahead"]][:9])
    return {"question": q, "options": cleaned, "recommend": rec if rec in cleaned else cleaned[-1]}


def _tool_question(task, tool, value, cwd=None):
    if tool == "ExitPlanMode":
        return _ask_shape("Approve the agent's plan?", defaults=("Yes, approve", "No, skip"))
    if tool in QUESTION_TOOLS:
        qs = [q for q in (value.get("questions") if isinstance(value.get("questions"), list) else []) if isinstance(q, dict) and str(q.get("question") or "").strip()]
        q_text = "; ".join(str(q["question"]).strip() for q in qs) or " ".join(str(value.get("question") or "").split()) or REASONS["question"]
        return _ask_shape(q_text, qs[0].get("options") if qs and isinstance(qs[0].get("options"), list) else [])
    words = permission_words(tool, value, cwd or task.get("worktree") or task.get("folder")) if tool else None
    desc = " ".join(str(value.get("description") or "").split())
    text = f"{desc} ({words})?" if desc and words and desc.lower() not in words.lower() else f"Allow {words}?" if words else "Allow the agent's tool request?"
    return _ask_shape(text, defaults=("Yes, allow", "No, skip")) if tool else None


def full_question(task, address=None, ready_turn=False):
    """The full question an idle agent's last message asks (up to 260 chars), or None."""
    for d in reversed(log_tail(agent_log(task, address), 65536)):
        if from_person(d) is not None:
            return None
        text = re.sub(r"```.*?(```|$)", "", spoken(said(d)), flags=re.S).strip()
        if not text:
            continue
        lines = [x.strip(" *_`>-#") for x in text.split("\n") if x.strip(" *_`>-#")]
        if (not lines or not lines[-1].rstrip(" *_`\"')]").endswith("?")) if ready_turn else not re.search(r"\?(?=[\s*_`\"')\]]|$)", text):
            return None
        return asking_sentence(text, limit=260) or None
    return None


def open_question(task, k):
    """An open question from live worker terminal `k` (an open question/permission tool call, a
    blocked prompt, or an unanswered question at the end of its turn), shaped for `herdr-task ask`
    (FAC-70), or None when `k` isn't asking."""
    fg = k.get("foreground") or {}
    if (label_of(k.get("labels"), "role") in ("reviewer", "coordinator", "ask")
            or (fg.get("name") and not agent_running(k)) or (not fg.get("name") and k.get("agent") in SHELLS)):
        return None
    address, st = k.get("address"), k.get("status") if isinstance(k.get("status"), dict) else {}
    kind, msg, word = st.get("kind"), " ".join(str(st.get("msg") or "").split()), status_word(k)
    tool, value = pending_call(task, address)
    if tool in (*QUESTION_TOOLS, "ExitPlanMode") or (tool and word == "blocked" and kind != "auth"):
        return _tool_question(task, tool, value)
    if word == "blocked" and kind != "auth":
        if kind == "permission":
            m = re.match(r"^approve\s+(\S+?):\s*(.+)$", str(msg or "").strip(), re.I)
            text = ("Allow " + {"Bash": "Run: ", "WebFetch": "Fetch "}.get(m.group(1), m.group(1) + " ") + m.group(2) + "?") if m else (msg or "Allow the agent's permission request?")
            return _ask_shape(text, defaults=("Yes, allow", "No, skip"))
        if kind == "plan":
            return _ask_shape(msg or "Approve the agent's plan?", defaults=("Yes, approve", "No, skip"))
        return _ask_shape(msg or full_question(task, address) or own_line(task, address) or "Question left open when the task finished — check its terminal?")
    if word in ("idle", "done"):
        log = agent_log(task, address) or agent_log(task)
        if fg_job(k) or (log and (background_jobs(log)[0] or open_tool(log))):
            return None
        try:
            key = label_of(k.get("labels"), "task")
            if (key and any(parent_of(h) == address and status_word(h) in ("working", "blocked") for h in live_helpers(key))) \
                    or (address and cli_of(k) == "agy" and agy_bg_tasks(address)):
                return None
        except (OSError, Fail):
            pass
        rturns = task.get("ready_turns") or {}
        q = full_question(task, address, ready_turn=address in rturns and rturns.get(address) == (k.get("turn") or 0))
        return _ask_shape(q) if q else None
    return None


def asking(k):
    """Whether a live agent asks the person now (its own status says blocked)."""
    return status_word(k) == "blocked"


STARTING_MS = 90_000
REVIEWER_ON_IT = "Herdr's reviewer is on it"


def review_running(task):
    """A herdr-task review round for this task's head is still open (FAC-66)."""
    head = task.get("head")
    if not head:
        return False
    done = {r.get("round") for r in task.get("reviews") or [] if r.get("started_by") == "herdr-task"}
    failed = {f.get("address") for f in task.get("review_failures") or [] if f.get("address")}
    return any(r.get("head") == head and r.get("round") not in done and r.get("address") not in failed
               for r in task.get("review_runs") or [])


def starting(task):
    """An agent was started for it under STARTING_MS ago and hasn't ended."""
    last = (task.get("agents") or [None])[-1]
    if last and last.get("ended"):
        return False
    failed = {f.get("address") for f in task.get("review_failures") or []}
    runs = [r for r in task.get("review_runs") or [] if r.get("address") not in failed]
    at = max([x for x in (task.get("started"), (last or {}).get("at"), (task.get("review") or {}).get("at"),
                          (runs[-1] if runs else {}).get("at")) if isinstance(x, (int, float))] or [0])
    return now() - at < STARTING_MS


# ---- its own work in the background -------------------------------------------------------------
# An agent that ended its turn with its own work still running (SHEP-53) is not waiting on the
# person: Claude Code's background shells, Monitors and background agents (their start has an id,
# a <task-notification> with a status ends it), Codex's background terminals (a process left
# running with a session id, until an output says it exited). Or it said so: its status line, set
# this turn and still fresh, says it waits on CI, a check, a review or a background job ("Waiting on
# review round 1", "Codex reviewing the change"), and no background job of its ended since.
# It asks the person something (a question tool or permission: blocked): Needs you all the same; a
# plain question in its last message only once nothing of its own runs (SHEP-148). As core/tasks.js place() with task.own_work, which news_pass keeps.
WAITING_BG = "Waiting on a background job"
BG_LOG_BYTES = 64 * 1048576  # a first look at a log reads at most this much of its end
# A tool call (Claude Code's log line, compact JSON) and its result; one open longer than FG_MAX_MS
# (a foreground command times out at 10 minutes) no longer counts as running.
TOOL_USE = re.compile(r'"type":\s*"tool_use",\s*"id":\s*"([^"]+)",\s*"name":\s*"([^"]+)"')
TOOL_DONE = re.compile(r'"tool_use_id":\s*"([^"]+)"')
TIMESTAMP = re.compile(r'"timestamp":\s*"([^"]+)"')
SIDECHAIN = re.compile(r'"isSidechain":\s*true')
FG_MAX_MS = 15 * 60000
# Only lines with one of these are parsed: a log's screenshots are megabytes of lines that aren't.
BG_HINTS = ("<task-notification>", "task_id", "backgroundTaskId", "timeoutMs", "isAsync", "in background with ID",
            "Monitor started", "Async agent launched", "session_id", "session ID",
            # Codex (SHEP-207): a poll's result without its session id, a wait on a code-mode cell
            "exit_code", "Process exited", "cell_id", "cell ID")
BG_MAX_MS = 3 * 3600_000  # a background job started longer ago than this no longer counts
BG_STARTED = re.compile(r"Command running in background with ID: (\w+)|Monitor started \(task (\w+)|Async agent launched[\s\S]*?agentId: (\w+)")
# A Monitor that expired says so in its <event>, with no <status> (FAC-96: WUKO-67's stayed running).
BG_ENDED = re.compile(r"<task-id>(\w+)</task-id>(?:(?!</task-notification>)[\s\S])*?(?:<status>(\w+)</status>|\[Monitor expired\b)")
CODEX_RUNNING = re.compile(r"Process running with session ID (\d+)")
# Codex 0.163+ code mode (SHEP-207): a script starts commands and polls them with tools.exec_command /
# tools.write_stdin, all inside one exec call. Its output says "Script running with cell ID N" while
# it waits, and a wait call names that cell; the script's results come as one JSON array, in the order
# its calls were made, and a poll's result drops its session_id once the command has exited.
CODEX_CELL = re.compile(r"Script running with cell ID (\d+)")
CODEX_CELL_ARG = re.compile(r'"cell_id"\s*:\s*"?(\d+)')
CODEX_SESSION_ARG = re.compile(r"session_id[\"']?\s*:\s*[\"']?(\d+)[\"']?\s*(?=[,}])")
CODEX_HOST = "codex-code-mode-host"  # Codex's code-mode process: no job of its own
WAITS_ON = re.compile(
    r"\b(wait\w*|watch\w*|polling)\b.*\b(ci|checks?|reviews?|reviewer|build\w*|tests?|background|jobs?|deploy\w*|"
    r"release|github|pipeline|actions?|run|workflow|simulator|xcodebuild)\b|\breviewing\b|"
    r"\b(build\w*|run\w*|test\w*) in (?:the )?background\b|"
    r"\bbackground (?:job|shell|build|test|check|process|command|run)\w*(?:\s+(?:is\s+)?(?:running|in progress|active)\b|:)|"
    r"\b(?:build|tests?|checks?|ci|xcodebuild)\s+(?:is\s+)?(?:running|in progress)\b", re.I)
WAITS_ON_PERSON = re.compile(r"\b(you|your|yours|person|human|answer|reply|approv\w*|decid\w*|decision|confirm\w*|ok|okay|"
                             r"input|accept\w*|sign(?:ed)?[- ]?in|login|a go|go-ahead)\b", re.I)
_bg_cache = {}


def _ms(stamp):
    try:
        return int(datetime.datetime.fromisoformat(str(stamp).replace("Z", "+00:00")).timestamp() * 1000)
    except ValueError:
        return None


def _text(x):
    """A tool result's or output's words, whatever its shape."""
    if isinstance(x, str):
        return x
    if isinstance(x, list):
        return "\n".join(_text(y) for y in x)
    if isinstance(x, dict):
        return _text(x.get("text") if "text" in x else x.get("content"))
    return ""


# ---- a background job that ended while its worker sat quiet (FAC-106) -----------------------------
# A worker shown Waiting on a background job whose jobs all ended without a notice in its log (Claude
# Code's shell for it, or Codex's process, gone: WUKO-79, WUKO-99) and that took no turn for BG_NUDGE_MS is
# told once per job (tick's bg_ended_pass, kept as task["bg_told"]); no turn BG_GRACE_MS after that:
# Needs you, saying so. While any job of its own still runs, it is left alone.
BG_NUDGE_MS = BG_GRACE_MS = 5 * 60000
BG_ENDED_WHY = "Waits on a background job that ended"
BG_ENDED_MSG = ("Your background job {} ended (no longer running); check its output and carry on.",
                "Your background jobs {} ended (no longer running); check their output and carry on.")


def turn_since(told, live):
    """Its told agent took a turn since: a finished turn more, or its status changed after."""
    return any(k.get("address") == told.get("address") and ((k.get("turn") or 0) > (told.get("turn") or 0)
                                                          or (k.get("status_ms") or 0) > told["at"]) for k in live)


def descendants(k):
    """The args of every process under its agent, or None when its pid is unknown."""
    pid = (k.get("foreground") or {}).get("pid") or (k.get("address") and (detail(k).get("foreground") or {}).get("pid"))
    if not pid:
        return None
    children, out, todo = {}, [], [str(pid)]
    for line in run(["ps", "-axo", "pid=,ppid=,args="]).stdout.splitlines():
        parts = line.split(None, 2)
        if len(parts) >= 2:
            children.setdefault(parts[1], []).append(parts)
    while todo:
        for parts in children.pop(todo.pop(), []):
            todo.append(parts[0])
            out.append(parts[2] if len(parts) > 2 else "")
    return out


def ended_jobs(task, k):
    """{id: name} of the background jobs its agent's log says still run whose process is gone (Claude Code's
    shell for it, Codex's process; an ended one's notice already ends Waiting on a background job); {} while
    any of its own still runs, or might (a background agent or WebSocket Monitor: no process; its pid unknown)."""
    if fg_job(k) or (k.get("address") and cli_of(k) == "agy" and agy_bg_tasks(k["address"])):
        return {}
    log = agent_log(task, k.get("address")) or agent_log(task)
    running = background_jobs(log)[0] if log else {}
    jobs, out = (_bg_cache.get(log) or {}).get("jobs") or {}, {}
    shells = [i for i in running if not i.startswith("codex:")]
    if any((jobs.get(i) or {"opaque": True})["opaque"] for i in shells):
        return {}
    if shells:
        procs = descendants(k)
        if procs is None or any("shell-snapshots" in a for a in procs):
            return {}
        out.update({i: jobs[i]["name"] for i in shells})
    codex = [i for i in running if i.startswith("codex:")]
    if codex and codex_working(k):
        return {}
    out.update({i: i for i in codex})
    return out


def background_jobs(log):
    """(running, last_ended): its agent's background jobs still running by its session log ({id:
    started ms}), and when the last one ended (ms, or 0). Read as the log grows: only what was
    added since the last look, so a job started before megabytes of screenshots still counts
    (SHEP-133: a 1 MB tail lost it, and its quiet agent read Needs you)."""
    try:
        st = os.stat(log)
    except (OSError, TypeError):
        return {}, 0
    c = _bg_cache.get(log)
    if not c or c["ino"] != st.st_ino or st.st_size < c["offset"]:
        c = {"ino": st.st_ino, "offset": max(0, st.st_size - BG_LOG_BYTES), "running": {}, "ended": 0, "calls": {}, "cells": {}, "cut": True}
        _bg_cache[log] = c
    if st.st_size > c["offset"]:
        try:
            with open(log, "rb") as f:
                f.seek(c["offset"])
                chunk = f.read(st.st_size - c["offset"])
        except OSError:
            chunk = b""
        if c.pop("cut", False) and c["offset"]:
            nl = chunk.find(b"\n")  # a first line cut in half is left out
            c["offset"] += nl + 1 if nl >= 0 else 0
            chunk = chunk[nl + 1:] if nl >= 0 else b""
        end = chunk.rfind(b"\n") + 1  # a last line still being written waits for its end
        c["offset"] += end
        _bg_scan(chunk[:end].decode("utf-8", "replace").split("\n"), c)
    return {i: t for i, t in c["running"].items() if now() - t < BG_MAX_MS}, c["ended"]


def open_tool(log):
    """The tool call its agent (Claude Code) has open in its log's main thread, started under
    FG_MAX_MS ago: (name, started ms), or None. A question tool or a plan is no work of its own."""
    background_jobs(log)
    c = _bg_cache.get(log) or {}
    calls = c.get("open") or {}
    for i in [i for i, (_, at) in calls.items() if now() - at >= FG_MAX_MS]:
        calls.pop(i)
    mine = [x for x in calls.values() if x[0] not in ("AskUserQuestion", "ExitPlanMode")]
    return max(mine, key=lambda x: x[1]) if mine else None


def _bg_scan(lines, seen):
    """background_jobs()'s reading of new lines, into seen: {running, calls, ended, open}. Tool
    calls open and done are read off the raw line (an image's result is megabytes of JSON)."""
    running, calls, ended = seen["running"], seen["calls"], seen["ended"]
    cells = seen.setdefault("cells", {})  # Codex code-mode cell id -> the session ids its wait polls
    opened = seen.setdefault("open", {})
    jobs, names = seen.setdefault("jobs", {}), seen.setdefault("names", {})  # FAC-106: each job's name, and whether a process shows it
    for raw in lines:
        # Claude Code keeps a copy of each request it sends (api-request-blob): every call and
        # result so far again, never news of one.
        if '"api-request' in raw[:200]:
            continue
        if '"tool_use"' in raw and not SIDECHAIN.search(raw):
            stamp = TIMESTAMP.search(raw)
            for m in TOOL_USE.finditer(raw):
                opened[m.group(1)] = (m.group(2), (_ms(stamp.group(1)) if stamp else None) or now())
            if '"Bash"' in raw or '"Monitor"' in raw:  # a Bash may go to the background later (its timeout)
                try:
                    content = (json.loads(raw).get("message") or {}).get("content") or []
                except (ValueError, AttributeError):
                    content = []
                for c in content if isinstance(content, list) else []:
                    if isinstance(c, dict) and c.get("type") == "tool_use" and isinstance(c.get("input"), dict):
                        inp = c["input"]  # a WebSocket Monitor has no process of its own to look for
                        names[c.get("id")] = (" ".join(str(inp.get("description") or inp.get("command") or "").split())[:80], "ws" in inp)
                for i in list(names)[:-100]:
                    names.pop(i)
        if '"tool_use_id"' in raw:
            for m in TOOL_DONE.finditer(raw):
                opened.pop(m.group(1), None)
        if any(h in raw for h in BG_HINTS):
            try:
                d = json.loads(raw)
            except ValueError:
                continue
            if not isinstance(d, dict):
                continue
            at = _ms(d.get("timestamp")) or now()
            # Claude Code: ended by its notice (in the queue, an attachment or a user line), or stopped.
            if "<task-notification>" in raw and (d.get("type") in ("queue-operation", "attachment") or from_person(d) is not None):
                for m in BG_ENDED.finditer(raw):
                    running.pop(m.group(1), None)
                    ended = max(ended, at)
            r = d.get("toolUseResult")
            if isinstance(r, dict) and r.get("task_id") and "stopped" in str(r.get("message") or "").lower():
                running.pop(str(r["task_id"]), None)
                ended = max(ended, at)
            if d.get("type") == "user" and '"tool_result"' in raw and not d.get("isSidechain"):
                ids = []
                if isinstance(r, dict):
                    ids = [r.get("backgroundTaskId") or (r.get("taskId") if r.get("timeoutMs") is not None else None)
                           or (r.get("agentId") if r.get("isAsync") else None)]
                tid = None
                for c in (d.get("message") or {}).get("content") or []:
                    if isinstance(c, dict) and c.get("type") == "tool_result":
                        tid = tid or c.get("tool_use_id")
                        m = BG_STARTED.match(_text(c.get("content")).strip())
                        ids += [next(g for g in m.groups() if g)] if m else []
                for i in filter(None, ids):
                    running.setdefault(str(i), at)
                    name, ws = names.pop(tid, None) or (None, False)
                    jobs.setdefault(str(i), {"at": at, "name": name or str(i), "opaque": ws or "Async agent launched" in raw
                                             or bool(isinstance(r, dict) and r.get("isAsync"))})
            # Codex: a process left running has a session id until an output says it exited.
            p = d.get("payload") or {}
            if d.get("type") == "response_item" and p.get("type") in ("function_call", "custom_tool_call"):
                calls[p.get("call_id")] = str(p.get("arguments") or p.get("input") or "")
            elif d.get("type") == "response_item" and p.get("type") in ("function_call_output", "custom_tool_call_output"):
                call = calls.get(p.get("call_id")) or ""
                cell = CODEX_CELL_ARG.search(call)  # a wait on a code-mode cell: the sessions it polls
                polled = cells.get(cell.group(1), []) if cell else _codex_polled(call)
                if _codex_output(p.get("output"), polled, cells, running, at):
                    ended = max(ended, at)
    seen["ended"] = ended
    for i in [i for i, j in jobs.items() if now() - j["at"] >= BG_MAX_MS]:
        jobs.pop(i)


def _codex_polled(call):
    """Recognise literal polls and the numeric for-of batch emitted by Codex.

    Keep command slots too: an exec_command result must never end a neighbouring poll.
    Unknown JavaScript stays unresolved; never execute an agent's script to read its log.
    """
    loop = re.compile(r"for\s*\(const\s+(\w+)\s+of\s+\[([\d,\s]+)\]\s*\)\s*\{(.*?)\}\s*\}", re.S)
    def expand(m):
        body = m.group(3) + "}"
        return "\n".join(re.sub(r"session_id\s*:\s*" + re.escape(m.group(1)) + r"\b",
                                    "session_id:" + sid.strip(), body)
                         for sid in m.group(2).split(",") if sid.strip())
    call = loop.sub(expand, call)
    slots = []
    for m in re.finditer(r"tools\.(exec_command|write_stdin)\s*\(\s*\{([^{}]*)\}", call):
        sid = CODEX_SESSION_ARG.search(m.group(2) + "}") if m.group(1) == "write_stdin" else None
        slots.append(sid.group(1) if sid else None)
    if slots:
        return slots
    try:  # legacy write_stdin takes a JSON argument object
        sid = json.loads(call).get("session_id")
        return [str(sid)] if isinstance(sid, (int, str)) and str(sid).isdigit() else []
    except (ValueError, AttributeError):
        return []


def _codex_results(output):
    """Read result envelopes, never JSON or session-id examples inside command stdout."""
    results = []
    blocks = output if isinstance(output, list) else [output]
    def add(value):
        if isinstance(value, list):
            for item in value:
                add(item)
        elif isinstance(value, dict):
            if value.get("status") == "fulfilled":
                add(value.get("value"))
            elif value.get("status") == "rejected":
                results.append({})  # preserves its place in allSettled
            elif "session_id" in value or "exit_code" in value:
                results.append(value)
    for block in blocks:
        text = _text(block)
        try:
            add(json.loads(text))
        except ValueError:
            pass
    return results


def _codex_output(output, polled, cells, running, at):
    """One Codex output's word on the sessions it covers, applied to running ({"codex:<id>": started
    ms}). polled: the session ids the call that made it polled, in order (a code-mode script's
    calls, or a write_stdin's own). Returns True when a session ended."""
    text = _text(output)
    ended = False
    results = _codex_results(output)
    # Legacy plain-text output only. Structured command stdout is not tool metadata.
    if not results:
        for m in CODEX_RUNNING.finditer(text):
            running.setdefault("codex:" + m.group(1), at)
    cell = CODEX_CELL.search(text)
    if cell:
        cells[cell.group(1)] = polled
    for i, d in enumerate(results):
        sid = d.get("session_id")
        if sid is None and len(results) == len(polled):
            sid = polled[i]  # an exited command's poll drops its session id: its place in the script says which
        if sid is None:
            continue
        key = "codex:" + str(sid)
        if d.get("exit_code") is not None:
            ended = running.pop(key, None) is not None or ended
        elif d.get("session_id") is not None:
            running.setdefault(key, at)
    # a poll of one session whose output says it exited (no session id in it, or the old format's text)
    if len(polled) == 1 and polled[0] is not None and not results and re.search(r"^Process exited with code", text, re.M):
        ended = running.pop("codex:" + str(polled[0]), None) is not None or ended
    return ended


def waits_on_own(task, address=None, ended=0):
    """Its status line, set this turn by the agent itself and still fresh, says it waits on CI, a
    check, a review or a background job, and no background job of its ended since."""
    p = task.get("progress") or {}
    said = p.get("set") or p.get("at") or 0
    # Out of review rounds (ready refused, review_out): it waits on its Assistant's review-more,
    # never on a review of its own, whatever its line says ("waiting for a review round", #784).
    if task.get("review_out"):
        return False
    if p.get("by") or not said or said < ended or not own_line(task, address):
        return False
    left = p.get("left") if isinstance(p.get("left"), (int, float)) else 0
    if now() - said >= max(ON_IT_MS, left):
        return False
    text = str(p.get("text") or "")
    return bool(WAITS_ON.search(text)) and not WAITS_ON_PERSON.search(text)


def fg_job(k):
    """hc get's foreground is a command or subshell still running in the worker's terminal."""
    fg = k.get("foreground") or {}
    argv = fg.get("argv") or []
    if any(x in argv[:4] for x in ("herdr-sandbox", "herdr-clean")) or any('exec "$0"' in str(x) for x in argv[1:4]):
        return False
    name = os.path.basename(fg.get("name") or (argv[0] if argv else "")).lstrip("-")
    if not name or name in AGENT_BINS or AGENT_VER.match(name):
        return False
    if name in SHELLS:
        return runs_command(argv)
    return not (name in ("node", "bun") and (not argv or any(b in str(a).lower() for a in argv[:3] for b in AGENT_BINS)))


def status_waits(k, ended=0):
    """hc get's fresh status/report msg says the worker waits on a background job, build or check."""
    at = k.get("status_ms") or 0
    if at and (at < ended or now() - at >= ON_IT_MS):
        return False
    return any(isinstance(b, dict) and b.get("msg") and WAITS_ON.search(str(b["msg"])) and not WAITS_ON_PERSON.search(str(b["msg"]))
               for b in (k.get("status"), k.get("report")))


def helper_working(task, live):
    for k in live:
        if label_of(k.get("labels"), "role") != "reviewer" and status_word(k) != "blocked":
            address, key = k.get("address"), label_of(k.get("labels"), "task")
            if key and any(parent_of(h) == address and status_word(h) in ("working", "blocked") for h in live_helpers(key)):
                return True
    return False


AGY_BG_TASKS = re.compile(r"\b[1-9]\d*\s+task\(s\)(?!\w)")
BOX_RULE = re.compile(r"^\s*(?:[─━]{8,}|╰[─━]{8,}╯)\s*$")


def agy_bg_tasks(address):
    """agy's footer shows active background work ("1 task(s) · /tasks") below its input box."""
    try:
        out = run(["hc", "read", address, "--lines", "0"]).stdout or ""
    except (OSError, Fail):
        return False
    lines = [ln for ln in out.splitlines() if ln.strip()]
    rules = [i for i, ln in enumerate(lines) if BOX_RULE.match(ln)]
    return bool(AGY_BG_TASKS.search("\n".join(lines[max(len(lines) - 3, rules[-1] + 1 if rules else 0):])))


_BG_ITEMS = r"(\d+\s+(?:shells?|monitors?)(?:\s*,\s*\d+\s+(?:shells?|monitors?))*)"
CLAUDE_BG_SHELLS = (re.compile(r"^\s*[^\w\s]\s+\w+ for \S.*·\s*" + _BG_ITEMS + r"\s+still\s+running\s*$"),  # "✻ Crunched for 2m 55s · done 9:38 AM · 2 shells, 1 monitor still running"
                    re.compile(r"^\s*⏵⏵.*·\s*" + _BG_ITEMS + r"\s*(?:·|$)"))  # "⏵⏵ auto mode on · 2 shells, 1 monitor · ↓ to manage"


def claude_bg_shells(screen):
    """Claude Code's footer shows its background shells (FAC-79): its status line after a finished turn and its mode
    line. Only those two kinds of line in the last lines of the screen count, never prose the reviewer printed."""
    lines = [ln.rstrip() for ln in (screen or "").splitlines() if ln.strip()]
    for ln in lines[-5:]:
        for rx in CLAUDE_BG_SHELLS:
            m = rx.match(ln)
            if m and any(int(n) for n in re.findall(r"(\d+)\s+(?:shell|monitor)", m.group(1))):
                return True
    return False


# Codex starts its tool servers (MCP servers from its config, its own runtime helpers) as its own
# children within a second or two of starting, and they live as long as it does (#946). A command of
# the agent's own starts in a turn, after the model answered: never this soon.
CODEX_STARTUP_S = 5


def _ps_started(stamp):
    """ps's lstart ("Sun Nov 15 10:50:03 2026", in the C locale) as epoch seconds, or None."""
    try:
        return time.mktime(time.strptime(" ".join(stamp.split()), "%a %b %d %H:%M:%S %Y"))
    except (ValueError, OverflowError):
        return None


def codex_working(k):
    """Whether a Codex agent has a process of its own running under it: a background terminal or a
    command of its code mode, but not its code-mode host (SHEP-207), nor what it started with itself:
    its MCP servers and runtime helpers and all under them (#946: they kept finished jobs counting).
    Its pid unknown: True, so its log decides as before. One `ps` for all processes, so it works on
    macOS too."""
    fg = k.get("foreground") or {}
    if not fg.get("pid") and k.get("address"):
        fg = detail(k).get("foreground") or {}
    if not fg.get("pid"):
        return True
    children, started = {}, {}
    out = run(["ps", "-axo", "pid=,ppid=,lstart=,args="], env=dict(os.environ, LC_ALL="C", LANG="C")).stdout
    for line in out.splitlines():
        parts = line.split(None, 7)  # pid, ppid, the five words of lstart, args
        if len(parts) >= 7 and parts[1].isdigit():
            started[parts[0]] = _ps_started(" ".join(parts[2:7]))
            children.setdefault(parts[1], []).append((parts[0], parts[7] if len(parts) > 7 else ""))
    root = str(fg["pid"])
    born = started.get(root)
    todo, seen_pids = [root], set()
    while todo:
        for pid, args in children.get(todo.pop(), []):
            if pid in seen_pids:
                continue
            seen_pids.add(pid)
            if CODEX_HOST in args:
                todo.append(pid)  # its code mode's commands run under it
                continue
            at = started.get(pid)
            if born is not None and at is not None and at - born <= CODEX_STARTUP_S:
                continue  # started with Codex itself: a tool server, never a job (nor anything under it)
            return True
    return False


BG_WORK_MAX_MS = 30 * 60_000  # a task waiting on its own background job stays Working at most 30 minutes


def own_work(task, live, ready=False, since=0):
    """Its quiet agent is on its own work (above). What really runs (a background job, a command
    open in the foreground, an agy background task, a helper of its own at work: SHEP-54) counts
    whatever its last message says: a plain question with work still running is no ask yet
    (SHEP-148; asking for real is its question tool or a permission: blocked). Its status line or
    status msg alone counts only when its last message asks nothing, and never for a Ready task.
    since: background jobs its log says started before then don't count (FAC-96: one already listed
    while it sat Ready, like an expired Monitor, never held its merge); a Ready task its own turn
    took back and settle_back() made Ready again keeps that cut (settled_at)."""
    if not ready and review_running(task):
        return False
    for k in live:
        if label_of(k.get("labels"), "role") == "reviewer" or status_word(k) == "blocked":
            continue
        address = k.get("address")
        key = label_of(k.get("labels"), "task")
        if key and any(parent_of(h) == address and status_word(h) in ("working", "blocked") for h in live_helpers(key)):
            return True
        if k.get("status_ms") and now() - k["status_ms"] >= BG_WORK_MAX_MS:
            continue
        log = agent_log(task, address) or agent_log(task)
        jobs, ended = background_jobs(log) if log else ({}, 0)
        cut = since or (task.get("settled_at") or 0 if ready else 0)
        told = task.get("bg_told") or {}
        gone = set(told.get("ids") or []) if told.get("at") and turn_since(told, live) else set()
        jobs = {i: at for i, at in jobs.items() if at >= cut and i not in gone and now() - at < BG_WORK_MAX_MS}
        # Codex's jobs are only what its process tree shows (SHEP-207): a session the log says is
        # still open, with no process of the agent running, is over.
        if any(i.startswith("codex:") for i in jobs) and not codex_working(k):
            jobs = {i: t for i, t in jobs.items() if not i.startswith("codex:")}
        if jobs or fg_job(k) or (address and cli_of(k) == "agy" and agy_bg_tasks(address)):
            return True
        if not ready and (waits_on_own(task, address, ended) or status_waits(k, ended)) and not last_question(task, address):
            return True
        # Mid-turn, a long command of its own still running in the foreground: Herdr can read
        # Claude Code idle then (SHEP-133: HOME-1 read Needs you during a 50 s Bash).
        if log and open_tool(log):
            return True
    return False
