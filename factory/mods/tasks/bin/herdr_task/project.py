"""Starting a factory project in one command (`herdr-task project start`, docs/factory.md 12b.4)."""

import argparse, contextlib, io, json, os, re, shlex, shutil
from . import autopilot as ap, base, settings as st
from .base import Fail
from .launch import CLEAN_ENV, coordinators, coordinator_in
from .output import say
from .store import auto_prefix, locate, prefixes_in_use, read_list, Store, store_path

FACTORY_WORKER = {"agent": "agy", "model": "gemini-4-carbon-high", "effort": "", "fast": False, "perm": "full"}
FACTORY_REVIEWER = {"agent": "agy", "model": "gemini-4-argon-high", "effort": "", "fast": False}
FACTORY_TASKS = {"merge": "checks", "review_min": 2}
HEARTBEAT_HEAD = "Requirements: read docs/requirements.md and build toward it milestone by milestone.\n\n"

def json_dict(raw):
    try:
        return d if isinstance(d := json.loads(raw or ""), dict) else None
    except ValueError:
        return None

def try_run(*args):
    try:
        return base.run(list(args))
    except OSError:
        return None

def coord_for_repo(repo, space_id=None):
    try:
        if space_id and (found := coordinator_in(st.bare(space_id))):
            return found, (base.hc_json("get", found) or {}).get("labels") or {}
        for addr in coordinators():
            if (k := base.hc_json("get", addr) or {}).get("state") == "live" and k.get("cwd") and os.path.realpath(k["cwd"]) == repo:
                return addr, k.get("labels") or {}
    except OSError:
        pass
    return None, {}

def detect_check(repo):
    if not os.path.isdir(repo):
        return ""
    pkg = read_list(os.path.join(repo, "package.json"))
    if (t := str(((pkg.get("scripts") if isinstance(pkg, dict) else None) or {}).get("test") or "").strip()) and "no test specified" not in t.lower():
        return "npm test"
    for mk in ("Makefile", "makefile", "GNUmakefile"):
        if os.path.isfile(p := os.path.join(repo, mk)):
            with open(p, errors="replace") as f:
                if re.search(r"(?m)^test\s*:", f.read()):
                    return "make test"
    for sub in ("test", "tests"):
        if os.path.isdir(d := os.path.join(repo, sub)) and any(f.endswith(".py") for _, _, fs in os.walk(d) for f in fs):
            return f"python3 -m unittest discover -s {sub}"
    entries = sorted(os.listdir(repo))
    if any(re.match(r"^(test_.*|.*_test)\.py$", f) for f in entries if os.path.isfile(os.path.join(repo, f))):
        return "python3 -m unittest discover"
    subs = [os.path.join(d, f) for d in entries if not d.startswith(".") and d != "node_modules" and not d.endswith(".xcodeproj")
            and os.path.isdir(dp := os.path.join(repo, d)) for f in sorted(os.listdir(dp))]
    for ext, flag in ((".xcworkspace", "-workspace"), (".xcodeproj", "-project")):
        if hit := next((r for r in entries + subs if r.endswith(ext)), None):
            return f"xcodebuild test {flag} {shlex.quote(hit)} -scheme {shlex.quote(os.path.splitext(os.path.basename(hit))[0])}"
    return ""

def parse_req(req):
    if not (src := (req or "").strip()):
        return None
    if re.match(r"^https?://\S+$", src, re.I):
        return True, src
    if not os.path.isfile(p := os.path.realpath(os.path.expanduser(src))):
        raise Fail(f"No such requirements file: {req}")
    return False, p

def ensure_repo(path, raw_given, dry):
    has_git = os.path.isdir(path) and (os.path.exists(os.path.join(path, ".git")) or (not raw_given and locate(path)["kind"] == "repo"))
    repo = locate(path)["folder"] if has_git else path
    if has_git and (base.git(repo, "rev-parse", "--verify", "--quiet", "HEAD")[0] or base.git(repo, "ls-files")[1]):
        return repo, False, f"Git repo: found {repo}"
    if dry:
        return repo, True, f"Git repo: would {'initialize' if os.path.isdir(path) else 'create and initialize'} {repo}"
    os.makedirs(path, exist_ok=True)
    repo = os.path.realpath(repo)
    c = ["-c", "user.useConfigOnly=false", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
         "commit", "-q", "--allow-empty", "-m", "First commit"]
    if (not has_git and not base.git(repo, "init", "-q", "--initial-branch=main")[0]) or (
            not base.git(repo, *c)[0] and not base.git(repo, "-c", "user.name=Herdr", "-c", "user.email=herdr@localhost", *c)[0]):
        raise Fail("git init failed")
    return repo, True, f"Git repo: initialized {repo}"

def ensure_prefix(repo, given_name, dry):
    _, spath = store_path(repo)
    if existing := (read_list(spath) or {}).get("prefix"):
        return existing, False, f"Tasks: found prefix {existing}"
    seed = (given_name or "").strip() or os.path.basename(repo)
    cand = seed.upper() if given_name and re.fullmatch(r"[A-Za-z]{2,5}", seed) else auto_prefix(seed)
    taken = prefixes_in_use(spath)
    prefix = next(p for n in range(1, 999) if (p := cand if n == 1 else f"{cand}{n}") not in taken)
    if not dry:
        with Store(locate(repo), write=True) as s:
            s.data["prefix"], s.dirty = prefix, True
    return prefix, True, f"Tasks: {'would set' if dry else 'set'} prefix {prefix}"

def ensure_requirements(repo, req_info, dry):
    dest = os.path.join(repo, "docs", "requirements.md")
    if os.path.isfile(dest) or not req_info:
        return (dest if os.path.isfile(dest) else None), False, f"Requirements: {'found docs/requirements.md' if os.path.isfile(dest) else 'none given'}"
    is_url, src = req_info
    if not dry:
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        if is_url:
            with open(dest, "w") as f:
                f.write(f"[{src}]({src})\n")
        else:
            shutil.copyfile(src, dest)
    return dest, True, f"Requirements: {'would ' + ('save link to ' if is_url else 'copy ') + src + ' into' if dry else ('saved link in' if is_url else 'copied to')} docs/requirements.md"

def ensure_space(repo, name, prefix, cfg, dry):
    step = f"herdr-app do space.new --folder {shlex.quote(repo)} --name {shlex.quote(name)}"
    r = try_run("herdr-app", "ls")
    ls_out = json_dict(r.stdout) if r and r.returncode == 0 else None
    for x in (ls_out.get("spaces") or []) if ls_out else []:
        if x.get("id") and "/" not in x["id"] and not x.get("machine") and x.get("folder") and os.path.realpath(x["folder"]) == repo:
            return x["id"], False, None, f"Space: found {x['id']} ({x.get('name') or name})"
    lb = coord_for_repo(repo)[1]
    sid = next((k for k, v in (cfg.get("spaces") or {}).items()
                if "/" not in k and isinstance(v, dict) and v.get("folder") == repo), None) or st.bare(lb.get("herdr.coordinator.space") or lb.get("herdr.space") or "") or None
    if ls_out is not None and dry:
        return sid or prefix.lower(), True, None, f"Space: would create in Herdr window ({step})"
    if ls_out is not None and (cr := try_run("herdr-app", "do", "space.new", "--folder", repo, "--name", name)):
        if (out := json_dict(cr.stdout) if cr.returncode == 0 else None) and out.get("space") and not out.get("error"):
            return st.bare(str(out["space"])), True, None, f"Space: created {st.bare(str(out['space']))} ({name})"
    return sid or prefix.lower(), False, step, f"Space: creating a space needs the Herdr window; run `{step}` ({'found' if sid else 'using'} {sid or prefix.lower()})"

def ensure_settings(repo, space_id, name, given_check, d, dry):
    if d.get("pushed_from"):
        raise Fail("This machine's Shepherdr settings are a pushed copy: start the project from the window's machine.")
    bare_id, spaces = st.bare(space_id), dict(d.get("spaces") or {})
    old_ids = [k for k, v in spaces.items() if k != bare_id and "/" not in k and isinstance(v, dict) and v.get("folder") == repo]
    layer = {**{kk: vv for k in old_ids for kk, vv in spaces[k].items()}, **dict(spaces.get(bare_id) or {})}
    changed = bool(old_ids)
    for k, v in (("folder", repo), ("name", name), ("worker", dict(FACTORY_WORKER)), ("reviewer", dict(FACTORY_REVIEWER))):
        if (layer.get(k) != v) if k == "folder" else not layer.get(k):
            layer[k], changed = v, True
    tasks = dict(layer.get("tasks") or {})
    chk = (given_check.strip() if given_check is not None else detect_check(repo)) if not tasks.get("check") else None
    for k, v in (*FACTORY_TASKS.items(), *((("check", chk),) if chk else ())):
        if tasks.get(k) is None or (k == "check" and not tasks.get("check")):
            tasks[k], changed = v, True
    if tasks != (layer.get("tasks") or {}):
        layer["tasks"] = tasks
    chk_words = f"check: {tasks['check']}" if tasks.get("check") else "check: none"
    if not changed or dry:
        return layer, changed, old_ids, f"Settings: {'would write factory defaults' if changed else 'found'} for {bare_id} ({chk_words})"
    for k in old_ids:
        spaces.pop(k, None)
    spaces[bare_id] = layer
    os.makedirs(os.path.dirname(st.SETTINGS), exist_ok=True)
    with open(tmp := f"{st.SETTINGS}.{os.getpid()}.tmp", "w") as f:
        json.dump({**d, "version": 1, "spaces": spaces}, f, indent=2)
        f.write("\n")
    os.replace(tmp, st.SETTINGS)
    return layer, True, old_ids, f"Settings: wrote factory defaults for {bare_id} ({chk_words})"

def ensure_assistant(repo, space_id, dry):
    bare_id, shared = st.bare(space_id), read_list(os.path.join(base.APP_HOME, "shared.json"))
    slots = (shared.get("coordinator.slots") if isinstance(shared, dict) else None) or {}
    slot, rec = next(((k, v) for k, v in (slots.items() if isinstance(slots, dict) else [])
                      if isinstance(v, dict) and v.get("pane") and v.get("space") in (space_id, bare_id)), (None, None))
    kv = ([f"herdr.chat={rec['pane']}", f"herdr.chat.slot={slot}", f"herdr.chat.origin=slot:{slot}"]
          + ([f"herdr.coordinator.tab={st.bare(str(rec['tab']))}"] if rec.get("tab") else []) if slot else []) \
         + ["herdr.role=coordinator", f"herdr.coordinator.space={bare_id}"]
    addr, cur_lb = coord_for_repo(repo, bare_id)
    if addr:
        if all(cur_lb.get(p.split("=", 1)[0]) == p.split("=", 1)[1] for p in kv):
            return addr, False, f"Assistant: found {addr}"
        if dry:
            return addr, True, f"Assistant: would attach {addr} to {bare_id}"
        if (r := try_run("hc", "label", addr, *kv)) is None or r.returncode != 0:
            raise Fail("hc isn't on PATH (install herdr-core first)." if r is None else ((r.stderr or r.stdout).strip() or f"Couldn't label {addr}."))
        return addr, True, f"Assistant: attached {addr} to {bare_id}"
    if dry:
        return None, True, f"Assistant: would start for {bare_id}" + (f" (slot {slot})" if slot else "")
    tpl_name, one_path = "assistant", None
    if os.path.isfile(tpl_src := os.path.join(st.TEMPLATES, "assistant.md")):
        with open(tpl_src) as f:
            src = f.read()
        if (m := re.match(r"\A---\n([\s\S]*?)\n---\n?", src)) and (cm := re.search(r"(?m)^command:[ \t]*(.+)$", m.group(1))):
            tpl_name, one_path = f"herdr-assistant-{os.getpid()}", os.path.join(st.TEMPLATES, f"herdr-assistant-{os.getpid()}.md")
            with open(one_path, "w") as f:
                f.write(f"---\nname: {tpl_name}\ndescription: One-off assistant start.\n"
                        f"command: {CLEAN_ENV} exec {re.sub(r'^exec\\s+', '', cm.group(1).strip())}\n---\n{src[m.end():]}")
    try:
        r = try_run("hc", "run", "--bg", "--json", "--no-parent", "--keep", "--cwd", repo, "--name", f"{bare_id}-assistant",
                    *[x for p in kv for x in ("--label", p)], tpl_name)
    finally:
        if one_path and os.path.exists(one_path):
            os.remove(one_path)
    if r is None or not (out := json_dict(r.stdout) if r.returncode == 0 else None) or not out.get("address"):
        raise Fail("hc isn't on PATH (install herdr-core first)." if r is None else ((r.stderr or r.stdout).strip() or "Couldn't start the Assistant."))
    return out["address"], True, f"Assistant: started {out['address']}"

def sync_autopilot(repo, space_id, name, old_ids, want_on, asst_changed, dry):
    bare_id, ap_spaces = st.bare(space_id), (read_list(ap.STATE) or {}).get("spaces") or {}
    stale = [k for k, v in ap_spaces.items() if k != bare_id and "/" not in k and (k in old_ids or (isinstance(v, dict) and v.get("folder") == repo))]
    cur = {**{kk: vv for k in stale for kk, vv in (ap_spaces.get(k) or {}).items()}, **dict(ap_spaces.get(bare_id) or {})}
    was_on = bool(cur.get("on"))
    has_beat = want_on and any(bool((ap.autopilot_layers(st.read_settings(), k)[1].get("heartbeat") or "").strip()) for k in (bare_id, *old_ids))
    def move():
        with ap.state(write=True) as d:
            m = {**{kk: vv for k in stale for kk, vv in (d["spaces"].pop(k, None) or {}).items()}, **dict(d["spaces"].get(bare_id) or {})}
            m["name"], m["folder"] = m.get("name") or name, repo
            d["spaces"][bare_id] = m
            if m.get("on") and m.get("pending"):
                ap.deliver(bare_id, m)
    if not want_on or (has_beat and was_on and not stale):
        if (stale or (asst_changed and was_on and cur.get("pending"))) and not dry:
            move()
        line = f"Autopilot: {'would move' if dry else 'moved'} to {bare_id}" if stale else (f"Autopilot: found on for {bare_id}" if (want_on or was_on) else None)
        return (True if want_on else was_on), bool(stale), line
    if dry:
        parts = (["move to " + bare_id] if stale and was_on else []) + ([] if has_beat else ["set heartbeat -> docs/requirements.md"]) + ([] if was_on else [f"turn on for {bare_id}"])
        return True, True, f"Autopilot: would {' and '.join(parts)}"
    if not has_beat:
        st.write_autopilot(bare_id, {"heartbeat": HEARTBEAT_HEAD + ap.STARTER})
    if stale or (was_on and not cur.get("folder")):
        move()
    if not was_on:
        with contextlib.redirect_stdout(io.StringIO()):
            ap.cmd_autopilot(argparse.Namespace(autopilot="on", space=bare_id, everywhere=False, json=True, name=name, space_folder=repo))
    return True, True, f"Autopilot: on for {bare_id} (heartbeat -> docs/requirements.md)"

def cmd_project(args):
    if args.project != "start":
        raise Fail("Usage: herdr-task project start <folder>")
    dry, raw = bool(getattr(args, "dry_run", False)), args.folder if args.folder is not None else getattr(args, "folder_opt", None)
    if raw is not None and (not raw.strip() or "\0" in raw):
        raise Fail("Pick a folder.")
    path = os.path.realpath(os.path.expanduser((raw or os.getcwd()).strip()))
    if path in ("/", os.path.realpath(base.HOME)) or (os.path.exists(path) and not os.path.isdir(path)):
        raise Fail("Pick a project folder, not ~ or /." if os.path.isdir(path) else f"Not a folder: {path}")
    pre = locate(path)["folder"] if os.path.isdir(path) and (os.path.exists(os.path.join(path, ".git")) or (raw is None and locate(path)["kind"] == "repo")) else path
    req_info = None if os.path.isfile(os.path.join(pre, "docs", "requirements.md")) else parse_req(getattr(args, "requirements", None))
    if not isinstance(cfg := (read_list(st.SETTINGS) if os.path.exists(st.SETTINGS) else {}), dict):
        raise Fail(f"Can't read {st.SETTINGS} (fix invalid JSON first).")
    repo, c1, l1 = ensure_repo(path, raw is not None, dry)
    name = (args.name or "").strip() or os.path.basename(repo)
    prefix, c2, l2 = ensure_prefix(repo, args.name, dry)
    req_path, c3, l3 = ensure_requirements(repo, req_info, dry)
    space_id, c4, space_step, l4 = ensure_space(repo, name, prefix, cfg, dry)
    layer, c5, old_ids, l5 = ensure_settings(repo, space_id, name, args.check, cfg, dry)
    assistant, c6, l6 = ensure_assistant(repo, space_id, dry)
    ap_on, c7, l7 = sync_autopilot(repo, space_id, name, old_ids, bool(args.autopilot), c6, dry)
    lines, changed = [l1, l2, l3, l4, l5, l6, *([l7] if l7 else [])], any((c1, c2, c3, c4, c5, c6, c7))
    out = {"folder": repo, "name": name, "prefix": prefix, "space": st.bare(space_id), "space_step": space_step,
           "assistant": assistant, "check": (layer.get("tasks") or {}).get("check"), "requirements": req_path,
           "autopilot": ap_on, "dry_run": dry, "changed": changed, "steps": lines}
    say(args, out, "\n".join(f"[dry-run] {x}" if dry else x for x in lines))
