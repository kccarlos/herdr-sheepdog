# Design: the Anthropic orchestrator adapter

Status: **implemented, 2026-10-19** (design 2026-10-18, #283). Step 4 of [plan-model-setup.md](plan-model-setup.md#the-anthropic-orchestrator-native-adapter); the plan says why and what. This note fixes how the adapter is built before the code lands. No quality numbers yet: the eval runs once there is a key.

The adapter is `internal/brain/anthropic.go`: a `MessageModel` (and `Model`, `ModelCatalog`) over the official Go SDK, `github.com/anthropics/anthropic-sdk-go`, not an OpenAI-compatible shim. Provider id `anthropic`, default model `claude-opus-5-5`.

The sources are the Claude API guide's model-migration notes: "Migrating to Claude Opus 5.5" (breaking changes 1–3, effort, safeguards) and, for the preserved-thinking rules that section says "apply verbatim", "Migrating to Claude Fable 5.1 from Claude Fable 5", breaking changes 2 and 3. The quotes below come from those sections.

## 1. Mapping the brain's window to the Messages API

The loop hands the adapter the window (`[]Message`) and a `genai.GenerateContentConfig` (system text, tools, answer-only tool config, thinking level), exactly as it does for DeepSeek's `OpenAICompatModel`. The adapter returns a `genai.GenerateContentResponse` and a raw message to store on the transcript (`Message.Raw`, with `Provider`/`Model` set by the loop).

| Brain | Messages API |
|---|---|
| `cfg.SystemInstruction` (the stable system text + `asm.SystemAddition`) | top-level `system`: one text block, `cache_control` on it |
| `cfg.Tools` (function declarations) | `tools`: one custom tool each, `strict: true`, `input_schema` from `schemaOf` made strict (below), sorted as declared (the declaration order is fixed) |
| context messages (said, heard, push, worker event, request, note, facts note, compaction summary, event screens) | `user` text, one line each (`contextLine`), consecutive lines joined into one text block |
| the state block (`KindState`, last message) | a separate `text` block at the end of the last `user` message |
| a step's tool calls (`KindToolCall`) | one `assistant` message. When this provider and model produced it **in the current turn**, the content blocks the API returned, replayed byte for byte from `Raw` (`thinking` with its signature, `text`, `tool_use` with its `input` as raw JSON). Otherwise rebuilt from the neutral fields (`tool_use` blocks, plus a `text` block when the call had text), with no thinking |
| tool results (`KindToolResult`) | `tool_result` blocks in **one** `user` message for the whole step, `tool_use_id` = the call's id, `is_error: true` when `ToolResult.Error` is set; text that follows in the window (a facts note, "Time is up", the state) goes after the `tool_result` blocks in the same message |
| a reply (`KindReply`) | `assistant` text; in the current turn (the bad-JSON correction), the API's blocks from `Raw` |

Rules the API imposes and the mapping keeps:

- **Alternation.** Consecutive `user` items merge into one message; the request never ends on an `assistant` message (no prefill: a 400 on Opus 5.5). The window always ends on a user line (the request, a tool result or the state), so this holds by construction; the adapter checks it and fails the call rather than send a prefill.
- **Tool ids.** A call another provider made may have an empty id or one the API rejects (`tool_use.id` must match `^[a-zA-Z0-9_-]+$`). The adapter derives a stable id from the call's turn, position and name (`toolu_sd_<hash>`) and uses the same derivation for the matching result, so the request is the same bytes on every step.
- **No empty text blocks.**
- **Strict schemas.** `strict: true` requires `additionalProperties: false` on every object and accepts no numeric or string-length constraints. `strictSchema` walks `schemaOf`'s output: it adds `additionalProperties: false`, turns `nullable: true` into a `["T", "null"]` type and drops anything strict mode refuses. A unit test runs it over every declaration in `toolDeclarations` (read-only and full).
- **No eager input streaming.** Nothing reads partial tool input, and the field turns off the server-side validation that `strict` gives.
- **Response.** Blocks are read by `type`, never by position: `text` becomes a text part, `tool_use` a function call part (its id, name and `input` parsed into `Args`), `thinking` is not shown (the brain reads no reasoning; `display` stays at the default `omitted`, so the blocks are empty strings with a signature). `stop_reason` maps onto the finish reason: `end_turn`/`tool_use` stop, `max_tokens` max tokens, `refusal` safety (section 5).
- **Raw.** For a step with tool calls and for a reply, `Raw` holds `{"content": [<the blocks as the API returned them>], "state": "<the state text sent before it>", "seen": <highest Seq in the request>}` (section 2 says why the last two). The first tool-call message of a step carries it, as for Gemini and DeepSeek.
- **`Generate` over genai contents** (compaction summary, eval harness): plain user/assistant text, tool calls and results rebuilt from the parts, no thinking replay, effort `low`.

## 2. Preserved thinking: the decision

On Opus 5.5 "thinking blocks must be passed back unchanged", and each block's signature "records the conversation prefix that produced it - the top-level `system` prompt, the set of tools in `tools`, and every message before the block". Where the check is enforced (every account created on or after 2026-08-31, which includes most new sheepdog users), "a request that replays an invalidated block is rejected with a 400". Sheepdog is self-hosted with the user's own key, so the adapter must assume enforcement.

### Where the brain edits history

Pruning and compaction are not the only edits. The brain changes what it sends in six places:

| Edit | When | Effect on a replayed block |
|---|---|---|
| **The state block.** `withState` appends the state (roster, record, time) as the last message on every step and never ingests it; step N+1 drops step N's copy and appends a new one | every step of every multi-step turn | removes text from before the step-N assistant turn: every step-N block is invalid on step N+1. This is the main problem, independent of pruning |
| **Late context.** `Said`, `Heard`, `Pushed`, `WorkerEvent` ingest from other goroutines while a step is in flight; such a message gets a lower `Seq` than the tool call stored after the response, so the next request has it *before* the assistant turn | often, while the voice is speaking | an insertion before the block |
| **`Retract`** (`Echo`) | rare, any time | removes a message |
| **Background compaction.** `afterTurn` releases the turn lock before `Compact`, so the summary can be swapped in during the next turn | when the window passes `brain.window_tokens` | "background (async) compaction … fails the same way" as keep-tail compaction |
| **Pruning** (`brain.prune_after_turns`) | after a turn, older turns only | "deleting old tool results" invalidates every later block |
| **Tools and system between turns.** Event turns get `readTools`, utterance turns all tools; the stable system text changes when an agent joins or leaves (USER.md aliases); `asm.SystemAddition` can change | between turns | "rebuilding the top-level `system` prompt or `tools` array" invalidates every later block |

### The three options

- **(a) Server-side context editing or compaction instead of local pruning.** Compaction's default trigger is about 150 K tokens; the brain keeps a 12 K window (`brain.window_tokens`) for voice latency, and its compaction summary is part of the product (the record line, the Supervisor tab). Context editing helps only once local pruning is off, so (a) becomes (b). It also leaves the state block, late context and the per-turn tool set untouched, and those are the edits that matter inside a turn.
- **(b) Append-only history for this provider, local pruning off, the window kept by server-side compaction.** The context engine would become provider-dependent: shared code, a second window policy to test and evaluate, and a failover from Anthropic to DeepSeek or Gemini would then land on an unpruned window. It also still needs the state-block and tool-set changes. Too much shared change for one adapter.
- **(c) Replay thinking only where the prefix is provably unchanged, and drop it everywhere else.** The guide allows it: "removing a *leading* run of thinking blocks, oldest first" keeps later blocks valid, and the documented recovery is to "strip every `thinking` and `redacted_thinking` block from the history (each turn's `text` and `tool_use` blocks stay)". Client-side compaction is recommended in its "simple" shape, which "replay[s] nothing else - no earlier turns, no earlier thinking blocks", and for keep-tail compaction the fix is to "strip the thinking blocks from the retained turns (text and tool calls can stay)".

**Decision: (c), made safe inside the turn, with two backstops.** It needs no change to the shared engine, it is the same rule the DeepSeek adapter already follows for `reasoning_content` (replayed for the newest turn only), and every edit in the table except the first three happens between turns, where (c) has already dropped the thinking.

1. **Thinking only for the current turn.** The adapter replays `thinking` blocks only from its own `Raw` (same provider and model) of messages whose `Turn` is the newest in the window. Every earlier turn goes back as its `text` and `tool_use` blocks only. Removing all earlier turns' blocks is removing a leading run, which the guide allows. The current turn's blocks were made after the last pruning, compaction, tool-set change and system change, so those edits never touch a replayed block.
2. **The turn itself is append-only.** This deals with the first three rows:
   - *State block:* each assistant message's `Raw` stores the state text that was sent just before it (`state`). On replay, the adapter puts that copy back where it was (a text block at the end of the preceding user message) and appends the current state at the end. Earlier copies stay in place, which is the guide's own replacement for a per-turn reminder: "append the reminder as a text block after the `tool_result` blocks and leave earlier copies in place".
   - *Late context:* each `Raw` stores the highest window `Seq` its request contained (`seen`). On replay, a context message of the current turn's region with a higher `Seq` that sits before that assistant message is moved after the step's tool results, the same deferral `modelOrder` already applies to context that arrives during a step.
   - Within a turn the stable system text, the tool set and the window's earlier messages do not change (pruning runs in `AfterTurn`, compaction after the lock is released). `asm.SystemAddition` is empty with the default engine.
3. **Backstop 1: `drop_block`.** Every request to a model that thinks sends the `thinking-binding-controls-2026-08-01` beta with `thinking.block_binding.prefix_mismatch_behavior: "drop_block"`. For what is left (a `Retract`, or a background compaction that lands mid-turn), "the API drops the first mismatched block and every thinking block after it … the request proceeds". Setting the field explicitly also makes the behaviour the same on old accounts (which are otherwise not enforced) and new ones, so tests against a fake server and real use agree. Each response's `input_transformations` is counted, and the adapter logs `brain.thinking_dropped` (count and reasons) when it is non-zero, so the leftover cases are measured, not assumed. The retry of backstop 2 logs the same event with `reason: retry_without_thinking`.
4. **Backstop 2: strip and retry once.** A 400 whose message says a `thinking` block's signature is invalid or "bound to a different conversation", or that rejects the `anthropic-beta` value (a proxy or gateway at a custom `base_url`), is retried once without any thinking blocks and without the beta. A retry on the same body would fail the same way; the stripped body cannot fail the check. The retry stays in the same model call, so failover and the step budget see one call.

What (c) costs, said plainly: earlier turns' reasoning is not carried forward (the turn's text, tool calls and results are), and each new turn rewrites the messages cache from where the previous turn's thinking was dropped. Voice turns are short and the brain's window is already rebuilt every turn (state, pruning, compaction), so little reuse is lost; the system and tools prefix still caches (section 4). If the eval shows that cross-turn reasoning matters, the follow-up is (b) with server-side compaction, measured.

Model switches need no extra rule: a block from another model or provider is never replayed (rule 1 checks provider and model), and a block the fallback model cannot read is dropped by the API ("the request succeeds, dropped blocks aren't billed").

## 3. Effort, streaming and timeouts

- **Thinking.** Opus 5.5 always thinks: `thinking: {type: "disabled"}` and `budget_tokens` are a 400 at every effort. The adapter sends `thinking: {type: "adaptive", block_binding: …}` and never `disabled`, whatever the brain's thinking level says.
- **Effort per turn kind**, through `output_config.effort`:
  - **event and announce turns** (`TriggerEvent`, which the loop marks with the minimal thinking level; also the compaction summary): `low`. The setting is `anthropic.event_effort`.
  - **utterance and investigation turns**: `medium`, the API's default for Opus 5.5, set explicitly. The setting is `anthropic.effort`.

  Both values are starting points to tune with the eval. A top-level effort change between requests "does invalidate the prompt cache" for messages; it does not invalidate thinking blocks ("changing any request parameter outside `system` / `tools` / `messages` (… `output_config` incl. `effort` …)" keeps them valid). The cheaper per-message form (beta `mid-conversation-output-config-2026-07-01`) is a later optimisation, to measure first.
- **Models without these features.** `fallbacks`, `block_binding` and effort are sent only to the current model families that take them (Opus/Sonnet/Haiku/Fable 5.x). An older model id someone types gets a plain request: no thinking field, no effort, no fallbacks. Haiku 5.5 is excluded from fallbacks because "refusals have no server-side fallback" there.
- **Streaming.** Requests stream (`Beta.Messages.NewStreaming`, the events accumulated into the final message). Replies are short JSON, but thinking counts toward `max_tokens` and a stream keeps a slow turn from hitting a server-side HTTP timeout. `max_tokens` is 16000, enough for thinking at `medium` plus the reply.
- **Timeouts.** No client timeout and `option.WithMaxRetries(0)`: the turn's context is the only bound (`brain.utterance_timeout_s`, `brain.announce_turn_timeout_s`, the failover reserve), and the SDK's own retries on 429/529/5xx would spend the turn's budget behind the failover's back and break the brain's no-retry rule for a 429. The answer-only step (`brain.step_reserve_s`) is unchanged: the loop decides when it happens and the adapter only maps it (below).
- **Answer-only step.** `tool_choice: {type: "none"}` with the tools still declared (removing them would change the prefix the current turn's blocks are bound to). Forced `any`/`tool` is never sent (a 400 on Opus 5.5).
- **Latency to measure.** An ordinary announce turn has 6 s (`brain.announce_turn_timeout_s`) and the failover keeps 8 s for its retry; Opus 5.5 always thinks. Whether event turns at `low` fit is the first thing the eval measures. No default timeout changes before that.

## 4. Caching

Render order is `tools` → `system` → `messages`, and the daemon's system text is already stable across turns (`TestSystemTextIsStable`: the roster and time live in the state block, not the system prompt). Breakpoints:

1. on the system block: caches tools + system together (the guide's "cache_control after tools + system");
2. on the last block **before** the state text in the last user message: caches the window so far, so the next step of the same turn reads it back.
3. and 4. **across turns**, on the end of the prefix that pruning has finished with. Anthropic reads the cache only at a breakpoint (with a lookback of at most 20 blocks), and every turn changes the window two turns back: pruning (`brain.prune_after_turns`, P) stubs that turn's tool results, and the previous turn loses its thinking. So breakpoint 2 alone, at the newest block, never matched on the next turn, and only tools and system were read across turns (61 % hit rate in [eval/anthropic-opus-2026-10-19.md](eval/anthropic-opus-2026-10-19.md)). In turn `cur`, everything before turn `cur-P` is already pruned and has no thinking, so later turns send it unchanged. `messagesOf` marks the last block before turn `cur-P` (written now, read next turn) and the last block before turn `cur-P-1` (the previous turn's mark, read now); a replayed text or `tool_use` block takes `cache_control` too. With the system breakpoint, that makes at most 4. The first turns (`cur` ≤ P+2) have nothing stable to mark, and a compaction rewrites the prefix once. `TestAnthropicCacheBreakpointsStableAcrossTurns` checks the placement against the fake. A live smoke run (workday-morning-standup, 1 run, 2026-10-19) read **78 %** of its prompt tokens from the cache, against 56–57 % for the same scenario's three runs before the fix. The cost estimate was **$1.01** for 650 K prompt tokens, against $1.19–1.29 for 454–498 K before (that run had more steps).

Pruning in batches was the alternative. It changes the shared context engine for one provider, so it was not chosen.

**Warm-up.** The first turn of a run used to pay for the cache write of tools + system in its own latency (the first announce took 6.7 s at the median in the eval and timed out in both smoke runs). `Brain.WarmCache` (`internal/brain/warmup.go`) now writes that prefix ahead of time, in the background: once at the end of `supervisor.New` (the roster is loaded by then, so the system text names the same agents) and after a model switch. It runs only when the current Model implements `CacheWarmer`, which only the Anthropic adapter does. It sends two requests, the event-turn prefix first (read-only tools, `anthropic.event_effort`) and then the utterance-turn prefix (all tools, `anthropic.effort`). Each is the request a turn's first step would send, built by the same functions (`systemPrompt`, `turnConfig`, `systemWith`, `params`: same model, betas, thinking, effort, fallbacks, tools and system with its breakpoint), with `max_tokens: 0`, not streamed (`max_tokens: 0` is a 400 with `stream: true`), and one placeholder user line that carries no breakpoint. The API runs the prefill, writes the cache and bills no output. A failure is logged as `brain.warmup` with its `error`, a debug-level line in the runtime log, and nothing else happens. `TestAnthropicWarmCacheMatchesTurns` checks against the fake that `system`, `tools`, `thinking`, `output_config`, `model`, `fallbacks` and the beta header are the same bytes as the next real event turn and user turn.

Measured live on 2026-10-19, with a fresh brain and a roster alias used only once, so that each system text was new:
- After the warm-up, the first announce read 6,432 of its 7,031 prompt tokens from the cache. Without it, step 1 read nothing.
- The turn took 1.9 / 2.5 / 2.4 s, against 3.1 / 2.6 / 2.7 s cold, about 0.3–1 s saved.
- The two warm-ups wrote 6.4 K and 8.3 K tokens (about $0.07 at the write price) and took about 2 s each.

The warm-up does not help in two cases:
- **A turn that starts during the warm-up.** `eval sim` seeds its workers right after the supervisor starts, so its first announce starts within milliseconds. In a smoke run (workday-morning-standup, 1 run) that announce still took 6.8 s, and the hit rate was 75 %, against 78 % for the run in the paragraph above.
- **A change of roster.** The stable system text names the agents (USER.md's aliases), so an agent joining or leaving rewrites the whole prefix. In that sim run the utterance warm-up wrote 5 agents, and the first user turn already had 6. Taking the aliases out of the system text would fix this for every provider. It is a prompt change and needs an eval of its own.

**Not done: re-warming after idle.** The cache lives 5 minutes, refreshed by every read, so the first turn after a longer quiet spell is cold again. A keep-alive costs a cache read (about $0.003 for both prefixes) every few minutes for as long as the daemon is idle, about $1 a day, whether or not a turn ever comes. That spend is the user's call, so it is not a default. The cheaper option to measure first is the 1-hour TTL on the system breakpoint: the write costs 2x instead of 1.25x, and any turn within the hour keeps it warm.

The tool set differs between event turns (read tools) and utterance turns, so the two kinds keep two cached prefixes; that is cheaper than declaring the full set on event turns and forbidding writes in the prompt (a rail belongs in code). The 512-token minimum is far below the system prompt's size.

`brain.step` also logs `served_model`, the model that answered: the response's top-level `model`, or, after a mid-output switch, the last `fallback` block's `to` model (a stream's `message_start` names the model that started). It differs from `model` when a server-side fallback served the step, so fallbacks can be counted from the event log. The transcript's raw message keeps the same value (`served`).

Usage is mapped onto the genai usage so `brain.step` logs it unchanged: `prompt_tokens` = `input_tokens` + `cache_read_input_tokens` + `cache_creation_input_tokens` (Anthropic's `input_tokens` excludes cached tokens), `cache_hit_tokens` = `cache_read_input_tokens`, `completion_tokens` = `output_tokens`. Cache writes are counted in `prompt_tokens` only; the brain's shared step log is unchanged.

## 5. Refusals, fallbacks and account problems

- **Server-side fallbacks** are on by default: beta `server-side-fallback-2026-07-01` with `fallbacks: "default"`, which "retries on the model Anthropic recommends for that category". The user docs say so (docs/models.md, docs/privacy.md). A fallback runs without Opus 5.5's thinking blocks, which the API drops. `brain.step`'s `served_model` names the model that served a step (section 4).
- **Refusal.** `stop_reason` is checked before the content is read. A response that still ends in `refusal` (the fallback also declined, or the category has none, such as `reasoning_extraction`) is an error, `brain: anthropic: refused (category …)`, with no content used: the turn fails and the daemon speaks its own sentence, as for any failed turn. It is not an account problem and not a reason to fail over (another provider would answer what Anthropic's classifier declined, which is not the adapter's decision to make). `brain.step` logs the category.
- **HTTP errors.** The SDK's `*anthropic.Error` is converted into the brain's `*APIError{Provider: "anthropic", Status, Message}`, so `AccountProblemOfErr`, `failoverReason`, `isRateLimited` and `refusedCode` work unchanged:

  | Status (error `type`) | Brain |
  |---|---|
  | 401 `authentication_error`, 403 `permission_error` | `ProblemInvalidKey`: the provider is marked down (`brain.provider_down`), the user hears which key to fix, failover answers meanwhile |
  | 402 `billing_error`, or a 400 saying the credit balance is too low | `ProblemBalance`: same path, "top up the account" (`AccountProblemOf` learns the wording and the error type) |
  | 429 `rate_limit_error` | wrapped in `ErrAllKeysRateLimited` and logged as `brain.rate_limited` (one key, nothing to rotate), with `retry-after` when sent; fails over |
  | 529 `overloaded_error`, 5xx | fails over (`http_529`), no account alert |
  | other 4xx | a plain error: the turn fails, no failover |
- **`ServiceOf("anthropic")`**: "Anthropic", `ANTHROPIC_API_KEY`, `https://console.anthropic.com/settings/keys`, so alerts and doctor name the right key.
- **No key in logs.** The key goes only in `x-api-key`; errors carry the API's message, never the request.

## 6. Configuration, models and wiring

```toml
[anthropic]
# api_key from ANTHROPIC_API_KEY (never in the file the daemon writes)
base_url = "https://api.anthropic.com"   # SHEEPDOG_ANTHROPIC_BASE_URL
effort = "medium"                        # utterance turns; SHEEPDOG_ANTHROPIC_EFFORT
event_effort = "low"                     # event/announce turns; SHEEPDOG_ANTHROPIC_EVENT_EFFORT
fallbacks = true                         # server-side refusal fallbacks; SHEEPDOG_ANTHROPIC_FALLBACKS
```

- Provider id `anthropic`: `brain.provider = "anthropic"` (default model `claude-opus-5-5` when `brain.model` is not set), `sheepdog brain-test --provider anthropic`, the `/debug` picker, failover (after the providers already listed; `DefaultModel()` returns `claude-opus-5-5`).
- **The client takes nothing from the environment.** The SDK's default client reads `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL` and an `ant` login profile. A Claude Code user often has `ANTHROPIC_BASE_URL` or `ANTHROPIC_AUTH_TOKEN` set, and with both a key and a token the API answers 401. The adapter passes the key and base URL from the config explicitly and clears the SDK's environment defaults; tests set those variables to sentinels and assert that the fake server saw only `x-api-key` with the test key.
- **Models** come from `GET /v1/models` (the SDK's `Models.List`, free): `ModelInfo{Name: id, DisplayName, InputTokenLimit: max_input_tokens, OutputTokenLimit: max_tokens}`, with `claude-opus-5-5` first and every `claude-*` id recommended. The same call is the doctor's key check (Anthropic exposes no balance) and the setup page's "Find models".
- **Setup page.** The Anthropic preset replaces the `ANTHROPIC PRESET GOES HERE` marker in `internal/onboard/presets.go`: native, key `ANTHROPIC_API_KEY`, default model `claude-opus-5-5`, label "★ most recommended", "Test tool calling" with the same tiny probe as the other presets (one tool, `tool_choice` auto, the prompt asks for the call). The page's own routes are `POST /api/setup/anthropic-models` and `POST /api/setup/anthropic-probe`; they take a key and a model, never a host, so a saved key only goes to the configured base URL.
- **Docs.** CONFIG.md `[anthropic]`; docs/models.md (what it costs: Opus 5.5 $4 / $20 per MTok input/output, cache reads $0.20; Sonnet 5.5 $2 / $10; Haiku 5.5 $0.10 / $0.50, first-party prices, 2026-10); docs/privacy.md (what goes to Anthropic: the system prompt, the window, tool results such as screen excerpts, the key).

## Tests (fake server only)

There is no Anthropic key for live tests. An `httptest` server speaks the Messages API's SSE stream and **enforces the prefix check itself**: each thinking block's signature is a hash of `system` + `tools` + every message before it, and a replayed block whose prefix hash does not match is answered with the API's 400 text (or dropped and listed in `input_transformations` when the request sets `drop_block`). Scenarios run through the real loop: a multi-step tool turn with state blocks, late context during a step, a read-only event turn after a user turn, pruning and compaction between turns, a `Retract` mid-turn. The assertions: no request is ever rejected; the current turn's blocks are replayed unchanged; earlier turns carry none; effort per turn kind; `cache_control` placement; `tool_choice` none on the answer-only step; refusal → error; 401/402/429/529 → the account problem or failover above; the models list; the key in no log line, error or response.

What the fake cannot show, and the first live run must: that real signatures accept the reconstructed state block (byte-identical text is the claim), real latency of event turns at `low`, and the cache hit rate.
