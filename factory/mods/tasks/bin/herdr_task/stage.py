"""One ordered stage table per task: first matching row wins; owner-waiting rows are the inbox."""

from dataclasses import dataclass
from typing import Optional

from . import agents

# Owner-facing ask kinds (phase 1 + owner-contract v1); coordinator-answered "decision" is excluded
# unless explicitly marked gated=True.
OWNER_ASK_KINDS = ("release", "paid-key", "delete", "install", "reset", "owner", "gate", "rule")


@dataclass(frozen=True)
class StageResult:
    stage: str
    inbox: bool
    label: str
    why: Optional[str]
    status: str
    needs: Optional[str]
    legacy_why: Optional[str]


def _no_ci_run():
    from .github import NO_CI_RUN
    return NO_CI_RUN


def _effective_task_and_attention(task, live):
    """Apply GRACE_MS and on_it() rules to attention, returning (view_task, active_attention, on_it_working)."""
    att = task.get("attention")
    if not att:
        return task, None, False
    st = task.get("state")
    if st in ("todo", "queued"):
        return task, att, False
    if st not in ("running", "ready"):
        return task, None, False
    if (not task.get("recap") and not str(att).startswith(agents.REVIEWER_FAILED)
            and agents.now() - (task.get("attention_at") or 0) < agents.GRACE_MS):
        return {**task, "attention": None}, None, False
    if not agents.on_it(task, live):
        return task, att, False
    return task, None, True


def _live_states(task, live):
    return ["working" if agents._startup_auth(task, k) else "blocked" if agents.asking(k) else agents.status_word(k)
            for k in live]


def _quota_wait(task):
    q = task.get("review_quota_wait") or {}
    return bool(q.get("head") == task.get("head") and task.get("head"))


def _owner_asks(store, task, asks):
    if asks is None:
        if store is not None and hasattr(store, "ref"):
            from .ask import open_asks
            asks = open_asks(store, task)
        else:
            asks = []
    if not asks:
        return []
    workers = {a.get("address") for a in task.get("agents") or [] if isinstance(a, dict)}
    out = []
    for a in asks:
        if not isinstance(a, dict) or a.get("answer") is not None:
            continue
        if a.get("status") in ("answered", "cancelled"):
            continue
        gated = a.get("gated")
        kind = a.get("kind")
        if gated is True:
            out.append(a)
        elif gated is False:
            continue
        elif kind in OWNER_ASK_KINDS or (kind != "decision" and a.get("to") not in workers):
            out.append(a)
    return out


def _blocked_live(task, live):
    states = _live_states(task, live)
    if "blocked" in states:
        return agents.REASONS["question"]
    _, dialog_why = agents.startup_dialog(live)
    return dialog_why


def _deps_waiting(store, task, deps):
    if deps is not None:
        return list(deps)
    if store is not None and hasattr(store, "prefix") and hasattr(store, "data"):
        from .queues import waiting_ext, waiting_for
        return [f"{store.prefix}-{i}" for i in waiting_for(store, task)] + waiting_ext(task)
    from .queues import waiting_ext
    return waiting_ext(task)


def _waits_for_owner_accept(store, task):
    if task.get("accept_wait") or (task.get("pr") or {}).get("no_ci"):
        why = task.get("accept_wait") or _no_ci_run()
        return True, why
    if store is not None and hasattr(store, "loc"):
        from .settings import accept_bar, merge_policy
        if merge_policy(store, task) != "ask":
            return False, None
        _, bar_why = accept_bar(store, task)
        if bar_why == "waits for Fable's review round":
            return False, None
        return True, bar_why or "Waiting for owner Accept"
    if not task.get("auto_merge"):
        return True, "Waiting for owner Accept"
    return False, None


def _is_working(ctx):
    task, live = ctx["task"], ctx["live"]
    st = task.get("state")
    if st == "ready":
        if ctx["on_it_working"]:
            return True
        if agents.back_at_work(task, live):
            return True
        if live and agents.not_at_rest(task, live):
            return True
        if agents.review_running(task) or _quota_wait(task):
            return True
        wait = agents.ready_wait(task, live)
        if wait == agents.MERGING:
            return True
        return False
    if st != "running":
        return False
    if ctx["on_it_working"]:
        return True
    if agents.back_at_work(task, live):
        return True
    r = task.get("retry")
    if r and any(k.get("address") == r.get("address") for k in live) and not r.get("over"):
        return True
    if any(x not in ("idle", "done", "blocked") for x in ctx["states"]):
        return True
    if agents.starting(task) or agents.review_running(task) or _quota_wait(task):
        return True
    if live and agents.helper_working(task, live):
        return True
    told = task.get("bg_told")
    if told and told.get("at") and any(k.get("address") == told.get("address") for k in live) and not agents.turn_since(told, live):
        return agents.now() - told["at"] < agents.BG_GRACE_MS
    if live and agents.own_work(task, live):
        return True
    if live and agents.settling(live):
        return True
    return False


# ---- Ordered stage table matchers (first match wins) --------------------------------------------

def _match_owner_answer(ctx):
    task, live = ctx["task"], ctx["live"]
    asks = ctx["owner_asks"]
    dev_why = (task.get("device_wait") or {}).get("why") if task.get("state") == "running" and live else None
    if asks:
        q = str(asks[0].get("question") or "Owner answer requested")
        if dev_why:
            return q, "needs", "reply", agents.device_why(task)
        return q, None, None, None
    if dev_why:
        why = agents.device_why(task)
        return why, "needs", "reply", why
    return None


def _match_done(ctx):
    if ctx["raw_task"].get("state") == "done":
        return None, "done", None, None
    return None


def _match_owner_gate(ctx):
    task, live, store = ctx["task"], ctx["live"], ctx["store"]
    st = task.get("state")
    if st == "running" and task.get("review_out"):
        from .settings import review_kind
        if review_kind(task):
            why = agents.out_of_rounds_why(task)
            return why, "needs", "reply", why
        return None
    if st != "ready":
        return None
    if ctx["active_attention"] or ctx["on_it_working"] or ctx["blocked_why"]:
        return None
    if agents.back_at_work(task, live) or agents.ready_wait(task, live):
        return None
    if (live and agents.not_at_rest(task, live)) or agents.review_running(task) or _quota_wait(task):
        return None
    waits, why = _waits_for_owner_accept(store, task)
    if waits:
        return why, "ready", None, None
    return None


def _match_blocked_or_failed(ctx):
    task, live = ctx["task"], ctx["live"]
    st = task.get("state")
    if ctx["active_attention"]:
        return ctx["active_attention"], "needs", "attention", ctx["active_attention"]
    if st not in ("running", "ready"):
        return None
    r = task.get("retry") if st == "running" else None
    if r and any(k.get("address") == r.get("address") for k in live) and r.get("over"):
        why = r.get("reason")
        return why, "needs", "provider", why
    if ctx["blocked_why"]:
        return ctx["blocked_why"], "needs", "asks", ctx["blocked_why"]
    if st == "running" and task.get("review_out"):
        why = agents.out_of_rounds_why(task)
        return why, "needs", "reply", why
    if st == "running" and not live and not agents.starting(task) and not agents.review_running(task) and not _quota_wait(task):
        return agents.STOPPED, "needs", "stopped", agents.STOPPED
    return None


def _match_ci(ctx):
    task, live, store = ctx["task"], ctx["live"], ctx["store"]
    if task.get("state") != "ready" or ctx["on_it_working"] or agents.back_at_work(task, live):
        return None
    wait = agents.ready_wait(task, live)
    if wait:
        return None if wait == agents.MERGING else (wait, "working", None, wait)
    if (live and agents.not_at_rest(task, live)) or agents.review_running(task) or _quota_wait(task):
        return None
    if task.get("pr"):
        if store is not None and hasattr(store, "loc"):
            from .settings import merge_policy
            if merge_policy(store, task) != "ask":
                return "Merges once its checks pass", "ready", None, None
        elif task.get("auto_merge"):
            return "Merges once its checks pass", "ready", None, None
    return None


def _match_waiting_on_dep(ctx):
    task = ctx["task"]
    st = task.get("state")
    if st not in ("todo", "queued", "scheduled"):
        return None
    deps = ctx["deps"]
    if not deps:
        return None
    why = "Waiting on " + ", ".join(deps)
    if st == "scheduled":
        from .when import starts_words
        return why, "scheduled", None, starts_words(task.get("scheduled_for") or agents.now(), agents.now())
    return why, st, None, None


def _match_queued(ctx):
    task = ctx["task"]
    st = task.get("state")
    if st in ("running", "ready", "done"):
        return None
    if st == "scheduled":
        from .when import starts_words
        sw = starts_words(task.get("scheduled_for") or agents.now(), agents.now())
        return sw, "scheduled", None, sw
    if st == "queued":
        why = ctx["queue_why"] or task.get("reason") or task.get("held") or "Queued"
        return why, "queued", None, None
    return None, (st if st in agents.STATUSES else "todo"), None, None


def _match_idle(ctx):
    task, live = ctx["task"], ctx["live"]
    if task.get("state") != "running" or not live or _is_working(ctx):
        return None
    told = task.get("bg_told")
    if told and told.get("at") and any(k.get("address") == told.get("address") for k in live) and not agents.turn_since(told, live):
        return agents.BG_ENDED_WHY, "needs", "reply", agents.BG_ENDED_WHY
    return agents.REASONS["reply"], "needs", "reply", agents.REASONS["reply"]


def _match_working(ctx):
    task, live, store = ctx["task"], ctx["live"], ctx["store"]
    st = task.get("state")
    if st == "ready":
        if ctx["on_it_working"] or agents.back_at_work(task, live):
            return agents.WORKING_AGAIN, "working", None, agents.WORKING_AGAIN
        wait = agents.ready_wait(task, live)
        if wait:
            return wait, "working", None, wait
        if live and agents.not_at_rest(task, live):
            return None, "working", None, None
        if agents.review_running(task):
            why = None if (task.get("progress") or {}).get("text") else agents.REVIEWER_ON_IT
            return why, "ready", None, None
        if _quota_wait(task):
            return task["review_quota_wait"]["text"], "ready", None, None
        if store is not None and hasattr(store, "loc"):
            from .settings import accept_bar
            if accept_bar(store, task)[1] == "waits for Fable's review round":
                return "Waits for Fable's review round", "ready", None, None
        return None, "ready", None, None
    if agents.back_at_work(task, live):
        return agents.WORKING_AGAIN, "working", None, agents.WORKING_AGAIN
    r = task.get("retry")
    if r and any(k.get("address") == r.get("address") for k in live):
        rl = agents.retry_line(r)
        return rl, "working", None, rl
    if ctx["on_it_working"]:
        return None, "working", None, None
    if any(x not in ("idle", "done", "blocked") for x in ctx["states"]):
        return None, "working", None, None
    if agents.starting(task):
        return None, "working", None, None
    if agents.review_running(task):
        why = None if (task.get("progress") or {}).get("text") else agents.REVIEWER_ON_IT
        return why, "working", None, why
    if _quota_wait(task):
        qw = task["review_quota_wait"]["text"]
        return qw, "working", None, qw
    if live and agents.helper_working(task, live):
        return None, "working", None, None
    if live and (agents.own_work(task, live) or (task.get("bg_told") and not agents.turn_since(task["bg_told"], live))):
        return agents.WAITING_BG, "working", None, agents.WAITING_BG
    if live and agents.settling(live):
        return None, "working", None, None
    return None, "working", None, None


# Single ordered stage table: first matching row wins.
# Owner-waiting rows (inbox=True) are the owner's inbox; nothing else is.
STAGE_TABLE = (
    ("owner_answer",      True,  "Owner answer",          _match_owner_answer),
    ("done",              False, "Done",                  _match_done),
    ("owner_gate",        True,  "Owner gate",            _match_owner_gate),
    ("blocked_or_failed", False, "Blocked or failed",     _match_blocked_or_failed),
    ("ci",                False, "CI",                    _match_ci),
    ("waiting_on_dep",    False, "Waiting on dependency", _match_waiting_on_dep),
    ("queued",            False, "Queued",                _match_queued),
    ("idle",              False, "Idle",                  _match_idle),
    ("working",           False, "Working",               _match_working),
)

STAGES = tuple(row[0] for row in STAGE_TABLE)
OWNER_STAGES = frozenset(row[0] for row in STAGE_TABLE if row[1])
STAGE_LABELS = {row[0]: row[2] for row in STAGE_TABLE}
STAGE_ORDER = {row[0]: i for i, row in enumerate(STAGE_TABLE)}


def resolve_stage(task, live=(), *, store=None, asks=None, deps=None, queue_why=None):
    """Evaluate STAGE_TABLE in order; the first matching row wins."""
    live = list(live or ())
    view_task, active_attention, on_it_working = _effective_task_and_attention(task, live)
    ctx = {
        "raw_task": task,
        "task": view_task,
        "live": live,
        "store": store,
        "owner_asks": _owner_asks(store, task, asks),
        "active_attention": active_attention,
        "on_it_working": on_it_working,
        "states": _live_states(view_task, live),
        "blocked_why": _blocked_live(view_task, live) if view_task.get("state") in ("running", "ready") else None,
        "deps": _deps_waiting(store, view_task, deps) if view_task.get("state") in ("todo", "queued", "scheduled") else [],
        "queue_why": queue_why,
    }
    matched = None
    for stage_name, inbox, label, matcher in STAGE_TABLE:
        res = matcher(ctx)
        if res is None:
            continue
        why, status, needs, legacy_why = res
        if status is not None:
            if matched is None:
                matched = (stage_name, inbox, label, why)
            return StageResult(
                stage=matched[0],
                inbox=matched[1],
                label=matched[2],
                why=matched[3],
                status=status,
                needs=needs,
                legacy_why=legacy_why,
            )
        if matched is None:
            matched = (stage_name, inbox, label, why)
    # Unreachable because _match_working is a catch-all, but defensive.
    return StageResult("working", False, "Working", None, "working", None, None)
