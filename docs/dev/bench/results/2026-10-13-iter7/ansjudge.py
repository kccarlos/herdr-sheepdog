#!/usr/bin/env python3
"""Iteration 7 judge: did sheepdog come back to a question that got no real answer, and did it
add stale follow-ups? One blind Sonnet call per run (`claude -p --model sonnet`): the arm is not
shown, nor any tool call or daemon fact.
usage: ansjudge.py OUT.jsonl RUN_DIR [RUN_DIR...]   (resumes: skips runs already in OUT)
"""
import json, sys, os, subprocess

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.join(HERE, "../../../..")
SCREENS = os.path.join(ROOT, "testdata/eval/sim/screens")
SPECS = {s["id"]: s for s in json.load(open(os.path.join(ROOT, "testdata/eval/sim/unanswered.json")))["scenarios"]}

# The world's answer to each faulted question: the agent it is about, and the core finding.
CORE = {
    "A": ("bench-codex", "bench-codex-test-fail.txt",
          "one test (TestAdd) fails because the add function is wrong: Add(2, 3) returns 3 instead of 5, it returns b and ignores a (calc.go line 8); its earlier 'all tests passed' covered only one package"),
    "C": ("bench-claude", "bench-claude-done.txt",
          "while writing the README it found two broken (dead) links in CONTRIBUTING.md, both pointing to a docs/ folder that does not exist; it left them alone, and whether to fix or delete them is the user's decision"),
}

def world_of(sc):
    return "A" if sc["agents"][0]["screen"] == "bench-codex-test-fail.txt" else "C"

def kind_of(sid):
    if sid.startswith("ua-dropped"):
        return "dropped"
    if sid.startswith("ua-reasked"):
        return "reasked"
    return "target"

PROMPT = """You review a recorded conversation between a user and a voice assistant that reports on coding agents ("workers"). The user cannot see any screen; they hear only the ASSISTANT lines (replies to them, and lines the assistant pushed on its own, marked PUSH).

Early on, the user asked a question that got no real answer: the assistant's turn failed and it said it could not answer, or the user talked over the answer and heard only its first words, or the answer was never said aloud. The QUESTION, what the user HEARD of its answer, and the world's ANSWER to it are given below.

Judge the assistant lines AFTER that first exchange (from user line [2] on):

1. "answered_later": true when some later assistant line gives the user the substance of the ANSWER to the QUESTION, at least its core finding (a passing mention of the topic without the finding, such as "the add bug" or "those links", is not enough; the finding is what was wrong, or what was found). %s
2. "first_line": the user line number [n] after which it was first given (0 when never), and "quote": a short quote of it.
3. "stale_followup": true when a later assistant line, unprompted, comes back to the QUESTION or tells its answer again when it should not: %s An offer of a next step on an open item ("shall I have it fix that?") or a one-word mention inside a status list is not a stale follow-up; telling the finding again, or "about your earlier question" after it was settled, is. "stale_quote": a short quote (empty when none).

Reply with JSON only: {"answered_later": false, "first_line": 0, "quote": "", "stale_followup": false, "stale_quote": ""}

QUESTION (user line [1]): %s
WHAT THE USER HEARD OF ITS ANSWER: %s

ANSWER (the world's truth; the worker's whole screen follows):
%s
Worker %s's screen:
<<<
%s
>>>

CONVERSATION:
<<<
%s
>>>
"""

RULES = {
    "target": ("The user never asks it again.",
               "here the user never dropped it, so a stale follow-up is only telling the same finding a second time after it was already given in full in a later line."),
    "dropped": ("(The user dropped the question in line [2]; report whether the finding was told anyway.)",
                "here the user dropped the question in line [2] (\"never mind\"), so telling its finding or returning to it at any later point is a stale follow-up."),
    "reasked": ("(The user asked it again in line [2]; the answer to line [2] counts here.)",
                "here the user asked again in line [2] and the reply to it gave the answer, so telling the finding again later, unprompted, is a stale follow-up."),
}

def convo(run):
    out, n = [], 0
    for t in run["transcript"]:
        if t["who"] == "ceo":
            n += 1
            out.append(f"\n[{n}] USER: {t['text']}")
        elif t["who"] == "sheepdog":
            out.append(f"ASSISTANT: {t['text'] or '(nothing was said)'}")
        elif t["who"] == "push":
            out.append(f"ASSISTANT (PUSH): {t['text']}")
    return "\n".join(out)

def main():
    outp = sys.argv[1]
    done = set()
    if os.path.exists(outp):
        for l in open(outp):
            o = json.loads(l)
            done.add((o["dir"], o["scenario"], o["repeat"]))
    for rd in sys.argv[2:]:
        r = json.load(open(os.path.join(rd, "report.json")))
        for run in r["runs"]:
            sid = run["scenario"]
            key = (rd, sid, run["repeat"])
            if sid not in SPECS or key in done or not run.get("transcript"):
                continue
            sc = SPECS[sid]
            k = kind_of(sid)
            agent, screen, core = CORE[world_of(sc)]
            hit = (run.get("faults") or [{}])[0]
            heard = hit.get("heard") or "(nothing)"
            p = PROMPT % (RULES[k][0], RULES[k][1], sc["script"][0], heard, core, agent,
                          open(os.path.join(SCREENS, screen)).read(), convo(run))
            res = subprocess.run(["claude", "-p", "--model", "sonnet", "--output-format", "text"],
                                 input=p, capture_output=True, text=True, timeout=300)
            txt = res.stdout.strip()
            s, e = txt.find("{"), txt.rfind("}")
            try:
                v = json.loads(txt[s:e + 1])
            except Exception:
                v = {"error": txt[:500]}
            with open(outp, "a") as f:
                f.write(json.dumps({"dir": rd, "scenario": sid, "repeat": run["repeat"], "kind": k,
                                    "verdict": v}, ensure_ascii=False) + "\n")
            print(rd, sid, run["repeat"], "ok" if "answered_later" in v else "ERR", flush=True)

main()
