"""Device waits (FAC-110): a task blocked only on the physical iPhone (locked, untrusted, not connected) says so and
waits. `herdr-task device-wait "<why>"` keeps {"at", "why"} as the task's "device_wait": Needs you, "Device not
available: <why>", with one alert. The tick checks at most every CHECK_MS whether a paired device is available
(device_available(): True, False or None for unknown) and, once it is, clears the wait and tells the worker to carry on.
devicectl shows pairing and connection, maybe not a lock: a wait recorded again within REWAIT_MS of a clear (a locked
phone read as available) alerts no more and clears only after a check has seen it unavailable, never in a loop."""

import json, os, shutil, subprocess, sys

from .base import Fail, now
from .output import say
from .store import locate, read_list, Store, store_files

CHECK_MS, REWAIT_MS = 60_000, 10 * 60_000
WHY = "Device not available: {}"
CARRY_ON = "The device is available now: carry on with the device check."


def device_available():
    """True when `xcrun devicectl list devices` shows a paired (trusted) device it can reach: tunnelState "connected", or
    "disconnected" (the table's "available (paired)"), not "unavailable"; False when it lists none such;
    None (unknown) off macOS, without xcrun, or on any output it can't read."""
    if sys.platform != "darwin" or not (xc := shutil.which("xcrun")):
        return None
    try:
        r = subprocess.run([xc, "devicectl", "list", "devices", "--timeout", "10", "--quiet", "--json-output", "-"],
                           stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, universal_newlines=True, timeout=30)
        devices = json.loads(r.stdout)["result"]["devices"] if r.returncode == 0 else None
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        return None
    if not isinstance(devices, list):
        return None

    def field(d, name):  # in the old connectionProperties or the newer properties dictionary
        for v in d.values():
            if isinstance(v, dict):
                if name in v:
                    return v[name]
                for x in v.values():
                    if isinstance(x, dict) and name in x:
                        return x[name]
        return None
    return any(isinstance(d, dict) and field(d, "pairingState") == "paired"
               and (field(d, "tunnelState") in ("connected", "disconnected") or field(d, "ddiServicesAvailable") is True) for d in devices)


def cmd_device_wait(args):
    from .commands import send_alert
    from .simulators import _open_for_task
    store_ctx, explicit, env_ref = _open_for_task(args)
    with store_ctx as s:
        if not (ref := explicit or env_ref):
            raise Fail(f"Pass a task id (like {s.prefix}-1) or run it inside a task's worker.")
        t = s.find(ref)
        if args.clear:
            was = t.pop("device_wait", None)
            s.dirty = bool(was)
            return say(args, {"task": s.ref(t), "cleared": bool(was)}, f"{s.ref(t)}: " + ("device wait cleared." if was else "no device wait to clear."))
        why = " ".join(str(args.why or "").split())[:200]
        if not why:
            raise Fail('Say why: herdr-task device-wait "unlock and trust the iPhone"')
        if t.get("state") != "running":
            raise Fail(f"{s.ref(t)} isn't running; it can't wait on a device.")
        again = now() - (t.get("device_cleared_at") or 0) < REWAIT_MS
        first = not t.get("device_wait") and not again
        t["device_wait"] = {**(t.get("device_wait") or ({"needs_off": True} if again else {})), "at": now(), "why": why}
        s.dirty = True
    alerted = False
    if first and os.environ.get("HC_TERMINAL_ID"):  # one alert per wait, never per tick or per repeat
        try:
            send_alert(os.environ["HC_TERMINAL_ID"], WHY.format(why), f"{s.ref(t)} waits until the phone is ready: "
                       "it carries on by itself once it's seen, or say so in its chat.", task=s.ref(t))
            alerted = True
        except (Fail, OSError):
            pass
    say(args, {"task": s.ref(t), "why": why, "alerted": alerted},
        f"{s.ref(t)} waits on the device: {why}. End your turn; you're told when it's available. Carrying on before that "
        f"(the person says it's ready): run `herdr-task device-wait --clear` first.")


def device_pass(report, folder=None, check=None):
    """Clears device waits once a paired device is available (checked at most every CHECK_MS per wait, once per
    pass, outside the store's lock) and tells each task's worker; unknown never clears. A task no longer running
    drops its wait."""
    from .agents import agent_running, detail, HELPER_ROLES, live_agents
    from .news import send_or_hold
    lists = [d for d in map(read_list, store_files(folder))
             if d and os.path.isdir(d.get("folder") or "") and any(t.get("device_wait") for t in d.get("tasks") or [])]
    due = any(t.get("state") == "running" and now() - ((t.get("device_wait") or {}).get("checked") or 0) >= CHECK_MS
              for d in lists for t in d.get("tasks") or [] if t.get("device_wait"))
    seen = (check or device_available)() if due else None
    for d in lists:
        try:
            with Store(locate(d["folder"])) as s:
                for t in s.data["tasks"]:
                    w = t.get("device_wait")
                    if not w:
                        continue
                    if t.get("state") != "running":
                        t.pop("device_wait"); s.dirty = True
                        continue
                    if not due or now() - (w.get("checked") or 0) < CHECK_MS:
                        continue
                    w["checked"] = now(); s.dirty = True
                    if seen is False:
                        w["seen_off"] = True
                    if seen is not True or (w.get("needs_off") and not w.get("seen_off")):
                        continue
                    workers = [k for k in map(detail, live_agents(s.key(t))) if agent_running(k)
                               and (k.get("labels") or {}).get("herdr.role") not in HELPER_ROLES]
                    if workers and send_or_hold(workers[0]["address"], CARRY_ON):
                        t.pop("device_wait")
                        t["device_cleared_at"] = now()
                        report.setdefault("device_ready", []).append(s.ref(t))
        except Fail:
            continue
