package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// SimTurn is what the simulated user does next.
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
	// Beat and Tag, with an agenda: the beat this turn carries out (0:
	// none) and, for a line that does not move on, answer or again
	// (SimLine.Beat, SimLine.Tag).
	Beat int
	Tag  string
}

// SimTurn kinds.
const (
	TurnSay  = "say"
	TurnWait = "wait"
	TurnEnd  = "end"
)

// SimUser plays the user.
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
	system := fmt.Sprintf(simUserSystem, sc.Goal, persona, lang)
	tools := simUserTools
	var next, stuck int
	if len(sc.Agenda) > 0 {
		var t SimTurn
		var done bool
		next, stuck, t, done = agendaState(sc, convo)
		if done {
			return t, nil
		}
		system = fmt.Sprintf(simUserAgendaSystem, sc.Goal, persona, lang, agendaList(sc.Agenda, next, stuck))
		tools = simUserAgendaTools
		if waitedSinceSpoke(convo) {
			// Already waited since the user last spoke: talk now (a
			// second wait only runs the silence cap down).
			tools = nil
		}
	}
	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
		Tools:             tools,
		Temperature:       genai.Ptr[float32](0.7),
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := u.Model.Generate(ctx, u.ModelID, simUserContents(convo), cfg)
		if err != nil {
			return SimTurn{}, fmt.Errorf("simulated user: %w", err)
		}
		if t, ok := simTurnOf(resp); ok {
			if len(sc.Agenda) > 0 {
				t = agendaTurn(t, next, stuck)
			}
			return t, nil
		}
		last = fmt.Errorf("simulated user: empty turn")
	}
	return SimTurn{}, last
}

const simUserAgendaSystem = `You are role-playing a busy CEO talking by voice (on a phone, eyes off the screen) to "sheepdog", your voice assistant that supervises AI coding agents ("workers") running in terminal panes. You cannot see any screen; you only hear what sheepdog says. This is a long call during a working day.

WHAT THIS CALL IS ABOUT: %s
%s
How to talk:
- One spoken utterance per turn, the way people talk on the phone. No stage directions, no quotes, no "CEO:" prefix, no emoji.
- Speak %s unless your agenda or speaking style says otherwise.
- Do not invent facts about the workers; ask sheepdog. Only count something as done when sheepdog said so.
- Never ask sheepdog to stop, approve, decline or change anything your agenda does not say. If sheepdog asks permission for something your agenda does not cover, say no.

YOUR AGENDA, one item per turn, in order:
%s
Each turn, do the item marked NOW: say it in your own words and in your speaking style (quoted words may be said nearly as written; notes in brackets are for you only, never say them). When the item is a decision (approve, not now, stop, decline), give exactly that decision.
Two exceptions, which do not move the agenda on:
- Sheepdog just asked you something that needs your answer (which one, yes or no, a choice): answer it, and start that line with [ANSWER].
- Sheepdog did not answer your previous item at all, or you could not follow the answer: repeat or rephrase it, and start that line with [AGAIN]. At most twice per item; then move on.
If sheepdog said it is checking and will tell you, you may call wait_for_sheepdog once instead of talking. The call ends by itself after the last item; do not say goodbye early.`

// simUserAgendaTools: an agenda call has no end_call (it ends when the
// beats run out), only the wait.
var simUserAgendaTools = []*genai.Tool{{FunctionDeclarations: simUserTools[0].FunctionDeclarations[1:]}}

// AgendaOpening is the opening turn: beat 1 when the scenario has an
// agenda.
func AgendaOpening(sc SimScenario) SimTurn {
	t := SimTurn{Kind: TurnSay, Text: sc.Opening}
	if len(sc.Agenda) > 0 {
		t.Beat = 1
	}
	return t
}

// agendaState reads where the call is in the agenda: next is the beat
// due (1-based), stuck how many lines since the last beat did not move
// on. When the agenda is done, or the beat due is a wait, the turn is
// decided here (decided true) without the model.
func agendaState(sc SimScenario, convo []SimLine) (next, stuck int, t SimTurn, decided bool) {
	done := 0
	for _, l := range convo {
		if l.Beat > done {
			done = l.Beat
			stuck = 0
		} else if l.Who == "ceo" && l.Beat == 0 {
			stuck++
		}
	}
	next = done + 1
	if next > len(sc.Agenda) {
		return next, stuck, SimTurn{Kind: TurnEnd, Reason: "agenda done"}, true
	}
	if strings.TrimSpace(sc.Agenda[next-1]) == "<wait>" {
		return next, stuck, SimTurn{Kind: TurnWait, WaitSeconds: 30, Beat: next}, true
	}
	return next, stuck, SimTurn{}, false
}

// waitedSinceSpoke reports whether the user already waited (a wait
// marker) since their last line.
func waitedSinceSpoke(convo []SimLine) bool {
	for i := len(convo) - 1; i >= 0; i-- {
		switch {
		case convo[i].Who == "ceo":
			return false
		case convo[i].Who == "system" && convo[i].Text == simWaitMarker:
			return true
		}
	}
	return false
}

// agendaList renders the agenda for the prompt, the beat due marked.
func agendaList(agenda []string, next, stuck int) string {
	var b strings.Builder
	for i, a := range agenda {
		n := i + 1
		mark := "   "
		switch {
		case n < next:
			mark = "(done)"
		case n == next:
			mark = ">> NOW"
		}
		if strings.TrimSpace(a) == "<wait>" {
			a = "[stay quiet and let sheepdog report]"
		}
		fmt.Fprintf(&b, "%s %d. %s\n", mark, n, a)
	}
	if stuck >= 2 {
		b.WriteString("You have already spent two lines without moving on: do the NOW item this turn, no [ANSWER] or [AGAIN].\n")
	}
	return b.String()
}

// agendaTurn tags the model's turn: a line marked [ANSWER] or [AGAIN]
// does not move on (unless stuck), any other line carries out beat next.
func agendaTurn(t SimTurn, next, stuck int) SimTurn {
	if t.Kind != TurnSay {
		return t
	}
	for _, tag := range []string{"answer", "again"} {
		marker := "[" + strings.ToUpper(tag) + "]"
		if i := strings.Index(strings.ToUpper(t.Text), marker); i >= 0 {
			t.Text = strings.TrimSpace(t.Text[:i] + t.Text[i+len(marker):])
			if stuck < 2 {
				t.Tag = tag
				return t
			}
		}
	}
	t.Beat = next
	return t
}

// simUserContents renders the conversation from the user's side: their own
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
	if len(sc.Agenda) > 0 && len(sc.Script) == 0 {
		// Offline: each beat said as written.
		next, _, t, decided := agendaState(sc, convo)
		if decided {
			if t.Kind == TurnWait {
				t.WaitSeconds = 5
			}
			return t, nil
		}
		return SimTurn{Kind: TurnSay, Text: sc.Agenda[next-1], Beat: next}, nil
	}
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
