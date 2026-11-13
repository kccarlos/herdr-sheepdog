"""Fixed iOS simulator pool per folder (FAC-72): each task leases one simulator from Settings'
`tasks.simulators` under the store's lock, never creates another, and frees + shuts it down on end."""

import json, os, re, shlex, shutil, subprocess

from . import store as _store
from .base import Fail, hc_json, now
from .output import say
from .settings import simulator_pool
from .store import is_task_list, locate, prefixes_in_use, read_list, Store, store_files

ENDED = ("done", "stopped", "cancelled", "canceled")
off_message = lambda folder: f"Simulator pool feature is off for {folder} (no simulators configured)."


def _simctl_devices():
    if not (xc := shutil.which("xcrun")):
        return None
    try:
        r = subprocess.run([xc, "simctl", "list", "devices", "--json"], stdin=subprocess.DEVNULL,
                           stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, universal_newlines=True, timeout=15)
        raw = json.loads(r.stdout).get("devices") if r.returncode == 0 and r.stdout.strip() else None
    except (OSError, ValueError, subprocess.SubprocessError, AttributeError):
        return None
    rows = [d for rt in sorted(raw, reverse=True) for d in (raw.get(rt) or [])] if isinstance(raw, dict) else (raw if isinstance(raw, list) else [])
    return [d for d in rows if isinstance(d, dict) and d.get("udid")] or None


def pool_devices(pool):
    catalog, out, seen = _simctl_devices(), [], set()
    for item in pool:
        if not catalog:
            if item not in seen:
                seen.add(item); out.append({"pool": item, "name": item, "udid": item})
            continue
        if any(d["udid"] in seen and d["udid"].lower() == item.lower() for d in catalog):
            continue
        groups = ([d for d in catalog if d.get("isAvailable", True) and d["udid"] not in seen], [d for d in catalog if d["udid"] not in seen])
        hit = (next((d for g in groups for d in g if d["udid"].lower() == item.lower()), None)
               or next((d for g in groups for d in g if d.get("name") == item), None))
        if not hit:
            raise Fail(f"Simulator {item!r} in Settings isn't in `xcrun simctl list devices`.")
        seen.add(hit["udid"]); out.append({"pool": item, "name": hit.get("name") or hit["udid"], "udid": hit["udid"]})
    return out


def shutdown_sim(udid):
    if not (xc := shutil.which("xcrun") if udid else None):
        return False
    try:
        subprocess.run([xc, "simctl", "shutdown", str(udid)], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
        return True
    except (OSError, subprocess.SubprocessError):
        return False


def _task_ended(s, t, check_live=False):
    dropped = any(d.get("decision") == "drop" and (d.get("decided_at") or 0) >= (t.get("reopened_at") or 0) for d in (t.get("recaps") or [])[-1:])
    if t.get("state") in ENDED or (t.get("merged") and not t.get("kept_open")) or dropped:
        return True
    if not check_live or t.get("state") != "running" or not t.get("agents"):
        return False
    from .agents import agent_running, detail, is_helper, review_running, starting
    if starting(t) or review_running(t):
        return False
    try:
        d = hc_json("ls", "--label", f"herdr.task={s.key(t)}")
    except Fail:
        return t.get("alive") is False
    if d is None:
        return False
    live = [k for k in d.get("terminals") or [] if k.get("state") == "live"]
    mine = {k.get("address") for k in live}
    return not any(agent_running(detail(k)) for k in live if not is_helper(k, mine))


def release_task(s, t):
    cur, leases = t.pop("simulator", None), s.data.get("simulator_leases") or {}
    udids = [cur["udid"]] if isinstance(cur, dict) and cur.get("udid") else []
    for u, rec in list(leases.items()):
        if isinstance(rec, dict) and rec.get("task") == t.get("id"):
            leases.pop(u, None)
            cur = cur or {k: rec[k] for k in ("pool", "name", "udid", "leased_at") if k in rec}
            udids += [u] if u not in udids else []
    if not leases:
        s.data.pop("simulator_leases", None)
    if cur is not None or udids:
        s.dirty = True
    for u in udids:
        shutdown_sim(u)
    return cur


def _reap(s, check_live=False):
    freed = [{"task": s.ref(t), "name": r.get("name"), "udid": r.get("udid")}
             for t in list(s.data.get("tasks") or []) if t.get("simulator") and _task_ended(s, t, check_live) and (r := release_task(s, t))]
    by_id, leases = {t["id"]: t for t in s.data.get("tasks") or []}, s.data.get("simulator_leases") or {}
    for u, rec in list(leases.items()):
        t = by_id.get(rec.get("task") if isinstance(rec, dict) else None)
        if t is None or _task_ended(s, t, check_live):
            leases.pop(u, None); t and t.pop("simulator", None); s.dirty = True; shutdown_sim(u)
            freed.append({"task": (rec or {}).get("ref") or "", "name": (rec or {}).get("name") or u, "udid": u})
    if not leases:
        s.data.pop("simulator_leases", None)
    return freed


def simulator_pass(report, folder=None):
    for p in store_files(folder):
        d = read_list(p)
        if d and os.path.isdir(d.get("folder") or "") and (d.get("simulator_leases") or any(t.get("simulator") for t in d.get("tasks") or [])):
            try:
                with Store(locate(d["folder"])) as s:
                    if freed := _reap(s, check_live=True):
                        report.setdefault("simulators_released", []).extend(freed)
            except Fail:
                continue


def _match_dev(rec, devs):
    u, p, n = str((rec or {}).get("udid") or "").lower(), (rec or {}).get("pool"), (rec or {}).get("name")
    return next((d for d in devs if u and d["udid"].lower() == u), None) or next((d for d in devs if (p and d["pool"] == p) or (n and d["name"] == n)), None)


def active_leases(s, devs):
    out = {}
    for t in s.data.get("tasks") or []:
        if isinstance(rec := t.get("simulator"), dict) and not _task_ended(s, t):
            dev = _match_dev(rec, devs)
            if (uid := dev["udid"] if dev else rec.get("udid")) and uid not in out:
                out[uid] = {"task": t["id"], "ref": s.ref(t), "name": (dev or rec).get("name") or uid, "udid": uid, "leased_at": rec.get("leased_at")}
    return out


def simulator_line(s, task, space=None):
    if not simulator_pool(s, space or task.get("space")):
        return ""
    from .launch import own_path
    ht, folder = own_path(), shlex.quote(s.loc["folder"])
    return (f" This folder shares a fixed iOS simulator pool: lease a simulator with `{ht} simulator lease {s.ref(task)} --folder {folder}` "
            f"before booting or testing on one (if all are taken, wait or work on something else), never create or boot another one, "
            f"and release it when done with `{ht} simulator release {s.ref(task)} --folder {folder}`.")


def _open_for_task(args):
    explicit = getattr(args, "id", None) or getattr(args, "task_opt", None)
    env_ref = None if explicit else ((os.environ.get("HERDR_BROWSER_TASK") or "").strip() or None)
    if not explicit and not env_ref and os.environ.get("HC_TERMINAL_ID"):
        from .launch import caller_labels
        env_ref = (caller_labels().get("herdr.task") or "").strip() or None
    ref = explicit or env_ref
    folder = getattr(args, "folder", None)
    if not folder and ref:
        if "#" in ref[1:]:
            d = read_list(os.path.join(_store.STORE, ref.rpartition("#")[0] + ".json"))
            folder = d["folder"] if is_task_list(d) and os.path.isdir(d["folder"]) else None
        elif m := re.fullmatch(r"([A-Za-z]+[0-9]*)-[0-9]+", str(ref).strip()):
            folder = prefixes_in_use().get(m.group(1).upper())
    return Store(locate(folder), write=True), explicit, env_ref


def cmd_simulator(args):
    sub = getattr(args, "simulator", None) or "list"
    if sub == "list":
        with Store(locate(getattr(args, "folder", None)), write=False) as s:
            if not (pool := simulator_pool(s)):
                return say(args, {"enabled": False, "folder": s.loc["folder"], "simulators": [], "message": (m := off_message(s.loc["folder"]))}, m)
            devs = pool_devices(pool); held = active_leases(s, devs)
            rows = [{"name": d["name"], "udid": d["udid"], "holder": (held.get(d["udid"]) or {}).get("ref"), "leased_at": (held.get(d["udid"]) or {}).get("leased_at")} for d in devs]
        return say(args, {"enabled": True, "folder": s.loc["folder"], "simulators": rows}, "\n".join(f"{r['name']} ({r['udid']}) — {'held by ' + r['holder'] if r['holder'] else 'free'}" for r in rows))
    store_ctx, explicit, env_ref = _open_for_task(args)
    with store_ctx as s:
        if explicit and "#" in explicit[1:] and explicit.rpartition("#")[0] != s.slug:
            raise Fail(f"{explicit} isn't a task in {s.loc['folder']}.")
        own_env = env_ref if env_ref and ("#" not in env_ref[1:] or env_ref.rpartition("#")[0] == s.slug) else None
        paths = [os.path.realpath(os.path.expanduser(p)) for p in (getattr(args, "folder", None), os.getcwd()) if p]
        hits = [] if (explicit or own_env) else [t for t in s.data.get("tasks") or [] if t.get("worktree") and any(p == (wt := os.path.realpath(t["worktree"])) or p.startswith(wt + os.sep) for p in paths)]
        try:
            t = s.find(explicit or own_env) if (explicit or own_env) else (hits[0] if len(hits) == 1 else None)
        except Fail:
            if not simulator_pool(s):
                if sub == "release":
                    return say(args, {"enabled": False, "folder": s.loc["folder"], "released": None, "message": (m := off_message(s.loc["folder"]))}, m)
                raise Fail(off_message(s.loc["folder"]))
            raise
        pool = simulator_pool(s, t.get("space") if t else None)
        if sub == "release":
            if t is not None and t.get("simulator"):
                return say(args, {"enabled": bool(pool), "task": s.ref(t), "released": (prev := release_task(s, t))}, f"{s.ref(t)} released {prev['name']} ({prev['udid']}).")
            if not pool:
                return say(args, {"enabled": False, "folder": s.loc["folder"], "released": None, "message": (m := off_message(s.loc["folder"]))}, m)
            if t is None:
                raise Fail(f"Pass a task id (like {s.prefix}-1) or run inside a task's worker.")
            return say(args, {"enabled": True, "task": s.ref(t), "released": None}, f"{s.ref(t)} holds no simulator lease.")
        if not pool:
            raise Fail(off_message(s.loc["folder"]))
        if t is None:
            raise Fail(f"Pass a task id (like {s.prefix}-1) or run inside a task's worker.")
        if _task_ended(s, t):
            raise Fail(f"{s.ref(t)} has already ended ({t.get('state')}); it can't lease a simulator.")
        _reap(s); devs = pool_devices(pool); held = active_leases(s, devs)
        mine = _match_dev(t.get("simulator"), devs) if isinstance(t.get("simulator"), dict) else None
        if mine and held.get(mine["udid"], {}).get("task") in (None, t["id"]):
            pick, leased_at = mine, t["simulator"].get("leased_at") or now()
        else:
            if t.get("simulator"):
                release_task(s, t); held = active_leases(s, devs)
            if not (pick := next((d for d in devs if d["udid"] not in held), None)):
                who = ", ".join(f"{d['name']} ({d['udid']}) held by {held[d['udid']]['ref']}" for d in devs)
                raise Fail(f"All {len(devs)} simulator(s) in {s.loc['folder']} are leased: {who}. Wait or work on something else; never create or boot another simulator.")
            leased_at = now()
        rec = {"pool": pick["pool"], "name": pick["name"], "udid": pick["udid"], "leased_at": leased_at}
        t["simulator"] = rec
        s.data.setdefault("simulator_leases", {})[pick["udid"]] = {**rec, "task": t["id"], "ref": s.ref(t)}
        s.dirty = True
        out = {"task": s.ref(t), "name": rec["name"], "udid": rec["udid"], "leased_at": leased_at}
    say(args, out, f"{out['task']} leased {out['name']} ({out['udid']}).")
