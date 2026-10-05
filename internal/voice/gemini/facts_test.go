package gemini

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// agentNowStub names "auth-1" as "auth Claude", idle after work.
func agentNowStub(alias string) (string, string, bool) {
	if alias != "auth-1" {
		return "", "", false
	}
	return "auth Claude", "as this reached you: idle, ready for input, for 4 min; it last finished work 4 min ago", true
}

// TestBriefingCarriesAgeAndAgentState: a briefing says how old its
// facts were when it reached the model, names the agent by its spoken
// name (not the alias) and says the agent's state at delivery.
func TestBriefingCarriesAgeAndAgentState(t *testing.T) {
	c := newFakeConn("c1")
	cfg := briefingConfig()
	cfg.AgentNow = agentNowStub
	h := newHarness(t, cfg, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("auth finished; all 42 tests pass.", voice.UrgencyIdle, map[string]any{"agent": "auth-1"}); err != nil {
		t.Fatal(err)
	}
	r := c.waitSent(t, "briefing", isBriefing("all 42 tests pass")).tool.FunctionResponses[0].Response
	if r["agent"] != "auth Claude" {
		t.Errorf("agent = %v, want the spoken name", r["agent"])
	}
	if st, _ := r["agent_state"].(string); !strings.Contains(st, "it last finished work 4 min ago") {
		t.Errorf("agent_state = %v", r["agent_state"])
	}
	if age, _ := r["age"].(string); !strings.HasPrefix(age, "fresh") {
		t.Errorf("age = %v", r["age"])
	}
}

// TestAgeLine: a push that waited in the queue says how long.
func TestAgeLine(t *testing.T) {
	if got := ageLine(time.Second); !strings.HasPrefix(got, "fresh") {
		t.Errorf("1 s: %q", got)
	}
	if got := ageLine(95 * time.Second); !strings.Contains(got, "written 1 min before it reached you") {
		t.Errorf("95 s: %q", got)
	}
	if got := ageLine(42 * time.Second); !strings.Contains(got, "42 s") {
		t.Errorf("42 s: %q", got)
	}
}

// TestAnswerSaysNotVerified: a slow-brain check that timed out reaches
// the fast brain as its own fact, beside the facts (which are not
// edited), with the state of the agents the answer is about.
func TestAnswerSaysNotVerified(t *testing.T) {
	c := newFakeConn("c1")
	cfg := briefingConfig()
	cfg.AgentNow = agentNowStub
	cfg.Handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "auth has uncommitted changes.", Scheduling: voice.UrgencyIdle,
			NotVerified: []string{"run_cli about auth-1 timed out after 8 s"}, Agents: []string{"auth-1"}}, nil
	}
	newHarness(t, cfg, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	c.serve(toolCallMsg("call-1", voice.ToolToSupervisor, map[string]any{"text": "did auth commit? check git status"}))
	c.serve(turnCompleteMsg())
	r := c.waitSent(t, "answer", func(m sent) bool { return m.kind == "tool" && m.tool.FunctionResponses[0].ID == "call-1" }).tool.FunctionResponses[0].Response
	nv, _ := r["not_verified"].(string)
	if !strings.Contains(nv, "nothing they would have shown is verified") || !strings.Contains(nv, "run_cli about auth-1 timed out after 8 s") {
		t.Errorf("not_verified = %v", r["not_verified"])
	}
	if r["facts"] != "auth has uncommitted changes." {
		t.Errorf("facts edited: %v", r["facts"])
	}
	if st, _ := r["agents_now"].([]string); len(st) != 1 || !strings.HasPrefix(st[0], "auth Claude: as this reached you") {
		t.Errorf("agents_now = %v", r["agents_now"])
	}
	if r["age"] == nil {
		t.Errorf("answer has no age: %v", r)
	}
}

// TestUnvoicedBriefingIsReported: a spoken briefing the model never
// spoke after, before the CEO next talked, is logged, told to the fast
// brain silently, carried in its next answer, and reported to the
// supervisor as not heard. One the model spoke after is not.
func TestUnvoicedBriefingIsReported(t *testing.T) {
	c := newFakeConn("c1")
	cfg := briefingConfig()
	cfg.Handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "Claude is idle.", Scheduling: voice.UrgencyIdle}, nil
	}
	h := newHarness(t, cfg, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("Claude finished.", voice.UrgencyIdle, map[string]any{"agent": "claude"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "briefing", isBriefing("Claude finished"))
	// The model completes a turn without a word, then the CEO talks.
	c.serve(turnCompleteMsg())
	c.serve(inputTranscriptMsg("现在有结论了吗？", true))
	h.waitEvent("unheard", func(ev voice.Event) bool {
		e, ok := ev.(voice.EventPushUnheard)
		return ok && e.Push.Text == "Claude finished." && e.Push.Reason == voice.UnheardNotVoiced
	})
	if p := h.log.payloads("voice.briefing_unvoiced"); len(p) != 1 || p[0]["briefing"] != "b-1" {
		t.Fatalf("voice.briefing_unvoiced = %v", p)
	}
	c.waitSent(t, "silent fact", isBriefing("b-1 (news"))
	c.serve(toolCallMsg("call-1", voice.ToolToSupervisor, map[string]any{"text": "现在有结论了吗？"}))
	c.serve(turnCompleteMsg())
	r := c.waitSent(t, "answer", func(m sent) bool { return m.kind == "tool" && m.tool.FunctionResponses[0].ID == "call-1" }).tool.FunctionResponses[0].Response
	rel, _ := r["relation"].([]string)
	if len(rel) == 0 || !strings.Contains(rel[0], "the user has not heard it") {
		t.Errorf("answer relation = %v", r["relation"])
	}

	// A briefing the model spoke after is voiced.
	c.serve(outputTranscriptMsg("Claude is idle."))
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 1000)
	time.Sleep(150 * time.Millisecond)
	if err := h.s.PushTagged("codex finished.", voice.UrgencyIdle, map[string]any{"agent": "codex"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "briefing 2", isBriefing("codex finished"))
	c.serve(outputTranscriptMsg("Codex is done."))
	c.serve(turnCompleteMsg())
	c.serve(inputTranscriptMsg("ok", true))
	time.Sleep(150 * time.Millisecond)
	if p := h.log.payloads("voice.briefing_unvoiced"); len(p) != 1 {
		t.Errorf("a voiced briefing was reported unvoiced: %v", p)
	}
}

// TestAnnouncementCarriesDecisionOpen: a spoken announcement (a push
// tagged with its worker event) says whether a decision is open as it
// reaches the model; a silent line and a push of no event do not.
func TestAnnouncementCarriesDecisionOpen(t *testing.T) {
	c := newFakeConn("c1")
	cfg := briefingConfig()
	cfg.DecisionsNow = func() string { return "none: as this reached you, no agent is waiting for the user's answer" }
	h := newHarness(t, cfg, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("bench-claude added the changelog entry.", voice.UrgencyIdle, map[string]any{"agent": "bench-claude", "event_id": "evt-1"}); err != nil {
		t.Fatal(err)
	}
	r := c.waitSent(t, "announcement", isBriefing("changelog entry")).tool.FunctionResponses[0].Response
	if d, _ := r["decision_open"].(string); !strings.HasPrefix(d, "none:") {
		t.Errorf("decision_open = %v", r["decision_open"])
	}
	c.serve(outputTranscriptMsg("bench-claude added the changelog entry."))
	c.serve(turnCompleteMsg())
	if err := h.s.PushTagged("names line", voice.UrgencySilent, map[string]any{"event_id": "evt-2"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PushTagged("a reminder", voice.UrgencyIdle, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"names line", "a reminder"} {
		r := c.waitSent(t, want, isBriefing(want)).tool.FunctionResponses[0].Response
		if _, has := r["decision_open"]; has {
			t.Errorf("%s: decision_open = %v, want none", want, r["decision_open"])
		}
	}
}
