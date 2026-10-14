package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

const (
	wsReadLimit    = 1 << 20
	wsWriteTimeout = 5 * time.Second
	// backpressureEvery throttles web.client.backpressure events: one for
	// the first drop, then one per this many.
	backpressureEvery = 100
)

// clientAttacher is implemented by sessions that care whether a client
// is listening (GPT-Live opens a billed session only for one).
type clientAttacher interface{ ClientAttached(bool) }

// voiceStatser is implemented by sessions with counters for
// web.client.stats (GPT-Live's billed seconds).
type voiceStatser interface{ VoiceStats() map[string]any }

// audioEnder is implemented by sessions that can tell the model the
// microphone stream paused (Gemini: audioStreamEnd), so its VAD closes the
// user's turn when the browser's silence gate stops sending frames.
type audioEnder interface{ AudioStreamEnd() }

// playbackReporter takes the client's playback reports
// (docs/ws-contract.md "Playback reports"): the session keeps "the
// model is speaking" true until the client has played the reply, and
// truncates a cut-off reply's transcript to what was heard.
type playbackReporter interface {
	ClientPlayback(state string, playedMs int)
}

// speechReporter takes the client's own speech detector (a speaking
// message, or its silence gate read the same way) for push timing.
type speechReporter interface{ ClientSpeaking(on bool) }

// tagPusher is a session that carries tags onto a push
// (supervisor.TagPusher).
type tagPusher interface {
	PushTagged(text string, u voice.Urgency, tags map[string]any) error
}

// hub owns the single browser client and the daemon's view of the voice
// state (docs/ws-contract.md).
type hub struct {
	s *Server

	mu      sync.Mutex
	client  *client
	seq     int
	status  voice.Status
	reason  string
	stalled bool
	muted   bool
	// unheard: model audio is being dropped for want of a client in the
	// current run (web.audio_unheard is logged once per run; a client
	// attaching or the model's turn completing starts a new run).
	unheard bool
}

func newHub(s *Server) *hub {
	h := &hub{s: s, status: voice.StatusConnecting}
	if s.deps.Voice == nil {
		h.status, h.reason = voice.StatusClosed, "voice_off"
	} else if st, ok := s.deps.Voice.(interface{ Status() voice.Status }); ok {
		h.status = st.Status()
	}
	return h
}

// hubState is the daemon's view of the voice side for /api/state.
type hubState struct {
	status  voice.Status
	reason  string
	muted   bool
	stalled bool
	client  bool
}

// state returns the voice status, its reason, the mute and stalled flags
// and whether a browser client is attached.
func (h *hub) state() hubState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hubState{status: h.status, reason: h.reason, muted: h.muted, stalled: h.stalled, client: h.client != nil}
}

func (h *hub) statusMsg() wsproto.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := wsproto.Status(h.status.String(), h.muted, h.reason)
	m.Stalled = h.stalled
	if p := h.s.voiceProvider(); p != "off" {
		m.Provider = p
	}
	return m
}

// closedStatus is a status message announcing the connection is closing
// (a new client replaced it, or the daemon is shutting down). It carries
// the hub's real current mute state rather than a hard-coded one, so a
// status message never claims "unmuted" while the hub is actually muted,
// or vice versa (audit B2).
func (h *hub) closedStatus(reason string) wsproto.Message {
	h.mu.Lock()
	muted := h.muted
	h.mu.Unlock()
	return wsproto.Status(wsproto.VoiceClosed, muted, reason)
}

// handle upgrades the request and serves the client until it goes away.
// A new client replaces the current one (contract: single client).
func (h *hub) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.s.log.Log(logSource, "web.client.reject", "", map[string]any{"remote": r.RemoteAddr, "error": err.Error()})
		return
	}
	conn.SetReadLimit(wsReadLimit)
	c := newClient(h, conn, r.RemoteAddr, h.s.deps.OutQueue)

	h.mu.Lock()
	h.seq++
	c.id = h.seq
	busy := h.client != nil
	h.mu.Unlock()

	// A new client replaces the attached one only once its handshake is
	// accepted (handshake.go): a client about to be refused for its
	// protocol must not kick a working one off. With nobody attached
	// there is nobody to kick, so it attaches at once, as before.
	replaced := false
	if busy {
		c.mu.Lock()
		c.pendingAttach = true
		c.mu.Unlock()
	} else {
		replaced = h.attach(c)
	}
	h.s.log.Log(logSource, "web.client.connect", "", map[string]any{"client_id": c.id, "remote": c.remote, "replaced": replaced, "pending": busy})

	// The contract: a status message before any binary frame — sent at
	// once, before the handshake, so a legacy client is served exactly
	// as before. A pending client is told it starts unmuted (it will, on
	// attach) rather than the attached client's mute.
	st := h.statusMsg()
	if busy {
		unmuted := false
		st.Muted = &unmuted
	}
	first := controlFrame(st)
	c.mu.Lock()
	c.firstStatus = first.data
	c.mu.Unlock()
	c.enqueue(first)
	if !busy {
		h.sendAccounts(c)
	}
	c.startHandshake(h.s.deps.HandshakeTimeout)
	go c.writeLoop()
	go c.statsLoop(h.s.deps.StatsInterval)
	reason := c.readLoop(r.Context())
	c.stopHandshake()
	c.finish(reason)

	h.mu.Lock()
	c.gone = true
	last := h.client == c
	if last {
		h.client = nil
	}
	h.mu.Unlock()
	if ca, ok := h.s.deps.Voice.(clientAttacher); ok && last {
		ca.ClientAttached(false)
	}
}

// attach makes c the attached client, replacing (and closing) the one
// before it, and reports whether there was one. A client whose
// connection already ended is not attached.
func (h *hub) attach(c *client) bool {
	h.mu.Lock()
	if c.gone || h.client == c {
		h.mu.Unlock()
		return false
	}
	old := h.client
	h.client = c
	// docs/ws-contract.md: "reconnecting clients start unmuted" — a new
	// client's mic is never silenced by whatever the previous client left
	// behind (audit B2).
	h.muted = false
	h.unheard = false
	h.mu.Unlock()
	if h.s.deps.Voice != nil {
		h.s.deps.Voice.SetMuted(false)
	}
	if old != nil {
		old.enqueue(controlFrame(h.closedStatus("replaced")))
		old.closeAfterFlush(websocket.StatusNormalClosure, "replaced")
	}
	if ca, ok := h.s.deps.Voice.(clientAttacher); ok {
		ca.ClientAttached(true)
	}
	return old != nil
}

// current returns the attached client, if any.
func (h *hub) current() *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.client
}

// sendAudio forwards a model audio chunk to the client, if any. Audio
// with no client attached is dropped (docs/ws-contract.md "Reconnect
// behaviour": the conversation is real time), but not silently: the
// first chunk of each unheard run logs web.audio_unheard, so the log
// shows the model spoke to nobody (the turn's voice.audio_timing has
// the length). Spoken pushes are held by the voice session for this
// reason; what remains here is the model's own answer to audio it
// heard before the page went away.
func (h *hub) sendAudio(chunk []byte) {
	seq := h.s.audio.Arrived(h.s.deps.Now(), len(chunk))
	h.mu.Lock()
	c := h.client
	first := c == nil && !h.unheard
	if c == nil {
		h.unheard = true
	}
	h.mu.Unlock()
	if first {
		h.s.log.Log(logSource, "web.audio_unheard", "", map[string]any{"bytes": len(chunk), "provider": h.s.voiceProvider()})
	}
	if c == nil {
		return
	}
	c.enqueue(frame{binary: true, data: chunk, audioSeq: seq})
}

// send delivers a control message to the client, if any.
func (h *hub) send(m wsproto.Message) {
	if c := h.current(); c != nil {
		c.enqueue(controlFrame(m))
	}
}

// onVoiceEvent mirrors session events to the client.
func (h *hub) onVoiceEvent(ev voice.Event) {
	switch e := ev.(type) {
	case voice.EventStatus:
		h.mu.Lock()
		h.status, h.reason, h.stalled = e.Status, e.Reason, e.Stalled
		h.mu.Unlock()
		h.send(h.statusMsg())
		h.voiceConnected(e)
	case voice.EventInterrupted:
		h.flush()
	case voice.EventTurnComplete:
		h.s.audio.EndRun()
		h.mu.Lock()
		h.unheard = false // the next unheard audio is another turn
		h.mu.Unlock()
	case voice.EventTranscript:
		if e.Cut != nil {
			return // a correction for the orchestrator, not a caption
		}
		h.send(wsproto.Transcript(e.Dir.String(), e.Text, e.Final))
	case voice.EventError:
		if e.Err != nil && !h.voiceAccountError(e.Err) {
			h.send(wsproto.Error(e.Err.Error()))
		}
	}
}

// flush drops queued audio and tells the client to drop its buffer.
func (h *hub) flush() {
	h.s.audio.EndRun()
	c := h.current()
	if c == nil {
		return
	}
	dropped := c.dropAudio()
	c.enqueue(controlFrame(wsproto.Flush()))
	h.s.log.Log(logSource, "web.client.flush", "", map[string]any{"client_id": c.id, "dropped_audio": dropped})
}

// afterCancel is the stop tool's effect on the audio pipe: stop what is
// playing now, and let the model say the result (an interrupting push,
// which also makes the session emit Interrupted → flush for whatever it
// was saying). Without a voice session the result is shown as a
// transcript so the page has something to display.
func (h *hub) afterCancel(res voice.ToolResult) {
	h.flush()
	if h.s.deps.Voice != nil {
		// Tagged stop: the one reply that stays INTERRUPT even while the
		// model (or the client's playback) counts as speaking.
		push := func() error { return h.s.deps.Voice.Push(res.Spoken, voice.UrgencyInterrupt) }
		if tp, ok := h.s.deps.Voice.(tagPusher); ok {
			push = func() error {
				return tp.PushTagged(res.Spoken, voice.UrgencyInterrupt, map[string]any{"stop": true})
			}
		}
		if err := push(); err != nil {
			h.s.log.Log(logSource, "web.push_error", "", map[string]any{"error": err.Error()})
		}
		return
	}
	h.send(wsproto.Transcript(wsproto.DirOut, res.Spoken, true))
}

// setMuted records the client's mute state and mirrors it to the session.
func (h *hub) setMuted(on bool) {
	h.mu.Lock()
	h.muted = on
	h.mu.Unlock()
	if h.s.deps.Voice != nil {
		h.s.deps.Voice.SetMuted(on)
	}
	h.s.log.Log(logSource, "web.client.mute", "", map[string]any{"muted": on})
	h.send(h.statusMsg())
}

// setSilence records a silence-gate transition reported by the client and,
// when the gate closed, tells the session the audio stream paused. From a
// client that never sent a speaking message the gate is also its speech
// detector: open means speaking (docs/ws-contract.md "Client speech
// detector").
func (h *hub) setSilence(c *client, on bool) {
	c.mu.Lock()
	c.silent = on
	c.silenceTransitions++
	gateIsDetector := !c.speakingReports
	c.mu.Unlock()
	h.s.log.Log(logSource, "web.client.silence", "", map[string]any{"client_id": c.id, "on": on})
	if on {
		if v, ok := h.s.deps.Voice.(audioEnder); ok {
			v.AudioStreamEnd()
		}
	}
	if gateIsDetector {
		if v, ok := h.s.deps.Voice.(speechReporter); ok {
			v.ClientSpeaking(!on)
		}
	}
}

// setSpeaking records the client's own speech detector (a speaking
// message): from then on it, not the silence gate, is the signal.
func (h *hub) setSpeaking(c *client, on bool) {
	c.mu.Lock()
	c.speakingReports = true
	c.mu.Unlock()
	h.s.log.Log(logSource, "web.client.speaking", "", map[string]any{"client_id": c.id, "on": on})
	if v, ok := h.s.deps.Voice.(speechReporter); ok {
		v.ClientSpeaking(on)
	}
}

// setPlayback forwards a client playback report to the session, which
// logs it (voice.playback) with the run it describes.
func (h *hub) setPlayback(c *client, state string, playedMs int) {
	c.mu.Lock()
	c.playbackReports++
	c.mu.Unlock()
	if v, ok := h.s.deps.Voice.(playbackReporter); ok {
		v.ClientPlayback(state, playedMs)
	}
}

// clientDrain logs a client drain message (docs/ws-contract.md
// "Feedback and drains"). Diagnostic only: "speaking" and the playback
// run stay with the playback reports. skew_ms is the daemon's receive
// time minus the client's at: message latency plus clock offset.
func (h *hub) clientDrain(c *client, m wsproto.Message) {
	p := map[string]any{"client_id": c.id, "at": m.At, "buffer_ms": *m.BufferMs, "played_ms": *m.PlayedMs}
	if m.ReceivedMs != nil {
		p["received_ms"] = *m.ReceivedMs
	}
	if m.Reason != "" {
		p["reason"] = m.Reason
	}
	if at, err := time.Parse(time.RFC3339Nano, m.At); err == nil {
		p["skew_ms"] = h.s.deps.Now().Sub(at).Milliseconds()
	}
	h.s.log.Log(logSource, "web.client.drain", "", p)
}

// shutdown closes the client with a final status (daemon stopping).
func (h *hub) shutdown(reason string) {
	c := h.current()
	if c == nil {
		return
	}
	c.enqueue(controlFrame(h.closedStatus(reason)))
	c.closeAfterFlush(websocket.StatusGoingAway, reason)
}

// frame is one outbound WebSocket message.
type frame struct {
	binary bool
	data   []byte
	// close, when set, closes the connection after the queue drained.
	close  bool
	code   websocket.StatusCode
	reason string
	// at is when the frame was enqueued (set by enqueue if zero), used to
	// measure how long an audio frame waited in the per-client queue
	// before writeLoop sent it (web.client.stats send_wait_*).
	at time.Time
	// audioSeq is an audio frame's number in the server's AudioLog
	// (0: not recorded), so writeLoop can note when it went out.
	audioSeq uint64
}

func controlFrame(m wsproto.Message) frame {
	b, err := m.Encode()
	if err != nil {
		b = []byte(`{"type":"error","message":"encode failure"}`)
	}
	return frame{data: b}
}

// client is one browser connection: a reader (the handler goroutine) and
// a writer draining a bounded queue.
type client struct {
	h      *hub
	id     int
	conn   *websocket.Conn
	remote string
	max    int
	start  time.Time

	mu      sync.Mutex
	queue   []frame
	closing bool
	notify  chan struct{}
	done    chan struct{}
	once    sync.Once

	// counters (under mu)
	framesIn, framesOut   int
	bytesIn, bytesOut     int
	droppedAudio, dropped int // dropped = total drops, for throttling
	mutedDrops            int
	// silence gate, as reported by the client (silence messages and the
	// counters carried by its pings)
	silent             bool
	silenceTransitions int
	// speakingReports: the client sent a speaking message, so its
	// silence gate no longer stands in for its speech detector.
	speakingReports bool
	// playbackReports counts playback messages (web.client.stats).
	playbackReports int
	gatedFrames     int
	peak            int
	// playback stats reported by the browser's jitter buffer (Player in
	// audio-util.js), carried on ping messages.
	underruns int
	drains    int
	bufferMs  int
	// rate / stretchedMs: the time-stretch's current playback rate and
	// the wall-clock ms played at a rate other than 1.0 (ping rate,
	// stretched_ms).
	rate        float64
	stretchedMs int
	// playbackAt is when a ping last carried the playback counters: the
	// page pings every few seconds while statsLoop logs on its own
	// interval, so web.client.stats reports their age (playback_age_ms)
	// rather than passing a stale sample off as current (2026-09-24
	// 18:21:30–40: one 15 s-old sample of buffer_ms 3155 read as a
	// player frozen for 15 s). audioState is the page's AudioContext
	// state from the same ping.
	playbackAt time.Time
	audioState string
	// aec is the client's microphone echo cancellation from its ping
	// ("true", "false", "unknown"; empty: never reported).
	aec string
	// hs is the protocol handshake (handshake.go).
	hs handshake
	// screen is the client's one agent-window subscription (screen.go).
	screen *screenSub
	// pendingAttach: another client was attached when this one
	// connected, so it replaces that one only once its handshake is
	// accepted. firstStatus is the status it was sent on connect.
	pendingAttach bool
	firstStatus   []byte
	// gone (under hub.mu): the connection ended; never attach it.
	gone bool
	// sendWaits is a bounded rolling window of how long recent audio
	// frames waited in the outbound queue before writeLoop sent them
	// (ms), for web.client.stats send_wait_p95_ms / send_wait_max_ms.
	sendWaits []float64
}

// sendWaitWindow bounds the rolling window of recorded audio send-wait
// samples (recent behaviour matters more than the whole connection's).
const sendWaitWindow = 200

// recordSendWait appends one audio frame's queue-wait duration (ms).
func (c *client) recordSendWait(ms float64) {
	c.mu.Lock()
	c.sendWaits = append(c.sendWaits, ms)
	if len(c.sendWaits) > sendWaitWindow {
		c.sendWaits = c.sendWaits[len(c.sendWaits)-sendWaitWindow:]
	}
	c.mu.Unlock()
}

// sendWaitStats returns the max and p95 (nearest-rank) of the current
// rolling window, or (0, 0) if empty. Caller must not hold c.mu.
func (c *client) sendWaitStats() (maxMS, p95MS float64) {
	c.mu.Lock()
	n := len(c.sendWaits)
	if n == 0 {
		c.mu.Unlock()
		return 0, 0
	}
	sorted := make([]float64, n)
	copy(sorted, c.sendWaits)
	c.mu.Unlock()
	sort.Float64s(sorted)
	maxMS = sorted[n-1]
	idx := int(math.Ceil(0.95*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	p95MS = sorted[idx]
	return maxMS, p95MS
}

func newClient(h *hub, conn *websocket.Conn, remote string, maxQueue int) *client {
	return &client{h: h, conn: conn, remote: remote, max: maxQueue, start: h.s.deps.Now(), notify: make(chan struct{}, 1), done: make(chan struct{})}
}

// enqueue adds f to the outbound queue, dropping the oldest audio frame
// (or the oldest frame of any kind) when the queue is full.
func (c *client) enqueue(f frame) {
	if f.at.IsZero() {
		f.at = c.h.s.deps.Now()
	}
	c.mu.Lock()
	if c.closing && !f.close {
		c.mu.Unlock()
		return
	}
	if len(c.queue) >= c.max {
		idx := 0
		for i, q := range c.queue {
			if q.binary {
				idx = i
				break
			}
		}
		if c.queue[idx].binary {
			c.droppedAudio++
		}
		c.dropped++
		c.queue = append(c.queue[:idx], c.queue[idx+1:]...)
		if c.dropped == 1 || c.dropped%backpressureEvery == 0 {
			n := c.dropped
			c.mu.Unlock()
			c.h.s.log.Log(logSource, "web.client.backpressure", "", map[string]any{"client_id": c.id, "dropped": n, "queue": c.max})
			c.mu.Lock()
		}
	}
	c.queue = append(c.queue, f)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// dropAudio removes every queued audio frame and returns how many.
func (c *client) dropAudio() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.queue[:0]
	n := 0
	for _, f := range c.queue {
		if f.binary {
			n++
			continue
		}
		kept = append(kept, f)
	}
	c.queue = kept
	return n
}

// closeAfterFlush lets queued frames go out, then closes.
func (c *client) closeAfterFlush(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	// Nothing more is sent to a closing client: its window stops now.
	c.stopScreen("closed")
	c.enqueue(frame{close: true, code: code, reason: reason})
}

// writeLoop drains the queue until the connection closes.
func (c *client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case <-c.notify:
		}
		for {
			c.mu.Lock()
			if len(c.queue) == 0 {
				c.mu.Unlock()
				break
			}
			f := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			if f.close {
				_ = c.conn.Close(f.code, f.reason)
				return
			}
			typ := websocket.MessageText
			if f.binary {
				typ = websocket.MessageBinary
				if !f.at.IsZero() {
					wait := float64(c.h.s.deps.Now().Sub(f.at)) / float64(time.Millisecond)
					if wait < 0 {
						wait = 0
					}
					c.recordSendWait(wait)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			err := c.conn.Write(ctx, typ, f.data)
			cancel()
			if err != nil {
				_ = c.conn.CloseNow()
				return
			}
			if f.audioSeq != 0 {
				c.h.s.audio.Sent(f.audioSeq, c.h.s.deps.Now())
			}
			c.mu.Lock()
			c.framesOut++
			c.bytesOut += len(f.data)
			c.mu.Unlock()
		}
	}
}

// readLoop handles inbound frames until the connection ends; it returns
// the disconnect reason.
func (c *client) readLoop(ctx context.Context) string {
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			var ce websocket.CloseError
			switch {
			case errors.As(err, &ce):
				if ce.Reason != "" {
					return ce.Reason
				}
				return ce.Code.String()
			case ctx.Err() != nil:
				return "request cancelled"
			default:
				return "read error: " + err.Error()
			}
		}
		c.mu.Lock()
		c.framesIn++
		c.bytesIn += len(data)
		closing := c.closing
		c.mu.Unlock()
		if closing {
			// Replaced, shutting down or refused (protocol_too_old):
			// nothing more reaches the session.
			continue
		}
		if typ == websocket.MessageBinary {
			c.onAudio(data)
			continue
		}
		c.onControl(data)
	}
}

// onAudio forwards a microphone frame to the session.
func (c *client) onAudio(data []byte) {
	if !c.legacyIfUnresolved("first_frame") {
		return
	}
	h := c.h
	h.mu.Lock()
	muted := h.muted
	h.mu.Unlock()
	if muted || h.s.deps.Voice == nil {
		c.mu.Lock()
		c.mutedDrops++
		c.mu.Unlock()
		return
	}
	if err := h.s.deps.Voice.SendAudio(data); err != nil {
		c.enqueue(controlFrame(wsproto.Error("audio: " + err.Error())))
	}
}

// onControl routes one JSON control message.
func (c *client) onControl(data []byte) {
	h := c.h
	m, err := wsproto.Parse(data)
	if err == nil && m.Type == wsproto.TypeClientReady {
		c.onClientReady(m)
		return
	}
	if !c.legacyIfUnresolved("first_frame") {
		return
	}
	if err != nil {
		c.enqueue(controlFrame(wsproto.Error(err.Error())))
		return
	}
	switch m.Type {
	case wsproto.TypePing:
		c.mu.Lock()
		if m.GatedFrames != nil {
			c.gatedFrames = *m.GatedFrames
		}
		if m.Peak != nil {
			c.peak = *m.Peak
		}
		if m.Underruns != nil {
			c.underruns = *m.Underruns
		}
		if m.Drains != nil {
			c.drains = *m.Drains
		}
		if m.BufferMs != nil {
			c.bufferMs = *m.BufferMs
		}
		if m.Rate != nil {
			c.rate = *m.Rate
		}
		if m.StretchedMs != nil {
			c.stretchedMs = *m.StretchedMs
		}
		if m.BufferMs != nil {
			c.playbackAt = c.h.s.deps.Now()
		}
		if m.AudioState != "" {
			c.audioState = m.AudioState
		}
		if wsproto.ValidAEC(m.AEC) {
			c.aec = m.AEC
		}
		c.mu.Unlock()
		c.enqueue(controlFrame(wsproto.Pong()))
	case wsproto.TypeMute:
		h.setMuted(*m.On)
	case wsproto.TypeSilence:
		h.setSilence(c, *m.On)
	case wsproto.TypeSpeaking:
		h.setSpeaking(c, *m.On)
	case wsproto.TypePlayback:
		played := 0
		if m.PlayedMs != nil {
			played = *m.PlayedMs
		}
		h.setPlayback(c, m.State, played)
	case wsproto.TypeDrain:
		h.clientDrain(c, m)
	case wsproto.TypeScreenSubscribe:
		lines := 0
		if m.Lines != nil {
			lines = *m.Lines
		}
		c.subscribeScreen(m.PaneID, lines)
	case wsproto.TypeScreenUnsubscribe:
		c.stopScreen("unsubscribe")
	case wsproto.TypeCancel:
		ctx, cancel := h.s.toolCtx()
		res, err := h.s.runTool(ctx, voice.ToolCall{ID: "ws-cancel", Name: supervisor.ToolStop}, "ws")
		cancel()
		if err != nil {
			c.enqueue(controlFrame(wsproto.Error("cancel: " + err.Error())))
			return
		}
		h.afterCancel(res)
	case wsproto.TypeText:
		if h.s.deps.Voice != nil {
			h.s.log.Log(logSource, "web.utterance", "", map[string]any{"text": m.Text, "via": "voice", "client_id": c.id})
			h.s.typed(m.Text)
			if err := h.s.deps.Voice.SendText(m.Text); err != nil {
				c.enqueue(controlFrame(wsproto.Error("text: " + err.Error())))
			}
			return
		}
		// Echo the utterance first so the page shows it before the reply.
		c.enqueue(controlFrame(wsproto.Transcript(wsproto.DirIn, m.Text, true)))
		ctx, cancel := h.s.toolCtx()
		call, res, err := h.s.routeUtterance(ctx, m.Text)
		cancel()
		if err != nil {
			c.enqueue(controlFrame(wsproto.Error(err.Error())))
			return
		}
		if call.Name == supervisor.ToolStop {
			return // afterCancel already flushed and spoke the result
		}
		c.enqueue(controlFrame(wsproto.Transcript(wsproto.DirOut, res.Spoken, true)))
	default:
		// Server→client types arriving from the client.
		c.enqueue(controlFrame(wsproto.Error("unexpected message type " + m.Type)))
	}
}

// statsLoop logs web.client.stats with the counters every interval while
// the client is attached, so "muting sends zero frames" can be read off
// the event log without disconnecting (plan §9 WP6).
func (c *client) statsLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			payload := c.counters()
			if vs, ok := c.h.s.deps.Voice.(voiceStatser); ok {
				for k, v := range vs.VoiceStats() {
					payload[k] = v
				}
			}
			c.h.s.log.Log(logSource, "web.client.stats", "", payload)
		}
	}
}

// counters snapshots the frame counters for an event payload.
func (c *client) counters() map[string]any {
	maxWait, p95Wait := c.sendWaitStats()
	now := c.h.s.deps.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]any{
		"client_id": c.id,
		"frames_in": c.framesIn, "bytes_in": c.bytesIn, "frames_out": c.framesOut, "bytes_out": c.bytesOut,
		"dropped_audio": c.droppedAudio, "muted_drops": c.mutedDrops, "queued": len(c.queue),
		"silent": c.silent, "silence_transitions": c.silenceTransitions, "gated_frames": c.gatedFrames, "peak": c.peak,
		"playback_reports": c.playbackReports,
		"underruns":        c.underruns, "drains": c.drains, "buffer_ms": c.bufferMs,
		"rate": c.rate, "stretched_ms": c.stretchedMs,
		"send_wait_max_ms": round1(maxWait), "send_wait_p95_ms": round1(p95Wait),
		"dur_ms": now.Sub(c.start).Milliseconds(),
	}
	// Absent until the first ping with playback counters: the zeros
	// above are then placeholders, not a measurement.
	if !c.playbackAt.IsZero() {
		out["playback_age_ms"] = now.Sub(c.playbackAt).Milliseconds()
	}
	if c.audioState != "" {
		out["audio_state"] = c.audioState
	}
	if c.aec != "" {
		out["aec"] = c.aec
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// finish logs the disconnect and stops the writer. Safe to call twice.
func (c *client) finish(reason string) {
	c.once.Do(func() {
		c.stopScreen("disconnect")
		close(c.done)
		_ = c.conn.CloseNow()
		payload := c.counters()
		payload["remote"] = c.remote
		payload["reason"] = reason
		c.h.s.log.Log(logSource, "web.client.disconnect", "", payload)
	})
}
