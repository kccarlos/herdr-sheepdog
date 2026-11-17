"""What a project's coordinator knows of the owner's settings (SHEP-49): the ones that shape how it
makes and runs tasks, resolved for its project folder (Everywhere -> machine -> project), as a short
"Your settings" block. A new coordinator session gets the block at start, and a running one gets one
notice line per setting that changed for its project, several at once in one message, never for a
save that changed nothing there."""

import hashlib
import json
import os

from .base import hc_json, label_of, MAX_RUNNING, now, PROJECT_LABEL, run, STORE
from .settings import DEFAULTS, LOCAL_MACHINE, MUST_WORDS, SETTINGS, agent_key, bare, default_worker_agent, project_layer, read_settings
from .launch import coordinators, is_coordinator

# What a coordinator was last told, on its terminal: "review:1a2b3c,merge:…", a short hash of each line.
LABEL = "sheepdog.settings"
STATE = os.path.join(STORE, ".assistant-settings.json")
EVERY_MS = 60000  # looked at again this often (Autopilot's switch is in its own file), and at once when the settings file changes

HEAD = ("Your settings: (sheepdog factory settings for this project, the truth for every task you add; a task's own --set wins, "
        "and `herdr-task show <id>` says what one task has)")
MERGE_SHORT = {"ask": "ask the person first (they accept it by voice or on the phone's ask card, which merges it)",
               "checks": "after checks (Herdr merges a ready task once its checks pass)",
               "auto": "on its own (Herdr merges a ready task)"}
BY = {"project": "set for this project", "machine": "this machine's setting", "everywhere": "your setting everywhere"}
NOTICE_END = "Nothing to answer: reply only <hc no-reply/>."


def _is_coord(k):
    return bool(k) and (label_of(k.get("labels"), "role") == "coordinator" or is_coordinator(k))


def project_of(k, d=None):
    labels = (k or {}).get("labels") or {}
    proj = labels.get(PROJECT_LABEL)
    projects = (d or {}).get("projects") if isinstance(d, dict) else None
    if proj:
        if (isinstance(projects, dict) and proj in projects) or os.path.isabs(proj):
            return proj
        from .store import is_task_list, read_list, slug_of
        lst = read_list(os.path.join(STORE, proj + ".json"))
        if is_task_list(lst):
            return lst["folder"]
        if isinstance(projects, dict) and (hit := next((f for f in projects if os.path.isabs(str(f)) and slug_of(str(f)) == proj), None)):
            return hit
    sp = bare(labels.get("herdr.space") or labels.get("herdr.coordinator.space") or "")
    spaces = (d or {}).get("spaces") if isinstance(d, dict) else None
    if sp and isinstance(spaces, dict) and isinstance(spaces.get(sp), dict):
        return spaces[sp].get("folder") or sp
    cwd = (k or {}).get("cwd")
    if cwd:
        exp = os.path.expanduser(cwd)
        return os.path.realpath(exp) if os.path.isdir(exp) else exp
    return proj or sp or None


def layers(d, project):
    """(name, layer) of the settings file d, least specific first: Everywhere's, this machine's,
    this project's own."""
    obj = lambda x: x if isinstance(x, dict) else {}
    machine = obj(obj(d.get("machines")).get(d.get("here") or LOCAL_MACHINE))
    own = project_layer(d, project if project and os.path.isabs(str(project)) else None, project) if project else {}
    return [("everywhere", obj(d.get("everywhere"))), ("machine", machine), ("project", own)]


def resolve(ls):
    """The values that apply, from DEFAULTS and the layers (most specific wins)."""
    out = {k: dict(v) for k, v in DEFAULTS.items()}
    for _, layer in ls:
        for k, v in layer.items():
            if isinstance(v, dict) and isinstance(out.get(k), dict):
                out[k].update({kk: vv for kk, vv in v.items() if vv is not None})
            elif k in out and v is not None:
                out[k] = v
    return out


def pick_words(p, unset):
    """An agent pick {agent, model, effort} (or its argv) in a few words."""
    if not isinstance(p, dict) or not (p.get("agent") or p.get("argv")):
        return unset
    agent = p.get("agent") or agent_key([str(x) for x in p["argv"]])
    return agent + (f", model {p['model']}" if p.get("model") else "") + (f", effort {p['effort']}" if p.get("effort") else "")


def autopilot_of(project, cfg):
    """(on, schedule words, direction) of the project's Autopilot."""
    from . import autopilot as ap
    try:
        sid = next((k for k, v in ((cfg or {}).get("spaces") or {}).items()
                    if isinstance(v, dict) and v.get("folder") == project), None) if project else None
        with ap.state(write=False) as d:
            key = sid if sid and not (d.get("projects") or {}).get(project) else project
            rec = ((d.get("projects") or {}).get(project) or (d.get("spaces") or {}).get(key)) if project else None
            eff = ap.resolve(d, rec, key, cfg)
        return eff["on"], ap.trigger_of(eff), ap.direction_of(project, cfg)
    except Exception:  # never stops a session's start
        return False, "", ""


def rows(project, d=None, auto=None):
    """The block's lines for a project: [{key, label, text, value, by, scope}]. by: who set a line
    that differs from the default (None: the default); scope: "project" when the project sets one of
    its parts itself, else "everywhere" (or "machine")."""
    d = read_settings() if d is None else d
    ls = layers(d, project)
    on, every, direction = autopilot_of(project, d) if auto is None else auto

    def lines(cfg, autopilot):
        t = cfg["tasks"]
        rounds = int(t.get("review_rounds") or 0)
        must = t.get("must_fix") if t.get("must_fix") in MUST_WORDS else "major"
        review = "off" if rounds <= 0 else (f"on, at most {rounds} round{'s' if rounds > 1 else ''}, must fix {MUST_WORDS[must]}, "
                                            f"reviewer {pick_words(cfg.get('reviewer'), 'the other CLI')}")
        merge = t.get("merge") if t.get("merge") in MERGE_SHORT else "ask"
        notes = [w for w, k in (("start", "start_notes"), ("finish", "finish_notes")) if str(t.get(k) or "").strip()]
        a_on, a_every, a_dir = autopilot
        dflt_agent = default_worker_agent()
        return [
            ("review", "Review", review, "on" if rounds > 0 else "off",
             [("tasks", "review_rounds"), ("tasks", "must_fix"), ("reviewer", None)]),
            ("merge", "Merge", MERGE_SHORT[merge], merge, [("tasks", "merge")]),
            ("proof", "Proof", "on" if t.get("proof") is not False else "off (tasks show no proof)",
             "off" if t.get("proof") is False else "on", [("tasks", "proof")]),
            ("agent", "Task agent", pick_words(cfg.get("worker"), f"{dflt_agent}, its own default model and effort"),
             pick_words(cfg.get("worker"), dflt_agent).replace(", model ", "/").replace(", effort ", "/"), [("worker", None)]),
            ("base", "Base branch", f"{t['base']} (new tasks branch from it and merge into it)" if str(t.get("base") or "").strip()
             else "the folder's checked-out branch", str(t.get("base") or "").strip(), [("tasks", "base")]),
            ("worktrees", "Worktrees", "on (in a git repo, a worktree and branch per task)" if t.get("worktrees") is not False
             else "off (tasks work in the folder itself)", "off" if t.get("worktrees") is False else "on", [("tasks", "worktrees")]),
            ("max_running", "Tasks at once", f"{t.get('max_running') or MAX_RUNNING} per folder (more wait and start by themselves)",
             str(t.get("max_running") or MAX_RUNNING), [("tasks", "max_running")]),
            ("notes", "Task notes", (" and ".join(notes) + " notes set (every brief includes them)") if notes else "none",
             ",".join(notes) or "none", [("tasks", "start_notes"), ("tasks", "finish_notes")]),
            ("autopilot", "Autopilot", ("on, " + a_every + (f", direction: {a_dir}" if a_dir else "")) if a_on
             else "off" + (f" (direction when on: {a_dir})" if a_dir else ""), "on" if a_on else "off",
             [("autopilot", "every"), ("autopilot", "events"), ("autopilot", "direction"), ("autopilot", None)]),
        ]
    have = lines(resolve(ls), (on, every, direction))
    default = {x[0]: x[2] for x in lines(resolve([]), (False, every, ""))}
    out = []
    for key, label, text, value, parts in have:
        def sets(layer):
            if key == "autopilot" and layer is ls[2][1] and on:
                return True  # its switch is the project's own
            return any((f is None and layer.get(sec)) or (f is not None and isinstance(layer.get(sec), dict) and layer[sec].get(f) is not None)
                       for sec, f in parts)
        setters = [name for name, layer in ls if sets(layer)]
        scope = setters[-1] if setters else "everywhere"
        by = setters[-1] if setters and text != default[key] else None
        out.append({"key": key, "label": label, "text": text, "value": value, "by": by, "scope": scope})
    return out


def block(rs):
    return "\n".join([HEAD] + [f"- {r['label']}: {r['text']}" + (f"; {BY[r['by']]}" if r["by"] else "") for r in rs])


def digest(rs):
    """The label's value: each line's key and a short hash of what it says."""
    return ",".join(f"{r['key']}:{hashlib.sha1(r['text'].encode()).hexdigest()[:6]}" for r in rs)


def parse(label):
    return dict(x.split(":", 1) for x in (label or "").split(",") if ":" in x) if label else None


def attr(v):
    return str(v).replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;")


def notice(changed):
    """The message a running Assistant gets when some of its settings changed: one hidden
    <herdr-setting> line each (the chat strips Herdr's tags), the last saying nothing needs an answer."""
    out = []
    for i, r in enumerate(changed):
        words = (f"{r['label']} is now {r['text']}" + (f"; {BY[r['by']]}" if r["by"] else "; Herdr's default") +
                 ("." if r["key"] == "autopilot" else ". Tasks you add from now on get it; running ones keep their brief."))
        end = f" {NOTICE_END}" if i == len(changed) - 1 else ""
        out.append(f'<herdr-setting name="{r["key"]}" value="{attr(r["value"])}" scope="{r["scope"]}">{words}{end}</herdr-setting>')
    return "\n".join(out)


def session_note(terminal):
    """What an Assistant's new session is told: its settings block; "" for other agents or outside Herdr."""
    k = hc_json("get", terminal) if terminal else None
    if not _is_coord(k):
        return ""
    d = read_settings()
    rs = rows(project_of(k, d), d)
    run(["hc", "label", terminal, f"{LABEL}={digest(rs)}"])
    return block(rs)


def settings_pass(report, quiet=None):
    """Tells each live coordinator here what changed in its settings since it was last told (its
    LABEL), in one message. One without the label (started before this) gets the whole block once
    it has had a turn."""
    try:
        mtime = os.path.getmtime(SETTINGS)
    except OSError:
        mtime = 0
    try:
        with open(STATE) as f:
            st = json.load(f)
    except (OSError, ValueError):
        st = {}
    if st.get("mtime") == mtime and now() - (st.get("at") or 0) < EVERY_MS:
        return
    tmp = f"{STATE}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump({"mtime": mtime, "at": now()}, f)
    os.replace(tmp, STATE)
    from .news import news_send
    d = read_settings()
    for addr in coordinators():
        k = hc_json("get", addr) or {}
        if k.get("state") != "live" or not _is_coord(k):
            continue
        labels = k.get("labels") or {}
        rs = rows(project_of(k, d), d)
        had = parse(labels.get(LABEL) or labels.get("herdr.settings"))
        now_digest = parse(digest(rs))
        if had == now_digest:
            continue
        if had is None:
            if not k.get("turn"):
                continue  # just started
            text = f'<herdr-setting name="all">{block(rs)} {NOTICE_END}</herdr-setting>'
            changed = [{"key": "all"}]
        else:
            changed = [r for r in rs if had.get(r["key"]) != now_digest[r["key"]]]
            if not changed:  # a line no longer said: just note it
                run(["hc", "label", addr, f"{LABEL}={digest(rs)}"])
                continue
            text = notice(changed)
        if not news_send(addr, text):
            continue  # busy: next time
        run(["hc", "label", addr, f"{LABEL}={digest(rs)}"])
        report.setdefault("settings", []).append({"to": addr, "changed": [r["key"] for r in changed]})
