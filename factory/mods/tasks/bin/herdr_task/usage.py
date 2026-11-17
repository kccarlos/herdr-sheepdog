"""Usage pools (docs/factory.md section 7): what is left in each provider's 5-hour and weekly windows."""
import json
import os
import re
import subprocess
import time

PROVIDERS = ("claude", "codex", "antigravity")
FLOOR_5H, FLOOR_WEEK, LONG_5H = 5, 1, 20  # percent left
TTL = 60
# antigravity's extra windows: id suffix -> pool
_AG = {"gemini": "antigravity-gemini", "3p": "antigravity-claude-gpt"}


def _unknown(name):
    return {"name": name, "state": "unknown", "five": None, "week": None, "five_reset": None, "week_reset": None}


def _fill(pool, win):
    """Put one codexbar window into the pool's 5-hour or weekly slot, as % left."""
    if not isinstance(win, dict) or win.get("usedPercent") is None:
        return
    key = {300: "five", 10080: "week"}.get(win.get("windowMinutes"))
    if key:
        pool[key] = round(100 - win["usedPercent"], 1)
        pool[key + "_reset"] = win.get("resetsAt")
        pool["state"] = "ok"


def parse(provider, text):
    """Pools of one `codexbar usage --provider <p> --json` reading. No usable data: one unknown pool."""
    try:
        usage = json.loads(text)[0]["usage"]
        if provider != "antigravity":
            pool = _unknown(provider)
            for k in ("primary", "secondary"):
                _fill(pool, usage.get(k))
            return [pool] if pool["state"] == "ok" else [_unknown(provider)]
        pools = {name: _unknown(name) for name in _AG.values()}
        for w in usage.get("extraRateWindows") or []:
            m = re.fullmatch(r"antigravity-quota-summary-(\w+?)-(?:5h|weekly)", w.get("id", ""))
            if m and m[1] in _AG:
                _fill(pools[_AG[m[1]]], w.get("window"))
        return list(pools.values())
    except (ValueError, LookupError, TypeError, AttributeError):
        return [_unknown(provider)] if provider != "antigravity" else [_unknown(n) for n in _AG.values()]


def _codexbar(provider):
    try:
        return subprocess.run(["codexbar", "usage", "--provider", provider, "--json"], capture_output=True,
                              text=True, timeout=20).stdout
    except (OSError, subprocess.SubprocessError):
        return ""


def read(cache_dir, fetch=_codexbar, clock=time.time):
    """All pools, cached for a minute in <cache_dir>/usage-pools.json (shared by every caller)."""
    path = os.path.join(cache_dir, "usage-pools.json")
    try:
        with open(path) as f:
            cached = json.load(f)
        if 0 <= clock() - cached["at"] < TTL:
            return cached["pools"]
    except (OSError, ValueError, LookupError, TypeError):
        pass
    pools = [p for prov in PROVIDERS for p in parse(prov, fetch(prov))]
    os.makedirs(cache_dir, exist_ok=True)
    tmp = "%s.%d.tmp" % (path, os.getpid())
    with open(tmp, "w") as f:
        json.dump({"at": clock(), "pools": pools}, f)
    os.replace(tmp, path)
    return pools


def pick(pools, fits, long_job):
    """The pool to use among those named in `fits`: room above the floors, soonest weekly reset first.
    A long job needs 20% of a 5-hour window and never goes to an unknown pool; None when nothing fits."""
    def room(p):
        five_ok = p["five"] is None or p["five"] > (LONG_5H if long_job else FLOOR_5H)
        return five_ok and (p["week"] is None or p["week"] > FLOOR_WEEK)
    fitting = [p for p in pools if p["name"] in fits]
    for state in ("ok", "unknown"):
        if state == "unknown" and long_job:
            return None
        found = [p for p in fitting if p["state"] == state and (state == "unknown" or room(p))]
        if found:
            return min(found, key=lambda p: p["week_reset"] or "9")
    return None
