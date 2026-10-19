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
| **GPT-Live** | `OPENAI_API_KEY` | **$0.05 per minute of open session** | The session opens only for speech or a push and closes after 45 s idle (`voice.openai.idle_close_s`). The orchestrator uses this key only if you choose to reuse it ([presets](#presets-on-the-setup-page)). |

### Starting points on the setup page

Step 1 opens with four cards that fill in the two parts below; you can
change anything afterwards, and nothing is saved until you press Save.
**Recommended** is Gemini Live with the Anthropic orchestrator (needs a paid
Anthropic key) and is selected on a first run. **Cost-effective** is Gemini
Live with DeepSeek. **Free quick start** is Gemini Live with Gemini, one free
key for both. **Custom** leaves your choices as they are. A bar above Save
says which models you talk to and what they cost.

### Choosing the voice on the setup page

Step 1 of `sheepdog setup` has two parts: **your voice** and **your
orchestrator**. The voice is three cards: **Gemini Live** (Google AI Studio,
free key, selected by default), **Gemini Live on Vertex AI** and
**GPT-Live** (OpenAI, $0.05 per minute of open session).

Once the card's key checks out (or a key is already saved; for Vertex AI,
once the Google Cloud block checks out), the page says which model and voice
you will hear, for example "You'll talk to gemini-3.8-live, voice Puck.",
with a picker for each:

| Card | Models offered | Voices offered |
|---|---|---|
| Gemini Live | the models your key lists as Live-capable (`bidiGenerateContent` in Google's model list); the configured one if the list fails | Puck (default), Kore, Charon, Aoede |
| Gemini Live on Vertex AI | the configured model and `gemini-3.8-live`, the one we checked on Vertex (Vertex does not list its models for an API key) | the same as Gemini Live |
| GPT-Live | the `gpt-live…` models your key lists; `gpt-live-1` if the list fails | marin (default), cedar, alloy, ash, ballad, coral, echo, sage, shimmer, verse, quartz, ripple, vesper, willow, stone, gleam, meridian, bossa, tempo, beacon, delta, cinder |

The two Gemini cards share one model and voice setting. Google has more
prebuilt voices than the four offered; any of them works in
`gemini.voice`, and a voice set there by hand is offered on the page too.
For Vertex AI, the page checks that the location serves the model you pick.
There is no voice preview: hearing one needs a Live session, which uses
your quota or is billed.

Saving writes what changed to the env file (`SHEEPDOG_GEMINI_MODEL`,
`SHEEPDOG_GEMINI_VOICE`, or `SHEEPDOG_VOICE_OPENAI_MODEL`,
`SHEEPDOG_VOICE_OPENAI_VOICE`, next to `SHEEPDOG_VOICE_PROVIDER` and
`SHEEPDOG_GEMINI_BACKEND`) and reloads the daemon in place. To change the
model or the voice later, run `sheepdog setup` again, or set `gemini.model`,
`gemini.voice`, `voice.openai.model` or `voice.openai.voice`
([CONFIG.md](../CONFIG.md)).

## Orchestrator

| Orchestrator | Key | Cost | Measured |
|---|---|---|---|
| **Gemini** (default, `gemini-3.5-flash`) | the same `GEMINI_API_KEY` | Free tier; the daily cap is per model | Segment Understood 290/359 = 81 % (CI 77–85), 30 simulated-workday runs |
| **DeepSeek** (`deepseek-flash`) | `DEEPSEEK_API_KEY` | Paid per token | Segment Understood 365/402 = 91 % (CI 87–94), 34 runs. Our measured best. |
| **Anthropic** (`claude-opus-5-5`) | `ANTHROPIC_API_KEY` | Paid per token ([below](#anthropic)) | Not yet evaluated: no numbers until the eval runs. |
| **Vertex AI** (Gemini on Google Cloud) | the same `VERTEX_API_KEY` and project ([Vertex AI](#vertex-ai)) | Paid per token on the Google Cloud project | The same Gemini models as above; the Gemini numbers were measured on Vertex. |
| **Any OpenAI-compatible endpoint** | its own key (none for a local server) | Whatever the provider charges | Not evaluated. The model **must support tool calling**. |

### Presets on the setup page

Step 1 of `sheepdog setup` shows the orchestrator as a grid of presets. Pick
one and the page asks only for what it needs; you never type a base URL for a
known provider. The base URL stays visible, read-only, under **Details**.

| Preset | Base URL (filled in) | Key | Model | Note on the page |
|---|---|---|---|---|
| **Anthropic** | built in (Messages API) | `ANTHROPIC_API_KEY` | `claude-opus-5-5`, or any model from the key's list | ★ most recommended |
| **Gemini** | built in (AI Studio) | the Gemini key | `gemini-3.5-flash`, or any model from the key's list | free, can be slow or busy |
| **DeepSeek** | built in | `DEEPSEEK_API_KEY` | `deepseek-flash` | cost-effective · did well in our tests |
| **Vertex AI** | built in | `VERTEX_API_KEY` and a project | the models we checked on Vertex | Gemini on Google Cloud |
| **OpenRouter** | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | you pick from the list, tool-capable first | one key, many models |
| **OpenAI** | `https://api.openai.com/v1` | `OPENAI_ORCH_API_KEY`, or the voice's key if you tick the box | the newest `gpt-…-mini` in your key's list, else you pick | paid per token |
| **Ollama** | `http://localhost:11434/v1` | none | the first model you pulled | on this computer, no key |
| **LM Studio** | `http://localhost:1234/v1` | none | the first model you loaded | on this computer, no key |
| **Other URL** | you type it | optional | found or typed | any OpenAI-compatible endpoint |

- Once the key checks out (or a key is already saved), the page lists the
  models by itself and fills one in. You can always type another.
- For Ollama and LM Studio the page checks that the server is running and
  says "Ollama is running, 3 models" or "Not running: start Ollama, then press
  Check again". Small local models may not handle tool calling well: press
  **Test tool calling** before you save.
- **OpenAI and the voice key.** `OPENAI_API_KEY` is the GPT-Live voice key.
  The OpenAI orchestrator gets its own key (`OPENAI_ORCH_API_KEY`) unless you
  tick **Use the same OpenAI key (also used for the voice)**, which is off by
  default and only shown when a voice key is saved.
- Saving writes a `[[brain.openai_compat]]` entry named after the preset
  (`openrouter`, `openai`, `ollama`, `lmstudio`) to `config.toml`, or updates
  it, and starts the orchestrator on it. Details:
  [CONFIG.md](../CONFIG.md#brainopenai_compat).

The two Understood numbers come from different runs on the same tier-1
simulated-workday suite; DeepSeek minus Gemini is +10 points (CI +5 to +15).
The Gemini runs went through Vertex AI. The report is
[dev/eval/vertex-2026-10-11.md](dev/eval/vertex-2026-10-11.md); how every
number is produced is in [dev/eval/README.md](dev/eval/README.md).

OpenAI-compatible examples (each is one `[[brain.openai_compat]]` table in
`~/.config/sheepdog/config.toml`; the setup page's presets and its Other URL
choice write it for you):

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

## Anthropic

The Anthropic preset talks to Claude through Anthropic's own Messages API, not
an OpenAI-compatible shim. The default model is Claude Opus 5.5
(`claude-opus-5-5`). **Find models** lists what your key can use (`GET
/v1/models`, free), and **Test tool calling** sends one small billed request.
`sheepdog doctor` checks the key with the same free model list; Anthropic does
not report a balance.

What it costs (Anthropic's first-party prices per million tokens, October
2026):

| Model | Input | Output | Cache reads |
|---|---|---|---|
| Claude Opus 5.5 (`claude-opus-5-5`, default) | $4 | $20 | $0.20 |
| Claude Sonnet 5.5 (`claude-sonnet-5-5`) | $2 | $10 | $0.20 |
| Claude Haiku 5.5 (`claude-haiku-5-5`) | $0.10 | $0.50 | |

The system prompt and tools are cached between calls, so most of each
request's prompt is billed at the cache-read price. Claude always thinks, and
its thinking is billed as output. Your questions run at effort `medium` and
agent events at `low` (`anthropic.effort`, `anthropic.event_effort`).

**Refusals and fallbacks.** Anthropic's safety classifiers can decline a
request. sheepdog turns on Anthropic's server-side fallbacks by default: a
declined request is answered, in the same call, by the model Anthropic
recommends for that case, which may be an older Claude model. A request that
is still declined fails that turn, and sheepdog answers in its own words.
Turn the fallbacks off with `anthropic.fallbacks = false`
([CONFIG.md](../CONFIG.md#anthropic)).

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
3. In `sheepdog setup`, step 1: choose **Vertex AI** as the orchestrator,
   **Gemini Live on Vertex AI** as the voice, or both. One "Google
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
| Orchestrator (Google, DeepSeek, Anthropic or your endpoint) | What you said as text, the agents' screens, session records and command output the orchestrator reads | On each of your questions and each agent event |
| sheepdog's push relay, then Apple | Phone notifications, end-to-end encrypted: the relay sees the phone's push token, the timing and the kind, never the content | When an agent needs you and a phone is registered (`push.mode`, [iphone-app.md](iphone-app.md)) |
| Nobody else | | No telemetry |

A local Ollama orchestrator keeps agent screens on your machine; the voice
still goes to Google or OpenAI. Details and deletion: [privacy.md](privacy.md).
Check each provider's own terms, especially for free-tier keys.

## Switch models

- **Setup page:** `sheepdog setup`, step 1: the voice card, its model and
  its voice, then the orchestrator. Saving reloads the daemon in place.
- **While it runs:** the `/debug` page header (`sheepdog open debug`) and the
  iPhone app's Settings, Models switch voice and orchestrator at the next
  turn. The choice survives a restart.
- **Config:** `voice.provider`, `gemini.model`, `gemini.voice`,
  `voice.openai.model`, `voice.openai.voice`, `brain.provider` and `brain.model` in
  [CONFIG.md](../CONFIG.md), keys in `~/.config/sheepdog/.env`.
  `sheepdog config show` prints the effective config with keys redacted.
