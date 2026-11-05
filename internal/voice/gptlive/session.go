package gptlive

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/audiotiming"
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
	// rec records the voice context (capture.go); nil records nothing.
	rec *ctxcap.VoiceSession

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
	// lastLoud / loudSince bound the current run of loud microphone
	// frames: the user may be mid-sentence before any transcript
	// arrives, so the idle close waits for it (unless the "speech" has
	// gone on too long to be anything but room noise).
	lastLoud  time.Time
	loudSince time.Time
	up        upsampler

	live    *liveConn // the open, started session; nil in standby
	opening bool
	// resets counts Reset calls, so an open in flight can tell it was
	// reset (live.go's open).
	resets   int
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
	// settledText is the last finished utterance no delegation claimed,
	// kept briefly for a delegation that arrives after it settled.
	settledText string
	settledAt   time.Time
	// turnDoneOwed: the model's speech ended while the user was
	// talking; EventTurnComplete is emitted when the utterance settles
	// (it closes the supervisor's voice input, which must not split the
	// user's words).
	turnDoneOwed bool
	inText       string // the current input utterance (recap)
	outText      string // the current reply (recap)
	recap        voice.Recap
	// recapCarried: the recap came from another provider (SeedRecap);
	// the next session's history says so once.
	recapCarried bool
	held         []string // silent context received in standby
	pushQ        []pushItem
	lastSpoken   time.Time
	inflight     map[string]context.CancelFunc
	toolSeq      int
	billed       float64 // seconds of closed sessions
	sessions     int
	outBytes     int64
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
	// heard watches each spoken push until its speech is over, judged
	// by key terms (heard.go); requeue holds the owed unheard lines
	// waiting for a quiet moment; openCheck is the supervisor's.
	heard   voice.HeardTracker
	requeue voice.Requeue
	// turnDoneAt is when the model's speech last ended (voice.RequeueHold).
	turnDoneAt time.Time
	openCheck  voice.OpenCheck
	flushBy    string
	flushAt    time.Time
}

// outItemSpan tracks the active assistant output item's server-generated
// audio so conversation.item.truncate never exceeds server_audio_ms.
type outItemSpan struct {
	runID        uint32
	itemID       string
	contentIndex int
	serverBytes  int
	startedAt    time.Time
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
	pace     inputPacer    // input audio sent vs the wall clock (pace.go)

	// under Session.mu
	usage         float64
	closeWhy      string // our reason, when we asked to close
	finalized     bool
	startedOnce   sync.Once
	outItem       *outItemSpan
	awaitingItems []outItemSpan
	truncatedID   string
	truncSeq      int
}

// pushItem is one queued Push.
type pushItem struct {
	text      string
	urgency   voice.Urgency
	requested voice.Urgency
	tags      map[string]any
	at        time.Time
	// heldAt is when the push was first held for want of a listener
	// (zero: never held); see pumpPushes.
	heldAt time.Time
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
	s := &Session{
		cfg: cfg, dial: dial, log: log, now: time.Now, rec: cfg.Context.Session("openai", cfg.Model),
		audio:    make(chan []byte),
		events:   make(chan voice.Event, cfg.EventBuffer),
		out:      newOutQueue(),
		inflight: map[string]context.CancelFunc{},
		armed:    true,
		recap:    voice.Recap{Max: cfg.RecapTurns},
	}
	s.heard.Coverage = s.keyTermCoverage
	return s, nil
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
		s.rec.Marker("voice closed", "The voice session ended (shutdown).", nil)
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
	s.play.Detach()
	s.vad.Reset()
	s.mu.Unlock()
	if changed {
		s.log.Log(logSource, "voice.client", "", map[string]any{"attached": attached})
	}
}

// Reset implements the optional voice.Session Reset: the recap and any
// silent context held for the next session are dropped, so a session
// that reopens later (on the next speech or push) starts with no memory
// of the conversation so far ("New conversation", docs/dev/roles.md). An
// open billed session is closed now (session.close, no recap); a
// standby one simply loses its recap. No-op before Start or after
// Close.
func (s *Session) Reset(reason string) error {
	s.mu.Lock()
	if !s.started || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.recap.Reset()
	s.held = nil
	s.inputOpen = false
	s.inText, s.outText = "", ""
	s.unclaimed, s.settledText = "", ""
	s.heard.Reset()
	s.requeue.Reset()
	// An open in flight carries the dropped recap and audio: it checks
	// resets and closes its connection; audio held for it goes too.
	s.resets++
	s.pending, s.pendingN = nil, 0
	lc := s.live
	s.mu.Unlock()
	if lc != nil {
		s.closeLive(lc, "reset")
	}
	s.log.Log(logSource, "voice.reset", "", map[string]any{"reason": reason, "had_session": lc != nil})
	s.rec.Marker("conversation reset", "New conversation: the recap and the held context are dropped; the next session starts without the conversation so far.", nil)
	return nil
}

// Speaking implements voice.Session: the model is producing speech
// (speakingLocked) or the client is still playing it (ClientPlayback; a
// timing guess without playback reports).
func (s *Session) Speaking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speakingOrPlayingLocked(s.now())
}

// speakingOrPlayingLocked is Speaking under s.mu: what push timing uses.
func (s *Session) speakingOrPlayingLocked(now time.Time) bool {
	if s.speakingLocked(now) {
		return true
	}
	playing, _ := s.play.Playing(now)
	return playing
}

func buildTruncateLocked(lc *liveConn, span outItemSpan, playedMs int) *truncateEvent {
	if lc == nil || span.itemID == "" || span.serverBytes <= 0 || span.itemID == lc.truncatedID {
		return nil
	}
	serverAudioMs := span.serverBytes / (SampleRate * 2 / 1000)
	endMs := playedMs
	if endMs < 0 {
		endMs = 0
	}
	if endMs > serverAudioMs {
		endMs = serverAudioMs
	}
	lc.truncatedID = span.itemID
	lc.truncSeq++
	return &truncateEvent{
		Type:         evItemTruncate,
		EventID:      fmt.Sprintf("trunc_%d_%d", lc.seq, lc.truncSeq),
		ItemID:       span.itemID,
		ContentIndex: span.contentIndex,
		AudioEndMS:   endMs,
	}
}

// NoteAudioRun tags the current playback and push-watch run with runID
// when the /ws hub starts delivering a new spoken run.
func (s *Session) NoteAudioRun(runID uint32) {
	s.mu.Lock()
	s.play.SetRunID(runID)
	s.heard.SetRunID(runID)
	if s.live != nil && s.live.outItem != nil && s.live.outItem.runID == 0 {
		s.live.outItem.runID = runID
	}
	s.mu.Unlock()
}

// AssignFlushedRun tags an untagged awaiting run when a flush arrived at
// the /ws hub before any audio chunk of that run left the session.
func (s *Session) AssignFlushedRun(next func() uint32) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.play.AssignAwaitingRunID(next)
	if id != 0 {
		s.heard.TagLastCutRun(id)
		if s.live != nil {
			if n := len(s.live.awaitingItems); n > 0 && s.live.awaitingItems[n-1].runID == 0 {
				s.live.awaitingItems[n-1].runID = id
			} else if s.live.outItem != nil && s.live.outItem.runID == 0 {
				s.live.outItem.runID = id
			}
		}
	}
	return id
}

// ClientPlayback applies an untagged /ws client playback report.
func (s *Session) ClientPlayback(state string, playedMs int) {
	s.ClientPlaybackRun(state, playedMs, 0)
}

// ClientPlaybackRun applies a /ws client playback report optionally tagged
// with runID (docs/dev/ws-contract.md "Playback reports"): it logs
// voice.playback and, when a flushed run was cut short, truncates the live
// server item, trims the recap, and emits the heard part as a cut
// transcript for the orchestrator.
func (s *Session) ClientPlaybackRun(state string, playedMs int, runID uint32) {
	s.mu.Lock()
	now := s.now()
	rep := s.play.ReportRun(now, state, playedMs, runID)
	utt := s.playUtt.Started(state)
	var unheard []voice.UnheardPush
	var trunc *truncateEvent
	var truncConn *liveConn
	switch state {
	case voice.PlaybackDrained:
		if s.live != nil && (runID == 0 || s.live.outItem == nil || s.live.outItem.runID == 0 || s.live.outItem.runID == runID) {
			s.live.outItem = nil
		}
	case voice.PlaybackFlushed:
		if lc := s.live; lc != nil && s.lcActiveLocked(lc) {
			if runID != 0 {
				matched := false
				for i := range lc.awaitingItems {
					if lc.awaitingItems[i].runID == runID {
						span := lc.awaitingItems[i]
						lc.awaitingItems = lc.awaitingItems[i+1:]
						trunc, truncConn = buildTruncateLocked(lc, span, playedMs), lc
						matched = true
						break
					}
				}
				if !matched && lc.outItem != nil && (lc.outItem.runID == 0 || lc.outItem.runID == runID) && rep.RunMs > 0 {
					span := *lc.outItem
					lc.outItem = nil
					trunc, truncConn = buildTruncateLocked(lc, span, playedMs), lc
				}
			} else if len(lc.awaitingItems) > 0 {
				var span outItemSpan
				span, lc.awaitingItems = lc.awaitingItems[0], lc.awaitingItems[1:]
				trunc, truncConn = buildTruncateLocked(lc, span, playedMs), lc
			} else if lc.outItem != nil {
				span := *lc.outItem
				lc.outItem = nil
				trunc, truncConn = buildTruncateLocked(lc, span, playedMs), lc
			}
		}
		unheard = s.heard.FlushedRun(now, rep.Cut, rep.Heard, runID)
		if rep.Cut != nil {
			rep.Cut.UtteranceID = s.playUtt.Run()
			rep.Cut.ByPush = s.flushBy == "push" && now.Sub(s.flushAt) < 10*time.Second
			s.flushReplyLocked()
			s.recap.Cut(rep.Cut.Full, rep.Heard, rep.Cut.HeardMs)
		}
	}
	s.mu.Unlock()
	if trunc != nil && truncConn != nil {
		if err := s.send(truncConn, *trunc); err == nil {
			s.log.Log(logSource, "voice.item_truncated", "", map[string]any{
				"item_id": trunc.ItemID, "content_index": trunc.ContentIndex,
				"audio_end_ms": trunc.AudioEndMS, "session_id": truncConn.id,
			})
		}
	}
	s.reportUnheard(unheard)
	s.log.Log(logSource, "voice.playback", "", voice.TagPayload(rep.Payload(), utt))
	if rep.Cut != nil {
		s.emit(voice.EventTranscript{Dir: voice.DirOut, Text: rep.Heard, Final: true, Cut: rep.Cut})
	}
}

// LastPlayback is the client's most recent playback report.
func (s *Session) LastPlayback() voice.PlaybackReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.play.Last()
}

// LastSpeechOnset is when the client's speech detector last opened (ok
// false when it never reported one; see gemini.Session.LastSpeechOnset).
func (s *Session) LastSpeechOnset() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vad.Onset()
}

// ClientSpeaking records the /ws client's speech detector opening (true)
// or closing (false); pumpPushes holds spoken pushes for it.
func (s *Session) ClientSpeaking(on bool) {
	s.mu.Lock()
	s.vad.Set(s.now(), on)
	s.mu.Unlock()
}

// speakingLocked is whether the model is producing speech: voiced
// output audio or an output transcript within SpeechHold. It is the
// generation state alone — the synthesised end of the model's turn
// (speechEnd) and the idle close follow it, not the client's playback.
func (s *Session) speakingLocked(now time.Time) bool {
	if s.live == nil {
		return false
	}
	return (!s.lastVoiced.IsZero() && now.Sub(s.lastVoiced) < s.cfg.SpeechHold) ||
		(!s.lastOutText.IsZero() && now.Sub(s.lastOutText) < s.cfg.SpeechHold)
}

// No echo gate here (voice/echogate.go, #113, is Gemini's). Its audio
// path differs where the gate depends on it:
//   - The gate arms first on the model cutting its own reply right after
//     it starts (two self-interruptions). GPT-Live reports no
//     interruption: the session flushes only for a stop or an
//     interrupting push, so that signal never comes, and the gate would
//     arm only on the slower transcript or audio match.
//   - GPT-Live wants a continuous input stream paced in real time
//     (pace.go). Frames the gate withholds would be paid for as
//     keep-alive silence, and held frames released on a barge-in would
//     then arrive as lead; the pacer would have to count withheld audio
//     as sent first.
//   - The bench runs Gemini only, so arming, false openings and barge-in
//     latency on GPT-Live could not be measured.
//
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
		if s.lastLoud.IsZero() || now.Sub(s.lastLoud) > time.Second {
			s.loudSince = now
		}
		s.lastLoud = now
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
	return s.sendAudio(lc, pcm, srcClient)
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

// sendAudio appends pcm (24 kHz PCM16) to lc's input and records it on
// the pacer (pace.go).
func (s *Session) sendAudio(lc *liveConn, pcm []byte, src audioSrc) error {
	lc.pace.add(s.now(), len(pcm), src)
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
	s.rec.Marker("typed text (not sent to the model)", "GPT-Live has no text input: "+strconv.Quote(text)+" goes straight to the supervisor, and its reply is pushed.", nil)
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
	if voice.IsFocus(tags) {
		// The next billed session opens with it (recapItemsLocked).
		s.recap.SetFocus(text)
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

// interrupt drops queued model audio and tells the client to flush.
func (s *Session) interrupt(by string) {
	s.mu.Lock()
	now := s.now()
	s.flushBy, s.flushAt = by, now
	s.flushReplyLocked()
	runID, awaited := s.play.FlushRun(now)
	var trunc *truncateEvent
	var truncConn *liveConn
	if lc := s.live; lc != nil && s.lcActiveLocked(lc) && lc.outItem != nil {
		span := *lc.outItem
		if span.runID == 0 {
			span.runID = runID
		}
		lc.outItem = nil
		if s.play.Reports() && awaited {
			lc.awaitingItems = append(lc.awaitingItems, span)
			if len(lc.awaitingItems) > 4 {
				lc.awaitingItems = lc.awaitingItems[len(lc.awaitingItems)-4:]
			}
		} else {
			estMs := int(now.Sub(span.startedAt).Milliseconds())
			trunc, truncConn = buildTruncateLocked(lc, span, estMs), lc
		}
	}
	unheard := s.heard.InterruptedRun(now, by, awaited, runID)
	s.mu.Unlock()
	if trunc != nil && truncConn != nil {
		if err := s.send(truncConn, *trunc); err == nil {
			s.log.Log(logSource, "voice.item_truncated", "", map[string]any{
				"item_id": trunc.ItemID, "content_index": trunc.ContentIndex,
				"audio_end_ms": trunc.AudioEndMS, "session_id": truncConn.id,
			})
		}
	}
	s.reportUnheard(unheard)
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
