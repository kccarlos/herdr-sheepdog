# Iteration 1 (nc) confirmation runs, 2026-10-12

Branch `iter/1-nc`. Live tier-2 conversations only (judge fake); judged afterwards with Sonnet. Binary: main 871fe38.

Production config: brain DeepSeek `deepseek-flash` with Vertex `gemini-3.5-flash` as the only failover target (the bench clears the AI Studio key pool when the voice is on Vertex; failover order vertex, deepseek, gemini), voice Vertex Live `gemini-3.8-live`, Vertex TTS `gemini-2.5-flash-tts`, chattiness chatty, simulated user Vertex `gemini-3.5-flash`. Scratch daemons on :7911/:7912 only (2 concurrent), 1 min pause between chunks. No adversarial scenario was sent to Google.

Arms (`docs/eval/prompt-arms/2026-10-11`): `a` = main prompts (identical to `sheepdog prompts export` at 871fe38), `nc` = a + the never-compress rule in `assistant.md`. Per arm: explain category (6 scenarios incl. 2 controls) x5 = 30 runs, 6 non-adversarial workday scenarios x2 (`--personas rotate`) = 12 runs. Chunks interleaved a, nc: `explain-1`, `explain-2`, `workday-1`, `workday-2`. Scripts: `driver.sh`, `run-chunk.sh`. Spend per chunk: `spend-chunks.txt`; driver log: `driver.log`.

## Progress

Updated as chunks finish (see `spend-chunks.txt`).

**tier-2 runs done** (2026-10-12 01:51): all 8 chunks finished, 42 runs per arm (explain 6 x5, workday 6 x2). Vertex bench-meter total ≈ $19.77 (a $9.48, nc $10.29). DeepSeek balance after the runs: 39.34 CNY. Scratch daemons on :7911/:7912 are stopped; the ports are free. Next: Sonnet segment rejudge and comparison on this branch.
