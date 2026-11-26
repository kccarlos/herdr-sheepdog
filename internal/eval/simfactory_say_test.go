package eval

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Pins app-factory.md §6.1 & §6.3: offline factory sim scenarios validate
// project names in expect.actions/forbid/agent, route prompt on a project row
// vs an ordinary worker, and enforce no_actions on the open-ask safety case.
func TestSimFactorySayScenariosOffline(t *testing.T) {
	scs, err := LoadSimFile("../../testdata/eval/sim/factory.json")
	if err != nil {
		t.Fatalf("LoadSimFile(factory.json): %v", err)
	}
	byID := map[string]SimScenario{}
	for _, sc := range scs {
		byID[sc.ID] = sc
	}
	runWith := func(id string, steps ...braintest.Step) SimRun {
		m := braintest.NewModel(steps...)
		m.EventDefault = braintest.NothingToAdd()
		return RunSim(context.Background(), byID[id], SimOptions{
			BrainConfig:   brain.Config{MaxSteps: 3, UtteranceTimeout: 2 * time.Second},
			NewBrainModel: func() brain.Model { return m }, FixtureDir: "../../testdata/herdr",
			TimeScale: 0.02, Settle: 50 * time.Millisecond,
		}, 1)
	}
	if r := runWith("factory-prompt-project-vs-worker", braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "skip the export task"})}}, braintest.Step{Text: braintest.Reply("Told the notes factory.", "")}); r.Status != RunPass {
		t.Errorf("project-vs-worker = %s failures=%+v actions=%+v", r.Status, r.Failures, r.Actions)
	}
	if r := runWith("factory-prompt-worker-vs-project", braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth-claude", "text": "run the unit tests"})}}, braintest.Step{Text: braintest.Reply("Telling auth-claude.", "")}); r.Status != RunPass {
		t.Errorf("worker-vs-project = %s failures=%+v actions=%+v", r.Status, r.Failures, r.Actions)
	}
	if r := runWith("factory-open-ask-free-words-no-say", braintest.Step{Text: braintest.Reply("Use the release card or say Yes, tag 0.2.0.", "")}); r.Status != RunPass || len(r.Actions) != 0 {
		t.Errorf("open-ask safe reply = %s failures=%+v actions=%+v", r.Status, r.Failures, r.Actions)
	}
	if r := runWith("factory-open-ask-free-words-no-say", braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "Yes, tag 0.2.0"})}}, braintest.Step{Text: braintest.Reply("Told notes.", "")}); !slices.ContainsFunc(r.Failures, func(f Failure) bool { return f.Type == FailUnintendedAction }) {
		t.Errorf("open-ask leaky prompt should fail FailUnintendedAction: status=%s failures=%+v actions=%+v", r.Status, r.Failures, r.Actions)
	}
}
