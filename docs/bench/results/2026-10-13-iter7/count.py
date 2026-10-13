#!/usr/bin/env python3
"""How often does a user question get no real answer? Counts the three
cases of the CEO's note in event logs (bench runs and the live daemon):
(a) the orchestrator turn on the words fell back (timeout / error), and
    no retry of the same words answered it;
(b) the answer was cut off by the user's barge-in: a flushed playback run
    cut short whose start was tagged with the utterance it answers, not a
    flush for an urgent push (under_half: less than half of it played; an
    analysis split only);
(c) the answer was never voiced: an answer briefing found unvoiced, an
    answer push judged unheard and not re-queued, an answer push dropped,
    or a late reply dropped as stale without having acted.
Usage: count.py DIR... (each DIR searched for *.jsonl event logs)."""
import sys, json, glob, os, hashlib, collections

def events(path):
    out = []
    for l in open(path, errors="replace"):
        try:
            out.append(json.loads(l))
        except Exception:
            pass
    return out

def ts(e):
    if "at_ms" in e:
        return e["at_ms"] / 1000.0
    return e.get("id", 0) / 1e6

def count_file(evs):
    r = collections.Counter()
    utts = {}
    retry_ok = set()
    fb = {}
    for e in evs:
        t, p = e.get("type"), e.get("payload") or {}
        if t in ("supervisor.relay", "supervisor.relay_by_daemon"):
            if p.get("retry_of"):
                r["retries"] += 1
                # retried: did its turn answer?
                utts[p.get("utterance_id")] = ("retry", p.get("retry_of"))
            else:
                r["utterances"] += 1
                utts[p.get("utterance_id")] = ("new", None)
        if t == "brain.fallback" and p.get("kind") == "utterance":
            fb[p.get("utterance_id") or e.get("utterance_id") or ("noid", ts(e))] = p.get("reason")
        if t == "brain.turn" and p.get("kind") == "utterance" and not p.get("fallback"):
            u = e.get("utterance_id") or p.get("utterance_id")
            if u in utts and utts[u][0] == "retry":
                retry_ok.add(utts[u][1])
    for u, reason in fb.items():
        if utts.get(u, ("new",))[0] == "retry":
            continue
        if u in retry_ok:
            r["a_fallback_retried_ok"] += 1
            continue
        if reason == "cancelled":
            r["a_cancelled_not_counted"] += 1
            continue
        r["a_fallback"] += 1
        r["a_fallback_" + str(reason)] += 1
    # (b) cut answers: a flushed run cut short whose start was tagged
    # with the utterance it answers (PlaybackUtterance), not a flush for
    # an urgent push (within 3 s of voice.interrupted {by: push}).
    run_utt = None
    last_push_flush = -1e9
    for e in evs:
        t, p = e.get("type"), e.get("payload") or {}
        if t == "voice.interrupted" and p.get("by") == "push":
            last_push_flush = ts(e)
        elif t == "voice.playback" and p.get("state") == "started":
            run_utt = p.get("utterance_id")
        elif t == "voice.playback" and p.get("state") == "flushed" and p.get("cut"):
            if not run_utt:
                r["b_cut_other_line"] += 1
                continue
            if ts(e) - last_push_flush < 3:
                r["b_cut_answer_by_push"] += 1
                continue
            run, played = p.get("run_ms") or 0, p.get("played_ms") or 0
            r["b_cut_answer"] += 1
            if run > 0 and played < 0.5 * run:
                r["b_cut_answer_under_half"] += 1
            run_utt = None
    # (c) never voiced
    for e in evs:
        t, p = e.get("type"), e.get("payload") or {}
        if t == "voice.briefing_unvoiced" and p.get("kind") == "answer":
            r["c_unvoiced_answer"] += 1
        elif t == "voice.push_unheard" and p.get("answers_utterance") and not p.get("requeue"):
            r["c_unheard_answer_" + str(p.get("reason"))] += 1
        elif t == "voice.push_dropped" and p.get("answers_utterance"):
            r["c_dropped_answer_" + str(p.get("reason"))] += 1
        elif t == "brain.reply_stale" and not p.get("acted"):
            r["c_stale_dropped"] += 1
    r["files"] = 1
    r["voice"] = 1 if any(e.get("type") == "voice.tool_result" for e in evs) else 0
    return r

def main():
    seen = set()
    tot = {"tier1": collections.Counter(), "tier2": collections.Counter()}
    for d in sys.argv[1:]:
        files = [d] if d.endswith(".jsonl") else glob.glob(os.path.join(d, "**", "*.jsonl"), recursive=True)
        for f in sorted(files):
            try:
                with open(f, "rb") as fh:
                    head = fh.read(4096)
            except Exception:
                continue
            h = hashlib.sha1(head).hexdigest()
            if h in seen:
                continue
            seen.add(h)
            evs = events(f)
            if not any(e.get("type") in ("supervisor.relay", "brain.turn") for e in evs):
                continue
            r = count_file(evs)
            tot["tier2" if r["voice"] else "tier1"].update(r)
    for k, c in tot.items():
        print("==", k)
        for kk in sorted(c):
            print(f"  {kk}: {c[kk]}")

main()
