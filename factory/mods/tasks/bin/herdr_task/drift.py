"""After-the-fact comparisons; record facts for the coordinator, never merge."""
import re

from .base import git, now, PR_EVERY
from .github import (BAD, STOPPED, SYNC_GRACE_MS, ci_cap_hold, ci_stopped, gh, hit_limit, main_red_hold,
                     pr_behind, pr_repo, pr_update_branch, pr_view, synced_head, word)
from .stage import resolve_stage

WAIT_MS = 30 * 60000


def inspect(s, t, v):
    """Compare one freshly read PR with its task, retaining only current drift."""
    pr = t["pr"]
    head = v.get("headRefOid") or ""
    checks = v.get("statusCheckRollup") or []
    failures = [c for c in checks if word(c) in BAD - STOPPED]
    run_id = lambda c: (re.search(r"/actions/runs/(\d+)", c.get("detailsUrl") or "") or [None, None])[1]
    limited = {run_id(c) for c in checks if word(c) in STOPPED and hit_limit(t, c)[0]}
    failed_names = [c.get("name") or c.get("context") or "check" for c in failures]
    if pr.get("failed_checks") != failed_names:
        pr["failed_checks"] = failed_names
        s.dirty = True
    if v.get("state") == "MERGED" and not pr.get("drift_verified_merged"):
        pr["drift_verified_merged"] = True
        s.dirty = True
    drift = {}
    if t.get("state") == "done" and v.get("state") in ("OPEN", "CLOSED"):
        drift["done"] = "Task is done but its pull request is not merged"
    expected = t.get("head")
    if t.get("state") in ("ready", "done") and not t.get("push_held") and expected and head and expected != head and synced_head(t, pr_repo(t, s), head) is None:
        drift["head"] = "Stored head differs from the pull request head"
    if v.get("state") == "OPEN":
        accepted = t.get("accepted_at") or t.get("merge_queued") or t.get("merge_requested")
        if accepted and now() - accepted >= WAIT_MS:
            drift["unmerged"] = "Accepted pull request is still open after 30 minutes"
            if any(word(c) in STOPPED for c in checks):
                ci_stopped(t, v, pr_repo(t, s))
                s.dirty = True
            # Use the existing update-branch path, with the same sync, queue and runner rails.
            from .checks import sync_plan
            from .commands import uses_queue
            repo = pr_repo(t, s)
            synced = synced_head(t, repo, head) if expected and head != expected else head
            recent = now() - ((pr.get("synced") or {}).get("at") or 0) < SYNC_GRACE_MS
            if (repo and expected and head and synced and not recent and not main_red_hold(s, t.get("base") or "main")
                    and not uses_queue(s, t.get("base") or "main") and sync_plan(s, t)[0] == "full"
                    and not ci_cap_hold(s, t, repo) and pr_behind(t, repo, head)):
                pr_update_branch(t, repo, head)
                s.dirty = True
        reviewed = (t.get("state") == "ready" or t.get("accepted_at") or t.get("merge_queued")
                    or t.get("merge_requested") or (t.get("review_out") and expected == head))
        if failures and reviewed and resolve_stage(t, store=s).stage != "ci":
            runs = sorted({run_id(c) for c in failures if run_id(c) and run_id(c) not in limited})
            if pr.get("drift_rerun_head") != head and head and runs:
                for run in runs:
                    gh("run", "rerun", run, "--failed", "--repo", pr_repo(t, s))
                pr["drift_rerun_head"] = head
                s.dirty = True
            drift["red"] = "CI red on a reviewed head: flaky test or real?"
        hold = (main_red_hold(s, t.get("base") or "main") or (t.get("push_held") or {}).get("reason")
                or t.get("accept_wait"))
        green_hold = (t.get("state") == "ready" and not t.get("attention") and checks and all(word(c) in {"SUCCESS", "NEUTRAL", "SKIPPED"} for c in checks) and hold)
        if green_hold:
            previous = pr.get("drift_hold") or {}
            if previous.get("reason") != hold:
                previous = pr["drift_hold"] = {"reason": hold, "at": now()}
                s.dirty = True
            if now() - previous["at"] >= WAIT_MS:
                drift["hold"] = f"Ready and green but held: {hold}"
        else:
            if pr.pop("drift_hold", None):
                s.dirty = True
    if pr.get("drift") != drift:
        pr["drift"] = drift
        s.dirty = True


def drift_pass(s, t, v=None, at=None):
    """Refresh PR facts at the existing cadence, including tasks already marked done."""
    at = now() if at is None else at
    pr = t.get("pr")
    if not pr:
        return
    if v is None:
        if at - max(pr.get("drift_checked") or 0, pr.get("checked") or 0, t.get("done_at") or 0) < PR_EVERY:
            return
        pr["drift_checked"] = at
        s.dirty = True
        v = pr_view(t)
    if v:
        inspect(s, t, v)


def audit_done(s, at):
    """Bound old PR reads to one per folder per tick, oldest observation first."""
    tasks = [t for t in s.data["tasks"] if t.get("state") == "done" and t.get("pr") and not t["pr"].get("drift_verified_merged")]
    if tasks:
        tasks.sort(key=lambda t: (max(t["pr"].get("drift_checked") or 0, t["pr"].get("checked") or 0, t.get("done_at") or 0), t["id"]))
        drift_pass(s, tasks[0], at=at)


def base_drift(s):
    """A local base ahead of its remote is a claim mismatch, not a reason to push."""
    bases = {t.get("base") or "main" for t in s.data["tasks"] if t.get("pr")}
    found = []
    for base in sorted(bases):
        ok, out, _ = git(s.loc["folder"], "rev-list", "--count", f"refs/remotes/origin/{base}..refs/heads/{base}")
        if ok and out.isdigit() and int(out) > 0:
            found.append(base)
    if s.data.get("drift_bases") != found:
        s.data["drift_bases"] = found
        s.dirty = True
