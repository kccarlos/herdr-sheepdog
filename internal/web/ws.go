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
	old := h.client
	h.client = c
	h.mu.Unlock()

	replaced := old != nil
	if replaced {
		old.enqueue(controlFrame(wsproto.Status(wsproto.VoiceClosed, false, "replaced")))
		old.closeAfterFlush(websocket.StatusNormalClosure, "replaced")
	}
	h.s.log.Log(logSource, "web.client.connect", "", map[string]any{"client_id": c.id, "remote": c.remote, "replaced": replaced})
	if ca, ok := h.s.deps.Voice.(clientAttacher); ok {
		ca.ClientAttached(true)
	}

	// The contract: a status message before any binary frame.
	c.enqueue(controlFrame(h.statusMsg()))
	go c.writeLoop()
	go c.statsLoop(h.s.deps.StatsInterval)
	reason := c.readLoop(r.Context())
	c.finish(reason)

	h.mu.Lock()
	last := h.client == c
	if last {
		h.client = nil
	}
	h.mu.Unlock()
	if ca, ok := h.s.deps.Voice.(clientAttacher); ok && last {
		ca.ClientAttached(false)
	}
}

// current returns the attached client, if any.
func (h *hub) current() *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.client
}

// sendAudio forwards a model audio chunk to the client, if any.
func (h *hub) sendAudio(chunk []byte) {
	c := h.current()
	if c == nil {
		return
	}
	c.enqueue(frame{binary: true, data: chunk})
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
	case voice.EventInterrupted:
		h.flush()
	case voice.EventTranscript:
		h.send(wsproto.Transcript(e.Dir.String(), e.Text, e.Final))
	case voice.EventError:
		if e.Err != nil {
			h.send(wsproto.Error(e.Err.Error()))
		}
	}
}

// flush drops queued audio and tells the client to drop its buffer.
func (h *hub) flush() {
	c := h.current()
	if c == nil {
		return
	}
	dropped := c.dropAudio()
	c.enqueue(controlFrame(wsproto.Flush()))
	h.s.log.Log(logSource, "web.client.flush", "", map[string]any{"client_id": c.id, "dropped_audio": dropped})
}

// afterCancel is the cancel tool's effect on the audio pipe: stop what is
// playing now, and let the model say the result (an interrupting push,
// which also makes the session emit Interrupted → flush for whatever it
// was saying). Without a voice session the result is shown as a
// transcript so the page has something to display.
func (h *hub) afterCancel(res voice.ToolResult) {
	h.flush()
	if h.s.deps.Voice != nil {
		if err := h.s.deps.Voice.Push(res.Spoken, voice.UrgencyInterrupt); err != nil {
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
// when the gate closed, tells the session the audio stream paused.
func (h *hub) setSilence(c *client, on bool) {
	c.mu.Lock()
	c.silent = on
	c.silenceTransitions++
	c.mu.Unlock()
	h.s.log.Log(logSource, "web.client.silence", "", map[string]any{"client_id": c.id, "on": on})
	if on {
		if v, ok := h.s.deps.Voice.(audioEnder); ok {
			v.AudioStreamEnd()
		}
	}
}

// shutdown closes the client with a final status (daemon stopping).
func (h *hub) shutdown(reason string) {
	c := h.current()
	if c == nil {
		return
	}
	c.enqueue(controlFrame(wsproto.Status(wsproto.VoiceClosed, false, reason)))
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
	gatedFrames        int
	peak               int
	// playback stats reported by the browser's jitter buffer (Player in
	// audio-util.js), carried on ping messages.
	underruns int
	drains    int
	bufferMs  int
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
		c.mu.Unlock()
		if typ == websocket.MessageBinary {
			c.onAudio(data)
			continue
		}
		c.onControl(data)
	}
}

// onAudio forwards a microphone frame to the session.
func (c *client) onAudio(data []byte) {
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
		c.mu.Unlock()
		c.enqueue(controlFrame(wsproto.Pong()))
	case wsproto.TypeMute:
		h.setMuted(*m.On)
	case wsproto.TypeSilence:
		h.setSilence(c, *m.On)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"client_id": c.id,
		"frames_in": c.framesIn, "bytes_in": c.bytesIn, "frames_out": c.framesOut, "bytes_out": c.bytesOut,
		"dropped_audio": c.droppedAudio, "muted_drops": c.mutedDrops, "queued": len(c.queue),
		"silent": c.silent, "silence_transitions": c.silenceTransitions, "gated_frames": c.gatedFrames, "peak": c.peak,
		"underruns": c.underruns, "drains": c.drains, "buffer_ms": c.bufferMs,
		"send_wait_max_ms": round1(maxWait), "send_wait_p95_ms": round1(p95Wait),
		"dur_ms": c.h.s.deps.Now().Sub(c.start).Milliseconds(),
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// finish logs the disconnect and stops the writer. Safe to call twice.
func (c *client) finish(reason string) {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.CloseNow()
		payload := c.counters()
		payload["remote"] = c.remote
		payload["reason"] = reason
		c.h.s.log.Log(logSource, "web.client.disconnect", "", payload)
	})
}
