package gemini

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Campaign D approve-zh-hurried r2 (docs/dev/eval/campaign-d-2026-10-03.md
// N1), as logged.
const (
	n1Push  = "bench-codex wants to run the full test suite with the race detector, and it's waiting on your yes or no. Approve the test run?"
	n1Names = "The agents are now called: bench-codex. Use these names; never a nickname."
	n1Focus = "Current focus, for follow-ups (context only, never read it out). Agent in focus: bench-codex. Open question you asked: Approve the test run?"
)

// TestSilentContextWaitsForTheVoiceToBeQuiet is approve-zh-hurried r2:
// the approval push went out at 1378 ms, the voice started on it, and
// the names line went out at 3380 ms, while it was still speaking. The
// voice stopped after "detector, and it's", the client drained, and the
// rest came back only after the user's Chinese, glued to the next reply.
// The names line now waits for the voice's turn to complete and for the
// client to finish playing it.
func TestSilentContextWaitsForTheVoiceToBeQuiet(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged(n1Push, voice.UrgencyInterrupt, map[string]any{"push_id": "push-tmc3bw-3", "needs_user": true}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "approval", isContent(n1Push))
	speakReply(h, c, []string{"bench-codex wants to", "run the full"}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)

	if err := h.s.Push(n1Names, voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := countSent(c, n1Names); n != 0 {
		t.Fatalf("names line sent while the voice was speaking")
	}
	speakReply(h, c, []string{"test suite with the race", "detector, and it's", "waiting on your yes or no. Approve the test run?"}, 300)
	c.serve(turnCompleteMsg())
	time.Sleep(400 * time.Millisecond)
	if n := countSent(c, n1Names); n != 0 {
		t.Fatalf("names line sent while the client was still playing the push")
	}
	h.s.ClientPlayback(voice.PlaybackDrained, 1500)
	c.waitSent(t, "names line once quiet", isContent(n1Names))
	if p := h.pushesLogged(2); len(p) != 2 || p[1]["scheduling_sent"] != "silent" {
		t.Errorf("voice.push = %v", p)
	}
}

// TestQueuedFocusLineIsReplaced: two current-focus lines queued while the
// voice speaks; only the latest goes out once it is quiet.
func TestQueuedFocusLineIsReplaced(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged(n1Push, voice.UrgencyIdle, map[string]any{"push_id": "push-1"}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "push", isContent(n1Push))
	speakReply(h, c, []string{"bench-codex wants to"}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	old := "Current focus, for follow-ups (context only, never read it out). Agent in focus: bench-claude."
	if err := h.s.PushTagged(old, voice.UrgencySilent, map[string]any{voice.TagFocus: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Push(n1Names, voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PushTagged(n1Focus, voice.UrgencySilent, map[string]any{voice.TagFocus: true}); err != nil {
		t.Fatal(err)
	}
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 300)
	c.waitSent(t, "names line", isContent(n1Names))
	c.waitSent(t, "latest focus", isContent(n1Focus))
	if n := countSent(c, old); n != 0 {
		t.Errorf("the replaced focus line was sent")
	}
	if got := h.s.recap.Focus(); got != n1Focus {
		t.Errorf("recap focus = %q", got)
	}
}
