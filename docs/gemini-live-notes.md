# Gemini Live API notes for `internal/voice/gemini`

Recorded 2026-09-17. Sources, in order of authority:

1. Go SDK `google.golang.org/genai` **v1.71.0** (module cache
   `~/go/pkg/mod/google.golang.org/genai@v1.71.0/`, files `live.go`,
   `live_converters.go`, `types.go`). `go.mod` says **`go 1.24`**; our module is
   `go 1.25` on Go 1.27.1, so that is satisfied. Every Live symbol is marked
   `// Preview.` in its doc comment.
2. Live smoke tests run **against `gemini-3.8-live` with a real key** (throwaway
   programs in the session scratchpad, not committed): one through the SDK and
   one raw `gorilla/websocket` client to see exact wire JSON. Trimmed output is
   in §9.
3. Reference implementations (Python SDK / raw JS) under
   `/Users/kc/Documents/reference_repo/gemini-live-api-examples` and
   `/Users/kc/Documents/reference_repo/live-api-web-console` for cross-checks.

---

## 1. Decision: use the Go SDK (`client.Live`), not a raw WebSocket

**Decision: SDK.** Rationale:

- The SDK exposes every field the plan needs (verified in `types.go` and by the
  smoke test): response modalities, system instruction, speech config, in/out
  transcription, tools with `Behavior: NON_BLOCKING`, `FunctionResponse.Scheduling`,
  `ContextWindowCompression{TriggerTokens, SlidingWindow{TargetTokens}}`,
  `SessionResumption{Handle, Transparent}`, and it decodes `interrupted`,
  `turnComplete`, `generationComplete`, `inputTranscription`,
  `outputTranscription`, `toolCall`, `toolCallCancellation`, `goAway`,
  `sessionResumptionUpdate`, `usageMetadata`.
- The Gemini API converter (`liveConnectConfigToMldev`) passes all of those
  through unchanged (only `ExplicitVADSignal` is rejected for the Gemini API
  backend), so there is no hidden field loss.
- It already uses `github.com/gorilla/websocket` and puts the API key in the
  `x-goog-api-key` header (not the URL). Endpoint it dials:
  `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent`.
- Cost of the SDK: a large dependency (`types.go` alone is 8.7k lines) and the
  quirks in §2. All are easy to wrap. If a future field is missing, the raw
  message shapes in §8 are enough to drop to `gorilla/websocket` directly; the
  wire protocol is plain JSON text frames.

Preview status: the SDK marks the whole Live surface `Preview`; the API version
used is `v1beta` (SDK default for `BackendGeminiAPI`). `LiveConnectConfig.GenerationConfig`
was removed in an earlier major version (fields are flattened onto
`LiveConnectConfig`). Expect churn; pin the SDK version in `go.mod`.

## 2. SDK quirks that must be handled

1. **`Session.Receive()` returns the `setupComplete` message first.** `Connect`
   reads it and stores it in `bufferedMessage`; the first `Receive()` call
   hands it back. Treat a message with only `SetupComplete` set as a no-op.
2. **Empty keep-alive frames.** The server sends `{}` and
   `{"serverContent":{}}` frames constantly while generating (one before almost
   every audio chunk). They decode to a `LiveServerMessage` with all fields nil
   (or `ServerContent` non-nil but empty). Ignore them; do not treat a nil
   `ServerContent.ModelTurn` as an error.
3. **Not goroutine-safe.** `Send*` all call `conn.WriteMessage`; `Receive` calls
   `conn.ReadMessage`. gorilla allows exactly one concurrent writer and one
   concurrent reader. Use one reader goroutine and a mutex around every
   `SendRealtimeInput`/`SendClientContent`/`SendToolResponse`. (A second
   concurrent `Receive` in the smoke test produced
   `websocket: RSV2 set, continuation after FIN`.)
4. **Errors arrive as `Receive` errors.** Server error frames (`{"error":…}`)
   and close frames come back as `error`, e.g.
   `websocket: close 1007 (invalid payload data): The requested combination of response modalities (TEXT) is not supported by the model.`
   A `goAway` is a normal message, not an error.
5. **`gemini-3.8-live` refuses `ResponseModalities: TEXT`** (close 1007 above).
   Only `AUDIO` works. For cheap/automated tests use `AUDIO` and read
   `OutputTranscription.Text` (this is what the smoke test does); the audio
   bytes can be discarded.
6. `Connect` returns an error if `HTTPOptions.APIVersion` is empty — leave the
   client defaults alone (`genai.NewClient(ctx, nil)` sets `v1beta` and reads
   `GEMINI_API_KEY`), or set `APIVersion` explicitly if you pass `ClientConfig`.
7. `SetupComplete.SessionID` was `""` in every run; do not rely on it.
8. Enum types are Go string types: `genai.BehaviorNonBlocking` == `"NON_BLOCKING"`,
   `genai.FunctionResponseSchedulingInterrupt` == `"INTERRUPT"`, etc.

## 3. Connecting — `LiveConnectConfig` (exact Go field names)

```go
import "google.golang.org/genai"

client, err := genai.NewClient(ctx, nil) // GEMINI_API_KEY from env, BackendGeminiAPI, APIVersion v1beta

cfg := &genai.LiveConnectConfig{
    ResponseModalities: []genai.Modality{genai.ModalityAudio}, // TEXT is rejected by gemini-3.8-live
    SpeechConfig: &genai.SpeechConfig{
        VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: "Puck"}},
        // LanguageCode: "en-US", // optional
    },
    SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: systemPrompt}}},
    Tools:             sheepdogTools(), // §5
    InputAudioTranscription:  &genai.AudioTranscriptionConfig{CustomVocabulary: aliases}, // §7
    OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
    ContextWindowCompression: &genai.ContextWindowCompressionConfig{
        TriggerTokens: genai.Ptr(int64(16000)),
        SlidingWindow: &genai.SlidingWindow{TargetTokens: genai.Ptr(int64(4000))},
    },
    SessionResumption: &genai.SessionResumptionConfig{Handle: previousHandle}, // "" = new session
    // RealtimeInputConfig: &genai.RealtimeInputConfig{            // VAD tuning, optional (plan: API default)
    //     AutomaticActivityDetection: &genai.AutomaticActivityDetection{
    //         StartOfSpeechSensitivity: genai.StartSensitivityHigh,
    //         EndOfSpeechSensitivity:   genai.EndSensitivityLow,
    //         PrefixPaddingMs: genai.Ptr(int32(20)), SilenceDurationMs: genai.Ptr(int32(500)),
    //     },
    //     ActivityHandling: genai.ActivityHandlingStartOfActivityInterrupts,
    //     TurnCoverage:     genai.TurnCoverageTurnIncludesOnlyActivity,
    // },
    // Proactivity: &genai.ProactivityConfig{ProactiveAudio: genai.Ptr(true)}, // lets the model stay silent; consider for v1.1
    // ThinkingConfig, Temperature, MaxOutputTokens, EnableAffectiveDialog also available.
}
session, err := client.Live.Connect(ctx, "gemini-3.8-live", cfg)
```

`TriggerTokens`/`TargetTokens` are `*int64` serialised as JSON **strings**
(`"16000"`), which is what the wire expects. The plan's numbers (16 000 / 4 000)
were accepted by the server. `SlidingWindow.TargetTokens` defaults to
`trigger/2` when unset.

What the SDK actually sends (captured from the raw probe, identical shape):

```json
{"setup":{
  "model":"models/gemini-3.8-live",
  "generationConfig":{"responseModalities":["AUDIO"],
                      "speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Puck"}}}},
  "systemInstruction":{"parts":[{"text":"…"}]},
  "tools":[{"functionDeclarations":[{"name":"briefing","description":"…","behavior":"NON_BLOCKING"}]}],
  "sessionResumption":{},
  "contextWindowCompression":{"triggerTokens":"16000","slidingWindow":{"targetTokens":"4000"}},
  "inputAudioTranscription":{},
  "outputAudioTranscription":{}}}
```

Server replies `{"setupComplete":{}}` (≈150–450 ms after dial), then
immediately `{"sessionResumptionUpdate":{"newHandle":"<uuid>","resumable":true}}`.

## 4. Sending

### Audio (browser mic → model)

```go
err := session.SendRealtimeInput(genai.LiveRealtimeInput{
    Audio: &genai.Blob{MIMEType: "audio/pcm;rate=16000", Data: pcm16leMonoChunk},
})
// wire: {"realtimeInput":{"audio":{"mimeType":"audio/pcm;rate=16000","data":"<base64>"}}}
```

Input requirements: raw little-endian 16-bit PCM, mono, 16 kHz native (other
rates are accepted if declared in the MIME type). Send small chunks
(the reference clients use 20–100 ms). Only one of `Audio`/`Media`/`Video`/`Text`/
`ActivityStart`/`ActivityEnd`/`AudioStreamEnd` per call. When the mic is muted
send nothing (plan §6); optionally `SendRealtimeInput{AudioStreamEnd: true}`
when a stream ends so the server flushes VAD.

### Text as if spoken (debug page "simulate utterance")

```go
err := session.SendRealtimeInput(genai.LiveRealtimeInput{Text: "what's going on"})
// wire: {"realtimeInput":{"text":"what's going on"}}
```

Observed: behaves like a user utterance; the model answered in ~700 ms.

### Push — make the model speak unprompted (`push(text, urgency)`)

```go
// urgency == interrupt | idle : speak now
err := session.SendClientContent(genai.LiveClientContentInput{
    Turns: []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text:
        "[system event, not spoken by the user] Agent auth is blocked asking whether to delete the old migration. Tell the user in one sentence."}}}},
    // TurnComplete defaults to true
})
// wire: {"clientContent":{"turns":[{"role":"user","parts":[{"text":"…"}]}],"turnComplete":true}}

// urgency == silent : context only, no generation
err = session.SendClientContent(genai.LiveClientContentInput{
    Turns:        []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: "[system context] docs is now idle."}}}},
    TurnComplete: genai.Ptr(false),
})
```

Observed behaviour:

- `TurnComplete=true` → the model generated speech within ~0.6–0.9 s and read
  the event aloud ("The auth agent has successfully completed its task.").
- `TurnComplete=false` → **no server message at all**; the content was added
  to context and the next question ("Is docs still blocked?") was answered
  from it ("No, the docs agent is no longer blocked and is now idle."). This is
  the `silent` urgency and the "re-push briefing after compression" mechanism.
- Sending a `clientContent` while the model is speaking **interrupts** it:
  the server emitted `{"serverContent":{"interrupted":true}}` then
  `{"serverContent":{"turnComplete":true}}` (no `generationComplete`), then
  started the new turn. Use this for `interrupt` urgency; for `idle` urgency
  the daemon must wait for `turnComplete` itself (the push queue in §3.6 of
  the plan) — the API has no "queue after current turn" for client content.
- Trade-offs: `clientContent` turns are ordered relative to each other but
  the SDK warns that interleaving them with realtime audio "is not
  recommended". In practice it worked fine here. `Role` should be `"user"`;
  the model treats bracketed system-event text sensibly with a short system
  prompt. Every push re-bills the whole context (see `usageMetadata`), which
  is why compression matters.

### Tool responses

See §5.

## 5. Tools: `NON_BLOCKING` declarations and scheduled responses

```go
func sheepdogTools() []*genai.Tool {
    obj := func(props map[string]*genai.Schema, req []string) *genai.Schema {
        return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: req}
    }
    return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
        {Name: "briefing", Behavior: genai.BehaviorNonBlocking,
            Description: "Current status of all agents in one spoken sentence. Call when the user asks what's happening, how things are going, or for a status.",
            Parameters:  obj(map[string]*genai.Schema{}, nil)},
        {Name: "ask", Behavior: genai.BehaviorNonBlocking,
            Description: "Ask a question that needs looking at an agent's screen, diff, logs, or the repo. Use for 'why', 'what did it change', 'is it stuck'. Tell the user you're checking, then relay the answer when it arrives.",
            Parameters: obj(map[string]*genai.Schema{
                "question": {Type: genai.TypeString},
                "agent":    {Type: genai.TypeString, Description: "alias if the user named one"},
            }, []string{"question"})},
        {Name: "propose", Behavior: genai.BehaviorNonBlocking,
            Description: "Send an instruction or an answer to an agent. Use when the user tells an agent to do, stop, or answer something. The system will echo what it will send; the user can say stop to cancel.",
            Parameters: obj(map[string]*genai.Schema{
                "agent":       {Type: genai.TypeString},
                "instruction": {Type: genai.TypeString, Description: "what the user said, in their words"},
            }, []string{"instruction"})},
        {Name: "confirm", Behavior: genai.BehaviorNonBlocking,
            Description: "User says yes / go ahead / do it to the most recent question from an agent.",
            Parameters: obj(map[string]*genai.Schema{
                "answer": {Type: genai.TypeString, Description: "the user's answer if it isn't a plain yes, e.g. 'no', 'option 2', 'keep the old one'"},
            }, nil)},
        {Name: "cancel", Behavior: genai.BehaviorNonBlocking,
            Description: "User says stop, cancel, wait, or no. Cancels the in-flight action or the pending question.",
            Parameters:  obj(map[string]*genai.Schema{}, nil)},
        {Name: "note", Behavior: genai.BehaviorNonBlocking,
            Description: "Remember something the user decided or wants kept in mind, e.g. 'don't touch the migrations today'.",
            Parameters: obj(map[string]*genai.Schema{"text": {Type: genai.TypeString}}, []string{"text"})},
    }}}
}
```

`FunctionDeclaration.Behavior` (`genai.Behavior`: `UNSPECIFIED | BLOCKING |
NON_BLOCKING`) is documented as "currently only supported by the
BidiGenerateContent method" — i.e. exactly the Live API. A no-parameter tool
may omit `Parameters` entirely (the raw probe did); an empty object schema also
works.

Receiving and answering a call:

```go
case msg.ToolCall != nil:
    for _, fc := range msg.ToolCall.FunctionCalls {        // fc.ID, fc.Name, fc.Args map[string]any
        go func(fc *genai.FunctionCall) {
            spoken, sched := supervisor.Handle(fc.Name, fc.Args) // sched chosen by the Supervisor
            sendMu.Lock(); defer sendMu.Unlock()
            _ = session.SendToolResponse(genai.LiveToolResponseInput{
                FunctionResponses: []*genai.FunctionResponse{{
                    ID:         fc.ID,
                    Name:       fc.Name,
                    Response:   map[string]any{"spoken": spoken},
                    Scheduling: sched, // genai.FunctionResponseSchedulingInterrupt | WhenIdle | Silent
                    // WillContinue: genai.Ptr(true) turns the call into a generator (more responses later)
                }},
            })
        }(fc)
    }
case msg.ToolCallCancellation != nil:
    // msg.ToolCallCancellation.IDs — user barged in; drop in-flight work for these ids
```

Wire shape the SDK produces (top-level `scheduling`, matching the proto):

```json
{"toolResponse":{"functionResponses":[{"id":"call_281653","name":"briefing","response":{"spoken":"Two agents. Auth is running tests, docs is blocked asking whether to delete the old migration."},"scheduling":"INTERRUPT"}]}}
```

The official docs/JS example instead put `"scheduling"` *inside* the
`response` object. **Both placements were tested and both work** (the model
read the `spoken` text verbatim either way). Use the SDK's typed field.

`FunctionResponseScheduling` values: `SCHEDULING_UNSPECIFIED`, `SILENT` (add
to context, do not generate), `WHEN_IDLE` (default — speak after the current
turn), `INTERRUPT` (speak now, cutting the model off). Only meaningful for
`NON_BLOCKING` declarations; ignored otherwise.

Observed call sequence for a NON_BLOCKING tool (important for the receive
loop): the model emits `toolCall` and **immediately ends its own turn**
(`generationComplete` + `turnComplete` with ~5 response tokens, no audio). The
function response then starts a *new* model generation ~0.6–0.75 s later. The
model did not say "let me check" before calling `briefing`; whether it speaks
first for `ask` depends on the prompt ("Tell the user you're checking" is in
the description). The Supervisor must therefore not assume a turn boundary
means "the model has finished speaking the result".

## 6. Receiving — `LiveServerMessage` field map

```go
for {
    msg, err := session.Receive()
    if err != nil { /* reconnect with handle, §7 */ }
    switch {
    case msg.SetupComplete != nil:            // first message after Connect (buffered), ignore
    case msg.GoAway != nil:                   // msg.GoAway.TimeLeft time.Duration → start reconnect
    case msg.SessionResumptionUpdate != nil:  // .NewHandle, .Resumable, .LastConsumedClientMessageIndex → persist handle
    case msg.ToolCall != nil:                 // .FunctionCalls []*FunctionCall
    case msg.ToolCallCancellation != nil:     // .IDs []string
    case msg.ServerContent != nil:
        sc := msg.ServerContent
        if sc.Interrupted { flushOutputQueue(); tellBrowserFlush() }
        if sc.ModelTurn != nil {
            for _, p := range sc.ModelTurn.Parts {
                if p.InlineData != nil { enqueueAudio(p.InlineData.Data) } // MIMEType "audio/pcm;rate=24000"
                if p.Text != "" { /* only in TEXT modality */ }
            }
        }
        if sc.InputTranscription != nil  { log("voice.transcript.in",  sc.InputTranscription.Text,  sc.InputTranscription.Finished) }
        if sc.OutputTranscription != nil { log("voice.transcript.out", sc.OutputTranscription.Text) }
        if sc.GenerationComplete { /* model finished producing; audio may still be playing */ }
        if sc.TurnComplete       { /* safe point for the next queued push; sc.TurnCompleteReason, sc.InteractionStatus */ }
        // also: sc.InterimInputTranscription, sc.WaitingForInput, sc.GroundingMetadata
    }
    if msg.UsageMetadata != nil { /* .PromptTokenCount .ResponseTokenCount .TotalTokenCount .ThoughtsTokenCount */ }
}
```

Observed message sequence for one spoken answer (`Say hello in three words.`),
timings relative to the send:

```
+ 12ms   sessionResumptionUpdate{newHandle, resumable:true}     (only after the first client message / at connect)
+452ms   {}                                                      keep-alive
+452ms   {"serverContent":{}}                                    keep-alive
+725ms   {}
+726ms   serverContent.modelTurn.parts[0].inlineData(audio/pcm;rate=24000) + outputTranscription{"Hello, team "}
+740ms … more inlineData chunks (1.9–35 KB each, ≈40–700 ms of audio), every other frame a {} keep-alive,
         outputTranscription arrives in phrase-sized pieces attached to some audio frames
+1250ms  serverContent.generationComplete
+2624ms  serverContent.turnComplete + usageMetadata{promptTokenCount:583, responseTokenCount:49, thoughtsTokenCount:49, totalTokenCount:632,
                                                    promptTokensDetails:[{TEXT:344},{AUDIO:205}], responseTokensDetails:[{AUDIO:49}]}
```

`turnComplete` lags `generationComplete` by the estimated playback time of the
audio (1.4–4.3 s in the runs), exactly as the field doc says ("model assumes
realtime playback"). First audio byte latency for text-in was 0.7–1.0 s; one
turn in the SDK run took 7 s (server-side variance, not reproducible).

Barge-in sequence observed (client content sent while the model was counting):

```
+989ms   inlineData + outputTranscription{"One, two, "}
         → client sends new clientContent
+1068ms  serverContent.interrupted
+1107ms  serverContent.turnComplete + usageMetadata            (no generationComplete)
+575ms   (new turn) inlineData + outputTranscription{"Okay."} … generationComplete … turnComplete
```

`Transcription` fields: `Text`, `Finished`, `LanguageCode`, `SpeakerLabel`,
`Words`. Output transcription arrived as incremental fragments without
`finished:true` in these runs; concatenate per turn.

## 7. Session resumption, GoAway, compression, keyword biasing

### Resumption (verified)

- Enable with `SessionResumption: &genai.SessionResumptionConfig{}` (empty) on
  the first connect. The server then sends `sessionResumptionUpdate` messages;
  keep the **latest `NewHandle` where `Resumable == true`**. Handles are UUIDs
  (`"8149f0e4-9364-4fd5-89ca-255fb6b3c9cc"`). One arrived right after setup
  and none during the following turns in the SDK run, so persist whatever you
  have; the docs say updates come periodically and after turns.
- Reconnect: `client.Live.Connect(ctx, model, cfgWith(SessionResumption: &genai.SessionResumptionConfig{Handle: h}))`.
  You must resend the full config (tools, system instruction, compression).
  Verified: after `Close()` and reconnecting with the handle (590 ms), the
  question "what did the briefing say about docs?" was answered correctly from
  the previous connection's context, and a fresh `sessionResumptionUpdate`
  arrived immediately.
- `Transparent: true` additionally gives `LastConsumedClientMessageIndex` so a
  client can replay buffered audio frames sent after the last handle; not
  needed for v1 (a few hundred ms of audio lost on reconnect is acceptable).
- What is lost: anything the model was generating at the moment of the drop
  (`resumable:false` updates mark such moments), and any in-flight tool calls
  — re-issue nothing; the Supervisor should re-push the briefing as `silent`
  after reconnect (plan §6).

### GoAway (not observed — the smoke test did not run for the ~10 min limit)

Type: `LiveServerGoAway{TimeLeft time.Duration}`; wire `{"goAway":{"timeLeft":"10s"}}`
(protobuf Duration string; the SDK's `UnmarshalJSON` converts it). Flow:
on `GoAway`, mark the session draining, finish the current turn if possible,
open the new connection with the saved handle, swap the sender, close the old
one. The connection limit is enforced per connection; the resumable *session*
outlives it. `Receive()` returning a close error without a preceding `GoAway`
is the "unexpected drop" case — reconnect the same way.

### Context window compression (accepted, effect not observable in a short run)

`{"contextWindowCompression":{"triggerTokens":"16000","slidingWindow":{"targetTokens":"4000"}}}`
was accepted in setup. There is **no server message announcing that a
compression happened**; the only visible effect is `usageMetadata.promptTokenCount`
dropping between turns. The plan's "after each compression, push the briefing
silently" therefore needs a client-side heuristic: watch `promptTokenCount`
fall by more than ~30 % between consecutive turns, or simply re-push the
briefing silently on every N-th turn / every few minutes.

### Keyword biasing / custom vocabulary — **yes, partially**

`genai.AudioTranscriptionConfig` has `CustomVocabulary []string` ("biases the
ASR model to improve recognition of these specific terms"; `AdaptationPhrases`
is the deprecated predecessor) plus `LanguageCodes`, `Diarization`,
`WordTimestamp`, `Mode` (`VERBATIM|SMART`). The smoke test connected with
`InputAudioTranscription: &genai.AudioTranscriptionConfig{CustomVocabulary: []string{"herdr","sheepdog","codex"}}`
and the server accepted the setup. Caveat: this biases the **transcription**
stream (`inputTranscription`), which is what `resolve_instruction` reads; it
does not necessarily change what the native-audio model itself hears. So do
both: put aliases in `CustomVocabulary` **and** in the system prompt (plan
§3.7 fallback). Effectiveness on real speech was not measured (no mic in the
smoke test) — verify in WP2's `voice-test`.

## 8. Audio formats

- **Output:** `inlineData.mimeType = "audio/pcm;rate=24000"`, 16-bit LE PCM,
  mono (chunks of 1.9–35 KB observed, i.e. 40–730 ms). Confirms plan §6.
- **Input:** `"audio/pcm;rate=16000"`, 16-bit LE PCM mono via `SendRealtimeInput{Audio}`.
- Text input: `SendRealtimeInput{Text}` (counts as user activity) or
  `SendClientContent` (ordered turn).

## 9. Smoke test — observed sequence (trimmed)

SDK program, `gemini-3.8-live`, `AUDIO` modality, six NON_BLOCKING tools,
compression 16k/4k, resumption on, in/out transcription on (audio-only keep-alive
frames filtered out):

```
backend=BackendGeminiAPI apiVersion=v1beta
connected in 440ms sessionId=""
[hello +    0ms] setupComplete
[hello +    0ms] sessionResumptionUpdate{resumable=true handle=91d82179-a54… lastIdx=0}
[hello +  789ms] modelTurn.inlineData(audio/pcm;rate=24000,2B) outputTranscription="Hello, I am ready "
[hello + 1131ms] modelTurn.inlineData(audio/pcm;rate=24000,9600B) outputTranscription="to supervise your coding"
[hello + 1774ms] modelTurn.inlineData(audio/pcm;rate=24000,24960B) outputTranscription=" agents."
[hello + 1881ms] generationComplete
[hello + 4014ms] turnComplete usageMetadata{prompt=838 resp=86 total=924}
[ask-status +  755ms] toolCall{id=call_263855 name=briefing args={}}
[ask-status-drain +    0ms] generationComplete
[ask-status-drain +    0ms] turnComplete usageMetadata{prompt=958 resp=5 total=963}
   → SendToolResponse{id, name, response:{spoken:…}, scheduling:INTERRUPT} after 1.5 s
[tool-resp +  747ms] modelTurn.inlineData(...) outputTranscription="Two agents. Auth "
[tool-resp + 1030ms] … "is running" … " tests, docs" … " is blocked asking" … " whether to delete the old" … " migration."
[tool-resp + 2676ms] generationComplete
[tool-resp + 6627ms] turnComplete usageMetadata{prompt=996 resp=152 total=1148}
   → SendClientContent{"[system event …] Agent auth just finished its task. Tell the user in one sentence."}
[push +  933ms] outputTranscription="The auth agent " … "has successfully completed" … " its task."
[push + 1563ms] generationComplete
[push + 4125ms] turnComplete usageMetadata{prompt=1211 resp=89 total=1300}
   → SendClientContent{"[system context] docs is now idle.", TurnComplete:false}   (no server response)
   → SendClientContent{"Is docs still blocked? Answer in one sentence."}
[after-silent + 7142ms] outputTranscription="No, the docs agent " … "is no longer" … " blocked and is now idle."
[after-silent +10794ms] turnComplete usageMetadata{prompt=1342 resp=98 total=1440}
   → SendClientContent{"Count slowly from one to twenty."}
[barge +  989ms] outputTranscription="One, two, "        → SendClientContent{"Stop. Just say okay."}
[barge + 1068ms] interrupted
[barge + 1107ms] turnComplete usageMetadata{prompt=1475 resp=12 total=1487}
[barge-2nd +  575ms] outputTranscription="Okay."  … generationComplete … turnComplete
   → SendRealtimeInput{Text:"Thanks. Which agent was blocked?"}
[realtime-text +  705ms] outputTranscription="The docs agent was the " … "one that was blocked."
[realtime-text + 3230ms] turnComplete usageMetadata{prompt=1556 resp=67 total=1623}
handle captured: true
   → Close(); Connect(model, cfg{SessionResumption.Handle: <handle>})
resumed in 590ms sessionId=""
[after-resume +    0ms] setupComplete
[after-resume +    0ms] sessionResumptionUpdate{resumable=true handle=39c83e7e-ef6… lastIdx=0}
   → SendClientContent{"Remind me: what did the briefing say about docs?"}
[after-resume +  792ms] outputTranscription="The briefing stated that " … "docs was blocked" … " asking whether to delete" … " the old migration."
[after-resume + 5502ms] turnComplete usageMetadata{prompt=1657 resp=122 total=1779}
```

Earlier run with `ResponseModalities: TEXT`:

```
[hello +   21ms] sessionResumptionUpdate{resumable=true …}
[hello] receive error: websocket: close 1007 (invalid payload data): The requested combination of response modalities (TEXT) is not supported by the model. models/gemini_api_beyond_live_rev25_
```

Everything the plan needs was exercised except `GoAway` (needs a ~10 min
connection) and real microphone audio.

## 10. Deviations from sheepdog-v1-plan.md

- **§9 WP0 / §6 "cheaper TEXT smoke test":** `gemini-3.8-live` does not
  support `TEXT` response modality; tests must use `AUDIO` and read
  `outputTranscription`. Plan §6 (audio out) is unaffected.
- **§3.7 `push(text, urgency)` mapping:** `interrupt` = `SendClientContent`
  (TurnComplete true) sent immediately — it interrupts the current model
  turn by itself; `idle` = same call but the daemon must wait for
  `turnComplete` first (no server-side "when idle" for client content; that
  scheduling only exists for **function responses**); `silent` =
  `SendClientContent` with `TurnComplete: false` (verified: no generation,
  context retained).
- **§6 "after each compression, push briefing silently":** the API emits no
  compression event; detect via `usageMetadata.promptTokenCount` drop or push
  periodically.
- **§3.7 / §11 keyword biasing:** exists as
  `AudioTranscriptionConfig.CustomVocabulary` (accepted by the server) but
  applies to the transcription side; keep aliases in the system prompt too.
- **§11 Go SDK coverage of resumption and compression:** full; both verified
  (resumption end-to-end, compression accepted at setup).
- **§6 non-blocking tools:** exact names are
  `FunctionDeclaration.Behavior = genai.BehaviorNonBlocking` and
  `FunctionResponse.Scheduling = genai.FunctionResponseScheduling{Interrupt,WhenIdle,Silent}`.
  Additional observed semantics: a NON_BLOCKING call ends the model's turn
  immediately; the spoken result comes as a new generation after the tool
  response.
- **Concurrency:** the plan's "owns one WebSocket session" needs one reader
  goroutine plus a write mutex; the SDK session is not goroutine-safe.
- **Version requirement:** the SDK needs Go ≥ 1.24 (module has `go 1.25`, fine).

## 11. WP2 findings (live runs of `sheepdog voice-test`, 2026-09-17)

Five short sessions against `gemini-3.8-live` with the echo tools; all
free-tier, no 429 seen.

- **Do not answer a NON_BLOCKING call before its `turnComplete`.** The
  first run sent the function response < 1 ms after `toolCall` (before the
  server's `generationComplete`/`turnComplete` for that turn) and the model
  said "This function is not available at the moment." `internal/voice/gemini`
  now holds the response until that `turnComplete` (bounded by
  `Config.ToolResponseWait`, 1.5 s). With that, the result arrived as a new
  generation ~0.6 s after the response, as in §5.
- **The model judges the `spoken` text.** A bare `"briefing called with no
  arguments"` was not read out ("Let me get that update for you."); the
  sentence `"Echo test: the briefing tool was called with no arguments."`
  was read verbatim in two runs and paraphrased once ("Confirmed,
  proceedings will continue as planned." for `confirm`). Real Supervisor
  results (plan D9) are full sentences, so this only affects stubs. In one
  run the model called `briefing` twice back to back and then apologised
  that it "couldn't retrieve the current status" — model-side
  non-determinism worth watching once WP3 returns real briefings.
- **Resumption after an abrupt close.** Closing the socket mid-session and
  reconnecting immediately with the handle got
  `failed to receive setup complete: websocket: close 1011 (internal server error): Internal error encountered.`
  once; the retry 1 s later resumed (`resumed:true`) and the model answered
  a follow-up from pre-drop context. Keep the reconnect backoff.
- `SendRealtimeInput{Text}` produced no `inputTranscription` (only audio
  is transcribed); the model still called the right tool (`confirm` for
  "yes go ahead").
- Realtime audio after the WAV: the daemon must keep streaming silence
  frames after the utterance so the server VAD closes the turn; with
  silence the tool call arrived ~1.3 s after the end of speech.
- `voice-test` timings: connect 330–430 ms; first output audio 0.6–0.7 s
  after the tool response; idle push spoken ~0.9 s after `SendClientContent`.

## 12. WP7b findings (live daemon run, 2026-09-17)

See `docs/e2e-results.md` for the full run. Session-level observations:

- **No `GoAway` in a 13-minute session.** The server instead closed the
  connection with `close 1011 (internal server error): Internal error
  encountered.` at 13.1 min, and every reconnect that presented the
  resumption handle failed at setup with the same error after ~30 s. The
  session now drops the handle after `Config.ResumeAttempts` (1: the
  second try failed exactly like the first in both observed closes) such
  failures and reconnects fresh (`voice.handle_dropped`; the supervisor
  re-pushes the briefing on `EventResumed{ContextLost:true}`).
- **Zero-valued audio degrades turn handling.** With all-zero PCM frames
  streamed continuously (a hardware-muted mic, or a test client),
  `SendRealtimeInput{Text}` turns were answered only after 3.9–34.7 s (as
  soon as the stream stopped) and two of three idle pushes
  (`SendClientContent` with `turnComplete`) produced no response at all.
  A ±8 LSB noise floor, like a real microphone, behaved normally
  (0.9–1.0 s). Clients should send nothing while muted (the WS contract
  already says so) rather than zeros. The browser now gates near-zero
  frames itself (`docs/ws-contract.md`, "Silence gate") and the daemon
  sends `audioStreamEnd` when the gate closes.
- **The model sometimes answers a push with nothing.** An idle push whose
  turn never gets `turnComplete` used to hold every later idle push
  until an unrelated event; `pushReady` now re-checks after
  `TurnTimeout`. Pushes are also paraphrased unless the system prompt
  explains the `[system event …]` prefix (it does now).
- **Compression did not trigger** in 12 minutes of text-driven chatter:
  prompt tokens peaked at 4 277 against a 16 000 trigger; continuous
  silent/near-silent audio did not raise the count.
- Free tier, 4 keys: ~20 minutes connected across 4 sessions, no 429.


## 13. Turn health: stalls and recycling (first headset session, 2026-09-18)

In the first real headset session the model was fine for four minutes
(tool calls < 1 s after the utterance), then degraded on the same
connection: a `propose` call came 13 s after the utterance, an idle push
waited 18 s, three utterances in a row got no reaction at all, and after
~21 minutes the server closed with `1011 Internal error`; the resumption
handle was rejected and a fresh session lost the context. Prompt tokens
were only 6.4k, so this was not compression or a full window; the mic had
streamed near-silence for long stretches (§12, now gated client-side).

The session now measures every input turn and acts on the pattern:

- **`voice.turn`** is logged when a turn completes: `input_to_response_ms`
  (last input transcription fragment or typed text → first tool call or
  first output audio), `tool_calls`, `pushes_spoken`, `responded`,
  `stalled`, `end` (`complete` | `superseded` by new input after the model
  answered | `reconnect`). A tool call keeps the turn open until the
  result's own generation ends (`turnComplete` with no tool response owed),
  so the number covers the whole exchange.
- **`voice.stalled`** fires `Config.StallAfter` (`gemini.stall_after_s`,
  10 s) after the last input when the model has neither called a tool nor
  produced audio; `EventStatus{Stalled:true}` is emitted (the `/voice` dot
  turns red with "stalled (no reply)", `/debug` shows STALLED, `/api/state`
  has `voice.stalled`) and cleared by the model's next reaction. A
  `turnComplete` with no audio and no tool call closes the turn without
  counting as a stall (an empty answer is still an answer).
- **`voice.push_timeout`** counts idle pushes that outwaited `TurnTimeout`
  (§12: the model answered the push with nothing).
- **`voice.recycle {reason: stalled | push_timeout, handle}`**: the second
  stall, or the second push timeout, on one connection triggers a
  proactive reconnect with the current resumption handle while it is still
  valid — the GoAway path: dial, swap, close the old socket, replay any
  buffered audio — so the conversation survives instead of ending in a
  1011 whose handle is dead. Counters reset per connection. If the
  proactive dial fails the old connection is kept and the ordinary
  disconnect path runs later.
- The 1011 path presents the handle **once** before dropping it
  (`ResumeAttempts` default 1).

Whether the recycle actually prevents the 1011 is untested live (it needs
another degraded session); the numbers in `voice.turn` are what to look at
first when it happens again.
