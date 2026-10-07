package gptlive

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func vlabels(p ctxcap.VoicePoint) []string {
	var out []string
	for _, s := range p.Sections {
		out = append(out, s.Label)
	}
	return out
}

func vsection(p ctxcap.VoicePoint, label string) (ctxcap.Section, string) {
	for _, s := range p.Sections {
		if strings.HasPrefix(s.Label, label) {
			return s, p.Text[s.Start:s.End]
		}
	}
	return ctxcap.Section{}, ""
}

// The voice context log of a GPT-Live session, through a fake socket:
// session.start (instructions, the delegation setting), the user's
// audio with what the model heard, the delegation and the client's
// answer to it (linked by the delegation id, the utterance claimed by
// it), a push with its link tags, and a reset whose next session starts
// a new context with the silent context held meanwhile.
func TestVoiceContextCapture(t *testing.T) {
	vl := ctxcap.NewVoiceLog(nil)
	cfg := testConfig()
	cfg.Context = vl
	cfg.Handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "Auth is waiting for approval.", Scheduling: voice.UrgencyIdle}, nil
	}
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": " What's going on?"})
	c.server(map[string]any{"type": evDelegationCreated, "delegation": map[string]any{"id": "item_1", "target": "client"}})
	waitFor(t, "commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	if err := s.PushTagged("auth finished.", voice.UrgencyIdle, map[string]any{ctxcap.TagPushID: "p1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "push", func() bool { return len(c.events(evCommentaryAppend)) == 2 })
	var push ctxcap.VoiceTurn
	waitFor(t, "push item", func() bool {
		for _, tr := range vl.Turns() {
			if tr.Kind == ctxcap.VKindPush {
				push = tr
				return true
			}
		}
		return false
	})
	p, _, _, err := vl.Get(push.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCoverage(); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(vlabels(p), "|")
	for _, want := range []string{"session open (fresh)", "system instructions", "tool declarations", "user (audio + transcript)", "delegation to client", "delegation result (commentary)", "push (commentary)"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %s", want, got)
		}
	}
	if !strings.HasPrefix(got, "session open (fresh)|system instructions|tool declarations|user") {
		t.Errorf("order: %s", got)
	}
	if _, text := vsection(p, "system instructions"); !strings.Contains(text, "be brief") {
		t.Errorf("instructions = %q", text)
	}
	if s, text := vsection(p, "user"); strings.Join(s.Links, ",") != "call:item_1" || !strings.Contains(text, "What's going on?") || !strings.Contains(text, "🎤 audio") {
		t.Errorf("utterance = %v %q", s.Links, text)
	}
	for _, label := range []string{"delegation to client", "delegation result"} {
		if s, _ := vsection(p, label); strings.Join(s.Links, ",") != "call:item_1" {
			t.Errorf("%s links = %v", label, s.Links)
		}
	}
	if s, text := vsection(p, "push"); strings.Join(s.Links, ",") != "push:p1" || !strings.Contains(text, "auth finished.") {
		t.Errorf("push = %v %q", s.Links, text)
	}
	if strings.Contains(p.Text, "sk-test") {
		t.Error("key material in the voice context")
	}

	// Reset, then silent context in standby, then the next session: a new
	// context whose input carries the held context.
	if err := s.Reset("test"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "standby", func() bool { return !s.Live() })
	if err := s.Push("docs is idle", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "held", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.held) == 1 })
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "reopen", func() bool { return d.count() == 2 && s.Live() })
	var fresh ctxcap.VoiceTurn
	waitFor(t, "second epoch", func() bool {
		for _, tr := range vl.Turns() {
			if tr.Epoch == 2 && tr.Kind == ctxcap.VKindContext {
				fresh = tr
				return true
			}
		}
		return false
	})
	cur, _, _, err := vl.Get(fresh.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if s, text := vsection(cur, "held silent context"); s.Key == "" || !strings.Contains(text, "docs is idle") {
		t.Errorf("held context = %q in %q", text, vlabels(cur))
	}
	var reset bool
	for _, tr := range vl.Turns() {
		if tr.Label == "conversation reset" {
			reset = true
		}
	}
	if !reset {
		t.Error("no reset marker")
	}
}
