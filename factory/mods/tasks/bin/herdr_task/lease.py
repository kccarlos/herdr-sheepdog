"""One coordinator per project, named by a lease in the project's task list (design section 5, notes 53 to 55).

The lease is the task list's "coordinator" record: {address, created_ms, since, session, log, template, starts}.
News, wakes, asks and workers' parents go to the lease holder (launch.coordinator_in), never to "whatever
terminal has the role label": a tab named "Assistant", a hand-started claude or a terminal someone labelled
holds no lease, so it is not the coordinator, whatever it looks like. Only the engine writes the lease: when
it starts a coordinator (project start, reconcile_pass, restart). A second claimant is refused while the
holder lives.

reconcile_pass, every tick: each project with open tasks has exactly one live coordinator. None (it exited,
a crash, a reboot that lost its terminal): a new one starts in the project's repo folder with a handoff,
without a first message from the owner, takes the lease, and the workers whose parent is gone report to it;
messages held for the old one go to the new one."""

import fcntl, json, os, re, time

from . import base, store as _store
from .base import agent_pid, end_group, Fail, now, PACKAGE, probe_volume, PROJECT_LABEL, ROLE_LABEL


def run(argv):
    return base.run(argv)


def hc_json(*args):
    return base.hc_json(*args)


def hc_get(addr):
    """("live" | "gone" | "unknown", hc's record or None) of a terminal here. "gone": hc says there is no such terminal,
    or it has finished; "unknown": hc failed (not answering, a timeout), so nothing may be decided from it."""
    if not addr or "/" in addr:
        return "unknown", None
    try:
        r = run(["hc", "get", addr, "--json"])
    except (OSError, Fail):
        return "unknown", None
    if r.returncode == 0:
        try:
            k = json.loads(r.stdout)
        except ValueError:
            return "unknown", None
        if isinstance(k, dict) and not k.get("error"):
            return ("live" if k.get("state") == "live" else "gone"), k
    said = f"{r.stdout or ''} {r.stderr or ''}"
    return ("gone" if re.search(r'"not_found"|\bno (?:such )?terminal\b', said) else "unknown"), None


def pid_started_before(pid, at_ms):
    """Whether process `pid` started before `at_ms` (ps's start time, to the second): the process a handoff record named,
    not a later one given its recycled pid. False when it can't be told."""
    try:
        r = base.run(["ps", "-o", "lstart=", "-p", str(int(pid))], env={**os.environ, "LC_ALL": "C"})
        started = time.mktime(time.strptime(" ".join(r.stdout.split()), "%a %b %d %H:%M:%S %Y")) if r.returncode == 0 else None
    except (OSError, ValueError, TypeError, OverflowError, Fail):
        return False
    return started is not None and (started + 1) * 1000 <= at_ms  # lstart is floored to the second

KEY = "coordinator"
OPEN = ("running", "ready", "queued", "todo", "scheduled")  # a task in one of these needs its project's coordinator
START_GRACE_MS = 10 * 60000   # a coordinator started this recently that is gone again: no new start yet
STARTS_PER_HOUR = 3           # more starts than this in an hour: stop starting, say so
HOUR_MS = 3600000
SHELL_MS = 15 * 60000         # a holder whose terminal is a shell with no agent this long (revive's resume failed, or its
                              # agent exited after one): no coordinator, replaced (revive looks only 10 minutes)


def _canon(p):
    exp = os.path.expanduser(str(p))
    return os.path.realpath(exp) if os.path.isdir(exp) else os.path.normpath(exp)


def list_path(project):
    """The task list of a project (its folder, or its slug), else None."""
    if not project:
        return None
    raw = getattr(project, "loc", {}).get("folder") if hasattr(project, "loc") else str(project)
    if os.path.isabs(raw):
        return _store.store_path(_canon(raw))[1]
    return os.path.join(_store.STORE, raw + ".json") if "/" not in raw else None


def lease_of(project):
    """The lease record of a project ({} without one)."""
    p = list_path(project)
    d = _store.read_list(p) if p else None
    rec = d.get(KEY) if isinstance(d, dict) else None
    return rec if isinstance(rec, dict) else {}


def leases():
    """[(folder, lease record)] of every task list here that has a lease."""
    out = []
    for p in _store.store_files():
        d = _store.read_list(p)
        if isinstance(d, dict) and isinstance(d.get(KEY), dict) and d.get("folder"):
            out.append((d["folder"], d[KEY]))
    return out


def live_holders():
    """[(address, folder)] of the live coordinators here: the lease holders."""
    return [(k["address"], folder) for folder, rec in leases() for k in [_live(rec)] if k]


def _state(rec):
    """("live", k) while the record's terminal is that same terminal and live here, ("gone", None) once it is not,
    ("unknown", None) when hc couldn't say (one failed call never replaces a healthy holder)."""
    addr = rec.get("address")
    if not addr or "/" in addr:
        return "gone", None
    st, k = hc_get(addr)
    if st == "live" and rec.get("created_ms") and k.get("created_ms") and rec["created_ms"] != k["created_ms"]:
        return "gone", None  # a later terminal at the same address
    return st, (k if st == "live" else None)


def _live(rec):
    """hc's record of the lease's terminal while it is that same terminal and live here, else None."""
    return _state(rec)[1]


def holder(project):
    """The address of the project's live coordinator on this machine: the lease holder, else None."""
    k = _live(lease_of(project))
    return k["address"] if k else None


def _edit(project, fn):
    """Calls fn(task list) under the list's lock (the same lock as store.Store) and saves it. fn's result is returned."""
    p = list_path(project)
    if not p or not os.path.exists(p):
        raise Fail(f"No task list for {project}: run herdr-task project start there first.")
    with open(os.path.join(_store.STORE, "." + os.path.basename(p) + ".lock"), "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        with open(p) as f:
            d = json.load(f)
        out = fn(d)
        tmp = f"{p}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            json.dump(d, f, indent=2)
            f.write("\n")
        os.replace(tmp, p)
    return out


def claim(project, addr, replacing=None, pid=None, pid_at=None, **extra):
    """Gives the lease to `addr` (a coordinator the engine just started). Refused (False) while another live
    terminal holds it, unless that is `replacing` (a restart hands it on). Taking it from an earlier holder writes a
    handoff record (that terminal, its created_ms, its agent's pid) that finish_handoff carries out and then removes,
    so a tick cut short anywhere in between finishes the same steps next time. `pid_at`: when `pid` was read (the record's
    `at`, which the pid's start time is checked against). An hc failure about the holder refuses too: only a holder hc
    says is gone gives way."""
    k = hc_json("get", addr) or {}
    from .watchdog import template_info

    def put(d):
        cur = d.get(KEY) if isinstance(d.get(KEY), dict) else {}
        if cur.get("address") not in (None, addr, replacing) and _state(cur)[0] != "gone":
            return False
        rec = {"from": cur.get("address"), "created_ms": cur.get("created_ms"), "at": pid_at or now(),
               "pid": pid if replacing and cur.get("address") == replacing else None}  # a pid only for the terminal it was read from
        d[KEY] = {"address": addr, "created_ms": k.get("created_ms"), "since": now(), "template": template_info()[0],
                  # every handoff not finished yet stays, the newest last (one replaced twice before a tick finished it)
                  **({"handoffs": pending} if (pending := [*handoffs_of(cur), *([rec] if cur.get("address") not in (None, addr) else [])]) else {}),
                  **({"starts": cur["starts"]} if cur.get("starts") else {}), **extra}
        return True
    return _edit(project, put)


def remember_session(project, k):
    """Keeps the holder's session and log on its lease (revive's record), so a later coordinator's handoff names them."""
    from .revive import read_record
    rec = read_record(k) or {}
    cur = lease_of(project)
    if rec.get("session") and (cur.get("session"), cur.get("log")) != (rec["session"], rec.get("log")):
        _edit(project, lambda d: d.get(KEY, {}).update(session=rec["session"], log=rec.get("log")) if isinstance(d.get(KEY), dict) else None)


# ---- starting one ------------------------------------------------------------------------------

def template_path():
    from .settings import TEMPLATES
    return next((p for p in (os.path.join(TEMPLATES, "assistant.md"),
                             os.path.normpath(os.path.join(PACKAGE, "..", "..", "..", "..", "templates", "assistant.md")))
                 if os.path.isfile(p)), None)


def free_name(base):
    """base, or base-2, -3 … while a live terminal has the name (a restart starts beside the old one)."""
    for n in range(1, 20):
        name = base if n == 1 else f"{base[:32 - len(str(n)) - 1]}-{n}"
        k = hc_json("get", name)
        if not k or k.get("state") != "live":
            return name
    return f"{base[:25]}-{int(time.time()) % 100000}"


def labels_for(slug):
    return [f"{PROJECT_LABEL}={slug}", f"{ROLE_LABEL}=coordinator", "herdr.role=coordinator"]


def start(folder, prefix, message=None):
    """Starts a coordinator for the project in its repo folder (never the old terminal's cwd or the focused pane's,
    note 55): the assistant template's command and words, the project's settings block, then `message` (a
    handoff). Kept (hc run --keep), so a lost program comes back as a shell revive types it into. Returns its address."""
    from . import assistant_settings as aset, settings as st
    from .launch import CLEAN_ENV
    slug = _store.store_path(folder)[0]
    kv, tpl_name, one_path, extra = labels_for(slug), "assistant", None, [message] if message else []
    src_path = template_path()
    if src_path:
        with open(src_path) as f:
            src = f.read()
        if (m := re.match(r"\A---\n([\s\S]*?)\n---\n?", src)) and (cm := re.search(r"(?m)^command:[ \t]*(.+)$", m.group(1))):
            rs = aset.rows(folder, st.read_settings())
            kv = [*kv, f"{aset.LABEL}={aset.digest(rs)}"]
            tpl_name, one_path = f"herdr-assistant-{os.getpid()}", os.path.join(st.TEMPLATES, f"herdr-assistant-{os.getpid()}.md")
            cmd_text = re.sub(r'^exec\s+', '', cm.group(1).strip())
            # FAC-87: no auto-update mid-run (before exec: exec takes an assignment as the program)
            pre = "DISABLE_AUTOUPDATER=1 " if re.match(r'(\S*/)?claude(\s|$)', cmd_text) else ""
            body = "\n\n".join(x for x in (src[m.end():].rstrip(), aset.block(rs), message) if x)
            os.makedirs(st.TEMPLATES, exist_ok=True)
            with open(one_path, "w") as f:
                f.write(f"---\nname: {tpl_name}\ndescription: One-off assistant start.\n"
                        f"command: {CLEAN_ENV} {pre}exec {cmd_text}\n---\n{body}\n")
            extra = []
    try:
        r = run(["hc", "run", "--bg", "--json", "--no-parent", "--keep", "--cwd", folder, "--name", free_name(f"{prefix.lower()}-assistant"),
                 *[x for p in kv for x in ("--label", p)], tpl_name, *extra])
    except OSError:
        r = None
    finally:
        if one_path and os.path.exists(one_path):
            os.remove(one_path)
    try:
        out = json.loads(r.stdout) if r is not None and r.returncode == 0 else None
    except ValueError:
        out = None
    if not isinstance(out, dict) or not out.get("address"):
        raise Fail("hc isn't on PATH (install herdr-core first)." if r is None else ((r.stderr or r.stdout).strip() or "Couldn't start the Assistant."))
    return out["address"]


def workers(folder, d=None):
    """[(address, task ref, title)] of the project's task agents still live here."""
    d = d if d is not None else _store.read_list(list_path(folder)) or {}
    out = []
    for t in d.get("tasks") or []:
        if t.get("archived") or t.get("state") not in OPEN:
            continue
        for a in (t.get("agents") or []) + (t.get("reviewers") or []):
            k = hc_json("get", a["address"]) if a.get("address") and not a.get("ended") and "/" not in a["address"] else None
            if k and k.get("state") == "live":
                out.append((a["address"], f"{d.get('prefix') or 'TASK'}-{t['id']}", t.get("title") or ""))
    return out


def handoff(folder, old, k=None, lease=None):
    """What a coordinator taking over hears: why, the previous one's screen and session log, the workers now its own."""
    lease = lease if lease is not None else lease_of(folder)
    parts = [f"You are taking over as this project's Assistant from {old}, which "
             + ("was restarted by the watchdog." if k else "is gone (it exited, or Herdr restarted).")] if old else [
        "You are this project's Assistant; Herdr started you because the project has open tasks."]
    log = lease.get("log")
    pid = agent_pid(old) if k else None
    if pid:
        from .sessions import session_of
        log = session_of(pid)[1] or log
    if log and os.path.isfile(log):
        parts.append(f"Its session log is {log}: read the recent part to pick up where it left off.")
    if k:
        screen = run(["hc", "read", old, "--lines", "400"]).stdout or ""
        if screen.strip():
            d = os.path.join(_store.STORE, "handoff")
            os.makedirs(d, exist_ok=True)
            path = os.path.join(d, f"{old}-{int(time.time())}.txt")
            with open(path, "w") as f:
                f.write(screen)
            parts.append(f"Its last screen is saved in {path}.")
    ws = workers(folder)
    if ws:
        parts.append("Workers still running, who now report to you: " + ", ".join(f"{a} ({ref} {title})".rstrip() for a, ref, title in ws)
                     + ". Watch each with hc wait <t>.")
    parts.append("Run `herdr-task list` to see every task and what waits on you.")
    return " ".join(parts)


def handoffs_of(rec):
    """The lease's handoff records not finished yet, oldest first."""
    hs = rec.get("handoffs") if isinstance(rec.get("handoffs"), list) else []
    return [h for h in hs if isinstance(h, dict) and h.get("from")]


def _same(a, b):
    """The same handoff record (its fields may have gained a verdict since)."""
    return (a.get("from"), a.get("created_ms"), a.get("at"), a.get("pid")) == (b.get("from"), b.get("created_ms"), b.get("at"), b.get("pid"))


def finish_handoff(folder, new):
    """Carries out the lease's handoff records, oldest first, removing each once done: workers and held news move from
    the earlier holder to `new`; that very terminal (same created_ms) ends if it still runs, and its agent's process
    group ends whenever its pid is known (also when a tick stopped after the kill). A later terminal at its address is
    not it: its news and life are left alone. Every step can run twice. Returns (workers moved, [ended addresses])."""
    moved, ended = 0, []
    for h in handoffs_of(lease_of(folder)):
        old = h["from"]
        st, k = hc_get(old) if "/" not in old else ("gone", None)
        if st == "unknown":
            break  # hc didn't answer: the record stays for the next tick
        if h.get("pid") and "group" not in h:
            # whether the pid is still the process the record named: asked while its leader lives (ps -p fails once the
            # kill below ends it), and kept, so a tick that stops between the kill and end_group still ends the group
            verdict = bool(h.get("at")) and pid_started_before(h["pid"], h["at"])
            _edit(folder, lambda d: [x.update(group=verdict) for x in handoffs_of(d.get(KEY, {})) if _same(x, h)])
            h = dict(h, group=verdict)
        if old != new:
            st2 = "gone" if k is None else ("live" if k.get("state") == "live" else "finished")
            here = k is not None and not (h.get("created_ms") and k.get("created_ms") and h["created_ms"] != k["created_ms"])
            moved += reparent(folder, old, new, held=here or st2 != "live")
            if here and st2 == "live":  # never two coordinators: it ends, as the restart would have ended it
                run(["hc", "kill", old])
                ended.append(old)
            if here:
                run(["hc", "rm", old])
        # its agent's children (agy's) don't orphan; only the process the record named, never a later one with its pid
        if h.get("pid") and h.get("group"):
            end_group(h["pid"])
        _edit(folder, lambda d: d.get(KEY, {}).update(handoffs=[x for x in handoffs_of(d.get(KEY, {})) if not _same(x, h)])
              if isinstance(d.get(KEY), dict) else None)
    return moved, ended


def reparent(folder, old, new, held=True):
    """Workers whose parent is `old` or gone report to `new` from now on (their task's agent record names it: hc
    can't change a terminal's parent), and messages held for `old` go to `new`. Returns how many workers moved."""
    gone = {}

    def dead(addr):
        if addr not in gone:
            gone[addr] = bool(addr) and "/" not in addr and hc_get(addr)[0] == "gone"
        return gone[addr]

    def put(d):
        n = 0
        for t in d.get("tasks") or []:
            for a in (t.get("agents") or []) + (t.get("reviewers") or []):
                if not a.get("ended") and a.get("parent") and a["parent"] != new and (a["parent"] == old or dead(a["parent"])):
                    a["parent"], n = new, n + 1
        return n
    moved = _edit(folder, put)
    if old and held:
        from .news import held_messages
        with held_messages() as held:
            # one typed into a busy agy coordinator that never took it (pending) is sent again too
            items = [{k: v for k, v in x.items() if k != "pending"} for x in held.pop(old, [])]
            if items:
                held.setdefault(new, []).extend(items)
    return moved


def orphans(d, holder_addr):
    """Whether a live task agent of the list reports to a parent that is gone (not `holder_addr`)."""
    parents = {a["parent"] for t in d.get("tasks") or [] for a in (t.get("agents") or []) + (t.get("reviewers") or [])
               if not a.get("ended") and a.get("parent") and a["parent"] != holder_addr and "/" not in a["parent"]}
    return any(hc_get(p)[0] == "gone" for p in parents)


def restart(folder, k, why=None):
    """Restarts the project's coordinator `k` (the lease holder) in engine code: a new one in the repo folder with a
    handoff, the lease and its workers handed on, then the old one ends with its process group. The new address, or None."""
    old = k["address"]
    d = _store.read_list(list_path(folder)) or {}
    pid, pid_at = agent_pid(old), now()  # read before the start (up to a minute): the record's `at` is this moment
    try:
        new = start(folder, d.get("prefix") or "TASK", handoff(folder, old, k))
    except Fail:
        return None
    try:
        ok = claim(folder, new, replacing=old, pid=pid, pid_at=pid_at)
    except (Fail, OSError, ValueError):
        ok = False
    if not ok:  # the new one never holds the lease: it ends, never a second coordinator
        run(["hc", "kill", new])
        run(["hc", "rm", new])
        return None
    finish_handoff(folder, new)  # workers, held news, then the old one and its process group
    return new


# ---- every tick --------------------------------------------------------------------------------

def reconcile_pass(report):
    """Every project with open tasks has exactly one live coordinator here: none, one starts with a handoff and takes
    the lease. A terminal that looks like one without the lease is reported, never used, ended or relabelled."""
    holders = set()
    for p in _store.store_files():
        d = _store.read_list(p)
        if not _store.is_task_list(d) or d.get("kind", "repo") != "repo" or not d.get("folder"):
            continue
        folder, lease = d["folder"], d.get(KEY) if isinstance(d.get(KEY), dict) else {}
        st, k = _state(lease)
        stale = None
        if st == "unknown":
            continue  # hc didn't answer about the holder: decide nothing this tick
        if k:
            from .agents import agent_running
            if agent_running(k):
                if lease.get("no_agent_since"):
                    _edit(folder, lambda x: x.get(KEY, {}).pop("no_agent_since", None))
            elif not lease.get("no_agent_since"):
                _edit(folder, lambda x: x.get(KEY, {}).update(no_agent_since=now()))
            elif now() - lease["no_agent_since"] >= SHELL_MS:
                k, stale = None, k  # a shell, no agent: replaced below like a gone one
        if k:
            holders.add(k["address"])
            try:
                remember_session(folder, k)
                if handoffs_of(lease):  # a handoff cut short: finish it
                    moved, ended = finish_handoff(folder, k["address"])
                    report.setdefault("coordinators", []).append({"action": "re-parented", "folder": folder, "to": k["address"],
                                                                  "workers": moved, **({"ended": ended[-1]} if ended else {})})
                elif orphans(d, k["address"]):  # workers on a parent that is gone
                    report.setdefault("coordinators", []).append({"action": "re-parented", "folder": folder, "to": k["address"],
                                                                  "workers": reparent(folder, None, k["address"])})
            except (OSError, ValueError, Fail):
                pass
            continue
        if not any(not t.get("archived") and t.get("state") in OPEN for t in d.get("tasks") or []):
            continue
        starts = [x for x in lease.get("starts") or [] if now() - x < HOUR_MS]
        if starts and now() - starts[-1] < START_GRACE_MS:
            continue
        if len(starts) >= STARTS_PER_HOUR:
            if not lease.get("gave_up"):
                _edit(folder, lambda x: x.get(KEY, {}).update(gave_up=now()))
                report.setdefault("coordinators", []).append({"action": "gave-up", "folder": folder, "starts": len(starts)})
            continue
        stalled = probe_volume(folder)  # a stalled disk: a new one would hang too (note 54, FAC-83)
        if stalled:
            report.setdefault("coordinators", []).append({"action": "disk-stalled", "folder": folder, "why": stalled})
            continue
        old, new = lease.get("address"), None
        pid, pid_at = (agent_pid(old), now()) if stale else (None, None)  # the shell's pid, read before the start
        try:
            new = start(folder, d.get("prefix") or "TASK", handoff(folder, old, lease=lease))
            try:
                ok = claim(folder, new, replacing=old, pid=pid, pid_at=pid_at, starts=[*starts, now()])
            except (Fail, OSError, ValueError):
                ok = False
            if not ok:  # another start won the lease meanwhile, or the claim failed: the new one ends
                run(["hc", "kill", new])
                run(["hc", "rm", new])
                report.setdefault("coordinators", []).append({"action": "refused", "folder": folder, "address": new})
                continue
            moved, _ = finish_handoff(folder, new)  # a shell it left (stale) is that same terminal, so it ends too
            moved = moved or reparent(folder, None, new)  # no earlier holder: workers on a gone parent still move
        except (Fail, OSError, ValueError) as e:
            report.setdefault("coordinators", []).append({"action": "failed", "folder": folder, "why": str(e)})
            continue
        holders.add(new)
        report.setdefault("coordinators", []).append({"action": "started", "folder": folder, "from": old, "to": new, "workers": moved})
    for lbl in (f"{ROLE_LABEL}=coordinator", "herdr.role=coordinator"):
        for t in (hc_json("ls", "--label", lbl) or {}).get("terminals") or []:
            if t.get("state") == "live" and "/" not in (t.get("address") or "/") and t["address"] not in holders:
                row = {"address": t["address"], "cwd": t.get("cwd")}
                if row not in report.get("not_coordinator", []):
                    report.setdefault("not_coordinator", []).append(row)
