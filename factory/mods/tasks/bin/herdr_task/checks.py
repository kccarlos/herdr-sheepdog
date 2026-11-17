"""The merge gate's checks (docs/factory.md section 6): for a Ready task in a gate folder (review.min > 0)
with tasks.check set, herdr-task runs the check itself on the task's exact head, in a throwaway
detached worktree, and records the real result (contracts/gate-evidence.md, t["checks"])."""

import json
import subprocess
import shutil
import tempfile

from .base import git, hc_json, now, PROJECT_LABEL, ROLE_LABEL, run
from .github import gh, pr_repo
from .settings import gate_min, settings_for

LIMIT_MS = 20 * 60000
TAIL = 4000
CI_WINDOW_MS = 10 * 60000


def ci_of(t, sha):
    """GitHub's state for sha on the task's pull request: {state, url}, or None without one."""
    pr = t.get("pr")
    repo = pr_repo(t) if pr else None
    if not pr or not repo:
        return None
    ok, out, _ = gh("api", f"repos/{repo}/commits/{sha}/check-runs?per_page=100")
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
            if t.get("pr"):
                pr_repo(t, s)
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
    cmd = (settings_for(s)["tasks"].get("check") or "").strip()
    mine = [r for r in t.get("checks") or [] if r["sha"] == sha]
    last = mine[-1] if mine and t.get("pr") else None
    state = ((last or {}).get("ci") or {}).get("state")
    # pending (or unknown) until GitHub answers; "none" a while, as GitHub registers a push's runs late
    if last and (state in (None, "pending") or state == "none" and now() - last["finished_at"] < CI_WINDOW_MS):
        pr_repo(t, s)
        ci = ci_of(t, sha)
        if ci != mine[-1]["ci"]:
            mine[-1]["ci"], s.dirty = ci, True
    if not cmd or not ok or mine:
        return
    tmp = tempfile.mkdtemp(prefix="herdr-check-")
    if not git(wt, "worktree", "add", "--detach", tmp, sha)[0]:
        return clean(s, tmp)
    key = s.key(t)
    slug = getattr(s, "slug", None) or key.partition("#")[0]
    try:
        r = run(["hc", "run", "--bg", "--json", "--no-parent", "--no-notify", "--cwd", tmp,
                 "--label", f"{PROJECT_LABEL}={slug}", "--label", f"{ROLE_LABEL}=checks",
                 "--label", "herdr.role=checks", "--label", f"herdr.checks={key}", "--", "sh", "-c", cmd])
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


# FAC-111 (WUKO-130 B1): tasks.sync says what a green pull request behind its base needs before Herdr merges it.
# full: GitHub's update-branch and a whole new CI run on the new head (FAC-98). cheap: the folder's tasks.check run
# here on its head merged with the base's tip, then merged; the run on the base after the merge is the integration
# test. off: merged as it is. cheap without a tasks.check is full: never quietly off.
SYNC_MODES = ("full", "cheap", "off")
SYNC_LIMIT_S = 2 * 60  # a cheap check runs in seconds (inside the tick); one this slow falls back to full, never merges unchecked
SYNC_FETCH_S = 60
SYNC_FAILED = "Its check failed on it merged with its base branch (the base moved on since)"


def sync_mode(s, t):
    k = settings_for(s)["tasks"]
    mode = k.get("sync") if k.get("sync") in SYNC_MODES else "full"
    return "full" if mode == "cheap" and not (k.get("check") or "").strip() else mode


def sync_check(s, t, at, repo=None):
    """cheap's local check: `at` (the pull request's head) merged with its base's tip on the remote, in a throwaway
    detached worktree, then tasks.check there. Returns ("pass", None), ("conflict", files), ("fail", why) or
    ("error", why) when it couldn't check (fall back to full). Leaves the task's worktree, branch and folder alone."""
    from .github import remote_of, remote_repo
    main, base = s.loc["folder"], t.get("base") or "main"
    cmd = (settings_for(s)["tasks"].get("check") or "").strip()
    remote = remote_of(main)
    if repo and remote_repo(main, remote) != repo:  # a fork's remote: its base isn't the pull request's
        return "error", f"{remote} isn't {repo}"
    try:
        ok, _, err = git(main, "fetch", "--quiet", remote, f"+refs/heads/{base}:refs/remotes/{remote}/{base}", timeout=SYNC_FETCH_S)
    except subprocess.TimeoutExpired:
        ok, err = False, "timed out"
    ok2, tip, _ = git(main, "rev-parse", "--verify", "--quiet", f"refs/remotes/{remote}/{base}^{{commit}}") if ok else (False, "", "")
    if not (ok2 and tip):
        return "error", f"couldn't fetch {base}: {err}"
    if not git(main, "cat-file", "-e", f"{at}^{{commit}}")[0]:
        return "error", f"{at[:8]} isn't here"
    if git(main, "merge-base", "--is-ancestor", tip, at)[0]:  # GitHub says behind: a stale or other base, never unchecked
        return "error", f"{base} here at {tip[:8]} isn't ahead of {at[:8]}"
    tmp = tempfile.mkdtemp(prefix="herdr-sync-")
    try:
        if not git(main, "worktree", "add", "--detach", tmp, at)[0]:
            return "error", "couldn't make a worktree"
        # committed (detached, thrown away): a check reading git status sees a clean merge result
        ok, out, err = git(tmp, "-c", "user.name=Herdr", "-c", "user.email=herdr@localhost", "-c", "commit.gpgsign=false",
                           "merge", "--no-ff", "--no-edit", "--no-verify", tip)
        if not ok:
            files = git(tmp, "diff", "--name-only", "--diff-filter=U")[1].split()
            return ("conflict", ", ".join(files[:5]) + (" and more" if len(files) > 5 else "")) if files else ("error", err or out)
        try:
            r = run(["sh", "-c", cmd], cwd=tmp, timeout=SYNC_LIMIT_S)
        except subprocess.TimeoutExpired:
            return "error", f"the check ran longer than {SYNC_LIMIT_S // 60} minutes"
        if r.returncode in (126, 127):  # its program isn't on Herdr's PATH: not the code's fault
            return "error", f"`{cmd}` couldn't run here (exit {r.returncode})"
        if r.returncode:
            tail = (r.stdout + r.stderr).strip()[-600:]
            return "fail", f"`{cmd}` exited {r.returncode} on {at[:8]} merged with {base} at {tip[:8]}" + (f":\n{tail}" if tail else "")
        return "pass", None
    finally:
        clean(s, tmp)
