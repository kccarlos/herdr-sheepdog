# Iteration 6 (untrimmed retest) tier-1 runs (2026-10-14): facts before explaining, on current main

CEO decision (2026-10-14): "再跑一下没问题就合并" (run it again; merge if there is no problem). The prompt token budget test was removed in #217, so iteration 6 goes in without a trim.

## The change (branch `iter/6-untrimmed`, 9466819)

- Iteration 6's RULES.md wording (branch `iter/6-facts-before-explaining`), applied by hand onto main bdda0b6 (that branch's rebase conflicts): the Intent detail sentence (get the facts again on every follow-up; your own earlier lines are not the facts; relay what the user has not heard; when neither holds it, say so and `investigate`; never fill the gap) and Explaining ("asks the same question again"; "A request for more is detail (Intent).").
- Nothing else: no trim of the Tools paragraph (unlike `iter/6-trimmed`), nothing from iteration 6b.

## Setup

Same method as [2026-10-13-iter6t](../2026-10-13-iter6t/STATUS.md), scripts copied unchanged except paths. Tier 1. Brain DeepSeek `deepseek-flash`, `--fallback-provider none`; simulated user DeepSeek `deepseek-flash` (workday has adversarial content: kept off Google); judge fake, then Sonnet (`claude -p --model sonnet`, the local subscription). Chattiness chatty. Main's eval harness as is.

Arms (prompts embedded in each binary): **main** = main bdda0b6; **fix** = iter/6-untrimmed 9466819.

Runs (`driver.sh`): explain.json (four explain worlds + two controls) ×5 per arm, chunks e1 ×3 then e2 ×2; workday ×5 per arm, w1 ×3 then w2 ×2, the two arms side by side. Workday uses the first 5 finished runs per scenario, chunks in order (`merge.py`, KEEP=5). An errored run (status error, infrastructure) is counted as recorded; it is not rerun (except under the held-out rule below).

## Metrics and merge rule (pre-registered, before any comparison run)

CIs: explain metrics by per-run cluster bootstrap (`boot.py`, 10,000 draws); workday by `bench northstar --vs` (Newcombe for rates, cluster bootstrap for segments).

Explain (iteration 6's Sonnet fact judge `factjudge.py`, whole screens, one blind call per run; `combine.py`, `looks.py`), over detail lines (then / more / why / example, 70 per arm):

- **Primary: restating follow-ups** (judge `restates`). fix − main must have its 95 % CI entirely below 0.
- **Wrong facts** (a detail answer with at least one wrong or unsupported fact). fix − main must not rise beyond its CI: the CI's lower bound ≤ 0.
- Reported: no fresh look, new facts per detail answer, detail-turn latency median and p90 (`guards.py`), fallbacks, investigate starts, control reads, first answers wrong. Also reported, not a gate: fix vs iteration 6's own untrimmed fix arm (`2026-10-13-iter6/rows-fix.json`) and vs the trimmed fix arm (`2026-10-13-iter6t/rows-fix.json`).

Workday gate (×5 per arm):

- **Segment Understood** (Sonnet segment judge, `bench rejudge --judge claude --judge-model sonnet --skip-run-level` on copies `main/wd-sonnet`, `fix/wd-sonnet`, both arms). Not worse = the CI's lower bound above −5 pp and the point estimate ≥ −2 pp. If the Claude weekly floor (1 %) forces a cap, both arms are scoped equally (r1–r3 per arm), never one arm fuller than the other.
- **Safe**: run level (code checks) and segment Safe (Sonnet); neither fix − main CI entirely below 0.
- **Held-outs** adversarial-afternoon and explain-deep: fix pass count ≥ main pass count, each.
- **Held-out provider-error rule (written before the runs).** In either arm, a held-out run that fails only because of a provider or model error (a `brain_fallback` with `model_error`, an HTTP/provider error, a malformed tool call from the model, or status error from infrastructure), and not because of an unsafe action, an unintended action or a wrong statement, is rerun once (same binary, same scenario, one repeat, chunk `hr`). The rerun's result replaces that run in the held-out pass count only; the original failure stays in every other workday metric and is noted in the log. A rerun that fails again counts as a failure, whatever the cause.
- Reported: missed news, actions never told, brain fallbacks, Low effort, unmatched claims.

Merge (squash, via gh) only if all hold: segment Understood not worse; primary beyond its CI; wrong facts not up beyond its CI; Safe not regressed; held-outs not regressed. Otherwise do not merge. Log the result either way as "6 (untrimmed retest)" in harness-iteration-log.md and docs/eval/README.md.

## Limits

DeepSeek balance recorded before and after; stop at about 1 CNY left. No adversarial content to Google models. API spend under $10.

## Log
- DeepSeek balance before any run: 25.27 CNY.
- Explain e1 ×3 + e2 ×2 per arm (60 runs, ≈ $0.96 list); DeepSeek 25.27 → 24.57 CNY. Fact judge 60 calls; 2 fix verdicts had trailing text after the JSON (parse errors) and were judged again with a fresh call (`factjudge-fix.log`), 0 errors after that.
- Main vs fix (70 detail lines each, `explain-boot.txt`): **restating follow-ups 49 → 40 %, −9 pp (CI −24 to +7): not beyond its CI. The primary fails.** Wrong facts 27 → 11 % (−16 pp, −29 to −3): down beyond its CI (passes). No fresh look 83 → 43 % (−40 pp, −54 to −26). New facts per detail answer 1.33 → 1.53 (+0.20, −0.23 to +0.63). Composite (failed) 50 → 39 % (−11 pp, −30 to +8). First answers wrong 2/20 → 0/20. Guards (`explain-guards-*.txt`): detail-turn median / p90 4.7 / 8.3 s → 6.1 / 9.2 s; 0 fallbacks on detail turns in both arms; investigate starts 0 → 1; control reads 14 → 15 of 20. Run failures outside the detail turns: main e1 one unintended action (a `prompt` to a worker on a detail turn), fix e2 one brain fallback.
- Beside the gate (`explain-vs-iter6.txt`): this fix arm vs iteration 6's untrimmed fix arm (d52defc): restates 44 → 40 % (−4, −20 to +11), wrong 13 → 11 % (−1, −12 to +9), no fresh look 41 → 43 %: iteration 6 reproduces closely. Vs the trimmed fix arm (b0c5716): wrong 26 → 11 % (−14 pp, −28 to −1), so the trim's accuracy cost seen yesterday is consistent with this run. Main now vs main yesterday: restates 57 → 49 % (−9, −26 to +8), wrong 20 → 27 % (n.s.).
- Workday w1 ×3 + w2 ×2 per arm (70 runs, ≈ $8.5 list), merged first 5 per scenario (`merge.py`). DeepSeek 7.30 CNY after all runs.
- Workday code checks (`northstar-wd-main-vs-fix-fakejudge.md`): Safe 34/35 → 32/35 (−6 pp, −20 to +7; the 3 fix runs are actions never told, 0 → 3 of 89; main's 1 is an unintended `y` to etl-codex in scale-ten); missed news 11 → 16 % (+5 pp, −2 to +11); unmatched claims 3 → 7 % (+4, −1 to +9); Low effort 23/35 both; time to goal +7 s (−2 to +19). Held-outs: adversarial-afternoon 5/5 both, explain-deep 5/5 both; no held-out rerun was needed. Brain fallback runs: main 1 (morning-standup, yielded to a user turn), fix 5 (4 yielded to a user turn in long-haul ×2 and scale-ten ×2, 1 turn timeout in scale-ten).
- Sonnet segment judge on workday ×5 per arm (`rejudge-*.log`, 516 + 519 calls, 0 errors; binary from iter/6-untrimmed, whose eval code equals main's): **segment Understood 375/419 (89.5 %) → 367/421 (87.2 %), −2 pp (CI −7 to +2): fails "not worse"** (lower bound below −5 pp and point estimate below −2 pp). Segment Safe 97.3 → 98.3 % (500/514, 509/518). `northstar-wd-main-vs-fix-sonnet.md`.
- Decision: **not merged.** Two items fail: the primary (restating follow-ups −9 pp, CI −24 to +7) and segment Understood (−2 pp, CI −7 to +2). Wrong facts (−16 pp, beyond its CI), Safe and both held-outs pass. Branch `iter/6-untrimmed` kept.
- Spend: DeepSeek 25.27 → 7.30 CNY (17.97 CNY; the runs' own list-price estimate ≈ $9.48). Claude subscription: 60 + 2 fact-judge calls, 1,035 segment-judge calls; 5-hour session 100 % → 11 % left, weekly 36 % → 29 %.
