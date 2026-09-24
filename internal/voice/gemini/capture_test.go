package gemini

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// latestKind polls the log until its newest turn has kind (and epoch,
// when epoch > 0).
func waitTurn(t *testing.T, vl *ctxcap.VoiceLog, kind string, epoch int) ctxcap.VoiceTurn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turns := vl.Turns()
		for i := len(turns) - 1; i >= 0; i-- {
			if turns[i].Kind == kind && (epoch == 0 || turns[i].Epoch == epoch) {
				return turns[i]
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s turn (epoch %d) in %+v", kind, epoch, vl.Turns())
	return ctxcap.VoiceTurn{}
}

func labels(p ctxcap.VoicePoint) []string {
	var out []string
	for _, s := range p.Sections {
		out = append(out, s.Label)
	}
	return out
}

func section(p ctxcap.VoicePoint, label string) (ctxcap.Section, string) {
	for _, s := range p.Sections {
		if strings.HasPrefix(s.Label, label) {
			return s, p.Text[s.Start:s.End]
		}
	}
	return ctxcap.Section{}, ""
}

// The voice context log of a Gemini session, through a fake connection:
// the fresh session's instructions and tools, the shepherd's audio with
// what the model heard, the to_supervisor call and its response (linked
// by the call id, the utterance claimed by it), a push with its link
// tags, and a reset that starts a new context.
func TestVoiceContextCapture(t *testing.T) {
	vl := ctxcap.NewVoiceLog(nil)
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	handler := func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "Auth is running the tests.", Scheduling: voice.UrgencyIdle}, nil
	}
	h := newHarness(t, Config{Context: vl, Handler: handler, Aliases: []string{"auth"}}, &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}})

	for i := 0; i < 3; i++ {
		if err := h.s.SendAudio(make([]byte, 3200)); err != nil { // 100 ms each
			t.Fatal(err)
		}
	}
	c1.serve(inputTranscriptMsg("what is auth ", false))
	c1.serve(inputTranscriptMsg("doing?", true))
	c1.serve(toolCallMsg("fc-1", voice.ToolToSupervisor, map[string]any{"text": "what is auth doing?"}))
	c1.serve(outputTranscriptMsg("Checking."))
	c1.serve(turnCompleteMsg())
	c1.waitSent(t, "tool response", func(m sent) bool { return m.kind == "tool" })
	c1.serve(turnCompleteMsg()) // the model spoke the result
	if err := h.s.PushTagged("auth finished.", voice.UrgencyIdle, map[string]any{ctxcap.TagPushID: "p1", ctxcap.TagEventID: "e1"}); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "push", isContent("auth finished."))
	push := waitTurn(t, vl, ctxcap.VKindPush, 1)

	p, _, _, err := vl.Get(push.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	want := []string{"session open (fresh)", "system instructions", "tool declarations", "shepherd (audio + transcript)", "tool call to_supervisor", "voice said", "tool response to_supervisor (when_idle)", "push (client content, turn complete)"}
	if got := labels(p); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("items = %q\nwant    %q", got, want)
	}
	if s, text := section(p, "system instructions"); s.Class != ctxcap.ClassSystem || !strings.Contains(text, "auth") {
		t.Errorf("instructions = %+v %.80q", s, text)
	}
	if _, text := section(p, "tool declarations"); !strings.Contains(text, "to_supervisor(text) [non_blocking]") || !strings.Contains(text, "stop()") {
		t.Errorf("tools = %q", text)
	}
	if s, text := section(p, "shepherd"); !strings.Contains(text, "🎤 audio 0.3 s") || !strings.Contains(text, `heard: "what is auth doing?"`) || strings.Join(s.Links, ",") != "call:fc-1" {
		t.Errorf("utterance = %+v %q", s.Links, text)
	}
	for _, label := range []string{"tool call to_supervisor", "tool response"} {
		if s, _ := section(p, label); strings.Join(s.Links, ",") != "call:fc-1" || len(s.Raw) == 0 {
			t.Errorf("%s links = %v raw %d", label, s.Links, len(s.Raw))
		}
	}
	if s, text := section(p, "push"); strings.Join(s.Links, ",") != "push:p1,evt:e1" || !strings.Contains(text, "[system event, not spoken by the user] auth finished.") {
		t.Errorf("push = %v %q", s.Links, text)
	}
	if strings.Contains(p.Text, "key-0") {
		t.Error("key material in the voice context")
	}

	// Reset: a marker, then a fresh connection opens a new context.
	if err := h.s.Reset("test"); err != nil {
		t.Fatal(err)
	}
	fresh := waitTurn(t, vl, ctxcap.VKindMarker, 2)
	cur, prev, hasPrev, err := vl.Get(fresh.Turn)
	if err != nil || !hasPrev {
		t.Fatal(err)
	}
	d := ctxcap.DiffVoice(cur, prev, hasPrev)
	if len(d.Removed) == 0 || d.Removed[0].Reason != ctxcap.ReasonReset {
		t.Errorf("reset diff = %+v", d)
	}
	if !strings.Contains(prev.Text, "conversation reset") {
		t.Errorf("no reset marker before the fresh session: %q", labels(prev))
	}
}
