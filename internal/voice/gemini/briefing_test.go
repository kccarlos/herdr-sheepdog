package gemini

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// isBriefing matches a function response named "briefing" whose facts
// contain text.
func isBriefing(text string) func(sent) bool {
	return func(m sent) bool {
		if m.kind != "tool" || len(m.tool.FunctionResponses) == 0 {
			return false
		}
		fr := m.tool.FunctionResponses[0]
		facts, _ := fr.Response["facts"].(string)
		return fr.Name == BriefingName && strings.Contains(facts, text)
	}
}

func briefingConfig() Config { return Config{BriefingChannel: ChannelFunctionResponse} }

// TestBriefingIsAFunctionResponse: a spoken push reaches the model as a
// function response to a call it never made, labelled with who it is
// from, the time and what it is — never as a user-role turn.
func TestBriefingIsAFunctionResponse(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, briefingConfig(), &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("auth finished; all 42 tests pass.", voice.UrgencyIdle, map[string]any{"agent": "auth", "event_id": "evt-1"}); err != nil {
		t.Fatal(err)
	}
	m := c.waitSent(t, "briefing", isBriefing("all 42 tests pass"))
	fr := m.tool.FunctionResponses[0]
	if fr.ID != "b-1" || fr.Scheduling != genai.FunctionResponseSchedulingWhenIdle {
		t.Errorf("id %q scheduling %q", fr.ID, fr.Scheduling)
	}
	for _, k := range []string{"about_this", "at", "from", "kind", "agent"} {
		if fr.Response[k] == nil || fr.Response[k] == "" {
			t.Errorf("briefing lacks %s: %v", k, fr.Response)
		}
	}
	if fr.Response["from"] != "slow brain" {
		t.Errorf("from = %v", fr.Response["from"])
	}
	if c.count("content") != 0 {
		t.Errorf("a user-role turn was sent")
	}
	if p := h.log.payloads("voice.push"); len(p) != 1 || p[0]["briefing"] != "b-1" || p[0]["channel"] != ChannelFunctionResponse {
		t.Errorf("voice.push = %v", p)
	}
}

// TestBriefingRelations: the second line about the same open prompt
// says so, and a newer line about an agent says it supersedes the older.
func TestBriefingRelations(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, briefingConfig(), &fakeDialer{results: []func() (Conn, error){ok(c)}})
	push := func(text string, tags map[string]any) sent {
		t.Helper()
		if err := h.s.PushTagged(text, voice.UrgencyIdle, tags); err != nil {
			t.Fatal(err)
		}
		m := c.waitSent(t, text, isBriefing(text))
		c.serve(turnCompleteMsg())
		h.s.ClientPlayback(voice.PlaybackDrained, 1000)
		time.Sleep(150 * time.Millisecond)
		return m
	}
	push("docs asks to run rm. Approve?", map[string]any{"agent": "docs", "pending_id": "ap-1"})
	m := push("docs is still waiting on rm. Approve?", map[string]any{"agent": "docs", "pending_id": "ap-1"})
	rel, _ := m.tool.FunctionResponses[0].Response["relation"].([]string)
	if len(rel) == 0 || !strings.Contains(rel[0], "briefing number 2 about the same open prompt") {
		t.Errorf("relation = %v", rel)
	}
	m = push("auth is done.", map[string]any{"agent": "auth"})
	m = push("auth failed its last test.", map[string]any{"agent": "auth"})
	rel, _ = m.tool.FunctionResponses[0].Response["relation"].([]string)
	if len(rel) == 0 || !strings.Contains(rel[0], "newer than b-3") {
		t.Errorf("relation = %v", rel)
	}
}

// TestSilentBriefingIsResentAfterResume: a silent briefing sent after
// the last resumable handle is not in the context a resume restores, so
// it is sent again on the resumed connection, marked as re-sent.
func TestSilentBriefingIsResentAfterResume(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	h := newHarness(t, briefingConfig(), &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}})
	c1.serve(handleMsg("handle-A"))
	time.Sleep(20 * time.Millisecond)
	if err := h.s.Push("Current focus: docs, open question: approve rm?", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	m := c1.waitSent(t, "silent briefing", isBriefing("Current focus"))
	if m.tool.FunctionResponses[0].Scheduling != genai.FunctionResponseSchedulingSilent {
		t.Errorf("scheduling = %q", m.tool.FunctionResponses[0].Scheduling)
	}
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	m = c2.waitSent(t, "re-sent briefing", isBriefing("Current focus"))
	if m.tool.FunctionResponses[0].Response["resent"] == nil {
		t.Errorf("not marked re-sent: %v", m.tool.FunctionResponses[0].Response)
	}
}

// TestAnswerIsBriefingShaped: a to_supervisor answer carries facts, who
// answers and the time, not a line to read.
func TestAnswerIsBriefingShaped(t *testing.T) {
	c := newFakeConn("c1")
	cfg := briefingConfig()
	cfg.Handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "auth is done; 42 tests pass.", Scheduling: voice.UrgencyIdle}, nil
	}
	h := newHarness(t, cfg, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	_ = h
	c.serve(toolCallMsg("call-1", voice.ToolToSupervisor, map[string]any{"text": "how is auth"}))
	c.serve(turnCompleteMsg())
	m := c.waitSent(t, "answer", func(m sent) bool { return m.kind == "tool" && m.tool.FunctionResponses[0].ID == "call-1" })
	r := m.tool.FunctionResponses[0].Response
	if r["facts"] != "auth is done; 42 tests pass." || r["spoken"] != nil || r["from"] != "slow brain" || r["at"] == nil {
		t.Errorf("response = %v", r)
	}
}

// TestCutFactSaysWhoCut: a flush the daemon made for an urgent briefing
// is not reported as the CEO cutting the voice off.
func TestCutFactSaysWhoCut(t *testing.T) {
	s, err := newSession(Config{APIKeys: []string{"k"}}, &recLogger{}, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rep := voice.PlaybackReport{State: voice.PlaybackFlushed, PlayedMs: 2100, RunMs: 6400, At: now, Cut: &voice.TranscriptCut{}, Heard: "bench-codex wants to"}
	s.mu.Lock()
	fact := s.noteCutLocked(rep)
	s.brief.flushBy, s.brief.flushAt = "push", now
	byDaemon := s.noteCutLocked(rep)
	s.mu.Unlock()
	if !strings.Contains(fact, "the CEO cut you off after 2.1 s of 6.4 s") || !strings.Contains(fact, `"bench-codex wants to"`) {
		t.Errorf("fact = %q", fact)
	}
	if !strings.Contains(byDaemon, "the daemon cut you off") {
		t.Errorf("daemon cut = %q", byDaemon)
	}
}
