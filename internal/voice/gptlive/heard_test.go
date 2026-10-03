package gptlive

import (
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// unheardEvents collects the session's EventPushUnheard events.
type unheardEvents struct {
	mu  sync.Mutex
	evs []voice.EventPushUnheard
}

func collectUnheard(s *Session) *unheardEvents {
	u := &unheardEvents{}
	go func() {
		for ev := range s.Events() {
			if e, ok := ev.(voice.EventPushUnheard); ok {
				u.mu.Lock()
				u.evs = append(u.evs, e)
				u.mu.Unlock()
			}
		}
	}()
	return u
}

func (u *unheardEvents) all() []voice.EventPushUnheard {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]voice.EventPushUnheard(nil), u.evs...)
}

// openLive opens a live session with a listening client.
func openLive(t *testing.T, s *Session, d *fakeDialer) *fakeConn {
	t.Helper()
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	return d.last()
}

// speak serves the model saying words (voiced audio, then each word's
// transcript).
func speak(c *fakeConn, words ...string) {
	for _, w := range words {
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 20*24))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
}

func heardConfig() Config {
	cfg := testConfig()
	cfg.Names = func() []string { return []string{"bench-codex", "bench-claude"} }
	return cfg
}

func shortSettle(t *testing.T) {
	old := requeueSettle
	requeueSettle = 50 * time.Millisecond
	t.Cleanup(func() { requeueSettle = old })
}

// TestGPTLiveParaphrasedPushIsHeard: GPT-Live voices a push in its own
// words (run 2's line, in Chinese, with the numbers said as numerals):
// the key terms are there, so nothing is reported.
func TestGPTLiveParaphrasedPushIsHeard(t *testing.T) {
	s, d, l := startSession(t, heardConfig())
	u := collectUnheard(s)
	c := openLive(t, s, d)
	if err := s.PushTagged("bench-codex：测试还是失败（结果是3，应该是5），跟之前说的全部通过不一样。", voice.UrgencyIdle, map[string]any{"agent": "bench-codex"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	speak(c, "bench-codex", "那边测试还是没过，", "算出来是三，", "应该是五。")
	waitFor(t, "speech over", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow && !s.speakingOrPlayingLocked(s.now())
	})
	waitFor(t, "judged", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.heard.Watching() })
	if p := l.find("voice.push_unheard"); len(p) != 0 || len(u.all()) != 0 {
		t.Fatalf("a paraphrased push was judged unheard: %v", p)
	}
}

// TestGPTLiveUnheardNeedsYouPushIsRequeued: the shepherd starts talking
// just after an approval line went out, and the model answers them
// instead (run 2, 00:48:38): the line is judged unheard (user_turn),
// reported, and re-queued once at idle after the shepherd's turn and its
// reply.
func TestGPTLiveUnheardNeedsYouPushIsRequeued(t *testing.T) {
	shortSettle(t)
	s, d, l := startSession(t, heardConfig())
	u := collectUnheard(s)
	s.SetOpenCheck(func(map[string]any) bool { return true })
	c := openLive(t, s, d)
	line := "bench-claude wants to run go test. Options: Yes; No."
	if err := s.PushTagged(line, voice.UrgencyIdle, map[string]any{"agent": "bench-claude", "needs_user": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	c.server(map[string]any{"type": evInputTranscript, "delta": " what is the docs agent doing?"})
	speak(c, "Checking", "on", "the", "docs", "agent", "now.")
	waitFor(t, "unheard", func() bool { return len(l.find("voice.push_unheard")) == 1 })
	p := l.find("voice.push_unheard")[0]
	if p["reason"] != voice.UnheardUserTurn || p["requeue"] != true || p["judge"] != "key_terms" || p["text"] != line {
		t.Fatalf("voice.push_unheard = %v", p)
	}
	waitFor(t, "event", func() bool { return len(u.all()) == 1 })
	if ev := u.all()[0]; !ev.Requeue || ev.Push.Text != line {
		t.Fatalf("EventPushUnheard = %+v", ev)
	}
	waitFor(t, "re-queued", func() bool { return len(c.events(evCommentaryAppend)) == 2 })
	if r := l.find("voice.push_requeued"); len(r) != 1 || r[0]["requeued"] != true {
		t.Errorf("voice.push_requeued = %v", r)
	}
	// The re-queued line is unheard again: reported, never re-queued.
	c.server(map[string]any{"type": evInputTranscript, "delta": " and the tests?"})
	speak(c, "Let", "me", "check", "the", "tests.")
	waitFor(t, "second verdict", func() bool { return len(l.find("voice.push_unheard")) == 2 })
	if p := l.find("voice.push_unheard")[1]; p["requeue"] != false {
		t.Errorf("a re-queued line was re-queued again: %v", p)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(c.events(evCommentaryAppend)); n != 2 {
		t.Errorf("%d commentary appends, want 2", n)
	}
}

// TestGPTLiveUnheardNewsGoesToTheSupervisor: plain news nobody owes a
// hearing of is reported (the supervisor tells the brain) and not
// re-queued; an approval the open check says is answered is dropped.
func TestGPTLiveUnheardNewsGoesToTheSupervisor(t *testing.T) {
	shortSettle(t)
	s, d, l := startSession(t, heardConfig())
	u := collectUnheard(s)
	s.SetOpenCheck(func(map[string]any) bool { return false })
	c := openLive(t, s, d)
	if err := s.PushTagged("bench-codex finished: 3 of 5 tests pass.", voice.UrgencyIdle, map[string]any{"agent": "bench-codex"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	c.server(map[string]any{"type": evInputTranscript, "delta": " hello?"})
	speak(c, "Hi,", "I'm", "here.")
	waitFor(t, "event", func() bool { return len(u.all()) == 1 })
	if ev := u.all()[0]; ev.Requeue || ev.Push.Reason != voice.UnheardUserTurn {
		t.Fatalf("EventPushUnheard = %+v", ev)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(c.events(evCommentaryAppend)); n != 1 || len(l.find("voice.push_requeued")) != 0 {
		t.Errorf("plain news was re-queued (%d appends)", n)
	}
}
