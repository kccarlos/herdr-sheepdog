package eval

import (
	"context"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// AssistantModel is the assistant set's model surface: a plain
// (non-Live) GenerateContent call. *brain.GenaiModel implements it.
type AssistantModel interface {
	Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// DefaultAssistantModel is the model `sheepdog eval --set assistant
// --live` calls.
const DefaultAssistantModel = "gemini-3.5-flash"

// defaultAliases is used when a case does not set Aliases.
var defaultAliases = []string{"auth", "docs"}

// RunAssistant runs cases through model (nil: offline — files are only
// validated). pl resolves prompts/assistant.md (nil: embedded default).
// Cases after a rate-limited call are reported as skipped rather than
// retried (the Gemini free tier needs a cool-down, not a hot loop).
func RunAssistant(ctx context.Context, cases []AssistantCase, pl *prompts.Loader, model AssistantModel, modelName string) []Result {
	out := make([]Result, 0, len(cases))
	for i, c := range cases {
		if model == nil {
			out = append(out, Result{ID: c.ID, Status: StatusSkipped, Detail: "offline"})
			continue
		}
		r := runAssistantCase(ctx, c, pl, model, modelName)
		out = append(out, r)
		if r.Err != nil && isRateLimited(r.Err) {
			for _, rest := range cases[i+1:] {
				out = append(out, Result{ID: rest.ID, Status: StatusSkipped, Detail: "rate limited (429); stopped rather than hot-retry"})
			}
			break
		}
	}
	return out
}

// isRateLimited reports whether err looks like a 429 / RESOURCE_EXHAUSTED
// from the Gemini API (brain.GenaiModel already rotates keys on this and
// only returns it once every key was tried).
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"resource_exhausted", "429", "rate limited", "quota exceeded"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func runAssistantCase(ctx context.Context, c AssistantCase, pl *prompts.Loader, model AssistantModel, modelName string) Result {
	aliases := c.Aliases
	if len(aliases) == 0 {
		aliases = defaultAliases
	}
	prompt, _ := pl.Render(prompts.Assistant, prompts.AssistantVars("", aliases))
	contents := buildAssistantContents(c.Context, c.Utterance)
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: prompt}}},
		Tools:             gemini.TextToolDeclarations(),
	}
	t0 := time.Now()
	resp, err := model.Generate(ctx, modelName, contents, cfg)
	dur := time.Since(t0)
	if err != nil {
		return Result{ID: c.ID, Status: StatusFail, Detail: firstLine(err.Error()), Err: err, Duration: dur}
	}
	calls := resp.FunctionCalls()
	gotTool, gotAgent, gotText := "none", "", ""
	if len(calls) > 0 {
		call := calls[0]
		gotTool = call.Name
		if a, ok := call.Args["agent"].(string); ok {
			gotAgent = a
		}
		for _, k := range []string{"question", "instruction", "text", "answer"} {
			if s, ok := call.Args[k].(string); ok && s != "" {
				gotText = s
				break
			}
		}
	}
	reasons := checkAssistantExpect(c.Expect, gotTool, gotAgent, gotText, c.Utterance)
	if len(reasons) > 0 {
		return Result{ID: c.ID, Status: StatusFail, Detail: strings.Join(reasons, "; "), Duration: dur,
			Got: map[string]any{"tool": gotTool, "agent": gotAgent, "text": gotText}}
	}
	return Result{ID: c.ID, Status: StatusPass, Duration: dur, Got: map[string]any{"tool": gotTool, "agent": gotAgent, "text": gotText}}
}

// checkAssistantExpect compares what the model did against exp, returning
// a failure reason per mismatch (empty: pass).
func checkAssistantExpect(exp AssistantExpect, gotTool, gotAgent, gotText, utterance string) []string {
	var reasons []string
	if exp.Tool != "" && exp.Tool != gotTool {
		reasons = append(reasons, "tool="+gotTool+", want "+exp.Tool)
	}
	if exp.Verbatim && strings.TrimSpace(gotText) != strings.TrimSpace(utterance) {
		reasons = append(reasons, "text "+quoteOrEmpty(gotText)+" is not the utterance verbatim ("+quoteOrEmpty(utterance)+")")
	}
	if exp.Agent != nil && *exp.Agent != gotAgent {
		reasons = append(reasons, "agent="+quoteOrEmpty(gotAgent)+", want "+quoteOrEmpty(*exp.Agent))
	}
	if len(exp.QuestionContains) > 0 {
		lower := strings.ToLower(gotText)
		found := false
		for _, want := range exp.QuestionContains {
			if strings.Contains(lower, strings.ToLower(want)) {
				found = true
				break
			}
		}
		if !found {
			reasons = append(reasons, "text "+quoteOrEmpty(gotText)+" contains none of: "+strings.Join(exp.QuestionContains, ", "))
		}
	}
	return reasons
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return `""`
	}
	return s
}

// buildAssistantContents renders context lines and the final utterance as
// model contents: a "Dog: " line is the model's own prior turn; anything
// else (a CEO utterance, or a push with its bracketed prefix exactly as
// internal/voice/gemini's pushTurn renders it) is a user turn.
func buildAssistantContents(context []string, utterance string) []*genai.Content {
	out := make([]*genai.Content, 0, len(context)+1)
	for _, line := range context {
		if text, ok := strings.CutPrefix(line, "Dog: "); ok {
			out = append(out, &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: text}}})
			continue
		}
		out = append(out, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: line}}})
	}
	out = append(out, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: utterance}}})
	return out
}
