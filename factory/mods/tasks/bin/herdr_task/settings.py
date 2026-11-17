"""The owner's settings (~/.config/sheepdog/settings.json: Everywhere -> machine -> project), the agent
each task runs, and the Settings block a task's brief ends with."""

import json
import os
import re
import shlex
import tempfile
import shutil
import subprocess

from .base import Fail, git, HOME, MAX_RUNNING, WHERE


# What each agent key starts by default. Unknown keys run as themselves; --argv overrides.
AGENTS = {"claude": ["claude"], "codex": ["codex", "--no-daemon"], "agy": ["agy"]}


# ---- the person's settings ------------------------------------------------------------------

SETTINGS = os.environ.get("HERDR_TASK_SETTINGS") or WHERE.path("settings")
# The role instructions are herdr-core templates (`hc templates` lists them): assistant.md (the
# coordinator's), worker.md and reviewer.md. Workers and reviewers used ~/.herdr/instructions.
TEMPLATES = os.environ.get("HERDR_TASK_TEMPLATES") or os.path.join(HOME, ".config", "herdr-core", "templates")
OLD_INSTRUCTIONS = os.path.join(HOME, ".herdr", "instructions")
ASSISTANT_TEMPLATES = ("assistant", "coordinator")  # "coordinator": Assistants started before the rename
DEFAULTS = {"tasks": {"merge": "ask", "review_rounds": 0, "review_min": 0, "must_fix": "major", "max_running": MAX_RUNNING, "worktrees": True,
                      "start_notes": "", "finish_notes": "", "archive_after": 7, "proof": True, "base": "", "branch": "",
                      "worktree_root": "", "check": "", "simulators": [], "reviewer_sandbox": True,
                      "auto_accept": True, "sync": "cheap"},
            "automations": {"enabled": True}, "worker": {}, "reviewer": {}, "coordinator": {}, "backup": {}, "project": {}}
LEVELS = ["blocker", "major", "minor"]


LOCAL_MACHINE = "local"
_UNSET = object()


def _folder_of(s):
    if isinstance(s, str):
        return s
    if isinstance(s, dict):
        return s.get("folder")
    return (getattr(s, "loc", None) or {}).get("folder")


def read_settings():
    """The settings file as it is ({} when there is none or it can't be read)."""
    try:
        with open(SETTINGS) as f:
            d = json.load(f)
    except (OSError, ValueError):
        return {}
    return d if isinstance(d, dict) else {}


def _same_folder(a, b):
    return bool(a and b) and (a == b or os.path.realpath(a) == os.path.realpath(b))


def project_layer(d, folder=None, space=None):
    """This project's raw layer: d["projects"][folder] merged per section over any legacy d["spaces"] entry."""
    if not isinstance(d, dict):
        return {}
    projects = d.get("projects") if isinstance(d.get("projects"), dict) else {}
    spaces = d.get("spaces") if isinstance(d.get("spaces"), dict) else {}
    sid = bare(str(space)) if space and not os.path.isabs(str(space)) and space not in projects else None
    sp = spaces.get(sid) if sid and isinstance(spaces.get(sid), dict) else None
    if folder and sp and sp.get("folder") is not None and not _same_folder(sp.get("folder"), folder):
        sp = None
    if sp and not folder and isinstance(sp.get("folder"), str):
        folder = sp["folder"]
    if not sp and folder:
        sp = next((v for v in spaces.values() if isinstance(v, dict) and _same_folder(v.get("folder"), folder)), None)
    key = (folder if folder and folder in projects else
           next((k for k in projects if _same_folder(k, folder)), None) if folder else None) or (space if space and space in projects else folder)
    proj = projects.get(key) if key and isinstance(projects.get(key), dict) else None
    out = dict(sp) if isinstance(sp, dict) else {}
    for k, v in (proj or {}).items():
        out[k] = {**out[k], **{kk: vv for kk, vv in v.items() if vv is not None}} if isinstance(v, dict) and isinstance(out.get(k), dict) else v
    return out


def autopilot_layers(d, project=None):
    """(Everywhere's, the project's own) Autopilot in a settings file d: {every, events, heartbeat, direction},
    each only when set. Keyed by project folder in d["projects"] (merged over any legacy d["spaces"] entry)."""
    def part(layer):
        a = (layer or {}).get("autopilot") if isinstance(layer, dict) else None
        return {k: v for k, v in a.items() if k in ("every", "events", "heartbeat", "direction") and v is not None} if isinstance(a, dict) else {}
    own = project_layer(d, project if project and os.path.isabs(str(project)) else None, project) if project else None
    return part(d.get("everywhere")), part(own)


def write_autopilot(project, values):
    """Sets Autopilot fields at one level of the settings file (project None: Everywhere's; folder path:
    d["projects"][folder]); a value None unsets it there, so it follows the level above again."""
    d = read_settings()
    if project:
        section = "projects" if os.path.isabs(project) or project in (d.get("projects") or {}) else "spaces"
        layer = (d.setdefault(section, {})).setdefault(project, {})
    else:
        section, layer = None, d.setdefault("everywhere", {})
    a = dict(layer.get("autopilot") or {})
    for k, v in values.items():
        if v is None:
            a.pop(k, None)
        else:
            a[k] = v
    if a:
        layer["autopilot"] = a
    else:
        layer.pop("autopilot", None)
    if project and not layer:
        d[section].pop(project, None)
    d["version"] = 1
    os.makedirs(os.path.dirname(SETTINGS), exist_ok=True)
    tmp = f"{SETTINGS}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump(d, f, indent=2)
        f.write("\n")
    os.replace(tmp, SETTINGS)


def _write_task_setting(s, key, value):
    """Writes one project-folder tasks.<key> setting into d["projects"][folder] (None unsets it)."""
    try:
        with open(SETTINGS) as f:
            d = json.load(f)
    except FileNotFoundError:
        d = {}
    except (OSError, ValueError) as e:
        raise Fail(f"Can't read {SETTINGS} ({e}): not changing it.")
    if not isinstance(d, dict):
        raise Fail(f"{SETTINGS} isn't a settings object: not changing it.")
    projects = d.setdefault("projects", {})
    folder = _folder_of(s)
    if not folder:
        raise Fail("No project folder to configure.")
    proj_key = folder if folder in projects else next((k for k in projects if _same_folder(k, folder)), folder)
    layer = projects.setdefault(proj_key, {})
    tasks = dict(layer.get("tasks") or {})
    tasks.pop(key, None) if value is None else tasks.update({key: value})
    layer.update(tasks=tasks) if tasks else layer.pop("tasks", None)
    if not layer:
        projects.pop(proj_key, None)
    if value is None and isinstance(d.get("spaces"), dict):
        for v in d["spaces"].values():
            if isinstance(v, dict) and _same_folder(v.get("folder"), folder) and isinstance(v.get("tasks"), dict):
                v["tasks"].pop(key, None)
                if not v["tasks"]:
                    v.pop("tasks", None)
    d["version"] = 1
    os.makedirs(os.path.dirname(SETTINGS), exist_ok=True)
    tmp = f"{SETTINGS}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump(d, f, indent=2)
        f.write("\n")
    os.replace(tmp, SETTINGS)
    return proj_key


def write_max_running(s, space=None, value=_UNSET):
    """FAC-103: `config --set tasks.max_running=N`, the project folder's "Tasks at once" in
    d["projects"][folder]. None unsets it there. Returns the project folder. Refused on a file
    that exists on disk but isn't a valid JSON object (writing would lose every other setting)."""
    if value is _UNSET:
        value = space
    return _write_task_setting(s, "max_running", value)


def write_task_check(s, space=None, value=_UNSET):
    """`config --set tasks.check=CMD`: sets or clears the project folder's tasks.check command."""
    if value is _UNSET:
        value = space
    return _write_task_setting(s, "check", value)


_AGENTS_CHECK_RE = re.compile(
    r"(?im)^(?:[-*]\s*)?(?:\*\*)?(?:check(?:\s+command)?|test(?:\s+command)?|tests?|run\s+(?:the\s+)?(?:tests?|checks?))(?:\*\*)?\s*:\s*`([^`\n]+)`(?:\s|$|[.,;])"
)


def derive_check(folder):
    """Derives a repo's check command from facts it already states (Makefile test/check target,
    explicit AGENTS.md test/check line, or package.json scripts.test), returning (cmd, source) or ('', None)."""
    if not folder or not os.path.isdir(folder):
        return "", None
    for mk in ("Makefile", "makefile", "GNUmakefile"):
        p = os.path.join(folder, mk)
        if os.path.isfile(p):
            try:
                with open(p, errors="replace") as f:
                    text = f.read(262144)
            except OSError:
                continue
            if re.search(r"(?m)^test\s*:(?!=)", text):
                return "make test", mk
            if re.search(r"(?m)^check\s*:(?!=)", text):
                return "make check", mk
    agents_md = os.path.join(folder, "AGENTS.md")
    if os.path.isfile(agents_md):
        try:
            with open(agents_md, errors="replace") as f:
                if m := _AGENTS_CHECK_RE.search(f.read(262144)):
                    return m.group(1).strip(), "AGENTS.md"
        except OSError:
            pass
    pkg_path = os.path.join(folder, "package.json")
    if os.path.isfile(pkg_path):
        try:
            with open(pkg_path) as f:
                pkg = json.load(f)
            t = str(((pkg.get("scripts") if isinstance(pkg, dict) else None) or {}).get("test") or "").strip()
            if t and "no test specified" not in t.lower():
                return "npm test", "package.json"
        except (OSError, ValueError):
            pass
    return "", None


def settings_layers(s, space=None):
    """(Everywhere's, this machine's, this project's) raw settings, least specific first."""
    d = read_settings()
    pick = project_layer(d, _folder_of(s), space)
    machine = (d.get("machines") or {}).get(d.get("here") or LOCAL_MACHINE)
    return d.get("everywhere") or {}, machine or {}, pick


def settings_for(s, space=None):
    """The settings for this project folder's tasks: Everywhere, then this machine's, then
    this project's override in d["projects"][folder] (most specific wins)."""
    everywhere, machine, pick = settings_layers(s, space)
    out = {k: {kk: list(vv) if isinstance(vv, list) else vv for kk, vv in v.items()} for k, v in DEFAULTS.items()}

    def merge(part):
        for k, v in (part or {}).items():
            if isinstance(v, dict) and isinstance(out.get(k), dict):
                out[k].update({kk: vv for kk, vv in v.items() if vv is not None})
            elif k in out and v is not None:
                out[k] = v
    merge(everywhere)
    merge(machine)
    merge(pick)
    return out


def notes(s, task, which, space=None):
    """A task's "When a task starts" / "finishes" notes (which: "start" or "finish"): its own
    (--start-notes / --finish-notes), else its folder's from Shepherdr settings. Herdr runs
    nothing itself: the notes go into the agents' starting prompts."""
    own = (task.get(f"{which}_notes") or "").strip()
    return own or (settings_for(s)["tasks"].get(f"{which}_notes") or "").strip()


def notes_part(head, text):
    """ "Before you start: ..." on one line, or the head over the notes when they have several."""
    return f"{head}:\n{text}" if "\n" in text else f"{head}: {text}" if text else ""


def worktrees_on(s, space=None):
    """Tasks here get a git worktree each: in a git repo, unless the person's Shepherdr settings
    turn worktrees off for it (then it works in the folder itself, as a folder without git)."""
    return s.loc["kind"] == "repo" and settings_for(s)["tasks"].get("worktrees") is not False


# Models only another CLI knows (a reviewer model left from agy or Claude): Codex on a ChatGPT account refuses them.
FOREIGN_MODEL = re.compile(r"(?:claude|gemini)-", re.I)


def codex_review_argv(pick, folder):
    """A Codex gate reviewer's argv (FAC-80), unattended on a task worktree: nothing may wait for a person.
    - Trust: its worktree, its repository's root (a worktree's .git file points there, and Codex asks to trust
      that root) and its scratch folder as a `projects` table, for this run only; hooks off (a new hook stops
      it on "Hooks need review").
    - Approvals: --ask-for-approval never, so it never asks. Read-only would then fail the check (temp dirs, git
      caches) and `herdr-task review` (reaches hc), and an escalation would wait for a person. So it runs
      workspace-write with -C an empty scratch folder: the only writable places are that folder and the temp
      dirs, and the worktree (read everywhere) stays read-only to it."""
    scratch = tempfile.mkdtemp(prefix="herdr-review-")
    a = ["codex", "--no-daemon", "--disable", "hooks", "-C", scratch]
    roots = list(dict.fromkeys(trust_roots(folder) + trust_roots(scratch)))
    a += ["-c", "projects={" + ",".join(f'{json.dumps(r)}={{trust_level="trusted"}}' for r in roots) + "}"]
    if pick.get("model") and not FOREIGN_MODEL.match(pick["model"]):
        a += ["-m", pick["model"]]
    if pick.get("effort"):
        a += ["-c", f'model_reasoning_effort="{pick["effort"]}"']
    a += ["-c", 'service_tier="default"', "-s", "workspace-write", "-a", "never"]
    return a


def argv_for(pick):
    """How to start an agent from a role's settings {agent, model, effort, fast, perm} (or its own
    argv), as the chat's agent picker builds it (mods/chat/agent.js argvFor). None: no setting."""
    if not pick:
        return None
    if pick.get("argv"):
        return [str(x) for x in pick["argv"]]
    agent = pick.get("agent")
    if agent == "claude":
        a = ["claude"]
        if pick.get("model"):
            a += ["--model", pick["model"]]
        if pick.get("effort"):
            a += ["--effort", pick["effort"]]
        a += ["--settings", json.dumps({"fastMode": bool(pick.get("fast"))})]
        perm = pick.get("perm")
        if perm == "bypassPermissions":
            a.append("--dangerously-skip-permissions")
        elif perm:
            a += ["--permission-mode", "default" if perm == "manual" else perm]
        return a
    if agent == "codex":
        a = ["codex", "--no-daemon"]
        if pick.get("model"):
            a += ["-m", pick["model"]]
        if pick.get("effort"):
            a += ["-c", f'model_reasoning_effort="{pick["effort"]}"']
        # always said, so Fast off wins over a service_tier in config.toml (default: the standard tier)
        a += ["-c", 'service_tier="%s"' % ("fast" if pick.get("fast") else "default")]
        perm = pick.get("perm")
        if perm == "full":
            a.append("--dangerously-bypass-approvals-and-sandbox")
        elif perm == "read-only":
            a += ["-s", "read-only", "-a", "on-request"]
        elif perm:
            a += ["-s", "workspace-write", "-a", "on-request"]
        return a
    if agent == "agy":
        a = ["agy"]
        if pick.get("model"):
            a += ["--model", pick["model"]]
        if pick.get("effort"):
            a += ["--effort", pick["effort"]]
        perm = pick.get("perm")
        if perm == "full":
            a.append("--dangerously-skip-permissions")
        else:
            a += ["--mode", "plan" if perm == "plan" else "accept-edits"]
        return a
    return [agent] if agent else None


def worker_pick(s, task, space=None, agent=None):
    """What a task's agent runs on, {agent, model, effort, fast, perm}: Settings' worker, then the
    task's own --agent / --model / --effort, then `agent` (start --agent). Another CLI than
    Settings' starts from its own defaults (models and permissions differ per CLI)."""
    w = dict(settings_for(s)["worker"] or {})
    pick = dict(w)
    if task.get("agent") and task["agent"] != pick.get("agent"):
        pick = {"agent": task["agent"]}
    if not pick.get("agent") and not pick.get("argv"):
        pick["agent"] = "claude"
    for k in ("model", "effort"):
        if task.get(k):
            pick[k] = task[k]
    if agent and agent != pick.get("agent"):
        pick = dict(w) if w.get("agent") == agent else {"agent": agent}
    return pick


def worker_argv(s, task, space=None, agent=None):
    """A task's agent when nobody gave an argv: worker_pick's."""
    pick = worker_pick(s, task, agent=agent)
    if pick.get("argv") and not (task.get("model") or task.get("effort")):
        return argv_for(pick)
    pick.pop("argv", None)
    pick.setdefault("agent", "claude")
    return argv_for(pick) or agent_argv(pick["agent"])


def argv_model(argv):
    """(model, effort) an agent's argv asks for, as argv_for writes them; None where it doesn't."""
    model = effort = None
    for i, a in enumerate(argv[:-1]):
        nxt = argv[i + 1]
        if a in ("--model", "-m"):
            model = nxt
        elif a == "--effort":
            effort = nxt
        elif a in ("-c", "--config") and nxt.startswith("model_reasoning_effort="):
            effort = nxt.split("=", 1)[1].strip("\"'")
    return model, effort


# ---- the Settings block: the settings that shape a task's work, as words in its brief ---------
# As in Shepherdr: settings only change what goes into an agent's starting prompt. Every brief
# ends with this block, one line per setting; a line that differs from Herdr's default says who
# changed it. The coordinator changes one task's with `add --set key=value` (and --model, --effort,
# --reviewer...). The brief's own words win over the block.

SETTINGS_HEAD = "Settings: (from herdr-task show; where the brief above says otherwise, it wins)"
SET_BY = {"task": "set by the assistant for this task", "folder": "this folder's setting", "machine": "this machine's setting",
          "all": "your setting everywhere", "flag": "the flag file ~/.config/sheepdog/agy-review-unsandboxed"}
MERGE_WORDS = {"ask": "ask the person first: they accept it in the Tasks panel, which merges it",
               "checks": "after checks: once you say ready, Herdr merges it when its checks pass",
               "auto": "on its own: once you say ready, Herdr merges it"}
MUST_WORDS = {"blocker": "blocker", "major": "major and up (blocker, major)", "minor": "everything (blocker, major, minor)"}
# --set's keys (Shepherdr's spellings too) and the task fields they set.
SET_KEYS = {"review": "review", "brief.review": "review", "merge": "merge", "brief.merge": "merge",
            "review.kind": "review_kind", "review-kind": "review_kind", "review_kind": "review_kind", "kind": "review_kind",
            "rounds": "review_rounds", "review.rounds": "review_rounds", "review-rounds": "review_rounds", "review_rounds": "review_rounds",
            "review.min": "review_min", "review_min": "review_min",
            "mustfix": "must_fix", "must-fix": "must_fix", "must_fix": "must_fix", "review.mustfix": "must_fix",
            "agent": "agent", "task.agent": "agent", "model": "model", "task.model": "model", "effort": "effort", "task.effort": "effort",
            "reviewer": "reviewer", "reviewer.agent": "reviewer", "reviewer.model": "reviewer_model", "reviewer.effort": "reviewer_effort",
            "reviewer_model": "reviewer_model", "reviewer-model": "reviewer_model",
            "reviewer_effort": "reviewer_effort", "reviewer-effort": "reviewer_effort",
            "proof": "proof", "brief.proof": "proof", "auto_accept": "auto_accept", "auto-accept": "auto_accept", "autoaccept": "auto_accept",
            "estimate.time": "estimate_time", "estimate.tokens": "estimate_tokens", "estimate.lines": "estimate_lines",
            "priority": "priority", "task.priority": "priority"}
# FAC-103: an urgent task starts before normal queued ones, one slot past the folder's cap if it must.
PRIORITY_WORDS = "urgent: starts before normal queued tasks, in one slot past this folder's \"Tasks at once\" cap if it must"
# The Reviewer sandbox line: whether an agy reviewer here runs sandboxed (tasks.reviewer_sandbox, or the flag file).
REVIEWER_BOX_WORDS = {"on": "on: the agy reviewer runs in a sandbox; agy's keychain sign-in isn't there, so it can stop at \"Sign in required\" (turn it off in Settings)",
                      "off": "off: the agy reviewer runs as the person's own agy, outside the sandbox"}
# The Proof line (as Shepherdr's skill asks of its task agents), on and off.
PROOF_WORDS = {"on": "show verifiable proof in one recipe artifact: each proof with a one-line caption, real and from this work now, "
                     "plus a \"Not proven\" list; then say ready with --proof <its id>",
               "off": "not needed for this task: no proof artifact; say in your summary how you checked it"}
AUTO_ACCEPT_WORDS = {"off": "off: the person accepts it",
                     "owner": "on, but {why}: the person accepts it",
                     "fable": "on: after its review, a Fable round judges it as the owner would; its pass merges it, its findings come back to you",
                     "engine": "on: with its passing review rounds on your final commit, green CI and its proof, Herdr accepts and merges it"}
MERGE_VALUES = {"ask": "ask", "ask me first": "ask", "ask first": "ask", "ask the person first": "ask", "wait": "ask",
                "checks": "checks", "after checks": "checks", "after github checks": "checks", "after checks pass": "checks",
                "auto": "auto", "on its own": "auto", "merge it": "auto", "at once": "auto"}
MUST_VALUES = {"blocker": "blocker", "blockers": "blocker", "critical": "blocker", "major": "major",
               "minor": "minor", "trivial": "minor", "everything": "minor"}
EFFORTS = {"none", "minimal", "lowest", "low", "medium", "high", "xhigh", "max", "ultra"}
ONOFF = {"on": "on", "yes": "on", "true": "on", "1": "on", "off": "off", "no": "off", "false": "off", "0": "off"}
REVIEW_KINDS = {"design": "design", "doc": "doc", "docs": "doc"}


def review_kind(t):
    """A design or doc task's kind (--set review.kind=design|doc), or None."""
    return REVIEW_KINDS.get(str((t.get("set") or {}).get("review_kind") or "").lower())


def set_task(t, key, value):
    """One --set key=value (or flag) on task t; an empty value or "default" goes back to Settings'."""
    field = SET_KEYS.get(key.strip().lower())
    if not field:
        raise Fail(f"--set takes review, review.kind, review.rounds, review.min, review.mustFix, merge, proof, auto_accept, priority, estimate.time, estimate.tokens, estimate.lines, agent, model, effort or reviewer settings, not {key!r}.")
    v = value.strip().strip("\"'").strip()
    sets = t.setdefault("set", {})
    if not v or v.lower() == "default":
        sets.pop(field, None)
        t.pop(field, None)
        if not sets:
            t.pop("set", None)
        return
    low = v.lower()
    if field == "priority":
        if low not in ("urgent", "normal"):
            raise Fail("priority is urgent or normal.")
        sets.pop(field, None) if low == "normal" else sets.update(priority="urgent")  # normal: no setting at all
    elif field in ("review", "proof", "auto_accept"):
        if low not in ONOFF:
            raise Fail(f"{field} is on or off.")
        sets[field] = ONOFF[low]
    elif field == "review_kind":
        if low not in REVIEW_KINDS:
            raise Fail("review.kind is design or doc.")
        sets[field] = REVIEW_KINDS[low]
    elif field == "review_rounds":
        m = re.fullmatch(r"(\d+)\s*(rounds?)?", low)
        if not m or not 0 <= int(m.group(1)) <= 10:
            raise Fail("review.rounds is 1 to 10 (0: no review).")
        sets[field] = int(m.group(1))
    elif field == "review_min":
        if not re.fullmatch(r"\d+", low) or int(low) > 10:
            raise Fail("review.min is 0 to 10: the passing rounds herdr-task's own reviewer must give (0: the worker reviews).")
        sets[field] = int(low)
    elif field == "must_fix":
        if low not in MUST_VALUES:
            raise Fail("review.mustFix is blocker, major or minor.")
        sets[field] = MUST_VALUES[low]
    elif field in ("estimate_time", "estimate_tokens", "estimate_lines"):  # the task's own fields, read by budget.py
        from .budget import parse_lines, parse_time, parse_tokens
        sets.pop(field, None)
        t[field] = {"estimate_time": parse_time, "estimate_tokens": parse_tokens, "estimate_lines": parse_lines}[field](v)
    elif field == "merge":
        if low not in MERGE_VALUES:
            raise Fail('merge is "on its own", "after checks" or "ask me first".')
        sets[field] = MERGE_VALUES[low]
    else:
        # Agents, models and efforts are the task's own fields (worker_pick reads them).
        if field in ("effort", "reviewer_effort") and low not in EFFORTS:
            raise Fail(f"{field.replace('_', ' ')} is one of {', '.join(sorted(EFFORTS))} (or default).")
        sets.pop(field, None)
        t[field] = v
    if not sets:
        t.pop("set", None)


def set_opts(t, args):
    """add/edit's --set pairs and --agent, --model, --effort, --reviewer, --reviewer-model,
    --reviewer-effort, in that order (a --set pair wins over its flag)."""
    for flag in ("agent", "model", "effort", "reviewer", "reviewer_model", "reviewer_effort"):
        v = getattr(args, flag, None)
        if v is not None:
            set_task(t, flag, v)
    for pair in getattr(args, "set", None) or []:
        if "=" not in pair:
            raise Fail(f"--set takes key=value, not {pair!r}.")
        k, v = pair.split("=", 1)
        set_task(t, k, v)
    if getattr(args, "reviewer_model", None) or getattr(args, "reviewer", None) or any(
            SET_KEYS.get(p.split("=", 1)[0].strip().lower()) in ("reviewer", "reviewer_model") for p in getattr(args, "set", None) or []):
        check_reviewer_model(t)


def by_layer(s, space, section, key, value, default):
    """Who set a setting that differs from Herdr's default: "folder", "machine", "all", or None."""
    if value == default:
        return None
    everywhere, machine, mine = settings_layers(s)
    for by, layer in (("folder", mine), ("machine", machine), ("all", everywhere)):
        if key in ((layer.get(section) or {}) if key else {}) or (not key and layer.get(section)):
            return by
    return None


def other_cli(agent):
    """The reviewer by default: the other CLI than the task agent's where it's installed, else its own."""
    return next((o for o in ("claude", "codex") if o != agent and shutil.which(o)), agent)


def cli_here(kind):
    """The last sign-in check's word on a CLI here (takeover.py's cli-check, which the scheduler
    keeps every 15 minutes): "ready", "signed-out", "missing" or None (unknown). Only read, never run."""
    from .takeover import read_checks
    return (((read_checks().get("machines") or {}).get("") or {}).get(kind))


CLI_NAMES = {"claude": "Claude Code", "codex": "Codex"}
LOGIN = {"claude": "claude auth login", "codex": "codex login"}


def codex_models():
    """The model ids Codex offers here: its models_cache.json, which Codex fetches for the account it is signed in
    with (a ChatGPT account gets fewer than an API key, #948); None when there is none to read."""
    home = os.environ.get("CODEX_HOME") or os.path.join(os.path.expanduser("~"), ".codex")
    try:
        with open(os.path.join(home, "models_cache.json")) as f:
            d = json.load(f)
    except (OSError, ValueError):
        return None
    ids = [m.get("slug") for m in (d.get("models") or [] if isinstance(d, dict) else []) if isinstance(m, dict) and m.get("slug")]
    return ids or None


def codex_lacks(model):
    """The models Codex offers here when `model` isn't one of them (it would refuse to start on it), else None."""
    ids = codex_models()
    return ids if model and ids and model not in ids else None


CLAUDE_MODEL = re.compile(r"^(claude-|opus|sonnet|haiku|fable)", re.I)


def check_reviewer_model(t):
    """add/edit: a reviewer model Codex here can't use is refused when it is set (#948), not found at review time."""
    m = t.get("reviewer_model")
    if not m or t.get("reviewer") == "claude" or (not t.get("reviewer") and CLAUDE_MODEL.match(m)):
        return
    ids = codex_lacks(m)
    if ids:
        raise Fail(f"Codex here doesn't offer {m} for the account it is signed in with: pick one of {', '.join(ids)} "
                   "(or leave the reviewer model unset for Codex's own default).")


def codex_login_now():
    """Codex's own word on its sign-in here, asked now (#947): (True, "") signed in, (False, what it said) when not
    (an unreadable auth file says an error, and Codex then opens its sign-in screen), (None, "") when unknown."""
    if not shutil.which("codex"):
        return None, ""
    try:
        r = subprocess.run(["codex", "login", "status"], stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        return None, ""
    out = f"{r.stdout}\n{r.stderr}"
    if re.search(r"^\s*logged in", out, re.I | re.M):
        return True, ""
    said = next((ln.strip() for ln in out.splitlines() if ln.strip()), "")
    return False, said[:160]


def trust_roots(folder):
    """The folders Codex asks to trust for an agent started in folder: the folder itself and its git
    repository's root, which for a worktree is the main checkout's (Codex: "Trusting will apply to
    the repository root")."""
    if not folder:
        return []
    out = [folder] + [r for r in (os.path.realpath(folder),) if r != folder]  # Codex may match the resolved path
    ok, common, _ = git(folder, "rev-parse", "--path-format=absolute", "--git-common-dir")
    if ok and common:
        root = os.path.dirname(common) if os.path.basename(common) == ".git" else \
            git(folder, "rev-parse", "--show-toplevel")[1]
        if root and root not in out:
            out.append(root)
    return out


def reviewer_command(who, model=None, effort=None, folder=None):
    """The command a worker starts its reviewer with (the brief's Reviewer line): read-only, and for
    Codex on its own app server (--no-daemon, as task agents run: a shared one can keep a sign-in
    since replaced, and every turn then fails, #745). Codex would first stop on its own screens,
    nobody at the keys (#798, #753): "Trust this folder?" in a folder it hasn't seen (the task's
    folder and its repository's root are trusted for this run only, as a table: a dotted
    projects."<path>" key breaks on a dot in the path, as in ~/.local) and "Hooks need review"
    when a hook is new or changed (hooks off: a read-only reviewer needs none, and they would run
    outside its sandbox)."""
    if who == "codex":
        a = ["codex", "--no-daemon", "--sandbox", "read-only", "--ask-for-approval", "never", "--disable", "hooks"]
        roots = trust_roots(folder)
        if roots:
            a += ["-c", "projects={" + ",".join(f'{json.dumps(r)}={{trust_level="trusted"}}' for r in roots) + "}"]
        a += ["-m", model] if model else []
        a += ["-c", f'model_reasoning_effort="{effort}"'] if effort else []
    elif who == "claude":
        a = ["claude", "--permission-mode", "plan"]
        a += ["--model", model] if model else []
        a += ["--effort", effort] if effort else []
    else:
        return None
    return shlex.join(a)


def task_settings(s, t, space=None, argv=None, live=False):
    """The settings that shape this task's work, each {label, text, value, by}. live: for a brief, so its Reviewer
    line is checked against what Codex says now (sign-in and models), not only the last periodic check."""
    cfg = settings_for(s)
    k, sets = cfg["tasks"], t.get("set") or {}
    lines = []

    def add(label, text, value, by):
        lines.append({"label": label, "text": text, "value": value, "by": by})

    kind = review_kind(t)
    rounds_cfg = int(k.get("review_rounds") or 0)
    gate = gate_min(s, t)
    # The Review its brief gave (keep_review): a setting changed while it runs is for tasks added
    # later, as the change's notice says (#750); the Assistant's own --set for it still wins.
    kept = t.get("brief_review") if not gate and "review" not in sets and "review_rounds" not in sets and not kind else None
    if gate:  # a gate folder (or review.kind): herdr-task reviews (FAC-13, FAC-69)
        on, rev_by = True, "task" if "review_min" in sets or kind else by_layer(s, None, "tasks", "review_min", gate, 0)
    elif "review" in sets:
        on, rev_by = sets["review"] == "on", "task"
    elif "review_rounds" in sets:
        on, rev_by = sets["review_rounds"] > 0, "task"
    elif kind:
        on, rev_by = True, "task"
    elif kept:
        on, rev_by = bool(kept.get("on")), kept.get("by")
    else:
        on, rev_by = rounds_cfg > 0, by_layer(s, None, "tasks", "review_rounds", rounds_cfg > 0, False)
    here = t.get("mode") != "worktree"
    add("Review", "off" if not on else "on" if not gate else (
        f"herdr-task's own: once you say ready it starts the reviewer itself, for your final commit, and needs {gate} passing "
        f"round{'' if gate == 1 else 's'}; start no reviewer of your own and don't run `reviewed`. Findings come to you as a "
        f"message: fix them, commit, ready again"), on, rev_by)
    if kind:
        add("Review kind", f"{kind} (1 review round, then the person reviews it)", kind, "task")
    # What it runs on: the given argv, else its last agent's, else what it would start with.
    argv = argv or (t["agents"][-1].get("argv") if t.get("agents") else None) or worker_argv(s, t)
    agent = agent_key(argv)
    if on:
        rv = dict(cfg.get("reviewer") or {})
        if t.get("reviewer") and t["reviewer"] != rv.get("agent"):
            rv = {"agent": t["reviewer"]}
        rv_by = "task" if any(t.get(f) for f in ("reviewer", "reviewer_model", "reviewer_effort")) else \
            by_layer(s, None, "reviewer", None, bool(cfg.get("reviewer")), False)
        for f in ("model", "effort"):
            if t.get(f"reviewer_{f}"):
                rv[f] = t[f"reviewer_{f}"]
        want = rv.get("agent") or other_cli(agent)
        who, said = want, ""
        foreign = None
        if want == "codex" and FOREIGN_MODEL.match(rv.get("model") or ""):
            foreign, rv = rv["model"], {**rv, "model": None}
        if want != agent and cli_here(want) == "signed-out":
            # Signed out here, it would sit at its sign-in screen and never review (#745): the task's own CLI does.
            who, rv = agent, {}
        elif want == "codex" and want != agent and live:
            # #947: the periodic check can't read every answer (an unreadable auth file says an error, not "Not logged
            # in"), so a brief asks Codex now; anything but signed in would leave the reviewer on its sign-in screen
            ok, said = codex_login_now()
            if ok is False:
                who, rv = agent, {}
        # #948: a model Codex here doesn't offer this account stops it at once: its own default instead, said
        lacks = codex_lacks(rv.get("model")) if who == "codex" else None
        dropped = rv.get("model")
        if lacks:
            rv = {k: v for k, v in rv.items() if k != "model"}
        text = who + (f", model {rv['model']}" if rv.get("model") else "") + (f", effort {rv['effort']}" if rv.get("effort") else "")
        if foreign and who == "codex":
            text += f" (model {foreign} is another CLI's, not passed to Codex)"
        if lacks:
            text += (f" (Codex's own default model: Codex here doesn't offer {dropped} "
                     "for the account it is signed in with)")
        if who != want:
            text += (f" (your own CLI: {CLI_NAMES.get(want, want)} isn't signed in here"
                     + (f", `{LOGIN.get(want, want + ' login')} status` says \"{said}\"" if said and not re.match(r"not logged in", said, re.I) else "")
                     + f"; the person runs `{LOGIN.get(want, want + ' login')}` on this machine, then asks for the review again)")
        elif not rv.get("agent"):
            text += " (the other CLI)" if who != agent else " (your own CLI: no other is installed)"
        folder = t.get("worktree") or getattr(s, "loc", {}).get("folder")
        cmd = reviewer_command(who, rv.get("model"), rv.get("effort"), folder)
        if cmd:
            text += f"; start it as `{cmd}`"
        add("Reviewer", text, {"agent": who, "model": rv.get("model"), "effort": rv.get("effort"), "command": cmd}, rv_by)
        from .launch import reviewer_sandbox, sandboxes
        if sandboxes([who]):  # an agy reviewer on a Mac (launch.sandboxes)
            box, box_by = reviewer_sandbox(s)
            add("Reviewer sandbox", REVIEWER_BOX_WORDS["on" if box else "off"], box, box_by)
        must = sets.get("must_fix") or (k.get("must_fix") if k.get("must_fix") in LEVELS else "major")
        add("Must fix", MUST_WORDS[must], must, "task" if "must_fix" in sets else by_layer(s, None, "tasks", "must_fix", must, "major"))
        rounds = (sets.get("review_rounds") or max(1, gate)) if kind else (
            sets.get("review_rounds") or (kept or {}).get("rounds") or max(rounds_cfg or 3, gate))
        more = int(t.get("review_rounds_added") or 0)
        add("Review rounds", f"at most {rounds + more}" + (f" ({more} added by the assistant)" if more else ""), rounds + more,
            "task" if "review_rounds" in sets or kind else (kept or {}).get("rounds_by") if kept else
            by_layer(s, None, "tasks", "review_rounds", rounds, 3))
    policy = sets.get("merge") or (k.get("merge") if k.get("merge") in MERGE_WORDS else "ask")
    m_by = "task" if "merge" in sets else by_layer(s, None, "tasks", "merge", policy, "ask")
    if here:
        add("Merge", "nothing to merge: you work in the folder itself, and the person accepts it", policy, m_by)
    else:
        why = merge_wait_why(s, t)
        add("Merge", f"ask me first ({why})" if why else MERGE_WORDS[policy], "ask" if why else policy, None if why else m_by)
        if why or policy == "ask":  # FAC-99: who takes the person's Accept
            from .ask import open_asks
            on, gate = auto_accept_on(s, t), owner_gate(s, t, open_asks(s, t))
            add("Auto-accept", AUTO_ACCEPT_WORDS["off" if not on else "owner" if gate else "fable" if why else "engine"].format(why=gate),
                on, "task" if "auto_accept" in sets else by_layer(s, None, "tasks", "auto_accept", on, True))
        if t.get("base_set"):
            add("Base", f"{t['base']}: your branch starts from it and merges into it", t["base"],
                "folder" if t.get("base_by") in ("project", "folder", "space") else "task")
    proof = sets["proof"] == "on" if "proof" in sets else k.get("proof") is not False
    add("Proof", PROOF_WORDS["on" if proof else "off"], proof,
        "task" if "proof" in sets else by_layer(s, None, "tasks", "proof", proof, True))
    bdir = build_dir_of(s)
    if bdir:
        ref = s.ref(t) if hasattr(s, "ref") else f"{(s.get('prefix') if isinstance(s, dict) else getattr(s, 'prefix', None)) or 'T'}-{t['id']}"
        if TASK_KEY_RE.fullmatch(ref):
            add("Build output", f"{bdir}/{ref}/ (e.g. xcodebuild -derivedDataPath there); never the default DerivedData.",
                f"{bdir}/{ref}/", "folder")
    model, effort = argv_model(argv)
    text = agent + (f", model {model}" if model else "") + (f", effort {effort}" if effort else "")
    a_by = "task" if any(t.get(f) for f in ("agent", "model", "effort")) else \
        by_layer(s, None, "worker", None, bool(cfg.get("worker")), False)
    add("Agent", text, {"agent": agent, "model": model, "effort": effort}, a_by)
    if sets.get("priority") == "urgent":
        add("Priority", PRIORITY_WORDS, "urgent", "task")
    return lines


def settings_block(s, t, space=None, argv=None, live=False):
    lines = task_settings(s, t, argv=argv, live=live)
    rows = [f"- {x['label']}: {x['text']}" + (f" ({SET_BY[x['by']]})" if x["by"] else "") for x in lines]
    # Codex's sandbox can't start here (SHEP-227, #998): the worker hears it in its brief and tells the person.
    word = sandbox_word(lines, argv or (t["agents"][-1].get("argv") if t.get("agents") else None) or worker_argv(s, t))
    if word:
        rows.append(f"- Sandbox: {word}")
    return "\n".join([SETTINGS_HEAD, *rows])


def gate_min(s, t, space=None):
    """The passing rounds a worktree task needs from herdr-task's own reviewer (review.min); 0
    outside gate folders, and for a task in the folder itself."""
    if t.get("mode") != "worktree":
        return 0
    sets = t.get("set") or {}
    if "review_min" in sets:
        return int(sets["review_min"])
    if review_kind(t):
        return 0 if sets.get("review") == "off" or sets.get("review_rounds") == 0 else 1
    return int(settings_for(s)["tasks"].get("review_min") or 0)


def sandbox_word(lines, argv):
    """The Settings block's Sandbox line when this task's worker or reviewer runs Codex in its
    sandbox and that sandbox can't start on this machine (sandbox.check), else None."""
    from . import sandbox
    rv = next((x["value"] for x in lines if x["label"] == "Reviewer"), None) or {}
    if not (sandbox.sandboxed(argv) or rv.get("agent") == "codex"):
        return None
    broken = sandbox.check()
    return sandbox.notice(broken, short=True) if broken else None


def keep_review(s, t, space=None):
    """A fresh brief gives today's Review: kept on the task (brief_review), so its Settings block,
    ready and reviewed keep agreeing with that brief while it runs (#750)."""
    t.pop("brief_review", None)
    rows = {x["label"]: x for x in task_settings(s, t)}
    rev, rounds = rows["Review"], rows.get("Review rounds")
    t["brief_review"] = {"on": bool(rev["value"]), "by": rev["by"]}
    if rounds:
        t["brief_review"].update(rounds=rounds["value"] - int(t.get("review_rounds_added") or 0), rounds_by=rounds["by"])


def review_limits(s, t):
    """(review on, rounds allowed now): the Settings block's Review and Review rounds lines, with
    the rounds the assistant added (review-more)."""
    on, rounds = False, 0
    for x in task_settings(s, t):
        if x["label"] == "Review":
            on = x["value"]
        elif x["label"] == "Review rounds":
            rounds = x["value"]
    return on, rounds


def project_base(s, space=None):
    """The branch a project's new tasks branch from and merge into (tasks.base, per project);
    "" when unset: the folder's current branch."""
    return str(settings_for(s)["tasks"].get("base") or "").strip()


space_base = project_base  # compatibility shim for commands.py until SDF-7


def simulator_pool(s, space=None):
    """This folder's fixed iOS simulator pool (Settings' tasks.simulators: names or UDIDs); [] means off."""
    raw = settings_for(s)["tasks"].get("simulators")
    items = raw.split(",") if isinstance(raw, str) else (raw if isinstance(raw, (list, tuple)) else [])
    return [str(x).strip() for x in items if str(x).strip()]


TASK_KEY_RE = re.compile(r"[A-Za-z]+[0-9]*-[0-9]+")


def build_dir_of(s):
    """This folder's build-output directory (`herdr-task config --build-dir`), or None."""
    d = s if isinstance(s, dict) else getattr(s, "data", None)
    raw = str(((d or {}).get("config") or {}).get("build_dir") or "").strip()
    p = os.path.normpath(os.path.expanduser(raw)) if raw else ""
    return p if p and p != os.sep else None


def ensure_build_dir(s, task):
    """Creates <build-dir>/<task-key>/ when a task starts, if this folder sets a build dir."""
    bdir = build_dir_of(s)
    ref = s.ref(task) if bdir and hasattr(s, "ref") else ""
    if not bdir or not TASK_KEY_RE.fullmatch(ref):
        return None
    target = os.path.join(bdir, ref)
    if os.path.islink(target):
        raise Fail(f"Build output path {target} is a symlink; refusing to use it.")
    try:
        os.makedirs(target, exist_ok=True)
    except OSError as e:
        raise Fail(f"Can't create build output folder {target}: {e}")
    return target


def _rmtree_strict(top):
    if os.path.islink(top) or not os.path.isdir(top):
        return False
    try:
        os.chmod(top, 0o700)
    except OSError:
        pass
    with os.scandir(top) as it:
        for e in it:
            if e.is_symlink():
                os.unlink(e.path)
            elif e.is_dir(follow_symlinks=False):
                _rmtree_strict(e.path)
            else:
                try:
                    os.unlink(e.path)
                except PermissionError:
                    os.chmod(e.path, 0o600)
                    os.unlink(e.path)
    os.rmdir(top)
    return True


def remove_build_dir(s, task):
    """Removes <build-dir>/<task-key>/ on done when resolved directly under the build dir."""
    bdir = build_dir_of(s)
    ref = s.ref(task) if bdir and hasattr(s, "ref") else ""
    entry = os.path.join(bdir, ref) if bdir and TASK_KEY_RE.fullmatch(ref) else ""
    if not entry or not os.path.isdir(bdir) or os.path.islink(entry) or not os.path.isdir(entry):
        return False
    real_bdir, real_entry = os.path.realpath(bdir), os.path.realpath(entry)
    if real_bdir == os.sep or real_entry == real_bdir or os.path.dirname(real_entry) != real_bdir or os.path.basename(real_entry) != ref:
        return False
    try:
        return _rmtree_strict(real_entry)
    except OSError:
        return False


# Shepherdr settings > Tasks > Branch names (tasks.branch, #570): a folder's new task branches.
# {n}: the task's number, {key}: its id (SHEP-4), {slug}: its title's words. Unset: this.
BRANCH_PATTERN = "task/{n}-{slug}"


def branch_pattern_problem(pattern):
    """Why a Branch names pattern can't be used, or None (core/settings.js branchPattern says the same)."""
    p = str(pattern or "").strip()
    if not p:
        return None
    if "{n}" not in p and "{key}" not in p:
        return "Put {n} or {key} in it, so each task's branch is its own."
    if re.search(r"\{(?!(?:n|key|slug)\})[^}]*\}", p):
        return "Only {n}, {key} and {slug} can be filled in."
    sample = p.replace("{n}", "4").replace("{key}", "ABC-4").replace("{slug}", "fix-login")
    if (sample.startswith("-") or sample.startswith("/") or sample.endswith("/") or sample.endswith(".") or sample.endswith(".lock")
            or re.search(r"[\s~^:?*\[\\]|\.\.|@\{|//|/\.", sample)):
        return "That isn't a name git allows for a branch."
    return None


def merge_wait_why(s, t):
    """Why a task always waits in Ready for the person's Accept, whatever Settings' merge; or None."""
    if review_kind(t):
        return "design/doc tasks always wait for your Accept"
    from .ask import open_asks
    if open_asks(s, t):
        return "tasks with open questions always wait for your Accept"
    return None


# FAC-99: asks only the owner answers (herdr-task ask --kind): their task never auto-accepts.
OWNER_ASK_KINDS = ("release", "paid-key", "delete", "install", "reset", "owner", "decision", "gate", "rule")
FABLE_CHANGES = 2  # Fable's change requests on a task, then the person decides
FABLE = {"agent": "claude", "model": "claude-fable-5-2", "effort": None}


def auto_accept_on(s, t):
    """tasks.auto_accept (on by default), or the task's own --set auto_accept=on|off."""
    v = (t.get("set") or {}).get("auto_accept")
    return v == "on" if v else settings_for(s)["tasks"].get("auto_accept") is not False


def owner_gate(s, t, asks):
    """Why only the person may accept this task (auto-accept off, or an open owner ask), or None."""
    if not auto_accept_on(s, t):
        return "auto-accept is off"
    kinds = sorted({a.get("kind") for a in asks if a.get("kind") in OWNER_ASK_KINDS})
    if kinds:
        return f"an open {'/'.join(kinds)} question is yours to answer"
    # Only a plan question its own worker raised is Fable's to judge; one its Assistant asked is the person's.
    workers = {a.get("address") for a in t.get("agents") or [] if isinstance(a, dict)}
    return "an open question its Assistant asked is yours to answer" if any(a.get("to") not in workers for a in asks) else None


def fable_runs(t):
    """The terminals of Fable's review rounds (its review_runs, by model)."""
    return {r.get("address") for r in t.get("review_runs") or [] if r.get("model") == FABLE["model"]}


def fable_said(t, verdict):
    """How many of Fable's rounds on this task gave this verdict."""
    fable = fable_runs(t)
    return sum(1 for r in t.get("reviews") or [] if r.get("by") in fable and r.get("verdict") == verdict)


def fable_due(s, t):
    """A judgement task (design/doc, or open plan questions) that auto-accept hands to a Fable round."""
    from .ask import open_asks
    asks = open_asks(s, t)
    return (t.get("mode") == "worktree" and bool(review_kind(t) or asks) and not owner_gate(s, t, asks)
            and fable_said(t, "changes") < FABLE_CHANGES)


def reviewed_for_head(s, t, head):
    """Reviews on `head`, or on the latest earlier head that `head` extends only with base merges."""
    all_revs = t.get("reviews") or []
    if not head:
        return []
    revs = [r for r in all_revs if r.get("head") == head]
    if revs:
        return revs
    wt = t.get("worktree") if t.get("mode") == "worktree" else None
    folder = (s.loc or {}).get("folder") or ""
    git_dir = wt if wt and os.path.exists(os.path.join(wt, ".git")) else (folder if folder and os.path.exists(os.path.join(folder, ".git")) else None)
    if not git_dir:
        return []
    from .budget import merge_only_commits
    seen, base, branch = set(), t.get("base") or "main", t.get("branch")
    for r in reversed(all_revs):
        rh = r.get("head")
        if rh and rh != head and rh not in seen:
            seen.add(rh)
            if merge_only_commits(git_dir, base, rh, head, branch):
                return [x for x in all_revs if x.get("head") == rh]
    return []


def accept_bar(s, t):
    """(who accepts, why not): ("engine"|"fable", None) when auto-accept takes the person's place,
    else (None, what's missing). Green CI is checked where it merges (merge_now)."""
    from .ask import open_asks
    asks, head = open_asks(s, t), t.get("head")
    if why := owner_gate(s, t, asks):
        return None, why
    if t.get("mode") != "worktree" or t.get("nothing_to_merge"):
        return None, "nothing to merge"
    revs = reviewed_for_head(s, t, head)
    if review_kind(t) or asks:
        ok = any(r.get("by") in fable_runs(t) and r.get("verdict") == "pass" for r in revs)
        return ("fable", None) if ok else (None, "Fable asked for changes twice" if fable_said(t, "changes") >= FABLE_CHANGES
                                           else "waits for Fable's review round")
    gate = gate_min(s, t)  # at least one passing round, even with review off
    got = sum(1 for r in revs if ((r.get("started_by") == "herdr-task" and r.get("verdict") == "pass") if gate else
                                  r.get("result") in ("report", "pass")))
    if got < max(1, gate):
        return None, f"{got} of {max(1, gate)} passing review rounds on its final commit"
    sets = t.get("set") or {}
    if (sets["proof"] == "on" if "proof" in sets else settings_for(s)["tasks"].get("proof") is not False) \
            and not t.get("proof"):
        return None, "no proof artifact attached"
    if not t.get("pr"):
        return None, "no pull request, so no CI to check"
    return "engine", None


def merge_policy(s, t):
    """How a ready task merges: --set merge or Settings'; one that would wait for the person (ask,
    design/doc, open asks) merges after checks when auto-accept's bar holds (FAC-99)."""
    p = (t.get("set") or {}).get("merge") or settings_for(s)["tasks"].get("merge")
    p = p if p in MERGE_WORDS else "ask"
    if p != "ask" and not merge_wait_why(s, t):
        return p
    return "checks" if accept_bar(s, t)[0] else "ask"


def merge_hold(s, t):
    """A pending merge to drop: the task now waits for the person (FAC-94's waits, or auto-accept's bar no longer met)."""
    return merge_policy(s, t) == "ask"


def auto_accepter(s, t):
    """"engine"/"fable" when auto-accept is what lets this task merge (else it waits for the person), else None."""
    p = (t.get("set") or {}).get("merge") or settings_for(s)["tasks"].get("merge")
    if p in ("auto", "checks") and not merge_wait_why(s, t):
        return None
    return accept_bar(s, t)[0]


def accepted_word(t):
    """Who accepted a task merged without the person: "engine"/"fable" (FAC-99), else "auto"."""
    return (t.get("auto_accepted") or {}).get("by") or "auto"

def agent_argv(key, argv_json=None):
    if argv_json:
        argv = json.loads(argv_json)
        if not isinstance(argv, list) or not argv:
            raise Fail("--argv must be a JSON list")
        argv = [str(x) for x in argv]
    else:
        argv = list(AGENTS.get(key, [key]))
    # Codex runs its commands outside hc without --no-daemon.
    if agent_key(argv) == "codex" and "--no-daemon" not in argv:
        argv.append("--no-daemon")
    return argv


def agent_key(argv):
    return os.path.basename(argv[0])


def bare(i):
    """Ids as labels use them: "vm/sp12" -> "sp12"."""
    return i.split("/")[-1] if i else i


def machine_limits(machine=None):
    """A machine's start limits (Settings > Machines; HERD-40): Everywhere's ("everywhere" ->
    "machine"), then that machine's own ("machines" -> its key -> "machine"; "local" is this
    computer, and a file Herdr pushed to another machine names it in "here"). Each is a number,
    or null for off; one not set is Shepherdr's default (machine.HOLD)."""
    from .machine import hold_limits
    try:
        with open(SETTINGS) as f:
            d = json.load(f)
    except (OSError, ValueError):
        d = {}
    key = machine or d.get("here") or "local"
    out = dict((d.get("everywhere") or {}).get("machine") or {})
    out.update(((d.get("machines") or {}).get(key) or {}).get("machine") or {})
    return hold_limits(out)


def machine_option(name, default=None, machine=None):
    """One of a machine's own settings (Settings > Machines; HERD-61: low_priority, worker_memory):
    Everywhere's "machine" layer, then the machine's own; set in neither: default."""
    try:
        with open(SETTINGS) as f:
            d = json.load(f)
    except (OSError, ValueError):
        d = {}
    if not isinstance(d, dict):
        return default
    key = machine or d.get("here") or "local"
    out = default
    for layer in ((d.get("everywhere") or {}).get("machine") or {}, ((d.get("machines") or {}).get(key) or {}).get("machine") or {}):
        if name in layer:
            out = layer[name]
    return out


def machine_name():
    """This machine's name as Herdr's window knows it (a pushed settings file's "here"), else None."""
    try:
        with open(SETTINGS) as f:
            return json.load(f).get("here") or None
    except (OSError, ValueError, AttributeError):
        return None
