package gemini

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// A late answer as the function response (brain.reply_wait_s above the
// utterance budget): the call stays open while the orchestrator works, so the
// paths around it must not lose the answer.

// lateHandler blocks until gate closes, then answers like the
// supervisor's inline reply.
func lateHandler(gate <-chan struct{}) voice.ToolHandler {
	return func(ctx context.Context, _ voice.ToolCall) (voice.ToolResult, error) {
		select {
		case <-gate:
		case <-ctx.Done():
			return voice.ToolResult{}, ctx.Err()
		}
		return voice.ToolResult{Spoken: "Auth's tests are green.", Options: []string{"merge", "wait"}, Scheduling: voice.UrgencyIdle, UtteranceID: "utt-1"}, nil
	}
}

// docs/dev/timelines.md C23: the connection the call came on dies while the
// orchestrator works. The function response cannot go on the new connection,
// so the answer goes through the push queue instead of being dropped.
func TestLateAnswerOnDeadConnectionIsPushed(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	gate := make(chan struct{})
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{Handler: lateHandler(gate), ReconnectBackoff: 5 * time.Millisecond}, d)

	c1.serve(toolCallMsg("call_1", voice.ToolToSupervisor, map[string]any{"text": "how is auth"}))
	h.waitEvent("tool call", func(ev voice.Event) bool { _, ok := ev.(voice.EventToolCall); return ok })
	c1.serve(turnCompleteMsg())
	c1.fail(errors.New("websocket: close 1011 (internal server error): Internal error encountered."))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	close(gate)

	m := c2.waitSent(t, "answer pushed on the new connection", isContent("Auth's tests are green. Options: merge; wait."))
	if !*m.content.TurnComplete {
		t.Error("the pushed answer must be spoken (turn complete)")
	}
	if c2.count("tool") != 0 || c1.count("tool") != 0 {
		t.Errorf("a function response was sent: c1 %s c2 %s", describe(c1.sentMsgs()), describe(c2.sentMsgs()))
	}
	p := h.log.payloads("voice.tool_result_pushed")
	if len(p) != 1 || p[0]["reason"] != "send_error" {
		t.Errorf("voice.tool_result_pushed = %v", p)
	}
	push := h.log.payloads("voice.push")
	if len(push) != 1 || push[0]["answers_utterance"] != "utt-1" || push[0]["call_id"] != "call_1" {
		t.Errorf("voice.push = %v, want the answer tagged with its utterance and call", push)
	}
}

// A result that lands while the user is speaking waits until they
// stop, as a push does (speakingUntilLocked).
func TestLateAnswerHeldWhileShepherdSpeaks(t *testing.T) {
	c := newFakeConn("c1")
	gate := make(chan struct{})
	h := newHarness(t, Config{Handler: lateHandler(gate), SpeechHold: 200 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(toolCallMsg("call_1", voice.ToolToSupervisor, map[string]any{"text": "how is auth"}))
	h.waitEvent("tool call", func(ev voice.Event) bool { _, ok := ev.(voice.EventToolCall); return ok })
	c.serve(turnCompleteMsg())
	c.serve(inputTranscriptMsg("and also tell docs", false))
	h.waitEvent("in transcript", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Dir == voice.DirIn })
	start := time.Now()
	close(gate)
	time.Sleep(60 * time.Millisecond)
	if c.count("tool") != 0 {
		t.Fatal("the answer went out while the shepherd was speaking")
	}
	c.waitSent(t, "answer after the speech hold", func(m sent) bool { return m.kind == "tool" })
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Errorf("answer after %v, want the speech hold", el)
	}
	p := h.log.payloads("voice.push_deferred")
	if len(p) != 1 || p[0]["reason"] != "shepherd_speaking" {
		t.Errorf("voice.push_deferred = %v", p)
	}
}
