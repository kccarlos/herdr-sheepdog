"""Watchdog: restarts a live Assistant that is stuck, or alerts the person (FAC-62)."""

import hashlib, json, os, re, subprocess

from . import news
from .agents import agent_running, detail, held_ask, live_agents, STARTING_MS, status_word, task_status
from .base import addon_off, APP_HOME, Fail, guard, hc_json, HOME, now, PACKAGE, run, STORE
from .commands import send_alert
from .news import send_or_hold, stall_of, typing
from .settings import bare
from .store import read_list, store_files

STUCK_MS = 30 * 60000              # Needs you / Failed / Stalled, or "working" with an unchanged screen
RESTART_COOLDOWN_MS = 2 * 3600000  # stuck again within 2 hours of a restart: alert instead of restarting
NEWS_GRACE_MS = STARTING_MS        # time after news lands for the Assistant to start its turn
WATCHDOG = os.path.join(STORE, ".watchdog.json")
TEMPLATE_LABEL = "herdr.template"  # the hash of assistant.md an Assistant started with, set when the watchdog first sees it (FAC-78)
API_ERROR = re.compile(r"API Error[: ]|Please run /login|\boverloaded\b|rate.?limit(?:ed| reached| exceeded|_error)", re.I)
SIGNED_OUT = re.compile(r"not logged in|invalid (?:x-)?api key|authentication_error|oauth token .*expired|credit balance", re.I)  # a retry can't fix it
NUDGE = "Carry on with what you were doing; the last turn hit an API error."


def restart_assistant(k):
    """Restarts Assistant `k` with coordinator/restart.sh (HERDR_RESTART_CHAT when chat-backed)."""
    guard(["hc"])
    repo, inst = os.path.normpath(os.path.join(PACKAGE, "..", "..", "..", "coordinator", "restart.sh")), os.path.join(APP_HOME, "mods", "coordinator", "restart.sh")
    script = inst if os.path.isfile(inst) else repo if os.path.isfile(repo) else inst
    env = {x: v for x, v in os.environ.items() if x not in ("HC_TERMINAL_ID", "HERDR_RESTART_CHAT", "HERDR_RESTART_RESUME")}
    if (k.get("labels") or {}).get("herdr.chat"):
        env["HERDR_RESTART_CHAT"] = k["labels"]["herdr.chat"]
    r = subprocess.run(["bash", script, k["address"]], env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, timeout=120)
    nxt = ((r.stdout or "").strip().splitlines() or [""])[-1].strip()
    return nxt if r.returncode == 0 and nxt else None


def template_info():
    """(hash, mtime in ms) of the installed assistant template, (None, 0) without one."""
    path = os.path.join(HOME, ".config", "herdr-core", "templates", "assistant.md")
    try:
        with open(path, "rb") as f:
            return hashlib.sha1(f.read()).hexdigest()[:16], int(os.path.getmtime(path) * 1000)
    except OSError:
        return None, 0


def api_error(addr, lines=15):
    """The API-error line an Assistant's last turn ended on (the tail of its screen), else None."""
    r = run(["hc", "read", addr, "--lines", "80"])
    tail = [x.strip() for x in (r.stdout or "").splitlines() if x.strip()][-lines:] if r.returncode == 0 else []
    # Only an error after our nudge counts: one above it was handled by the turn the nudge started.
    tail = tail[max((i + 1 for i, x in enumerate(tail) if NUDGE[:13] in x), default=0):]
    return next((x for x in reversed(tail) if API_ERROR.search(x)), None)


def _condition(slug, prefix, t, tasks, lists):
    if t.get("state") not in ("running", "ready"):
        return None
    ref, live = f"{prefix}-{t['id']}", [w for w in map(detail, live_agents(f"{slug}#{t['id']}")) if agent_running(w)]
    status, needs, why = task_status(t, live)
    if status == "needs":
        failed = needs in ("stopped", "provider") or str(why or "").startswith("Couldn't start")
        return ("failed" if failed else "needs-you"), STUCK_MS, f"{ref} {'failed' if failed else 'in Needs you'}", None
    if status == "working":
        stall = stall_of(t, {"status": status}, live)
        return ("stalled", STUCK_MS, f"{ref} stalled", stall[0]) if stall else None
    wait = lambda x: x.get("state") in ("queued", "todo") and not x.get("archived")
    behind = any(wait(x) and t["id"] in (x.get("after") or []) for x in tasks) or any(
        wait(x) and any(not e.get("machine") and e.get("prefix") == prefix and e.get("id") == t["id"] for e in (x.get("after_ext") or [])) for _, _, o in lists for x in o)
    return ("ready", NEWS_GRACE_MS, f"{ref} Ready with tasks waiting behind it", None) if status == "ready" and behind else None


def _stuck_task(space, addr, k, rec, lists, news_data, live_addrs):
    outbox, delivered = news_data.get("outbox") or [], news_data.get("delivered") or {}
    cur_turn, st_ms = k.get("turn") or 0, k.get("status_ms") or 0
    restarted = rec.get("restarted_at") if rec.get("restarted_to") == addr else None
    prev_coord = rec.get("restarted_from") or {}
    for slug, prefix, tasks in lists:
        for t in tasks:
            key = f"{slug}#{t['id']}"
            cond = None if t.get("archived") or bare(t.get("space") or "") != space or any(x.get("key") == key for x in outbox) else _condition(slug, prefix, t, tasks, lists)
            d = delivered.get(key) if cond else None
            if not isinstance(d, dict) or not (d.get("event") == cond[0] or {d.get("event"), cond[0]} <= {"needs-you", "failed"}):
                continue
            kind, min_age, desc, stall_mark = cond
            if (kind == "stalled" and d.get("stall") != stall_mark) or d.get("pending") or not d.get("at"):
                continue
            if d.get("to") == addr:
                cond_since, reached_at = d.get("since") or d["at"], d["at"]
                had_turn = cur_turn > (d.get("turn") or 0) or (bool(d.get("idle")) and st_ms > d["at"])
            elif restarted and d.get("to") not in live_addrs and d["at"] <= restarted and not (
                    prev_coord.get("address") == d.get("to") and (prev_coord.get("turn") or 0) > (d.get("turn") or 0)):
                cond_since, reached_at, had_turn = restarted, restarted, cur_turn > 0 or st_ms > restarted
            else:
                continue
            if not had_turn and now() - cond_since >= min_age and now() - reached_at >= NEWS_GRACE_MS:
                return desc if kind == "ready" else f"{desc} for {(now() - cond_since) // 60000} min"
    return None


def _wake_inflight(ap_spaces, space, addr, k, ap):
    if addon_off("autopilot"):
        return False
    st = status_word(k)
    for sp, a in (ap_spaces or {}).items():
        f = (a or {}).get("inflight") if (a or {}).get("on") is not False else None
        if isinstance(f, dict) and (sp == space or f.get("to") == addr) and (not f.get("to") or f["to"] == addr):
            age = now() - (f.get("at") or 0)
            if (st == "working" and age <= ap.TURN_MAX_MS) or (
                    st != "working" and age <= ap.IDLE_AFTER_MS and (k.get("turn") or 0) <= (f.get("turn") or 0) and (k.get("status_ms") or 0) <= (f.get("at") or 0)):
                return True
    return False


def watchdog_pass(report):
    """Restarts a stuck local Assistant once with a handoff; stuck again within 2 hours, alerts once."""
    from . import autopilot as ap
    ls = hc_json("ls", "--label", "herdr.role=coordinator")
    if not isinstance(ls, dict):
        return
    raw_prev = read_list(WATCHDOG)
    prev_all = raw_prev if isinstance(raw_prev, dict) else {}
    news_data, held_data, retries = read_list(news.NEWS) or {}, read_list(news.HELD) or {}, read_list(os.path.join(STORE, ".retries.json")) or {}
    ap_spaces = ((read_list(ap.STATE) or {}).get("spaces")) or {}
    lists = [(os.path.basename(p)[:-5], d.get("prefix") or "TASK", d.get("tasks") or []) for p in store_files() for d in [read_list(p)] if isinstance(d, dict)]
    coords = [k for k in ls.get("terminals") or [] if k.get("state") == "live" and "/" not in (k.get("address") or "/")]
    live_addrs = {k["address"] for k in coords}
    state = {s: dict(r) for s, r in prev_all.items() if isinstance(r, dict) and r.get("restarted_at") and now() - r["restarted_at"] < RESTART_COOLDOWN_MS}

    for item in coords:
        addr = item["address"]
        k = None if held_ask(addr) else hc_json("get", addr)
        if not k or k.get("state") != "live" or "/" in (k.get("address") or "/") or not agent_running(k):
            for s, r in (prev_all.items() if k is None else ()):
                if isinstance(r, dict) and addr in (r.get("address"), r.get("restarted_to")):
                    state.setdefault(s, dict(r))
            continue
        labels = dict(k.get("labels") or {}, **{"herdr.role": "coordinator"})
        k["labels"] = labels
        by_prev = next((s for s, r in prev_all.items() if isinstance(r, dict) and addr in (r.get("restarted_to"), r.get("address"))), None)
        raw_space = bare(labels.get("herdr.space") or labels.get("herdr.coordinator.space") or "")
        slot = raw_space or by_prev or labels.get("herdr.chat") or labels.get("herdr.coordinator.tab") or labels.get("herdr.tab") or addr
        rec = dict(state.get(slot) or prev_all.get(slot) or {}, address=addr)
        space = raw_space or rec.get("space") or ""
        if space:
            rec["space"] = space
        if rec.get("alerted") and (rec["alerted"].get("address") != addr or (k.get("turn") or 0) > (rec["alerted"].get("turn") or 0)):
            rec.pop("alerted", None)

        st, why, alert_only, api_why = status_word(k), None, False, None
        tpl, tpl_ms = template_info()
        tpl_has = labels.get(TEMPLATE_LABEL)
        if tpl and not tpl_has and k.get("created_ms"):  # first sight: it started before the template changed, or not
            tpl_has = tpl if k["created_ms"] >= tpl_ms else "unknown"
            run(["hc", "label", addr, f"{TEMPLATE_LABEL}={tpl_has}"])
        if st == "working":
            r = run(["hc", "read", addr, "--lines", "80"])
            if r.returncode == 0:
                digest, prev = hashlib.sha1((r.stdout or "").encode("utf-8", "replace")).hexdigest()[:16], prev_all.get(slot) or {}
                since = prev["screen_since"] if prev.get("address") == addr and prev.get("screen") == digest and prev.get("screen_since") else now()
                rec["screen"], rec["screen_since"] = digest, since
                why = f"working with an unchanged screen for {(now() - since) // 60000} min" if now() - since >= STUCK_MS else None
        else:
            rec.pop("screen", None), rec.pop("screen_since", None)
            booting = not k.get("turn") and k.get("created_ms") and now() - k["created_ms"] < STARTING_MS
            if st in ("idle", "done", None) and space and not booting and not held_data.get(addr) and not (retries.get(addr) or {}).get("due"):
                spell, err = rec.get("api") or {}, None
                if any(not x.get("archived") and bare(x.get("space") or "") == space and x.get("state") in ("running", "queued", "todo") for _, _, o in lists for x in o):
                    err = api_error(addr)
                if not err:
                    rec.pop("api", None)  # no error (or no work waiting): the spell is over
                elif not spell and not typing(addr, k) and not _wake_inflight(ap_spaces, space, addr, k, ap) and send_or_hold(addr, NUDGE, "news") == "sent":
                    rec["api"] = {"stage": "nudged", "at": now()}
                    report.setdefault("watchdog", []).append({"action": "nudged", "address": addr, "space": space, "why": f"stopped on an API error: {err[:80]}"})
                elif (spell.get("stage") == "nudged" and now() - spell.get("at", 0) >= NEWS_GRACE_MS) or spell.get("stage") == "restarted":
                    api_why, alert_only = f"stopped on an API error: {err[:80]}", spell.get("stage") == "restarted" or bool(SIGNED_OUT.search(err))
                why = api_why or _stuck_task(space, addr, k, rec, lists, news_data, live_addrs)
            if not why and tpl and tpl_has and tpl_has != tpl and st in ("idle", "done", None) and not booting and rec.get("template") != tpl:
                why = "running an old Assistant template"

        if why and not _wake_inflight(ap_spaces, space, addr, k, ap) and not typing(addr, k):
            in_cooldown = bool(rec.get("restarted_at") and now() - rec["restarted_at"] < RESTART_COOLDOWN_MS)
            old_tpl = why == "running an old Assistant template"  # an idle one only; never an alert
            nxt = None if in_cooldown or alert_only else restart_assistant(k)
            if nxt:
                if slot == addr:
                    state.pop(slot, None)
                    slot = nxt
                rec = {"address": nxt, "space": space, "restarted_at": now(), "restarted_to": nxt, "restarted_from": {"address": addr, "turn": k.get("turn") or 0},
                       **({"api": {"stage": "restarted", "at": now()}} if api_why else {}), "template": tpl}
                report.setdefault("watchdog", []).append({"action": "restarted", "from": addr, "to": nxt, "space": space, "why": why})
            elif old_tpl:
                if not in_cooldown:
                    rec["template"] = tpl  # one try per template; in the cooldown, the next pass tries
            else:
                if not in_cooldown and not alert_only:
                    rec["restarted_at"] = now()
                if not rec.get("alerted"):
                    sp_name = (ap_spaces.get(space) or {}).get("name") if space else None
                    who = sp_name if sp_name and sp_name != "~" else (space or os.path.basename((k.get("cwd") or "").rstrip("/")) or addr)
                    title = f"{who}'s Assistant is stuck: {why}"
                    try:
                        send_alert(addr, title, body=why, about=True)
                        rec["alerted"] = {"address": addr, "turn": k.get("turn") or 0, "at": now()}
                        if api_why:
                            rec["api"] = {"stage": "alerted", "at": now()}
                        report.setdefault("watchdog", []).append({"action": "alerted", "address": addr, "space": space, "title": title})
                    except Fail:
                        pass
        state[slot] = rec

    if state != prev_all:
        os.makedirs(os.path.dirname(WATCHDOG), exist_ok=True)
        tmp = f"{WATCHDOG}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            json.dump(state, f)
        os.replace(tmp, WATCHDOG)
