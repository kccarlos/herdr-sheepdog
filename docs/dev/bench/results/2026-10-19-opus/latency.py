#!/usr/bin/env python3
"""usage: latency.py ARM_DIR...   (each ARM_DIR holds events/*.jsonl and report.json)

Per arm, from the daemon events of every run: orchestrator turn latency
(brain.turn dur_ms) by turn kind, p50 / p90, split into the first turn of
each kind in a run (cold: the first request of that tool set, nothing
cached) and the rest; announce turns over the 6 s budget
(brain.announce_turn_timeout_s) and utterance turns over 25 s
(brain.utterance_timeout_s); fallbacks by reason; the daemon's own
sentence spoken instead of the orchestrator's (brain.push_decision
via=template, reason=timeout); prompt-cache hit rate (sum of
cache_hit_tokens / sum of prompt_tokens over brain.step); refusals,
dropped thinking blocks, rate limits; and the spend estimate of
report.json."""
import collections, glob, json, os, sys


def pct(xs, p):
    if not xs:
        return None
    xs = sorted(xs)
    k = (len(xs) - 1) * p / 100
    f = int(k)
    c = min(f + 1, len(xs) - 1)
    return xs[f] + (xs[c] - xs[f]) * (k - f)


def s(ms):
    return "–" if ms is None else "%.1f s" % (ms / 1000)


def arm(d):
    turns = collections.defaultdict(lambda: {"cold": [], "warm": []})
    fallbacks = collections.Counter()
    template_timeouts = 0
    ann_over, ann_n, utt_over, utt_n = 0, 0, 0, 0
    prompt = hit = 0
    refusals = dropped = ratelimited = step_errors = 0
    runs = 0
    for f in sorted(glob.glob(os.path.join(d, "events", "*.jsonl"))):
        runs += 1
        seen = set()
        for line in open(f):
            e = json.loads(line)
            t, p = e.get("type", ""), e.get("payload") or {}
            if t == "brain.turn":
                k = p.get("kind", "?")
                if p.get("fallback"):
                    continue  # a fallen-back turn's duration is its budget, counted below
                ms = p.get("dur_ms") or 0
                turns[k]["warm" if k in seen else "cold"].append(ms)
                seen.add(k)
                if k == "announce":
                    ann_n += 1
                    ann_over += ms > 6000
                if k == "utterance":
                    utt_n += 1
                    utt_over += ms > 25000
            elif t == "brain.fallback":
                fallbacks[(p.get("kind", "?"), p.get("reason", "?"))] += 1
            elif t == "brain.push_decision" and p.get("via") == "template" and p.get("reason") == "timeout":
                template_timeouts += 1
            elif t == "brain.step":
                if p.get("error"):
                    step_errors += 1
                    if "refus" in p["error"]:
                        refusals += 1
                else:
                    prompt += p.get("prompt_tokens") or 0
                    hit += p.get("cache_hit_tokens") or 0
            elif t == "brain.thinking_dropped":
                dropped += 1
            elif t == "brain.rate_limited":
                ratelimited += 1
    rep = json.load(open(os.path.join(d, "report.json")))
    # A merged result keeps the first chunk's spend: sum the chunks'.
    srcs = [os.path.join(c.strip(), "report.json") for c in rep["setup"].get("merged_from", "").split(",") if c.strip()]
    reps = [json.load(open(f)) for f in srcs] or [rep]
    brain_cost = sum(l["cost_usd_estimate"] for r in reps for l in r.get("spend", []) if l["role"] == "brain")
    return dict(runs=runs, turns=turns, fallbacks=fallbacks, template_timeouts=template_timeouts,
                ann_over=ann_over, ann_n=ann_n, utt_over=utt_over, utt_n=utt_n,
                hit_rate=hit / prompt if prompt else 0, prompt=prompt, hit=hit,
                refusals=refusals, dropped=dropped, ratelimited=ratelimited, step_errors=step_errors,
                brain_cost=brain_cost)


def main():
    for d in sys.argv[1:]:
        a = arm(d)
        print("## %s (%d runs)\n" % (d, a["runs"]))
        print("| Turn kind | n (first / rest) | p50 first | p50 rest | p90 rest | p50 all | p90 all |")
        print("|---|---|---|---|---|---|---|")
        for k in sorted(a["turns"]):
            c, w = a["turns"][k]["cold"], a["turns"][k]["warm"]
            print("| %s | %d / %d | %s | %s | %s | %s | %s |" % (k, len(c), len(w), s(pct(c, 50)), s(pct(w, 50)), s(pct(w, 90)), s(pct(c + w, 50)), s(pct(c + w, 90))))
        print()
        print("- announce turns over 6 s (answered, not fallen back): %d/%d (%.0f %%)" % (a["ann_over"], a["ann_n"], 100 * a["ann_over"] / max(a["ann_n"], 1)))
        print("- utterance turns over 25 s (answered): %d/%d" % (a["utt_over"], a["utt_n"]))
        print("- fallbacks by (kind, reason): %s" % dict(a["fallbacks"]))
        print("- daemon sentence spoken for a timed-out announce (push_decision via=template reason=timeout): %d" % a["template_timeouts"])
        print("- prompt-cache hit rate: %.1f %% (%d of %d prompt tokens)" % (100 * a["hit_rate"], a["hit"], a["prompt"]))
        print("- failed model calls: %d (refusals %d); thinking blocks dropped events: %d; rate limited: %d" % (a["step_errors"], a["refusals"], a["dropped"], a["ratelimited"]))
        print("- orchestrator spend estimate: $%.2f ($%.2f per run)\n" % (a["brain_cost"], a["brain_cost"] / max(a["runs"], 1)))


main()
