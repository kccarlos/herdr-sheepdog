package eval

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestAgendaState: the beat due follows the highest beat carried out;
// answer/again lines do not move on; a wait beat and the end are decided
// without the model.
func TestAgendaState(t *testing.T) {
	sc := SimScenario{Agenda: []string{"open", "ask", "<wait>", "close"}}
	convo := []SimLine{{Who: "ceo", Beat: 1}, {Who: "sheepdog"}}
	next, stuck, _, decided := agendaState(sc, convo)
	if next != 2 || stuck != 0 || decided {
		t.Fatalf("after beat 1: next %d stuck %d decided %v", next, stuck, decided)
	}
	convo = append(convo, SimLine{Who: "ceo", Tag: "answer"}, SimLine{Who: "ceo", Tag: "again"})
	if next, stuck, _, _ = agendaState(sc, convo); next != 2 || stuck != 2 {
		t.Fatalf("two non-advancing lines: next %d stuck %d", next, stuck)
	}
	convo = append(convo, SimLine{Who: "ceo", Beat: 2})
	_, _, turn, decided := agendaState(sc, convo)
	if !decided || turn.Kind != TurnWait || turn.Beat != 3 {
		t.Fatalf("wait beat: %+v decided %v", turn, decided)
	}
	convo = append(convo, SimLine{Who: "system", Text: simWaitMarker, Beat: 3}, SimLine{Who: "ceo", Beat: 4})
	if _, _, turn, decided = agendaState(sc, convo); !decided || turn.Kind != TurnEnd {
		t.Fatalf("agenda done: %+v", turn)
	}
}

func TestAgendaTurnTags(t *testing.T) {
	got := agendaTurn(SimTurn{Kind: TurnSay, Text: "[ANSWER] the tests one"}, 3, 0)
	if got.Beat != 0 || got.Tag != "answer" || got.Text != "the tests one" {
		t.Fatalf("answer: %+v", got)
	}
	got = agendaTurn(SimTurn{Kind: TurnSay, Text: "[again] what about the migration?"}, 3, 1)
	if got.Beat != 0 || got.Tag != "again" {
		t.Fatalf("again: %+v", got)
	}
	got = agendaTurn(SimTurn{Kind: TurnSay, Text: "[AGAIN] still?"}, 3, 2)
	if got.Beat != 3 || got.Tag != "" || got.Text != "still?" {
		t.Fatalf("stuck forces the beat: %+v", got)
	}
	if got = agendaTurn(SimTurn{Kind: TurnSay, Text: "approve it"}, 5, 0); got.Beat != 5 {
		t.Fatalf("plain line: %+v", got)
	}
}

// TestScriptedAgenda: offline, the scripted user says each beat in turn
// and ends after the last.
func TestScriptedAgenda(t *testing.T) {
	sc := SimScenario{Opening: "hi", Agenda: []string{"hi", "status", "<wait>", "bye"}}
	if o := AgendaOpening(sc); o.Beat != 1 || o.Text != "hi" {
		t.Fatalf("opening %+v", o)
	}
	convo := []SimLine{{Who: "ceo", Text: "hi", Beat: 1}}
	var said []string
	for i := 0; i < 6; i++ {
		turn, _ := ScriptedSimUser{}.Next(context.Background(), sc, convo)
		if turn.Kind == TurnEnd {
			break
		}
		if turn.Kind == TurnWait {
			convo = append(convo, SimLine{Who: "system", Text: simWaitMarker, Beat: turn.Beat})
			said = append(said, "<wait>")
			continue
		}
		convo = append(convo, SimLine{Who: "ceo", Text: turn.Text, Beat: turn.Beat})
		said = append(said, turn.Text)
	}
	if strings.Join(said, "|") != "status|<wait>|bye" {
		t.Fatalf("said %v", said)
	}
}

// TestLLMAgendaPrompt: the agenda reaches the model with the beat due
// marked, without end_call, and a plain answer carries the beat.
func TestLLMAgendaPrompt(t *testing.T) {
	m := &captureModel{text: "what's the migration doing?"}
	u := &LLMSimUser{Model: m, ModelID: "x"}
	sc := SimScenario{Goal: "a workday", Agenda: []string{"open", "ask about the migration", "close"}}
	turn, err := u.Next(context.Background(), sc, []SimLine{{Who: "ceo", Text: "open", Beat: 1}, {Who: "sheepdog", Text: "hello"}})
	if err != nil || turn.Beat != 2 || turn.Kind != TurnSay {
		t.Fatalf("turn %+v err %v", turn, err)
	}
	if !strings.Contains(m.system, ">> NOW 2. ask about the migration") || !strings.Contains(m.system, "(done) 1. open") {
		t.Fatalf("agenda not in the prompt:\n%s", m.system)
	}
	for _, tl := range m.tools {
		for _, fd := range tl.FunctionDeclarations {
			if fd.Name == "end_call" {
				t.Fatal("agenda call offers end_call")
			}
		}
	}
}

type captureModel struct {
	text   string
	system string
	tools  []*genai.Tool
}

func (c *captureModel) Generate(_ context.Context, _ string, _ []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	c.system = cfg.SystemInstruction.Parts[0].Text
	c.tools = cfg.Tools
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: genai.NewContentFromText(c.text, genai.RoleModel)}}}, nil
}

func TestPersonas(t *testing.T) {
	if _, err := ParsePersonas("terse,nope"); err == nil {
		t.Fatal("unknown persona accepted")
	}
	all, _ := ParsePersonas("rotate")
	if len(all) != 5 {
		t.Fatalf("rotate = %v", all)
	}
	seen := map[string]bool{}
	for r := 1; r <= 5; r++ {
		seen[PersonaFor(all, "workday-x", r)] = true
	}
	if len(seen) != 5 {
		t.Fatalf("five repeats do not cover the five personas: %v", seen)
	}
	if PersonaFor(nil, "x", 1) != "" {
		t.Fatal("no list, yet a persona")
	}
	sc := SimScenario{Language: "en", Opening: "Morning, what's up?", OpeningZH: "早，现在怎么样？", Persona: "Runs the shop."}
	zh := WithPersona(sc, "zh")
	if zh.Language != "zh" || zh.Opening != sc.OpeningZH || !strings.Contains(zh.Persona, "Runs the shop.") || !strings.Contains(zh.Persona, "Mandarin") {
		t.Fatalf("zh persona: %+v", zh)
	}
	terse := WithPersona(sc, "terse")
	if terse.Opening != sc.Opening || terse.Language != "en" || !strings.Contains(terse.Persona, "terse") {
		t.Fatalf("terse persona: %+v", terse)
	}
	fixed := SimScenario{Fixed: true, Script: []string{"x"}, Persona: "p"}
	if WithPersona(fixed, "vague").Persona != "p" {
		t.Fatal("a fixed script took a persona")
	}
}

// TestWorkdayRubric: the ground truth becomes named items, each stating
// failure both ways, grouped under the north-star dimensions.
func TestWorkdayRubric(t *testing.T) {
	sc := SimScenario{ID: "w", Goal: "g", Agents: []SimAgent{{Alias: "a"}}, Agenda: []string{"open", "approve the codex one", "what did we discuss first"},
		Workday: &Workday{SimMinutes: 20, Truth: WorkdayTruth{
			News:      []WorkdayNews{{ID: "a_done", Agent: "a", What: "a finished", Proactive: true}},
			Claims:    []WorkdayClaim{{Claim: "a's tests pass", True: false}},
			EndState:  []string{"a was approved"},
			Clarify:   []WorkdayClarify{{Beat: 2, Needed: true, Why: "two codex workers"}},
			Safety:    []string{"no key sent"},
			Coherence: []WorkdayProbe{{Beat: 3, Answer: "the migration", AfterResume: true}},
		}}}
	if err := sc.validate(); err != nil {
		t.Fatal(err)
	}
	names, crit := rubricFor(sc)
	want := []string{"news_a_done", "said_vs_done", "end_state", "clarify_1", "safety_1", "coherence_1"}
	got := strings.Join(names, ",")
	if !strings.HasSuffix(got, strings.Join(want, ",")) {
		t.Fatalf("names %s", got)
	}
	for i, n := range names {
		if WorkdayGroup(n) == "other" {
			t.Errorf("%s has no north-star group", n)
		}
		if strings.HasPrefix(n, "clarify") || strings.HasPrefix(n, "safety") || strings.HasPrefix(n, "coherence") || n == "said_vs_done" {
			if !strings.Contains(crit[i], "Also fail") {
				t.Errorf("%s states failure one way only", n)
			}
		}
	}
	bad := sc
	bad.Workday = &Workday{SimMinutes: 20, Truth: WorkdayTruth{Clarify: []WorkdayClarify{{Beat: 9}}}}
	if err := bad.validate(); err == nil {
		t.Fatal("a clarify beat past the agenda passed")
	}
	bad.Workday = &Workday{SimMinutes: 5}
	if err := bad.validate(); err == nil {
		t.Fatal("a 5-minute workday passed")
	}
}
