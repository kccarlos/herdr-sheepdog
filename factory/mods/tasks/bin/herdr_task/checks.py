"""The merge gate's checks (docs/factory.md section 6): for a Ready task in a gate folder (review.min > 0)
with tasks.check set, herdr-task runs the check itself on the task's exact head, in a throwaway
detached worktree, and records the real result (contracts/gate-evidence.md, t["checks"])."""

import json
import shutil
import tempfile

from .base import git, hc_json, now, run
from .github import gh
from .settings import gate_min, settings_for

LIMIT_MS = 20 * 60000
TAIL = 4000
CI_WINDOW_MS = 10 * 60000


def ci_of(t, sha):
    """GitHub's state for sha on the task's pull request: {state, url}, or None without one."""
    pr = t.get("pr")
    if not pr:
        return None
    ok, out, _ = gh("api", f"repos/{pr['repo']}/commits/{sha}/check-runs?per_page=100")
    try:
        runs = json.loads(out)["check_runs"] if ok else None
    except (ValueError, KeyError):
        runs = None
    if runs is None:
        return {"state": "pending", "url": pr.get("url")}
    state = "none"
    if runs:
        done = [r.get("conclusion") for r in runs if r.get("status") == "completed"]
        state = "pending" if len(done) < len(runs) else \
            "failure" if any(c not in ("success", "neutral", "skipped") for c in done) else "success"
    return {"state": state, "url": pr.get("url")}


def clean(s, tmp):
    git(s.loc["folder"], "worktree", "remove", "--force", tmp)
    shutil.rmtree(tmp, ignore_errors=True)


def checks_pass(s, t):
    """One tick's step for one task: finish a check run (record it, or drop it when the task moved on or its
    head did), keep the CI state of a record fresh, or start a run at a head without a record."""
    wt, c = t.get("worktree"), t.get("check_run")
    on = t["state"] == "ready" and wt and gate_min(s, t) > 0
    ok, sha, _ = git(wt, "rev-parse", "HEAD") if on else (False, None, None)
    if c:
        k = hc_json("get", c["address"])
        if k is None and now() - c["started_at"] <= 2 * LIMIT_MS:
            return  # hc didn't answer: ask again next tick
        k = k or {}
        stale = not ok or sha != c["sha"] or not k or c.get("stale")  # a terminal hc no longer knows: dropped, checked again
        if k.get("state") == "live":
            if not stale and now() - c["started_at"] <= LIMIT_MS:
                return
            c["over"], c["stale"] = not stale, stale
            run(["hc", "kill", c["address"]])  # recorded or dropped once it has exited (hc rm needs that)
            s.dirty = True
            return
        if not stale:
            tail = run(["hc", "read", c["address"], "--lines", "200"]).stdout.rstrip()
            code = k.get("exit_code")
            t.setdefault("checks", []).append({
                "sha": c["sha"], "command": c["command"], "exit": 124 if c.get("over") else code if isinstance(code, int) else 1,
                "output_tail": (tail + (f"\nKilled: ran longer than {LIMIT_MS // 60000} minutes." if c.get("over") else ""))[-TAIL:],
                "started_at": c["started_at"], "finished_at": now(), "by": c["address"], "ci": ci_of(t, c["sha"])})
        clean(s, c["tmp"])
        run(["hc", "kill", c["address"]])  # a terminal hc no longer answers for may still run
        run(["hc", "rm", c["address"]])
        t.pop("check_run")
        s.dirty = True
        return
    cmd = (settings_for(s, t.get("space"))["tasks"].get("check") or "").strip()
    mine = [r for r in t.get("checks") or [] if r["sha"] == sha]
    last = mine[-1] if mine and t.get("pr") else None
    state = ((last or {}).get("ci") or {}).get("state")
    # pending (or unknown) until GitHub answers; "none" a while, as GitHub registers a push's runs late
    if last and (state in (None, "pending") or state == "none" and now() - last["finished_at"] < CI_WINDOW_MS):
        ci = ci_of(t, sha)
        if ci != mine[-1]["ci"]:
            mine[-1]["ci"], s.dirty = ci, True
    if not cmd or not ok or mine:
        return
    tmp = tempfile.mkdtemp(prefix="herdr-check-")
    if not git(wt, "worktree", "add", "--detach", tmp, sha)[0]:
        return clean(s, tmp)
    try:
        r = run(["hc", "run", "--bg", "--json", "--no-parent", "--no-notify", "--cwd", tmp,
                 "--label", "herdr.role=checks", "--label", f"herdr.checks={s.key(t)}", "--", "sh", "-c", cmd])
        address = json.loads(r.stdout)["address"] if r.returncode == 0 else None
    except (ValueError, KeyError):
        address = None
    except BaseException:
        clean(s, tmp)
        raise
    if not address:
        return clean(s, tmp)
    t["check_run"] = {"sha": sha, "command": cmd, "address": address, "tmp": tmp, "started_at": now()}
    s.dirty = True


def checks_line(t):
    """The task's view: where its checks stand, in a few words ("" when they don't apply)."""
    if t.get("check_run"):
        return "checks: running on " + t["check_run"]["sha"][:8]
    c = (t.get("checks") or [None])[-1]
    if not c:
        return ""
    ci = (c.get("ci") or {}).get("state")
    return f"checks: {'passed' if c['exit'] == 0 else 'failed (exit %d)' % c['exit']} on {c['sha'][:8]}" + (f", CI {ci}" if ci else "")
