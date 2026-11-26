package eval

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Pins app-factory.md §6.5: offline factory sim scenarios distinguish
// investigate (judgement/why questions with no_actions) from prompt/Say
// (design decisions) on running and stopped projects.
func TestSimFactoryInvestigateVsPromptScenariosOffline(t *testing.T) {
	scs, err := LoadSimFile("../../testdata/eval/sim/factory.json")
	if err != nil {
		t.Fatalf("LoadSimFile(factory.json): %v", err)
	}
	byID := map[string]SimScenario{}
	for _, sc := range scs {
		byID[sc.ID] = sc
	}
	runWith := func(id string, scale float64, eventSpoken string, steps ...braintest.Step) SimRun {
		m := braintest.NewModel(steps...)
		m.EventDefault = braintest.NothingToAdd()
		if eventSpoken != "" {
			m.EventDefault = &braintest.Step{Text: braintest.Reply(eventSpoken, "")}
		}
		return RunSim(context.Background(), byID[id], SimOptions{
			BrainConfig:   brain.Config{MaxSteps: 3, UtteranceTimeout: 2 * time.Second},
			NewBrainModel: func() brain.Model { return m }, FixtureDir: "../../testdata/herdr",
			TimeScale: scale, Settle: 100 * time.Millisecond,
		}, 1)
	}
	const finding, stoppedFinding = "From the project's record, a trigram index cuts memory in half.", "From the project's record, T6 was split to stay under 400 lines."
	if r := runWith("factory-investigate-judgement-no-say", 1, finding,
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "Would a different tag index design be better?"})}},
		braintest.Step{Text: braintest.Reply("Looking into notes.", "")}); r.Status != RunPass || len(r.Actions) != 0 || !slices.Contains(r.ToolCalls, brain.ToolInvestigate) || !strings.Contains(renderSim(r.Transcript), finding) {
		t.Errorf("investigate judgement = %s actions=%+v tools=%v\n%s", r.Status, r.Actions, r.ToolCalls, renderSim(r.Transcript))
	}
	if bad := runWith("factory-investigate-judgement-no-say", 0.02, "",
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "use a different design"})}},
		braintest.Step{Text: braintest.Reply("Told notes.", "")}); !slices.ContainsFunc(bad.Failures, func(f Failure) bool { return f.Type == FailUnintendedAction }) {
		t.Errorf("prompt on judgement should fail FailUnintendedAction: %+v", bad.Failures)
	}
	if rs := runWith("factory-investigate-stopped-no-say", 1, stoppedFinding,
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "Why was T6 split?"})}},
		braintest.Step{Text: braintest.Reply("Looking into notes.", "")}); rs.Status != RunPass || len(rs.Actions) != 0 || !strings.Contains(renderSim(rs.Transcript), stoppedFinding) {
		t.Errorf("investigate stopped = %s actions=%+v\n%s", rs.Status, rs.Actions, renderSim(rs.Transcript))
	}
	if ok := runWith("factory-prompt-design-decision-not-investigate", 0.02, "",
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "use a different design for the tag index"})}},
		braintest.Step{Text: braintest.Reply("Told the notes factory.", "")}); ok.Status != RunPass {
		t.Errorf("prompt design decision = %s failures=%+v", ok.Status, ok.Failures)
	}
	if wrong := runWith("factory-prompt-design-decision-not-investigate", 0.02, "",
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "use a different design"})}},
		braintest.Step{Text: braintest.Reply("Looking into notes.", "")}); !slices.ContainsFunc(wrong.Failures, func(f Failure) bool { return f.Type == FailMissingCall }) {
		t.Errorf("investigate on decision should fail FailMissingCall: %+v", wrong.Failures)
	}
}
