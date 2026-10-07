package bench

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// TestOrchestratorTranscript: the user's lines and the orchestrator's
// answers and spoken pushes in time order, silent context left out.
func TestOrchestratorTranscript(t *testing.T) {
	r := Run{Transcript: []eval.SimLine{{AtMS: 100, Who: "ceo", Text: "status?"}, {AtMS: 900, Who: "sheepdog", Text: "voice words"}}}
	evs := []voiceclient.Event{
		{AtMS: 50, Type: "voice.push", Payload: map[string]any{"text": "focus context", "scheduling_sent": "silent"}},
		{AtMS: 800, Type: "voice.tool_result", Payload: map[string]any{"spoken": "Two are busy.", "detail": "tests-codex runs 412 tests.", "options": []any{"Approve", "Wait"}}},
		{AtMS: 1200, Type: "voice.push", Payload: map[string]any{"text": "infra-codex finished.", "scheduling_sent": "when_idle"}},
	}
	tr := eval.RenderTranscript(OrchestratorTranscript(r, evs))
	for _, want := range []string{"CEO: status?", "Two are busy.", "412 tests", "Approve", "(unprompted): infra-codex finished."} {
		if !strings.Contains(tr, want) {
			t.Errorf("missing %q in\n%s", want, tr)
		}
	}
	if strings.Contains(tr, "voice words") || strings.Contains(tr, "focus context") {
		t.Errorf("voice speech or silent context in the orchestrator transcript:\n%s", tr)
	}
	if strings.Index(tr, "status?") > strings.Index(tr, "Two are busy.") {
		t.Error("not in time order")
	}
}

// TestRescoreOrchestrator asks only the content items.
func TestRescoreOrchestrator(t *testing.T) {
	sc := Scenario{SimScenario: eval.SimScenario{ID: "w", Goal: "g", Agents: []eval.SimAgent{{Alias: "a"}}, Agenda: []string{"hi"},
		Workday: &eval.Workday{SimMinutes: 20, Truth: eval.WorkdayTruth{News: []eval.WorkdayNews{{ID: "n", Agent: "a", What: "a done"}}, Safety: []string{"x"}}}}}
	r := Run{Scenario: "w", Status: eval.RunPass}
	if err := RescoreOrchestrator(context.Background(), &eval.FakeJudge{}, sc, &r, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range r.Orchestrator {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "answered,news_n" {
		t.Fatalf("orchestrator items %v", names)
	}
}
