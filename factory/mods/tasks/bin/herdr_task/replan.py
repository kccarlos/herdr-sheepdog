"""What a re-plan (`recap --decide new`, or `plan amend` with a retro reason) resets, keeps,
and archives on a task: one table (`REPLAN_TABLE`) shared with `docs/dev/factory/replan.md`."""

from dataclasses import dataclass
import json
import os
import re

from . import base as _base
from .base import Fail, label_of, now

_DEFAULT_HC_JSON = _base.hc_json


DISPOSITIONS = ("reset", "kept", "archived")
RETRO_REASON = re.compile(r"\bretro(?:spect\w*)?\b", re.I)
NON_TASK_PREFIXES = frozenset({"UTF", "SHA", "AES", "ISO", "RFC", "PEP", "CVE", "TLS", "HTTP", "IPV", "RSA", "CRC"})
GONE_WORDS = ("not found", "not_found", "unknown terminal", "no terminal", "no such terminal", "does not exist")


@dataclass(frozen=True)
class ReplanRow:
    key: str
    state: str
    disposition: str
    fields: tuple
    why: str
    apply: object


def _last_new_recap_at(task):
    return max(
        (int(d.get("at") or 0) for d in (task.get("recaps") or []) if isinstance(d, dict) and d.get("decision") == "new"),
        default=0,
    )


def _is_before_replan(rec, cutoff):
    if rec.get("before_replan"):
        return True
    at = rec.get("at")
    return bool(cutoff and isinstance(at, (int, float)) and at <= cutoff)


def archive_legacy_before_replan(task):
    """Marks any pre-existing records at/before a legacy `decision='new'` recap with `before_replan=True`."""
    cutoff = _last_new_recap_at(task)
    if not cutoff:
        return False
    changed = False
    for key in ("reviews", "review_runs", "review_failures"):
        for rec in task.get(key) or []:
            if isinstance(rec, dict) and not rec.get("before_replan") and _is_before_replan(rec, cutoff):
                rec["before_replan"] = True
                changed = True
    return changed


def active_reviews(task):
    """Review rounds of the current approach (`before_replan` and rounds at/before any `new` recap excluded)."""
    cutoff = _last_new_recap_at(task)
    return [r for r in task.get("reviews") or [] if not _is_before_replan(r, cutoff)]


def active_review_runs(task):
    """Gate review runs of the current approach (`before_replan` and runs at/before any `new` recap excluded)."""
    cutoff = _last_new_recap_at(task)
    return [r for r in task.get("review_runs") or [] if not _is_before_replan(r, cutoff)]


def active_review_failures(task):
    """Failed reviewer attempts of the current approach (`before_replan` and failures at/before any `new` recap excluded)."""
    cutoff = _last_new_recap_at(task)
    return [f for f in task.get("review_failures") or [] if not _is_before_replan(f, cutoff)]


# ---- who may trigger a re-plan ------------------------------------------------------------------

def _read_caller_terminal(me):
    """Reads `hc get <me> --json`, failing closed whenever HC_TERMINAL_ID is set and hc cannot verify it."""
    try:
        r = _base.run(["hc", "get", me, "--json"])
    except (OSError, Fail):
        r = None
    if r is None or r.returncode != 0:
        raise Fail("Couldn't verify the caller terminal; run this from your Assistant.")
    try:
        k = json.loads(r.stdout)
    except ValueError:
        k = None
    if not isinstance(k, dict) or k.get("error"):
        raise Fail("Couldn't verify the caller terminal; run this from your Assistant.")
    return k


def classify_replan_caller(s, task=None):
    """Returns `'coordinator'` or `'owner'`, or raises `Fail` for a worker, reviewer, helper, or unverified caller."""
    from . import launch as _launch

    for env_key in ("SHEEPDOG_TASK", "HERDR_BROWSER_TASK"):
        if (os.environ.get(env_key) or "").strip():
            raise Fail("A task's own agent can't re-plan a task or decide its recap: your Assistant does.")

    me = (os.environ.get("HC_TERMINAL_ID") or "").strip() or None
    patched = getattr(_launch.caller_labels, "_is_default_caller_labels", False) is False
    k = None
    if patched:
        labels = _launch.caller_labels() or {}
    elif me:
        if _launch.hc_json is not _DEFAULT_HC_JSON:
            try:
                k = _launch.hc_json("get", me)
            except (OSError, Fail):
                k = None
            if not isinstance(k, dict) or k.get("error"):
                raise Fail("Couldn't verify the caller terminal; run this from your Assistant.")
        else:
            k = _read_caller_terminal(me)
        labels = k.get("labels") or {}
    else:
        labels = {}

    role = label_of(labels, "role")
    task_label = label_of(labels, "task")
    if task is not None and task_label == s.key(task):
        raise Fail("A task's own agent can't re-plan a task or decide its recap: your Assistant does.")
    if task_label or role in ("worker", "reviewer", "helper", "checks", "ask"):
        raise Fail("A task worker, reviewer or helper can't re-plan a task or decide its recap: your Assistant does.")

    if not patched:
        tasks = (getattr(s, "data", None) or {}).get("tasks") or []
        task_addrs = {
            a.get("address")
            for o in tasks
            for a in (o.get("agents") or []) + (o.get("reviewers") or [])
            if isinstance(a, dict) and a.get("address")
        }
        bare_me = me.split("/")[-1] if me else None
        parent = (k or {}).get("parent")
        if (bare_me and (me in task_addrs or bare_me in task_addrs)) or (parent and parent in task_addrs):
            raise Fail("A task's own agent can't re-plan a task or decide its recap: your Assistant does.")

    lease = (getattr(s, "data", None) or {}).get("coordinator")
    lease_addr = lease.get("address") if isinstance(lease, dict) else None
    if role == "coordinator" or (k and _launch.is_coordinator(k)):
        if lease_addr:
            if me != lease_addr:
                raise Fail("Only the project's lease-holding coordinator or the owner may re-plan a task.")
            if lease.get("created_ms") and (k or {}).get("created_ms") and lease["created_ms"] != k["created_ms"]:
                raise Fail("Only the project's lease-holding coordinator or the owner may re-plan a task.")
        return "coordinator"
    if me and lease_addr and me == lease_addr:
        if not (lease.get("created_ms") and (k or {}).get("created_ms") and lease["created_ms"] != k["created_ms"]):
            return "coordinator"
    return "owner"


def require_replan_caller(s, task=None, *, action="re-plan a task"):
    try:
        return classify_replan_caller(s, task)
    except Fail as e:
        msg = str(e)
        if "can't re-plan a task or decide its recap" in msg:
            raise Fail(f"A task's own agent can't {action}: your Assistant does.")
        raise


# ---- external side effects (never run on plan amend --check) ------------------------------------

def cancel_queued_merge(s, task, *, was_merge_queued=None):
    """Dequeues an open PR from GitHub's merge queue and disables auto-merge before resetting local merge state."""
    from .github import gh, github, pr_repo, queue_entry

    pr = task.get("pr") if isinstance(task.get("pr"), dict) else None
    repo = pr_repo(task, s, github) if pr and pr.get("number") else None
    ref = s.ref(task) if s is not None and hasattr(s, "ref") else f"#{task.get('id')}"
    had_queued = bool(task.get("merge_queued")) if was_merge_queued is None else bool(was_merge_queued)
    if not repo:
        if had_queued:
            raise Fail(f"{ref} is marked as in GitHub's merge queue, and its pull request couldn't be reached to dequeue it.")
        return
    num = str(pr["number"])
    q = queue_entry(task)
    if q is None:
        raise Fail(f"Couldn't check pull request #{num} on GitHub before re-planning {ref}; try again once GitHub answers.")
    if q.get("state") == "MERGED":
        raise Fail(f"{ref}'s pull request #{num} is already merged on GitHub.")
    if q.get("in"):
        if q.get("id"):
            ok, _, err = gh(
                "api", "graphql",
                "-f", "query=mutation($id: ID!) { dequeuePullRequest(input: {id: $id}) { mergeQueueEntry { id } } }",
                "-f", f"id={q['id']}",
            )
        else:
            ok, _, err = gh("pr", "merge", num, "--repo", repo, "--disable-auto")
        if not ok:
            q2 = queue_entry(task)
            if not q2 or q2.get("state") == "MERGED" or q2.get("in"):
                why = "already merged on GitHub" if (q2 or {}).get("state") == "MERGED" else (err or "dequeue failed")
                raise Fail(f"Couldn't remove pull request #{num} from GitHub's merge queue ({why}).")
            q = q2
    if q.get("auto"):
        ok, _, err = gh("pr", "merge", num, "--repo", repo, "--disable-auto")
        if not ok:
            q2 = queue_entry(task)
            if not q2 or q2.get("state") == "MERGED" or q2.get("auto") or q2.get("in"):
                why = "already merged on GitHub" if (q2 or {}).get("state") == "MERGED" else (err or "disable-auto failed")
                raise Fail(f"Couldn't disable auto-merge on pull request #{num} ({why}).")


CONFIRMED_GONE_STATES = frozenset({"finished", "exited", "dead", "gone"})
TRANSIENT_HC_WORDS = ("socket", "daemon", "connect", "refused", "unavailable", "timeout", "timed out", "busy")


def _terminal_still_live(addr, hc_get, runner):
    """True unless `hc get` confirms `addr` exited or specifically reports the terminal as absent."""
    if hc_get is not _DEFAULT_HC_JSON:
        try:
            k = hc_get("get", addr)
        except (OSError, Fail):
            return True
        if not isinstance(k, dict) or k.get("error"):
            return True
        state = k.get("state")
        if state in CONFIRMED_GONE_STATES:
            return False
        return True
    try:
        r = runner(["hc", "get", addr, "--json"])
    except (OSError, Fail):
        return True
    if r.returncode == 0:
        try:
            k = json.loads(r.stdout)
        except ValueError:
            return True
        if not isinstance(k, dict) or k.get("error"):
            return True
        if k.get("state") in CONFIRMED_GONE_STATES:
            return False
        return True
    err = f"{r.stdout or ''} {r.stderr or ''}".lower()
    if any(w in err for w in TRANSIENT_HC_WORDS):
        return True
    addr_low = str(addr).lower()
    bare_low = addr_low.split("/")[-1]
    if any(w in err for w in ("unknown terminal", "no terminal", "no such terminal")):
        return False
    if ("terminal" in err or (addr_low and addr_low in err) or (bare_low and bare_low in err)) and any(
        w in err for w in ("not found", "not_found", "does not exist")
    ):
        return False
    return True


def retiring_live(s, task):
    """Drops exited pre-replan reviewers, retries `end_reviewer` for live ones, and holds new gate rounds while any lives."""
    from . import commands as _commands

    addrs = list(task.get("retiring_reviewers") or [])
    if not addrs:
        return False
    still = []
    for addr in addrs:
        if _terminal_still_live(addr, _commands.hc_json, _commands.run):
            still.append(addr)
            try:
                _commands.end_reviewer(addr)
            except Exception:
                pass
    if still != addrs:
        if still:
            task["retiring_reviewers"] = still
        else:
            task.pop("retiring_reviewers", None)
        if s is not None:
            s.dirty = True
    return bool(still)


def collect_live_reviewers(s, task, *, dry=False):
    """Collects live gate/legacy reviewers and live worker-started `role=reviewer` subagents, never `role=helper`."""
    from . import agents as _agents, commands as _commands

    legacy_rv = (task.get("review") or {}).get("reviewer") if isinstance(task.get("review"), dict) else None
    addrs = list(dict.fromkeys([
        *(task.get("retiring_reviewers") or []),
        *([legacy_rv] if legacy_rv else ()),
        *(rn["address"] for rn in _commands.open_runs(task) if rn.get("address")),
    ]))
    if not dry and s is not None and hasattr(s, "key"):
        known_reviewers = {a.get("address") for a in (task.get("reviewers") or []) if isinstance(a, dict) and a.get("address")}
        for k, _ in _agents._live(s.key(task), strict=True):
            addr = k.get("address")
            if not addr or addr in addrs:
                continue
            if addr in known_reviewers:
                addrs.append(addr)
                continue
            role = label_of(k.get("labels"), "role")
            if not role:
                det = _agents.hc_json("get", addr)
                if not isinstance(det, dict) or det.get("error") or ("state" not in det and "labels" not in det):
                    raise Fail(f"Couldn't verify terminal {addr}; try again when herdr-core is reachable.")
                if det.get("state") in CONFIRMED_GONE_STATES:
                    continue
                role = label_of(det.get("labels"), "role")
            if role == "reviewer":
                addrs.append(addr)
    return addrs


def retire_reviewers(addrs):
    from . import commands as _commands

    for addr in dict.fromkeys(addrs or ()):
        try:
            _commands.end_reviewer(addr)
        except Exception:
            pass


def _is_stale_worker_held_item(item, ref=None):
    """True only for held messages belonging to the pre-replan review, recap, Ready, merge, or PR state."""
    text = str((item or {}).get("text") or "").strip()
    if not text:
        return False
    if text.startswith("Herdr's review round ") or re.match(r"^Review round \d+ of \d+ for ", text):
        return True
    if text.startswith(("Herdr added one review round to ", "Your Assistant added one review round to ")):
        return True
    if text.startswith("Your Assistant read the recap of "):
        return True
    if text.startswith("<herdr-github "):
        return True
    if ref:
        if text.startswith((f"Stop {ref}: ", f'Merge task {ref} "', f'Task {ref} "')):
            return True
        tag = f'<herdr-news task="{ref}"'
        if text.startswith(tag) and 'event="answer"' not in text:
            return True
    elif text.startswith("Stop ") and ("recap first" in text or "two rounds in a row with new majors" in text):
        return True
    return False


def _strip_stale_task_news_lines(text, ref):
    tag = f'<herdr-news task="{ref}"'
    if tag not in text:
        return text
    kept = [
        ln for ln in text.splitlines()
        if not (ln.startswith(tag) and 'event="answer"' not in ln)
    ]
    return "\n".join(kept).strip()


def clear_task_held_news(s, task, retiring=(), *, at=None):
    """Clears disk `.held.json` and `.news.json` entries superseded by a re-plan of `task`."""
    from . import news as _news

    ref = s.ref(task) if s is not None and hasattr(s, "ref") else None
    key = f"{s.slug}#{task['id']}" if s is not None and getattr(s, "slug", None) else None
    worker_addrs = {a["address"] for a in task.get("agents") or [] if isinstance(a, dict) and a.get("address")}
    retire_set = set(retiring or ())
    reset_ts = int(at if at is not None else (task.get("review_reset_at") or now()))

    _news.record_mem_reset(key, reset_ts)
    news_dir = os.path.dirname(_news.NEWS)
    lock_cm = _news.news_lock(exclusive=True) if (news_dir and os.path.isdir(news_dir)) else _news.null_lock()
    with lock_cm:
        if key and news_dir and os.path.isdir(news_dir):
            with _news.news_state_file() as state:
                resets = state.setdefault("reset_at", {})
                resets[key] = max(int(resets.get(key) or 0), reset_ts)
                outbox = state.get("outbox")
                if isinstance(outbox, list):
                    outbox[:] = [x for x in outbox if not (x.get("key") == key and x.get("event") not in ("backup",))]
                delivered = state.get("delivered")
                if isinstance(delivered, dict):
                    delivered.pop(key, None)
                mine = (state.get("seen") or {}).get(s.slug)
                if isinstance(mine, dict) and isinstance(mine.get(str(task["id"])), dict):
                    prev = mine[str(task["id"])]
                    for k in ("held_ready_at", "recap", "reviewer_failed"):
                        prev.pop(k, None)

        if os.path.exists(_news.HELD):
            with _news.held_messages() as held:
                for addr in list(held):
                    if addr in retire_set:
                        held.pop(addr, None)
                        continue
                    items = held.get(addr) or []
                    if addr in worker_addrs:
                        cleaned_w = []
                        for x in items:
                            txt = x.get("text")
                            if isinstance(txt, str) and ref and f'<herdr-news task="{ref}"' in txt:
                                rem = _strip_stale_task_news_lines(txt, ref)
                                if not rem:
                                    continue
                                if rem != txt:
                                    x = {**x, "text": rem}
                            if not _is_stale_worker_held_item(x, ref):
                                cleaned_w.append(x)
                        items = cleaned_w
                    elif ref:
                        cleaned = []
                        for x in items:
                            if x.get("how") == "news" and isinstance(x.get("text"), str):
                                rem = _strip_stale_task_news_lines(x["text"], ref)
                                if not rem:
                                    continue
                                if rem != x["text"]:
                                    x = {**x, "text": rem}
                            cleaned.append(x)
                        items = cleaned
                    if items:
                        held[addr] = items
                    else:
                        held.pop(addr, None)


# ---- table row handlers -------------------------------------------------------------------------

def _apply_live_reviewers(ctx):
    t = ctx["task"]
    retiring = collect_live_reviewers(ctx["store"], t, dry=ctx["dry"])
    if retiring:
        t["retiring_reviewers"] = retiring
    else:
        t.pop("retiring_reviewers", None)
    ctx["retiring"] = retiring


def _apply_helper_terminals(ctx):
    # Worker records (`agents`), non-reviewer helpers (`role=helper`), and launch lineage (`reviewers`) stay intact.
    return None


def _apply_review_rounds(ctx):
    from .agents import REVIEWER_FAILED

    t = ctx["task"]
    for key in ("reviews", "review_runs", "review_failures"):
        for rec in t.get(key) or []:
            rec["before_replan"] = True
    for key in ("review", "review_out", "review_wait", "review_quota_wait", "merge_only_runs"):
        t.pop(key, None)
    if str(t.get("attention") or "").startswith(REVIEWER_FAILED):
        t.pop("attention", None)
        t.pop("attention_at", None)
    t["review_reset_at"] = ctx["at"]


def _apply_fable_runs(ctx):
    # Fable's rounds live in `review_runs` / `reviews` and are marked `before_replan: True` above;
    # `fable_runs` and `fable_said` read `active_review_runs` / `active_reviews`, resetting Fable's cap and verdict count.
    for key in ("reviews", "review_runs"):
        for rec in ctx["task"].get(key) or []:
            rec["before_replan"] = True


def _apply_review_more(ctx):
    t = ctx["task"]
    for key in ("review_more_granted", "review_rounds_added", "review_auto_grants"):
        t.pop(key, None)


def _apply_consecutive_majors(ctx):
    # Derived from `rounds_since(t, all_rounds=True)`, which excludes `before_replan` records and any rounds
    # at or before the `"new"` recap entry recorded on `recaps`.
    return None


def _apply_open_recap(ctx):
    t = ctx["task"]
    rc = t.pop("recap", None)
    ctx["had_reasons"] = list((rc or {}).get("reasons") or [])
    if str(t.get("attention") or "").startswith("Recap required"):
        t.pop("attention", None)
        t.pop("attention_at", None)


def _apply_recaps(ctx):
    from .budget import change_lines, token_sum

    s, t, at = ctx["store"], ctx["task"], ctx["at"]
    why = " ".join(str(ctx["why"] or "re-plan").split())[:300]
    cur = None if ctx["dry"] else change_lines(t)
    tokens = 0 if (ctx["dry"] or s is None) else token_sum(s, t)
    entry = {
        "decision": "new",
        "why": why,
        "at": at,
        "reasons": ctx.get("had_reasons") or [],
        "tokens": tokens,
        **({"lines": cur} if cur is not None else {}),
    }
    t.setdefault("recaps", []).append(entry)


def _apply_verdict_told(ctx):
    ctx["task"].pop("verdict_told", None)


def _apply_merge_queued(ctx):
    from .tick import QUEUE_REMOVED

    t = ctx["task"]
    for key in ("merge_queued", "requeued"):
        t.pop(key, None)
    if str(t.get("attention") or "").startswith(QUEUE_REMOVED):
        t.pop("attention", None)
        t.pop("attention_at", None)


def _apply_back_auto(ctx):
    from .commands import back_to_work

    s, t = ctx["store"], ctx["task"]
    if s is not None and (t.get("state") == "ready" or (t.get("back") or {}).get("auto")):
        back_to_work(s, t, "re-plan reset its review budget")
    else:
        if t.get("state") == "ready":
            t["state"] = "running"
        t.pop("back", None)
    t.pop("settled_at", None)


def _apply_ready_and_auto_accept(ctx):
    t = ctx["task"]
    for key in (
        "auto_merge", "merge_requested", "ready_at", "ready_head", "ready_turns", "stray_since",
        "accepted_by", "accepted_at", "auto_accepted", "accept_wait", "owner_approved",
        "nothing_to_merge", "head_moved_at", "push_held",
    ):
        t.pop(key, None)
    pr = t.get("pr")
    if isinstance(pr, dict):
        for key in ("wait", "no_ci", "drift_hold"):
            pr.pop(key, None)


def _apply_held_news(ctx):
    from .commands import REVIEW_RESUME_WAIT

    t = ctx["task"]
    t.pop("review_followup", None)
    if str(t.get("attention") or "").startswith(REVIEW_RESUME_WAIT):
        t.pop("attention", None)
        t.pop("attention_at", None)
    pr = t.get("pr")
    if isinstance(pr, dict):
        pr.pop("news", None)


def _apply_task_context(ctx):
    # Brief, amendments, worktree/branch, PR identity (`number`, `url`, `repo`), asks, simulator, device_wait,
    # token caches, and `ci_runs` (`ci_since` counts past `recaps[-1]["at"]`) remain intact.
    return None


# Single source of truth for re-plan state transitions (mirrored 1-to-1 in docs/dev/factory/replan.md).
REPLAN_TABLE = (
    ReplanRow(
        key="live_reviewers",
        state="Live reviewers",
        disposition="reset",
        fields=("retiring_reviewers",),
        why="Live gate reviewers and worker-started reviewer subagents judge the pre-replan commit; they are retired and hold replacement gate rounds until exited.",
        apply=_apply_live_reviewers,
    ),
    ReplanRow(
        key="helper_terminals",
        state="Helper terminals and worker",
        disposition="kept",
        fields=("agents", "reviewers"),
        why="The task's worker, non-reviewer helper terminals (role=helper), and terminal launch lineage stay so the worker continues in the same worktree.",
        apply=_apply_helper_terminals,
    ),
    ReplanRow(
        key="review_rounds",
        state="Review rounds and their records",
        disposition="archived",
        fields=("reviews", "review_runs", "review_failures", "review_reset_at", "review", "review_out", "review_wait", "review_quota_wait", "merge_only_runs"),
        why="Pre-replan rounds, runs, and failures stay in history marked before_replan=True while active counters and transient round waits reset to round 1.",
        apply=_apply_review_rounds,
    ),
    ReplanRow(
        key="fable_runs",
        state="Fable runs",
        disposition="archived",
        fields=("review_runs", "reviews"),
        why="Pre-replan Fable rounds stay in history marked before_replan=True so Fable's change-request cap, gate_need, and accept_bar start fresh.",
        apply=_apply_fable_runs,
    ),
    ReplanRow(
        key="review_more",
        state="The review-more grant",
        disposition="reset",
        fields=("review_more_granted", "review_rounds_added", "review_auto_grants"),
        why="The new approach gets a fresh base round ceiling with review-more and auto-grants available again if needed.",
        apply=_apply_review_more,
    ),
    ReplanRow(
        key="consecutive_majors",
        state="The consecutive-majors count",
        disposition="reset",
        fields=("reviews", "recaps"),
        why="Non-convergence counts only non-Fable rounds of the current approach after the re-plan (whereas continue preserves the streak).",
        apply=_apply_consecutive_majors,
    ),
    ReplanRow(
        key="open_recap",
        state="Open recap",
        disposition="reset",
        fields=("recap", "attention", "attention_at"),
        why="A re-plan resolves the open recap and clears its Recap required attention so reviewed and ready are no longer blocked.",
        apply=_apply_open_recap,
    ),
    ReplanRow(
        key="recaps",
        state="Decided recaps log",
        disposition="kept",
        fields=("recaps",),
        why="Past recap decisions stay on the task and a new decision='new' entry becomes recaps[-1], advancing CI/time/token baselines and superseding continue --small bounds.",
        apply=_apply_recaps,
    ),
    ReplanRow(
        key="verdict_told",
        state="verdict_told",
        disposition="reset",
        fields=("verdict_told",),
        why="verdict_told records delivered round numbers (1, 2, ...); clearing it ensures the new round 1 verdict is delivered to the worker.",
        apply=_apply_verdict_told,
    ),
    ReplanRow(
        key="merge_queued",
        state="merge_queued and a PR queued on GitHub",
        disposition="reset",
        fields=("merge_queued", "requeued"),
        why="An open PR in GitHub's merge queue or with auto-merge enabled is dequeued and has auto-merge disabled before clearing merge_queued, refusing if GitHub cannot dequeue it.",
        apply=_apply_merge_queued,
    ),
    ReplanRow(
        key="back_auto",
        state="back.auto and settle_back",
        disposition="reset",
        fields=("back", "settled_at", "state"),
        why="Clearing back.auto via non-auto back_to_work stops tick.settle_back() from returning a re-planned task to Ready.",
        apply=_apply_back_auto,
    ),
    ReplanRow(
        key="ready_and_auto_accept",
        state="Ready and auto-accept state and passing rounds",
        disposition="reset",
        fields=("state", "ready_at", "ready_head", "ready_turns", "stray_since", "auto_merge", "merge_requested", "accepted_by", "accepted_at", "auto_accepted", "accept_wait", "owner_approved", "nothing_to_merge", "head_moved_at", "push_held"),
        why="Ready/approval/auto-accept markers and transient PR waits clear and archived pre-replan passes no longer satisfy the gate or accept_bar.",
        apply=_apply_ready_and_auto_accept,
    ),
    ReplanRow(
        key="held_news",
        state="Held news",
        disposition="reset",
        fields=("review_followup", "pr.news", ".held.json", ".news.json"),
        why="Stale pre-replan review followups, PR news, retiring-reviewer prompts, worker review/recap/Ready/merge messages, and queued task status lines are dropped while preserving unrelated held messages such as device-ready notices, ask answers, and brief amendments.",
        apply=_apply_held_news,
    ),
    ReplanRow(
        key="task_context",
        state="Task brief, worktree, PR identity, asks, and CI log",
        disposition="kept",
        fields=("title", "prompt", "amendments", "worktree", "branch", "base", "pr", "asks", "simulator", "device_wait", "tokens", "ci_runs"),
        why="The task keeps its brief, amendments, worktree, branch, open PR record, open asks, leased resources, token cache, and CI log.",
        apply=_apply_task_context,
    ),
)

REPLAN_BY_KEY = {row.key: row for row in REPLAN_TABLE}


@dataclass
class ReplanEffect:
    store: object
    task: dict
    retiring: list
    at: int
    was_merge_queued: bool = False

    def commit(self):
        cancel_queued_merge(self.store, self.task, was_merge_queued=self.was_merge_queued)
        retire_reviewers(self.retiring)
        clear_task_held_news(self.store, self.task, self.retiring, at=self.at)


def has_replan_state(task):
    """Whether an existing task has started or accumulated review/recap/merge state that a retro amend resets."""
    if task.get("state") not in ("todo", "scheduled", "queued", "done"):
        return True
    return any(
        task.get(k)
        for k in (
            "returned_to_waiting", "reviews", "review_runs", "review_failures",
            "review_more_granted", "review_rounds_added", "review_auto_grants", "review_out",
            "recap", "recaps", "verdict_told", "merge_queued", "auto_merge", "review_followup",
        )
    ) or bool((task.get("pr") or {}).get("news") or (task.get("back") or {}).get("auto"))


def replan_task(s, task, *, why, at=None, dry=False, defer_side_effects=False):
    """Applies `REPLAN_TABLE` to `task`. Unless `dry` or `defer_side_effects`, runs GitHub/hc/news side effects too."""
    prev_reset = int(task.get("review_reset_at") or 0)
    at = max(now(), prev_reset + 1) if at is None else max(int(at), prev_reset + 1)
    was_merge_queued = bool(task.get("merge_queued"))
    if not dry and not defer_side_effects:
        cancel_queued_merge(s, task, was_merge_queued=was_merge_queued)
    ctx = {"store": s, "task": task, "why": why, "at": at, "dry": dry, "retiring": []}
    for row in REPLAN_TABLE:
        row.apply(ctx)
    if s is not None:
        s.dirty = True
    eff = ReplanEffect(store=s, task=task, retiring=list(ctx["retiring"]), at=at, was_merge_queued=was_merge_queued)
    if not dry and not defer_side_effects:
        retire_reviewers(eff.retiring)
        clear_task_held_news(s, task, eff.retiring, at=at)
    return eff


def select_retro_plan_targets(s, plan_tasks, by_key, reason, *, has_structural_changes=False):
    """Picks open tasks targeted for a re-plan in `plan amend` (explicit `retro: true`, else retro `--reason`)."""
    explicit = [by_key.get(t["key"]) for t, _ in plan_tasks if t.get("retro")]
    targets = [o for o in explicit if o is not None and o["state"] != "done"]
    if targets or not (reason and RETRO_REASON.search(reason)):
        return targets

    open_tasks = [o for t, _ in plan_tasks if (o := by_key.get(t["key"])) is not None and o["state"] != "done"]
    ref_tokens = [
        m.group(0)
        for m in re.finditer(r"(?<![\w-])([A-Za-z]{2,5}[0-9]*)-[0-9]+(?![\w-])", reason)
        if m.group(1).upper() not in NON_TASK_PREFIXES
    ]
    if ref_tokens:
        return [
            o for o in open_tasks
            if re.search(rf"(?<![\w-]){re.escape(s.ref(o))}(?![\w-])", reason, re.I)
        ]

    named = [
        o for o in open_tasks
        if re.search(rf"(?<![\w-]){re.escape(o['plan_key'])}(?![\w-])", reason, re.I)
    ]
    if named:
        return named

    known_other_keys = (
        {o.get("plan_key") for o in s.data.get("tasks") or [] if o.get("plan_key")}
        | set(s.data.get("removed_plan_keys") or ())
    ) - {o["plan_key"] for o in open_tasks if o.get("plan_key")}
    unresolved = any(
        re.search(rf"(?<![\w-]){re.escape(k)}(?![\w-])", reason, re.I) for k in known_other_keys
    )
    if unresolved or has_structural_changes:
        return []

    candidates = [o for o in open_tasks if o.get("recap") or o.get("review_out")]
    if len(candidates) > 1:
        refs = ", ".join(s.ref(o) for o in candidates)
        raise Fail(f"Multiple tasks need a recap or round reset ({refs}); name the one to re-plan in --reason or set \"retro\": true on it.")
    return candidates
