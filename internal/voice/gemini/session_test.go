package gemini

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func TestNewRequiresKeys(t *testing.T) {
	if _, err := newSession(Config{}, nil, &fakeDialer{}); !errors.Is(err, ErrNoAPIKeys) {
		t.Fatalf("err = %v, want ErrNoAPIKeys", err)
	}
}

func TestStartEmitsStatusAndUsesFirstKey(t *testing.T) {
	c := newFakeConn("c1")
	d := &fakeDialer{results: []func() (Conn, error){ok(c)}}
	h := newHarness(t, Config{Aliases: []string{"auth", "docs"}}, d)
	h.waitEvent("connected", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected
	})
	recs := d.records()
	if len(recs) != 1 || recs[0].key != "key-0" || recs[0].handle != "" {
		t.Fatalf("dials = %+v", recs)
	}
	if !h.log.has("voice.connect") {
		t.Error("voice.connect not logged")
	}
	if p := h.log.payloads("voice.system_prompt"); len(p) != 1 || !strings.Contains(p[0]["text"].(string), "aliases: auth, docs.") {
		t.Errorf("voice.system_prompt = %v", p)
	}
	if h.s.Status() != voice.StatusConnected {
		t.Errorf("status = %v", h.s.Status())
	}
}

func TestLiveConfigMatchesPlan(t *testing.T) {
	cfg := Config{APIKeys: []string{"k"}, Aliases: []string{"auth"}}.withDefaults()
	prompt, err := systemPrompt(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lc := liveConfig(cfg, prompt, "h-1")
	if len(lc.ResponseModalities) != 1 || lc.ResponseModalities[0] != genai.ModalityAudio {
		t.Errorf("modalities = %v", lc.ResponseModalities)
	}
	if lc.InputAudioTranscription == nil || lc.OutputAudioTranscription == nil {
		t.Error("transcription not enabled")
	}
	if lc.InputAudioTranscription.CustomVocabulary[0] != "auth" {
		t.Error("aliases not in custom vocabulary")
	}
	if *lc.ContextWindowCompression.TriggerTokens != 16000 || *lc.ContextWindowCompression.SlidingWindow.TargetTokens != 4000 {
		t.Error("compression config wrong")
	}
	if lc.SessionResumption.Handle != "h-1" {
		t.Error("handle not passed")
	}
	decls := lc.Tools[0].FunctionDeclarations
	names := map[string]bool{}
	for _, fd := range decls {
		names[fd.Name] = true
		if fd.Behavior != genai.BehaviorNonBlocking {
			t.Errorf("%s not NON_BLOCKING", fd.Name)
		}
	}
	// The voice is declared exactly two tools (docs/roles.md "One
	// decision-maker"): it relays and it stops, it never executes.
	if len(decls) != 2 {
		t.Errorf("%d tool declarations, want 2: %v", len(decls), names)
	}
	for _, n := range []string{voice.ToolToSupervisor, voice.ToolStop} {
		if !names[n] {
			t.Errorf("tool %s missing", n)
		}
	}
	if !names[voice.ToolToSupervisor] || len(decls[0].Parameters.Required) != 1 || decls[0].Parameters.Required[0] != "text" {
		t.Errorf("to_supervisor must require text: %+v", decls[0].Parameters)
	}
	if !bytes.Contains([]byte(lc.SystemInstruction.Parts[0].Text), []byte("aliases: auth.")) {
		t.Errorf("prompt aliases not rendered: %q", lc.SystemInstruction.Parts[0].Text)
	}
}

func TestAudioInAndMute(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	if err := h.s.SendAudio([]byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "audio", func(m sent) bool { return m.kind == "audio" && bytes.Equal(m.audio, []byte{1, 2, 3, 4}) })

	h.s.SetMuted(true)
	if !h.s.Muted() {
		t.Fatal("not muted")
	}
	c.waitSent(t, "audioStreamEnd", func(m sent) bool { return m.kind == "audioEnd" })
	for i := 0; i < 5; i++ {
		if err := h.s.SendAudio([]byte{9, 9}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := c.count("audio"); n != 1 {
		t.Fatalf("muted audio leaked: %d audio frames sent", n)
	}
	h.s.SetMuted(false)
	if err := h.s.SendAudio([]byte{5, 6}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "audio after unmute", func(m sent) bool { return m.kind == "audio" && bytes.Equal(m.audio, []byte{5, 6}) })
	if !h.log.has("voice.muted") {
		t.Error("voice.muted not logged")
	}
}

// TestAudioStreamEnd: the silence gate's "stream paused" signal reaches
// the server as audioStreamEnd, but not while muted (mute already sent
// it) and never before a connection exists.
func TestAudioStreamEnd(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.AudioStreamEnd()
	c.waitSent(t, "audioStreamEnd", func(m sent) bool { return m.kind == "audioEnd" })
	h.s.SetMuted(true)
	c.waitSent(t, "audioStreamEnd on mute", func(m sent) bool { return m.kind == "audioEnd" })
	h.s.AudioStreamEnd()
	time.Sleep(20 * time.Millisecond)
	if n := c.count("audioEnd"); n != 2 {
		t.Fatalf("audioEnd sent %d times, want 2 (gate, mute; not gate-while-muted)", n)
	}
	h.s.SetMuted(false)
	if err := h.s.SendAudio([]byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "audio reopens the stream", func(m sent) bool { return m.kind == "audio" })
}

func TestOutputQueueFlushOnInterrupted(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	// Nobody reads Audio() yet: chunks pile up in the queue.
	c.serve(audioMsg([]byte("a")))
	c.serve(audioMsg([]byte("b")))
	c.serve(outputTranscriptMsg("one two"))
	h.waitEvent("out transcript", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Dir == voice.DirOut && tr.Text == "one two"
	})
	c.serve(interruptedMsg())
	h.waitEvent("interrupted", func(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok })
	if !h.log.has("voice.interrupted") {
		t.Error("voice.interrupted not logged")
	}
	c.serve(audioMsg([]byte("c")))
	select {
	case got := <-h.s.Audio():
		if string(got) != "c" {
			t.Fatalf("got chunk %q after flush, want c", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no audio after flush")
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	c := newFakeConn("c1")
	var gotCall voice.ToolCall
	handler := func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		gotCall = call
		return voice.ToolResult{Spoken: "checking", Detail: "two agents, both idle", Options: []string{"a", "b"}, Pending: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(toolCallMsg("call_1", voice.ToolToSupervisor, map[string]any{"text": "what's going on"}))
	h.waitEvent("tool call", func(ev voice.Event) bool { tc, ok := ev.(voice.EventToolCall); return ok && tc.Call.ID == "call_1" })
	time.Sleep(20 * time.Millisecond)
	if c.count("tool") != 0 {
		t.Fatal("tool response sent before the calling turn completed")
	}
	c.serve(turnCompleteMsg()) // the model ends its turn right after a NON_BLOCKING call
	m := c.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	fr := m.tool.FunctionResponses[0]
	if fr.ID != "call_1" || fr.Name != voice.ToolToSupervisor || fr.Scheduling != genai.FunctionResponseSchedulingInterrupt {
		t.Fatalf("response = %+v", fr)
	}
	if fr.Response["spoken"] != "checking" || fr.Response["detail"] != "two agents, both idle" {
		t.Fatalf("response = %v", fr.Response)
	}
	if opts, _ := fr.Response["options"].([]string); len(opts) != 2 {
		t.Fatalf("options = %v", fr.Response["options"])
	}
	// pending travels with the reply: the voice says how long and carries
	// on, and the answer arrives on its own.
	if fr.Response["pending"] != true {
		t.Fatalf("pending not passed on: %v", fr.Response)
	}
	if gotCall.Name != voice.ToolToSupervisor || gotCall.Args["text"] != "what's going on" {
		t.Fatalf("handler saw %+v", gotCall)
	}
	h.waitEvent("tool call", func(ev voice.Event) bool { tc, ok := ev.(voice.EventToolCall); return ok && tc.Call.ID == "call_1" })
	h.waitEvent("tool result", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventToolResult)
		return ok && tr.Call.ID == "call_1" && tr.Err == nil && tr.Result.Spoken != ""
	})
	if !h.log.has("voice.tool_call") || !h.log.has("voice.tool_result") {
		t.Error("tool events not logged")
	}
	// The result carries the handler time and the wait for the turn so the
	// /debug swimlane can label the hop.
	results := h.log.payloads("voice.tool_result")
	if len(results) != 1 {
		t.Fatalf("tool_result payloads = %d, want 1", len(results))
	}
	for _, k := range []string{"dur_ms", "wait_ms"} {
		v, ok := results[0][k].(int64)
		if !ok || v < 0 {
			t.Errorf("tool_result %s = %v (%T), want a non-negative int64", k, results[0][k], results[0][k])
		}
	}
}

func TestToolResponseWaitTimesOut(t *testing.T) {
	c := newFakeConn("c1")
	handler := func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "ok"}, nil
	}
	h := newHarness(t, Config{Handler: handler, ToolResponseWait: 40 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	t0 := time.Now()
	c.serve(toolCallMsg("call_t", "note", map[string]any{"text": "x"}))
	c.waitSent(t, "tool response after timeout", func(m sent) bool { return m.kind == "tool" })
	if el := time.Since(t0); el < 35*time.Millisecond {
		t.Errorf("response after %v, want >= ToolResponseWait", el)
	}
	h.waitEvent("result", func(ev voice.Event) bool { _, ok := ev.(voice.EventToolResult); return ok })
	if !h.s.TurnOpen() {
		t.Error("a spoken tool response should open a turn")
	}
}

// TestToolResultDeferredWhileSpeaking: a tool result asking to INTERRUPT
// is downgraded to WHEN_IDLE, at send time, when the model has started
// speaking a new generation while the handler was still working — the
// same rule as a push (docs/roles.md "Pushes").
func TestToolResultDeferredWhileSpeaking(t *testing.T) {
	c := newFakeConn("c1")
	gate := make(chan struct{})
	handler := func(ctx context.Context, _ voice.ToolCall) (voice.ToolResult, error) {
		<-gate
		return voice.ToolResult{Spoken: "checking", Scheduling: voice.UrgencyInterrupt}, nil
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(toolCallMsg("call_1", voice.ToolToSupervisor, map[string]any{"text": "what's going on"}))
	h.waitEvent("tool call", func(ev voice.Event) bool { tc, ok := ev.(voice.EventToolCall); return ok && tc.Call.ID == "call_1" })
	c.serve(turnCompleteMsg()) // the model ends its turn right after the NON_BLOCKING call
	// A new generation starts speaking before the handler returns.
	c.serve(outputTranscriptMsg("Auth is also "))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })
	close(gate)

	m := c.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	fr := m.tool.FunctionResponses[0]
	if fr.Scheduling != genai.FunctionResponseSchedulingWhenIdle {
		t.Errorf("scheduling sent = %v, want WHEN_IDLE (deferred while speaking)", fr.Scheduling)
	}
	p := h.log.payloads("voice.push_deferred")
	if len(p) != 1 || p[0]["kind"] != "tool_result" || p[0]["name"] != voice.ToolToSupervisor || p[0]["reason"] != "speaking" || p[0]["urgency_requested"] != "interrupt" {
		t.Errorf("voice.push_deferred = %v", p)
	}
	results := h.log.payloads("voice.tool_result")
	if len(results) != 1 || results[0]["scheduling"] != "interrupt" || results[0]["scheduling_sent"] != "when_idle" {
		t.Errorf("voice.tool_result = %v, want scheduling interrupt, scheduling_sent when_idle", results)
	}
}

// TestStopToolStaysInterruptWhileSpeaking: stop() is the one tool result
// the shepherd is always owed at once (docs/roles.md "Pushes"), so it
// keeps INTERRUPT scheduling even while the model is speaking.
func TestStopToolStaysInterruptWhileSpeaking(t *testing.T) {
	c := newFakeConn("c1")
	gate := make(chan struct{})
	handler := func(ctx context.Context, _ voice.ToolCall) (voice.ToolResult, error) {
		<-gate
		return voice.ToolResult{Spoken: "Stopped.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(toolCallMsg("call_1", voice.ToolStop, map[string]any{}))
	h.waitEvent("tool call", func(ev voice.Event) bool { tc, ok := ev.(voice.EventToolCall); return ok && tc.Call.ID == "call_1" })
	c.serve(turnCompleteMsg())
	c.serve(outputTranscriptMsg("Auth is also "))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })
	close(gate)

	m := c.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	fr := m.tool.FunctionResponses[0]
	if fr.Scheduling != genai.FunctionResponseSchedulingInterrupt {
		t.Errorf("scheduling sent = %v, want INTERRUPT even while speaking", fr.Scheduling)
	}
	if h.log.has("voice.push_deferred") {
		t.Error("stop() must never be deferred")
	}
}

func TestToolSchedulingMapping(t *testing.T) {
	cases := []struct {
		u    voice.Urgency
		want genai.FunctionResponseScheduling
	}{
		{voice.UrgencyIdle, genai.FunctionResponseSchedulingWhenIdle},
		{voice.UrgencyInterrupt, genai.FunctionResponseSchedulingInterrupt},
		{voice.UrgencySilent, genai.FunctionResponseSchedulingSilent},
	}
	for _, tc := range cases {
		if got := scheduling(tc.u); got != tc.want {
			t.Errorf("scheduling(%v) = %v, want %v", tc.u, got, tc.want)
		}
	}
}

func TestToolHandlerErrorIsSpokenGently(t *testing.T) {
	c := newFakeConn("c1")
	handler := func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{}, errors.New("db exploded at /var/lib/x")
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	c.serve(toolCallMsg("call_2", "ask", map[string]any{"question": "why"}))
	m := c.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	spoken, _ := m.tool.FunctionResponses[0].Response["spoken"].(string)
	if bytes.Contains([]byte(spoken), []byte("/var/lib")) || spoken == "" {
		t.Fatalf("spoken = %q", spoken)
	}
	ev := h.waitEvent("tool result", func(ev voice.Event) bool { _, ok := ev.(voice.EventToolResult); return ok })
	if ev.(voice.EventToolResult).Err == nil {
		t.Error("handler error not reported on the event")
	}
}

func TestToolCallCancellation(t *testing.T) {
	c := newFakeConn("c1")
	started := make(chan struct{})
	handler := func(ctx context.Context, _ voice.ToolCall) (voice.ToolResult, error) {
		close(started)
		<-ctx.Done()
		return voice.ToolResult{Spoken: "too late"}, ctx.Err()
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	c.serve(toolCallMsg("call_3", "ask", map[string]any{"question": "slow"}))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not started")
	}
	c.serve(toolCancelMsg("call_3"))
	ev := h.waitEvent("cancelled result", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventToolResult)
		return ok && tr.Cancelled
	})
	if ev.(voice.EventToolResult).Call.ID != "call_3" {
		t.Errorf("wrong call cancelled: %+v", ev)
	}
	time.Sleep(20 * time.Millisecond)
	if c.count("tool") != 0 {
		t.Error("a response was sent for a cancelled call")
	}
}

func TestPushSilentAndInterrupt(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	if err := h.s.Push("docs is idle", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	m := c.waitSent(t, "silent push", isContent("docs is idle"))
	if *m.content.TurnComplete {
		t.Error("silent push must not complete the turn")
	}
	if m.content.Turns[0].Role != "user" {
		t.Error("push role must be user")
	}

	// Nobody is speaking: an interrupt push flushes and goes out at once
	// (docs/roles.md "Pushes" only defers while the model is speaking).
	if h.s.Speaking() {
		t.Fatal("not speaking yet")
	}
	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	m = c.waitSent(t, "interrupt push", isContent("auth crashed"))
	if !*m.content.TurnComplete {
		t.Error("interrupt push must complete the turn")
	}
	h.waitEvent("flush on interrupt push", func(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok })
	h.waitEvent("push event", func(ev voice.Event) bool {
		p, ok := ev.(voice.EventPush)
		return ok && p.Urgency == voice.UrgencyInterrupt
	})
	payloads := h.log.payloads("voice.push")
	if n := len(payloads); n != 2 {
		t.Fatalf("voice.push logged %d times, want 2", n)
	}
	if payloads[1]["urgency"] != "interrupt" || payloads[1]["scheduling_sent"] != "interrupt" {
		t.Errorf("voice.push = %v, want urgency and scheduling_sent both interrupt", payloads[1])
	}
	if h.log.has("voice.push_deferred") {
		t.Error("nothing to defer: the model was not speaking")
	}
}

// TestSpeakingLifecycle: Speaking() is narrower than TurnOpen() — false
// until the model actually produces output, true from the first
// fragment or audio chunk of a turn, false again on turnComplete.
func TestSpeakingLifecycle(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	// Sending text opens a turn (waiting for a reply) but is not speech.
	if err := h.s.SendText("what's going on"); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("turn open", func(ev voice.Event) bool { return h.s.TurnOpen() })
	if h.s.Speaking() {
		t.Fatal("sending text is not the model speaking")
	}

	c.serve(outputTranscriptMsg("Auth is "))
	h.waitEvent("out transcript", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Dir == voice.DirOut
	})
	if !h.s.Speaking() {
		t.Fatal("the first output fragment must start speaking")
	}

	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if h.s.Speaking() {
		t.Fatal("turnComplete must end speaking")
	}

	// An audio chunk starts speaking too.
	c.serve(audioMsg([]byte("a")))
	h.waitEvent("audio", func(ev voice.Event) bool { return h.s.Speaking() })

	// An interruption — the server's report, or the client's own flush —
	// ends speaking without waiting for turnComplete.
	c.serve(interruptedMsg())
	h.waitEvent("interrupted ends speaking", func(ev voice.Event) bool { return !h.s.Speaking() })
}

// TestPushInterruptDeferredWhileSpeaking: an untagged INTERRUPT push
// asked for while the model is speaking is deferred to WHEN_IDLE
// (docs/roles.md "Pushes": nothing interrupts the model mid-sentence
// except the shepherd) — it waits like any idle push and is not the
// local flush an immediate interrupt would be.
func TestPushInterruptDeferredWhileSpeaking(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(outputTranscriptMsg("Auth is running the tests"))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })

	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("push deferred", func(ev voice.Event) bool { return h.log.has("voice.push_deferred") })
	if p := h.log.payloads("voice.push_deferred"); len(p) != 1 || p[0]["kind"] != "push" || p[0]["reason"] != "speaking" || p[0]["urgency_requested"] != "interrupt" {
		t.Errorf("voice.push_deferred = %v", p)
	}
	time.Sleep(30 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("deferred push sent while the model was still speaking: %s", describe(c.sentMsgs()))
	}
	if n := h.countEvents(func(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok }); n != 0 {
		t.Error("a deferred push must not flush the model's output")
	}

	c.serve(turnCompleteMsg())
	m := c.waitSent(t, "deferred push, now idle", isContent("auth crashed"))
	if !*m.content.TurnComplete {
		t.Error("push must still complete the turn")
	}
	p := h.log.payloads("voice.push")
	if len(p) != 1 || p[0]["urgency"] != "interrupt" || p[0]["scheduling_sent"] != "when_idle" {
		t.Errorf("voice.push = %v, want urgency interrupt, scheduling_sent when_idle", p)
	}
}

// TestPushExemptTagsStayInterruptWhileSpeaking: due_promise and
// needs_user are the two exceptions (docs/roles.md "Pushes") that keep
// INTERRUPT scheduling even while the model is speaking.
func TestPushExemptTagsStayInterruptWhileSpeaking(t *testing.T) {
	for _, tag := range []string{"due_promise", "needs_user"} {
		t.Run(tag, func(t *testing.T) {
			c := newFakeConn("c1")
			h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

			c.serve(outputTranscriptMsg("Auth is running the tests"))
			h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })

			if err := h.s.PushTagged("interrupt me when it's done", voice.UrgencyInterrupt, map[string]any{tag: true}); err != nil {
				t.Fatal(err)
			}
			c.waitSent(t, "exempt push sent at once", isContent("interrupt me when it's done"))
			h.waitEvent("flush", func(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok })
			if h.log.has("voice.push_deferred") {
				t.Errorf("%s must not be deferred", tag)
			}
			p := h.log.payloads("voice.push")
			if len(p) != 1 || p[0]["urgency"] != "interrupt" || p[0]["scheduling_sent"] != "interrupt" || p[0][tag] != true {
				t.Errorf("voice.push = %v", p)
			}
		})
	}
}

func TestPushIdleWaitsForTurnAndGap(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: 150 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	// Model speaking: idle push must wait for turnComplete.
	c.serve(audioMsg([]byte("x")))
	h.waitEvent("turn open", func(ev voice.Event) bool { return h.s.turnOpenNow() })
	if err := h.s.Push("first", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Push("second", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("idle push sent while model was speaking: %s", describe(c.sentMsgs()))
	}
	c.serve(turnCompleteMsg())
	c.waitSent(t, "first push", isContent("first"))
	t1 := time.Now()
	if c.count("content") != 1 {
		t.Fatalf("second push sent before first turn completed: %s", describe(c.sentMsgs()))
	}
	// "first" opened a new turn; complete it, then the 150 ms gap applies.
	c.serve(turnCompleteMsg())
	c.waitSent(t, "second push", isContent("second"))
	if gap := time.Since(t1); gap < 140*time.Millisecond {
		t.Errorf("second push after %v, want >= 150ms gap", gap)
	}
	msgs := c.sentMsgs()
	if !isContent("first")(msgs[0]) || !isContent("second")(msgs[1]) {
		t.Errorf("push order wrong: %s", describe(msgs))
	}
}

func TestPushNotWhileUserSpeaking(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{SpeechHold: 200 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(inputTranscriptMsg("tell auth to", false))
	h.waitEvent("in transcript", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Dir == voice.DirIn })
	start := time.Now()
	if err := h.s.Push("docs finished", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("push sent while user speaking: %s", describe(c.sentMsgs()))
	}
	c.waitSent(t, "push after speech hold", isContent("docs finished"))
	if el := time.Since(start); el < 180*time.Millisecond {
		t.Errorf("push went out after %v, want >= 200ms speech hold", el)
	}

	// A finished transcription ends the hold immediately.
	c.serve(turnCompleteMsg())
	c.waitSent(t, "queued interrupt push", isContent("auth crashed"))
	c.serve(inputTranscriptMsg("okay", true))
	h.waitEvent("final in transcript", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Final })
	c.serve(turnCompleteMsg())
	if err := h.s.Push("silent ctx", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "silent push right after final transcript", isContent("silent ctx"))
	if !h.log.has("voice.transcript.in") {
		t.Error("voice.transcript.in not logged")
	}
}

func TestSendText(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.SendText("yes go ahead"); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "text", func(m sent) bool { return m.kind == "text" && m.text == "yes go ahead" })
}

func TestReconnectOnDropResumesAndReplaysAudio(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	gate := make(chan struct{})
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}, gate: gate}
	h := newHarness(t, Config{}, d)

	c1.serve(handleMsg("handle-A"))
	c1.serve(handleMsg("handle-B"))
	time.Sleep(20 * time.Millisecond)
	c1.fail(errors.New("websocket: close 1011 (internal server error)"))

	h.waitEvent("reconnecting", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusReconnecting
	})
	// Audio sent during the gap is buffered, in order.
	for _, b := range []string{"p1", "p2", "p3"} {
		if err := h.s.SendAudio([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.SendText("x"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("SendText during gap: %v, want ErrNotConnected", err)
	}
	close(gate)

	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if r := ev.(voice.EventResumed); r.Handle != "handle-B" || r.ContextLost {
		t.Fatalf("resumed = %+v", r)
	}
	recs := d.records()
	if len(recs) != 2 || recs[1].handle != "handle-B" || recs[1].key != "key-0" {
		t.Fatalf("dials = %+v", recs)
	}
	c2.waitSent(t, "replayed audio", func(m sent) bool { return m.kind == "audio" && string(m.audio) == "p3" })
	var got []string
	for _, m := range c2.sentMsgs() {
		if m.kind == "audio" {
			got = append(got, string(m.audio))
		}
	}
	if len(got) != 3 || got[0] != "p1" || got[1] != "p2" || got[2] != "p3" {
		t.Fatalf("replayed = %v", got)
	}
	if p := h.log.payloads("voice.resume"); len(p) != 1 || p[0]["handle"] != "handle-B" {
		t.Errorf("voice.resume payloads = %v", p)
	}
	h.waitEvent("connected again", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected
	})
	if h.countEvents(func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected
	}) != 2 {
		t.Error("expected two connected events")
	}
}

func TestGoAwayReconnectsProactively(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	c1.serve(handleMsg("handle-G"))
	time.Sleep(10 * time.Millisecond)
	c1.serve(goAwayMsg(10 * time.Second))
	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if ev.(voice.EventResumed).Handle != "handle-G" {
		t.Fatalf("resumed = %+v", ev)
	}
	if !h.log.has("voice.goaway") {
		t.Error("voice.goaway not logged")
	}
	select {
	case <-c1.closed:
	case <-time.After(time.Second):
		t.Fatal("old connection not closed after swap")
	}
	if err := h.s.SendAudio([]byte("new")); err != nil {
		t.Fatal(err)
	}
	c2.waitSent(t, "audio on new conn", func(m sent) bool { return m.kind == "audio" })
	// The old reader's exit must not trigger a second reconnect.
	time.Sleep(30 * time.Millisecond)
	if len(d.records()) != 2 {
		t.Errorf("dials = %+v, want exactly 2", d.records())
	}
}

func TestDropConnectionReconnects(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}})
	h.s.DropConnection()
	h.waitEvent("resumed after drop", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if !h.log.has("voice.drop") {
		t.Error("voice.drop not logged")
	}
}

func TestKeyRotationOn429AtConnect(t *testing.T) {
	c := newFakeConn("c1")
	d := &fakeDialer{results: []func() (Conn, error){
		fail(errors.New("Connect failed: websocket: close 1008: RESOURCE_EXHAUSTED: quota exceeded")),
		fail(errors.New("Connect to wss://x failed: websocket: bad handshake")),
		ok(c),
	}}
	h := newHarness(t, Config{}, d)
	recs := d.records()
	if len(recs) != 3 || recs[0].key != "key-0" || recs[1].key != "key-1" || recs[2].key != "key-2" {
		t.Fatalf("dials = %+v", recs)
	}
	rot := h.log.payloads("voice.key_rotated")
	if len(rot) != 2 || rot[0]["index"] != 1 || rot[1]["index"] != 2 {
		t.Fatalf("key_rotated = %v", rot)
	}
	if h.s.Status() != voice.StatusConnected {
		t.Error("not connected")
	}
}

func TestAllKeysExhaustedAtStart(t *testing.T) {
	d := &fakeDialer{results: []func() (Conn, error){fail(errors.New("429 Too Many Requests"))}}
	log := &recLogger{}
	s, err := newSession(Config{APIKeys: []string{"a", "b"}}, log, d)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(context.Background())
	if !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("Start err = %v, want ErrAllKeysExhausted", err)
	}
	if len(d.records()) != 2 {
		t.Errorf("dials = %+v, want both keys tried once", d.records())
	}
	if err := s.SendAudio([]byte{1}); !errors.Is(err, ErrClosed) {
		t.Errorf("SendAudio after failed start: %v", err)
	}
	// Events must be closed with a final closed status.
	var last voice.Event
	for ev := range s.Events() {
		last = ev
	}
	if st, ok := last.(voice.EventStatus); !ok || st.Status != voice.StatusClosed {
		t.Errorf("last event = %v", last)
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}

func TestNonRotatableConnectErrorFailsStart(t *testing.T) {
	d := &fakeDialer{results: []func() (Conn, error){fail(errors.New("dial tcp: no route to host"))}}
	s, err := newSession(Config{APIKeys: []string{"a", "b"}}, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil || errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("Start err = %v", err)
	}
	if len(d.records()) != 1 {
		t.Errorf("dials = %+v, want 1 (no rotation)", d.records())
	}
}

func TestRateLimitMidSessionBacksOffAndRotates(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	c1.serve(handleMsg("handle-R"))
	time.Sleep(10 * time.Millisecond)
	t0 := time.Now()
	c1.fail(errors.New(`received error in response: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.2s"}]}}`))

	h.waitEvent("rate limit error event", func(ev voice.Event) bool { _, ok := ev.(voice.EventError); return ok })
	ev := h.waitEvent("reconnected", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if el := time.Since(t0); el < 190*time.Millisecond {
		t.Errorf("reconnected after %v, want >= retryDelay 200ms", el)
	}
	rl := h.log.payloads("voice.rate_limited")
	if len(rl) != 1 || rl[0]["wait_ms"] != int64(200) || rl[0]["key_index"] != 0 {
		t.Fatalf("rate_limited = %v", rl)
	}
	recs := d.records()
	if len(recs) != 2 || recs[1].key != "key-1" {
		t.Fatalf("dials = %+v, want second dial on key-1", recs)
	}
	// The handle belongs to the old key's project: dropped, context lost.
	if r := ev.(voice.EventResumed); recs[1].handle != "" || !r.ContextLost {
		t.Errorf("handle reused across keys: dial=%+v event=%+v", recs[1], r)
	}
	if !h.log.has("voice.key_rotated") {
		t.Error("voice.key_rotated not logged")
	}
}

func TestReconnectBackoffOnRepeatedFailure(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){
		ok(c1),
		fail(errors.New("dial tcp: connection refused")),
		fail(errors.New("dial tcp: connection refused")),
		ok(c2),
	}}
	h := newHarness(t, Config{ReconnectBackoff: 15 * time.Millisecond}, d)
	t0 := time.Now()
	c1.fail(errors.New("eof"))
	h.waitEvent("reconnected", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if el := time.Since(t0); el < 40*time.Millisecond { // 15 + 30
		t.Errorf("reconnected after %v, want backoff >= 45ms", el)
	}
	if n := h.countEvents(func(ev voice.Event) bool { _, ok := ev.(voice.EventError); return ok }); n != 2 {
		t.Errorf("error events = %d, want 2", n)
	}
	if len(d.records()) != 4 {
		t.Errorf("dials = %d, want 4", len(d.records()))
	}
}

func TestCloseIsCleanAndIdempotent(t *testing.T) {
	c := newFakeConn("c1")
	d := &fakeDialer{results: []func() (Conn, error){ok(c)}}
	s, err := newSession(Config{APIKeys: []string{"k"}}, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil { // before Start
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.serve(audioMsg([]byte("a")))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-s.Audio(); ok {
		t.Error("Audio not closed")
	}
	var last voice.Event
	for ev := range s.Events() {
		last = ev
	}
	if st, ok := last.(voice.EventStatus); !ok || st.Status != voice.StatusClosed || st.Reason != "shutdown" {
		t.Errorf("last event = %+v", last)
	}
	if err := s.Push("x", voice.UrgencyIdle); !errors.Is(err, ErrClosed) {
		t.Errorf("Push after close: %v", err)
	}
}

func TestSendBeforeStart(t *testing.T) {
	s, err := newSession(Config{APIKeys: []string{"k"}}, nil, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendAudio([]byte{1}); !errors.Is(err, ErrNotStarted) {
		t.Errorf("SendAudio: %v", err)
	}
	if err := s.SendText("x"); !errors.Is(err, ErrNotStarted) {
		t.Errorf("SendText: %v", err)
	}
	if err := s.Push("x", voice.UrgencyIdle); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Push: %v", err)
	}
}

func TestReconnectBufferIsBounded(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	gate := make(chan struct{})
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}, gate: gate}
	h := newHarness(t, Config{ReconnectBuffer: 4}, d)
	c1.fail(errors.New("eof"))
	h.waitEvent("reconnecting", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusReconnecting
	})
	for _, b := range []string{"aa", "bb", "cc"} {
		_ = h.s.SendAudio([]byte(b))
	}
	close(gate)
	c2.waitSent(t, "replay", func(m sent) bool { return m.kind == "audio" && string(m.audio) == "cc" })
	var got []string
	for _, m := range c2.sentMsgs() {
		if m.kind == "audio" {
			got = append(got, string(m.audio))
		}
	}
	if len(got) != 2 || got[0] != "bb" {
		t.Fatalf("replayed = %v, want oldest dropped", got)
	}
}

func TestRateLimitHelpers(t *testing.T) {
	cases := []struct {
		err     string
		limited bool
		delay   time.Duration
	}{
		{"websocket: close 1008 (policy violation): RESOURCE_EXHAUSTED", true, 0},
		{`{"error":{"code":429,"message":"You exceeded your current quota. Please retry in 12.5s.","status":"RESOURCE_EXHAUSTED"}}`, true, 12500 * time.Millisecond},
		{`"retryDelay":"30s"`, false, 30 * time.Second},
		{"websocket: close 1011 (internal server error)", false, 0},
		{"", false, 0},
	}
	for _, tc := range cases {
		var err error
		if tc.err != "" {
			err = errors.New(tc.err)
		}
		if got := isRateLimited(err); got != tc.limited {
			t.Errorf("isRateLimited(%q) = %v", tc.err, got)
		}
		d, ok := retryDelay(err)
		if ok != (tc.delay > 0) || d != tc.delay {
			t.Errorf("retryDelay(%q) = %v,%v", tc.err, d, ok)
		}
	}
}

// A push whose turn the model never completes must not hold the next idle
// push past TurnTimeout (WP7b: pushes were stuck until the next tool call).
func TestPushIdleReleasedAfterTurnTimeout(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: 10 * time.Millisecond, TurnTimeout: 80 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	if err := h.s.Push("first", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "first push", isContent("first"))
	t1 := time.Now()
	// No turnComplete ever arrives for "first".
	if err := h.s.Push("second", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "second push", isContent("second"))
	if el := time.Since(t1); el < 70*time.Millisecond || el > 2*time.Second {
		t.Errorf("second push after %v, want ~TurnTimeout (80ms)", el)
	}
}

// A resumption handle the server keeps rejecting is dropped after
// ResumeAttempts failures so the session reconnects fresh (WP7b: the
// daemon retried a dead handle forever, ~30 s per attempt).
func TestDeadResumptionHandleIsDropped(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	setupErr := errors.New("failed to receive setup complete: websocket: close 1011 (internal server error): Internal error encountered.")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(setupErr), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d) // default ResumeAttempts (1)

	c1.serve(handleMsg("handle-A"))
	time.Sleep(20 * time.Millisecond)
	c1.fail(errors.New("websocket: close 1011 (internal server error)"))

	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if r := ev.(voice.EventResumed); !r.ContextLost || r.Handle != "" {
		t.Fatalf("resumed = %+v, want a fresh session (context lost)", r)
	}
	recs := d.records()
	if len(recs) != 3 || recs[1].handle != "handle-A" || recs[2].handle != "" {
		t.Fatalf("dials = %+v, want one resume attempt then a fresh dial", recs)
	}
	if p := h.log.payloads("voice.handle_dropped"); len(p) != 1 {
		t.Errorf("voice.handle_dropped payloads = %v, want 1", p)
	}
	if p := h.log.payloads("voice.connect_error"); len(p) != 1 {
		t.Errorf("voice.connect_error payloads = %d, want 1", len(p))
	}
}

// TestTurnLogged: voice.turn carries the input → first response time and
// the tool call count, and is written only once the tool's result turn
// completed.
func TestTurnLogged(t *testing.T) {
	c := newFakeConn("c1")
	handler := func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "One agent, idle.", Scheduling: voice.UrgencyIdle}, nil
	}
	h := newHarness(t, Config{Handler: handler}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(inputTranscriptMsg("what's going on", false))
	h.waitEvent("in transcript", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Dir == voice.DirIn })
	time.Sleep(30 * time.Millisecond)
	c.serve(toolCallMsg("call_1", "briefing", nil))
	c.serve(turnCompleteMsg()) // ends the calling turn; the tool response is still owed
	c.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	if h.log.has("voice.turn") {
		t.Fatal("voice.turn logged before the result was spoken")
	}
	c.serve(audioMsg([]byte("spoken")))
	c.serve(turnCompleteMsg())
	deadline := time.Now().Add(2 * time.Second)
	for !h.log.has("voice.turn") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	turns := h.log.payloads("voice.turn")
	if len(turns) != 1 {
		t.Fatalf("voice.turn = %v, want 1", turns)
	}
	p := turns[0]
	ms, _ := p["input_to_response_ms"].(int64)
	if p["responded"] != true || p["tool_calls"] != 1 || p["end"] != "complete" || p["stalled"] != false || ms < 25 || ms > 1500 {
		t.Fatalf("voice.turn = %v", p)
	}

	// A turn the model answers with nothing (turnComplete, no audio, no
	// tool) is closed without counting as a stall.
	c.serve(inputTranscriptMsg("hm", false))
	c.serve(turnCompleteMsg())
	deadline = time.Now().Add(2 * time.Second)
	for len(h.log.payloads("voice.turn")) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if turns = h.log.payloads("voice.turn"); len(turns) != 2 || turns[1]["responded"] != false {
		t.Fatalf("voice.turn = %v", turns)
	}
	if h.log.has("voice.stalled") {
		t.Error("voice.stalled logged for an answered turn")
	}
}

// TestStallDetectionAndRecycle: an utterance the model ignores for
// StallAfter logs voice.stalled and flags the status; the model reacting
// clears the flag; a second stall on the same connection recycles it with
// the current resumption handle (voice.recycle → voice.resume).
func TestStallDetectionAndRecycle(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{StallAfter: 40 * time.Millisecond}, d)
	c1.serve(handleMsg("handle-A"))

	c1.serve(inputTranscriptMsg("hello?", false))
	h.waitEvent("stalled", func(ev voice.Event) bool { st, ok := ev.(voice.EventStatus); return ok && st.Stalled })
	if p := h.log.payloads("voice.stalled"); len(p) != 1 || p[0]["stalls"] != 1 {
		t.Fatalf("voice.stalled = %v", p)
	}
	if h.log.has("voice.recycle") {
		t.Fatal("recycled after one stall")
	}
	c1.serve(audioMsg([]byte("late answer")))
	h.waitEvent("stall cleared", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected && !st.Stalled
	})
	c1.serve(turnCompleteMsg())

	c1.serve(inputTranscriptMsg("anyone there?", false))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if p := h.log.payloads("voice.recycle"); len(p) != 1 || p[0]["reason"] != "stalled" || p[0]["handle"] != true {
		t.Fatalf("voice.recycle = %v", p)
	}
	recs := d.records()
	if len(recs) != 2 || recs[1].handle != "handle-A" {
		t.Fatalf("dials = %+v, want a reconnect with the handle", recs)
	}
	if h.s.Status() != voice.StatusConnected {
		t.Fatalf("status = %v", h.s.Status())
	}
	// The turn that was open at the swap is closed, and the counters
	// start over: a third stall alone does not recycle again.
	turns := h.log.payloads("voice.turn")
	if len(turns) == 0 || turns[len(turns)-1]["end"] != "reconnect" {
		t.Fatalf("voice.turn = %v, want the last one ended by the reconnect", turns)
	}
	c2.serve(inputTranscriptMsg("still there?", false))
	time.Sleep(120 * time.Millisecond)
	if n := len(h.log.payloads("voice.stalled")); n != 3 {
		t.Fatalf("voice.stalled count = %d, want 3", n)
	}
	if n := len(h.log.payloads("voice.recycle")); n != 1 {
		t.Fatalf("voice.recycle count = %d, want 1 (counters reset on reconnect)", n)
	}
}

// TestPushTimeoutsRecycle: two idle pushes that each outwait TurnTimeout
// (the model answered them with nothing) recycle the connection.
func TestPushTimeoutsRecycle(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{PushGap: 5 * time.Millisecond, TurnTimeout: 40 * time.Millisecond}, d)
	c1.serve(handleMsg("handle-B"))

	for _, text := range []string{"first", "second", "third"} {
		if err := h.s.Push(text, voice.UrgencyIdle); err != nil {
			t.Fatal(err)
		}
	}
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if p := h.log.payloads("voice.push_timeout"); len(p) != 2 {
		t.Fatalf("voice.push_timeout = %v, want 2", p)
	}
	if p := h.log.payloads("voice.recycle"); len(p) != 1 || p[0]["reason"] != "push_timeout" {
		t.Fatalf("voice.recycle = %v", p)
	}
	if recs := d.records(); len(recs) != 2 || recs[1].handle != "handle-B" {
		t.Fatalf("dials = %+v", recs)
	}
}

// TestSystemPromptName substitutes the dog's own name (config
// voice.name) and the aliases into the voice prompt, for the embedded
// default and for a SystemPrompt override alike.
func TestSystemPromptName(t *testing.T) {
	got, err := SystemPrompt(Config{Name: "Rex", Aliases: []string{"auth", "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "You are Rex,") || strings.Contains(got, "{name}") || !strings.Contains(got, "aliases: auth, docs.") {
		t.Errorf("prompt = %q", got[:min(80, len(got))])
	}
	// No configured name: the built-in one.
	if got, _ = SystemPrompt(Config{Aliases: []string{"auth"}}); !strings.HasPrefix(got, "You are "+prompts.DefaultName+",") {
		t.Errorf("default name: %q", got[:min(40, len(got))])
	}
	if got, _ = SystemPrompt(Config{Name: "Rex", SystemPrompt: "I am {name}; the agents are {aliases}."}); got != "I am Rex; the agents are none configured yet." {
		t.Errorf("override = %q", got)
	}
}
