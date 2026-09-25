package gemini

import (
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// contentIndex is the position of the push whose text is want among the
// content messages sent, or -1.
func contentIndex(c *fakeConn, want string) int {
	i := 0
	for _, m := range c.sentMsgs() {
		if m.kind != "content" {
			continue
		}
		if isContent(want)(m) {
			return i
		}
		i++
	}
	return -1
}

// TestOwedPushOvertakesIdleNews is docs/timelines.md C29: an answer to
// the shepherd's utterance queued behind an idle line that is waiting
// for the model's turn to end is spoken first once the turn ends, and
// the overtaking is logged (voice.push_overtook). Before the fix the
// queue looked at its head only (log 2026-09-24: voice.push_timeout at
// 00:24:11 and 16:21:42, an idle head stalling).
func TestOwedPushOvertakesIdleNews(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(outputTranscriptMsg("Auth is running the tests"))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })
	if err := h.s.PushTagged("docs finished", voice.UrgencyIdle, map[string]any{"push_id": "push-news"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PushTagged("Auth has two failing tests.", voice.UrgencyIdle, map[string]any{"push_id": "push-answer", "answers_utterance": "utt-1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("idle pushes sent while the model spoke: %s", describe(c.sentMsgs()))
	}
	c.serve(turnCompleteMsg())
	c.waitSent(t, "the answer", isContent("Auth has two failing tests."))
	if contentIndex(c, "docs finished") >= 0 {
		t.Fatalf("the idle news went before the answer: %s", describe(c.sentMsgs()))
	}
	ov := h.log.payloads("voice.push_overtook")
	if len(ov) != 1 || ov[0]["push_id"] != "push-answer" {
		t.Fatalf("voice.push_overtook = %v", ov)
	}
	if over, _ := ov[0]["over"].([]string); len(over) != 1 || over[0] != "push-news" {
		t.Errorf("over = %v, want [push-news]", ov[0]["over"])
	}
	c.serve(turnCompleteMsg())
	c.waitSent(t, "the news after it", isContent("docs finished"))
}

// TestStopReplyPassesAWaitingIdleHead: the stop button's reply (tag
// stop, appended by afterCancel) goes at once, although an idle line
// queued before it still waits for the model's turn.
func TestStopReplyPassesAWaitingIdleHead(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(outputTranscriptMsg("Auth is running the tests"))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })
	if err := h.s.Push("docs finished", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PushTagged("Stopped.", voice.UrgencyInterrupt, map[string]any{"stop": true}); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "the stop reply", isContent("Stopped."))
	if contentIndex(c, "docs finished") >= 0 {
		t.Fatalf("the idle line went first: %s", describe(c.sentMsgs()))
	}
	if ov := h.log.payloads("voice.push_overtook"); len(ov) != 1 || ov[0]["over"].([]string)[0] != "docs finished" {
		t.Errorf("voice.push_overtook = %v", ov)
	}
}

// TestIdlePushesStayFIFO: two lines of plain idle news keep their order,
// and nothing is logged as overtaken.
func TestIdlePushesStayFIFO(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	c.serve(audioMsg([]byte("x")))
	h.waitEvent("turn open", func(ev voice.Event) bool { return h.s.turnOpenNow() })
	for _, text := range []string{"docs finished", "auth finished"} {
		if err := h.s.Push(text, voice.UrgencyIdle); err != nil {
			t.Fatal(err)
		}
	}
	c.serve(turnCompleteMsg())
	c.waitSent(t, "first", isContent("docs finished"))
	c.serve(turnCompleteMsg())
	c.waitSent(t, "second", isContent("auth finished"))
	if contentIndex(c, "docs finished") != 0 || contentIndex(c, "auth finished") != 1 {
		t.Errorf("order: %s", describe(c.sentMsgs()))
	}
	if h.log.has("voice.push_overtook") {
		t.Error("FIFO idle lines logged as overtaking")
	}
}

// TestAnswersToOneUtteranceKeepTheirOrder: two lines answering the same
// utterance are never reordered, even when the second could go now.
func TestAnswersToOneUtteranceKeepTheirOrder(t *testing.T) {
	log := &recLogger{}
	s, err := newSession(Config{APIKeys: []string{"k"}}, log, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.conn = newFakeConn("c1")
	s.turnOpen, s.turnOpenedAt = true, now
	s.pushQ = []pushItem{
		{text: "first half", urgency: voice.UrgencyIdle, requested: voice.UrgencyIdle, at: now, tags: map[string]any{"answers_utterance": "utt-1"}},
		{text: "second half", urgency: voice.UrgencyInterrupt, requested: voice.UrgencyInterrupt, at: now, tags: map[string]any{"answers_utterance": "utt-1"}},
		{text: "other answer", urgency: voice.UrgencyInterrupt, requested: voice.UrgencyInterrupt, at: now, tags: map[string]any{"answers_utterance": "utt-2"}},
	}
	p := s.pickPush()
	if !p.ok || p.item.text != "other answer" {
		t.Fatalf("picked %+v, want \"other answer\" (utt-1's second half must wait for its first)", p.item)
	}
	s.turnOpen = false
	s.lastSpoken = now.Add(-time.Minute)
	if p = s.pickPush(); !p.ok || p.item.text != "first half" {
		t.Fatalf("picked %+v, want \"first half\"", p.item)
	}
}

// TestPushDroppedAfterFailedSends: a push whose send keeps failing is put
// back at the front three times, then dropped with voice.push_dropped
// {reason: send_failed} — before the fix only an EventError said so
// (docs/timelines.md §4 (d), §6 rank 6).
func TestPushDroppedAfterFailedSends(t *testing.T) {
	c := newFakeConn("c1")
	c.contentErr = errors.New("fake: write failed")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("auth finished", voice.UrgencyIdle, map[string]any{"push_id": "push-1"}); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("dropped", func(ev voice.Event) bool { return h.log.has("voice.push_dropped") })
	d := h.log.payloads("voice.push_dropped")
	if len(d) != 1 || d[0]["reason"] != "send_failed" || d[0]["push_id"] != "push-1" || d[0]["retries"] != maxPushRetries+1 {
		t.Errorf("voice.push_dropped = %v", d)
	}
	h.s.mu.Lock()
	n := len(h.s.pushQ)
	h.s.mu.Unlock()
	if n != 0 {
		t.Errorf("queue = %d items after the drop", n)
	}
}

// TestDrainPushesHandsOverTheQueue: what a provider switch carries over
// (voice.Switcher) is the queue in order, at the requested urgency (not
// a downgrade), and the queue is left empty.
func TestDrainPushesHandsOverTheQueue(t *testing.T) {
	s, err := newSession(Config{APIKeys: []string{"k"}}, &recLogger{}, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	s.pushQ = []pushItem{
		{text: "auth crashed", urgency: voice.UrgencyIdle, requested: voice.UrgencyInterrupt},
		{text: "docs is idle", urgency: voice.UrgencySilent, requested: voice.UrgencySilent, tags: map[string]any{"push_id": "p2"}},
	}
	got := s.DrainPushes()
	if len(got) != 2 || got[0].Urgency != voice.UrgencyInterrupt || got[1].Tags["push_id"] != "p2" || len(s.pushQ) != 0 {
		t.Errorf("DrainPushes = %+v, queue left %d", got, len(s.pushQ))
	}
}
