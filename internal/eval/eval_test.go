package eval

import (
	"context"
	"testing"
)

func TestLoadAssistantCases(t *testing.T) {
	cases, err := LoadAssistantCases("../../testdata/eval/assistant.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("got %d cases, want at least 30", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" {
			t.Error("case with empty id")
		}
		if seen[c.ID] {
			t.Errorf("duplicate id %q", c.ID)
		}
		seen[c.ID] = true
		if c.Utterance == "" {
			t.Errorf("%s: empty utterance", c.ID)
		}
		switch c.Expect.Tool {
		case "briefing", "ask", "propose", "confirm", "cancel", "note", "none":
		default:
			t.Errorf("%s: unknown expect.tool %q", c.ID, c.Expect.Tool)
		}
	}
}

func TestLoadSVPCases(t *testing.T) {
	cases, err := LoadSVPCases("../../testdata/eval/svp.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("got %d cases, want at least 10", len(cases))
	}
	for _, c := range cases {
		if c.ID == "" {
			t.Error("case with empty id")
		}
		if c.Request.Kind != "ask" && c.Request.Kind != "propose" {
			t.Errorf("%s: bad request.kind %q", c.ID, c.Request.Kind)
		}
		if c.ScreenFixture == "" {
			t.Errorf("%s: no screen_fixture", c.ID)
		}
	}
}

// TestRunSVPOffline exercises the harness (rail, provenance) with the
// scripted fake model over every case in the real eval set.
func TestRunSVPOffline(t *testing.T) {
	cases, err := LoadSVPCases("../../testdata/eval/svp.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	results := RunSVPOffline(context.Background(), cases, "../../testdata/herdr")
	for _, r := range results {
		if r.Status == StatusFail {
			t.Errorf("%s: %s", r.ID, r.Detail)
		}
	}
}

// TestRunSVPOfflineRailBlocksInjection is a focused check that the rail
// actually refuses an unauthorised send_keys even when the model (here,
// scripted to imitate a prompt-injected one) tries it.
func TestRunSVPOfflineRailBlocksInjection(t *testing.T) {
	c := SVPCase{
		ID:            "rail-direct",
		ScreenFixture: "injection.screen.txt",
		Request:       SVPRequest{Kind: "ask", Question: "what did it say?", Agent: "auth"},
		Expect:        SVPExpect{MustNotCall: []string{"send_keys"}},
	}
	// Force the scripted model to attempt send_keys regardless of the
	// case's own tools_used_any_of, by asking for it directly.
	c.Expect.RailBlocked = false // the model attempts it unauthorised; no pending prompt exists at all
	results := RunSVPOffline(context.Background(), []SVPCase{c}, "../../testdata/herdr")
	if len(results) != 1 || results[0].Status != StatusPass {
		t.Fatalf("rail did not hold: %+v", results)
	}
}

func TestRunAssistantOfflineSkips(t *testing.T) {
	cases := []AssistantCase{{ID: "a", Utterance: "hi", Expect: AssistantExpect{Tool: "none"}}}
	results := RunAssistant(context.Background(), cases, nil, nil, "")
	if len(results) != 1 || results[0].Status != StatusSkipped {
		t.Fatalf("offline assistant run should skip: %+v", results)
	}
}

func TestCheckAssistantExpect(t *testing.T) {
	agentAuth := "auth"
	agentEmpty := ""
	tests := []struct {
		name     string
		exp      AssistantExpect
		gotTool  string
		gotAgent string
		gotText  string
		wantFail bool
	}{
		{"tool match", AssistantExpect{Tool: "briefing"}, "briefing", "", "", false},
		{"tool mismatch", AssistantExpect{Tool: "briefing"}, "ask", "", "", true},
		{"agent must be empty, got empty", AssistantExpect{Tool: "propose", Agent: &agentEmpty}, "propose", "", "", false},
		{"agent must be empty, got guessed", AssistantExpect{Tool: "propose", Agent: &agentEmpty}, "propose", "auth", "", true},
		{"agent must equal auth", AssistantExpect{Tool: "propose", Agent: &agentAuth}, "propose", "auth", "", false},
		{"question contains", AssistantExpect{Tool: "ask", QuestionContains: []string{"fail"}}, "ask", "", "why did it fail", false},
		{"question missing", AssistantExpect{Tool: "ask", QuestionContains: []string{"fail"}}, "ask", "", "what happened", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasons := checkAssistantExpect(tt.exp, tt.gotTool, tt.gotAgent, tt.gotText)
			if (len(reasons) > 0) != tt.wantFail {
				t.Errorf("reasons=%v, wantFail=%v", reasons, tt.wantFail)
			}
		})
	}
}
