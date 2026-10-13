#!/usr/bin/env python3
"""Metric B of iteration 6: are the facts in each answer right? One Sonnet call per run
(`claude -p --model sonnet`), blind to the arm and to the tool calls.
usage: factjudge.py SCENARIO_FILE OUT.jsonl RUN_DIR [RUN_DIR...]   (resumes: skips runs already in OUT)
For every user line of an explain scenario the judge gets the world facts (screens, the
scripted investigation answer) and the conversation, and returns per answer:
  wrong: claims about the worker's work that the facts contradict or do not hold
         (invented numbers, causes, observations, steps); analogies marked as such are not claims.
  new_facts: how many facts from the world this answer states that no earlier answer stated.
"""
import json, sys, os, subprocess, glob
SCREENS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "../../../../testdata/eval/sim/screens")
PROMPT = """You check a voice assistant's answers for factual accuracy. The assistant reports on coding agents ("workers") to a user who cannot see their screens. Below are WORLD FACTS (everything that is true: the workers' screens and what an investigation would find) and the CONVERSATION (user lines numbered, each followed by the assistant's answer).

For each numbered user line, judge the assistant's answer to it:
- "wrong": a list of short quotes from the answer that state something about the workers or their work that the world facts contradict or do not contain: an invented or changed number, cause, observation, step taken, outcome, or claim about what a worker saw or did. An analogy or illustration clearly framed as one ("think of a coffee counter") is not a claim. General knowledge that explains a term is not a claim. A paraphrase, a summary or a plain inference that follows from the facts ("just started" for a task running for under three minutes) is not wrong. A hypothetical example clearly framed as made up ("say you told it…") is not a claim; an example presented as something that happened is a claim. Rounding that keeps the meaning ("about one in twelve") is fine; a changed value ("a quarter of an hour" for 25 minutes) is wrong. Empty list when none.
- "new_facts": the number of distinct facts from the world facts that this answer states and no earlier answer in the conversation stated.
- "restates": true when the answer mostly repeats earlier answers in other words.

Reply with JSON only: {"answers": [{"line": 1, "wrong": [...], "new_facts": 0, "restates": false}, ...]} with one entry per numbered user line.

WORLD FACTS:
<<<
%s
>>>

CONVERSATION:
<<<
%s
>>>
"""
def world(spec, run):
    """The full world: every agent's whole screen and scripted investigation answer, then the
    run's own notes (scripted changes during the call). The run's facts field shows only the
    last lines of each screen, which made the judge flag true facts from the screen's top."""
    out = []
    for a in spec["agents"]:
        out.append(f"Worker {a['alias']} ({a.get('kind', 'codex')}), state {a.get('state', '')}.")
        if a.get("answer"): out.append("  What an investigation of it finds (a read-only look at its repository): " + a["answer"])
        if a.get("screen"): out.append("  Its whole screen:\n" + open(os.path.join(SCREENS, a["screen"])).read())
    f = run.get("facts", ""); i = f.find("Notes:")
    if i >= 0: out.append(f[i:])
    return "\n".join(out)

def convo(run):
    out, n = [], 0
    for t in run["transcript"]:
        if t["who"] == "ceo":
            n += 1; out.append(f"\n[{n}] USER: {t['text']}")
        elif t["who"] in ("sheepdog", "push"):
            out.append(f"ASSISTANT: {t['text']}")
    return "\n".join(out), n

def main():
    specs = {s["id"]: s for s in json.load(open(sys.argv[1]))["scenarios"] if s.get("explain")}
    outp = sys.argv[2]
    done = set()
    if os.path.exists(outp):
        for l in open(outp): o = json.loads(l); done.add((o["dir"], o["scenario"], o["repeat"]))
    for rd in sys.argv[3:]:
        r = json.load(open(os.path.join(rd, "report.json")))
        for run in r["runs"]:
            key = (rd, run["scenario"], run["repeat"])
            if run["scenario"] not in specs or key in done: continue
            c, n = convo(run)
            p = PROMPT % (world(specs[run["scenario"]], run), c)
            res = subprocess.run(["claude", "-p", "--model", "sonnet", "--output-format", "text"], input=p, capture_output=True, text=True, timeout=300)
            txt = res.stdout.strip()
            s, e = txt.find("{"), txt.rfind("}")
            try: v = json.loads(txt[s:e+1])
            except Exception: v = {"error": txt[:500]}
            with open(outp, "a") as f:
                f.write(json.dumps({"dir": rd, "scenario": run["scenario"], "repeat": run["repeat"], "lines": n, "verdict": v}, ensure_ascii=False) + "\n")
            print(rd, run["scenario"], run["repeat"], "ok" if "answers" in v else "ERR", flush=True)
main()
