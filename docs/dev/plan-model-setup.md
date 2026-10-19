# Plan: choosing models in setup (step 1), and the Anthropic orchestrator

Status: **proposal, approved in outline by the CEO on 2026-10-16; not started.** Implementation waits for the weekly Claude quota reset. Builds on #271 (model discovery and the tool-calling probe on the setup page).

## Goals

- Users understand the two parts: the **voice** they talk to and the **orchestrator** that reads their agents and acts. The orchestrator decides most of the experience.
- Guide users to a good orchestrator without blocking anyone. The free one-key path stays one click away and says honestly what it trades.
- No URLs to type for known providers: pick a preset, paste a key (local servers need none).
- Always show which model will be used, with a way to change it.
- GPT-Live is a first-class voice option, not an afterthought.

## Step 1: choose your models

### Starting points (cards; each pre-fills the two sections below, everything stays editable)

| Card | Voice | Orchestrator | Copy (short) |
|---|---|---|---|
| **Recommended** (selected by default) | Gemini Live (free key) | **Anthropic, Claude Opus 5.5** | "The strongest orchestrator: understands your agents best." Needs an Anthropic API key (paid). |
| **Cost-effective** | Gemini Live | **DeepSeek** (deepseek-flash) | "Cost-effective, and it did well in our tests." |
| **Free quick start** | Gemini Live | Gemini (same key) | "One free key for both. Good for trying; can be slow or busy. Upgrade any time." |
| **Custom** | any | any | Pick each part. |

### 1. Voice

| Option | Needs | Shows once the key is valid |
|---|---|---|
| Gemini Live (AI Studio) | a free Gemini key | "You'll talk to gemini-3.8-live, voice Puck", with the Live-capable models this key can use and a voice picker |
| Gemini Live on Vertex AI | Vertex key, project, location | the same, for Google Cloud users |
| GPT-Live (OpenAI) | an OpenAI key | "Billed per minute of open session (about $0.05/min)", model gpt-live-1 and voice marin, both changeable |

### 2. Orchestrator presets

| Preset | Base URL (filled in) | Key | Default model | Label |
|---|---|---|---|---|
| **Anthropic** | Anthropic Messages API (native adapter, below) | yes | **claude-opus-5-5** (also lists Sonnet 5.5, Haiku 5.5 via `GET /v1/models`) | **★ Most recommended** |
| **DeepSeek** | built in | yes | deepseek-flash | **Cost-effective · measured well in our evals** (91 % segment Understood vs 81 % for Gemini 3.5 Flash, docs/dev/eval/vertex-2026-10-11.md); shows the balance |
| OpenRouter | `https://openrouter.ai/api/v1` | yes | pick from the list (tool-capable first) | one key, many models; shows credit |
| OpenAI | `https://api.openai.com/v1` | yes | a small tool-capable model | asks before reusing a GPT-Live key (the OpenAI key was voice-only by the CEO's earlier rule; reuse needs an explicit yes) |
| Gemini | built in (AI Studio) | reuses the voice key | gemini-3.5-flash, changeable from the key's model list | free; can be slow or busy |
| Vertex AI | built in (native provider, not OpenAI-compatible) | Vertex key + project | Gemini Flash / Pro | for Google Cloud users |
| Ollama · local | `http://localhost:11434/v1` | no | the pulled models | detects whether it is running; warns that small local models may not handle tool calling |
| LM Studio · local | `http://localhost:1234/v1` | no | the loaded models | same |
| Other URL | typed | optional | discovered or typed | today's "OpenAI-compatible" option |

Every preset reuses #271's **Find models** and **Test tool calling**. Quality labels quote only our own measurements; everything else says "not yet measured by us".

### Summary bar

Always visible: "You talk to X. Your agents are handled by Y. Cost: …". Updates live. "Save and start" keeps today's flow (in-place reload, "Saved ✓ The voice is connected").

## The Anthropic orchestrator (native adapter)

The orchestrator is Go, so the adapter uses the official Go SDK (`github.com/anthropics/anthropic-sdk-go`), not an OpenAI-compatible shim.

- **Requests:** `POST /v1/messages` with the daemon's tools, streaming where turns can be long. Model default `claude-opus-5-5`.
- **Thinking and effort:** Opus 5.5 always thinks; `output_config.effort` is the only control, default `medium`. Use `low` for event/announce turns and `medium` for user turns, then tune with the eval for latency against the turn budget (`brain.utterance_timeout_s`, the voice's 8 s fallback).
- **Tool choice:** forced `tool_choice` `any`/`tool` returns 400 on Opus 5.5; use `auto` (plus `none` for the answer-only step the brain already has) and `strict: true` tools.
- **Prompt caching:** the daemon already keeps a stable system prefix (`TestSystemTextIsStable`); put `cache_control` after tools + system. Check `usage.cache_read_input_tokens` in logs.
- **Refusals:** check `stop_reason == "refusal"` before reading content; enable server-side `fallbacks: "default"` (beta) and tell the user in docs.
- **Preserved thinking (the main design risk):** on Opus 5.5, thinking blocks must be passed back unchanged, and editing earlier turns invalidates them; accounts created after 2026-08-31 get a 400 on edited history. The brain prunes old tool results and compacts (`brain.prune_after_turns`, compaction). Before coding, read the Claude API guide's preserved-thinking section and choose: server-side context editing / compaction instead of local pruning for this provider, or an append-only history. This decides the adapter's shape. **Decided in [anthropic-adapter.md](anthropic-adapter.md#2-preserved-thinking-the-decision):** thinking is replayed for the current turn only, the turn is kept append-only, and `drop_block` plus one strip-and-retry back it up.
- **Keys and models:** `x-api-key` from `ANTHROPIC_API_KEY`; validation and the model list via `GET /v1/models`; doctor check; `/debug` picker; failover (`brain.provider_down`) like the other providers; account problems (401/402/429) said plainly through `brain.AccountProblemOf`.
- **Cost note for docs:** Opus 5.5 $4 / $20 per MTok input/output, cache reads $0.20; Sonnet 5.5 $2 / $10; Haiku 5.5 $0.10 / $0.50 (first-party API, 2026-10).

## Evidence before claims

- Run the tier-1 workday suite (segment Understood, Done, Safe, latency) for **Claude Opus 5.5** (effort low and medium) against DeepSeek before the UI shows any number for Opus 5.5. The "most recommended" label is the CEO's call; the numbers next to it must come from our runs. Optionally also Sonnet 5.5 / Haiku 5.5 and one OpenRouter model.
- Watch latency: if Opus 5.5 turns exceed the budget at `medium`, the recommendation stays but the default effort drops.

## Work order (one agent at a time; PRs merge on green unless noted)

1. **Presets and orchestrator section** (OpenRouter, OpenAI, Ollama, LM Studio, Vertex, Gemini model choice): config preset table, `[[brain.openai_compat]]` generated from presets, local server detection, OpenAI key-reuse consent. Screenshots for review.
2. **Voice section:** Gemini Live model and voice choice from the key's models, Vertex fields, GPT-Live as an option with its model and voice.
3. **Cards and summary bar** (visual; CEO reviews screenshots before merge).
4. **Anthropic adapter** (design: [anthropic-adapter.md](anthropic-adapter.md); done, PR "feat(brain): Anthropic orchestrator"), with unit tests against a fake server, doctor, `/debug`, failover.
5. **Eval run** for Opus 5.5 vs DeepSeek; update labels and docs/models.md with measured numbers.
6. **Docs:** docs/models.md, docs/quick-start.md, CONFIG.md.

## Open questions

1. Default card: Recommended (Opus 5.5) needs a paid Anthropic account; is the extra signup acceptable for most users, or should "Free quick start" be preselected with Recommended highlighted?
2. OpenAI as an orchestrator: reuse the GPT-Live key only on an explicit yes (proposed).
3. Budget for the eval runs (Anthropic API spend and Claude quota).
