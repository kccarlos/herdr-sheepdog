# Explanation evals: baseline (arm A) on main, 2026-10-05

Step 1 of "Next: unified roles, explaining in detail, explanation evals"
in [handover-2026-10-04.md](../handover-2026-10-04.md). The CEO's
complaint from the morning phone test: the voice feels lazy. In the
events of 2026-10-05 18:02–18:05Z the orchestrator's `spoken` had four
sentences with reasons and an example, the voice said two, and on
"你可以给我一点详细的解释吗" it only rephrased without asking the
orchestrator. This page measures that on current main, before any prompt
change, so the A/B of step 3 has a baseline.

Raw reports: [`docs/dev/bench/results/2026-10-05-explain/`](../bench/results/2026-10-05-explain/)
(`t1/` and `t2/`: `report.md`, `report.json`, tier-2 `spend.log`).
Per-scenario table: [`explain-baseline-2026-10-05/`](explain-baseline-2026-10-05/)
(`aggregate.py` and its output).

## What is measured

**Category `explain`**: six fixed-script scenarios, tier 1
(`testdata/eval/sim/explain.json`) and tier 2
(`testdata/bench/scenarios/explain.json`). Each turn is labelled, and
each world has a worker screen with real substance (numbers, a cause, an
example, a consequence, options and their cost) plus a deeper
investigation answer for a hand-off.

| Scenario | Lang | Turns | World |
|---|---|---|---|
| explain-zh-input-rules | zh | explain, 然后呢, 你可以给我一点详细的解释吗, 为什么, 举个例子 | the evidence session: hard-coded word lists in the input layer (1,500 chars, three lists that disagree, the "好的继续，不过先跑测试" case, 1.2 s cost) |
| explain-en-flaky-test | en | explain, then, more detail please, why, example | a flaky checkout test (1 in 12, cache warm-up race, 15 % discount missed, two blocked deploys, a 3-line fix) |
| explain-zh-notes-ledger | zh | explain, why, example, then | the notes ledger (5-note cap, 2 stale notes, the next note pushes out "ask before pushing to main") |
| explain-en-invoice-job | en | explain, why, example, more | a failed invoice job (4,200 invoices vs 100 calls/min, 4,100 unsent before 9:00, bulk vs retry fix) |
| explain-control-en-done | en | 2 simple | "Is bench-codex done?", "Did the tests pass?" |
| explain-control-zh-busy | zh | 2 simple | "bench-claude 现在在忙吗？", "bench-codex 跑完了吗？" |

**Rubric** (`internal/eval/explain.go`, the existing judge on Vertex
`gemini-3.1-flash-lite`, one yes/no call per item). Two sides are scored
separately:

- *orchestrator*: per question, the `spoken` and `detail` (and options)
  of every `voice.tool_result`, plus spoken pushes (silent focus/context
  pushes are left out);
- *voice*: what the voice model said and the phone played (tier 2 only;
  tier 1 has no voice model).

| Item | How |
|---|---|
| (a) coverage | each scenario's 6–7 key points, asked per point and answer; *coverage* = share of key points the side stated anywhere in the call; *of orchestrator* = of the points the orchestrator gave the voice, the share the voice said |
| (b) ear comprehension | three calls per side over the side's answers: could a listener who cannot see the screen restate what it is / why it matters / what can be done (share of yes) |
| (c) follow-up depth | on then / more / why / example: new content, "that's all I have" with an offer, or a hand-off is yes; rephrasing only is no. The orchestrator is judged only on questions it was asked; the voice on every one. A no is a `judge_no` failure |
| (d) no padding | on the controls: the direct answer and at most one short extra sentence. The orchestrator is judged on `spoken` only (its `detail` is background for the voice). A no is a `judge_no` failure |

Words per answer count Han characters as words. The default rubric
still runs; the explain scenarios skip `ear_friendly` (its "short by
default" contradicts the point), the controls keep it.

## Setup

- Binary from this branch (main `0e1ed4b` plus the bench code; no prompt
  or daemon change). Brain Vertex `gemini-3.5-flash`, voice Vertex
  `gemini-3.8-live`, judge and simulated CEO Vertex
  `gemini-3.1-flash-lite`, TTS `gemini-2.5-flash-tts`.
- Tier 1: `eval sim --live`, the 6 scenarios ×2.
- Tier 2: `bench run --tier 2`, the 6 scenarios ×3, scratch daemons on
  ports 7821–7823. Pass 3 was cut by a tool timeout and rerun in the
  background (`t2-p3`); its 3 errored runs are replaced, and the three
  passes merged and rebuilt with `bench report`.
- The tier-2 scores were rebuilt with `bench report --rescore-explain`
  after three scorer fixes found while reading the runs (the voice's
  speech is assigned by fragment time; a to_supervisor result by its
  call's id; comprehension reads only the answers the side gave). Tier 1
  needed none of them.
- **Judge noise.** The three judge passes over the same 18 tier-2 runs
  gave orchestrator coverage 0.54 / 0.54 / 0.55 and voice coverage
  0.30 / 0.29 / 0.37 (of the orchestrator's points 0.56 / 0.53 / 0.64;
  the voice's text did not change between them). Differences under about
  0.1 between arms are noise at this sample size; the
  orchestrator-to-voice gap (0.18–0.25) held in every pass.

## Headline

| | Tier 1 (orchestrator = what the CEO hears) | Tier 2 orchestrator | Tier 2 voice |
|---|---|---|---|
| Runs, pass | 12, 6 pass | 18, 4 pass | |
| Key-point coverage | 0.68 | 0.55 | **0.37** |
| Of the orchestrator's points | – | – | **0.64** |
| Comprehension (what/why/action) | 1.00 | 1.00 | 0.94 |
| Words per answer en / zh | 89 / 142 | 108 / 176 (spoken alone 63 / 108) | **34 / 55** |
| Simple answers en / zh | 24 / 43 | 31 / 60 | 14 / 31 |
| Follow-up depth | 24/28 | 12/13 | **30/42** |
| No padding (controls) | 7/8 | 3/9 | 5/12 |
| Follow-ups answered without the orchestrator | 0/28 | | **29/42** |

**The voice is where detail is lost.** The orchestrator states about
half of the world's key points (0.55 at tier 2; 0.68 at tier 1, where it
is asked every follow-up and gets more chances). The voice says only
0.64 of what the orchestrator gave it: 0.37 of the world's points
overall. On the answers where it did consult the orchestrator, it said
34 words for the orchestrator's 63 spoken words in English (17
answers), and 76 characters for 108 in Chinese (8 answers), before counting the `detail` it never
uses. Then it stops asking: two thirds of the follow-ups (29 of 42; 12
of 12 in the evidence world `explain-zh-input-rules`) were answered from
what the voice already had, and 12 of the 42 failed the depth item,
mostly by rephrasing (depth 30/42, against the orchestrator's 12/13 on the follow-ups it was asked).

The evidence session reproduces. `explain-zh-input-rules` r1, first
answer: the orchestrator's spoken had the 1,500 characters, the two
match words, "不经过模型", the "好的继续，不过先跑测试" example, the word lists
that disagree across files and the 1.2 s cost; the voice said "summary-claude
发现输入层里硬编码了快捷应答规则，导致复合指令会被截断…它建议删除这些规则…会多花一点二秒" (the judge finds one or two of the seven key points in it, depending on the pass; in the orchestrator's answer, six).
On "然后呢" it answered "我现在等待您的决定" (it read "then?" as "so,
your decision?"), and on "你可以给我一点详细的解释吗" it rephrased
without a tool call.

Per scenario (tier 2; `aggregate-output.txt` has tier 1 too):

| Scenario | Orch coverage | Voice coverage | Voice of orch | Orch words | Voice words | Voice depth | Not consulted (follow-ups) |
|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 0.62 | 0.43 | 0.69 | 206 | 51 | 7/12 | 12/12 |
| explain-en-flaky-test | 0.48 | 0.38 | 0.75 | 103 | 35 | 7/12 | 7/12 |
| explain-zh-notes-ledger | 0.50 | 0.39 | 0.67 | 159 | 60 | 7/9 | 7/9 |
| explain-en-invoice-job | 0.61 | 0.28 | 0.44 | 113 | 32 | 9/9 | 3/9 |

## Other findings

- **"Then?" read as consent (tier 1).** In `explain-en-flaky-test` r1
  the orchestrator answered "Then?" by starting the fix ("I've started
  Codex on fixing the test") and in `explain-zh-notes-ledger` (first
  tier-1 run, discarded for the scorer fix) "然后呢" started the delete
  tool. An action beyond the user's words; step 3's A/B should count it.
- **The orchestrator's own depth is fine** (24/28 at tier 1), but its
  coverage is still about half: it prefers analogies ("像个抢答器",
  "a store still putting tags on the shelves") over the numbers on the
  screen, and its later answers repeat the analogy.
- **Controls.** Simple answers are short (14 words in English, 31
  characters in Chinese from the voice). The padding item is strict:
  "bench-claude 正在把购物车里的 Items 字段重命名为 Lines…" for "在忙吗"
  counts as padding. Treat padding as a guard against regressions when
  arm B asks for more detail, not as a problem today.
- **Comprehension is near the ceiling** at flash-lite (1.00 / 0.94): a
  listener can get the gist even from the voice's two sentences. It will
  only show a regression; coverage and depth are the discriminating
  items.
- **Judge noise on the default items.** Several `no_hallucination` /
  `facts_current` noes are wrong (e.g. "1,500 characters contradicts
  300 lines", where the screen has both). Pass rates on this category
  are dominated by such noes and by depth/padding; compare the
  explanation metrics, not the pass rate.
- **Harness limit.** A fixed script moves on once the voice has been
  quiet for 1.5 s, so a slow answer can arrive after the next question
  (`explain-en-flaky-test` r1, first question). The orchestrator's result
  is attributed to the question its call was made for and the voice's
  speech by fragment time, but the CEO did talk over the late answer.

## Spend

| Item | Estimate |
|---|---|
| Tier 1, first run (before the padding fix, discarded) | $0.31 |
| Tier 1, 12 runs | $0.30 |
| Tier 2 smoke, 1 run | $0.09 |
| Tier 2 passes 1–2 and the cut pass 3 | $1.03 |
| Tier 2 pass 3 rerun | $0.41 |
| Three rescores (judge only) | $0.54 |
| TTS for the six openings | < $0.01 |
| **Total** | **≈ $2.7 of the $8 cap** |

## Reproduce, and run the A/B

```sh
export SHEEPDOG_GEMINI_BACKEND=vertex SHEEPDOG_GEMINI_MODEL=gemini-3.8-live
# Arm A (embedded prompts):
sheepdog eval sim --live --provider vertex --model gemini-3.5-flash --filter explain --repeat 2 --parallel 3 --env-file .env --out OUT/A-t1
sheepdog bench run --tier 2 --env-file .env --brain-provider vertex --brain-model gemini-3.5-flash --filter explain --repeat 3 --parallel 3 --ports 7821,7822,7823 --budget-usd 8 --out OUT/A-t2
# Arm B: a prompts directory with any of assistant.md, USER.md, svp/SOUL.md, svp/RULES.md
# (start from `sheepdog prompts export DIR`); files left out stay embedded.
sheepdog eval sim ... --prompts-dir DIR_B --out OUT/B-t1
sheepdog bench run ... --prompts-dir DIR_B --out OUT/B-t2
# Score recorded runs again (judge only), e.g. after a scorer change:
sheepdog bench report --from OUT/A-t2 --rescore-explain --env-file .env
python3 docs/dev/eval/explain-baseline-2026-10-05/aggregate.py OUT/A-t1 OUT/A-t2
```

`--prompts-dir` is checked before the campaign: a file there that the
loader rejects (a `svp/RULES.md` without its protected sentences) stops
the run instead of silently running the embedded prompt, and the report's
setup lists every prompt file with its source and SHA-256 prefix.
