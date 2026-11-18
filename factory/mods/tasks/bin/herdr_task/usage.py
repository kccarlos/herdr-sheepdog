"""Usage pools (docs/factory.md section 7): what is left in each provider's 5-hour and weekly windows."""
import json
import os
import re
import shutil
import subprocess
import time

PROVIDERS = ("claude", "codex", "antigravity")
FLOOR_5H, FLOOR_WEEK = 5, 1  # percent left
TTL = 60
# antigravity's extra windows: id suffix -> pool
_AG = {"gemini": "antigravity-gemini", "3p": "antigravity-claude-gpt"}


def _unknown(name, reason="no data"):
    return {"name": name, "state": "unknown", "reason": reason,
            "five": None, "week": None, "five_reset": None, "week_reset": None}


def _fill(pool, win):
    """Put one codexbar window into the pool's 5-hour or weekly slot, as % left."""
    if not isinstance(win, dict) or win.get("usedPercent") is None:
        return
    key = {300: "five", 10080: "week"}.get(win.get("windowMinutes"))
    if key:
        pool[key] = round(100 - win["usedPercent"], 1)
        pool[key + "_reset"] = win.get("resetsAt")
        pool["state"] = "ok"
        pool["reason"] = None


def parse(provider, text, reason="no data"):
    """Pools of one `codexbar usage --provider <p> --json` reading. No usable data: one unknown pool."""
    try:
        usage = json.loads(text)[0]["usage"]
        if provider != "antigravity":
            pool = _unknown(provider, reason)
            for k in ("primary", "secondary"):
                _fill(pool, usage.get(k))
            return [pool]
        pools = {name: _unknown(name, reason) for name in _AG.values()}
        for w in usage.get("extraRateWindows") or []:
            m = re.fullmatch(r"antigravity-quota-summary-(\w+?)-(?:5h|weekly)", w.get("id", ""))
            if m and m[1] in _AG:
                _fill(pools[_AG[m[1]]], w.get("window"))
        return list(pools.values())
    except (ValueError, LookupError, TypeError, AttributeError):
        return [_unknown(n, reason) for n in (_AG.values() if provider == "antigravity" else (provider,))]


def _codexbar(tool, provider):
    try:
        # OAuth avoids provider CLI/PTY fallbacks and their login prompts.
        result = subprocess.run([tool, "usage", "--provider", provider, "--json", "--source", "oauth"],
                                stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=20)
    except FileNotFoundError:
        return parse(provider, "", "not installed")
    except subprocess.TimeoutExpired:
        return parse(provider, "", "timed out")
    except (OSError, UnicodeError):
        return parse(provider, "")
    if result.returncode == 4:
        return parse(provider, "", "timed out")
    auth = re.search(r"not (?:signed|logged) in|(?:sign[ -]?in|log[ -]?in|authentication) required|"
                     r"no [^\n]{0,60}(?:oauth credentials|session key)|oauth credentials not found|unauthorized",
                     result.stderr + "\n" + result.stdout, re.IGNORECASE)
    return parse(provider, result.stdout, "not signed in" if auth else "no data")


def read(cache_dir, fetch=None, clock=time.time):
    """All pools, cached for a minute in <cache_dir>/usage-pools.json (shared by every caller)."""
    tool = shutil.which("codexbar") if fetch is None else None
    source = tool if fetch is None else "fixture"
    path = os.path.join(cache_dir, "usage-pools.json")
    try:
        with open(path) as f:
            cached = json.load(f)
        if cached["tool"] == source and 0 <= clock() - cached["at"] < TTL:
            return cached["pools"]
    except (OSError, ValueError, LookupError, TypeError):
        pass
    pools = []
    for provider in PROVIDERS:
        if fetch is not None:
            pools.extend(parse(provider, fetch(provider)))
        elif tool:
            pools.extend(_codexbar(tool, provider))
        else:
            pools.extend(parse(provider, "", "not installed"))
    tmp = "%s.%d.tmp" % (path, os.getpid())
    try:
        os.makedirs(cache_dir, exist_ok=True)
        with open(tmp, "w") as f:
            json.dump({"at": clock(), "tool": source, "pools": pools}, f)
        os.replace(tmp, path)
    except OSError:
        pass  # still use this reading if writing the cache fails
    return pools


def table(pools):
    """One row per pool for the coordinator's wake."""
    lines = []
    for pool in pools:
        if pool["state"] == "unknown":
            reason = pool["reason"]
            line = f"unknown ({'codexbar ' if reason == 'not installed' else ''}{reason})"
        else:
            line = ", ".join(f"{label} {pool[key]:g}% left" for key, label in (("five", "5h"), ("week", "week"))
                             if pool[key] is not None)
        lines.append(f"{pool['name']}: {line}")
    return "\n".join(lines)
