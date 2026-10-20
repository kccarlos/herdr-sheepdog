#!/usr/bin/env python3
"""usage: split.py   splits main/merged and cards/merged into the runs whose cards-arm screen was
visible and those where it was hidden (the same scenario and repeat in both arms: the persona rotates
by repeat), as subset/{main,cards}-{visible,hidden}/ with report.json and events, for northstar --vs."""
import json, os, shutil
here = os.path.dirname(os.path.abspath(__file__))
cards = json.load(open(os.path.join(here, "cards/merged/report.json")))
screen = {(r["scenario"], r["repeat"]): r.get("screen", "") for r in cards["runs"]}
for arm in ("main", "cards"):
    d = json.load(open(os.path.join(here, arm, "merged/report.json")))
    for s in ("visible", "hidden"):
        out = os.path.join(here, "subset", f"{arm}-{s}")
        os.makedirs(os.path.join(out, "events"), exist_ok=True)
        sub = dict(d)
        sub["runs"] = [r for r in d["runs"] if screen.get((r["scenario"], r["repeat"])) == s]
        for r in sub["runs"]:
            src = os.path.join(here, arm, "merged/events", "%s.r%d.jsonl" % (r["scenario"], r["repeat"]))
            if os.path.exists(src):
                shutil.copy(src, os.path.join(out, "events"))
        json.dump(sub, open(os.path.join(out, "report.json"), "w"), indent=2, ensure_ascii=False)
        print(out, len(sub["runs"]))
