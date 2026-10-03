package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// SimTurn is what the simulated CEO does next.
type SimTurn struct {
	// Kind is say, wait (stay quiet until sheepdog reports back) or end
	// (end_call).
	Kind string
	Text string
	// Reason is end_call's reason; GoalMet its self-report, which is
	// recorded but never graded (the judge decides).
	Reason  string
	GoalMet bool
	// WaitSeconds is how long a wait is willing to last.
	WaitSeconds int
}

// SimTurn kinds.
const (
	TurnSay  = "say"
	TurnWait = "wait"
	TurnEnd  = "end"
)

// SimUser plays the CEO.
type SimUser interface {
	Next(ctx context.Context, sc SimScenario, convo []SimLine) (SimTurn, error)
}

// LLMSimUser is a SimUser over a text model with two tools, end_call
// and wait_for_sheepdog.
type LLMSimUser struct {
	Model   brain.Model
	ModelID string
}

const simUserSystem = `You are role-playing a busy CEO talking by voice (on a phone, eyes off the screen) to "sheepdog", your voice assistant that supervises AI coding agents ("workers") running in terminal panes. You cannot see any screen; you only hear what sheepdog says.

YOUR GOAL FOR THIS CALL: %s
%s
How to talk:
- One short spoken utterance per turn, the way people talk on the phone (usually 3 to 15 words). No stage directions, no quotes, no "CEO:" prefix, no emoji.
- Speak %s unless your instructions say to switch.
- Refer to workers the way a person would ("the codex one", "bench-codex", "the tests").
- If sheepdog asks you something, answer it in line with your goal. If it asks permission for something your goal does not cover, say no.
- If sheepdog says it is checking or will tell you later, call wait_for_sheepdog instead of filling the silence.
- Do not invent facts about the workers; ask sheepdog.
- Do not ask sheepdog to stop, approve, decline or change anything your goal does not mention.

When your goal is achieved, or after two or three tries it clearly will not be, call end_call with a short reason. Only count something as done when sheepdog said so; never assume an action happened. Never keep the call going just to be polite.`

var simUserTools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
	{
		Name:        "end_call",
		Description: "Hang up: the goal is achieved, or clearly will not be.",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
			"reason":   {Type: genai.TypeString, Description: "Why you are hanging up, in a few words."},
			"goal_met": {Type: genai.TypeBoolean, Description: "Whether you think your goal was achieved."},
		}, Required: []string{"reason", "goal_met"}},
	},
	{
		Name:        "wait_for_sheepdog",
		Description: "Stay quiet and wait for sheepdog to report back (it said it is checking, or a worker is still busy).",
		Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
			"seconds": {Type: genai.TypeInteger, Description: "How long you are willing to wait, 10 to 60."},
		}, Required: []string{"seconds"}},
	},
}}}

// Next implements SimUser.
func (u *LLMSimUser) Next(ctx context.Context, sc SimScenario, convo []SimLine) (SimTurn, error) {
	if u == nil || u.Model == nil {
		return SimTurn{}, errNoModel
	}
	lang := "English"
	if sc.Language == "zh" {
		lang = "Mandarin Chinese (simplified characters)"
	}
	persona := ""
	if p := strings.TrimSpace(sc.Persona); p != "" {
		persona = "\nMore about you and this call: " + p + "\n"
	}
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(fmt.Sprintf(simUserSystem, sc.Goal, persona, lang), genai.RoleUser),
		Tools:             simUserTools,
		Temperature:       genai.Ptr[float32](0.7),
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := u.Model.Generate(ctx, u.ModelID, simUserContents(convo), cfg)
		if err != nil {
			return SimTurn{}, fmt.Errorf("simulated user: %w", err)
		}
		if t, ok := simTurnOf(resp); ok {
			return t, nil
		}
		last = fmt.Errorf("simulated user: empty turn")
	}
	return SimTurn{}, last
}

// simUserContents renders the conversation from the CEO's side: their own
// lines are the model's turns, sheepdog's are the user's.
func simUserContents(convo []SimLine) []*genai.Content {
	var out []*genai.Content
	add := func(role, text string) {
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts[0].Text += "\n" + text
			return
		}
		out = append(out, &genai.Content{Role: role, Parts: []*genai.Part{{Text: text}}})
	}
	add(genai.RoleUser, "[The call connects. Sheepdog is listening.]")
	for _, l := range convo {
		switch l.Who {
		case "ceo":
			add(genai.RoleModel, l.Text)
		case "sheepdog":
			add(genai.RoleUser, "Sheepdog: "+l.Text)
		case "push":
			add(genai.RoleUser, "Sheepdog (on its own): "+l.Text)
		case "system":
			add(genai.RoleUser, "["+l.Text+"]")
		}
	}
	if out[len(out)-1].Role == genai.RoleModel {
		add(genai.RoleUser, "[Sheepdog says nothing.]")
	}
	return out
}

// simTurnOf reads the model's turn: a tool call, or the text it says.
// Models without tool calling may write [END_CALL] or [WAIT] instead.
func simTurnOf(resp *genai.GenerateContentResponse) (SimTurn, bool) {
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			fc := p.FunctionCall
			if fc == nil {
				continue
			}
			switch fc.Name {
			case "end_call":
				reason, _ := fc.Args["reason"].(string)
				met, _ := fc.Args["goal_met"].(bool)
				return SimTurn{Kind: TurnEnd, Reason: reason, GoalMet: met}, true
			case "wait_for_sheepdog":
				return SimTurn{Kind: TurnWait, WaitSeconds: toInt(fc.Args["seconds"])}, true
			}
		}
	}
	text := strings.TrimSpace(textOf(resp))
	switch {
	case strings.Contains(text, "[END_CALL]"):
		return SimTurn{Kind: TurnEnd, Reason: strings.TrimSpace(strings.ReplaceAll(text, "[END_CALL]", ""))}, true
	case strings.Contains(text, "[WAIT]"):
		return SimTurn{Kind: TurnWait, WaitSeconds: 30}, true
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "CEO:"), "Me:")
	text = strings.Trim(strings.TrimSpace(text), `"“”`)
	if text == "" {
		return SimTurn{}, false
	}
	return SimTurn{Kind: TurnSay, Text: text}, true
}

// ScriptedSimUser says the scenario's Script lines in order, one per
// turn ("<wait>" waits), then ends the call: the offline stand-in.
type ScriptedSimUser struct{}

// Next implements SimUser.
func (ScriptedSimUser) Next(_ context.Context, sc SimScenario, convo []SimLine) (SimTurn, error) {
	n := 0
	for _, l := range convo {
		if l.Who == "ceo" || (l.Who == "system" && l.Text == simWaitMarker) {
			n++
		}
	}
	lines := sc.Script
	if sc.Opening != "" && len(lines) > 0 && lines[0] == sc.Opening {
		lines = lines[1:]
	}
	if sc.Opening != "" {
		n-- // the opening is not a script line
	}
	if n < 0 {
		n = 0
	}
	if n >= len(lines) {
		return SimTurn{Kind: TurnEnd, Reason: "script done"}, nil
	}
	if lines[n] == "<wait>" {
		return SimTurn{Kind: TurnWait, WaitSeconds: 5}, nil
	}
	return SimTurn{Kind: TurnSay, Text: lines[n]}, nil
}

// simWaitMarker is the system line a wait leaves in the conversation.
const simWaitMarker = "The CEO waits in silence."

func sortLines(lines []SimLine) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].AtMS < lines[j].AtMS })
}
