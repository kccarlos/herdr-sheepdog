package gemini

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/audiotiming"
)

const (
	logSource = "voice"
	inputMIME = "audio/pcm;rate=16000"
)

// Session implements voice.Session for Gemini Live. Create with New, then
// Start; see the package documentation for the goroutine layout.
type Session struct {
	cfg  Config
	dial Dialer
	log  logging.Logger
	now  func() time.Time
	// rec records the voice context (capture.go); nil records nothing.
	rec *ctxcap.VoiceSession

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

	mu      sync.Mutex // guards everything below
	conn    Conn
	connGen int // bumped on every installed connection
	status  voice.Status
	muted   bool
	// noClient: the /ws hub said no browser is attached
	// (ClientAttached(false)); spoken pushes are held until one is
	// (push.go holdPush). The zero value — never told — does not hold,
	// so a Session used without the hub (voice-test) speaks as before.
	noClient bool
	handle   string // latest resumable handle
	// Resume generations (handle.go "never resume a resumed session"):
	// resumeGen is how many resumes deep the live connection is (0:
	// opened fresh), handleGen the resumeGen of the connection that
	// issued handle, dialGen the handleGen the last dial presented, and
	// freshWhy why the last dial left a handle out ("" or
	// reasonResumedTwice).
	resumeGen, handleGen, dialGen int
	freshWhy                      string
	// handleAt is when the handle was accepted; postHandle the silent
	// pushes sent since (handle.go); lastPromptTokens the prompt size
	// of the latest usage report.
	handleAt         time.Time
	postHandle       []pushItem
	lastPromptTokens int32
	// lastUsage is the latest usage report of the live connection,
	// handleUsage the one current when the accepted handle was issued,
	// resumeCtx a resumed connection waiting for its first report
	// (usage.go).
	lastUsage, handleUsage usageSnap
	resumeCtx              *resumeCtx
	keyIdx                 int
	turnOpen               bool
	turnOpenedAt           time.Time
	// speaking is narrower than turnOpen: true only once the model has
	// actually started producing output (audio or an output transcript
	// fragment) for the open turn, false again on turnComplete or an
	// interruption. turnOpen alone also covers "waiting for a reply"
	// (SendText, a sent push, a tool response), which is not speech.
	speaking     bool
	turnDone     chan struct{} // closed and replaced on every turnComplete
	lastInput    time.Time
	inputDone    bool
	lastSpoken   time.Time
	pending      [][]byte // audio-in buffered during reconnect
	pendingBytes int
	inflight     map[string]context.CancelFunc
	pushQ        []pushItem
	// turn health (turn.go): the open input turn, and per-connection
	// stall / push-timeout counters that decide a recycle
	turn         *turnStats
	turnSeq      int
	stalls       int
	pushTimeouts int
	stalledNow   bool
	// tailGen is bumped by every gate close and every client frame, so a
	// comfort-noise tail stops when the gate reopens (AudioStreamEnd).
	tailGen int
	// audioTiming follows the open model turn's output-audio arrivals
	// (voice.audio_timing, WP: audible gaps investigation); nil between
	// turns. audioTurnSeq numbers turns across the session's lifetime.
	audioTiming  *audiotiming.Tracker
	audioTurnSeq int
	// play follows what the /ws client actually plays (its playback
	// reports, or a timing guess without them); vad is the client's own
	// speech detector. Both are forgotten when the client changes.
	play voice.PlaybackTracker
	// playUtt tags the next playback start with the reply it plays
	// (voice.PlaybackUtterance).
	playUtt voice.PlaybackUtterance
	vad     voice.ClientSpeech
	// echo holds the microphone back while the client plays the model's
	// audio, dropping what the reply explains (voice/echogate.go).
	echo voice.EchoGate
	// recap is the recent conversation (recap.go): what the user
	// said (inText until it is flushed), what the voice said (outText),
	// cut to what was heard, and the spoken pushes. A connection that
	// comes back without its resumption handle is seeded with it.
	recap   voice.Recap
	inText  string
	outText string

	// Session refresh (refresh.go): when the model session last started
	// fresh, when the client went away (zero while one is attached),
	// the refresh due and since when, and whether one is under way
	// (spoken pushes wait for the fresh connection).
	freshAt      time.Time
	clientGoneAt time.Time
	everAttached bool
	refreshDue   string
	refreshDueAt time.Time
	refreshing   bool
	// owedReplies are to_supervisor calls answered "pending", by call
	// id, until the push carrying their reply goes out.
	owedReplies map[string]time.Time
	// openCheck is the supervisor's voice.OpenCheck (nil: none).
	openCheck voice.OpenCheck
	// turnDoneAt is when the model last completed a turn
	// (voice.RequeueHold).
	turnDoneAt time.Time
	// brief is the briefing ledger (briefing.go).
	brief briefLedger

	readerExit chan readerResult
	refreshReq chan refreshRequest
	goAway     chan struct{}
	recycle    chan string
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
	return newSession(cfg, log, newSDKDialer(cfg.Vertex))
}

func newSession(cfg Config, log logging.Logger, dial Dialer) (*Session, error) {
	if len(cfg.APIKeys) == 0 {
		return nil, ErrNoAPIKeys
	}
	if v := cfg.Vertex; v != nil && (v.Project == "" || v.Location == "") {
		return nil, ErrVertexConfig
	}
	cfg = cfg.withDefaults()
	if log == nil {
		log = logging.NopLogger{}
	}
	// Fail fast on a bad prompt (e.g. a malformed cfg.SystemPrompt
	// override); the per-connect render below cannot normally fail once
	// this one has succeeded.
	if _, err := systemPrompt(cfg); err != nil {
		return nil, err
	}
	return &Session{
		cfg:         cfg,
		dial:        dial,
		log:         log,
		now:         time.Now,
		rec:         cfg.Context.Session("gemini", cfg.Model),
		audio:       make(chan []byte),
		events:      make(chan voice.Event, cfg.EventBuffer),
		out:         newOutQueue(),
		status:      voice.StatusConnecting,
		inflight:    map[string]context.CancelFunc{},
		turnDone:    make(chan struct{}),
		readerExit:  make(chan readerResult, 4),
		refreshReq:  make(chan refreshRequest, 1),
		owedReplies: map[string]time.Time{},
		goAway:      make(chan struct{}, 1),
		recycle:     make(chan string, 1),
		kick:        make(chan struct{}, 1),
		dropReq:     make(chan struct{}, 1),
		recap:       voice.Recap{Max: reseedTurns},
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

	s.wg.Add(4)
	go func() { defer s.wg.Done(); s.out.pump(s.ctx, s.audio) }()
	go func() { defer s.wg.Done(); s.pushLoop(s.ctx) }()
	go func() { defer s.wg.Done(); s.run(s.ctx) }()
	go func() { defer s.wg.Done(); s.refreshLoop(s.ctx) }()
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
		if s.turn != nil && s.turn.stallTimer != nil {
			s.turn.stallTimer.Stop()
		}
		s.mu.Unlock()
		s.emit(voice.EventStatus{Status: voice.StatusClosed, Reason: reason})
		s.log.Log(logSource, "voice.closed", "", map[string]any{"reason": reason})
		s.rec.Marker("session closed", "The voice session ended ("+reason+").", nil)
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
	if changed && muted {
		s.audioStreamEnd(conn)
	}
	if changed {
		s.log.Log(logSource, "voice.muted", "", map[string]any{"muted": muted})
	}
}

// ClientAttached is called (through the voice Switcher) by the /ws hub
// when a browser client connects (true) or the last one goes away
// (false). While none is attached, spoken pushes are held in the push
// queue (push.go holdPush) and released, under the usual urgency rules,
// when one attaches. A client back after a long absence makes a session
// refresh due (refresh.go). Logs voice.client on a change.
func (s *Session) ClientAttached(attached bool) {
	s.mu.Lock()
	changed := s.noClient == attached
	s.noClient = !attached
	s.noteAttachLocked(s.now(), attached, changed)
	s.play.Detach()
	s.vad.Reset()
	s.echo.Reset()
	s.mu.Unlock()
	if changed {
		s.log.Log(logSource, "voice.client", "", map[string]any{"attached": attached})
	}
	if attached {
		s.poke()
	}
}

// AudioStreamEnd tells the server the microphone stream paused (the
// client's silence gate closed) so its VAD finalises the current utterance
// instead of waiting for frames that will not come. The next SendAudio
// reopens the stream. No-op while muted or disconnected.
//
// Before the stream end it streams Config.GateTail of comfort noise (a
// ±8 LSB floor, like a real microphone's) at real-time pace: found by
// the audio-e2e harness (2026-09-23), an utterance followed only by the
// client's 300 ms of digital silence and audioStreamEnd was never
// transcribed — Live's activity detection did not see the speech end,
// and the audio was only heard, twice over, once a later stream carried
// a real noise floor. The same tail sent as one burst still left a
// short "stop" unheard while a tool response was pending: the detector
// wants the pause in real time. A frame from the client (the gate
// reopened) cancels the tail and the stream end.
func (s *Session) AudioStreamEnd() {
	s.mu.Lock()
	held, verdict := s.echo.StreamEnd(s.now())
	s.mu.Unlock()
	s.logEchoVerdict(verdict, "stream_end")
	for _, f := range held {
		_ = s.forwardAudio(f)
	}
	s.mu.Lock()
	conn, muted := s.conn, s.muted
	s.tailGen++
	gen := s.tailGen
	live := s.started && s.status != voice.StatusClosed && s.ctx.Err() == nil
	s.mu.Unlock()
	if muted || conn == nil || !live {
		return
	}
	tail := comfortNoise(s.cfg.GateTail)
	if len(tail) == 0 {
		s.audioStreamEnd(conn)
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		const chunk = 16000 * 2 / 10 // 100 ms
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for i := 0; i < len(tail); i += chunk {
			if !s.tailCurrent(gen) {
				return
			}
			if err := s.sendAudio(conn, tail[i:min(i+chunk, len(tail))]); err != nil {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
			}
		}
		if s.tailCurrent(gen) {
			s.audioStreamEnd(conn)
		}
	}()
}

// tailCurrent reports whether the comfort-noise tail gen is still wanted
// (no client frame, mute or newer gate close since).
func (s *Session) tailCurrent(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tailGen == gen && !s.muted
}

// comfortNoise returns d of 16 kHz PCM16 low-level noise (±8 LSB).
func comfortNoise(d time.Duration) []byte {
	n := int(d.Seconds() * 16000)
	if n <= 0 {
		return nil
	}
	b := make([]byte, n*2)
	x := uint32(2463534242)
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		v := int16(int32(x%17) - 8)
		b[2*i], b[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return b
}

func (s *Session) audioStreamEnd(conn Conn) {
	if conn == nil {
		return
	}
	s.sendMu.Lock()
	_ = conn.SendRealtimeInput(genai.LiveRealtimeInput{AudioStreamEnd: true})
	s.sendMu.Unlock()
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
	frames, verdict := s.echo.Mic(s.now(), pcm16)
	s.mu.Unlock()
	s.logEchoVerdict(verdict, "barge_in")
	for _, f := range frames {
		if err := s.forwardAudio(f); err != nil {
			return err
		}
	}
	return nil
}

// forwardAudio sends one microphone frame the echo gate let through
// (buffered while reconnecting).
func (s *Session) forwardAudio(pcm16 []byte) error {
	s.mu.Lock()
	if s.muted || s.status == voice.StatusClosed {
		s.mu.Unlock()
		return nil
	}
	s.tailGen++ // the gate reopened: no comfort tail, no stream end
	conn := s.conn
	if conn == nil {
		s.bufferLocked(pcm16)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	return s.sendAudio(conn, pcm16)
}

// logEchoVerdict logs the echo gate arming (the client's echo showed
// itself) or letting the user through while the reply played
// (voice.echo_gate).
func (s *Session) logEchoVerdict(v *voice.EchoVerdict, why string) {
	if v == nil {
		return
	}
	s.mu.Lock()
	st := s.echo.Stats()
	s.mu.Unlock()
	if v.Armed {
		why = "armed"
	}
	s.log.Log(logSource, "voice.echo_gate", "", map[string]any{
		"open": v.Open, "why": why, "corr": round2(v.Corr), "residual": round2(v.Residual), "lag_ms": v.LagMs,
		"released": v.Released, "withheld_total": st.Withheld, "opens_total": st.Opens,
	})
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// VoiceStats is merged into web.client.stats: the echo gate's counters.
func (s *Session) VoiceStats() map[string]any {
	s.mu.Lock()
	st := s.echo.Stats()
	s.mu.Unlock()
	return map[string]any{"echo_withheld_frames": st.Withheld, "echo_released_frames": st.Released, "echo_opens": st.Opens, "echo_armed": st.Armed}
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
	s.flushRecapLocked()
	s.recap.Add(voice.RecapUser, text)
	s.mu.Unlock()
	s.logTurn(s.noteInput(s.now()))
	s.log.Log(logSource, "voice.text", "", map[string]any{"text": text})
	return nil
}

// logTurn writes a finished turn's voice.turn line (nil payload: nothing).
func (s *Session) logTurn(payload map[string]any) {
	if payload != nil {
		s.log.Log(logSource, "voice.turn", "", payload)
	}
}

// finishAudioTimingLocked closes the in-progress audio-timing tracker (if
// any) and returns its voice.audio_timing payload. Caller holds s.mu.
func (s *Session) finishAudioTimingLocked() map[string]any {
	t := s.audioTiming
	s.audioTiming = nil
	if t == nil {
		return nil
	}
	return t.Finish()
}

// logAudioTiming writes a finished turn's voice.audio_timing line (nil
// payload: nothing).
func (s *Session) logAudioTiming(payload map[string]any) {
	if payload != nil {
		s.log.Log(logSource, "voice.audio_timing", "", payload)
	}
}

// Push implements voice.Session by enqueuing; see push.go for the policy.
func (s *Session) Push(text string, u voice.Urgency) error {
	return s.PushTagged(text, u, nil)
}

// PushTagged implements supervisor.TagPusher: the same push, with tags
// (answers_job, due_promise, needs_user) carried onto its voice.push
// event. An INTERRUPT item is downgraded to WHEN_IDLE in the queue
// (push.go's peekPush) while the model is speaking, unless a tag
// exempts it; queueing it here at the requested urgency lets that
// decision be made at send time, when Speaking() is current.
func (s *Session) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.status == voice.StatusClosed {
		s.mu.Unlock()
		return ErrClosed
	}
	if voice.IsFocus(tags) {
		// Kept for a fresh connection's re-seed as well (reseed.go).
		s.recap.SetFocus(text)
		if s.replaceFocusLocked(text, u, tags) {
			s.mu.Unlock()
			return nil
		}
	}
	s.pushQ = append(s.pushQ, pushItem{text: text, urgency: u, requested: u, tags: tags, at: s.now()})
	s.mu.Unlock()
	s.poke()
	return nil
}

// replaceFocusLocked puts a new current-focus line in the place of one
// still queued (silent context waits while the voice speaks, push.go
// silentWaitLocked): only the latest focus is worth sending. The names
// line needs no such step: the supervisor debounces it and never sends
// the same line twice. Caller holds s.mu.
func (s *Session) replaceFocusLocked(text string, u voice.Urgency, tags map[string]any) bool {
	for i := range s.pushQ {
		if voice.IsFocus(s.pushQ[i].tags) {
			s.pushQ[i].text, s.pushQ[i].urgency, s.pushQ[i].requested, s.pushQ[i].tags = text, u, u, tags
			return true
		}
	}
	return false
}

// Reset implements the optional voice.Session Reset: it drops the
// resumption handle and reconnects, so the fresh connection starts with
// no memory of the conversation so far ("New conversation",
// docs/roles.md). The supervisor re-learns the roster on the resulting
// EventResumed{ContextLost:true} the same way it does after any other
// context-losing reconnect. No-op before Start or after Close.
func (s *Session) Reset(reason string) error {
	s.mu.Lock()
	live := s.started && s.status != voice.StatusClosed
	s.mu.Unlock()
	if !live {
		return nil
	}
	s.dropHandle(reason)
	s.mu.Lock()
	s.recap.Reset() // a new conversation must not be re-seeded with the old one
	s.inText, s.outText = "", ""
	// Nor given the old conversation's briefings again (reseed.go).
	s.brief.recs, s.brief.cut = nil, ""
	s.mu.Unlock()
	s.rec.Marker("conversation reset", "New conversation: the resumption handle is dropped, so the next connection starts without the conversation so far.", nil)
	select {
	case s.dropReq <- struct{}{}:
	default:
	}
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

// Speaking implements voice.Session: true once the model has actually
// started producing output for the open turn (see the speaking field),
// and on until the client has finished playing it (ClientPlayback; a
// timing guess without playback reports).
func (s *Session) Speaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speakingLocked(s.now())
}

// speakingLocked is Speaking under s.mu.
func (s *Session) speakingLocked(now time.Time) bool {
	if s.speaking {
		return true
	}
	playing, _ := s.play.Playing(now)
	return playing
}

// ClientPlayback applies a /ws client playback report
// (docs/ws-contract.md "Playback reports"): it logs voice.playback,
// wakes the push queue (a drained reply may release a push), and when a
// flushed run was cut short emits the heard part as a cut transcript
// for the orchestrator.
func (s *Session) ClientPlayback(state string, playedMs int) {
	s.mu.Lock()
	now := s.now()
	rep := s.play.Report(now, state, playedMs)
	s.echo.Playback(now, state)
	utt := s.playUtt.Started(state)
	if rep.Cut != nil {
		s.flushRecapLocked()
		s.recap.Cut(rep.Cut.Full, rep.Heard, rep.Cut.HeardMs)
	}
	var cutFact string
	if state == voice.PlaybackFlushed && s.useBriefings() {
		cutFact = s.noteCutLocked(rep)
	}
	s.mu.Unlock()
	if cutFact != "" {
		// The voice cannot see the phone: tell it what the user heard
		// of the line they talked over (a fact, not a re-queue).
		_ = s.PushTagged(cutFact, voice.UrgencySilent, map[string]any{"kind": "delivery", "from": "daemon"})
	}
	s.log.Log(logSource, "voice.playback", "", voice.TagPayload(rep.Payload(), utt))
	if rep.Cut != nil {
		s.emit(voice.EventTranscript{Dir: voice.DirOut, Text: rep.Heard, Final: true, Cut: rep.Cut})
	}
	s.poke()
}

// LastPlayback is the client's most recent playback report.
func (s *Session) LastPlayback() voice.PlaybackReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.play.Last()
}

// LastSpeechOnset is when the client's speech detector last opened (ok
// false when it never reported one): the echo guard tells the
// user's answer, begun after the voice stopped playing, from its
// echo, begun while it played.
func (s *Session) LastSpeechOnset() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vad.Onset()
}

// ClientSpeaking records the /ws client's speech detector opening (true)
// or closing (false); push timing holds for it (speakingUntilLocked).
func (s *Session) ClientSpeaking(on bool) {
	s.mu.Lock()
	s.vad.Set(s.now(), on)
	s.mu.Unlock()
	s.poke()
}
