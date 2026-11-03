// Package fakevoice is a voice Session with no model and no network, for
// `sheepdog serve --voice=fake --dev` (docs/dev/e2e.md). It reports the
// voice as open the moment it starts, so a /ws client can hold it and
// /api/state shows voice.status "connected", and it answers a text message
// from the client with a canned transcript while it "speaks" for a moment.
// It never calls a tool and never touches the orchestrator or any pane, so
// it cannot approve or send anything.
package fakevoice

import (
	"context"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// SpeakFor is how long the fake claims to speak after a text message.
const SpeakFor = 6 * time.Second

// Session implements voice.Session.
type Session struct {
	audio  chan []byte
	events chan voice.Event

	mu       sync.Mutex
	closed   bool
	speakEnd time.Time
	now      func() time.Time
}

// New returns a Session.
func New() *Session {
	return &Session{audio: make(chan []byte, 1), events: make(chan voice.Event, 64), now: time.Now}
}

// Start reports the voice open.
func (s *Session) Start(context.Context) error {
	s.emit(voice.EventStatus{Status: voice.StatusConnected})
	return nil
}

// Close ends the session; Audio and Events are closed.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.audio)
	close(s.events)
	return nil
}

// SendAudio drops the microphone audio.
func (s *Session) SendAudio([]byte) error { return nil }

// SendText answers with a canned transcript and speaks for SpeakFor.
func (s *Session) SendText(text string) error {
	s.mu.Lock()
	s.speakEnd = s.now().Add(SpeakFor)
	s.mu.Unlock()
	s.emit(voice.EventTranscript{Dir: voice.DirIn, Text: text, Final: true})
	s.emit(voice.EventTranscript{Dir: voice.DirOut, Text: "Fake voice: I heard " + text, Final: true})
	return nil
}

// Push drops what the supervisor would have the model say.
func (s *Session) Push(string, voice.Urgency) error { return nil }

// Speaking is true for SpeakFor after the last SendText.
func (s *Session) Speaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.speakEnd)
}

// Audio yields nothing; closed on Close.
func (s *Session) Audio() <-chan []byte { return s.audio }

// Events yields the status and transcripts; closed on Close.
func (s *Session) Events() <-chan voice.Event { return s.events }

// SetMuted is ignored.
func (s *Session) SetMuted(bool) {}

func (s *Session) emit(e voice.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.events <- e:
	default:
	}
}

var _ voice.Session = (*Session)(nil)
