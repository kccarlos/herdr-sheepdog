#!/usr/bin/env python3
"""Merge an arm's chunks r1..r3 into ARM/merged (runs/, events/), renumbering
each chunk's single repeat to its chunk number. usage: merge.py ARM"""
import json, os, re, shutil, sys

arm = sys.argv[1]
base = os.path.join(os.path.dirname(os.path.abspath(__file__)), arm)
out = os.path.join(base, 'merged')
for d in ('runs', 'events'):
    os.makedirs(os.path.join(out, d), exist_ok=True)
n = 0
for k in (1, 2, 3):
    src = os.path.join(base, 'r%d' % k)
    if not os.path.isdir(os.path.join(src, 'runs')):
        continue
    for f in sorted(os.listdir(os.path.join(src, 'runs'))):
        m = re.match(r'(.+)\.r1\.json$', f)
        if not m:
            continue
        rec = json.load(open(os.path.join(src, 'runs', f)))
        rec['repeat'] = k
        with open(os.path.join(out, 'runs', '%s.r%d.json' % (m.group(1), k)), 'w') as w:
            json.dump(rec, w, ensure_ascii=False, indent=1)
        ev = os.path.join(src, 'events', '%s.r1.jsonl' % m.group(1))
        if os.path.exists(ev):
            shutil.copy(ev, os.path.join(out, 'events', '%s.r%d.jsonl' % (m.group(1), k)))
        n += 1
print('%s: %d runs merged into %s' % (arm, n, out))
