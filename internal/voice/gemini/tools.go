package gemini

import (
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// toolDeclarations returns the plan §5 tool surface as NON_BLOCKING
// function declarations (docs/gemini-live-notes.md §5).
func toolDeclarations() []*genai.Tool {
	obj := func(props map[string]*genai.Schema, req []string) *genai.Schema {
		return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: req}
	}
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: "briefing", Behavior: genai.BehaviorNonBlocking,
			Description: "Current status of all agents in one or two spoken sentences: what each is doing, asking or last did. Call when the user asks what's happening, how things are going, or for a status.",
			Parameters:  obj(map[string]*genai.Schema{}, nil)},
		{Name: "ask", Behavior: genai.BehaviorNonBlocking,
			Description: "Answer something the user wants to KNOW by looking at an agent's screen, output, diff, logs or repo: 'what did it say', 'what did it do', 'what does that mean', 'explain the new section', 'ask you to explain …', 'is it stuck', 'why …', 'what changed'. Nothing is sent to the agent. Tell the user you're checking, then relay the answer when it arrives. When unsure between ask and propose, use ask.",
			Parameters: obj(map[string]*genai.Schema{
				"question": {Type: genai.TypeString, Description: "the user's question, in their words"},
				"agent":    {Type: genai.TypeString, Description: "alias only if the user said one; empty for 'it' / 'that one' (the supervisor uses the agent discussed last)"},
			}, []string{"question"})},
		{Name: "propose", Behavior: genai.BehaviorNonBlocking,
			Description: "Send an instruction or an answer TO an agent: only when the user tells an agent to do, stop or answer something ('tell auth to rerun the tests', 'ask docs to add a section', 'answer no'). Not for questions the user asks you. The system will echo what it will send; the user can say stop to cancel.",
			Parameters: obj(map[string]*genai.Schema{
				"agent":       {Type: genai.TypeString, Description: "alias only if the user said one; empty for 'it' / 'that one' (never guessed)"},
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
			Parameters:  obj(map[string]*genai.Schema{"text": {Type: genai.TypeString}}, []string{"text"})},
	}}}
}

// SystemPrompt renders the voice system prompt a Session with cfg would
// run with (the daemon serves it on /api/voice/system_prompt even when
// no session exists).
func SystemPrompt(cfg Config) (string, error) { return systemPrompt(cfg) }

// systemPrompt renders the voice system prompt with the aliases.
func systemPrompt(cfg Config) (string, error) {
	aliases := strings.Join(cfg.Aliases, ", ")
	if aliases == "" {
		aliases = "none configured yet"
	}
	if cfg.SystemPrompt != "" {
		return strings.ReplaceAll(cfg.SystemPrompt, "{aliases}", aliases), nil
	}
	return prompts.Render(prompts.VoiceSystem, map[string]string{"aliases": aliases})
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
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{CustomVocabulary: cfg.Aliases},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			TriggerTokens: genai.Ptr(cfg.CompressionTrigger),
			SlidingWindow: &genai.SlidingWindow{TargetTokens: genai.Ptr(cfg.CompressionTarget)},
		},
		SessionResumption: &genai.SessionResumptionConfig{Handle: handle},
	}
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

// pushTurn wraps a push as a user-role client turn. The bracketed prefix
// tells the model the text was not spoken by the user (notes §4).
func pushTurn(text string, u voice.Urgency) []*genai.Content {
	var prefix string
	if u == voice.UrgencySilent {
		prefix = "[system context, not spoken by the user] "
	} else {
		prefix = "[system event, not spoken by the user] "
	}
	return []*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: prefix + text}}}}
}

// errorResult is the spoken result when a tool handler fails (D9: written
// for the ear, no internals).
func errorResult(name string) voice.ToolResult {
	return voice.ToolResult{Spoken: fmt.Sprintf("Sorry, %s failed. Try again.", name), Scheduling: voice.UrgencyIdle}
}
