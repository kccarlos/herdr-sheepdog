"""The loop-breaker (FAC-54): a task whose herdr-task review rounds aren't converging, or that is
far over its own estimate, stops as Needs you "Recap required"; its worker writes RECAP.md and
the Assistant decides (recap --decide). docs/factory.md 11b: recap, then decide; never grind."""

import json
import os
import re
import shlex

from .base import Fail, now

DECISIONS = ("continue", "split", "new", "drop")
# Claude tokens in input equivalents, as docs/factory-cost-report-2026-11-08.md weighs them.
WEIGHTS = {"input_tokens": 1, "cache_creation_input_tokens": 1.25, "cache_read_input_tokens": 0.1, "output_tokens": 5}
FILE = re.compile(r"[\w-]+(?:/[\w.-]+)*\.([A-Za-z]\w{0,5})\b")


def parse_time(text):
    """90m, 3h, 1h30m, 2d: in ms."""
    t = str(text).strip().lower().replace(" ", "")
    parts = re.findall(r"(\d+(?:\.\d+)?)([dhm])", t)
    if not parts or "".join(a + b for a, b in parts) != t:
        raise Fail(f"estimate.time is like 90m, 3h or 1h30m, not {text!r}.")
    return int(sum(float(n) * {"d": 86400000, "h": 3600000, "m": 60000}[u] for n, u in parts))


def parse_tokens(text):
    """3M, 800k, 1200000: Claude tokens in input equivalents."""
    m = re.fullmatch(r"(\d+(?:\.\d+)?)([km]?)", str(text).strip().lower().replace(" ", ""))
    if not m or not float(m.group(1)):
        raise Fail(f"estimate.tokens is like 3M or 800k, not {text!r}.")
    return int(float(m.group(1)) * {"": 1, "k": 1000, "m": 1000000}[m.group(2)])


def tokens_words(n):
    return f"{n / 1e6:.1f}M" if n >= 1e6 else f"{n // 1000}k" if n >= 1000 else str(n)


def time_words(ms):
    m = int(ms // 60000)
    return f"{m // 60}h{m % 60:02d}m" if m >= 60 else f"{m}m"


def files_in(report):
    """The file paths a review report names (file.py, dir/file.ts:12): a simple regex, lines dropped."""
    return {m.group(0) for m in FILE.finditer(report or "") if len(m.group(1)) > 1 or "/" in m.group(0)}


def log_tokens(path, cache):
    """A Claude transcript's weighted tokens, read only past what cache {off, sum, last} saw; each API
    response counted once (Claude Code logs one line per content block, the same usage on each)."""
    try:
        size = os.path.getsize(path)
    except OSError:
        return cache
    if size <= cache.get("off", 0):
        return cache if size == cache.get("off", 0) else log_tokens(path, {})  # shorter: a new file
    with open(path, "rb") as f:
        f.seek(cache.get("off", 0))
        chunk = f.read(size - cache.get("off", 0))
    whole = chunk[:chunk.rfind(b"\n") + 1]  # a half-written last line waits for the next look
    total, last = cache.get("sum", 0), cache.get("last")
    for line in whole.splitlines():
        if b'"usage"' not in line:
            continue
        try:
            m = json.loads(line).get("message") or {}
        except ValueError:
            continue
        u = m.get("usage") if isinstance(m, dict) else None
        if not isinstance(u, dict) or (m.get("id") and m.get("id") == last):
            continue
        last = m.get("id")
        total += sum(int(u.get(k) or 0) * w for k, w in WEIGHTS.items())
    return {"off": cache.get("off", 0) + len(whole), "sum": total, "last": last}


def token_sum(s, t):
    """The task's Claude tokens so far, from its Claude agents' logs (cached on the task by log)."""
    caches, total = t.setdefault("tokens", {}), 0
    for log in dict.fromkeys(a.get("log") for a in t.get("agents") or []):  # a resumed session shares its log
        if not log or "/.claude/" not in log:
            continue
        got = log_tokens(log, caches.get(log) or {})
        if got != caches.get(log):
            caches[log] = got
            s.dirty = True
        total += got.get("sum", 0)
    if not caches:
        t.pop("tokens")
    return int(total)


def rounds_since(t):
    """herdr-task's own review rounds recorded after the task's last recap decision."""
    since = ((t.get("recaps") or [{}])[-1]).get("at", 0)
    return [r for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task" and (r.get("at") or 0) > since]


def triggers(s, t):
    """Why this task should stop for a recap, most telling first; [] when it is converging."""
    why, rounds = [], rounds_since(t)
    last = rounds[-1] if rounds else None
    if last and len(rounds) >= 2 and last.get("verdict") == "changes":
        why.append(f"review round {len(rounds)} still has {last.get('must_fix') or 'some'} finding(s) at Must fix")
    for a, b in zip(rounds, rounds[1:]):
        if a.get("verdict") == b.get("verdict") == "changes":
            if (b.get("must_fix") or 0) >= (a.get("must_fix") or 0):
                why.append(f"Must-fix findings didn't drop ({a.get('must_fix')} then {b.get('must_fix')})")
    flagged = [files_in(r.get("report")) for r in rounds if r.get("verdict") == "changes"]
    again = sorted(set.union(set(), *[x & y for i, x in enumerate(flagged) for y in flagged[i + 1:]]))
    if again:
        why.append("the same file flagged in two rounds: " + ", ".join(again[:3]))
    # Budgets count from the last decision (else from its start); time only while it works, not
    # while it waits in Ready for the person.
    est, dec = t.get("estimate_time"), (t.get("recaps") or [{}])[-1]
    began = dec.get("at") or t.get("started")
    if est and began and t["state"] == "running" and now() - began > 2 * est:
        why.append(f"over twice its time estimate ({time_words(now() - began)} of {time_words(est)})")
    est = t.get("estimate_tokens")
    used = token_sum(s, t) - (dec.get("tokens") or 0) if est else 0
    if est and used > 2 * est:
        why.append(f"over twice its token estimate ({tokens_words(used)} of {tokens_words(est)})")
    return list(dict.fromkeys(why))


def budget_pass(s, t):
    """The tick's look at a running or ready worktree task: a trigger opens a recap once, makes the
    task Needs you and tells its worker to stop and write RECAP.md. True when one is open."""
    from .commands import back_to_work, tell, workers  # commands imports this module
    if t.get("recap"):
        if t.get("attention") != t["recap"]["title"]:  # something else changed it: the recap still waits
            t["attention"], t["attention_at"] = t["recap"]["title"], now()
            s.dirty = True
        unmergeable(s, t)
        return True
    why = triggers(s, t)
    if not why:
        return False
    unmergeable(s, t)
    again = bool(t.get("recaps"))
    title = ("Recap required again: change direction" if again else f"Recap required: {why[0]}")[:120]
    t["recap"] = {"at": now(), "reasons": why, "round": len(rounds_since(t)), "title": title}
    t["attention"], t["attention_at"] = title, now()
    s.dirty = True
    if workers(s, t):
        try:
            t["recap"]["told"] = tell(s, t, stop_words(s, t, why, again), None)[0]
        except Fail:
            pass
    return True


def last_findings(t):
    r = next((r for r in reversed(rounds_since(t)) if r.get("verdict") == "changes"), None)
    return f"\n\nThe last review round's findings:\n{r['report']}\n\n" if r and r.get("report") else " "


def unmergeable(s, t):
    """Never mergeable while a recap is open, nor after it (drop, split): Working until its next ready."""
    from .commands import back_to_work
    back_to_work(s, t, "a recap is required")
    if t.pop("auto_merge", None) is not None:
        s.dirty = True


def stop_words(s, t, why, again):
    wt = t.get("worktree") or "the worktree"
    return (f"Stop {s.ref(t)}: Herdr's loop-breaker says recap first ({'; '.join(why)}).{last_findings(t)}Don't fix anything more. "
            f"Write RECAP.md at {wt}/RECAP.md, untracked and never committed, half a page at most: the goal, what was "
            f"tried round by round, the recurring findings and the premise behind them, and the options (one more round, "
            f"split, new approach, drop). Then end your turn and wait: your Assistant decides what happens next."
            + (" This is the second recap: one more round of the same is no longer an option." if again else ""))


def budget_lines(t):
    """show's lines: the task's estimate, its open recap and its past decisions."""
    est = [w for k, f in (("estimate_time", time_words), ("estimate_tokens", tokens_words)) if t.get(k) for w in [f(t[k])]]
    lines = [f"estimate: {', '.join(est)}" + (f" (Claude tokens so far: {tokens_words(int(sum(c.get('sum', 0) for c in t['tokens'].values())))})"
                                             if t.get("tokens") else "")] if est else []
    if t.get("recap"):
        lines.append(f"recap open: {t['recap']['title']} (the Assistant decides: recap --decide)")
    lines += [f"recap decided: {d['decision']} ({d['why']})" for d in t.get("recaps") or []]
    return lines


def recap_refusal(s, t):
    if t.get("recap"):
        return (f"{s.ref(t)} needs a recap first: write RECAP.md (untracked) at your worktree's root, end your turn and "
                f"wait; your Assistant decides what happens next.")
    return None


def cmd_recap(args):
    """The Assistant's decision on a task's open recap (continue once, split, new, drop); no flags:
    the open recap and RECAP.md."""
    from .launch import caller_labels
    from .output import say
    from .store import open_store
    with open_store(args, write=bool(args.decide)) as s:
        t = s.find(args.id)
        rc, wt = t.get("recap"), t.get("worktree") or ""
        if not args.decide:
            try:
                with open(os.path.join(wt, "RECAP.md")) as f:
                    text = f.read()
            except OSError:
                text = None
            lines = [f"{s.ref(t)}: " + (rc["title"] if rc else "no recap open")] + [f"- {x}" for x in (rc or {}).get("reasons", [])]
            lines += [f"decided: {d['decision']} ({d['why']})" for d in t.get("recaps") or []]
            lines += ["", text.strip()] if text else ["(no RECAP.md yet)"] if rc else []
            say(args, {"recap": rc, "recaps": t.get("recaps") or [], "recap_md": text}, "\n".join(lines))
            return
        labels = caller_labels()
        if labels.get("herdr.task") == s.key(t) or labels.get("herdr.role") == "reviewer":
            raise Fail("A task's own agent can't decide its recap: your Assistant does.")
        if not rc:
            raise Fail(f"{s.ref(t)} has no recap open.")
        if not (args.why or "").strip():
            raise Fail("Say why in one line: --why \"…\".")
        if args.decide == "continue" and t.get("recaps"):
            raise Fail(f"{s.ref(t)} already had its one continue after a recap: split it, give it a new approach, or drop it.")
        found = last_findings(t)  # before the decision starts the round count afresh
        t.setdefault("recaps", []).append({"decision": args.decide, "why": " ".join(args.why.split())[:300], "at": now(),
                                           "reasons": rc.get("reasons", []), "tokens": token_sum(s, t)})
        t.pop("recap")
        if (t.get("attention") or "").startswith("Recap required"):
            t.pop("attention", None)
            t.pop("attention_at", None)
        s.dirty = True
        told = None
        if args.decide == "continue":
            from .commands import tell, workers
            from .launch import own_path
            if workers(s, t):
                told = tell(s, t, f"Your Assistant read the recap of {s.ref(t)} and says go on: {args.why.strip()}"
                                  f"{found}Carry on, then run `{own_path()} ready {s.ref(t)} --folder {shlex.quote(s.loc['folder'])}` again.", None)[0]
    say(args, {"decision": args.decide, "told": told},
        f"{s.ref(t)}: recap decided: {args.decide}" + (f"; told {told}." if told else "." if args.decide == "continue" else
                                                       "; act on it yourself (new tasks, a new brief, or the person's cancel)."))
