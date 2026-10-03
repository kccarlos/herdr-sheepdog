package gemini

import (
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// toolDeclarations returns the voice's two tools as NON_BLOCKING
// function declarations (docs/roles.md "One decision-maker";
// docs/gemini-live-notes.md §5). The voice classifies nothing and
// executes nothing: it hands the user's words to the supervisor and
// says stop when they do.
func toolDeclarations() []*genai.Tool {
	obj := func(props map[string]*genai.Schema, req []string) *genai.Schema {
		return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: req}
	}
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: voice.ToolToSupervisor, Behavior: genai.BehaviorNonBlocking,
			Description: "Hand the user's words to the supervisor, verbatim, whenever they are about the agents or the work: a status question, something to find out, an instruction, an answer to an agent's question ('yes', 'option two'), 'let me know when…', 'remember…', 'explain…', a doubt ('are you sure?'). The supervisor decides what they mean and what to do. Do not paraphrase and do not guess which agent is meant. Say 'on it' or 'checking' as you call it; the reply comes back as spoken, detail, options, clarify and pending.",
			Parameters: obj(map[string]*genai.Schema{
				"text": {Type: genai.TypeString, Description: "exactly what the user said, in their own words"},
			}, []string{"text"})},
		{Name: voice.ToolStop, Behavior: genai.BehaviorNonBlocking,
			Description: "The user says stop, wait, cancel, no no. Call it at once, before anything else; the supervisor works out what to stop and tells you. \"Never mind\" is not stop: hand it to to_supervisor.",
			Parameters:  obj(map[string]*genai.Schema{}, nil)},
	}}}
}

// SystemPrompt renders the voice system prompt a Session with cfg would
// run with (the daemon serves it on /api/voice/system_prompt even when
// no session exists).
func SystemPrompt(cfg Config) (string, error) { return systemPrompt(cfg) }

// TextToolDeclarations returns the same two tool declarations as the
// voice session's, without the Live-only Behavior field (a plain
// GenerateContent call, unlike LiveConnect, may reject a non-zero
// Behavior). Used by `sheepdog eval --set assistant --live`, which runs
// the assistant prompt through gemini-3.5-flash in text mode.
func TextToolDeclarations() []*genai.Tool {
	live := toolDeclarations()
	decls := make([]*genai.FunctionDeclaration, len(live[0].FunctionDeclarations))
	for i, d := range live[0].FunctionDeclarations {
		cp := *d
		cp.Behavior = ""
		decls[i] = &cp
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// systemPrompt renders the voice system prompt with the dog's name and
// the aliases.
func systemPrompt(cfg Config) (string, error) {
	vars := prompts.AssistantVars(cfg.Name, cfg.names())
	if cfg.SystemPrompt != "" {
		text := cfg.SystemPrompt
		for k, v := range vars {
			text = strings.ReplaceAll(text, "{"+k+"}", v)
		}
		return text, nil
	}
	text, _ := cfg.Prompts.Render(prompts.Assistant, vars)
	return text, nil
}

// liveConfig builds the LiveConnectConfig for one connection (plan §6).
// handle is the resumption handle, empty for a fresh session.
func liveConfig(cfg Config, prompt, handle string) *genai.LiveConnectConfig {
	return &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SpeechConfig: &genai.SpeechConfig{
			VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: cfg.Voice}},
		},
		SystemInstruction:        &genai.Content{Parts: []*genai.Part{{Text: prompt}}},
		Tools:                    toolDeclarations(),
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{CustomVocabulary: cfg.names()},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			TriggerTokens: genai.Ptr(cfg.CompressionTrigger),
			SlidingWindow: &genai.SlidingWindow{TargetTokens: genai.Ptr(cfg.CompressionTarget)},
		},
		SessionResumption:   &genai.SessionResumptionConfig{Handle: handle},
		RealtimeInputConfig: realtimeInput(cfg),
	}
}

// realtimeInput maps the configured activity-detection sensitivities
// (config gemini.start_sensitivity / end_sensitivity) onto Live's
// automatic activity detection; nil when both are "default" or unset.
func realtimeInput(cfg Config) *genai.RealtimeInputConfig {
	var aad genai.AutomaticActivityDetection
	switch cfg.StartSensitivity {
	case "low":
		aad.StartOfSpeechSensitivity = genai.StartSensitivityLow
	case "high":
		aad.StartOfSpeechSensitivity = genai.StartSensitivityHigh
	}
	switch cfg.EndSensitivity {
	case "low":
		aad.EndOfSpeechSensitivity = genai.EndSensitivityLow
	case "high":
		aad.EndOfSpeechSensitivity = genai.EndSensitivityHigh
	}
	if aad.StartOfSpeechSensitivity == "" && aad.EndOfSpeechSensitivity == "" {
		return nil
	}
	return &genai.RealtimeInputConfig{AutomaticActivityDetection: &aad}
}

// scheduling maps an Urgency to the function response scheduling hint.
func scheduling(u voice.Urgency) genai.FunctionResponseScheduling {
	switch u {
	case voice.UrgencyInterrupt:
		return genai.FunctionResponseSchedulingInterrupt
	case voice.UrgencySilent:
		return genai.FunctionResponseSchedulingSilent
	default:
		return genai.FunctionResponseSchedulingWhenIdle
	}
}

// The push markers: a user-role turn that starts with one is the brain's
// (or the daemon's) words, never the shepherd's (notes §4;
// prompts/assistant.md explains both).
const (
	pushEventMarker   = "[system event, not spoken by the user]"
	pushContextMarker = "[system context, not spoken by the user]"
)

// pushTurn wraps a push as a user-role client turn: the marker, then the
// text, as two parts of one turn. With the marker glued to the text
// (one part) the voice read it aloud in 25 utterances of the 2026-10-03
// tier-2 campaign ("[system event, not spoken by the user] bench-codex
// wants…"; docs/bench/tier2-2026-10-03.md #4). Measured on
// barge-in-hold-to-talk, status-en and deferral-zh, 3 runs each, with
// the same prompt (prompts/assistant.md explains the marker): glued,
// 3 of 17 spoken pushes began with the marker; as its own part, 0 of 19;
// a non-word marker ("»", the prompt rewritten for it) also read none
// (0 of 16), but the voice then
// relayed four pushes back as the shepherd's words.
func pushTurn(text string, u voice.Urgency) []*genai.Content {
	marker := pushEventMarker
	if u == voice.UrgencySilent {
		marker = pushContextMarker
	}
	return []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: marker}, {Text: text}}}}
}

// errorResult is the spoken result when a tool handler fails (D9: written
// for the ear, no internals).
func errorResult(name string) voice.ToolResult {
	return voice.ToolResult{Spoken: fmt.Sprintf("Sorry, %s failed. Try again.", name), Scheduling: voice.UrgencyIdle}
}
