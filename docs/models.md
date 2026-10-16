# models

**The two models sheepdog needs, and the keys that run them.**

sheepdog needs two models. They never share a role:

- **The voice** hears you and speaks to you in real time. It has no hands.
- **The orchestrator** (the "brain") reads your agents' screens, investigates,
  remembers and acts on the agents behind code-enforced safety rails.

**The simplest setup is one free Gemini API key** from
<https://aistudio.google.com/apikey>. It runs both. The setup page
(`sheepdog setup`) asks for nothing else.

## Voice

| Voice | Key | Cost | Notes |
|---|---|---|---|
| **Gemini Live** (default) | `GEMINI_API_KEY` | Free tier available, with Google's quota limits | Idle costs nothing. When a limit is hit the setup page says `Gemini (AI Studio) says the quota is used up (HTTP 429…)`: wait for the reset or add more keys. `GEMINI_API_KEY_1` … `GEMINI_API_KEY_9` form a pool that the voice rotates through on a 429. |
| **GPT-Live** | `OPENAI_API_KEY` | **$0.05 per minute of open session** | The session opens only for speech or a push and closes after 45 s idle (`voice.openai.idle_close_s`). The OpenAI key is for the voice only. |

## Orchestrator

| Orchestrator | Key | Cost | Measured |
|---|---|---|---|
| **Gemini** (default, `gemini-3.5-flash`) | the same `GEMINI_API_KEY` | Free tier; the daily cap is per model | Segment Understood 290/359 = 81 % (CI 77–85), 30 simulated-workday runs |
| **DeepSeek** (`deepseek-flash`) | `DEEPSEEK_API_KEY` | Paid per token | Segment Understood 365/402 = 91 % (CI 87–94), 34 runs. Our measured best. |
| **Any OpenAI-compatible endpoint** | its own key (none for a local server) | Whatever the provider charges | Not evaluated. The model **must support tool calling**. |

The two Understood numbers come from different runs on the same tier-1
simulated-workday suite; DeepSeek minus Gemini is +10 points (CI +5 to +15).
The Gemini runs went through Vertex AI. The report is
[dev/eval/vertex-2026-10-11.md](dev/eval/vertex-2026-10-11.md); how every
number is produced is in [dev/eval/README.md](dev/eval/README.md).

OpenAI-compatible examples (each is one `[[brain.openai_compat]]` table in
`~/.config/sheepdog/config.toml`; the setup page's Advanced section writes it
for you):

```toml
[[brain.openai_compat]]
name = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
model = "deepseek/deepseek-v3.2"

[[brain.openai_compat]]
name = "ollama"
base_url = "http://localhost:11434/v1"   # local: no key needed
model = "qwen3:8b"

[[brain.openai_compat]]
name = "groq"
base_url = "https://api.groq.com/openai/v1"
model = "openai/gpt-oss-120b"
```

On the setup page, **Find models** lists the models an OpenAI-compatible endpoint offers (free, and it marks the ones that support tool calling when the endpoint says so), and **Test tool calling** sends one small request to the model you typed (it may be billed, and it only runs when you press it). Typing the model name by hand always works.

`sheepdog doctor` sends a few tokens to check that the model can call tools.
Skip it with `sheepdog doctor --no-probe`.

## What goes to whom

| Provider | Receives | When |
|---|---|---|
| Voice (Google or OpenAI) | Your microphone audio, and the briefings the daemon writes for the voice | While a voice session is open |
| Orchestrator (Google, DeepSeek or your endpoint) | What you said as text, the agents' screens, session records and command output the orchestrator reads | On each of your questions and each agent event |
| sheepdog's push relay, then Apple | Phone notifications, end-to-end encrypted: the relay sees the phone's push token, the timing and the kind, never the content | When an agent needs you and a phone is registered (`push.mode`, [iphone-app.md](iphone-app.md)) |
| Nobody else | | No telemetry |

A local Ollama orchestrator keeps agent screens on your machine; the voice
still goes to Google or OpenAI. Details and deletion: [privacy.md](privacy.md).
Check each provider's own terms, especially for free-tier keys.

## Switch models

- **Setup page:** `sheepdog setup`, step 1, Advanced. Saving reloads the
  daemon in place.
- **While it runs:** the `/debug` page header (`sheepdog open debug`) and the
  iPhone app's Settings, Models switch voice and orchestrator at the next
  turn. The choice survives a restart.
- **Config:** `voice.provider`, `brain.provider` and `brain.model` in
  [CONFIG.md](../CONFIG.md), keys in `~/.config/sheepdog/.env`.
  `sheepdog config show` prints the effective config with keys redacted.
