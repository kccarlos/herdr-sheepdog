package eval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// erroringModel always fails (not a rate limit), forcing every turn to
// fall back (internal/brain's ErrFallback wraps two errors with a double
// %w, so errors.Unwrap on it is nil — regression test for the panic that
// caused in runSVPCase's fallback branch).
type erroringModel struct{}

func (erroringModel) Generate(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return nil, errors.New("boom: simulated model failure")
}

// rateLimitedModel always returns brain.ErrAllKeysRateLimited, as
// brain.GenaiModel does once every key in the pool has been tried.
type rateLimitedModel struct{}

func (rateLimitedModel) Generate(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return nil, fmt.Errorf("%w: simulated 429", brain.ErrAllKeysRateLimited)
}

// TestRunSVPLiveStopsOnRateLimit is a regression test: brain.fail wraps a
// rate limit inside ErrFallback, which runSVPCase otherwise tolerates as
// a normal (non-fatal) outcome — RunSVPLive must still see through that
// wrapping and stop rather than hot-retry the remaining cases against an
// exhausted key pool.
func TestRunSVPLiveStopsOnRateLimit(t *testing.T) {
	cases := []SVPCase{
		{ID: "a", ScreenFixture: "codex-after-approval.screen.txt", Request: SVPRequest{Kind: "ask", Question: "what did it say?", Agent: "auth"}},
		{ID: "b", ScreenFixture: "codex-after-approval.screen.txt", Request: SVPRequest{Kind: "ask", Question: "what did it say?", Agent: "auth"}},
		{ID: "c", ScreenFixture: "codex-after-approval.screen.txt", Request: SVPRequest{Kind: "ask", Question: "what did it say?", Agent: "auth"}},
	}
	results := RunSVPLive(context.Background(), cases, "../../testdata/herdr", rateLimitedModel{}, "gemini-3.5-flash")
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Status != StatusFail {
		t.Errorf("case a: got %+v, want a FAIL reporting the rate limit", results[0])
	}
	if results[1].Status != StatusSkipped || results[2].Status != StatusSkipped {
		t.Errorf("cases after a rate limit should be skipped, got %+v %+v", results[1], results[2])
	}
}

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
		// The voice has two tools and decides nothing, so every case
		// expects a relay, a stop, or no call at all.
		switch c.Expect.Tool {
		case voice.ToolToSupervisor, voice.ToolStop, "none":
		default:
			t.Errorf("%s: unknown expect.tool %q", c.ID, c.Expect.Tool)
		}
		if c.Expect.Verbatim && c.Expect.Tool != voice.ToolToSupervisor {
			t.Errorf("%s: verbatim only applies to %s", c.ID, voice.ToolToSupervisor)
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
		switch brain.Kind(c.Request.Kind) {
		case brain.KindUtterance, brain.KindAsk, brain.KindPropose:
		default:
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

// TestRunSVPLiveFallbackDoesNotPanic is a regression test: a model error
// (not a rate limit) makes the turn fall back with a double-%w error;
// runSVPCase must not panic building the Got["fallback"] detail.
func TestRunSVPLiveFallbackDoesNotPanic(t *testing.T) {
	c := SVPCase{
		ID:            "fallback-case",
		ScreenFixture: "codex-after-approval.screen.txt",
		Request:       SVPRequest{Kind: "ask", Question: "what did it say?", Agent: "auth"},
		Expect:        SVPExpect{},
	}
	results := RunSVPLive(context.Background(), []SVPCase{c}, "../../testdata/herdr", erroringModel{}, "gemini-3.5-flash")
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	// The point of the test is that this call did not panic; a clean
	// fallback with no Expect assertions violated is a PASS by design
	// (see checkSVPExpect), so just check the fallback detail landed.
	fb, _ := results[0].Got["fallback"].(string)
	if fb == "" {
		t.Errorf("expected Got[\"fallback\"] to be set, got %+v", results[0])
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
	relay := AssistantExpect{Tool: "to_supervisor", Verbatim: true}
	tests := []struct {
		name      string
		exp       AssistantExpect
		gotTool   string
		gotAgent  string
		gotText   string
		utterance string
		wantFail  bool
	}{
		{"tool match", AssistantExpect{Tool: "stop"}, "stop", "", "", "stop", false},
		{"tool mismatch", AssistantExpect{Tool: "stop"}, "to_supervisor", "", "stop", "stop", true},
		{"relayed verbatim", relay, "to_supervisor", "", "Why did it fail?", "Why did it fail?", false},
		{"relayed with whitespace", relay, "to_supervisor", "", " Why did it fail? ", "Why did it fail?", false},
		{"paraphrased", relay, "to_supervisor", "", "why auth failed", "Why did it fail?", true},
		{"agent must be empty, got empty", AssistantExpect{Tool: "to_supervisor", Agent: &agentEmpty}, "to_supervisor", "", "", "", false},
		{"agent must be empty, got guessed", AssistantExpect{Tool: "to_supervisor", Agent: &agentEmpty}, "to_supervisor", "auth", "", "", true},
		{"agent must equal auth", AssistantExpect{Tool: "to_supervisor", Agent: &agentAuth}, "to_supervisor", "auth", "", "", false},
		{"text contains", AssistantExpect{Tool: "to_supervisor", QuestionContains: []string{"fail"}}, "to_supervisor", "", "why did it fail", "", false},
		{"text missing", AssistantExpect{Tool: "to_supervisor", QuestionContains: []string{"fail"}}, "to_supervisor", "", "what happened", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reasons := checkAssistantExpect(tt.exp, tt.gotTool, tt.gotAgent, tt.gotText, tt.utterance)
			if (len(reasons) > 0) != tt.wantFail {
				t.Errorf("reasons=%v, wantFail=%v", reasons, tt.wantFail)
			}
		})
	}
}
