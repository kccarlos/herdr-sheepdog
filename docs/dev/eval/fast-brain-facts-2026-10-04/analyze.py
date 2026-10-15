#!/usr/bin/env python3
"""A/B analysis for the fast-brain prompt (old vs new), tier 2.

Per CEO line (from the run record's user_lines): was it handed off (a
to_supervisor call between the end of the line and the next line) or
self-answered (bot speech, no call)? Answer latency: end of the CEO's
line to the first bot speech of the answer (self) or the first bot
speech after the call's tool_result (hand-off). Self-answers are graded
for grounding by an LLM (Vertex gemini-3.5-flash) against what the
voice had been told so far (pushes and tool results).
"""
import glob, json, os, re, statistics, sys, urllib.request

AB = os.path.dirname(os.path.abspath(__file__))
GRADE = os.environ.get("GRADE") == "1"
KEY = os.environ.get("VERTEX_API_KEY", "")
MODEL = "gemini-3.5-flash"
CACHE = os.path.join(AB, "grade-cache.json")
cache = json.load(open(CACHE)) if os.path.exists(CACHE) else {}


def grade(context, line, answer):
    k = json.dumps([context[-6000:], line, answer], ensure_ascii=False)
    if k in cache:
        return cache[k]
    if not GRADE or not KEY:
        return None
    prompt = (
        "A voice assistant supervises coding agents. It may only state facts about agents that it was told "
        "in the briefings below (or said earlier itself, based on them). The CEO said a line and the assistant "
        "answered by itself, without asking its back end.\n\nBRIEFINGS AND EARLIER CONTEXT (oldest first):\n"
        + context[-6000:]
        + "\n\nCEO: " + line + "\nASSISTANT: " + answer
        + "\n\nIs every factual claim the assistant made about agents, their work, numbers, or the earlier "
        "conversation supported by the context above and not contradicted by it? Rephrasing, word meanings, "
        "small talk, 'let me check' and asking a clarifying question count as supported. Reply with one word: "
        "YES or NO, then a short reason."
    )
    body = json.dumps({"contents": [{"role": "user", "parts": [{"text": prompt}]}],
                       "generationConfig": {"temperature": 0}}).encode()
    url = f"https://aiplatform.googleapis.com/v1/publishers/google/models/{MODEL}:generateContent?key={KEY}"
    for _ in range(4):
        try:
            req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=60) as r:
                out = json.load(r)
            txt = "".join(p.get("text", "") for p in out["candidates"][0]["content"]["parts"])
            ok = txt.strip().upper().startswith("YES")
            cache[k] = {"ok": ok, "why": txt.strip()[:300]}
            return cache[k]
        except Exception as e:  # noqa
            err = str(e)
    return None


def load_events(path):
    ev = []
    for l in open(path):
        e = json.loads(l)
        if e.get("at_ms", -1) >= 0:
            ev.append(e)
    return ev


def analyse_run(run_path, events_path):
    run = json.load(open(run_path))
    ev = load_events(events_path) if os.path.exists(events_path) else []
    lines = run.get("user_lines") or []
    out = []
    for i, ul in enumerate(lines):
        end = ul["at_ms"] + ul.get("dur_ms", 0)
        nxt = lines[i + 1]["at_ms"] if i + 1 < len(lines) else 10**12
        win = [e for e in ev if end - 1500 <= e["at_ms"] < nxt]
        calls = [e for e in win if e["type"] == "voice.tool_call" and e["payload"].get("name") == "to_supervisor"]
        outs = [e for e in win if e["type"] == "voice.transcript.out" and e["at_ms"] >= end - 500]
        kind = "none"
        lat = None
        answer = ""
        if calls:
            kind = "handoff"
            cid = calls[0]["payload"].get("id")
            res = [e for e in win if e["type"] == "voice.tool_result" and e["payload"].get("id") == cid]
            t0 = res[0]["at_ms"] if res else calls[0]["at_ms"]
            after = [e for e in outs if e["at_ms"] >= t0]
            if after:
                lat = after[0]["at_ms"] - end
        elif outs:
            kind = "self"
            lat = outs[0]["at_ms"] - end
            # the answer: output transcript until a gap of 3 s
            t = outs[0]["at_ms"]
            for e in outs:
                if e["at_ms"] - t > 3000:
                    break
                answer += e["payload"].get("text", "")
                t = e["at_ms"]
        ctx = []
        for e in ev:
            if e["at_ms"] >= end:
                break
            p = e["payload"]
            if e["type"] == "voice.push":
                ctx.append(f"[briefing {p.get('scheduling_sent','')}] {p.get('text','')}")
            elif e["type"] == "voice.tool_result" and p.get("name") == "to_supervisor":
                ctx.append(f"[answer to its question] {p.get('spoken','')} {p.get('detail','')}")
            elif e["type"] == "voice.transcript.in":
                ctx.append(f"[CEO] {p.get('text','')}")
            elif e["type"] == "voice.transcript.out":
                if ctx and ctx[-1].startswith("[assistant]"):
                    ctx[-1] += p.get("text", "")
                else:
                    ctx.append("[assistant] " + p.get("text", ""))
        g = grade("\n".join(ctx), ul["said"], answer.strip()) if kind == "self" and answer.strip() else None
        out.append({"line": ul["said"], "kind": kind, "lat": lat, "answer": answer.strip(), "grade": g})
    return run, out


def arm_files(prefix):
    runs = []
    for d in sorted(glob.glob(os.path.join(AB, prefix + "*"))):
        if not os.path.isdir(d):
            continue
        for rp in sorted(glob.glob(os.path.join(d, "runs", "*.json"))):
            base = os.path.basename(rp)[:-5]
            runs.append((d, rp, os.path.join(d, "events", base + ".jsonl")))
    return runs


def pct(a, b):
    return f"{a}/{b} ({100*a/b:.0f}%)" if b else "0/0"


def p50(xs):
    xs = [x for x in xs if x is not None]
    return f"{statistics.median(xs)/1000:.2f} s" if xs else "-"


def summarise(prefix, label, detail=False):
    rows = arm_files(prefix)
    passed = total = errored = 0
    kinds = {"self": 0, "handoff": 0, "none": 0}
    lat_all, lat_self, lat_hand = [], [], []
    graded = ok = 0
    turn_lat = []
    byscen = {}
    timeouts = 0
    details = []
    for d, rp, ep in rows:
        run, lines = analyse_run(rp, ep)
        st = run["status"]
        if st in ("error", "errored", "not_run"):
            errored += 1
            continue
        total += 1
        passed += st == "pass"
        byscen.setdefault(run["scenario"], [0, 0])
        byscen[run["scenario"]][1] += 1
        byscen[run["scenario"]][0] += st == "pass"
        for l in run.get("latency") or []:
            if l.get("total_ms") is not None:
                turn_lat.append(l["total_ms"])
        if os.path.exists(ep):
            timeouts += sum(1 for x in open(ep) if '"brain.tool_timeout"' in x)
        for l in lines:
            kinds[l["kind"]] += 1
            if l["lat"] is not None:
                lat_all.append(l["lat"])
                (lat_self if l["kind"] == "self" else lat_hand).append(l["lat"])
            if l["grade"] is not None:
                graded += 1
                ok += l["grade"]["ok"]
                if not l["grade"]["ok"]:
                    details.append((run["scenario"], run["repeat"], l["line"], l["answer"], l["grade"]["why"]))
    n = sum(kinds.values())
    res = {
        "label": label, "runs": total, "pass": passed, "errored": errored,
        "lines": n, "handoff": kinds["handoff"], "self": kinds["self"], "none": kinds["none"],
        "self_graded": graded, "self_ok": ok,
        "lat_p50_all": p50(lat_all), "lat_p50_self": p50(lat_self), "lat_p50_handoff": p50(lat_hand),
        "turn_latency_p50": p50(turn_lat), "tool_timeouts": timeouts, "byscen": byscen,
    }
    print(f"\n## {label}")
    print(f"runs {total} (errored {errored}); pass {pct(passed, total)}")
    print(f"CEO lines {n}: handed off {pct(kinds['handoff'], n)}, self-answered {pct(kinds['self'], n)}, no answer {kinds['none']}")
    print(f"self-answers grounded: {pct(ok, graded)}")
    print(f"answer latency p50 (end of CEO line -> first speech of the answer): all {res['lat_p50_all']}, self {res['lat_p50_self']}, hand-off {res['lat_p50_handoff']}; daemon turn.latency total p50 {res['turn_latency_p50']}")
    print(f"brain.tool_timeout events: {timeouts}")
    for s, (a, b) in sorted(byscen.items()):
        print(f"  {s}: {a}/{b}")
    if detail:
        for x in details:
            print("  UNGROUNDED", x)
    return res


if __name__ == "__main__":
    out = {}
    for prefix, label in [("facts-old", "facts cases, old prompt (v4)"), ("facts-new", "facts cases, new prompt (v5)"),
                          ("suite-old", "existing suite, old prompt (v4)"), ("suite-new", "existing suite, new prompt (v5)"), ("v51-facts", "facts cases, prompt v5.1 (language line restored)"), ("v51-suite", "existing suite, prompt v5.1")]:
        out[prefix] = summarise(prefix, label, detail="-v" in sys.argv)
    json.dump(cache, open(CACHE, "w"), ensure_ascii=False, indent=0)
    json.dump(out, open(os.path.join(AB, "summary.json"), "w"), ensure_ascii=False, indent=1)
