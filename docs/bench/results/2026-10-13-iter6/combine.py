#!/usr/bin/env python3
"""Join metric A (fresh look per turn, from events) with metric B (fact judge).
usage: combine.py SCENARIO_FILE FACTS.jsonl RUN_DIR [RUN_DIR...]"""
import json, sys, os, glob, math, importlib.util
spec = importlib.util.spec_from_file_location("looks", os.path.join(os.path.dirname(__file__), "looks.py"))
sys_argv = sys.argv; sys.argv = [sys.argv[0], sys.argv[1]]  # looks.main() runs on import; give it no dirs
L = importlib.util.module_from_spec(spec); spec.loader.exec_module(L); sys.argv = sys_argv
specs = L.load_specs(sys.argv[1])
fj = {}
for l in open(sys.argv[2]):
    o = json.loads(l); fj[(os.path.normpath(o["dir"]), o["scenario"], o["repeat"])] = o["verdict"].get("answers", [])
rows = []
for rd in sys.argv[3:]:
    for ev in sorted(glob.glob(os.path.join(rd, "events", "*.jsonl"))):
        base = os.path.basename(ev); sid, rep = base.split(".r")[0], int(base.split(".r")[1].split(".")[0])
        if sid not in specs: continue
        kinds = specs[sid]["explain"]["turns"]; ts = L.turns_of(ev)
        ans = {a.get("line"): a for a in fj.get((os.path.normpath(rd), sid, rep), [])}
        for i, k in enumerate(kinds):
            if i >= len(ts) or (i + 1) not in ans: continue
            a = ans[i + 1]; look = any(c[0] in L.LOOK for c in ts[i]["calls"])
            rows.append(dict(sid=sid, rep=rep, i=i, kind=k, look=look, wrong=len(a.get("wrong", [])) > 0,
                             nwrong=len(a.get("wrong", [])), new=a.get("new_facts", 0), restates=bool(a.get("restates")), quotes=a.get("wrong", [])))
def rate(rs, f):
    k = sum(1 for r in rs if f(r)); lo, hi = L.wilson(k, len(rs)); return f"{k}/{len(rs)} = {k/max(1,len(rs)):.0%} ({lo:.0%}-{hi:.0%})"
det = [r for r in rows if r["kind"] in L.DETAIL]
print("detail answers:", len(det))
print("  no fresh look          ", rate(det, lambda r: not r["look"]))
print("  any wrong fact         ", rate(det, lambda r: r["wrong"]))
print("  restates               ", rate(det, lambda r: r["restates"]))
print("  mean new facts          %.2f" % (sum(r["new"] for r in det) / max(1, len(det))))
for lab, sub in (("with a look", [r for r in det if r["look"]]), ("without a look", [r for r in det if not r["look"]])):
    print(f"  {lab:15s} wrong {rate(sub, lambda r: r['wrong'])}; restates {rate(sub, lambda r: r['restates'])}; mean new facts {sum(r['new'] for r in sub)/max(1,len(sub)):.2f}")
first = [r for r in rows if r["kind"] == "explain"]
print("first explain answers: wrong", rate(first, lambda r: r["wrong"]), "mean new %.2f" % (sum(r["new"] for r in first)/max(1,len(first))))
if os.environ.get("ROWS"):
    for r in rows:
        if r["quotes"]: print(r["sid"], r["rep"], r["i"], r["kind"], "LOOK" if r["look"] else "none", r["quotes"])
