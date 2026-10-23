package config

// Voice is the voice's spoken half, as the operator meets it.
type Voice struct {
	// Name is what the voice calls itself, substituted into
	// prompts/assistant.md as {name} (default "Sheepdog").
	Name string `toml:"name"`
	// Provider is the voice model the daemon starts on: "gemini" (Gemini
	// Live, [gemini]) or "openai" (GPT-Live, [voice.openai]). The initial
	// value only: the provider picked at runtime (PUT
	// /api/voice/provider, /debug) is kept in the state file's
	// session.voice_provider and wins over this on restart.
	Provider string `toml:"provider"`
	// OpenAI configures the GPT-Live provider.
	OpenAI VoiceOpenAI `toml:"openai"`
}

// VoiceOpenAI configures the GPT-Live voice provider (billed per second
// of open session, so the daemon closes it when idle).
type VoiceOpenAI struct {
	// APIKey authenticates GPT-Live (env OPENAI_API_KEY). Never logged.
	// The voice only: the orchestrator never uses it.
	APIKey string `toml:"api_key"`
	// Model is the Live model (default gpt-live-1).
	Model string `toml:"model"`
	// Voice is the output voice (default marin).
	Voice string `toml:"voice"`
	// IdleCloseS closes a live GPT-Live session after this many seconds
	// without speech, a reply, a delegation or a push (default 45).
	IdleCloseS int `toml:"idle_close_s"`
}

// VoiceProviders are the accepted values of voice.provider.
var VoiceProviders = []string{"gemini", "openai"}

// GeminiSensitivities are the accepted values of
// gemini.start_sensitivity / gemini.end_sensitivity.
var GeminiSensitivities = []string{"low", "high", "default"}

// DefaultVoiceName is the built-in voice.name: what the voice calls
// itself (docs/dev/roles.md "Names").
const DefaultVoiceName = "Sheepdog"

// Gemini configures the voice session (plan §6).
type Gemini struct {
	// APIKeys is the key pool. The voice session binds to one key per
	// connection and rotates to the next on a rate-limit error at connect.
	// Never logged.
	APIKeys []string `toml:"api_keys"`
	// Model is the Live model name.
	Model string `toml:"model"`
	// Voice is the prebuilt voice name.
	Voice string `toml:"voice"`
	// StallAfterS is how many seconds an utterance may go without any
	// model response (tool call or audio) before the session logs
	// voice.stalled; two stalls on one connection recycle it.
	StallAfterS int `toml:"stall_after_s"`
	// StartSensitivity / EndSensitivity tune Live's automatic activity
	// detection: "low", "high" or "default" (the server's own). Start
	// defaults to low: room noise was transcribed as speech ("fishing",
	// "Discord") at the default.
	StartSensitivity string `toml:"start_sensitivity"`
	EndSensitivity   string `toml:"end_sensitivity"`
	// BriefingChannel is how the daemon's and the orchestrator's words reach the
	// Live model (docs/dev/design/dual-brain.md): "function_response" (the
	// default; a function response labelled with provenance) or
	// "user_turn" (the older marked user-role client turn, the A/B
	// control).
	BriefingChannel string `toml:"briefing_channel"`
	// Backend is where the Live connection goes: "aistudio" (Google AI
	// Studio, the api_keys pool) or "vertex" (Vertex AI, [gemini.vertex]).
	// Everything above the connection is the same for both.
	Backend string `toml:"backend"`
	// Vertex configures the Vertex AI backend (used when Backend is
	// "vertex").
	Vertex GeminiVertex `toml:"vertex"`
}

// GeminiVertex configures Gemini Live on Vertex AI: one API key (no
// pool), a project and a location.
type GeminiVertex struct {
	// APIKey is the Vertex AI API key (bound to a service account). Never
	// logged.
	APIKey string `toml:"api_key"`
	// Project is the Google Cloud project id.
	Project string `toml:"project"`
	// Location is the Vertex AI location the Live model is served from
	// (gemini-3.8-live is not served from "global").
	Location string `toml:"location"`
}

// GeminiBackends are the accepted values of gemini.backend.
var GeminiBackends = []string{"aistudio", "vertex"}

// GeminiBriefingChannels are the valid gemini.briefing_channel values.
var GeminiBriefingChannels = []string{"function_response", "user_turn"}
