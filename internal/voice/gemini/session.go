package gemini

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

const (
	logSource = "voice"
	inputMIME = "audio/pcm;rate=16000"
)

// Session implements voice.Session for Gemini Live. Create with New, then
// Start; see the package documentation for the goroutine layout.
type Session struct {
	cfg    Config
	dial   Dialer
	log    logging.Logger
	prompt string
	now    func() time.Time

	audio  chan []byte
	events chan voice.Event
	out    *outQueue

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool
	once    sync.Once

	emitMu       sync.RWMutex // guards eventsClosed vs. sends on events
	eventsClosed bool

	sendMu sync.Mutex // serialises every Send* on the current conn

	mu           sync.Mutex // guards everything below
	conn         Conn
	connGen      int // bumped on every installed connection
	status       voice.Status
	muted        bool
	handle       string // latest resumable handle
	keyIdx       int
	turnOpen     bool
	turnOpenedAt time.Time
	lastInput    time.Time
	inputDone    bool
	lastSpoken   time.Time
	pending      [][]byte // audio-in buffered during reconnect
	pendingBytes int
	inflight     map[string]context.CancelFunc
	pushQ        []pushItem

	readerExit chan readerResult
	goAway     chan struct{}
	kick       chan struct{}
	dropReq    chan struct{}
}

type readerResult struct {
	gen int
	err error
}

// New builds a Session. log may be nil (events are still delivered on
// Events()). It does not connect; call Start.
func New(cfg Config, log logging.Logger) (*Session, error) {
	return newSession(cfg, log, newSDKDialer())
}

func newSession(cfg Config, log logging.Logger, dial Dialer) (*Session, error) {
	if len(cfg.APIKeys) == 0 {
		return nil, ErrNoAPIKeys
	}
	cfg = cfg.withDefaults()
	if log == nil {
		log = logging.NopLogger{}
	}
	prompt, err := systemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	return &Session{
		cfg:        cfg,
		dial:       dial,
		log:        log,
		prompt:     prompt,
		now:        time.Now,
		audio:      make(chan []byte),
		events:     make(chan voice.Event, cfg.EventBuffer),
		out:        newOutQueue(),
		status:     voice.StatusConnecting,
		inflight:   map[string]context.CancelFunc{},
		readerExit: make(chan readerResult, 4),
		goAway:     make(chan struct{}, 1),
		kick:       make(chan struct{}, 1),
		dropReq:    make(chan struct{}, 1),
	}, nil
}

// Start connects (rotating keys on rate limits) and launches the
// background goroutines. It returns the connect error when every key is
// exhausted or the dial fails for another reason.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("gemini: Start called twice")
	}
	s.started = true
	s.mu.Unlock()

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.emit(voice.EventStatus{Status: voice.StatusConnecting})
	conn, resumed, err := s.connect(s.ctx)
	if err != nil {
		s.cancel()
		s.finish("error")
		return err
	}
	s.install(conn, resumed, false)

	s.wg.Add(3)
	go func() { defer s.wg.Done(); s.out.pump(s.ctx, s.audio) }()
	go func() { defer s.wg.Done(); s.pushLoop(s.ctx) }()
	go func() { defer s.wg.Done(); s.run(s.ctx) }()
	return nil
}

// Close ends the session: cancels in-flight tool calls, closes the
// connection, waits for every goroutine and closes Audio and Events.
func (s *Session) Close() error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	s.cancel()
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	s.wg.Wait()
	s.finish("shutdown")
	return nil
}

// finish emits the final status and closes Events exactly once. The run
// loop's goroutines must be done before it is called.
func (s *Session) finish(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.status = voice.StatusClosed
		for _, cancel := range s.inflight {
			cancel()
		}
		s.mu.Unlock()
		s.emit(voice.EventStatus{Status: voice.StatusClosed, Reason: reason})
		s.log.Log(logSource, "voice.closed", "", map[string]any{"reason": reason})
		s.emitMu.Lock()
		s.eventsClosed = true
		close(s.events)
		s.emitMu.Unlock()
	})
}

// Audio implements voice.Session.
func (s *Session) Audio() <-chan []byte { return s.audio }

// Events implements voice.Session.
func (s *Session) Events() <-chan voice.Event { return s.events }

// SetMuted implements voice.Session. Muting drops SendAudio frames; the
// server is told the audio stream ended so its VAD does not wait.
func (s *Session) SetMuted(muted bool) {
	s.mu.Lock()
	changed := s.muted != muted
	s.muted = muted
	conn := s.conn
	s.mu.Unlock()
	if changed && muted && conn != nil {
		s.sendMu.Lock()
		_ = conn.SendRealtimeInput(genai.LiveRealtimeInput{AudioStreamEnd: true})
		s.sendMu.Unlock()
	}
	if changed {
		s.log.Log(logSource, "voice.muted", "", map[string]any{"muted": muted})
	}
}

// Muted reports the current mute state.
func (s *Session) Muted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.muted
}

// Status reports the current connection status.
func (s *Session) Status() voice.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// SendAudio implements voice.Session. Frames are dropped while muted and
// buffered (bounded) while reconnecting.
func (s *Session) SendAudio(pcm16 []byte) error {
	if len(pcm16) == 0 {
		return nil
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.status == voice.StatusClosed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.muted {
		s.mu.Unlock()
		return nil
	}
	conn := s.conn
	if conn == nil {
		s.bufferLocked(pcm16)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.sendAudio(conn, pcm16)
}

func (s *Session) sendAudio(conn Conn, pcm16 []byte) error {
	chunk := make([]byte, len(pcm16))
	copy(chunk, pcm16)
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return conn.SendRealtimeInput(genai.LiveRealtimeInput{Audio: &genai.Blob{MIMEType: inputMIME, Data: chunk}})
}

// bufferLocked appends audio to the reconnect buffer, dropping the oldest
// chunks past the configured size. Caller holds s.mu.
func (s *Session) bufferLocked(pcm16 []byte) {
	chunk := make([]byte, len(pcm16))
	copy(chunk, pcm16)
	s.pending = append(s.pending, chunk)
	s.pendingBytes += len(chunk)
	for s.pendingBytes > s.cfg.ReconnectBuffer && len(s.pending) > 0 {
		s.pendingBytes -= len(s.pending[0])
		s.pending = s.pending[1:]
	}
}

// SendText implements voice.Session: the text is delivered as realtime
// input, which the model treats like a spoken utterance.
func (s *Session) SendText(text string) error {
	conn, err := s.currentConn()
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	err = conn.SendRealtimeInput(genai.LiveRealtimeInput{Text: text})
	s.sendMu.Unlock()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.openTurnLocked()
	s.mu.Unlock()
	s.log.Log(logSource, "voice.text", "", map[string]any{"text": text})
	return nil
}

// Push implements voice.Session by enqueuing; see push.go for the policy.
func (s *Session) Push(text string, u voice.Urgency) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.status == voice.StatusClosed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.pushQ = append(s.pushQ, pushItem{text: text, urgency: u})
	s.mu.Unlock()
	s.poke()
	return nil
}

// DropConnection forcibly closes the current connection without ending
// the session, to exercise the resumption path (voice-test --drop-after).
func (s *Session) DropConnection() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		s.log.Log(logSource, "voice.drop", "", nil)
		_ = conn.Close()
	}
}

// currentConn returns the live connection or the reason there is none.
func (s *Session) currentConn() (Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.started:
		return nil, ErrNotStarted
	case s.status == voice.StatusClosed:
		return nil, ErrClosed
	case s.conn == nil:
		return nil, ErrNotConnected
	}
	return s.conn, nil
}

// openTurnLocked marks that the model is (about to be) generating so idle
// pushes wait for turnComplete. Caller holds s.mu.
func (s *Session) openTurnLocked() {
	s.turnOpen = true
	s.turnOpenedAt = s.now()
}

// emit delivers an event without ever blocking the caller; a full buffer
// drops the event and logs the fact.
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

// poke wakes the push loop.
func (s *Session) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// turnOpenNow reports whether the model is considered mid-turn (tests).
func (s *Session) turnOpenNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnOpen
}

// TurnOpen reports whether the model is considered mid-turn: audio or a
// transcript arrived, or a push/text/tool response was sent, and no
// turnComplete has followed yet.
func (s *Session) TurnOpen() bool { return s.turnOpenNow() }
