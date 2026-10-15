# WP7b — live end-to-end results (2026-09-17)

Plan §9 WP7 acceptance and §10 scenarios run against real herdr 0.9.0
panes and a real `gemini-3.8-live` session. Event log:
`testdata/e2e/2026-09-17-e2e.jsonl` with a line index and the latency
table in `testdata/e2e/README.md`. Daemon: `bin/sheepdog serve
--voice=gemini --herdr=socket --dev`, default config (debounce 3 s,
snapshot 30 s, pending TTL 120 s, cheap LLM `gemini-3.5-flash-lite`, CLI
tool `auto` → codex). Voice client: a Go WebSocket client driving `/ws`
(no microphone — `testdata/audio/question.wav` streamed as 20 ms frames
for the audio trials, `{"type":"text"}` utterances for everything else).
Base commit 7db2ee6; the re-verification pass ran the three fixes below (the binary reported itself as c7e12b2, the pre-reword hash of 5a68474).

## Setup

- Scratch git repo (`mathlib`, one failing test `TestClampUpper`) copied
  into four herdr workspaces: `auth` (agy 1.2.5, name `one` → alias
  `auth-one`), `docs` (agy, `docs-two`; closed before the second run),
  `billing` (Codex 0.154 `--sandbox read-only -a on-request`,
  `billing-cx`), `search` (Claude Code 2.1.275 `--permission-mode default
  --model haiku`, `search-cc`). Every worker was checked to be in its
  prompting mode (agy default, Codex `on-request`, Claude "manual mode
  on"). Trust dialogs (all three kinds) were answered before the daemon
  started. The user's `kvoice` workspace was never touched.
- Plugin: `plugin/install.sh` cannot skip the launchd step, so it was run
  with `SHEEPDOG_DRY_RUN=1` only and the shim was linked directly with
  `herdr plugin link <worktree>/plugin`; unlinked at the end
  (`herdr plugin list` → "No plugins installed", as found).
- The hook channel works end to end: for every status change the log shows
  `herdr.state source=http` (the plugin's `POST /api/herdr/event`) first
  and `herdr.dedup source=socket` a few ms later (e.g. lines 87–89,
  463–464). The socket subscription won the race only when the hook was
  slow (line 551).

## Scenario table

| # | Scenario | Result | Notes |
|---|---|---|---|
| 1 | Status | **PASS** | `briefing`, one sentence, no `llm.call`. Audio 1.63/1.66/1.92 s, typed 0.92–0.99 s utterance end → first audio; the handler itself < 1 ms. See latency section. |
| 2 | Proactive block | **PASS** (Codex, Claude: real herdr detection; agy: hook-shaped event, see bugs) | Codex and Claude prompts detected by herdr, announced within 4.6–5.0 s of the `blocked` event (3.0 s of which is `debounce_ms`). |
| 3 | Answer by voice | **PASS** (agy, Codex, Claude) | "Yes." → `confirm` → `herdr.send keys=["1"]` → worker proceeded (README edited / `notes2.txt` / `claude-notes.txt` created) → "Told <alias> yes." → `pending.confirmed`. "No." on agy's edit prompt sent `["2"]` (Esc is disabled there). |
| 4 | Instruction with echo | **PASS** | `propose` → resolve → `pending.created` → `agent.prompt` → "Telling auth-one to add a one-line comment … Sent." → agy showed the edit. |
| 5 | Cancel in time | **PASS** | "Stop." 1.2 s into the echo → model interrupted (`flush`) → `cancel` → `ctrl+c` → agy "Interrupted" → "Cancelled." → nothing else sent. Was followed by a spurious "auth-one finished." (fixed, 5a68474; re-verified line 905). |
| 6 | Ambiguity | **PASS with caveat** | "Tell it to run the tests again." → `propose` (no agent) → `ambiguous` → "Do you mean auth-one, docs-two, billing-cx, or search-cc?" → nothing sent. The plan's exact phrase "tell it to continue" did **not** produce the question: once the voice model guessed `agent:"auth-one"` (the agent just discussed) and it was sent; once it routed it to `confirm(answer:"continue")` ("Nothing is waiting for an answer.", nothing sent). Model-side; see not-fixed list. |
| 7 | Deep ask | **PASS** | "Why did auth-one's tests fail?" → "I'm checking the logs …" at 0.77 s → `cli.run backend=codex` 24.1 s → "Auth-one's tests failed because Clamp returns the lower bound instead of the upper bound when a value exceeds the maximum." (1 sentence; names the function, not the test name `TestClampUpper` — `spoken_answer` compressed it). 27.4 s end to end. |
| 8 | Expiry | **PASS** | Claude prompt announced, no answer for 2 min → `pending.expired` → "Yes." → "That question expired; search-cc was asking whether it can create claude-notes2 txt. Should I answer yes?" (`pending.revived`) → "Yes." → `keys=["1"]` → file created. The periodic silent briefing re-push (10 min) fired during the wait (line 683). |
| 9 | Reconnect | **PASS** | WS client closed and re-dialled 3 s later; "What did I just ask you?" → "You asked why auth one's tests failed." |
| 10 | Compression + GoAway | **NOT PASSED / partially run** | One 13-minute session. No `voice.goaway` was ever sent; at 13.1 min the server closed with `close 1011 Internal error encountered`; every reconnect with the resumption handle then failed at setup with the same error, ~30 s each, forever (bug, fixed in 4d324ef — dead handle dropped after 2 attempts). Compression never triggered: prompt tokens peaked at 4 277 (trigger is 16 000); continuous zero-valued audio did not raise the prompt count (VAD-filtered). The silent briefing re-push on the 10-min timer worked and a "what's going on" 40 s after a block was correct. Needs a re-run with a ≥ 10-min session on the fixed binary to confirm `voice.resume`. |

Worker-kind matrix (prompt parsed by `summarize_block` / keys correct / herdr detects the prompt):

| Kind | Version | Parsed OK | Keys OK | herdr `blocked` detection |
|---|---|---|---|---|
| agy (Antigravity CLI) | 1.2.5 | yes — command prompt → options 1–4, yes `1` / no `4`; edit prompt → yes `1` / no `2` | yes (`1` accepted the edit; `2` rejected it; `ctrl+c` interrupts the turn, agent survives) | **no** — herdr's remote agy manifest (2026.06.24.1) does not match either 1.2.5 prompt; reported `idle`/`done` (see not-fixed) |
| Codex | 0.154.0 | yes — options 1–3, yes `1` / no `esc` | yes (`1` ran the command) | yes (hook + socket), 9 s after the prompt; note `--sandbox workspace-write` never asked (writes and blocked network both ran without approval) — use `read-only` to get a prompt |
| Claude Code | 2.1.275 | yes — options 1–3, yes `1` / no `3` | yes (`1` wrote the file, twice) | yes, 3–4.6 s after the prompt |

The agy screens were captured the WP0 way into `testdata/herdr/agy-*`
(`agent.read visible`/`detection`, `agent.get`, `agent.list`,
`agent.explain`, the `agent.prompt` wait response, the trust dialog, and
the "Esc disabled during file edits" variant); hostname, home path and the
account e-mail are redacted.

## Latency vs targets

| Target (plan) | Measured | Verdict |
|---|---|---|
| `briefing` utterance end → audio start < 1.2 s | typed 0.92–0.99 s; audio 1.63–1.92 s, of which 0.94–1.33 s is Gemini's end-of-speech detection + tool call and 0.60–0.72 s the model's first audio after the (< 1 ms) tool result | typed: met; audio: **missed by ~0.5 s, none of it sheepdog's** — the added latency is < 1 ms + one WS hop |
| `briefing` tool < 300 ms | < 1 ms (cache) | met |
| blocked → announced within ~5 s | hook event → first audio 4.56–4.96 s (3.0 s debounce + 0.8–1.3 s `summarize_block` + 0.6–0.9 s model); herdr's own detection adds 3–4.6 s (Claude) / 9 s (Codex) after the prompt appears | met on sheepdog's side; end to end 8–14 s |
| `ask` deep path | 27.4 s (codex exec 24.1 s + two cheap-LLM calls) | as expected, "Checking" spoken at 0.8 s |

The 3 s debounce is the largest sheepdog-controlled term. No `blocked`
flicker shorter than 3 s was seen on any of the three worker kinds in this
run (herdr's classification is already stable), so lowering
`debounce_ms` to ~1 s would cut the announcement to ~2.5 s.

## Bugs found and fixed (this branch)

| Commit | Fix | Evidence |
|---|---|---|
| 4d324ef | `fix(voice)`: a resumption handle the server keeps rejecting is dropped after `ResumeAttempts` (2) failures so the session reconnects fresh; connect failures are logged as `voice.connect_error`. Idle pushes waiting on a turn the model never completes are re-checked when `TurnTimeout` elapses instead of waiting for the next unrelated event. | lines 785–831 (reconnect loop, only visible as WS `error` frames); lines 469/486 and 554/577 (block announcements queued 60 s until the next tool call) |
| e4ecc2c | `fix(herdr)`: `WatcherConfig.Known` seeds the watcher from the persisted state table; the first snapshot closes rows whose pane is gone. | `docs-two` (workspace closed while the daemon was down) still in briefings/clarify at lines 362, 486; closed at startup at line 840 after the fix |
| 5a68474 | `fix(supervisor)`: no "finished" push for the working → done that follows a cancel's Ctrl-C (`supervisor.finished_suppressed`). `prompts/voice_system.txt` now explains the `[system event …]` / `[system context …]` push prefixes. | line 232 ("auth-one finished." read as "has completed the task" after a cancel); pushes paraphrased as "Bypassing output." (line 389), "has finished the requested update to the readme" (103); after the fix pushes were read as given (lines 921, 932, 958) |
| (tests) | `TestAgyPromptsAnswerWithDigits` pins the agy fixtures' key mapping; `TestWatcherKnownPanesReconciledBySnapshot`, `TestPushIdleReleasedAfterTurnTimeout`, `TestDeadResumptionHandleIsDropped`, `TestCancelSuppressesFinishedPush`. | |

## Bugs found and not fixed (for the orchestrator)

1. **herdr does not classify Antigravity CLI 1.2.5 prompts as `blocked`** (external, herdr).
   The remote manifest `agy.toml` 2026.06.24.1 requires "requesting
   permission for:" plus "do you want to proceed?" or ("tab amend" and
   "edit command"); agy 1.2.5 shows "Run this command?" / "tab Amend ·
   ctrl+g edit/expand command" for commands and "Accept this file edit?"
   (no "requesting permission" line at all) for edits. `agent.explain`
   → `default_known_agent_idle_fallback`. Consequences for sheepdog: no
   proactive announcement for agy, a false "auth-one finished." when agy
   reaches a prompt (herdr says `done`), and `agent.prompt` is not refused
   while agy is at a prompt (herdr thinks it is idle), so a `propose` would
   type into the approval UI. A local override
   (`~/.config/herdr/agent-detection/agy.toml` widening the `any` list) fixes
   it but writes to the user's herdr config, which this run was not allowed
   to do; the alternative is a manifest update upstream. Scenario 2 for agy
   was therefore driven by posting the hook payload herdr *would* send
   (`POST /api/herdr/event`, `source=http`); everything after that
   (pane read, summary, push, keys) was real.
2. **Zero-valued audio frames degrade the session.** While the test client
   streamed digital silence (all-zero PCM) continuously, the model answered
   realtime text only after 3.9–34.7 s (it answered as soon as the stream
   stopped) and swallowed two of three idle pushes. With a mic-like noise
   floor (±8 LSB) the same session type behaved normally (0.92–0.97 s).
   Real browsers send mic noise, but a hardware-muted mic sends zeros.
   Worth a note in `docs/dev/gemini-live-notes.md` and possibly a client-side
   guard (do not send frames whose peak is 0; sheepdog's `mute` contract
   already means "send nothing").
3. **Voice model routing of "tell it to continue"** (scenario 6 phrasing):
   the model either guessed the last-discussed agent or called `confirm`.
   The tool description says `agent` is "alias if the user named one"; a
   stronger instruction in `voice_system.txt` ("never fill `agent` unless
   the user said a name") may help; not attempted because prompt tuning
   needs more sessions than the quota allowed. *Follow-up (supervisor
   proactivity branch): the prompt and tool descriptions now separate
   "things you want to know" (`ask`) from "things an agent should do"
   (`propose`), and `ask` without an alias goes to the agent discussed
   last; the "tell it to continue" phrasing itself was not re-run.*
4. **GoAway never observed**; the server closed with 1011 at 13.1 min
   instead. Scenario 10 (resume + compression) needs another ≥ 10-minute
   session on the fixed binary. Compression at 16 000 trigger tokens is
   unreachable in a 12-minute text-driven session (peak 4 277).
5. **`spoken_answer` dropped the test name** in scenario 7 ("names the
   failing test" was not met); the raw codex output had it. A template hint
   ("keep the failing test's name") would fix it. *Follow-up: the template
   now says naming a test, function, package or file is fine and asks for
   the gist of the agent's final message; not re-run live on scenario 7.*
6. **Filenames in spoken text**: `summarize_block` said "README.md" and
   "claude-notes2 txt"; D9 says no paths. Minor prompt tweak.
7. **agy's "finished" bounce**: herdr reports agy `working → done → working`
   around its prompts, so sheepdog pushed "auth-one finished." twice in a
   row (lines 921/932); a consequence of (1).
8. **Codex `--sandbox workspace-write -a on-request` never prompted** for
   a workspace write or a (blocked) network call; the README's
   worker-mode warning could mention that only `read-only` reliably
   produces approval prompts for file changes.

## Quota used

- Gemini Live (free tier, 4 keys in the pool): 4 sessions, all on key 0,
  no 429, no `voice.key_rotated`, no `llm.rate_limited`. Session uptimes
  15 s + 414 s + 940 s (connected 784 s, the rest failed reconnects) +
  143 s ≈ 22.6 min daemon time, ≈ 20 min connected; ~1.9 min of model
  audio received (received WAVs: 71 s, 42 s, ~24 s).
- Cheap LLM (`gemini-3.5-flash-lite`): 13 `llm.call`s (7 summarize_block,
  5 resolve_instruction, 1 spoken_answer) plus 2 from `llm-test`; Ollama
  fallback not needed.
- Codex: one `cli.run` (`codex exec` read-only, 24 s) and two interactive
  turns; Claude Code: two interactive turns (haiku); agy: about eight
  turns.

## Files added / changed

- `testdata/e2e/2026-09-17-e2e.jsonl`, `testdata/e2e/README.md` — the run
  and its index/latency table.
- `testdata/herdr/agy-*.{txt,json}` — Antigravity CLI 1.2.5 screens and
  herdr responses (trust dialog, command approval, file-edit prompt, the
  "Esc disabled" variant).
- `docs/e2e-results.md` — this file.
- Fixes: `internal/voice/gemini/{run,push,config}.go`,
  `internal/herdr/watcher.go`, `internal/app/app.go`,
  `internal/supervisor/{supervisor,tools,transitions}.go`,
  `prompts/voice_system.txt`, tests alongside.

## Teardown

All four scratch workspaces closed; `herdr agent list` empty; `herdr api
snapshot` shows only the user's `kvoice` workspace (`w4`); plugin
unlinked; the daemon and the test client stopped; no herdr config files
were created.
