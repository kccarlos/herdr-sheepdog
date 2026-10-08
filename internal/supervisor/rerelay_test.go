package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// incidentWords are the user's words at 2026-10-08 03:04:58.
const incidentWords = "那我目前先不认可这样改。我想问的是, 自己的这种 API 支持有没有更好的解决方案? 不用整个 fork 过去。"

// 2026-10-08 03:04:58: the user asked; the orchestrator's turn failed
// and fell back; the voice did not speak the fallback but called
// to_supervisor again a second later with the same words, which the gate
// ignored as "nothing new", and the user heard nothing. Now the re-relay
// retries the user's recorded words once: a second orchestrator turn
// runs on them and its answer is the result.
func TestReRelayAfterFailedTurnRetriesTheWords(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: braintest.ErrBoom},
		braintest.Step{Text: braintest.Reply("Keep your own adapter. No fork needed.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)

	res := h.say(incidentWords)
	if res.Spoken != brainFailedLine || res.Scheduling != voice.UrgencyInterrupt {
		t.Fatalf("first result = %+v, want the fallback", res)
	}
	if !strings.Contains(res.Detail, "failed: the orchestrator's model returned an error") || !strings.Contains(res.Detail, "heard nothing") {
		t.Errorf("fallback detail = %q", res.Detail)
	}
	h.clock.Advance(time.Second)
	// The voice relays again with its own wording; nothing new was heard.
	res = h.relay("那我目前先不认可这样改。我想问的是，自己的这种 API 支持有没有更好的解决方案，不用整个 fork 过去？")
	if res.Spoken != "Keep your own adapter. No fork needed." {
		t.Fatalf("re-relay result = %+v, want the retried turn's answer", res)
	}
	if n := h.log.count("supervisor.voice_initiative_ignored"); n != 0 {
		t.Errorf("voice_initiative_ignored = %d, want 0", n)
	}
	relays := h.log.ofType("supervisor.relay")
	if len(relays) != 2 {
		t.Fatalf("supervisor.relay = %v", relays)
	}
	p := payload(relays[1])
	if p["heard"] != incidentWords || p["retry_of"] != payload(relays[0])["utterance_id"] || p["retry_of"] == "" {
		t.Errorf("retry relay = %v", p)
	}
	calls := m.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Text(), incidentWords) {
		t.Errorf("the retry did not run on the recorded words: %d calls", len(calls))
	}
}

// The retry is spent once: a re-relay after the retried turn failed too
// runs nothing, and its silent fact says what happened — the turn
// failed, why, that it was retried, and that the user has heard no
// answer yet.
func TestReRelayRetriesOnlyOnce(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom}, braintest.Step{Err: braintest.ErrBoom})
	h := newBrainHarness(t, m)
	h.say(incidentWords)
	h.clock.Advance(time.Second)
	if res := h.relay(incidentWords); res.Spoken != brainFailedLine {
		t.Fatalf("retry result = %+v, want the fallback", res)
	}
	h.clock.Advance(time.Second)
	res := h.relay(incidentWords)
	if !noFreshWords(res) {
		t.Fatalf("third relay = %+v, want the silent fact", res)
	}
	for _, want := range []string{"failed at", "returned an error", "already retried once", "heard no answer yet", "runs again on the user's next words"} {
		if !strings.Contains(res.Spoken, want) {
			t.Errorf("fact %q lacks %q", res.Spoken, want)
		}
	}
	if n := len(m.Calls()); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}
}

// An answer handed to the voice that it never spoke: a re-relay runs
// nothing (the user's words were answered — the self-relay the gate
// exists for), but the fact says the answer is there and unheard. Once
// the voice has spoken, the plain fact is back.
func TestReRelayAfterUnspokenAnswerSaysSo(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Keep your own adapter. No fork needed.", "")})
	h := newBrainHarness(t, m)
	h.say(incidentWords)
	h.clock.Advance(time.Second)
	res := h.relay(incidentWords)
	if !noFreshWords(res) || !strings.Contains(res.Spoken, `answered those words at`) || !strings.Contains(res.Spoken, "Keep your own adapter") || !strings.Contains(res.Spoken, "has not heard that answer yet") {
		t.Errorf("fact = %+v", res)
	}
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Keep your own adapter.", Final: true})
	h.clock.Advance(time.Second)
	res = h.relay(incidentWords)
	if !noFreshWords(res) || strings.Contains(res.Spoken, "answered those words") {
		t.Errorf("after the voice spoke, fact = %+v", res)
	}
	if n := len(m.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

// "Right after" is bounded: past rerelayWindow a re-relay is the voice's
// own initiative again, with the plain fact.
func TestReRelayLongAfterFailureIsIgnored(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom}, braintest.Step{Text: braintest.Reply("late", "")})
	h := newBrainHarness(t, m)
	h.say(incidentWords)
	h.clock.Advance(rerelayWindow + time.Second)
	res := h.relay(incidentWords)
	if !noFreshWords(res) || strings.Contains(res.Spoken, "failed") {
		t.Errorf("fact = %+v", res)
	}
	if n := len(m.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}

// New words after a failed turn are an ordinary new utterance, not a
// retry of the old one.
func TestNewWordsAfterFailureAreNotARetry(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Err: braintest.ErrBoom}, braintest.Step{Text: braintest.Reply("Status is fine.", "")})
	h := newBrainHarness(t, m)
	h.say(incidentWords)
	h.clock.Advance(time.Second)
	if res := h.say("what's the status?"); res.Spoken != "Status is fine." {
		t.Fatalf("result = %+v", res)
	}
	relays := h.log.ofType("supervisor.relay")
	if len(relays) != 2 || payload(relays[1])["retry_of"] != nil || payload(relays[1])["heard"] != "what's the status?" {
		t.Errorf("relays = %v", relays)
	}
}
