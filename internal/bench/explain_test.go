package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// The call is split at the CEO's lines: the orchestrator's results and
// pushes and the voice's speech go to the question they followed; a
// question with no to_supervisor result was not consulted.
func TestExplainExchanges(t *testing.T) {
	spec := &eval.ExplainSpec{Turns: []string{"explain", "more"}, KeyPoints: []string{"p"}}
	lines := []UserLine{{AtMS: 1000, Said: "讲讲"}, {AtMS: 9000, Said: "BARGE", BargeIn: true}, {AtMS: 20000, Said: "详细一点"}}
	rec := voiceclient.Recording{
		Events: []voiceclient.Event{
			{AtMS: 500, Type: "voice.push", Payload: map[string]any{"text": "connect announcement"}},
			{AtMS: 4000, Type: "voice.tool_result", Payload: map[string]any{"name": "to_supervisor", "spoken": "Four sentences.", "detail": "More detail.", "options": []any{"重构", "先不管"}}},
			{AtMS: 6000, Type: "voice.push", Payload: map[string]any{"text": "A push.", "urgency": "when_idle"}},
			{AtMS: 7000, Type: "voice.push", Payload: map[string]any{"text": "Current focus, for follow-ups (context only).", "focus": "True", "urgency": "silent"}},
		},
		Bot: []voiceclient.BotUtterance{
			{StartMS: 5000, Heard: "Two sentences."},
			// Runs on past the next question; flushed after "Rephr".
			{StartMS: 15000, Text: "Late. Rephrased.", Heard: "Late. Rephr", Fragments: []voiceclient.Fragment{{AtMS: 15000, Text: "Late."}, {AtMS: 21000, Text: " Rephrased."}}},
		},
	}
	ex := explainExchanges(spec, lines, rec)
	if len(ex) != 2 {
		t.Fatalf("%d exchanges, want 2 (the barge-in is not a question)", len(ex))
	}
	if !ex[0].Consulted || ex[0].Orchestrator != "Four sentences. More detail. Options: 重构; 先不管. A push." || ex[0].Voice != "Two sentences. Late." || ex[0].Kind != "explain" {
		t.Fatalf("exchange 1 = %+v", ex[0])
	}
	if ex[1].Consulted || ex[1].Orchestrator != "" || ex[1].Voice != "Rephr" || ex[1].Kind != "more" || ex[1].Turn != 2 {
		t.Fatalf("exchange 2 = %+v", ex[1])
	}
}

// --prompts-dir reaches the scratch daemon's config, and the Loader over
// it takes the slow brain's svp/ files as well as the voice prompt, so an
// A/B arm can swap both brains' prompts.
func TestPromptsDirCoversSVP(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "svp"), 0o755); err != nil {
		t.Fatal(err)
	}
	soul := "You are the orchestrator (arm B)."
	if err := os.WriteFile(filepath.Join(dir, "svp", "SOUL.md"), []byte(soul), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, prompts.Assistant), []byte("You are the voice (arm B)."), 0o644); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Prompts.Dir = dir
	sc := ScratchConfig(DaemonOptions{Config: cfg, Port: 7821, Dir: t.TempDir()})
	if sc.Prompts.Dir != dir {
		t.Fatalf("scratch prompts.dir = %q, want %q", sc.Prompts.Dir, dir)
	}
	l := prompts.NewLoader(sc.Prompts.Dir, nil)
	if text, src := l.Load(prompts.SVPSoul); src != prompts.SourceOverride || text != soul {
		t.Fatalf("svp/SOUL.md = (%q, %s), want the override", text, src)
	}
	line, err := PromptsArm(dir)
	if err != nil || !strings.Contains(line, "svp/SOUL.md override") || !strings.Contains(line, "assistant.md override") || !strings.Contains(line, "svp/RULES.md embedded") {
		t.Fatalf("PromptsArm = %q, %v", line, err)
	}
	// A RULES.md override without the protected sentences is rejected by
	// the Loader; the arm check refuses it instead of running embedded.
	if err := os.WriteFile(filepath.Join(dir, "svp", "RULES.md"), []byte("no rail here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PromptsArm(dir); err == nil || !strings.Contains(err.Error(), "svp/RULES.md rejected") {
		t.Fatalf("PromptsArm with a bad RULES.md: err = %v", err)
	}
	if _, err := PromptsArm(t.TempDir()); err == nil {
		t.Fatal("an empty prompts dir should be refused")
	}
}

// A rescore replaces the run's explanation failures and keeps the rest.
func TestRescoreExplainReplacesItsFailures(t *testing.T) {
	sc := Scenario{SimScenario: eval.SimScenario{ID: "x", Explain: &eval.ExplainSpec{Turns: []string{"simple"}}}}
	r := &Run{Scenario: "x", Status: eval.RunFail, EndReason: eval.EndCall,
		User: []UserLine{{AtMS: 0, Said: "Is it done?"}},
		Bot:  []voiceclient.BotUtterance{{StartMS: 100, Heard: "Yes."}},
		Failures: []eval.Failure{
			{Type: eval.FailJudgeNo, Detail: "explain_padding_t1 (voice): old"},
			{Type: eval.FailJudgeNo, Detail: "answered: kept"},
		}}
	RescoreExplain(t.Context(), &eval.FakeJudge{}, sc, r, nil)
	if len(r.Failures) != 1 || r.Failures[0].Detail != "answered: kept" || r.Explain == nil || r.Explain.Voice.PaddingPassed != 1 {
		t.Fatalf("failures %+v explain %+v", r.Failures, r.Explain)
	}
}
