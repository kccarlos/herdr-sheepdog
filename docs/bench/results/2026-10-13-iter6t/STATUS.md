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
