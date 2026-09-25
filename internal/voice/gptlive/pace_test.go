package gptlive

import (
	"encoding/base64"
	"testing"
	"time"
)

// paceSession is a Session with a live fake connection on a fake clock
// and the production 40 ms Tick, for keepAlive's pacing.
func paceSession(t *testing.T) (*Session, *fakeConn, *recLog, *time.Time) {
	t.Helper()
	cfg := testConfig()
	cfg.Tick = 40 * time.Millisecond
	l := &recLog{}
	s, err := newSession(cfg, l, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := newFakeConn()
	s.live = &liveConn{conn: c, id: "live_fake"}
	return s, c, l, &now
}

// sentAudio sums the audio appended on c, as a duration at 24 kHz.
func sentAudio(t *testing.T, c *fakeConn) time.Duration {
	t.Helper()
	var n int
	for _, ev := range c.events(evInputAudioAppend) {
		b, err := base64.StdEncoding.DecodeString(ev["audio"].(string))
		if err != nil {
			t.Fatal(err)
		}
		n += len(b)
	}
	return time.Duration(n/2) * time.Second / SampleRate
}

// frame is d of 24 kHz PCM16 (zeros; the level does not matter here).
func frame(d time.Duration) []byte { return make([]byte, durSamples(d)*2) }

// TestKeepAlivePacesClosedGateAtRealTime: 10 s of wall clock with the
// client's gate closed sends ≈ 10 s of input audio (it used to be ≤ 5 s:
// one 40 ms frame only every second tick).
func TestKeepAlivePacesClosedGateAtRealTime(t *testing.T) {
	s, c, l, now := paceSession(t)
	start := *now
	_ = s.sendAudio(s.live, frame(40*time.Millisecond), srcClient) // the client's last frame, then the gate closes
	for now.Sub(start) < 10*time.Second {
		*now = now.Add(40 * time.Millisecond)
		s.keepAlive(*now)
	}
	got := sentAudio(t, c)
	if d := got - 10*time.Second; d < -100*time.Millisecond || d > 100*time.Millisecond {
		t.Fatalf("sent %v of audio in 10 s of wall clock, want 10 s ± 100 ms", got)
	}
	if len(l.find("voice.input_debt_dropped")) != 0 {
		t.Fatal("debt dropped on a steady tick")
	}
}

// TestKeepAliveSilentWhileClientStreams: a client streaming 20 ms frames
// at real time gets no silence mixed in.
func TestKeepAliveSilentWhileClientStreams(t *testing.T) {
	s, c, _, now := paceSession(t)
	start := *now
	for i := 1; now.Sub(start) < 10*time.Second; i++ {
		_ = s.sendAudio(s.live, frame(20*time.Millisecond), srcClient)
		*now = now.Add(20 * time.Millisecond)
		if i%2 == 0 {
			s.keepAlive(*now)
		}
	}
	if got, want := sentAudio(t, c), 10*time.Second; got != want {
		t.Fatalf("sent %v, want exactly the client's %v (no silence)", got, want)
	}
}

// TestKeepAliveTickJitterDoesNotAccumulateDebt: ticks that arrive up to
// 60 ms late still keep the input at real time — a late tick pays more.
func TestKeepAliveTickJitterDoesNotAccumulateDebt(t *testing.T) {
	s, c, l, now := paceSession(t)
	start := *now
	_ = s.sendAudio(s.live, frame(40*time.Millisecond), srcClient)
	for i := 0; now.Sub(start) < 10*time.Second; i++ {
		step := 40 * time.Millisecond
		if i%3 == 1 {
			step += 60 * time.Millisecond
		}
		*now = now.Add(step)
		s.keepAlive(*now)
	}
	got, wall := sentAudio(t, c), now.Sub(start)
	if d := got - wall; d < -100*time.Millisecond || d > 100*time.Millisecond {
		t.Fatalf("sent %v in %v of wall clock with jittery ticks", got, wall)
	}
	if len(l.find("voice.input_debt_dropped")) != 0 {
		t.Fatal("debt dropped under 60 ms jitter")
	}
}

// TestKeepAliveDropsDebtOverBurst: a 3 s stall pays at most 200 ms at
// once and logs the forgiven rest once.
func TestKeepAliveDropsDebtOverBurst(t *testing.T) {
	s, c, l, now := paceSession(t)
	_ = s.sendAudio(s.live, frame(40*time.Millisecond), srcClient)
	before := sentAudio(t, c)
	*now = now.Add(3 * time.Second)
	s.keepAlive(*now)
	if got := sentAudio(t, c) - before; got != paceMaxBurst {
		t.Fatalf("burst %v, want %v", got, paceMaxBurst)
	}
	*now = now.Add(40 * time.Millisecond)
	s.keepAlive(*now)
	d := l.find("voice.input_debt_dropped")
	if len(d) != 1 || d[0]["ms"] != int64(2760) {
		t.Fatalf("input_debt_dropped %v, want one of 2760 ms", d)
	}
}

// TestKeepAliveForgivesLongLead: input sent far ahead of the wall clock
// (a replayed pre-roll, a fast client clock) holds the keep-alive off for
// at most paceMaxLead.
func TestKeepAliveForgivesLongLead(t *testing.T) {
	s, c, _, now := paceSession(t)
	_ = s.sendAudio(s.live, frame(5*time.Second), srcClient)
	start := *now
	for now.Sub(start) < 3*time.Second {
		*now = now.Add(40 * time.Millisecond)
		s.keepAlive(*now)
	}
	if got := sentAudio(t, c) - 5*time.Second; got < 800*time.Millisecond || got > 1100*time.Millisecond {
		t.Fatalf("silence after a 5 s lead over 3 s: %v, want ≈ 1 s less the client-quiet wait (lead capped at %v)", got, paceMaxLead)
	}
}

// TestKeepAliveReplayIsNotLead: the pre-roll and the frames buffered
// while the session opened, replayed at once, are audio from the past —
// once the gate closes after the utterance, silence starts within the
// client-quiet wait, not after the replay's 1.5 s.
func TestKeepAliveReplayIsNotLead(t *testing.T) {
	s, c, _, now := paceSession(t)
	_ = s.sendAudio(s.live, frame(1500*time.Millisecond), srcReplay)
	for i := 0; i < 50; i++ { // 1 s of live speech
		*now = now.Add(20 * time.Millisecond)
		_ = s.sendAudio(s.live, frame(20*time.Millisecond), srcClient)
	}
	speech := sentAudio(t, c)
	gateClosed := *now
	var firstSilence time.Duration
	for now.Sub(gateClosed) < 10*time.Second {
		*now = now.Add(40 * time.Millisecond)
		s.keepAlive(*now)
		if firstSilence == 0 && sentAudio(t, c) > speech {
			firstSilence = now.Sub(gateClosed)
		}
	}
	if firstSilence == 0 || firstSilence > 200*time.Millisecond {
		t.Fatalf("first silence %v after the gate closed, want ≤ 200 ms", firstSilence)
	}
	if got := sentAudio(t, c) - speech; got < 9800*time.Millisecond || got > 10100*time.Millisecond {
		t.Fatalf("silence %v in 10 s after the gate closed, want ≈ 10 s", got)
	}
}
