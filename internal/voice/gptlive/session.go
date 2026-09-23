package gptlive

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

const logSource = "voice"

// Errors returned by the Session methods.
var (
	ErrNotStarted = errors.New("gptlive: session not started")
	ErrClosed     = errors.New("gptlive: session closed")
)

// Status reasons on the voice.EventStatus{StatusConnected} events: no
// live GPT-Live session is open (nothing is billed) vs. one is.
const (
	ReasonStandby = "standby"
	ReasonLive    = ""
)

// Session implements voice.Session for GPT-Live. See the package
// documentation for the lifecycle.
type Session struct {
	cfg  Config
	dial Dialer
	log  logging.Logger
	now  func() time.Time

	audio  chan []byte
	events chan voice.Event
	out    *outQueue

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	emitMu       sync.RWMutex
	eventsClosed bool

	mu      sync.Mutex // guards everything below
	started bool
	closed  bool
	muted   bool
	client  bool
	// armed: a loud microphone frame may open a session. An idle close
	// disarms it (so a noisy room does not reopen what was just closed);
	// the silence gate closing, a quiet second, unmuting or a client
	// (re)attaching re-arms it.
	armed      bool
	quietSince time.Time
	up         upsampler

	live     *liveConn // the open, started session; nil in standby
	opening  bool
	nextOpen time.Time
	preroll  [][]byte // the last moments of microphone audio in standby
	pending  [][]byte // audio held while a session opens
	pendingN int

	lastActivity time.Time
	lastVoiced   time.Time
	lastOutText  time.Time
	speakingNow  bool
	lastInput    time.Time
	inputOpen    bool
	unclaimed    string // input text no delegation has claimed yet
	inText       string // the current input utterance (recap)
	outText      string // the current reply (recap)
	recap        []recapEntry
	held         []string // silent context received in standby
	pushQ        []pushItem
	lastSpoken   time.Time
	inflight     map[string]context.CancelFunc
	toolSeq      int
	billed       float64 // seconds of closed sessions
	sessions     int
	outBytes     int64
}

// liveConn is one GPT-Live session (one WebSocket).
type liveConn struct {
	conn     Conn
	seq      int
	id       string
	reason   string // why it was opened
	openedAt time.Time
	cancel   context.CancelFunc
	started  chan struct{}
	done     chan struct{} // closed when the reader exits
	lastSend atomic.Int64  // unix nanos of the last audio sent

	// under Session.mu
	usage       float64
	closeWhy    string // our reason, when we asked to close
	finalized   bool
	startedOnce sync.Once
}

type recapEntry struct {
	role string // "user" | "assistant"
	text string
}

// pushItem is one queued Push.
type pushItem struct {
	text      string
	urgency   voice.Urgency
	requested voice.Urgency
	tags      map[string]any
	at        time.Time
}

// New builds a Session; it does not connect (see Start).
func New(cfg Config, log logging.Logger) (*Session, error) {
	return newSession(cfg, log, wsDialer{})
}

func newSession(cfg Config, log logging.Logger, dial Dialer) (*Session, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	cfg = cfg.withDefaults()
	if log == nil {
		log = logging.NopLogger{}
	}
	return &Session{
		cfg: cfg, dial: dial, log: log, now: time.Now,
		audio:    make(chan []byte),
		events:   make(chan voice.Event, cfg.EventBuffer),
		out:      newOutQueue(),
		inflight: map[string]context.CancelFunc{},
		armed:    true,
	}, nil
}

// Start implements voice.Session. It does not dial: the session is
// available (StatusConnected, reason standby) and opens on demand.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("gptlive: Start called twice")
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()
	s.emit(voice.EventStatus{Status: voice.StatusConnected, Reason: ReasonStandby})
	s.log.Log(logSource, "voice.standby", "", map[string]any{"provider": "openai", "model": s.cfg.Model, "idle_close_s": s.cfg.IdleClose.Seconds()})
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.out.pump(s.ctx, s.audio) }()
	go func() { defer s.wg.Done(); s.tickLoop(s.ctx) }()
	return nil
}

// Close implements voice.Session: a live session is closed gracefully
// (session.close, then session.closed for the final usage) before the
// channels close.
func (s *Session) Close() error {
	s.mu.Lock()
	if !s.started || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	lc := s.live
	s.mu.Unlock()
	if lc != nil {
		s.closeLive(lc, "shutdown")
		select {
		case <-lc.done:
		case <-time.After(s.cfg.CloseTimeout + time.Second):
		}
	}
	s.cancel()
	s.mu.Lock()
	for _, c := range s.inflight {
		c()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.once.Do(func() {
		s.emit(voice.EventStatus{Status: voice.StatusClosed, Reason: "shutdown"})
		s.log.Log(logSource, "voice.closed", "", map[string]any{"reason": "shutdown", "billed_total_s": s.BilledSeconds()})
		s.emitMu.Lock()
		s.eventsClosed = true
		close(s.events)
		s.emitMu.Unlock()
	})
	return nil
}

// Audio implements voice.Session.
func (s *Session) Audio() <-chan []byte { return s.audio }

// Events implements voice.Session.
func (s *Session) Events() <-chan voice.Event { return s.events }

// Status reports StatusConnected once started (the session can always be
// opened on demand) and StatusClosed after Close.
func (s *Session) Status() voice.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return voice.StatusClosed
	case s.started:
		return voice.StatusConnected
	default:
		return voice.StatusConnecting
	}
}

// Live reports whether a billed GPT-Live session is open right now.
func (s *Session) Live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live != nil
}

// BilledSeconds is the GPT-Live duration billed so far: every closed
// session's final usage plus the open one's latest snapshot.
func (s *Session) BilledSeconds() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.billed
	if s.live != nil {
		t += s.live.usage
	}
	return t
}

// VoiceStats is merged into web.client.stats.
func (s *Session) VoiceStats() map[string]any {
	billed := s.BilledSeconds()
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"voice_provider": "openai", "voice_session_open": s.live != nil, "voice_sessions": s.sessions, "voice_billed_seconds": billed}
}

// SetMuted implements voice.Session. Muted frames are dropped; the idle
// timer then closes a live session.
func (s *Session) SetMuted(muted bool) {
	s.mu.Lock()
	changed := s.muted != muted
	s.muted = muted
	if !muted {
		s.armed = true
	}
	s.mu.Unlock()
	if changed {
		s.log.Log(logSource, "voice.muted", "", map[string]any{"muted": muted})
	}
}

// AudioStreamEnd is the client's silence gate closing: the next frame
// starts a new utterance, so it may open a session again.
func (s *Session) AudioStreamEnd() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

// ClientAttached is called by the /ws hub when a client connects (true)
// or goes away (false). A session opens only while one is attached.
func (s *Session) ClientAttached(attached bool) {
	s.mu.Lock()
	changed := s.client != attached
	s.client = attached
	if attached {
		s.armed = true
	}
	s.mu.Unlock()
	if changed {
		s.log.Log(logSource, "voice.client", "", map[string]any{"attached": attached})
	}
}

// Speaking implements voice.Session.
func (s *Session) Speaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speakingLocked(s.now())
}

func (s *Session) speakingLocked(now time.Time) bool {
	if s.live == nil {
		return false
	}
	return (!s.lastVoiced.IsZero() && now.Sub(s.lastVoiced) < s.cfg.SpeechHold) ||
		(!s.lastOutText.IsZero() && now.Sub(s.lastOutText) < s.cfg.SpeechHold)
}

// SendAudio implements voice.Session: 16 kHz PCM16 from /ws, resampled
// to 24 kHz. In standby a loud frame from an attached, unmuted, armed
// client opens a session; the frames around it are replayed once it has
// started, so the first words are not lost.
func (s *Session) SendAudio(pcm16 []byte) error {
	if len(pcm16) == 0 {
		return nil
	}
	now := s.now()
	s.mu.Lock()
	switch {
	case !s.started:
		s.mu.Unlock()
		return ErrNotStarted
	case s.closed:
		s.mu.Unlock()
		return ErrClosed
	case s.muted:
		s.mu.Unlock()
		return nil
	}
	pcm := s.up.process(pcm16)
	level := rms(pcm16)
	if level < s.cfg.OpenRMS {
		if s.quietSince.IsZero() {
			s.quietSince = now
		} else if now.Sub(s.quietSince) >= time.Second {
			s.armed = true
		}
	} else {
		s.quietSince = time.Time{}
	}
	lc := s.live
	if lc == nil {
		if s.opening {
			s.bufferLocked(pcm)
		} else {
			s.preroll = append(s.preroll, pcm)
			for n := totalLen(s.preroll); n > SampleRate*2/2 && len(s.preroll) > 1; n = totalLen(s.preroll) {
				s.preroll = s.preroll[1:] // keep ~0.5 s
			}
			if s.client && s.armed && level >= s.cfg.OpenRMS && !now.Before(s.nextOpen) {
				for _, c := range s.preroll {
					s.bufferLocked(c)
				}
				s.preroll = nil
				s.startOpenLocked("audio")
			}
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.sendAudio(lc, pcm)
}

func totalLen(chunks [][]byte) int {
	n := 0
	for _, c := range chunks {
		n += len(c)
	}
	return n
}

func (s *Session) bufferLocked(pcm []byte) {
	s.pending = append(s.pending, pcm)
	s.pendingN += len(pcm)
	for s.pendingN > s.cfg.ReconnectBuffer && len(s.pending) > 0 {
		s.pendingN -= len(s.pending[0])
		s.pending = s.pending[1:]
	}
}

func (s *Session) sendAudio(lc *liveConn, pcm []byte) error {
	lc.lastSend.Store(s.now().UnixNano())
	return s.send(lc, audioAppend{Type: evInputAudioAppend, Audio: base64.StdEncoding.EncodeToString(pcm)})
}

// send writes one client event on lc.
func (s *Session) send(lc *liveConn, ev any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return lc.conn.Send(ctx, ev)
}

// SendText implements voice.Session. GPT-Live has no text input for the
// user's side, so typed text skips the voice model: it goes straight to
// the tool handler (as the model would have relayed it) and the reply is
// pushed to be spoken.
func (s *Session) SendText(text string) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.addRecapLocked("user", text)
	s.lastActivity = s.now()
	s.mu.Unlock()
	s.log.Log(logSource, "voice.text", "", map[string]any{"text": text})
	call := s.classify(text)
	s.startTool(nil, "", call)
	return nil
}

// Push implements voice.Session.
func (s *Session) Push(text string, u voice.Urgency) error { return s.PushTagged(text, u, nil) }

// PushTagged implements supervisor.TagPusher (tags travel onto the
// voice.push event; due_promise / needs_user keep INTERRUPT while the
// model is speaking, like the Gemini session).
func (s *Session) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return ErrNotStarted
	}
	if s.closed {
		return ErrClosed
	}
	s.pushQ = append(s.pushQ, pushItem{text: text, urgency: u, requested: u, tags: tags, at: s.now()})
	return nil
}

// emit delivers an event without blocking.
func (s *Session) emit(ev voice.Event) {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.eventsClosed {
		return
	}
	select {
	case s.events <- ev:
	default:
		s.log.Log(logSource, "voice.event_dropped", "", map[string]any{"event": fmt.Sprintf("%T", ev)})
	}
}

// interrupt drops queued model audio and tells the client to flush.
func (s *Session) interrupt(by string) {
	dropped := s.out.Flush()
	s.log.Log(logSource, "voice.interrupted", "", map[string]any{"by": by, "dropped_chunks": dropped})
	s.emit(voice.EventInterrupted{})
}

// classify maps an utterance to its tool call.
func (s *Session) classify(text string) voice.ToolCall {
	if s.cfg.Classify != nil {
		if call, ok := s.cfg.Classify(text); ok {
			if call.Args == nil {
				call.Args = map[string]any{}
			}
			return call
		}
	}
	return voice.ToolCall{Name: voice.ToolToSupervisor, Args: map[string]any{"text": text}}
}
