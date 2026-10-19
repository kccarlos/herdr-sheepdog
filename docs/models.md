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
| **Gemini Live on Vertex AI** | `VERTEX_API_KEY` and a project ([Vertex AI](#vertex-ai)) | Paid per token on the Google Cloud project | The same model and voice as Gemini Live, for Google Cloud users. |
| **GPT-Live** | `OPENAI_API_KEY` | **$0.05 per minute of open session** | The session opens only for speech or a push and closes after 45 s idle (`voice.openai.idle_close_s`). The OpenAI key is for the voice only. |

## Orchestrator

| Orchestrator | Key | Cost | Measured |
|---|---|---|---|
| **Gemini** (default, `gemini-3.5-flash`) | the same `GEMINI_API_KEY` | Free tier; the daily cap is per model | Segment Understood 290/359 = 81 % (CI 77–85), 30 simulated-workday runs |
| **DeepSeek** (`deepseek-flash`) | `DEEPSEEK_API_KEY` | Paid per token | Segment Understood 365/402 = 91 % (CI 87–94), 34 runs. Our measured best. |
| **Vertex AI** (Gemini on Google Cloud) | the same `VERTEX_API_KEY` and project ([Vertex AI](#vertex-ai)) | Paid per token on the Google Cloud project | The same Gemini models as above; the Gemini numbers were measured on Vertex. |
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

## Vertex AI

For people who already use Google Cloud and want the voice, the orchestrator
or both billed to a Google Cloud project instead of an AI Studio key. It is
an option, not a recommendation: the free Gemini key is simpler.

1. In the Google Cloud console, pick or create a project with billing on and
   enable the Vertex AI API (APIs & Services → Library → "Vertex AI API", or
   <https://console.cloud.google.com/apis/library/aiplatform.googleapis.com>).
2. Create a service account in that project with the **Vertex AI User**
   role, then an **API key bound to that service account**, restricted to
   the Vertex AI API (`aiplatform.googleapis.com`).
3. In `sheepdog setup`, step 1, Advanced: choose **Gemini Live on Vertex AI**
   as the voice, **Vertex AI** as the orchestrator, or both. One "Google
   Cloud (Vertex AI)" block takes the key, the project id and, for the
   voice, the location (`us-central1` by default: `gemini-3.8-live` is
   served there and from the `us` multi-region, not from `global`).

The page checks the key as you type: one free `countTokens` call on
`gemini-3.5-flash`, and for the voice a Live connection that is closed as
soon as it opens (nothing is said). It tells you when the key is rejected,
the project is not found or the API is off, the location lacks the Live
model, or Google has declined the project under its terms of use (a
decision on the Google Cloud account that Google Cloud support can review).

The key is saved in the env file (`VERTEX_API_KEY`), the project and
location in `config.toml` (`[gemini.vertex]`). A saved key is only reused
for the project it was saved for. The orchestrator always calls Vertex at
`global`; Vertex does not list its models for an API key, so the page
offers the models we checked there. Details: [CONFIG.md](../CONFIG.md).

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
