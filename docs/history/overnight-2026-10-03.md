# Overnight run, 2026-10-03

Unattended run while the CEO was away. Rules for the night: merge when CI is green, no TestFlight tags, the user's daemon (port 7777) stopped, the user's herdr session and agents untouched, eval spend ≤ $100, keep ≥ 3 % of the weekly Claude allowance and never hit the session limit.

## What landed

Daemon (all squash-merged on green CI):

| PR | What |
|---|---|
| #100 | Docs refresh (status, bench backlog, README, CONFIG) |
| #101 | Current-focus sync brain → voice; herdr renames picked up; live system_prompt names; GPT-Live heard check |
| #102 | Vertex minimal-thinking refusal wording |
| #103 | Agent window API: `GET /api/agents/{pane}/screen`, `POST /api/agents/{pane}/input` |
| #104, #106 | Tier-2 voice benchmark (synthetic speech → Gemini Live → brain) and its first campaign |
| #105, #109 | Tier-3 benchmark (real daemon, real Claude workers, isolated herdr session), text and voice modes |
| #107 | Tier-3 findings: unnumbered trust menu, Claude command block, menu safety guard, title-based names, Chinese lines |
| #108 | Tier-2 findings: ack ≠ answer, deferral never declines, echo text guard, push framing, re-queue timing |
| #110 | Start a new Claude/Codex agent by voice (prompt tool parameters; rails: the CEO's unused words, project roots, 3 per 10 min) |
| #111 | Announce/brain leftovers: owed lines, heard names, failover notice and budget, buried prompts, queued stop, idle menus |
| #112, #116 | Campaigns D and E |
| #113 | Echo gate (holds back the reply's echo once a client's echo shows itself) |
| #114, #115 | Campaign-D fixes (supervisor/brain N2, N4–N8; voice delivery N1, N3) |

iOS: #31 agent window (live terminal + key bar per agent), #30 docs. Not on TestFlight yet (0.1.1 (20) predates #31).

Open, not merged: **#117** — brain answers delivered as the voice tool's late function response (`brain.reply_wait_s = 30`). Held for the CEO's decision on the voice layer's role.

## Measurements

| Tier | Campaign C | D | E |
|---|---|---|---|
| 1 text sim (DeepSeek) | 87 % | 85 % | **88 %** |
| 2 voice (echo excluded) | — | 61 % (first run 58 %) | **67 %** |
| 2 echo at −40 dB (with AEC) / −16 dB (no AEC) | — | 0/6 at −16 dB | 2/6 / 2/6 |
| 3 real herdr, Claude Haiku workers | A 9/12, B 8/12 | 8/12 | text 7/12, voice 5/12 |

Spend: about $52 of API credit in total for the night (judges, simulated users, TTS, Live audio, brains), list-price estimates. Claude usage at the end: session 65 % left, weekly 22 % left.

## A/B: answers as function responses (#117), 70 runs per arm

| | A: push (main) | B: function response |
|---|---|---|
| self-relay, answers / announcements | 7 / 0 | 0 / 3 |
| voiced as an ack only ("On it.", "Checking.", "我正在查看。"), answers / announcements | 5 / 0 | 6 / 0 |
| body skipped, nothing audible, answers / announcements | 1 / 5 | 0 / 2 |
| unheard answers to the CEO's own questions (sends) | 38 / 157 | 28 / 139 |
| turn.latency total p50 / p90 | 4.21 / 7.22 s | 4.17 / 6.84 s |
| first audio after hand-off p50 / p90 | 0.64 / 0.75 s | 0.64 / 0.78 s |
| pass rate | 47/70 (67 %) | 49/70 (70 %) |
| failures: real / judge artefact / sim-CEO artefact | 18 / 3 / 2 | 16 / 4 / 1 |
| real failures caused by delivery | 4 | 4 |

Only the self-relay difference is statistically clear (Fisher p ≈ 0.01). Most real failures in both arms are about what the brain said, not delivery. Details per run: docs/research/reply-as-function-response-2026-10-03.md (on the #117 branch).

## Proposed next changes, labelled

Per the CEO's instruction (relayed 2026-10-03): label each change as a **rail** (stops a runaway or unsafe action), **information** (a fact the model cannot see) or **judgment in code** (word lists, regexes, thresholds deciding what someone meant). No new rules of the third kind until the voice-layer decision.

| Finding (campaign E) | Proposed change | Kind |
|---|---|---|
| E3: "批准运行 go test" pressed "Yes, and don't ask again", which the CEO never heard | The rail accepts only options the CEO heard or named | rail |
| E4: Vertex 504s are our own deadline; the retry gets < 1 s | Give the first attempt and the retry budgets that both fit (timing) | rail |
| E1: "Stop the Claude worker" → voice stop() says "Nothing was in flight" before the brain stops it | Stop with a named target hands the words to the brain with the stop outcome as a fact | rail + information |
| E2: "About X:" prefix on late replies (code-added text) | Remove the prefix; give the brain the reply's subject as a fact if needed | removes code wording |
| E6: "send it now, don't wait" interrupted the running task | Tell the brain what interrupt does to a running turn | information |
| Thin approval lines on real Claude prompts | Parse and pass more of the prompt to the brain | information |
| E5/N3: announcements self-relayed / voiced as acks | Deliver announcements through a non-user channel (Stage 2: generator call or announcer) — depends on the voice-layer decision | information channel |
| E7: a spawned agent's spoken name changed 3 times in 30 s | Keep names stable; report title changes to the brain as facts instead of renaming | information (no threshold) |

Existing judgment-in-code rules that a briefing-channel design would make removable (inventory, nothing changed): ack cues (`relay.go` ackCues, assistantAckCues; #108, #114), deferral/decline word lists (#108, #114), heard-coverage thresholds (#89 50 %, #101 key terms, #115 content-word 0.5), echo text-guard thresholds (#108), echo-gate energy thresholds (#113), title sanitising for names (#107), fast-path answer words.

## Side effects on the machine

- **Codex was upgraded from 0.158.0 to 0.160.0** by an early tier-3 attempt: Codex opened on its "update available" menu, herdr reported the pane idle, and the instruction's Enter chose "Update now". `~/.codex/config.toml` untouched; the old release is still in `~/.codex/packages/standalone/releases/` (re-point `current` to roll back). #107 added a guard so instructions are never typed into a menu.
- Two early tier-3 pilot batches spent about 230k tokens of the AI Studio Gemini free quota before the bench daemon's env was cut to Vertex and DeepSeek keys.
- Claude Code recorded "trusted" entries for scratch folders in `~/.claude.json` (unavoidable with real workers).
- Codex on Vertex AI does not work (Codex speaks only the Responses API; Vertex's Responses endpoint rejects API keys and does not serve Gemini). Tier 3 used Claude Haiku workers; Codex usage: 89 % → 88 % left.

## Still open

- The voice-layer decision (mouthpiece vs briefing channel) and #117.
- N1 (a push stopping mid-sentence) is reduced, not gone; remaining stalls are ~7 s pauses on Gemini's side.
- Speakerphone without AEC (−16 dB) still fails; the app's AEC case (−40 dB) is clean on the voice layer.
- Device checks: the agent window, spawn by voice, and everything in 0.1.1+ need a TestFlight build and a phone session.
