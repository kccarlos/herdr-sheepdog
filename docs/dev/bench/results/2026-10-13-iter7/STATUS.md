# Iteration 7 tier-1 runs (2026-10-13): unanswered questions

CEO product note (approved scope, 2026-10-13): when the user asked a question that never got a real answer, the orchestrator comes back to it later ("about your earlier question on …"). The daemon records only the clear cases: (a) the orchestrator turn fell back (error, timeout, broken reply), (b) the answer was cut off by the user's barge-in before it was said, (c) it was never voiced. It does not judge whether an answer was good. Change type: **information**.

Setup: tier 1, brain DeepSeek `deepseek-flash`, `--fallback-provider none`, judge fake, then a Sonnet judge (`claude -p --model sonnet`) on what the decision needs. Chattiness chatty. Arms: `sd-main` = main 9ae2f15 + the voice cut tag (829783d, unread on main) + the eval fault harness (caa239f, 8f0e459); `sd-fix` = branch `iter/7-unanswered-questions` (51a9c99 + harness). Both arms read the same scenario file from the worktree.

## Step 0: how often (before any run)

`count.py` over every bench event log in the repo (deduplicated) and the live daemon's logs (read only). Results in `bench-counts.txt` and `live-counts.txt`; summary in the iteration report.

## Step 1: smoke (disclosed)

`main/smoke-v1`, `fix/smoke-v1`: the first scenario version ended on "anything else I should know?", which asks for open items, so an answer there says nothing about coming back. The scripts were changed (e857189) before any comparison run: the later turns never invite the old question (another agent's status, a wait for a finish, "thanks, that's all"). `main/smoke`: main ×1 on the new version (10 runs), read by hand: main already comes back to (a) and (c) questions in most runs, because its transcript has the fallback line and the "the user did not hear" note; it rarely comes back to a cut answer. The zh cut was split on spaces (one chunk), fixed (8f0e459) before the comparison runs.

## Step 2: pre-registered before the comparison runs

Runs: `unanswered.json`, 10 fixed-script scenarios, ×6 per arm, interleaved (main c1, fix c1, main c2, fix c2; ×3 each).

- Targets (6): `ua-{fail,cut,unvoiced}-{en,zh}`: the first question gets no real answer (fault), the later turns never ask it again or invite it.
- Noise controls (4): `ua-dropped-{en,zh}` (the user says never mind), `ua-reasked-{en,zh}` (the user asks again and gets the answer).

Judge: `ansjudge.py`, one blind `claude -p --model sonnet` call per run (arm not shown): the transcript, the faulted question and what the user heard of its answer, and the world's answer to that question (the whole screen of the agent it is about).

**Targeted metric (primary): answered later** = share of target runs in which a sheepdog line after the faulted turn (a reply or a pushed line) gives the user the substance of the answer to the faulted question (the world's core finding: for "why did the tests fail", that the add function gives the wrong sum / ignores a number; for "what did bench-claude find", the two broken links), unprompted (the scripts never re-ask). fix − main with a Newcombe 95 % CI over runs (one question per run). Passes if the CI is above 0.

**Noise guard: stale follow-ups** = share of control runs in which sheepdog, unprompted, tells the faulted question's answer again or comes back to it after the user dropped it, or after it was already answered in full (a bare offer of a next step on an open item, "shall I have it fix the add bug?", is not a stale follow-up). Also counted on targets: the answer told again after it was already told later (repeats). Must not be worse: fix − main point estimate at most +10 pp, and every flagged case read.

Gate (only if the targeted metric passes): workday ×5 per arm (35 runs each, DeepSeek simulated user, includes both held-outs) for Safe (run level, code checks) and the held-outs (adversarial-afternoon, explain-deep); segment Understood by the Sonnet segment judge (`bench rejudge`), scoped as the quota allows. If the targeted metric does not pass, the decision is settled and the workday gate runs only on code checks (iteration 6 precedent).

## Log
- Comparison runs done: main c1, fix c1, main c2, fix c2 (×3 each, 120 runs, ≈ $1.66 list). DeepSeek balance 44.27 → 42.71 CNY. Every fix run recorded its faulted question (fallback 24, cut 18, unvoiced 18) and the orchestrator cleared each one with answers_earlier on the next user turn (60/60, none by expiry).
- Sonnet judge (`ansjudge.py`, 120 calls + 10 on the smoke as a check): `summary.txt`. **Answered later 20/36 (56 %) → 36/36 (100 %), +44 pp (Newcombe 95 % CI +27 to +60): passes.** By case: fail 67 → 100 %, cut 8 → 100 %, unvoiced 92 → 100 %. Always at the first chance (the reply to user line 2) in both arms.
- Noise guard, as the judge flagged it: controls 1/24 → 6/24 (+21 pp, CI 0 to +41); repeats on targets 6/36 → 15/36. Every flagged case read, plus one blind Sonnet call over all 28 flagged quotes against the pre-registered rubric (`restale.py`, `restale.json`): most flags are reminders of an open item at the sign-off ("the two broken links are still there whenever you want them"), which the rubric does not count. Under the rubric: controls 0/24 → 2/24 (+8 pp; both `ua-dropped-zh`, "之前的那个问题按你说的不说了", "README 那件小事我按你说的先放下了": the question the user dropped is mentioned at the sign-off), targets 1/36 → 2/36. Within the pre-registered +10 pp, narrowly; disclosed.
- Tier-2 check (benign only: barge-in-long-answer, barge-in-hold-to-talk, slowness-brain-9s; Vertex Live, 2 scratch daemons on :7921/:7922; `t2/`): the first cut rule (run played time / run length) recorded a tail flush whose answer's gist had been heard and missed a long answer cut after its first clause, because the voice streams its audio. Fixed (93f144a): the first cut of an answer, judged by heard text / answer text. Tier-1 results unaffected (the stand-in cut hears 3 words of long answers). Re-run ×3 per arm (`t2/fix2-a`, `t2/main-a`, ≈ $1.3): fix recorded 4 of 6 barge-in answers, 3 cleared by the model; no utterance fell back on the 9 s slow provider.
- Workday ×5 per arm (w1 ×3 + w2 ×2, `merge.py` first 5 per scenario; `northstar-wd-main-vs-fix-fakejudge.md`): Safe 31/35 → 32/35 (+3 pp, −13 to +18); held-outs adversarial-afternoon 5/5 both, explain-deep 5/5 both; missed news 17 → 14 %; actions never told 6/91 → 2/89; Low effort 30/35 → 25/35 (−14 pp, −33 to +5; scale-ten 3/5 → 0/5, 7 rephrasings vs 2, the ambiguous-approval exchange). No utterance fell back in the fix arm's 35 runs, so no Unanswered section appeared there.
- Sonnet segment judge, scoped to ×3 per arm (r1–r3, 42 runs; the full ×5 projected to ~1.5 five-hour sessions): segment Understood 229/249 (92 %) → 232/253 (92 %), −0 pp (−5 to +5); segment Safe 98 → 99 % (`northstar-wd3-main-vs-fix-sonnet.md`). The partial ×5 rejudge logs are kept as `rejudge-*-partial.log`.
- DeepSeek balance 44.27 → 34.58 CNY. Claude weekly 54 → 48 %.
- **Decision: merge** (Understood not worse, targeted metric beyond its CI, Safe and held-outs not worse, noise guard within +10 pp under the rubric).
