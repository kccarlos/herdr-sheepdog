package supervisor

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The brain's failover notice reaches the CEO as a spoken daemon push
// (SetNotifier wired in New) and lands in the brain's transcript: three
// turns in a row fail over from the selected provider, one line is said.
func TestBrainFailoverNoticeIsSpoken(t *testing.T) {
	primary := braintest.NewModel()
	primary.Default = &braintest.Step{Err: genai.APIError{Code: 504, Message: "Deadline expired before operation could complete.", Status: "DEADLINE_EXCEEDED"}}
	h := newBrainHarness(t, primary)
	b := h.sup.Brain()
	fallback := braintest.NewModel()
	fallback.Default = &braintest.Step{Text: braintest.Reply("Auth is running the tests.", "")}
	fallback.EventDefault = braintest.NothingToAdd()
	if err := b.AddProvider(brain.ProviderDeepSeek, fallback); err != nil {
		t.Fatal(err)
	}
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	for i := 0; i < 3; i++ {
		if res := h.say("what is auth doing?"); res.Spoken != "Auth is running the tests." {
			t.Fatalf("turn %d result = %+v", i, res)
		}
	}
	b.Quiesce() // the notice goes out as the brain's after-turn work
	const want = "My usual model is having trouble; a backup is answering for now."
	var spoken []voice.EventPush
	for _, p := range h.pusher.all() {
		if p.Text == want {
			spoken = append(spoken, p)
		}
	}
	if len(spoken) != 1 || spoken[0].Urgency == voice.UrgencySilent {
		t.Fatalf("notice pushes = %+v (all %+v)", spoken, h.pusher.all())
	}
	var inTranscript bool
	for _, m := range b.Transcript().Messages() {
		if m.Kind == brain.KindPush && strings.Contains(m.Content, want) {
			inTranscript = true
		}
	}
	if !inTranscript {
		t.Error("the notice is not in the brain's transcript")
	}
	if b.Provider() != brain.ProviderGemini {
		t.Errorf("selection changed to %s", b.Provider())
	}
}

// The notice names no vendor and follows the CEO's language (tier-2
// bench 2026-10-03 #7: "DeepSeek is failing; using Vertex for now." made
// the simulated CEO ask about "the Vertex worker").
func TestBrainFailoverNoticeInChinese(t *testing.T) {
	primary := braintest.NewModel()
	primary.Default = &braintest.Step{Err: genai.APIError{Code: 503, Message: "high demand", Status: "UNAVAILABLE"}}
	h := newBrainHarness(t, primary)
	b := h.sup.Brain()
	fallback := braintest.NewModel()
	fallback.Default = &braintest.Step{Text: braintest.Reply("auth 在跑测试。", "")}
	fallback.EventDefault = braintest.NothingToAdd()
	if err := b.AddProvider(brain.ProviderDeepSeek, fallback); err != nil {
		t.Fatal(err)
	}
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	for i := 0; i < 3; i++ {
		h.say("auth 在干什么？")
	}
	b.Quiesce()
	var got []string
	for _, p := range h.pusher.all() {
		if strings.Contains(p.Text, "模型") || strings.Contains(p.Text, "model") {
			got = append(got, p.Text)
		}
	}
	if len(got) != 1 || got[0] != "我平时用的模型出了点问题，现在先由备用模型回答。" {
		t.Fatalf("notice = %q", got)
	}
	for _, vendor := range []string{"DeepSeek", "Vertex", "Gemini"} {
		if strings.Contains(got[0], vendor) {
			t.Errorf("notice names %s: %q", vendor, got[0])
		}
	}
}
