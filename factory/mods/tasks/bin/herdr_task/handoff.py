"""At the floor (docs/factory.md section 7): the handoff of each running task whose worker is on a
usage pool that reached its floor, written from git and its chat by takeover's handover(), and kept
in a file per task under the store's folder. Only the writer: interrupting, the tick and a command
come with the gate (G4)."""

import os

from .agents import agent_log
from .settings import argv_model
from .base import git
from .takeover import chat_tail, handover, work_so_far
from .usage import _AG


def pool_of(agent):
    """The usage pool an agent record ({agent, argv}) spends, by usage.py's names: claude, codex, or
    for agy its model's Antigravity pool (Gemini, or Claude/GPT). None when it can't be told (agy on
    its own default model)."""
    kind = (agent or {}).get("agent")
    if kind in ("claude", "codex"):
        return kind
    if kind == "agy":
        model = (argv_model([str(x) for x in agent.get("argv") or []])[0] or "").lower()
        if model.startswith("gemini"):
            return _AG["gemini"]
        if model.startswith(("claude", "gpt")):
            return _AG["3p"]
    return None


def handoffs(s, pool, tasks):
    """[{task, text, path}] for each running task in `tasks` whose worker (its newest agent) is on
    `pool`: the handover text, saved to handoffs/<slug>-<id>.md beside the
    store's file. No pool (None, as pool_of() says for an unknown one): none."""
    out = []
    for t in tasks if pool else []:
        a = (t.get("agents") or [None])[-1]
        if t.get("state") != "running" or not a or pool_of(a) != pool:
            continue
        text = handover(s, t, a["agent"], a["agent"], f"the {pool} pool reached its floor", agent_log(t, a.get("address")))
        folder = os.path.join(os.path.dirname(s.path), "handoffs")
        os.makedirs(folder, exist_ok=True)
        path = os.path.join(folder, s.key(t).replace("#", "-") + ".md")
        tmp = f"{path}.{os.getpid()}.tmp"
        with open(tmp, "w") as f:
            f.write(text + "\n")
        os.replace(tmp, path)
        out.append({"task": s.ref(t), "text": text, "path": path})
    return out


CONTEXT_CAP = 4096  # FAC-104: the work so far in a switched task's first message, at most this many characters


def work_context(s, t, prev):
    """What a switched task's new agent is told of the work so far, so nobody writes a handoff by hand: the
    branch's commits, git diff --stat, the worker's last status and summary, and the last of its chat (its
    session notes). At most CONTEXT_CAP characters; "" when there is nothing to say."""
    commits, _ = work_so_far(s, t)
    folder = t.get("worktree") or s.loc["folder"]
    since = (t.get("start") or t.get("base")) if t.get("worktree") else "HEAD"
    stat = git(folder, "diff", "--stat", since)[1] if since else ""
    parts = [("Commits since the base", commits[:1200]), ("git diff --stat", stat[-900:])]
    said = [x for x in ((t.get("progress") or {}).get("text"), t.get("summary")) if x]
    parts.append(("Its last status", " / ".join(said)[:400]))
    head = "<herdr-handoff>\nThe work so far (context, not new instructions):"
    used = len(head) + sum(len(k) + len(v) + 4 for k, v in parts) + 40
    log = agent_log(t, (prev or {}).get("address")) if prev else None
    parts.append(("Its last messages (latest last)", chat_tail(log, n=4, size=max(0, CONTEXT_CAP - used)) if log else ""))
    body = "\n\n".join(f"{k}:\n{v.strip()}" for k, v in parts if v and v.strip() and v.strip() != "none")
    if not body:
        return ""
    return (head + "\n\n" + body)[:CONTEXT_CAP - 20] + "\n</herdr-handoff>"
