"""The loop-breaker (FAC-54, FAC-57, FAC-63): a task whose herdr-task review rounds aren't
converging, whose pull request CI keeps failing, or that is far over its own estimate (time,
tokens, or change size on ready), stops as Needs you "Recap required"; its worker writes RECAP.md
and the Assistant decides (recap --decide). docs/factory.md 11b, 12b: recap, then decide."""

import json
import os
import re
import shlex

from .base import Fail, git, now
from .github import BAD, PENDING, remote_of

DECISIONS = ("continue", "split", "new", "drop")
SHA = re.compile(r"[0-9a-f]{40}")
CI_FAIL_LIMIT = 3
CI_WINDOW_MS = 3600000
RUN_URL = re.compile(r"/actions/runs/(\d+)")
SIZE_LINE = re.compile(r"(?im)^[\s>*+-]*\**size(?:\s+limit)?\**\s*:\s*(.+)")
BRIEF_LINES = re.compile(r"(?i)(?<![\w.-])~?\s*(\d+(?:,\d{3})*)(?:\s*(?:[-–—]|to)\s*~?\s*(\d+(?:,\d{3})*))?\s+lines\b")
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


def parse_lines(text):
    """300, ~300, 300 lines: line count for estimate.lines."""
    m = re.fullmatch(r"~?\s*(\d+(?:,\d{3})*)\s*(?:lines?)?", str(text).strip().lower())
    n = int(m.group(1).replace(",", "")) if m else 0
    if not n:
        raise Fail(f"estimate.lines is like 300, not {text!r}.")
    return n


def brief_lines(text):
    """Largest line count in a brief's size phrase ('about 150-300 lines', '~300 lines', '300 lines'); None when none."""
    def scan(src):
        nums = [int(g.replace(",", "")) for m in BRIEF_LINES.finditer(src) for g in m.groups() if g]
        return max((n for n in nums if n > 0), default=None)
    for line in SIZE_LINE.findall(text or ""):
        if (n := scan(line.split(". ")[0]) or scan(line)) is not None:
            return n
    return scan(text or "")


def size_estimate(t):
    """The task's line-count estimate: --set estimate.lines, else read from its brief; None when neither sets one."""
    return int(t["estimate_lines"]) if t.get("estimate_lines") else brief_lines(t.get("prompt") or "")


GENERATED_NAMES = ("package-lock.json", "bun.lockb", "pnpm-lock.yaml", "npm-shrinkwrap.json", "Package.resolved", "Cargo.lock",
                   "Gemfile.lock", "poetry.lock", "go.sum", "uv.lock", "yarn.lock")
GENERATED_SUFFIXES = (".lock", ".pbxproj", ".xcstrings", ".xcworkspacedata")  # an XcodeGen project.yml writes the .pbxproj


def size_path(path):
    """A numstat path as it is now: a rename's new name, no quotes."""
    return re.sub(r"/+", "/", re.sub(r"\{[^{}]*=>\s*([^{}]*)\}", r"\1", path.strip('"')).split(" => ", 1)[-1]).lstrip("/")


def ignored_size_file(path):
    """Generated and lock files left out of a task's change size, by name alone."""
    p = size_path(path)
    return (os.path.basename(p) in GENERATED_NAMES or p.endswith(GENERATED_SUFFIXES)
            or p.startswith("web/generated/") or "/web/generated/" in p)


def marked_generated(wt, paths):
    """The paths the repo's own .gitattributes marks generated: linguist-generated (set or true) or -diff."""
    if not paths:
        return set()
    ok, out, _ = git(wt, "check-attr", "-z", "linguist-generated", "diff", "--", *paths)
    f = out.split("\0") if ok else []
    return {f[i] for i in range(0, len(f) - 2, 3) if (f[i + 1], f[i + 2]) in (("linguist-generated", "set"),
            ("linguist-generated", "true"), ("diff", "unset"))}


def generated_only(wt, paths):
    """Whether every path in `paths` is generated (by name or .gitattributes)."""
    rest = [size_path(p) for p in paths if p and not ignored_size_file(p)]
    return not rest or set(rest) <= marked_generated(wt, rest)


def merge_only_step(wt, base, head, mine, branch=None):
    """FAC-89: whether `head` since the last reviewed head before it (which passed) is only merges of `base`
    whose trees differ from git's clean merge of their two parents only in generated files (and had no
    conflicts outside generated files)."""
    prev = next((r["head"] for r in reversed(mine or ()) if r.get("head") and r["head"] != head), None)
    if not prev or not wt or not os.path.isdir(wt):
        return False
    last_prev = next((r for r in reversed(mine) if r.get("head") == prev), None)
    if not last_prev or last_prev.get("verdict") != "pass":
        return False
    bases = (base, f"refs/remotes/{remote_of(wt, branch)}/{base}")
    c = head
    for _ in range(100):
        if c == prev:
            return c != head
        ok, line, _ = git(wt, "rev-list", "--parents", "-n", "1", c)
        parts = line.split() if ok else []
        if len(parts) != 3:
            return False
        _, first, other = parts
        if not any(git(wt, "merge-base", "--is-ancestor", other, b)[0] for b in bases):
            return False
        _, mt, _ = git(wt, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", first, other)
        mp = [p for p in mt.split("\0") if p]
        if not mp or not SHA.fullmatch(mp[0]):
            return False
        ok_d, dt, _ = git(wt, "diff-tree", "-r", "--no-commit-id", "--name-only", "--no-renames", "-z", mp[0], c)
        if not ok_d or not generated_only(wt, {*mp[1:], *(p for p in dt.split("\0") if p)}):
            return False
        c = first
    return False


def change_sizes(t):
    """(counted, generated): added + deleted lines in a worktree task's diff from its merge-base with base to HEAD,
    split by whether the file is generated; None when the task has no live worktree."""
    wt = t.get("worktree") if t.get("mode") == "worktree" else None
    if not wt or not os.path.isdir(wt):
        return None
    from .commands import fork_of
    ok, head, _ = git(wt, "rev-parse", "--verify", "--quiet", "HEAD")
    if not ok or not head:
        return None
    mb = fork_of(wt, t.get("base") or "main", head)
    if not mb and t.get("start"):
        ok_mb, out_mb, _ = git(wt, "merge-base", t["start"], head)
        mb = out_mb if ok_mb and out_mb else t["start"]
    if not mb:
        return None
    ok, out, _ = git(wt, "-c", "core.quotePath=false", "diff", "--numstat", f"{mb}..{head}")
    if not ok:
        return None
    rows = [(size_path(parts[2]), parts) for parts in (line.split("\t", 2) for line in out.splitlines()) if len(parts) == 3]
    marked = marked_generated(wt, [p for p, _ in rows if not ignored_size_file(p)])
    counted = generated = 0
    for p, (a, r, _) in rows:
        n = (int(a) if a.isdigit() else 0) + (int(r) if r.isdigit() else 0)
        if ignored_size_file(p) or p in marked:
            generated += n
        else:
            counted += n
    return counted, generated


def change_lines(t):
    """The counted lines of change_sizes: what the size check compares; None when the task has no live worktree."""
    sizes = change_sizes(t)
    return None if sizes is None else sizes[0]


def plus_generated(generated):
    return f" (plus {generated:,} generated)" if generated else ""


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
    """herdr-task's own non-merge-only review rounds recorded after the task's last recap decision."""
    since = ((t.get("recaps") or [{}])[-1]).get("at", 0)
    mine = [r for r in t.get("reviews") or [] if r.get("started_by") == "herdr-task"]
    skip = set(t.get("merge_only_runs") or ())
    known = {r.get("address") for r in t.get("review_runs") or [] if r.get("address")}
    wt = t.get("worktree") if t.get("mode") == "worktree" else None
    base, br = t.get("base") or "main", t.get("branch")
    return [r for i, r in enumerate(mine) if (r.get("at") or 0) > since and r.get("by") not in skip
            and not (r.get("by") not in known and SHA.fullmatch(r.get("head") or "") and merge_only_step(wt, base, r["head"], mine[:i], br))]


def ci_since(t):
    """Pull request CI runs recorded after the task's last recap decision."""
    since = ((t.get("recaps") or [{}])[-1]).get("at", 0)
    return [r for r in t.get("ci_runs") or [] if (r.get("at") or 0) > since]


def record_ci(s, t, v, at=None):
    """Records finished CI runs on the task's open pull request (v: pr_view's answer) as a small
    list of {sha, run, failed, at}. Re-running the same run id goes pending or changes its job
    or completion time, so it counts as another attempt; cancelled runs without a failure don't."""
    if not v or v.get("state") != "OPEN":
        return
    rows, head = v.get("statusCheckRollup") or [], v.get("headRefOid") or ""
    if not rows:
        if t.pop("ci_pending", None) is not None:
            s.dirty = True
        return
    at = now() if at is None else at
    groups, pending = {}, False
    for c in rows:
        if not isinstance(c, dict):
            continue
        m = RUN_URL.search(c.get("detailsUrl") or c.get("targetUrl") or "")
        rid = m.group(1) if m else None
        groups.setdefault((f"run:{rid}" if rid else f"sha:{head or 'pr'}", rid), []).append(c)
    seen = t.setdefault("ci_seen", {})
    for (key, rid), cs in groups.items():
        if any((c.get("status") or c.get("state") or "").upper() in PENDING and not (c.get("conclusion") or "") for c in cs):
            pending = True
            if seen.pop(key, None) is not None:
                s.dirty = True
            continue
        words = [(c.get("conclusion") or c.get("state") or "").upper() for c in cs]
        failed = any(w in BAD - {"CANCELLED"} for w in words)
        if not any(words) or (not failed and "CANCELLED" in words):
            continue
        sig = "|".join(sorted(f"{c.get('name') or c.get('context') or ''}:{(c.get('conclusion') or c.get('state') or '').upper()}:"
                              f"{c.get('detailsUrl') or c.get('targetUrl') or ''}:{c.get('completedAt') or c.get('startedAt') or ''}" for c in cs))
        if seen.get(key) == sig:
            continue
        seen[key] = sig
        while len(seen) > 50:
            seen.pop(next(iter(seen)))
        entry = {"sha": head, **({"run": rid} if rid else {}), "failed": failed, "at": at}
        t["ci_runs"] = (t.get("ci_runs") or [])[-49:] + [entry]
        s.dirty = True
    if bool(t.get("ci_pending")) != pending:
        if pending:
            t["ci_pending"] = True
        else:
            t.pop("ci_pending", None)
        s.dirty = True
    if not seen:
        t.pop("ci_seen", None)


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
    ci = ci_since(t)
    failed_ci = [r for r in ci if r.get("failed")]
    if len(failed_ci) >= CI_FAIL_LIMIT:
        why.append(f"{len(failed_ci)} failed CI runs")
    last_sha = ci[-1].get("sha") if ci else None
    still_red = (any(r.get("failed") for r in {r.get("run"): r for r in ci if r.get("sha") == last_sha}.values())
                 if last_sha else bool(ci and ci[-1].get("failed")))
    if ci and (still_red or (failed_ci and t.get("ci_pending"))) and now() - (ci[0].get("at") or 0) >= CI_WINDOW_MS:
        why.append("1h on CI")
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


def size_triggers(t):
    """Why ready stops this worktree task for size, or []. After recap --decide continue, doesn't fire again for that decision."""
    est, dec = size_estimate(t), (t.get("recaps") or [{}])[-1]
    if not est or (dec.get("decision") == "continue" and (
            not dec.get("reasons") or any("twice its size" in r for r in dec["reasons"]) or (dec.get("lines") or 0) > 2 * est)):
        return []
    sizes = change_sizes(t)
    n, gen = sizes or (None, 0)
    return [f"change is {n:,} lines{plus_generated(gen)}, over twice its size ({est})"] if n is not None and n > 2 * est else []


def keep_recap(s, t):
    if t.get("attention") != t["recap"]["title"]:  # something else changed it: the recap still waits
        t["attention"], t["attention_at"] = t["recap"]["title"], now()
        s.dirty = True
    unmergeable(s, t)
    return True


def open_recap(s, t, why):
    from .commands import tell, workers  # commands imports this module
    unmergeable(s, t)
    again = bool(t.get("recaps"))
    title = ("Recap required again: change direction" if again else f"Recap required: {why[0]}")[:120]
    t["recap"] = {"at": now(), "reasons": why, "round": len(rounds_since(t)), "title": title}
    t["attention"], t["attention_at"] = title, now()
    s.dirty = True
    me = os.environ.get("HC_TERMINAL_ID")
    if [w for w in workers(s, t) if w.get("address") != me]:
        try:
            t["recap"]["told"] = tell(s, t, stop_words(s, t, why, again), None)[0]
        except Fail:
            pass
    return True


def budget_pass(s, t):
    """The tick's look at a running or ready worktree task: a trigger opens a recap once, makes the
    task Needs you and tells its worker to stop and write RECAP.md. True when one is open."""
    if t.get("recap"):
        return keep_recap(s, t)
    why = triggers(s, t)
    return open_recap(s, t, why) if why else False


def size_pass(s, t):
    """ready's size check on a worktree task: past twice its size estimate opens a recap."""
    if t.get("recap"):
        return keep_recap(s, t)
    why = size_triggers(t)
    return open_recap(s, t, why) if why else False


def last_findings(t):
    r = next((r for r in reversed(rounds_since(t)) if r.get("verdict") == "changes"), None)
    return f"\n\nThe last review round's findings:\n{r['report']}\n\n" if r and r.get("report") else " "


def unmergeable(s, t):
    """Never mergeable while a recap is open, nor after it (drop, split): Working until its next ready."""
    from .commands import back_to_work
    back_to_work(s, t, "a recap is required")
    am, news = t.pop("auto_merge", None), (t.get("pr") or {}).pop("news", None)
    if am is not None or news is not None:
        s.dirty = True


def stop_words(s, t, why, again):
    wt = t.get("worktree") or "the worktree"
    ci = any("CI" in w for w in why)
    size = any("twice its size" in w for w in why)
    what = ("what was tried across the CI runs, which CI checks failed and why, and the options (new approach such as test "
            "retries or a skip list, one more round, split, drop)" if ci else
            "what drove the size, which files hold most of the change, and the options (continue as one change, split, "
            "new approach, drop)" if size else
            "what was tried round by round, the recurring findings and the premise behind them, and the options (one more "
            "round, split, new approach, drop)")
    return (f"Stop {s.ref(t)}: Herdr's loop-breaker says recap first ({'; '.join(why)}).{last_findings(t)}Don't fix anything more"
            + (" or re-run CI. " if ci else ". ")
            + f"Write RECAP.md at {wt}/RECAP.md, untracked and never committed, half a page at most: the goal, {what}. "
            + "Then end your turn and wait: your Assistant decides what happens next."
            + (" This is the second recap: one more round of the same is no longer an option." if again else ""))


def budget_lines(t):
    """show's lines: the task's estimate, its open recap and its past decisions."""
    sz = size_estimate(t)
    est = [w for k, f in (("estimate_time", time_words), ("estimate_tokens", tokens_words)) if t.get(k) for w in [f(t[k])]]
    if sz:
        est.append(f"{sz} lines")
    lines = []
    if est:
        so_far = ([f"Claude tokens so far: {tokens_words(int(sum(c.get('sum', 0) for c in t['tokens'].values())))}"]
                  if t.get("tokens") else [])
        cur = change_sizes(t) if sz else None
        if cur is not None:
            so_far.append(f"current size: {cur[0]:,} lines{plus_generated(cur[1])}")
        lines.append(f"estimate: {', '.join(est)}" + (f" ({'; '.join(so_far)})" if so_far else ""))
    if t.get("recap"):
        lines.append(f"recap open: {t['recap']['title']} (the Assistant decides: recap --decide)")
    lines += [f"recap decided: {d['decision']} ({d['why']})" for d in t.get("recaps") or []]
    return lines


def recap_refusal(s, t):
    rc = t.get("recap")
    if rc:
        why = f" ({'; '.join(rc['reasons'])})" if rc.get("reasons") else ""
        return (f"{s.ref(t)} needs a recap first{why}: write RECAP.md (untracked) at your worktree's root, end your turn and "
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
        cur = change_lines(t)
        t.setdefault("recaps", []).append({"decision": args.decide, "why": " ".join(args.why.split())[:300], "at": now(),
                                           "reasons": rc.get("reasons", []), "tokens": token_sum(s, t),
                                           **({"lines": cur} if cur is not None else {})})
        t.pop("recap")
        if (t.get("attention") or "").startswith("Recap required"):
            t.pop("attention", None)
            t.pop("attention_at", None)
        if args.decide == "drop":
            from .simulators import release_task
            release_task(s, t)
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
