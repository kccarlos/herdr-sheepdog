# Orchestrator and Simulated-User Model Options

## Decision needed

1. **Orchestrator model (`brain.provider` / `brain.model`):**
   - **Option A (Recommended): Run the $10 fixed-budget tier-1 comparison below before changing the default**, testing `gemini-3.8-flash` (Vertex), the current small GPT chat model (`gpt-*-mini` via OpenAI), and local Ollama `gemma4:latest` side by side against the existing `deepseek-flash` baseline. Switch the default only if a candidate clears the acceptance bar below (segment **Understood ≥ 89 %**, zero **Safe** regressions across 15+ `workday-scale-ten` runs, and event latency fitting the 6 s announce budget).
   - **Option B: Keep `deepseek-flash` as primary and add a backup chain now** (cloud secondary + local Ollama `gemma4:latest`), avoiding any prompt or timeout retuning unless DeepSeek is dropped completely.
   - **Option C: Switch off DeepSeek immediately without a new bake-off.** Fastest zero-code move is Vertex `gemini-3.8-flash` or OpenAI `gpt-*-mini`, accepting unmeasured quality (`gemini-3.5-flash` scored 81 % vs DeepSeek's 91 % in [vertex-2026-10-11.md](../eval/vertex-2026-10-11.md)).
2. **Bench and eval simulated user (`--simuser`):**
   - **Option A (Recommended): Use Vertex Flash-Lite (`--simuser vertex`, `gemini-3.1-flash-lite` / `gemini-3.5-flash-lite`) when Vertex is available, and validate local Ollama `gemma4:latest` (`--simuser ollama`) in Gate 2 as the $0 offline/fallback simulated user.** Both backends already exist in `internal/eval/llm.go`. Hold `--simuser` fixed across every arm of an orchestrator comparison.
   - **Option B: Add an `openai` small-model backend to `internal/eval/llm.go`** so `--simuser` can run on OpenAI when Vertex is unavailable and local GPU contention with an Ollama orchestrator must be avoided.
3. **Comparison budget and small harness wiring:**
   - Approve up to **$10 USD** total API spend (enforced with `--budget-usd`, using `claude -p` Sonnet on the existing subscription for $0 API judge spend) and the small CLI/eval changes needed before running: allow `[[brain.openai_compat]]` providers in `sheepdog eval sim --live`, add real OpenAI model prices to `eval.Prices`, and optionally pass `reasoning_effort` / thinking-off on event turns for generic OpenAI-compatible providers.
4. **When the primary balance runs out:**
   - **Option A (Recommended): Configure a local + cloud backup now (zero code) and ship four small resilience fixes** — configurable `brain.fallback_order`, startup fallback when the primary key is missing, bare `"yes"`/`"no"` voice fast path when all orchestrator providers are down, and `AccountAlerts` on HTTP/SSE for the `herdr-app` panel.
   - **Option B: Zero-code config only** — register a second provider and/or local Ollama in [CONFIG.md](../../../CONFIG.md) and rely on existing in-step failover.

---

## 1. What the code supports today

### Daemon orchestrator (`internal/config`, `internal/app`, `internal/brain`)

`brain.provider` in [CONFIG.md](../../../CONFIG.md) selects the starting provider; `app.BrainModels` (`internal/app/brain.go`) registers **every** configured provider that has usable credentials at startup so runtime switching (`PUT /api/brain/model`, persisted in `state.json`) and automatic failover can reach any of them:

| Provider ID | Adapter | Config & credentials | Default model | Notes |
|---|---|---|---|---|
| `gemini` | `brain.GenaiModel` (AI Studio) | `[gemini]`, `GEMINI_API_KEY` + `_1`…`_9` pool | `gemini-3.5-flash` (built-in config default) | Rotates keys on HTTP 429 (`withKeys`, one try per key). Free-tier quotas are tight. |
| `deepseek` | `brain.OpenAICompatModel` (DeepSeek mode) | `[deepseek]`, `DEEPSEEK_API_KEY`, `base_url` | `deepseek-flash` | Sends `thinking` + `reasoning_effort` (`low` default) and replays `reasoning_content` within the current turn. |
| `vertex` | `brain.GenaiModel` (Vertex AI) | `[gemini.vertex]`, `VERTEX_API_KEY` + `VERTEX_PROJECT` | `gemini-3.5-flash` | Forces `location = "global"`. Static picker catalog: `gemini-3.8-flash`, `gemini-3.5-flash`, `gemini-3.5-flash-lite`. Single key. |
| `anthropic` | `brain.AnthropicModel` (Messages SDK) | `[anthropic]`, `ANTHROPIC_API_KEY` | `claude-opus-5-5` | `effort = "medium"` (user) / `event_effort = "low"` (events), prompt cache warm-up, current-turn thinking block replay. |
| `<name>` in `[[brain.openai_compat]]` | `brain.OpenAICompatModel` (plain mode) | `base_url`, `api_key_env`, `model`; setup presets for `openrouter`, `openai` (`OPENAI_ORCH_API_KEY` or opt-in `OPENAI_API_KEY`), `ollama` (`:11434/v1`), `lmstudio` (`:1234/v1`), custom URL | Preset-picked (OpenAI picks newest `gpt-N-mini`; local picks first tool-capable model) | Plain `/chat/completions` + `/models`. Key is optional on loopback (`localhost`, `127.0.0.1`, `::1`). Sends **no** `thinking` or `reasoning_effort` fields (`Thinking: nil`). |

### Eval and bench harnesses (`cmd/sheepdog`, `internal/eval`, `internal/bench`)

| Surface | Orchestrator providers wired today | Simulated user (`--simuser`) wired today | Judge (`--judge`) wired today | Current gaps |
|---|---|---|---|---|
| `sheepdog eval --live` (L0 single-turn) | `gemini` (default), `vertex`, `deepseek`, plus any provider in `app.BrainModels` (`anthropic`, `[[brain.openai_compat]]`) | N/A (single-turn fixtures) | `claude` (`claude -p`, default `sonnet`), `vertex`, `ollama`, `fake`, `none` | Help string only lists `gemini`, `vertex`, `deepseek`. |
| `sheepdog eval sim --live` (Tier 1 multi-turn) | `vertex`, `deepseek`, `anthropic`, `gemini` (needs `--allow-ai-studio`) | `vertex` (default `gemini-3.1-flash-lite`, retries alternate `gemini-3.5-flash-lite`), `deepseek` (`deepseek-flash`, thinking off), `ollama` (`gemma4:latest`), `fake` | `claude` (default `sonnet`), `vertex`, `ollama`, `fake` | `parseEvalSimFlags` (`cmd/sheepdog/evalsim.go`) **rejects** `[[brain.openai_compat]]` names (`openai`, `ollama`); `--simuser` has **no** `openai` or AI Studio `gemini` backend. |
| `sheepdog bench run` (Tier 2 voice + scratch daemon) | Defaults to `deepseek` / `deepseek-flash`; `--brain-provider` accepts any provider in `app.BrainModels` (including `openai`, `ollama`) | Same `eval.NewBackend`: `vertex` (default), `deepseek`, `ollama`, `fake` | Same: `claude` (default `sonnet`), `vertex`, `ollama`, `fake` | `faults.brain_*` scenarios wire `faultproxy` **only** to `deepseek.base_url`; `spend.go` defaults missing `brain.step` model names to `deepseek-flash`. |

---

## 2. What is tuned to `deepseek-flash`

Reading `internal/brain/`, `internal/supervisor/`, `internal/bench/`, and `prompts/svp/` shows five places shaped around `deepseek-flash`:

1. **Tight turn and failover timeouts (`internal/brain/brain.go`, `failover.go`):**
   - `announce_wait_s = 2` (worker blocks speak a code-parsed sentence after 2 s if the orchestrator is still thinking).
   - `announce_turn_timeout_s = 6` for normal worker events (`deepseek-flash` measured p50 1.6 s, p90 2.0 s, 0/118 over 6 s in [anthropic-opus-2026-10-19.md](../eval/anthropic-opus-2026-10-19.md); Opus at `low` effort hit p50 4.8 s, p90 7.2 s, missing 6 s on 31/110 events). Owed finishes get `announce_timeout_s = 15`.
   - `reply_wait_s = 2` (past 2 s, voice says it is checking and delivers the answer as a push) and `utterance_timeout_s = 25` (`deepseek-flash` p50 3.0 s, p90 5.6 s), with `step_reserve_s = 3` and `final_grace_s = 3`.
   - `failoverHealthyAttempt = 8 s` and `failoverReserve = 8 s` (`failover.go`), calibrated directly to `deepseek-flash` p95 (8.3 s) and Vertex `gemini-3.8-flash` p95 (8.6 s).
2. **Thinking on for user turns, forced off for event turns (`turnThinking`, `openaicompat.go`):**
   - User utterances run with DeepSeek thinking enabled at `reasoning_effort = "low"`. Worker `announce` turns and compaction summaries pass `ThinkingLevelMinimal`, which DeepSeek maps to `"thinking": {"type": "disabled"}` (cutting ~470 reasoning tokens and ~2.3 s per step), `GenaiModel` maps to `minimal` (retrying once at `low` if rejected, as on `gemini-3.8-flash`), and `AnthropicModel` maps to `event_effort = "low"`.
   - Generic `[[brain.openai_compat]]` has `Thinking: nil`: it sends **no** reasoning control on event turns, so a reasoning-by-default OpenAI small model or thinking-enabled Ollama model spends full reasoning latency inside a 6 s event budget.
3. **Prefix-cache layout and `12000`-token window (`brain.go`, `openaicompat.go`):**
   - `window_tokens = 12000` and `prune_after_turns = 2` assume cheap automatic prefix caching: tools and system prompts stay static at the top while dynamic state (`roster`, `ledger`, `investigations`, `time`) is appended to the final user message. DeepSeek still hit 82 % cached prompt tokens despite pruning two turns back on every turn.
   - DeepSeek's `reasoning_content` is kept in `Message.Raw` only for the current turn (`Message.Turn == cur`) so ~20 KB reasoning traces from earlier turns do not blow past the 12k token window.
4. **System prompts and JSON answer contract (`prompts/svp/SOUL.md`, `RULES.md`):**
   - Iterated on `deepseek-flash` ([eval README](../eval/README.md)): multi-step tool calls (`read_screen`, `prompt`, `send_keys`, `promise`, `investigate`, `recall`) ending in one bare JSON object (`spoken`, `ask`, `detail`, `needs_user`, `options`, `clarify`, `urgency`, `relates_to`, `keeps_promise_ids`, `answers_earlier`), strict brevity (`length_fits` 94 % on DeepSeek vs 85 % on `gemini-3.5-flash`), and automatic English/Chinese switching.
5. **Hardcoded defaults and ordering:**
   - Setup (`internal/onboard/presets.go`) badges DeepSeek `"★ most recommended · 92 % understood in our tests · fast, low cost"` ([models.md](../../models.md)).
   - `failoverOrder` in `internal/brain/failover.go` hardcodes `["vertex", "deepseek", "gemini"]`; `anthropic` and `[[brain.openai_compat]]` entries are not in that list and sort alphabetically *after* those three.

---

## 3. Candidates reachable with this setup

> **Note on pricing:** Live vendor price sheets were not queried for this design, and vendor rates change without notice. Figures labelled *`eval.Prices` code estimate* below come from `internal/eval/llm.go` (comments there warn they are rough estimates, not billing truth). Where the repo has no figure or current live billing is unknown, this table says **unknown**.

| Candidate | Role fit | How this setup reaches it | Measured quality / readiness | Price status | Main risks |
|---|---|---|---|---|---|
| **Gemini Flash** (`gemini-3.8-flash`, `gemini-3.5-flash`; Flash-Lite `gemini-3.1-flash-lite` / `gemini-3.5-flash-lite` for `--simuser`) | Orchestrator & `--simuser` | `[gemini.vertex]` (`VERTEX_API_KEY` + `VERTEX_PROJECT`) or `[gemini]` AI Studio key pool (`GEMINI_API_KEY`…`_9`). Both orchestrator and `--simuser vertex` work today with **no code changes**. | `gemini-3.5-flash` scored **81 %** segment Understood vs DeepSeek **91 %** (−10 pp, CI −15 to −5) in [vertex-2026-10-11.md](../eval/vertex-2026-10-11.md), mainly from wordier replies (`length_fits` 85 % vs 94 %) plus 3 timeouts and 2 fallbacks in 30 runs. **`gemini-3.8-flash` is in the Vertex catalog (p95 ~8.6 s) but ungraded on tier-1 workday.** Flash-Lite is already the default `--simuser`. | Current Vertex Cloud billing rates: **unknown** (must check Google Cloud billing). `eval.Prices` code estimates: `3.5-flash` $0.30/$2.50, `3.8-flash` $0.50/$3.00, `flash-lite` $0.25/$1.50 per MTok (in/out). AI Studio is $0 within free quota, but 429-prone under multi-step load. | `3.5-flash` missed the quality bar; `3.8-flash` rejects `minimal` thinking (adapter retries once at `low`, adding one failed round-trip if not remembered); Vertex had intermittent 429s when parallel > 4. |
| **GPT small models** (e.g. `gpt-5-mini` / `gpt-5.4-mini`, matched by `onboard.SuggestModel`'s `^gpt-(\d+(?:\.\d+)?)-mini$`, or `nano` variants in `/v1/models`) | Orchestrator (ready in daemon); `--simuser` needs ~30 LoC in `eval/llm.go` | `[[brain.openai_compat]]` `openai` preset (`https://api.openai.com/v1`) using `OPENAI_ORCH_API_KEY` or opting in to share `OPENAI_API_KEY` from GPT-Live voice. | **0 eval runs so far.** Daemon and `bench run` support it today via `OpenAICompatModel`; `eval sim --live` needs the `--provider` flag check widened. | **Unknown.** `eval.Prices` has **no** `gpt-*` entries and falls back to `UnknownPrice` ($2.00/$10.00 per MTok), so `--budget-usd` will mis-meter OpenAI until exact model IDs and current OpenAI API prices are checked and added. | `OpenAICompatModel` sends no `reasoning_effort` (`Thinking: nil`): if a GPT `-mini` model reasons by default, 6 s `announce` turns may time out unless `reasoning_effort` is added to `[[brain.openai_compat]]`. |
| **Local Ollama `gemma4`** (`gemma4:latest`) | Emergency / $0 fallback orchestrator & $0 `--simuser` | `http://localhost:11434/v1` (`[[brain.openai_compat]]` `ollama` preset for orchestrator; `--simuser ollama` already built in `eval/llm.go`). No API key needed. | **0 tier-1 workday runs recorded.** Tool-calling support depends on the installed Ollama tag (`onboard.ProbeTools` checks it). | **$0 API spend** (`eval.Prices["gemma4:latest"] = {0, 0}`); local Mac memory/compute only. | Local prefill of 4k–12k prompt tokens + multi-step tool calls + unthrottled thinking may exceed `announce_wait_s` (2 s) and `announce_turn_timeout_s` (6 s). Running **both** orchestrator and `--simuser` on Ollama simultaneously will serialize on one local GPU and inflate latency. |

---

## 4. Cheap fixed-budget comparison plan (≤ $10 total)

### Pre-flight (no API spend)

1. **Small harness prep** (only if Option A is approved): allow configured `[[brain.openai_compat]]` names in `cmd/sheepdog/evalsim.go` (`parseEvalSimFlags`), look up current OpenAI and Vertex token prices to populate `eval.Prices` so `--budget-usd` stops accurately, and add an optional `reasoning_effort` / event-turn thinking-off setting on `[[brain.openai_compat]]` if GPT `-mini` or `gemma4` reasons on event turns.
2. **Hold judge and simulated user constant across orchestrator arms:**
   - Judge: `--judge claude --judge-model sonnet` (`claude -p` on the Claude subscription = **$0 API spend**, calibrated primary judge per [eval README](../eval/README.md)).
   - Simulated user: keep one fixed backend (`--simuser vertex` on `gemini-3.1-flash-lite`, or `--simuser deepseek` if remaining DeepSeek credit is being spent down; never run Ollama for both orchestrator and `--simuser` at the same time).
   - Always pass `--fallback-provider none` so a slow step never silently fails over to another provider mid-arm.

### Staged gates

| Gate | Existing eval set & command shape | Runs per candidate | Hard `--budget-usd` cap | Keep / drop rule |
|---|---|---|---|---|
| **Gate 0: Smoke & latency** | `sheepdog doctor` (tool probe), `sheepdog eval --set regressions --live --provider <cand>`, then `sheepdog eval sim --live --files testdata/eval/sim/workday.json --filter morning-standup --repeat 2 --fallback-provider none --budget-usd 0.50` | L0 regressions + 2 sim runs | **$0.50** per cloud candidate ($0 Ollama) | Drop immediately if tool calls fail, JSON replies fail to parse, or > 25 % of `announce` turns miss the 6 s budget. |
| **Gate 1: Tier-1 orchestrator bake-off** | 1) Benign workday: `testdata/eval/sim/workday.json` (6 non-adversarial scenarios × 3 = 18 runs, `--personas rotate`, matching [anthropic-opus-2026-10-19.md](../eval/anthropic-opus-2026-10-19.md)).<br>2) Safety & adversarial: `workday-adversarial-afternoon` × 3 + `workday-scale-ten` × 12 extra (= 15 total `scale-ten` runs) + `testdata/eval/sim/safety.json` × 3.<br>3) Explanation: `testdata/eval/sim/explain.json` (6 scenarios × 2 = 12 runs).<br>Rejudge with `sheepdog bench rejudge --judge claude --judge-model sonnet`; diff against the 10-19 DeepSeek baseline (or a fresh $0.35 DeepSeek control) via `sheepdog bench northstar --vs`. | 48 tier-1 sim runs per surviving candidate (~2 surviving cloud arms + $0 Ollama) | **$3.00** per cloud arm (~$6.00 max across two cloud arms) | Must meet every threshold in Section 5. |
| **Gate 2: `--simuser` swap check** | Fix `--provider` to the Gate 1 winner; run the 6 benign `workday.json` scenarios × 3 (18 runs, `--personas rotate`) with `--simuser ollama --simuser-model gemma4:latest` vs `--simuser vertex` (and `--simuser openai` if Option 2B is chosen). | 18 runs per `--simuser` candidate | **$1.50** total (Ollama simuser itself is $0; only orchestrator tokens bill) | Must meet the simulated-user bar in Section 5. |
| **Gate 3: Tier-2 audio check (winner only)** | `sheepdog bench run --filter workday --repeat 2 --budget-usd 2.00` (6 workday scenarios × 2 = 12 live voice runs using cached TTS openings in `testdata/bench/openings.json`). | 12 tier-2 runs on the single winner | **$2.00** | Confirms no live-voice regression or timeout cascade before changing defaults. |

**Total maximum spend:** **$0.50×2 + $3.00×2 + $1.50 + $1.50–$2.00 ≈ $9.50–$10.00** hard-capped by `--budget-usd` on every invocation.

---

## 5. What counts as good enough

### To replace `deepseek-flash` as primary orchestrator

1. **Segment Understood (primary north star):** within **−3 pp** of the `deepseek-flash` control on the same binary (**≥ 89 %** against a 92 % control), with the 95 % cluster-bootstrap CI lower bound **≥ 85 %** (`gemini-3.5-flash` at 81 % failed this bar).
2. **Safe (hard gate):** **0** unauthorised `send_keys` across **≥ 15** `workday-scale-ten` runs (`rm -rf ./data` prompt) and `safety.json`; segment `Safe` (judge) non-inferior to DeepSeek (~97 %).
3. **Action accuracy and brevity:** required actions **100 %** (18/18 on benign workday), segment `said_vs_done` **≥ 95 %**, `no_hallucination` **≥ 89 %**, and `length_fits` **≥ 90 %**.
4. **Latency:** `announce` turn p50 **≤ 3.0 s** with **< 10 %** exceeding `announce_turn_timeout_s` (6 s); user `utterance` turn p50 **≤ 6.0 s**, p90 **≤ 12.0 s**, and **< 2 %** hitting `utterance_timeout_s` (25 s).

### To qualify as a zero-balance fallback orchestrator only (e.g. local Ollama `gemma4`)

- **Safe:** still **0** unauthorised `send_keys` (the approval rail is enforced in Go code, never in the prompt).
- **Usability:** required actions **≥ 85 %**, segment **Understood ≥ 75 %**, user-turn p90 **≤ 20 s**. Missing the 6 s `announce` budget on worker events is acceptable in emergency fallback mode because `announce.go` falls back cleanly to deterministic `codePath` sentences after 2–6 s.

### To replace DeepSeek as bench / eval simulated user (`--simuser`)

- **Protocol compliance:** **< 5 %** of turns require `LLMSimUser`'s `declReminder` retry for missing `[ITEM n]` / `[AGAIN n]` / `[ANSWER]` tags (`internal/eval/simuser.go`); clean `end_call` / `wait_for_sheepdog` tool calls; **0** errored or hung runs.
- **Persona & language fidelity:** honours `zh` (speaks Mandarin) and `terse` / `rambling` / `impatient` / `vague` personas.
- **Measurement stability:** on the same orchestrator, segment **Understood** lands within **±4 pp** (cluster-bootstrap noise) of the `vertex` / `deepseek` simulated user.

---

## 6. What happens to users today when the orchestrator fails or runs out of balance

Traced directly through `internal/app/wire.go`, `internal/brain/account.go`, `internal/brain/failover.go`, `internal/supervisor/tools.go`, `internal/supervisor/utterance.go`, `internal/supervisor/fastpath.go`, `internal/supervisor/announce.go`, `internal/web/accounts.go`, and `internal/web/api.go`:

### Case A — Daemon starts when the configured primary provider has no key (`wire.go:99-109`)

- `wire.go` checks `models[cfg.Brain.Provider]`. If that one key is unset (for example, `DEEPSEEK_API_KEY` is missing), **`a.brain` is set to `nil` (`brainOff = "no DEEPSEEK_API_KEY"`) even if other providers have valid keys or local Ollama is configured**.
- **User impact:**
  - Voice utterances hit `noBrain()` (`utterance.go`): the user hears deterministic roster status plus *"The orchestrator is off, so I can only report status."* No instructions (`prompt`) can be sent to workers.
  - Because `s.brain == nil`, bare `"stop"`, `"start over"`, the 60 s bare `"yes"` / `"no"` / `"option N"` voice fast path (`utterance.go:287`), and UI option taps (`POST /api/pending/answer`) all work.
  - Typed chat (`POST /api/turn` in the iPhone app and `herdr-app` panel) returns **HTTP `503 Service Unavailable`**.
  - Worker blocks and finishes use deterministic `codePath` templates (`announce.go`).

### Case B — Runtime HTTP 402 / zero balance (or 401/403 bad key, or exhausted quota) WITH a registered backup (`account.go`, `failover.go`)

- `AccountProblemOfErr` matches HTTP `402` or error messages containing `"insufficient balance"`, `"insufficient_balance"`, `"insufficient credits"`, `"credit balance is too low"`, or `"billing_error"` as `ProblemBalance`.
- `generateStep` logs `brain.provider_down` and marks the primary **down in memory for 30 minutes** (`providerDownFor = 30m`). Within the **same step** (no retry on the broken provider), it fails over (`brain.failover`, `reason: "account_balance"`) to the first healthy registered provider in `failoverOrder` (`vertex`, `deepseek`, `gemini`, then alphabetical for `anthropic` and `[[brain.openai_compat]]`) and completes the turn normally.
- **User impact:**
  - The turn succeeds on the backup model.
  - Right after the turn finishes, `tellAccount` pushes **one** `UrgencyIdle` spoken notice (in English or Chinese, deduplicated in `AccountAlerts` until cleared): *"DeepSeek says the account balance is used up, so the orchestrator can't use it and a backup model answers for now. Top up the DeepSeek account."* The vague 3-turn `"My usual model is having trouble"` notice is suppressed while the provider is marked down.
  - `web/accounts.go` sends a `/ws` `error` frame (`problem: "balance"`, `role: "orchestrator"`, `provider`, `message`, `fix: "top up the account (https://platform.deepseek.com)"`) to `/voice` and the iPhone app, and re-sends active alerts on `/ws` reconnect. `/debug` (`GET /api/brain/models`) marks the provider `(down)` with `down_until`.
  - For the next 30 minutes, every turn routes straight to the backup at `step: 0` (`reason: "provider_down"`). After 30 minutes, one turn probes the primary again; if it still returns `402`, it is marked down for another 30 minutes and fails over again **without** repeating the spoken alert.

### Case C — Runtime HTTP 402 / zero balance WITHOUT any backup (or all backups also fail)

- **Spoken user turns (`to_supervisor`):** `Turn` fails with `ErrFallback`. `Supervisor.fallback` (`tools.go:85`) checks `AccountAlertFor(out.err)` and returns the specific spoken alert at `UrgencyInterrupt` instead of *"say it again"*: *"DeepSeek says the account balance is used up, so the orchestrator can't use it. Top up the DeepSeek account."* (`Detail` also instructs the voice model not to retry `to_supervisor`). Because `routeAroundDown` has no healthy backup to route to, **every subsequent spoken request** hits the down provider again and repeats that balance message.
- **Typed chat (`POST /api/turn`):** `TypedTurn` runs synchronously and returns **HTTP `200 OK`** with `acted: false` and `reply` set to that same balance error sentence.
- **`herdr-app` panel visibility gap:** the `herdr-app` desktop panel uses HTTP + SSE (`/api/events`), never `/ws`, so it does **not** receive the proactive `/ws` `error` banner — a panel user only sees the balance failure after sending a typed message (or running `sheepdog doctor`, which checks DeepSeek `GET /user/balance` and OpenRouter `GET /key` in `internal/onboard/onboard.go`).
- **Worker events (`announce.go`):** `s.brain.Turn` fails and falls back cleanly to `codePath` — blocked panes still speak the parsed approval prompt after `announce_wait_s` (2 s), and finished panes still speak `"<alias> finished."` (plus the one-time spoken balance notice if an event turn was first to hit `402`).
- **Sharp edge — answering worker prompts by voice breaks while UI taps still work:** tapping an option button (`POST /api/pending/answer`), saying `"stop"`, and saying `"start over"` still work without the orchestrator. **Saying `"yes"` or `"option 2"` by voice does not:** in `utterance.go:287`, `fastAnswer` runs only when `s.cfg.bareAnswers || s.brain == nil`. Because `s.brain` is non-nil, a spoken `"yes"` is sent to `s.brain.Turn`, hits `402`, and answers with the balance error instead of sending keys to the pane.

### Case D — Transient provider errors (5xx, timeout, per-minute 429, dropped connection)

- HTTP `499` or wire errors (`ECONNRESET`, `EOF`) retry **once** on the same provider (`brain.retry`).
- If a backup exists, `firstAttempt` caps the primary attempt at `8 s` (reserving `failoverReserve = 8 s` for the backup) and fails over (`brain.failover`). After 3 consecutive failovers within 10 minutes, the user hears *"My usual model is having trouble; a backup is answering for now."* at most once per 30 minutes.
- With no backup, the user hears *"Sorry, I couldn't get an answer just now. If you say it again, I'll try once more."*, and the question is saved in `unanswered` for one automatic `rerelay`.

---

## 7. Fallback design when the balance runs out

### Immediate zero-code fallback (works on `main` today)

Register at least one secondary cloud provider **and** local Ollama in `~/.config/sheepdog/config.toml`:

1. Keep the chosen paid primary (`brain.provider`).
2. Keep a second cloud credential in `.env` (for example, `[gemini.vertex]` or `[[brain.openai_compat]]` `name = "openai"` with `OPENAI_ORCH_API_KEY`) for full-quality cloud failover when one vendor's balance hits $0.
3. Add a `[[brain.openai_compat]]` entry for `name = "ollama"`, `base_url = "http://localhost:11434/v1"`, `model = "gemma4:latest"` so if all paid balances are exhausted (or the network is down) while Ollama is running, `app.BrainModels` fails over to local `gemma4` in the same step at $0.

### Four small code fixes worth shipping alongside it

1. **Configurable `brain.fallback_order` (`internal/config/brain.go`, `internal/brain/failover.go`):** Let `config.toml` specify `fallback_order = ["openai", "vertex", "ollama"]` instead of hardcoding `["vertex", "deepseek", "gemini"]` + alphabetical tie-breaking (which today puts `gemini` free tier ahead of `openai`, and `ollama` ahead of `openai`).
2. **Startup fallback when primary key is missing (`internal/app/wire.go`):** If `brain.provider` lacks its key at startup (or a local primary is unreachable) and another registered provider is usable, start on the first fallback in `fallback_order` and fire an `AccountAlert` instead of setting `a.brain = nil`.
3. **Enable `fastAnswer` when every orchestrator provider is down (`internal/supervisor/utterance.go`):** Gate `fastAnswer` on `s.cfg.bareAnswers || s.brain == nil || s.brain.AllDown()` so within the 60 s `fastAnswerWindow` (still strictly under the approval rail), saying `"yes"`, `"no"`, or `"option 2"` by voice keeps answering worker prompts even when every cloud balance is $0 and Ollama is stopped.
4. **Expose active `AccountAlerts` on `GET /api/state` and `/api/events` (`internal/web/`):** Surface active `AccountAlerts` to SSE/HTTP clients so the `herdr-app` desktop panel shows the same `"balance used up — top up …"` banner as `/voice` and the iPhone app, and optionally run `onboard.Doctor`'s free `/user/balance` / `/key` check once at daemon startup to warn before the first user turn fails.
