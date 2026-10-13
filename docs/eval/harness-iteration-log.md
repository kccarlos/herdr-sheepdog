# Harness iteration log

One row per iteration of the harness loop (docs/handover-2026-10-04.md, "Harness iteration, from 2026-10-11"): one labelled change (prompt, information or rail), measured at tier 1 and/or tier 2 against main, judged by Claude Sonnet at segment level, merged or dropped. Newest last. Each row links its report; the eval index (README.md) has the same row in its table.

| # | Date | Change (label) | Tier, n | Segment Understood (Δ, 95 % CI) | Safe | Targeted metric | Decision | Report |
|---|---|---|---|---|---|---|---|---|
| 1 | 2026-10-12 | "Never compress the orchestrator's spoken text" in `assistant.md` (prompt, `nc`) | tier 2, 42 runs per arm (explain ×5, workday ×2) | 66 → 71 % (+5 pp, −5 to +15) | run 38/42 both; segments 94 → 95 % | voice share of the orchestrator's points 0.38 → 0.50 (+0.13, +0.01 to +0.25) | merged; watch voice follow-up depth (−0.14) and no_hallucination (89 → 82 %) | [iteration-1-nc-2026-10-12.md](iteration-1-nc-2026-10-12.md) |
