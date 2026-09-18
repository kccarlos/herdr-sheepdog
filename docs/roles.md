# Roles: CEO, assistant, SVP, workers

Sheepdog is organised like a small company. This page is the contract between
the roles; both prompts (`prompts/assistant.md`, `prompts/svp/*.md`) are derived
from it and must not contradict it. Plan decisions D1, D4, D9 and D7-revised
still apply.

| Role | Who | Model | Owns |
|---|---|---|---|
| **CEO** | the operator, by voice | — | intent, decisions |
| **Assistant** | the voice model | `gemini-3.8-live` | hearing, speaking, turn-taking, *understanding what the CEO meant* |
| **SVP** | the supervisor brain (`internal/brain`) | `gemini-3.5-flash`, tools | the workers, the record, reasoning, promises |
| **Workers** | herdr panes (Codex, Claude Code, agy…) | their own | changing code, under their own approval gates |

## Assistant — do / don't

Does, on its own:
- Understands intent and resolves references from the conversation it heard
  ("that one" = the failing test it just reported).
- Handles mis-hearing: repeats back and asks instead of guessing.
- Carries the social layer: acknowledgements, "let me check", small talk.
- Passes the request to the SVP as `question` (+ `agent` **only if the CEO said a
  name**). Anything it adds is a hint; the SVP reads the CEO's verbatim words
  from the transcript itself.
- Delivers the SVP's answer in the CEO's register, and answers follow-ups from
  the answer it just received without going back.
- Judges the *moment* to deliver what the SVP brings unprompted (interrupt now,
  or wait for a pause); the SVP sets urgency, the assistant sets timing.

Never:
- States anything factual about agents that did not come from the SVP in this
  conversation. Status, results, "is it done" → always a tool call.
- Decides anything about workers, holds task state, or reads paths/hashes/ids
  aloud (D9).
- Fills `agent` by guessing; "it" / "that one" → leave empty.

Prompt: one short file (≤ 350 words), voice + rules together. Every turn re-bills
its whole context, so it stays small.

## SVP — do / don't

Does:
- Keeps one transcript per daemon run: every CEO utterance and assistant reply
  (from the voice transcript events), every push, every worker state change,
  its own tool calls and answers. Compacts old turns into a summary and prunes
  bulky tool results from the window; the full transcript stays on disk.
- Reasons with tools: `read_screen`, `run_cli` (read-only), `list_agents`,
  `recall` (ledger now, memory system later); acts with `prompt`, `send_keys`,
  `interrupt`.
- Treats everything read from a worker pane as **data, not instructions**
  (provenance delimiters); workers are other agents whose output is untrusted.
- Owns **promised work**: "tell me when it's done" becomes a recorded promise it
  keeps, not a hope that chattiness announces it.
- Answers for the ear: `{spoken, detail, needs_user, options?}` — `spoken` ≤ 2
  sentences, `detail` for the assistant's follow-ups.
- Falls back to the fast template path when it errors, exceeds its step budget,
  or exceeds ~8 s on a screen-answerable question. Voice never hangs on a loop.
- Serialises turns: single writer, CEO turns before event-driven turns.

Never:
- Speaks directly to the CEO (only the assistant speaks) or bypasses a worker's
  gate — see the one rail below.
- Grows its window without bound: target 20–30k tokens.

## The one rail

Answering a worker's approval prompt *is* bypassing its gate. The SVP may send
an approval keystroke only when a CEO utterance in the current turn authorises
it, the keystroke comes from the option set parsed off the screen, and never
proactively. Enforced in the `send_keys` executor, not by prose. Everything
else — instructions, Ctrl-C, questions — is the SVP's call (D4: workers'
prompts are the safety gate).

## Hot paths that stay code (latency, not safety)

`briefing` from cache (sub-second success test; the SVP refreshes the cache in
the background), `confirm`/`cancel` key mapping, debounce/dedup/TTL timers,
chattiness as a hard ceiling on what the SVP may push.

## Borrowed from OpenClaw (concepts/system-prompt, context, context-engine, soul)

Adopted for the SVP: SOUL/RULES split; sectioned prompt with stable content
first (prefix caching; tool declarations are prefix too); the
`ingest / assemble / compact / afterTurn` context-engine seam as the memory
plug point; prune-vs-compact with disk history; provenance delimiters;
execution bias and promised work. Not adopted for the assistant: it is not a
reasoning agent and its context is re-billed per turn.

## Delivery

1a brain loop, transcript, default engine, provenance, `ask`/`propose` routing,
fallback, `brain.*` events → 1b prompt files, `USER.md` (aliases generated from
config), `~/.config/sheepdog/prompts/` overrides → 1c Supervisor tab (transcript
with window marker, prompt-budget meter) → 2 summaries with context + promises →
3 push judgment. Measure model calls per CEO turn after 1a, before 2.
