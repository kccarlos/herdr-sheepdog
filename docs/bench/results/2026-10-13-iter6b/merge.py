#!/usr/bin/env python3
"""usage: merge.py OUT CHUNK_DIR...  concatenates eval sim chunks into OUT/report.json + OUT/events,
renumbering repeats per scenario in chunk order (rejudge then rewrites report.json/md and the totals)."""
import json, os, shutil, sys
out, chunks = sys.argv[1], sys.argv[2:]
KEEP = int(os.environ.get("KEEP", "5"))  # iterations 6 and 7: the first 5 finished runs per scenario, chunks in order
os.makedirs(os.path.join(out, "events"), exist_ok=True)
base, runs, n = None, [], {}
for c in chunks:
    d = json.load(open(os.path.join(c, "report.json")))
    base = base or d
    for r in sorted(d["runs"], key=lambda r: (r["scenario"], r["repeat"])):
        if r["status"] == "not_run" or n.get(r["scenario"], 0) >= KEEP: continue
        k = n[r["scenario"]] = n.get(r["scenario"], 0) + 1
        src = os.path.join(c, "events", "%s.r%d.jsonl" % (r["scenario"], r["repeat"]))
        if os.path.exists(src):
            shutil.copy(src, os.path.join(out, "events", "%s.r%d.jsonl" % (r["scenario"], k)))
        r["repeat"] = k
        runs.append(r)
base["runs"] = runs
base["repeat"] = max(n.values())
base["setup"]["merged_from"] = ", ".join(chunks)
json.dump(base, open(os.path.join(out, "report.json"), "w"), indent=2, ensure_ascii=False)
print(out, {k: v for k, v in n.items()})
