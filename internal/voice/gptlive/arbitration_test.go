package gptlive

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// pickSession is a Session with a live connection and a client, on a
// fake clock, for the push arbitration (pickPushLocked).
func pickSession(t *testing.T) (*Session, *time.Time) {
	t.Helper()
	s, err := newSession(testConfig(), &recLog{}, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.live = &liveConn{}
	s.client = true
	return s, &now
}

// queue adds an idle push.
func queue(s *Session, text string, tags map[string]any) {
	u := voice.UrgencyIdle
	s.pushQ = append(s.pushQ, pushItem{text: text, urgency: u, requested: u, tags: tags, at: s.now()})
}

// TestGPTLiveOwedPushOvertakesIdleNews: docs/dev/timelines.md C29 on
// GPT-Live — the answer to the user's utterance queued behind idle
// news goes first once the voice stops speaking.
func TestGPTLiveOwedPushOvertakesIdleNews(t *testing.T) {
	s, now := pickSession(t)
	s.lastVoiced = *now // the voice is speaking
	queue(s, "docs finished", map[string]any{"push_id": "push-news"})
	queue(s, "Auth has two failing tests.", map[string]any{"push_id": "push-answer", "answers_utterance": "utt-1"})
	if p := s.pickPushLocked(*now); p.ok {
		t.Fatalf("picked %q while the voice speaks", p.item.text)
	}
	*now = now.Add(time.Second)
	p := s.pickPushLocked(*now)
	if !p.ok || p.item.text != "Auth has two failing tests." || len(p.over) != 1 || p.over[0].text != "docs finished" {
		t.Fatalf("picked %+v over %v, want the answer over the news", p.item, p.over)
	}
	*now = now.Add(time.Second)
	if p = s.pickPushLocked(*now); !p.ok || p.item.text != "docs finished" || len(p.over) != 0 {
		t.Fatalf("then %+v, want the news", p.item)
	}
}

// TestGPTLiveIdlePushesStayFIFO: plain idle news keeps its order, and a
// kept promise (keeps_promise_ids) is owed like an answer.
func TestGPTLiveIdlePushesStayFIFO(t *testing.T) {
	s, now := pickSession(t)
	queue(s, "docs finished", nil)
	queue(s, "auth finished", nil)
	for _, want := range []string{"docs finished", "auth finished"} {
		*now = now.Add(time.Second)
		if p := s.pickPushLocked(*now); !p.ok || p.item.text != want || len(p.over) != 0 {
			t.Fatalf("picked %+v, want %q", p.item, want)
		}
	}
	s.lastVoiced = *now
	queue(s, "docs finished", nil)
	queue(s, "As you asked: auth finished.", map[string]any{"keeps_promise_ids": []string{"prom-1"}})
	*now = now.Add(time.Second)
	if p := s.pickPushLocked(*now); !p.ok || p.item.text != "As you asked: auth finished." {
		t.Fatalf("picked %+v, want the kept promise first", p.item)
	}
}

// TestGPTLiveDrainPushesHandsOverHeldContextAndQueue: a provider switch
// carries the silent context held for the next session and the queue.
func TestGPTLiveDrainPushesHandsOverHeldContextAndQueue(t *testing.T) {
	s, _ := pickSession(t)
	s.held = []string{"docs is idle"}
	queue(s, "auth finished", nil)
	got := s.DrainPushes()
	if len(got) != 2 || got[0].Urgency != voice.UrgencySilent || got[1].Text != "auth finished" || len(s.pushQ) != 0 || len(s.held) != 0 {
		t.Errorf("DrainPushes = %+v", got)
	}
}
