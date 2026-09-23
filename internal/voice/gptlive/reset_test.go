package gptlive

import "testing"

// Reset ("New conversation", docs/roles.md) closes an open billed
// session (no recap kept) so the one that reopens later carries none of
// the earlier conversation.
func TestResetClosesLiveSessionAndDropsRecap(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": "remember this please", "start_ms": 0, "end_ms": 500})
	// SettleWait (60ms in testConfig) closes the utterance into the
	// recap.
	waitFor(t, "settled into recap", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.recap) == 1
	})

	if err := s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session closed", func() bool { return len(l.find("voice.session_close")) == 1 })
	cl := l.find("voice.session_close")
	if cl[0]["reason"] != "reset" {
		t.Fatalf("session_close reason = %v, want reset", cl[0]["reason"])
	}
	if s.Live() {
		t.Fatal("still live after Reset")
	}
	s.mu.Lock()
	recapLen := len(s.recap)
	s.mu.Unlock()
	if recapLen != 0 {
		t.Fatalf("recap not cleared: %d entries", recapLen)
	}

	// The next session opens with no recap in its input.
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "reopened", func() bool { return d.count() == 2 })
	waitFor(t, "second session live", s.Live)
	start := d.last().events(evSessionStart)[0]["session"].(map[string]any)
	if input, ok := start["input"]; ok && input != nil {
		if arr, ok := input.([]any); ok && len(arr) != 0 {
			t.Fatalf("second session carried a recap: %v", input)
		}
	}
}

// Reset in standby (no live session) simply clears the recap.
func TestResetInStandbyClearsRecap(t *testing.T) {
	s, _, _ := startSession(t, testConfig())
	if err := s.SendText("remember this"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if len(s.recap) != 1 {
		s.mu.Unlock()
		t.Fatal("expected the typed text in the recap")
	}
	s.mu.Unlock()

	if err := s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := len(s.recap)
	s.mu.Unlock()
	if got != 0 {
		t.Fatalf("recap not cleared: %d entries", got)
	}
}

// Reset before Start (or after Close) is a harmless no-op.
func TestResetBeforeStartIsNoop(t *testing.T) {
	d := &fakeDialer{}
	s, err := newSession(testConfig(), nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reset("x"); err != nil {
		t.Fatalf("Reset before Start: %v", err)
	}
}
