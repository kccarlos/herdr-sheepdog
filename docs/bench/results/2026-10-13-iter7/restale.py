#!/usr/bin/env python3
"""Second look at the judge's stale-follow-up flags (STATUS.md: "every flagged case read"): one
blind Sonnet call over every flagged quote, shuffled, without the arm, scored against the
pre-registered rubric. Writes restale.json (quote id -> verdict) and prints per-arm counts."""
import json, glob, random, subprocess, collections
random.seed(7)
items = []
for arm in ("main", "fix"):
    for f in sorted(glob.glob(f"judge-{arm}-c*.jsonl")):
        for l in open(f):
            o = json.loads(l); v = o["verdict"]
            if v.get("stale_followup"):
                items.append({"arm": arm, "chunk": f, "scenario": o["scenario"], "repeat": o["repeat"], "kind": o["kind"], "quote": v.get("stale_quote", "")})
random.shuffle(items)
CTX = {"target": "The user asked the question once; it was answered later in the call.",
       "dropped": "The user dropped the question (\"never mind\", \"算了，那个不用说了\").",
       "reasked": "The user asked the question again and got the full answer then."}
lines = [f"{i+1}. [{CTX[it['kind']]}] Line near the end of the call: \"{it['quote']}\"" for i, it in enumerate(items)]
prompt = """A voice assistant reports on coding agents. Each item below is one line the assistant said late in a call, about an earlier question of the user's (why a test failed, or what an agent found). Classify each line against this rule:

STALE: the line tells the earlier question's finding again (what was wrong, what was found) when it was already settled, or comes back to the question itself after the user dropped it (including "about your earlier question…" or "as you said, I'll leave that question").
NOT STALE: an offer of a next step on an open item ("shall I have it fix that?"), or a reminder that an item is still open or waiting for the user's decision, without telling the finding again.

Reply with JSON only: {"verdicts": [{"item": 1, "stale": true}, ...]}

""" + "\n".join(lines)
res = subprocess.run(["claude", "-p", "--model", "sonnet", "--output-format", "text"], input=prompt, capture_output=True, text=True, timeout=300)
txt = res.stdout.strip(); s, e = txt.find("{"), txt.rfind("}")
v = {x["item"]: x["stale"] for x in json.loads(txt[s:e+1])["verdicts"]}
for i, it in enumerate(items): it["stale_rubric"] = v.get(i + 1)
json.dump(items, open("restale.json", "w"), ensure_ascii=False, indent=1)
c = collections.Counter((it["arm"], it["kind"], it["stale_rubric"]) for it in items)
for k in sorted(c, key=str): print(k, c[k])
