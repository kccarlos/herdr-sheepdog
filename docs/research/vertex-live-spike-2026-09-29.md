# Vertex AI Live spike — 2026-09-29

Question: can sheepdog's Gemini Live voice path (`internal/voice/gemini`,
today Google AI Studio only) run on Vertex AI with the Vertex API key in
`.env` (`VERTEX_API_KEY`, restricted to `aiplatform.googleapis.com`, bound
to a service account; `VERTEX_PROJECT`), and what differs from AI Studio
that our code cares about?

Method: a throwaway raw-WebSocket client (gorilla/websocket, not
committed) sending hand-written `setup` / `clientContent` JSON, about
thirty short handshakes and six one-line text turns in all; the model
listing over REST with a `gcloud auth print-access-token` bearer; the
`google.golang.org/genai` v1.71.0 source (`live.go`, `client.go`,
`transformer.go`); then the implementation itself, checked live with
`sheepdog voice-test` and a scratch daemon (below).

## Answer

**Yes, the API key works for Live.** No OAuth / ADC is needed.

| | AI Studio | Vertex AI (what works) |
|---|---|---|
| Host | `generativelanguage.googleapis.com` | `{location}-aiplatform.googleapis.com` (e.g. `us-central1-…`), or `aiplatform.us.rep.googleapis.com` for the `us` multi-region |
| Path | `/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent` | `/ws/google.cloud.aiplatform.{v1,v1beta1}.LlmBidiService/BidiGenerateContent` (both versions work; the SDK uses `v1beta1`) |
| Auth | `x-goog-api-key` header | `x-goog-api-key` header **or** `?key=` query; a bearer token also works |
| `setup.model` | `models/gemini-3.8-live` | `projects/{project}/locations/{location}/publishers/google/models/gemini-3.8-live` (the short `publishers/google/models/…` form is refused: close 1007 "Invalid resource field value in the request") |
| Model id | `gemini-3.8-live` | **the same `gemini-3.8-live`** |

### Where the model is served

| Location | `gemini-3.8-live` | `gemini-live-2.5-flash-native-audio` |
|---|---|---|
| `global` (`aiplatform.googleapis.com`) | not found (close 1008) | not found |
| `us-central1` | **works** | works |
| `us` (multi-region, `aiplatform.us.rep.googleapis.com`) | works | not found |
| `europe-west4` | not found | — |

"Not found" is a close 1008 (policy violation) right after the setup:
"Publisher model `projects/…/locations/global/publishers/google/models/gemini-3.8-live` was not found…"
(the close reason is truncated at 123 bytes). The REST
`ListPublisherModels` listing (needs OAuth: the API key gets 401
"API keys are not supported by this API") does not list
`gemini-3.8-live` at all, only `gemini-live-2.5-flash-native-audio` among
the Live models, so the listing is not a reliable guide; try the socket.
Also tried and not found at `global` and `us-central1`:
`gemini-3.8-live-preview`, `gemini-live-3.8`, `gemini-3.8-flash-live`,
`gemini-3.8-flash-live-preview`.

**Default chosen: location `us-central1`, model unchanged
(`gemini-3.8-live`).** The `us` multi-region also works (Google documents
a price premium for multi-region endpoints).

### The Go SDK (`google.golang.org/genai` v1.71.0) has two gaps on Vertex with an API key

1. With `APIKey` set and no explicit base URL it picks the global host
   `https://aiplatform.googleapis.com/`, which does not serve the Live
   model (above). Fix: set `HTTPOptions.BaseURL` to the regional host.
2. `Live.Connect` on Vertex only ever sends `Authorization: Bearer` from
   `Credentials`; it never puts the API key on the socket (it does for AI
   Studio). With `Credentials` nil the dial goes out unauthenticated
   (close 1008 "Request is missing required authentication credential").
   Fix: `HTTPOptions.Headers["x-goog-api-key"]`, which `Connect` merges
   into the handshake headers.

With `Project` and `Location` set the SDK builds the full
`projects/…/publishers/google/models/…` name itself, and with an API key
it skips Application Default Credentials. Both fixes live in
`internal/voice/gemini/conn.go` (`sdkDialer.clientConfig`); nothing
above the dialer changed.

## Schema differences that matter to our code

All checked live with the same setup our `liveConfig` sends (audio out,
Puck, system instruction, tools, input transcription with
`customVocabulary`, output transcription, sliding-window compression
16 000/4 000, session resumption, start-of-speech sensitivity LOW):

- **Setup** is accepted unchanged. `setupComplete` carries a
  `sessionId` on Vertex; we ignore it.
- **Session resumption**: `sessionResumptionUpdate {newHandle,
  resumable: true}` arrives after each turn; the handle is a UUID. A new
  connection presenting it resumed the conversation (asked to remember
  "pineapple", a fresh socket with the handle answered "pineapple"). The
  daemon's `--drop-after` check resumed with `context_lost: false`.
- **Context window compression, VAD sensitivities, transcription
  (including `customVocabulary`)**: accepted; output transcription
  arrives as `serverContent.outputTranscription` exactly as on AI Studio.
- **Tool calls**: `toolCall.functionCalls[{name, args, id}]` with ids
  like `call_3094`; `turnComplete` follows immediately. Through the real
  session the call was issued, the function response was sent and
  accepted (no error, the session carried on), but the model did not
  voice the echo stub's result ("Checking." only). The identical
  `voice-test` run on AI Studio behaved the same ("On it." only), so this
  is model / echo-stub behaviour, not a Vertex difference.
- **Usage**: `usageMetadata` arrives per turn (`voice.usage` logged).
- **Errors / close codes**: a bad or missing key is not an HTTP
  handshake failure but a close **1008** right after the setup ("API
  keys are not supported by this API…" for a wrong key, "Request is
  missing required authentication credential" for none). An unknown
  model is also 1008, a malformed model name 1007. None of these match
  `isRotatable`, so they take the ordinary doubling backoff.
- **Not observed** in this spike (short sessions, one key): `goAway`,
  a 1011, and a 429 / `RESOURCE_EXHAUSTED`. The code paths are
  backend-agnostic (the reader handles `goAway` the same; 1011 drops the
  handle the same). A Vertex quota error is expected to carry
  `RESOURCE_EXHAUSTED` / 429 (Google's error model; not seen here),
  which `isRateLimited` already matches. With a
  single key a 429 at connect is `ErrAllKeysExhausted` after one try and
  a 429 mid-session waits the retry delay (else 30 s), logs
  `voice.rate_limited` and retries the same key with its handle, no
  rotation (`TestVertexRateLimitBacksOffOnOneKey`).

## Live verification of the implementation

`SHEEPDOG_GEMINI_BACKEND=vertex sheepdog voice-test --text "What is the
status of auth?" --push "docs finished its task" --drop-after 12s`:

```
voice.connect   {"backend":"vertex","dur_ms":583,"key_index":0,"resumed":false}
voice.tool_call {"args":{"text":"What is the status of auth?"},"id":"call_6216","name":"to_supervisor"}
voice.transcript.out "Checking."
voice.push      {"text":"docs finished its task","urgency":"idle"}
voice.transcript.out "Docs finished " / "its task."
voice.drop
voice.connect   {"backend":"vertex","dur_ms":576,"key_index":0,"resumed":true}
voice.resume    {"replayed_chunks":28,"resumed":true}   (context_lost: false)
```

Scratch daemon (`serve --port 7791 --herdr mock --brain off --cli off
--voice gemini`, own data dir): `GET /api/voice/providers` lists
`"label": "Gemini Live (Vertex)"`; `POST /api/inject/utterance` "Hello,
can you hear me?" → `voice.transcript.out` "Yes, I can hear you
perfectly.", `voice.turn {"end":"complete","responded":true}`. The key
never appeared in stdout, stderr or the event log.
