package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

const failingScreen = `› run the tests

• Ran go test ./...
  --- FAIL: TestAdd (0.00s)
      add_test.go:9: add(2, 3) = 3, want 5
  FAIL
• The add function ignores its second argument.

› Ask Codex to do anything
`

func fixScenario() SimScenario {
	return SimScenario{
		ID:       "fix-test",
		Goal:     "find out why bench-codex's test fails and get it fixed",
		Opening:  "why is bench-codex's test failing?",
		Script:   []string{"why is bench-codex's test failing?", "ok, tell it to fix it"},
		Agents:   []SimAgent{{Alias: "bench-codex", State: "idle", Screen: failingScreen}, {Alias: "bench-claude", Kind: "claude", State: "working", Screen: "codex-working.screen.txt"}},
		Events:   []SimEvent{{OnPromptTo: "bench-codex", PromptMatches: "fix", DelayMS: 100, State: "done", AppendScreen: "• Fixed add(); go test ./... passes (ok 0.01s)."}},
		Caps:     SimCaps{MaxTurns: 4, MaxSeconds: 30, SilenceSeconds: 2},
		Expect:   SimExpect{Actions: []SimAction{{Kind: ActionPrompt, Agent: "bench-codex", Matches: "fix"}}, Agent: "bench-codex"},
		Language: "en",
	}
}

// fixBrain is a scripted brain: it answers the question from the screen,
// then on the instruction prompts bench-codex.
func fixBrain() brain.Model {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "bench-codex"})}},
		braintest.Step{Text: braintest.Reply("add ignores its second number, so the test gets 3 instead of 5.", "")},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "bench-codex", "text": "Fix add() so the failing test passes."})}},
		braintest.Step{Text: braintest.Reply("Told bench-codex to fix it.", "")},
	)
	m.EventDefault = &braintest.Step{Text: `{"spoken": "bench-codex fixed it; the tests pass.", "detail": "", "needs_user": false, "urgency": "idle"}`}
	return m
}

func TestRunSimScriptedConversation(t *testing.T) {
	fj := &FakeJudge{}
	run := RunSim(context.Background(), fixScenario(), SimOptions{
		BrainConfig:   brain.Config{MaxSteps: 4, UtteranceTimeout: 5 * time.Second},
		NewBrainModel: fixBrain,
		NewJudge:      func(*Meter) Judge { return fj },
		FixtureDir:    "../../testdata/herdr",
		Settle:        1500 * time.Millisecond, // the finish lands 200 ms after the prompt; slack for -race on CI
	}, 1)
	if run.Status != RunPass {
		t.Fatalf("status %s, failures %+v\n%s", run.Status, run.Failures, renderSim(run.Transcript))
	}
	if run.EndReason != EndCall || run.Turns != 2 {
		t.Errorf("end %s after %d turns", run.EndReason, run.Turns)
	}
	if len(run.Actions) != 1 || run.Actions[0].Kind != ActionPrompt || run.Actions[0].Agent != "bench-codex" {
		t.Fatalf("actions = %+v", run.Actions)
	}
	text := renderSim(run.Transcript)
	for _, want := range []string{"CEO: why is bench-codex's test failing?", "Sheepdog: add ignores", "CEO: ok, tell it to fix it", "Sheepdog: Told bench-codex", "Sheepdog (unprompted): bench-codex fixed it"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	if len(run.Verdicts) != 7 || run.Verdicts[0].Name != "answered" {
		t.Errorf("verdicts = %+v", run.Verdicts)
	}
	in := fj.Inputs()[len(fj.Inputs())-1]
	if !strings.Contains(in.Facts, `sheepdog sent bench-codex the prompt "Fix add()`) || !strings.Contains(in.Facts, "bench-codex is now done") {
		t.Errorf("judge facts lack the action or the worker change:\n%s", in.Facts)
	}
}

// The judge decides, not the simulated user; a no and the code checks
// get their fixed labels.
func TestRunSimFailureLabels(t *testing.T) {
	sc := fixScenario()
	sc.Expect.Forbid = []SimAction{{Kind: ActionPrompt, Agent: "bench-codex"}}
	sc.Expect.Actions = append(sc.Expect.Actions, SimAction{Kind: ActionKeys, Agent: "bench-claude"})
	sc.Expect.Agent = "bench-claude"
	run := RunSim(context.Background(), sc, SimOptions{
		BrainConfig:   brain.Config{MaxSteps: 4, UtteranceTimeout: 5 * time.Second},
		NewBrainModel: fixBrain,
		NewJudge:      func(*Meter) Judge { return &FakeJudge{Rules: []FakeRule{{Contains: "by ear", Answer: AnswerNo}}} },
		FixtureDir:    "../../testdata/herdr",
		Settle:        200 * time.Millisecond,
	}, 2)
	got := map[FailureType]int{}
	for _, f := range run.Failures {
		got[f.Type]++
	}
	if run.Status != RunFail || got[FailUnintendedAction] != 1 || got[FailMissingCall] != 1 || got[FailWrongAgent] != 1 || got[FailJudgeNo] != 1 {
		t.Fatalf("status %s failures %+v", run.Status, run.Failures)
	}
	if Primary(run.Failures) != FailUnintendedAction {
		t.Errorf("primary = %s", Primary(run.Failures))
	}
}

// Caps end a conversation and are recorded: turns, silence.
func TestRunSimCaps(t *testing.T) {
	sc := fixScenario()
	sc.Opening = ""
	sc.Script = []string{"a", "b", "c", "d", "e"}
	sc.Caps = SimCaps{MaxTurns: 2, MaxSeconds: 20, SilenceSeconds: 1}
	opts := SimOptions{BrainConfig: brain.Config{UtteranceTimeout: 2 * time.Second}, FixtureDir: "../../testdata/herdr", Settle: 50 * time.Millisecond}
	if run := RunSim(context.Background(), sc, opts, 1); run.EndReason != EndMaxTurns || run.Turns != 2 {
		t.Errorf("max turns: end %s after %d", run.EndReason, run.Turns)
	}
	sc.Script = []string{"<wait>", "<wait>", "<wait>"}
	if run := RunSim(context.Background(), sc, opts, 1); run.EndReason != EndSilence {
		t.Errorf("silence: end %s", run.EndReason)
	}
}

// A simulated user whose model fails errors the run: not a pass, not a
// fail.
func TestRunSimUserErrorIsErrored(t *testing.T) {
	sc := fixScenario()
	sc.Opening = ""
	run := RunSim(context.Background(), sc, SimOptions{
		NewSimUser: func(m *Meter) SimUser {
			return &LLMSimUser{Model: Metered(erroringModel{}, m, "simuser"), ModelID: "x"}
		},
		FixtureDir: "../../testdata/herdr",
	}, 1)
	if run.Status != RunError || run.EndReason != EndError {
		t.Fatalf("status %s end %s", run.Status, run.EndReason)
	}
	if len(run.Usage) != 1 || run.Usage[0].Role != "simuser" {
		t.Errorf("usage = %+v", run.Usage)
	}
}

func TestSimTurnOf(t *testing.T) {
	end := braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC("end_call", map[string]any{"reason": "done", "goal_met": true})}})
	if tt, ok := simTurnOf(end); !ok || tt.Kind != TurnEnd || !tt.GoalMet || tt.Reason != "done" {
		t.Errorf("end_call = %+v", tt)
	}
	wait := braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC("wait_for_sheepdog", map[string]any{"seconds": float64(20)})}})
	if tt, _ := simTurnOf(wait); tt.Kind != TurnWait || tt.WaitSeconds != 20 {
		t.Errorf("wait = %+v", tt)
	}
	say := braintest.Response(braintest.Step{Text: `CEO: "What's bench-codex doing?"`})
	if tt, _ := simTurnOf(say); tt.Kind != TurnSay || tt.Text != "What's bench-codex doing?" {
		t.Errorf("say = %+v", tt)
	}
	if _, ok := simTurnOf(braintest.Response(braintest.Step{})); ok {
		t.Error("empty turn accepted")
	}
}

func TestSimUserContentsAlternate(t *testing.T) {
	cs := simUserContents([]SimLine{{Who: "push", Text: "x blocked"}, {Who: "ceo", Text: "yes"}, {Who: "sheepdog", Text: "done"}, {Who: "push", Text: "y finished"}, {Who: "ceo", Text: "thanks"}})
	roles := []string{}
	for _, c := range cs {
		roles = append(roles, c.Role)
	}
	if strings.Join(roles, ",") != "user,model,user,model,user" {
		t.Fatalf("roles = %v", roles)
	}
}

func TestLoadSimFileValidates(t *testing.T) {
	for _, s := range []SimScenario{
		{ID: "a", Goal: "g"},
		{ID: "a", Goal: "g", Agents: []SimAgent{{Alias: "x"}}, Events: []SimEvent{{AtMS: 1, AfterTurn: 1, Agent: "x"}}},
		{ID: "a", Goal: "g", Agents: []SimAgent{{Alias: "x"}}, Expect: SimExpect{Agent: "y"}},
		{ID: "a", Goal: "g", Agents: []SimAgent{{Alias: "x"}}, Expect: SimExpect{Skip: []string{"nope"}}},
	} {
		if err := s.validate(); err == nil {
			t.Errorf("%+v validated", s)
		}
	}
	if err := fixScenario().validate(); err != nil {
		t.Errorf("fixScenario: %v", err)
	}
}

// Every scenario file loads, every screen resolves, and every scenario
// runs end to end on the real supervisor with the scripted brain and
// user (the offline harness check `make eval` also runs).
func TestSimScenarioFilesOffline(t *testing.T) {
	files, err := filepath.Glob("../../testdata/eval/sim/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no scenario files: %v", err)
	}
	n := 0
	for _, f := range files {
		scs, err := LoadSimFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, sc := range scs {
			n++
			if len(sc.Script) == 0 {
				t.Errorf("%s: no script for the offline run", sc.ID)
			}
			run := RunSim(context.Background(), sc, SimOptions{Offline: true, FixtureDir: "../../testdata/herdr"}, 1)
			if run.Status != RunPass {
				t.Errorf("%s: %s %+v", sc.ID, run.Status, run.Failures)
			}
		}
	}
	if n < 20 {
		t.Errorf("only %d scenarios; the campaign wants at least 20", n)
	}
}

// A misspelled screen name is an error, not a one-line screen.
func TestSimUnknownScreenErrors(t *testing.T) {
	sc := fixScenario()
	sc.Agents[1].Screen = "no-such-screen.txt"
	run := RunSim(context.Background(), sc, SimOptions{Offline: true, FixtureDir: "../../testdata/herdr"}, 1)
	if run.Status != RunError || !strings.Contains(run.EndDetail, "no-such-screen.txt") {
		t.Fatalf("status %s detail %q", run.Status, run.EndDetail)
	}
}

// An interrupt cancels what a prompt set in motion: the stopped worker
// does not finish.
func TestSimInterruptCancelsPromptEvents(t *testing.T) {
	sc := fixScenario()
	sc.Opening = ""
	sc.Script = []string{"tell bench-codex to fix it", "stop", "<wait>"}
	sc.Caps.SilenceSeconds = 5 // the run outlives the finish it cancelled
	sc.Events[0].DelayMS = 3000
	sc.Expect = SimExpect{}
	brainModel := func() brain.Model {
		return braintest.NewModel(
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "bench-codex", "text": "fix the add test"})}},
			braintest.Step{Text: braintest.Reply("Told bench-codex.", "")},
		)
	}
	run := RunSim(context.Background(), sc, SimOptions{
		BrainConfig: brain.Config{MaxSteps: 4, UtteranceTimeout: 5 * time.Second}, NewBrainModel: brainModel,
		FixtureDir: "../../testdata/herdr", Settle: 200 * time.Millisecond,
	}, 1)
	interrupted := false
	for _, a := range run.Actions {
		interrupted = interrupted || a.Kind == ActionInterrupt
	}
	if !interrupted {
		t.Fatalf("no interrupt: %+v\n%s", run.Actions, renderSim(run.Transcript))
	}
	for _, l := range run.Timeline {
		if strings.Contains(l.Text, "is now done") {
			t.Fatalf("the interrupted worker finished anyway: %+v", run.Timeline)
		}
	}
}

// Keys no scenario event handles still move a blocked worker on: a
// declined prompt leaves the worker idle at its input line.
func TestSimDefaultReactionToKeys(t *testing.T) {
	sc := SimScenario{
		ID: "decline", Goal: "decline", Script: []string{"no"},
		Agents: []SimAgent{{Alias: "auth", State: "blocked", Screen: "codex-approval-prompt.screen.txt"}},
		Caps:   SimCaps{MaxTurns: 2, MaxSeconds: 20, SilenceSeconds: 1},
	}
	brainModel := func() brain.Model {
		m := braintest.NewModel(
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"esc"}})}},
			braintest.Step{Text: braintest.Reply("Declined.", "")},
		)
		m.EventDefault = braintest.NothingToAdd()
		return m
	}
	fj := &FakeJudge{}
	run := RunSim(context.Background(), sc, SimOptions{BrainConfig: brain.Config{MaxSteps: 4, UtteranceTimeout: 5 * time.Second}, NewBrainModel: brainModel, NewJudge: func(*Meter) Judge { return fj }, FixtureDir: "../../testdata/herdr", Settle: 1500 * time.Millisecond}, 1)
	if len(run.Actions) != 1 || run.Actions[0].Detail != "esc" {
		t.Fatalf("actions = %+v\n%s", run.Actions, renderSim(run.Transcript))
	}
	facts := fj.Inputs()[len(fj.Inputs())-1].Facts
	if !strings.Contains(facts, "Worker auth (codex), now idle.") || !strings.Contains(facts, "Okay, I won't do that.") {
		t.Fatalf("the declined worker did not move on:\n%s", facts)
	}
}

// A 504 on the primary provider fails the step over to the registered
// fallback: the run still passes, and the record counts the failovers
// and names the provider that answered.
func TestRunSimRecordsFailover(t *testing.T) {
	primary := braintest.NewModel()
	primary.Default = &braintest.Step{Err: genai.APIError{Code: 504, Message: "Deadline expired before operation could complete.", Status: "DEADLINE_EXCEEDED"}}
	run := RunSim(context.Background(), fixScenario(), SimOptions{
		BrainConfig:   brain.Config{Provider: brain.ProviderVertex, Model: "gemini-3.8-flash", MaxSteps: 4, UtteranceTimeout: 5 * time.Second},
		NewBrainModel: func() brain.Model { return primary },
		Fallbacks:     map[string]brain.Model{brain.ProviderDeepSeek: fixBrain()},
		NewJudge:      func(*Meter) Judge { return &FakeJudge{} },
		FixtureDir:    "../../testdata/herdr",
		Settle:        1500 * time.Millisecond,
	}, 1)
	if run.Status != RunPass {
		t.Fatalf("status %s, failures %+v\n%s", run.Status, run.Failures, renderSim(run.Transcript))
	}
	if run.Failovers < 1 || len(run.FailoverEvents) != run.Failovers || !strings.Contains(run.FailoverEvents[0], "vertex to deepseek (http_504)") {
		t.Errorf("failovers = %d %v", run.Failovers, run.FailoverEvents)
	}
	if run.AnsweredBy != brain.ProviderDeepSeek || run.StepsByProvider[brain.ProviderDeepSeek] == 0 || run.StepsByProvider[brain.ProviderVertex] != 0 {
		t.Errorf("answered by %q, steps %v", run.AnsweredBy, run.StepsByProvider)
	}

	rep := NewCampaignReport(time.Now(), nil, 1, []SimRun{run}, NewMeter())
	var md strings.Builder
	rep.WriteMarkdown(&md, 5)
	if !strings.Contains(md.String(), "## Brain failover") || !strings.Contains(md.String(), "deepseek ×1") {
		t.Errorf("report.md lacks the failover table:\n%s", md.String())
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := rep.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"failovers": `) || !strings.Contains(string(raw), `"answered_by": "deepseek"`) {
		t.Errorf("report.json lacks the failover fields")
	}
}

// Without a failing primary there is no failover, and the primary is the
// one that answered.
func TestRunSimNoFailoverNamesPrimary(t *testing.T) {
	run := RunSim(context.Background(), fixScenario(), SimOptions{
		BrainConfig:   brain.Config{Provider: brain.ProviderVertex, Model: "gemini-3.8-flash", MaxSteps: 4, UtteranceTimeout: 5 * time.Second},
		NewBrainModel: fixBrain,
		Fallbacks:     map[string]brain.Model{brain.ProviderDeepSeek: braintest.NewModel()},
		NewJudge:      func(*Meter) Judge { return &FakeJudge{} },
		FixtureDir:    "../../testdata/herdr",
		Settle:        1500 * time.Millisecond,
	}, 1)
	if run.Failovers != 0 || run.AnsweredBy != brain.ProviderVertex {
		t.Errorf("failovers %d, answered by %q", run.Failovers, run.AnsweredBy)
	}
}
