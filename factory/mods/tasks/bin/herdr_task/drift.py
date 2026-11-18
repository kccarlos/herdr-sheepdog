"""After-the-fact comparisons; record facts for the coordinator, never merge."""
import re

from .base import git, now, PR_EVERY
from .github import (BAD, STOPPED, SYNC_GRACE_MS, ci_cap_hold, ci_stopped, gh, hit_limit, main_red_hold,
                     pr_behind, pr_pending, pr_repo, pr_update_branch, pr_view, synced_head, word)
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
    repo = pr_repo(t)
    mismatched = (t.get("state") in ("ready", "done") and not t.get("push_held")
                  and expected and head and expected != head)
    synced = synced_head(t, repo, head) if mismatched else None
    if mismatched and synced is None:
        drift["head"] = "Stored head differs from the pull request head"
    if v.get("state") == "OPEN":
        ready = t.get("state") == "ready" and not t.get("push_held")
        accepted = ready and (t.get("accepted_by") or t.get("merge_queued") or t.get("merge_requested"))
        accepted_since = max(t.get("merge_queued") or 0, t.get("merge_requested") or 0,
                             (t.get("accepted_at") or t.get("ready_at") or 0) if t.get("accepted_by") else 0)
        approved_head = bool(expected and head and (expected == head or synced))
        if accepted and accepted_since and now() - accepted_since >= WAIT_MS:
            drift["unmerged"] = "Accepted pull request is still open after 30 minutes"
            if repo and approved_head and any(word(c) in STOPPED for c in checks):
                ci_stopped(t, v, repo)
                s.dirty = True
            if repo and approved_head:
                update_held_branch(s, t, v, repo, head)
        from .replan import active_reviews
        reviewed = ready and head and any(
            r.get("head") == head and (r.get("verdict") or r.get("result")) == "pass"
            for r in active_reviews(t))
        if failures and (accepted and approved_head or reviewed) and resolve_stage(t, store=s).stage != "ci":
            stopped_retry = pr.get("ci_rerun") or {}
            unavailable = limited | (set(stopped_retry.get("runs") or ()) if stopped_retry.get("head") == head else set())
            runs = sorted({run_id(c) for c in failures if run_id(c)} - unavailable)
            if repo and runs and not pr_pending(v):
                retry = pr.get("drift_rerun") or {}
                if retry.get("head") != head:
                    retry = {"head": head, "runs": []}
                for run in runs:
                    if run not in retry["runs"]:
                        gh("run", "rerun", run, "--failed", "--repo", repo)
                        retry["runs"].append(run)
                        pr["drift_rerun"] = retry
                        s.dirty = True
            drift["red"] = "CI red on a reviewed head: flaky test or real?"
            if pr.get("drift_red_head") != head:
                pr["drift_red_head"] = head
                s.dirty = True
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


def update_held_branch(s, t, v, repo, head):
    """Use the existing remote update path for an accepted PR that has stalled."""
    from .checks import sync_plan
    from .commands import uses_queue

    pr = t["pr"]
    base = t.get("base") or "main"
    attempt = pr.get("drift_update") or {}
    if (pr_pending(v) or v.get("mergeable") == "CONFLICTING" or main_red_hold(s, base) or uses_queue(s, base)
            or now() - ((pr.get("synced") or {}).get("at") or 0) < SYNC_GRACE_MS
            or (attempt.get("head") == head and now() - attempt["at"] < WAIT_MS)
            or sync_plan(s, t)[0] == "off" or ci_cap_hold(s, t, repo)):
        return
    if pr_behind(t, repo, head):
        pr_update_branch(t, repo, head)
        pr["drift_update"] = {"head": head, "at": now()}
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
