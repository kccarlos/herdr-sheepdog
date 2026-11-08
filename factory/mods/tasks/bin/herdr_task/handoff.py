"""At the floor (docs/factory.md section 7): the handoff of each running task whose worker is on a
usage pool that reached its floor, written from git and its chat by takeover's handover(), and kept
in a file per task under the store's folder. Only the writer: interrupting, the tick and a command
come with the gate (G4)."""

import os

from .agents import agent_log
from .settings import argv_model
from .takeover import handover
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
