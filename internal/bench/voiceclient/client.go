// Package voiceclient is a phone emulator for the daemon's /ws: it
// connects like the iOS app (the client-ready / bot-ready handshake),
// streams microphone audio at real time in 40 ms frames through the
// apps' silence gate and speech detector, plays the bot's audio at real
// time through the apps' jitter buffer and sends the playback reports
// (started / drained / flushed with played_ms in stream time) the
// daemon's heard-detection depends on, supports barge-in, a background
// noise bed and a leaking speaker echo, and records everything: every
// /ws frame, the /events stream, the audio both ways and the bot's
// utterances with the part the shepherd actually heard.
//
// The voice bench (internal/bench) drives it with synthetic speech; a
// tier-3 harness against real herdr workers reuses it unchanged.
//
//	c, err := voiceclient.Dial(ctx, voiceclient.Options{BaseURL: "http://127.0.0.1:7790"})
//	_ = c.WaitConnected(ctx, 20*time.Second)
//	_ = c.Say(ctx, "status please", samples16k) // blocks for the clip's length
//	u, ok := c.NextUtterance(ctx, 15*time.Second) // what the bot said, and what was heard
//	rec := c.Close()
package voiceclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// Options configure a client.
type Options struct {
	// BaseURL is the daemon, e.g. http://127.0.0.1:7790.
	BaseURL string
	// Token is web.token, if any.
	Token string
	// Client names this client in client-ready (default
	// sheepdog-bench / tier2 / bench).
	Client wsproto.ClientInfo
	// Bed generates the room's background noise, n samples at a time at
	// 16 kHz; it is under the speech and fills the pauses (nil: digital
	// silence, which lets the silence gate close).
	Bed func(n int) []int16
	// Echo, when set, leaks the bot's played audio back into the
	// microphone (a phone without a perfect echo canceller).
	Echo *Echo
	// LocalBargeIn makes the client stop playing and report flushed the
	// moment the shepherd starts talking over the bot (and drop the rest
	// of that bot turn until the daemon's own flush, or until the
	// shepherd stops). Off, the bot keeps playing until the daemon
	// flushes.
	LocalBargeIn bool
	// NoSpeaking leaves out the speaking messages (the daemon then reads
	// the silence gate as the detector).
	NoSpeaking bool
	// Gate is the capture gate: GateIOS (default, the app's), GateWeb
	// (the /voice page's peak gate) or GateOff.
	Gate string
	// GateStartsOpen starts the iOS gate open on connect (it then closes
	// after 400 ms without voice) instead of closed with an immediate
	// silence on.
	GateStartsOpen bool
	// Player is the jitter buffer (default the Gemini numbers).
	Player PlayerConfig
	// NoEvents skips the /events stream.
	NoEvents bool
	// Grace is how long after an utterance ends late transcript
	// fragments still join it before it is handed out (default 700 ms).
	Grace time.Duration
	// Log, when set, gets one line per notable thing.
	Log io.Writer
}

// Echo is the speaker-to-microphone leak.
type Echo struct {
	DelayMS int     `json:"delay_ms"`
	GainDB  float64 `json:"gain_db"`
}

// Client is one connected phone. Safe for concurrent use.
type Client struct {
	o     Options
	start time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	wmu  sync.Mutex // serialises writes on conn
	conn *websocket.Conn

	mu        sync.Mutex
	rec       Recording
	player    *Player
	mic       *mic
	echo      *pcm.EchoLine
	speech    [][]int16 // queued speech frames (clean)
	speechEnd chan struct{}
	attached  bool
	statusCh  chan string
	// dropUntilFlush: after a local barge-in, the rest of the bot's turn
	// is dropped until the daemon's flush (or the shepherd stops).
	dropUntilFlush bool
	cur            *BotUtterance
	lastPush       time.Time
	lastPushID     string
	outQ           chan BotUtterance
	lastAudioAt    time.Time
	lastOutAt      time.Time
	lastInAt       time.Time
}

// Dial connects: /events first (so nothing the connection sets off is
// missed), then /ws with client-ready as the first text frame, then the
// microphone, player and ping loops.
func Dial(ctx context.Context, o Options) (*Client, error) {
	if o.Client.Name == "" {
		o.Client = wsproto.ClientInfo{Name: "sheepdog-bench", Version: "tier2", Platform: "bench"}
	}
	if o.Grace <= 0 {
		o.Grace = 700 * time.Millisecond
	}
	c := &Client{o: o, start: time.Now(), player: NewPlayer(o.Player), statusCh: make(chan string, 16), outQ: make(chan BotUtterance, 64)}
	c.rec.Start = c.start
	c.mic = newMic(o.Gate, !o.NoSpeaking)
	c.mic.startOpen = o.GateStartsOpen
	if o.Echo != nil {
		c.echo = pcm.NewEchoLine(InRate, o.Echo.DelayMS, o.Echo.GainDB)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if !o.NoEvents {
		ready := make(chan error, 1)
		c.wg.Add(1)
		go func() { defer c.wg.Done(); c.streamEvents(ready) }()
		select {
		case err := <-ready:
			if err != nil {
				c.cancel()
				c.wg.Wait()
				return nil, err
			}
		case <-time.After(5 * time.Second):
			c.cancel()
			c.wg.Wait()
			return nil, errors.New("voiceclient: /events did not connect")
		case <-ctx.Done():
			c.cancel()
			c.wg.Wait()
			return nil, ctx.Err()
		}
	}
	if err := c.attach(ctx); err != nil {
		c.cancel()
		c.wg.Wait()
		return nil, err
	}
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.micLoop() }()
	go func() { defer c.wg.Done(); c.playLoop() }()
	return c, nil
}

// attach opens /ws and sends client-ready.
func (c *Client) attach(ctx context.Context) error {
	u := "ws" + strings.TrimPrefix(c.o.BaseURL, "http") + "/ws"
	if c.o.Token != "" {
		u += "?token=" + url.QueryEscape(c.o.Token)
	}
	conn, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("voiceclient: dial /ws: %w", err)
	}
	conn.SetReadLimit(1 << 22)
	c.wmu.Lock()
	c.conn = conn
	c.wmu.Unlock()
	c.mu.Lock()
	c.attached = true
	initial := c.mic.reset()
	c.mu.Unlock()
	if err := c.send(wsproto.ClientReady(wsproto.ProtocolVersion, c.o.Client)); err != nil {
		return err
	}
	for _, m := range initial {
		if err := c.send(m); err != nil {
			return err
		}
	}
	c.wg.Add(2)
	go func() { defer c.wg.Done(); c.readLoop(conn) }()
	go func() { defer c.wg.Done(); c.pingLoop(conn) }()
	return nil
}

func (c *Client) since() int64 { return time.Since(c.start).Milliseconds() }

func (c *Client) logf(format string, args ...any) {
	if c.o.Log != nil {
		_, _ = fmt.Fprintf(c.o.Log, "[%6.1fs] "+format+"\n", append([]any{float64(c.since()) / 1000}, args...)...)
	}
}

func (c *Client) note(kind, text string) {
	c.rec.Notes = append(c.rec.Notes, Timed{AtMS: c.since(), Kind: kind, Text: text})
}

// send writes one control message (recorded).
func (c *Client) send(m wsproto.Message) error {
	b, err := m.Encode()
	if err != nil {
		return err
	}
	c.wmu.Lock()
	conn := c.conn
	if conn == nil {
		c.wmu.Unlock()
		return errors.New("voiceclient: not attached")
	}
	err = conn.Write(c.ctx, websocket.MessageText, b)
	c.wmu.Unlock()
	c.mu.Lock()
	c.rec.Frames = append(c.rec.Frames, Frame{AtMS: c.since(), Dir: "up", Kind: "text", Msg: b})
	c.mu.Unlock()
	return err
}

func (c *Client) sendAudio(frame []int16) error {
	b := pcm.ToBytes(frame)
	c.wmu.Lock()
	conn := c.conn
	if conn == nil {
		c.wmu.Unlock()
		return nil
	}
	err := conn.Write(c.ctx, websocket.MessageBinary, b)
	c.wmu.Unlock()
	c.mu.Lock()
	c.rec.Frames = append(c.rec.Frames, Frame{AtMS: c.since(), Dir: "up", Kind: "audio", Bytes: len(b)})
	c.mu.Unlock()
	return err
}

// readLoop records every daemon frame and feeds the player.
func (c *Client) readLoop(conn *websocket.Conn) {
	for {
		typ, data, err := conn.Read(c.ctx)
		if err != nil {
			c.mu.Lock()
			if c.conn == conn {
				c.note("ws_closed", err.Error())
			}
			c.mu.Unlock()
			return
		}
		now := time.Now()
		c.mu.Lock()
		if typ == websocket.MessageBinary {
			c.rec.Frames = append(c.rec.Frames, Frame{AtMS: c.since(), Dir: "down", Kind: "audio", Bytes: len(data)})
			s := pcm.FromBytes(data)
			c.rec.BotAudio = append(c.rec.BotAudio, s...)
			c.lastAudioAt = now
			if !c.dropUntilFlush {
				u := c.openUtterance()
				u.AudioMS += len(s) * 1000 / OutRate
				c.player.Push(now, s)
			}
			c.mu.Unlock()
			continue
		}
		c.rec.Frames = append(c.rec.Frames, Frame{AtMS: c.since(), Dir: "down", Kind: "text", Msg: append(json.RawMessage(nil), data...)})
		m, err := wsproto.Parse(data)
		if err != nil {
			c.mu.Unlock()
			continue
		}
		var reports []Report
		switch m.Type {
		case wsproto.TypeBotReady:
			c.rec.Features = m.Features
		case wsproto.TypeStatus:
			c.note("status", strings.TrimSpace(m.Voice+" "+m.Reason+" "+m.Provider))
			c.rec.Connected = m.Voice == "connected"
			select {
			case c.statusCh <- m.Voice:
			default:
			}
		case wsproto.TypeError:
			c.note("error", m.Message)
		case wsproto.TypeFlush:
			c.note("flush", "")
			c.dropUntilFlush = false
			r := c.player.Flush()
			reports = append(reports, r)
			c.closeUtterance(EndFlushed, r.PlayedMS)
		case wsproto.TypeTranscript:
			final := m.Final != nil && *m.Final
			if m.Dir == wsproto.DirIn {
				if m.Text != "" || final {
					c.rec.In = append(c.rec.In, Fragment{AtMS: c.since(), Text: m.Text, Final: final})
					c.lastInAt = now
				}
			} else if m.Text != "" && !c.dropUntilFlush {
				c.lastOutAt = now
				u := c.trailingOrOpen()
				u.Fragments = append(u.Fragments, Fragment{AtMS: c.since(), Text: m.Text, AudioMS: u.AudioMS})
			}
		}
		c.mu.Unlock()
		for _, r := range reports {
			_ = c.send(wsproto.Playback(r.State, r.PlayedMS))
		}
	}
}

// openUtterance returns the open bot utterance, opening one. c.mu held.
func (c *Client) openUtterance() *BotUtterance {
	if c.cur == nil {
		u := &BotUtterance{Index: len(c.rec.Bot), StartMS: c.since(), PlayStartMS: -1}
		if !c.lastPush.IsZero() && time.Since(c.lastPush) < 10*time.Second {
			u.Push, u.PushID = true, c.lastPushID
			c.lastPush = time.Time{}
		}
		c.cur = u
	}
	return c.cur
}

// trailingOrOpen: a fragment right after an utterance closed, with no
// new audio since, belongs to it (transcripts trail their audio).
func (c *Client) trailingOrOpen() *BotUtterance {
	if c.cur == nil && len(c.rec.Bot) > 0 {
		last := &c.rec.Bot[len(c.rec.Bot)-1]
		if c.since()-last.EndMS < c.o.Grace.Milliseconds() && c.lastAudioAt.Before(c.start.Add(time.Duration(last.EndMS)*time.Millisecond)) {
			return last
		}
	}
	return c.openUtterance()
}

// closeUtterance ends the open utterance and hands it out after the
// grace period. c.mu held.
func (c *Client) closeUtterance(end string, playedMS int) {
	u := c.cur
	if u == nil {
		return
	}
	c.cur = nil
	u.End, u.EndMS = end, c.since()
	if end == EndDrained {
		playedMS = u.AudioMS
	}
	u.PlayedMS = min(playedMS, u.AudioMS)
	c.rec.Bot = append(c.rec.Bot, *u)
	idx := len(c.rec.Bot) - 1
	c.logf("bot %s (%d/%d ms): %s", end, u.PlayedMS, u.AudioMS, joinFragments(u.Fragments))
	time.AfterFunc(c.o.Grace, func() {
		c.mu.Lock()
		b := &c.rec.Bot[idx]
		b.Text = joinFragments(b.Fragments)
		b.Heard = heardText(b.Fragments, b.PlayedMS, b.AudioMS)
		out := *b
		c.mu.Unlock()
		select {
		case c.outQ <- out:
		default:
		}
	})
}

// playLoop runs the player at real time (10 ms steps).
func (c *Client) playLoop() {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-t.C:
			c.mu.Lock()
			played, reports := c.player.Tick(now)
			if len(played) > 0 && c.echo != nil {
				c.echo.Feed(pcm.Resample(played, OutRate, InRate))
			}
			for _, r := range reports {
				switch r.State {
				case wsproto.PlaybackStarted:
					if c.cur != nil && c.cur.PlayStartMS < 0 {
						c.cur.PlayStartMS = c.since()
					}
				case wsproto.PlaybackDrained:
					c.closeUtterance(EndDrained, r.PlayedMS)
				}
			}
			// A transcript that never came with audio.
			if c.cur != nil && c.cur.AudioMS == 0 && !c.lastOutAt.IsZero() && now.Sub(c.lastOutAt) > 3*time.Second {
				c.closeUtterance(EndNoAudio, 0)
			}
			attached := c.attached
			c.mu.Unlock()
			if attached {
				for _, r := range reports {
					_ = c.send(wsproto.Playback(r.State, r.PlayedMS))
				}
			}
		}
	}
}

// micLoop sends one 40 ms frame per tick: queued speech (or the bed),
// plus the bed and the echo, through the gate.
func (c *Client) micLoop() {
	t := time.NewTicker(FrameDur)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		if !c.attached {
			c.mu.Unlock()
			continue
		}
		var clean []int16
		if len(c.speech) > 0 {
			clean = c.speech[0]
			c.speech = c.speech[1:]
			if len(c.speech) == 0 && c.speechEnd != nil {
				close(c.speechEnd)
				c.speechEnd = nil
			}
		} else {
			clean = make([]int16, FrameSamples)
		}
		frame := clean
		if c.o.Bed != nil {
			frame = pcm.Mix(frame, c.o.Bed(FrameSamples))
		}
		var echoNow []int16
		if c.echo != nil {
			echoNow = c.echo.Next(FrameSamples)
			frame = pcm.Mix(frame, echoNow)
		}
		c.rec.MicAudio = append(c.rec.MicAudio, frame...)
		voicePeak := pcm.Peak(clean)
		if c.echo != nil {
			voicePeak = pcm.Peak(pcm.Mix(clean, echoNow))
		}
		out := c.mic.push(frame, voicePeak)
		if c.dropUntilFlush && !c.mic.speaking && len(c.speech) == 0 {
			c.dropUntilFlush = false // the shepherd stopped and no flush came: play again
		}
		c.mu.Unlock()
		for _, m := range out.msgs {
			_ = c.send(m)
		}
		for _, f := range out.frames {
			_ = c.sendAudio(f)
		}
	}
}

// pingLoop sends a ping with the player's counters every 5 s.
func (c *Client) pingLoop(conn *websocket.Conn) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		c.wmu.Lock()
		same := c.conn == conn
		c.wmu.Unlock()
		if !same {
			return
		}
		c.mu.Lock()
		u, d, b := c.player.Stats()
		g, p := c.mic.gated, c.mic.peak
		c.mu.Unlock()
		m := wsproto.Ping()
		rate := 1.0
		m.GatedFrames, m.Peak, m.Underruns, m.Drains, m.BufferMs, m.Rate = &g, &p, &u, &d, &b, &rate
		m.AudioState, m.AEC = "running", wsproto.AECOn
		_ = c.send(m)
	}
}

// WaitConnected waits for status connected (the voice model is up).
func (c *Client) WaitConnected(ctx context.Context, timeout time.Duration) error {
	c.mu.Lock()
	ok := c.rec.Connected
	c.mu.Unlock()
	if ok {
		return nil
	}
	tm := time.NewTimer(timeout)
	defer tm.Stop()
	for {
		select {
		case v := <-c.statusCh:
			if v == "connected" {
				return nil
			}
			if v == "closed" {
				return errors.New("voiceclient: voice closed")
			}
		case <-tm.C:
			return fmt.Errorf("voiceclient: no voice connected within %s", timeout)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Say streams samples (16 kHz, clean speech) as the shepherd's words and
// blocks until the last frame went out (the clip's length, at real
// time). label names it in the recording. Talking over the bot is a
// barge-in: with LocalBargeIn the player stops at once and reports
// flushed.
func (c *Client) Say(ctx context.Context, label string, samples []int16) error {
	done, err := c.SayAsync(label, samples)
	if err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SayAsync queues the speech and returns a channel closed when it has
// been sent.
func (c *Client) SayAsync(label string, samples []int16) (<-chan struct{}, error) {
	c.mu.Lock()
	if !c.attached {
		c.mu.Unlock()
		return nil, errors.New("voiceclient: not attached")
	}
	var frames [][]int16
	for i := 0; i < len(samples); i += FrameSamples {
		f := make([]int16, FrameSamples)
		copy(f, samples[i:min(i+FrameSamples, len(samples))])
		frames = append(frames, f)
	}
	done := make(chan struct{})
	if len(frames) == 0 {
		close(done)
		c.mu.Unlock()
		return done, nil
	}
	if c.speechEnd != nil {
		close(c.speechEnd) // the earlier line is superseded
	}
	c.speech = append(c.speech, frames...)
	c.speechEnd = done
	barge := c.player.Active()
	startMS := c.since()
	c.rec.User = append(c.rec.User, UserUtterance{Label: label, StartMS: startMS, EndMS: startMS + int64(len(frames))*FrameDur.Milliseconds(), BargeIn: barge})
	var flushed *Report
	if barge && c.o.LocalBargeIn {
		r := c.player.Flush()
		flushed = &r
		c.closeUtterance(EndLocalFlush, r.PlayedMS)
		c.dropUntilFlush = true
	}
	c.logf("user: %s%s", label, map[bool]string{true: " (barge-in)"}[barge])
	c.mu.Unlock()
	if flushed != nil {
		_ = c.send(wsproto.Playback(flushed.State, flushed.PlayedMS))
	}
	return done, nil
}

// Cancel presses the stop button.
func (c *Client) Cancel() error { return c.send(wsproto.Cancel()) }

// SendText sends a typed utterance ({"type":"text"}).
func (c *Client) SendText(text string) error { return c.send(wsproto.SimulateText(text)) }

// Detach closes /ws (code 1000) as a phone going to sleep: the player
// is emptied (audio sent meanwhile is lost) and the microphone stops.
func (c *Client) Detach() {
	c.mu.Lock()
	c.attached = false
	c.speech = nil
	if c.speechEnd != nil {
		close(c.speechEnd)
		c.speechEnd = nil
	}
	r := c.player.Flush()
	c.closeUtterance(EndDetached, r.PlayedMS)
	c.note("detach", "")
	c.mu.Unlock()
	c.wmu.Lock()
	conn := c.conn
	c.conn = nil
	c.wmu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "bench detach")
	}
}

// Reattach opens /ws again with a fresh handshake (the same recording).
func (c *Client) Reattach(ctx context.Context) error {
	c.mu.Lock()
	c.note("reattach", "")
	c.rec.Connected = false
	c.mu.Unlock()
	return c.attach(ctx)
}

// NextUtterance waits for the next finished bot utterance.
func (c *Client) NextUtterance(ctx context.Context, timeout time.Duration) (BotUtterance, bool) {
	tm := time.NewTimer(timeout)
	defer tm.Stop()
	select {
	case u := <-c.outQ:
		return u, true
	case <-tm.C:
		return BotUtterance{}, false
	case <-ctx.Done():
		return BotUtterance{}, false
	}
}

// BotActive reports whether the bot is speaking or about to (audio
// buffered or playing, or an utterance open).
func (c *Client) BotActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.player.Active() || c.cur != nil
}

// PlayingFor is how long the current bot utterance has been playing (0
// when none is).
func (c *Client) PlayingFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil || c.cur.PlayStartMS < 0 || !c.player.Playing() {
		return 0
	}
	return time.Duration(c.since()-c.cur.PlayStartMS) * time.Millisecond
}

// CurrentText is the transcript of the bot utterance in progress ("" when
// none is open).
func (c *Client) CurrentText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		return ""
	}
	return joinFragments(c.cur.Fragments)
}

// UserSpeaking reports whether speech is still queued or the detector is
// open.
func (c *Client) UserSpeaking() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.speech) > 0 || c.mic.speaking
}

// QuietFor is how long since the bot last sent audio or an output
// transcript (since the start when it never did).
func (c *Client) QuietFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := c.start
	for _, t := range []time.Time{c.lastAudioAt, c.lastOutAt} {
		if t.After(last) {
			last = t
		}
	}
	if c.player.Active() || c.cur != nil {
		return 0
	}
	return time.Since(last)
}

// WaitQuiet waits until the bot has been quiet for quiet, at most max.
func (c *Client) WaitQuiet(ctx context.Context, quiet, max time.Duration) bool {
	end := time.Now().Add(max)
	for time.Now().Before(end) {
		if c.QuietFor() >= quiet {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

// InTranscriptSince is the input transcript (what the voice model heard
// the shepherd say) from atMS on.
func (c *Client) InTranscriptSince(atMS int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, f := range c.rec.In {
		if f.AtMS >= atMS {
			b.WriteString(f.Text)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// SinceMS is the client's clock (ms since Dial), the recording's time base.
func (c *Client) SinceMS() int64 { return c.since() }

// Events returns the daemon events recorded so far from index from on.
func (c *Client) Events(from int) []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	if from >= len(c.rec.Events) {
		return nil
	}
	return append([]Event(nil), c.rec.Events[from:]...)
}

// Snapshot copies the recording so far (audio included).
func (c *Client) Snapshot() Recording {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rec
	r.Frames = append([]Frame(nil), c.rec.Frames...)
	r.Events = append([]Event(nil), c.rec.Events...)
	r.Bot = append([]BotUtterance(nil), c.rec.Bot...)
	r.User = append([]UserUtterance(nil), c.rec.User...)
	r.In = append([]Fragment(nil), c.rec.In...)
	r.Notes = append([]Timed(nil), c.rec.Notes...)
	r.MicAudio = append([]int16(nil), c.rec.MicAudio...)
	r.BotAudio = append([]int16(nil), c.rec.BotAudio...)
	return r
}

// Close disconnects and returns the recording.
func (c *Client) Close() Recording {
	c.mu.Lock()
	if c.cur != nil {
		c.closeUtterance(EndDetached, c.player.PlayedMS())
	}
	c.attached = false
	c.mu.Unlock()
	c.wmu.Lock()
	conn := c.conn
	c.conn = nil
	c.wmu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "bench done")
	}
	c.cancel()
	c.wg.Wait()
	time.Sleep(10 * time.Millisecond)
	return c.Snapshot()
}

// streamEvents follows /events from now on (?since=<now in µs>, the
// event ids being Unix microseconds) and records every event.
func (c *Client) streamEvents(ready chan<- error) {
	u := c.o.BaseURL + "/events?since=" + strconv.FormatInt(time.Now().Add(-200*time.Millisecond).UnixMicro(), 10)
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, u, nil)
	if err != nil {
		ready <- err
		return
	}
	if c.o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.o.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ready <- fmt.Errorf("voiceclient: /events: %w", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		ready <- fmt.Errorf("voiceclient: /events: %s", resp.Status)
		return
	}
	ready <- nil
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<23)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var ev struct {
			ID      int64          `json:"id"`
			TS      time.Time      `json:"ts"`
			Type    string         `json:"type"`
			Pane    string         `json:"pane_id"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(line[6:], &ev) != nil || ev.Type == "" {
			continue
		}
		at := ev.TS.Sub(c.start).Milliseconds()
		if ev.TS.IsZero() {
			at = c.since()
		}
		c.mu.Lock()
		c.rec.Events = append(c.rec.Events, Event{AtMS: at, ID: ev.ID, Type: ev.Type, Pane: ev.Pane, Payload: ev.Payload})
		if ev.Type == "voice.push" {
			if u, _ := ev.Payload["urgency"].(string); u != "silent" {
				c.lastPush = time.Now()
				c.lastPushID, _ = ev.Payload["push_id"].(string)
			}
		}
		c.mu.Unlock()
	}
}
