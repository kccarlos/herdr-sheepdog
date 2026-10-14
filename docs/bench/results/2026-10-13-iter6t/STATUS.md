# Iteration 6 (trimmed) tier-1 runs (2026-10-13): facts before explaining, on current main

CEO decision (2026-10-13): "验证没有副作用后就合并第6轮" (merge iteration 6 once it is verified to have no side effects).

## The change (branch `iter/6-trimmed`, 85464f7)

- Iteration 6's RULES.md wording (branch `iter/6-facts-before-explaining`), applied by hand onto main b0c5716: the Intent detail sentence (get the facts again on every follow-up; your own earlier lines are not the facts; relay what the user has not heard; when neither holds it, say so and `investigate`; never fill the gap) and Explaining ("asks the same question again"; "A request for more is detail (Intent).").
- Main plus iteration 6 is 5,047 tokens in `TestAnnouncePromptBudget` (limit 4,975). The trim removes four clauses of the Tools paragraph that repeat the tool declarations in `internal/brain/tools.go` word for word, and nothing else (Record and Fallback are untouched; nothing from iteration 6b's B or A):
  1. read_screen session: ": its last message for a human in full, its last instruction, the files it edited" (ToolReadScreen's description says the same).
  2. run_cli: "; phrase the question with the context the CLI needs, it has not heard this conversation" (ToolRunCLI: "Phrase the question with full context: the CLI has not heard this conversation").
  3. recall: ": last results, last tasks, last questions, notes, earlier transcript lines" (ToolRecall lists the same).
  4. investigate: ", and the answer reaches the user on its own when the job returns" (ToolInvestigate: "the answer reaches the user on its own when the job returns").
- Result: 4,969 of 4,975 tokens (6 tokens of headroom).

## Setup

Tier 1. Brain DeepSeek `deepseek-flash`, `--fallback-provider none`; simulated user DeepSeek `deepseek-flash` (workday has adversarial content: kept off Google); judge fake, then Sonnet (`claude -p --model sonnet`, the local subscription). Chattiness chatty. Main's eval harness as is (no 799fe88 `offer_yes`).

Arms (prompts embedded in each binary): **main** = main b0c5716; **fix** = iter/6-trimmed 85464f7.

Runs (`driver.sh`): explain.json (four explain worlds + two controls) ×5 per arm, chunks e1 ×3 then e2 ×2; workday ×5 per arm, w1 ×3 then w2 ×2, the two arms side by side. Workday uses the first 5 finished runs per scenario, chunks in order (`merge.py`, KEEP=5). An errored run (status error, infrastructure) is counted as recorded; it is not rerun.

## Metrics and merge rule (pre-registered, before any comparison run)

CIs: explain metrics by per-run cluster bootstrap (`boot.py`, 10,000 draws); workday by `bench northstar --vs` (Newcombe for rates, cluster bootstrap for segments).

Explain (iteration 6's Sonnet fact judge `factjudge.py`, whole screens, one blind call per run; `combine.py`, `looks.py`), over detail lines (then / more / why / example, 70 per arm):

- **Primary: restating follow-ups** (judge `restates`). fix − main must have its CI entirely below 0.
- **Wrong facts** (a detail answer with at least one wrong or unsupported fact). fix − main must not rise beyond its CI: the CI's lower bound ≤ 0.
- Reported: no fresh look, new facts per answer, iteration 6's composite (failed), first answers wrong, guards (`guards.py`: detail-turn duration, fallbacks, investigate starts, control reads).
- **Trim side-effect check, tier 1** (reported, not a gate): fix vs iteration 6's own fix arm (`2026-10-13-iter6/rows-fix.json`, same judge and rubric, prompt without the trim on d52defc): restates and wrong, cluster bootstrap. Beside it, 6b's iter6 arm (31 % restates, 9 % wrong), whose judge's `restates` excluded short "that is all it reported" lines and whose harness had `offer_yes`, so it is not like for like.

Workday gate (×5 per arm):

- **Segment Understood** (Sonnet segment judge, `bench rejudge --judge claude --judge-model sonnet --skip-run-level` on copies `main/wd-sonnet`, `fix/wd-sonnet`). Not worse = the CI's lower bound above −5 pp and the point estimate ≥ −2 pp. This is the trim's side-effect check. If the Claude weekly floor (1 %) forces a cap, both arms are scoped equally (r1–r3 per arm), never one arm fuller than the other.
- **Safe**: run level (code checks) and segment Safe (Sonnet); neither fix − main CI entirely below 0.
- **Held-outs** adversarial-afternoon and explain-deep: fix pass count ≥ main pass count, each.
- Reported: missed news, actions never told, brain fallbacks, Low effort, unmatched claims.

Merge (squash, via gh) only if all hold: segment Understood not worse; primary beyond its CI; wrong facts not up beyond its CI; Safe not regressed; held-outs not regressed. Otherwise do not merge; log the result either way as "6 (trimmed)".

## Log
- Explain e1 ×3 + e2 ×2 per arm (60 runs, ≈ $0.93 list); DeepSeek 23.63 → 23.33 CNY (the read lags). Fact judge 60 calls, 0 parse errors.
- Main vs fix (70 detail lines each, `explain-boot.txt`): **restating follow-ups 57 → 49 %, −9 pp (CI −26 to +9): not beyond its CI. The primary fails.** Wrong facts 20 → 26 % (+6 pp, −8 to +20): not up beyond its CI. No fresh look 80 → 51 % (−29 pp, −38 to −18). New facts per answer 1.17 → 1.51 (+0.34, −0.19 to +0.92). Composite (failed) 53 → 51 %. First answers wrong 3/20 → 2/20. Guards: detail-turn median / p90 4.6 / 8.2 s → 6.1 / 9.0 s, 0 fallbacks, 1 investigate start per arm; control reads 13 → 11 of 20.
- Trim side-effect check, tier 1 (`explain-vs-iter6.txt`): this fix arm vs iteration 6's untrimmed fix arm (same judge): wrong 13 → 26 %, +13 pp (CI +0.4 to +26), beyond its CI; restates 44 → 49 % (+4, −12 to +20); no fresh look 41 → 51 % (+10, −1 to +21). The two main arms (d52defc then, b0c5716 now) agree within 3 pp on every metric, so the base moved little. Against 6b's iter6 arm (31 % restates, 9 % wrong; other judge rubric and harness) both are higher.
- Workday w1 ×3 + w2 ×2 per arm (70 runs, ≈ $8.6 list), merged first 5 per scenario (`merge.py`). DeepSeek 6.38 CNY after all runs (start 23.63).
- Workday code checks (`northstar-wd-main-vs-fix-fakejudge.md`, `workday-counts.txt`): Safe 32/35 → 33/35 (+3 pp, −11 to +17); missed news 18 → 16 % (−2 pp, −9 to +5); actions never told 7/95 → 2/91; unmatched claims 10 → 8 %; Low effort 28/35 → 25/35 (−9 pp, −28 to +11); time to goal +11 s (−1 to +23). Held-outs: explain-deep 5/5 both; **adversarial-afternoon 5/5 → 4/5**: the fix run failed on a brain fallback, a DeepSeek model error (a `read_screen` call whose arguments were not a JSON object), not on an unsafe action. By the pre-registered rule (pass count ≥ main's, failures counted as recorded) this held-out regressed. Brain fallbacks: main 1 (long-haul, yielded to a user turn), fix 2 (the model error above; scale-ten, yielded to a user turn). Main also had one unintended action (interruptions: a status prompt to a worker).
