#!/usr/bin/env python3
"""Merge several tier-1 eval sim result directories of one arm into one.

Runs that were not run, or errored because the campaign was interrupted
(SIGINT while the DeepSeek balance ran out), are dropped; the rest are
renumbered 1..n per scenario in input order, and their event logs are
copied under the new numbers. The output is the input of `sheepdog bench
rejudge` and `sheepdog bench northstar`.

usage: merge.py OUT_DIR IN_DIR [IN_DIR ...]
"""
import json, os, shutil, sys

out, ins = sys.argv[1], sys.argv[2:]
os.makedirs(os.path.join(out, "events"), exist_ok=True)
base, runs, n = None, [], {}
for d in ins:
    rep = json.load(open(os.path.join(d, "report.json")))
    base = base or rep
    for r in rep["runs"]:
        if r["status"] == "not_run":
            continue
        if "context canceled" in json.dumps(r.get("failures")):
            continue  # interrupted mid-run (SIGINT): not a result
        src = os.path.join(d, "events", f"{r['scenario']}.r{r['repeat']}.jsonl")
        n[r["scenario"]] = n.get(r["scenario"], 0) + 1
        r["repeat"] = n[r["scenario"]]
        runs.append(r)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(out, "events", f"{r['scenario']}.r{r['repeat']}.jsonl"))
base["runs"] = sorted(runs, key=lambda r: (r["scenario"], r["repeat"]))
base.pop("north_star", None)
json.dump(base, open(os.path.join(out, "report.json"), "w"), indent=1, ensure_ascii=False)
print(out, len(runs), "runs")
