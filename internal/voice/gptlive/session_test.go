package gptlive

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// fakeConn is one fake Live WebSocket: it answers session.start with
// session.started and session.close with session.closed, records every
// client event, and lets the test inject server events.
type fakeConn struct {
	mu     sync.Mutex
	sent   []map[string]any
	in     chan []byte
	closed chan struct{}
	once   sync.Once
	usage  float64
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan []byte, 256), closed: make(chan struct{}), usage: 7}
}

func (c *fakeConn) Send(_ context.Context, ev any) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	select {
	case <-c.closed:
		return errors.New("closed")
	default:
	}
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	switch m["type"] {
	case evSessionStart:
		c.server(map[string]any{"type": evSessionStarted, "client_event_id": m["event_id"], "session": map[string]any{"id": "live_fake", "model": "gpt-live-1"}})
	case evSessionClose:
		c.server(map[string]any{"type": evSessionClosed, "reason": "close_requested", "usage": map[string]any{"seconds": c.usage}})
	}
	return nil
}

func (c *fakeConn) server(ev map[string]any) {
	b, _ := json.Marshal(ev)
	c.in <- b
}

func (c *fakeConn) Receive(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.in:
		return b, nil
	case <-c.closed:
		return nil, errors.New("closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// events returns the sent events of the given types (audio appends
// excluded unless asked for).
func (c *fakeConn) events(types ...string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, m := range c.sent {
		for _, t := range types {
			if m["type"] == t {
				out = append(out, m)
			}
		}
	}
	return out
}

type fakeDialer struct {
	mu    sync.Mutex
	conns []*fakeConn
	err   error
}

func (d *fakeDialer) Dial(context.Context, string, string) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	c := newFakeConn()
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *fakeDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

func (d *fakeDialer) last() *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return nil
	}
	return d.conns[len(d.conns)-1]
}

// recLog records log events.
type recLog struct {
	mu  sync.Mutex
	evs []recEvent
}

type recEvent struct {
	typ     string
	payload map[string]any
}

func (l *recLog) Log(_, typ, _ string, payload any) {
	p, _ := payload.(map[string]any)
	l.mu.Lock()
	l.evs = append(l.evs, recEvent{typ, p})
	l.mu.Unlock()
}

func (l *recLog) find(typ string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, e := range l.evs {
		if e.typ == typ {
			out = append(out, e.payload)
		}
	}
	return out
}

func testConfig() Config {
	return Config{
		APIKey: "sk-test", Tick: 5 * time.Millisecond, IdleClose: 300 * time.Millisecond,
		SettleWait: 60 * time.Millisecond, SettleMax: 300 * time.Millisecond, SpeechHold: 60 * time.Millisecond,
		PauseHold: 80 * time.Millisecond, PushGap: time.Millisecond, CloseTimeout: 500 * time.Millisecond,
		OpenBackoff:  50 * time.Millisecond,
		Instructions: func() (string, error) { return "be brief", nil },
	}
}

func startSession(t *testing.T, cfg Config) (*Session, *fakeDialer, *recLog) {
	t.Helper()
	d := &fakeDialer{}
	l := &recLog{}
	s, err := newSession(cfg, l, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		for range s.Audio() {
		}
	}()
	t.Cleanup(func() { _ = s.Close() })
	return s, d, l
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// tone returns a 16 kHz PCM16 frame of the given amplitude.
func tone(amp int16, samples int) []byte {
	b := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := amp
		if i%2 == 1 {
			v = -amp
		}
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func b64(pcm []byte) string { return base64.StdEncoding.EncodeToString(pcm) }

func TestStartDoesNotDial(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	time.Sleep(30 * time.Millisecond)
	if d.count() != 0 {
		t.Fatalf("dialed %d times at Start", d.count())
	}
	if s.Status() != voice.StatusConnected || s.Live() {
		t.Fatalf("status %v live %v", s.Status(), s.Live())
	}
	ev := <-s.Events()
	if st, ok := ev.(voice.EventStatus); !ok || st.Status != voice.StatusConnected || st.Reason != ReasonStandby {
		t.Fatalf("first event %#v", ev)
	}
}

func TestLoudAudioOpensOnlyForAttachedClient(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	loud := tone(3000, 640)
	_ = s.SendAudio(loud) // no client attached
	_ = s.SendAudio(tone(50, 640))
	time.Sleep(30 * time.Millisecond)
	if d.count() != 0 {
		t.Fatal("opened without a client")
	}
	s.ClientAttached(true)
	_ = s.SendAudio(tone(50, 640)) // quiet: no open
	time.Sleep(20 * time.Millisecond)
	if d.count() != 0 {
		t.Fatal("quiet audio opened a session")
	}
	_ = s.SendAudio(loud)
	waitFor(t, "session open", s.Live)
	c := d.last()
	start := c.events(evSessionStart)[0]["session"].(map[string]any)
	if start["model"] != DefaultModel || start["instructions"] != "be brief" {
		t.Fatalf("session.start %v", start)
	}
	audio := start["audio"].(map[string]any)
	if f := audio["format"].(map[string]any); f["rate"] != float64(24000) || f["type"] != "audio/pcm" {
		t.Fatalf("format %v", f)
	}
	if audio["output"].(map[string]any)["voice"] != DefaultVoice {
		t.Fatalf("voice %v", audio)
	}
	if start["delegation"].(map[string]any)["type"] != "client" {
		t.Fatalf("delegation %v", start["delegation"])
	}
	// The pre-roll and the triggering frame are replayed, upsampled 2:3.
	waitFor(t, "replayed audio", func() bool { return len(c.events(evInputAudioAppend)) >= 1 })
	first := c.events(evInputAudioAppend)[0]
	raw, _ := base64.StdEncoding.DecodeString(first["audio"].(string))
	if len(raw) < 900 {
		t.Fatalf("replayed chunk %d bytes, want ~960 (640 samples × 1.5 × 2)", len(raw))
	}
	if got := l.find("voice.session_open"); len(got) != 1 || got[0]["reason"] != "audio" {
		t.Fatalf("session_open %v", got)
	}
}

func TestDelegationRelaysUtteranceAndAnswersWithCommentary(t *testing.T) {
	cfg := testConfig()
	var got voice.ToolCall
	var mu sync.Mutex
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		mu.Lock()
		got = call
		mu.Unlock()
		return voice.ToolResult{Spoken: "Auth is waiting for approval.", Detail: "it wants to run migrations", Scheduling: voice.UrgencyIdle}, nil
	}
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": " What's going", "start_ms": 1000, "end_ms": 1200})
	c.server(map[string]any{"type": evInputTranscript, "delta": " on?", "start_ms": 1200, "end_ms": 1400})
	c.server(map[string]any{"type": evDelegationCreated, "offset_ms": 1400, "delegation": map[string]any{"id": "item_1", "type": "delegation", "target": "client"}})
	waitFor(t, "commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	mu.Lock()
	if got.Name != voice.ToolToSupervisor || got.Args["text"] != "What's going on?" || got.ID != "item_1" {
		t.Fatalf("tool call %+v", got)
	}
	mu.Unlock()
	com := c.events(evCommentaryAppend)[0]
	if com["delegation_id"] != "item_1" || com["content"] != "Auth is waiting for approval." {
		t.Fatalf("commentary %v", com)
	}
	th := c.events(evThinkingAppend)
	if len(th) != 1 || !strings.Contains(th[0]["content"].(string), "migrations") || th[0]["delegation_id"] != "item_1" {
		t.Fatalf("thinking %v", th)
	}
	// The transcript reached the event stream (the rail reads it) and was
	// closed with an empty final fragment once it settled.
	var in []voice.EventTranscript
	waitFor(t, "final transcript", func() bool {
		for {
			select {
			case ev := <-s.Events():
				if tr, ok := ev.(voice.EventTranscript); ok && tr.Dir == voice.DirIn {
					in = append(in, tr)
				}
			default:
				return len(in) == 3 && in[2].Final && in[2].Text == ""
			}
		}
	})
}

// A pending result (the brain's reply comes later as a push, see
// brain.reply_wait_s) must not tell the model there is nothing to add.
func TestPendingDelegationResultDoesNotSayNothingToAdd(t *testing.T) {
	cfg := testConfig()
	cfg.Handler = func(context.Context, voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Pending: true, Scheduling: voice.UrgencySilent}, nil
	}
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": " Why did it fail?", "start_ms": 1000, "end_ms": 1400})
	c.server(map[string]any{"type": evDelegationCreated, "offset_ms": 1400, "delegation": map[string]any{"id": "item_p", "type": "delegation", "target": "client"}})
	waitFor(t, "thinking", func() bool { return len(c.events(evThinkingAppend)) == 1 })
	th := c.events(evThinkingAppend)[0]["content"].(string)
	if strings.Contains(th, "nothing to add") || !strings.Contains(th, "on its own") {
		t.Fatalf("pending result appended %q", th)
	}
	if n := len(c.events(evCommentaryAppend)); n != 0 {
		t.Fatalf("pending result spoke %d commentary items", n)
	}
}

func TestIdleCloseBillsAndStaysArmedForTheNextUtterance(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	waitFor(t, "idle close", func() bool { return len(l.find("voice.session_close")) == 1 })
	if len(c.events(evSessionClose)) != 1 {
		t.Fatal("no session.close sent")
	}
	cl := l.find("voice.session_close")[0]
	if cl["reason"] != "idle" || cl["confirmed"] != true || cl["billed_s"] != float64(7) {
		t.Fatalf("session_close %v", cl)
	}
	if s.Live() || s.BilledSeconds() != 7 {
		t.Fatalf("live %v billed %v", s.Live(), s.BilledSeconds())
	}
	// The gate had closed (no frames): the next utterance opens a new
	// session at once, with the recap as history.
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "reopen", func() bool { return d.count() == 2 && s.Live() })
}

// TestIdleCloseWaitsForSpeechInProgress: loud frames without a
// transcript yet (the shepherd's first words) hold the idle close off
// (seen live: the close fired 0.7 s into an utterance and lost it).
func TestIdleCloseWaitsForSpeechInProgress(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 100 * time.Millisecond
	cfg.NoiseRun = 10 * time.Second
	s, _, l := startSession(t, cfg)
	s.ClientAttached(true)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				_ = s.SendAudio(tone(3000, 320))
			}
		}
	}()
	waitFor(t, "open", s.Live)
	time.Sleep(500 * time.Millisecond)
	if len(l.find("voice.session_close")) != 0 || !s.Live() {
		t.Fatal("idle close fired while the shepherd was talking")
	}
}

// TestIdleCloseCountsMutedTimeAsIdle: the shepherd talks, then mutes
// (the push-to-talk rhythm of 2026-09-23); with nothing playing and no
// push pending, the muted time counts toward IdleClose at once — the
// loud frames sent just before the mute do not hold the session open as
// "a sentence in progress".
func TestIdleCloseCountsMutedTimeAsIdle(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 100 * time.Millisecond
	cfg.NoiseRun = 10 * time.Second
	s, d, l := startSession(t, cfg)
	s.ClientAttached(true)
	for i := 0; i < 10; i++ {
		_ = s.SendAudio(tone(3000, 320))
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, "open", s.Live)
	s.SetMuted(true)
	muted := time.Now()
	waitFor(t, "idle close while muted", func() bool { return len(l.find("voice.session_close")) == 1 })
	if el := time.Since(muted); el > time.Second {
		t.Errorf("closed %s after the mute; the pre-mute loud frames held it", el)
	}
	if len(d.last().events(evSessionClose)) != 1 || l.find("voice.session_close")[0]["reason"] != "idle" {
		t.Fatalf("session_close %v", l.find("voice.session_close"))
	}
}

// TestIdleCloseWaitsForADelegationWhileMuted: a muted shepherd waiting
// for an answer (delegation in flight) keeps the session: closing it
// would leave the answer queued until they unmute.
func TestIdleCloseWaitsForADelegationWhileMuted(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 100 * time.Millisecond
	release := make(chan struct{})
	cfg.Handler = func(ctx context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return voice.ToolResult{Spoken: "done"}, nil
	}
	s, d, l := startSession(t, cfg)
	defer close(release)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	d.last().server(map[string]any{"type": evInputTranscript, "delta": "what did auth find?"})
	d.last().server(map[string]any{"type": evDelegationCreated, "offset_ms": 0, "delegation": map[string]any{"id": "del_1", "target": "client"}})
	waitFor(t, "tool call", func() bool { return len(l.find("voice.tool_call")) == 1 })
	s.SetMuted(true)
	time.Sleep(400 * time.Millisecond)
	if len(l.find("voice.session_close")) != 0 || !s.Live() {
		t.Fatal("idle close fired with a delegation in flight")
	}
}

// TestNoisyRoomClosesAndDisarms: loud frames that go on past NoiseRun
// with nothing transcribed are room noise: the session closes, and the
// same noise does not reopen it until the gate closes.
func TestNoisyRoomClosesAndDisarms(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 100 * time.Millisecond
	cfg.NoiseRun = 150 * time.Millisecond
	s, d, l := startSession(t, cfg)
	s.ClientAttached(true)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				_ = s.SendAudio(tone(3000, 320))
			}
		}
	}()
	waitFor(t, "idle close", func() bool { return len(l.find("voice.session_close")) == 1 })
	time.Sleep(200 * time.Millisecond)
	if d.count() != 1 {
		t.Fatalf("the noise reopened the session (%d dials)", d.count())
	}
	close(stop)
	<-done
	s.AudioStreamEnd()
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "reopen after the gate closed", func() bool { return d.count() == 2 })
}

func TestKeepAliveSendsSilenceWhileGateClosed(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	n := len(c.events(evInputAudioAppend))
	waitFor(t, "keep-alive frames", func() bool { return len(c.events(evInputAudioAppend)) > n+3 })
}

func TestPushOpensSessionOnlyWithListener(t *testing.T) {
	cfg := testConfig()
	cfg.PushTTL = 100 * time.Millisecond
	s, d, l := startSession(t, cfg)
	_ = s.Push("docs finished", voice.UrgencyIdle)
	time.Sleep(40 * time.Millisecond)
	if d.count() != 0 {
		t.Fatal("push opened a session with nobody listening")
	}
	waitFor(t, "push dropped", func() bool { return len(l.find("voice.push_dropped")) == 1 })
	s.ClientAttached(true)
	_ = s.Push("auth needs you", voice.UrgencyIdle)
	waitFor(t, "commentary", func() bool {
		c := d.last()
		return c != nil && len(c.events(evCommentaryAppend)) == 1
	})
	com := d.last().events(evCommentaryAppend)[0]
	if com["content"] != "auth needs you" || com["delegation_id"] != nil {
		t.Fatalf("push commentary %v", com)
	}
	if got := l.find("voice.session_open"); got[0]["reason"] != "push" {
		t.Fatalf("session_open %v", got)
	}
}

func TestSilentPushInStandbyGoesIntoNextHistory(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	_ = s.Push("the build is green", voice.UrgencySilent)
	time.Sleep(30 * time.Millisecond)
	if d.count() != 0 {
		t.Fatal("silent push opened a session")
	}
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	start := d.last().events(evSessionStart)[0]["session"].(map[string]any)
	input, _ := start["input"].([]any)
	if len(input) != 1 || !strings.Contains(fmt.Sprint(input[0]), "the build is green") {
		t.Fatalf("input %v", input)
	}
}

func TestInterruptPushDeferredWhileSpeaking(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	// The model is talking: voiced output audio keeps arriving.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 480))})
			}
		}
	}()
	waitFor(t, "speaking", s.Speaking)
	_ = s.Push("promise due", voice.UrgencyInterrupt)
	time.Sleep(60 * time.Millisecond)
	if len(c.events(evInstructionsAppend)) != 0 || len(c.events(evCommentaryAppend)) != 0 {
		t.Fatal("push went out while the model was speaking")
	}
	close(stop)
	waitFor(t, "deferred push as commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	if len(l.find("voice.push_deferred")) != 1 {
		t.Fatal("no voice.push_deferred")
	}
	// An exempt interrupt goes at once as instructions.
	_ = s.PushTagged("decide now", voice.UrgencyInterrupt, map[string]any{"needs_user": true})
	waitFor(t, "instructions", func() bool { return len(c.events(evInstructionsAppend)) == 1 })
}

func TestOutputAudioSilenceDroppedAndTurnComplete(t *testing.T) {
	d := &fakeDialer{}
	s, err := newSession(testConfig(), nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var mu sync.Mutex
	got := 0
	go func() {
		for chunk := range s.Audio() {
			mu.Lock()
			got += len(chunk)
			mu.Unlock()
		}
	}()
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	silent := make([]byte, 960)
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(silent)}) // leading silence: dropped
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 480))})
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(silent)}) // a pause: kept
	waitFor(t, "audio", func() bool { mu.Lock(); defer mu.Unlock(); return got == 960+960 })
	sawDone := false
	waitFor(t, "turn complete", func() bool {
		for {
			select {
			case ev := <-s.Events():
				if _, ok := ev.(voice.EventTurnComplete); ok {
					sawDone = true
				}
			default:
				return sawDone
			}
		}
	})
	time.Sleep(150 * time.Millisecond)
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(silent)}) // long after: dropped
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got != 1920 {
		t.Fatalf("forwarded %d bytes, want 1920", got)
	}
}

func TestAudioTimingLoggedPerTurn(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 960))})
	time.Sleep(5 * time.Millisecond)
	c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 960))})
	waitFor(t, "voice.audio_timing logged", func() bool { return len(l.find("voice.audio_timing")) == 1 })
	p := l.find("voice.audio_timing")[0]
	if p["chunks"] != 2 {
		t.Fatalf("chunks = %v, want 2", p["chunks"])
	}
	if p["bytes"] != 3840 {
		t.Fatalf("bytes = %v, want 3840", p["bytes"])
	}
	for _, k := range []string{"turn", "audio_ms", "wall_ms", "max_gap_ms", "p95_gap_ms"} {
		if _, ok := p[k]; !ok {
			t.Errorf("missing %s in %v", k, p)
		}
	}
}

func TestBareStopWithoutDelegationRunsStop(t *testing.T) {
	cfg := testConfig()
	calls := make(chan voice.ToolCall, 4)
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		calls <- call
		return voice.ToolResult{Spoken: "Stopped.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	cfg.Classify = func(text string) (voice.ToolCall, bool) {
		if strings.EqualFold(strings.Trim(strings.TrimSpace(text), ".!"), "stop") {
			return voice.ToolCall{Name: voice.ToolStop}, true
		}
		return voice.ToolCall{Name: voice.ToolToSupervisor, Args: map[string]any{"text": text}}, true
	}
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": " Stop."})
	select {
	case call := <-calls:
		if call.Name != voice.ToolStop {
			t.Fatalf("call %+v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop not run")
	}
	// Its result is said at once (exempt interrupt → instructions).
	waitFor(t, "stop result", func() bool { return len(c.events(evInstructionsAppend)) == 1 })
	// A delegation that trails the stop finds nothing left to relay.
	c.server(map[string]any{"type": evDelegationCreated, "delegation": map[string]any{"id": "item_9", "target": "client"}})
	waitFor(t, "empty delegation note", func() bool { return len(c.events(evThinkingAppend)) == 1 })
	select {
	case call := <-calls:
		t.Fatalf("unexpected second call %+v", call)
	default:
	}
}

func TestSendTextSkipsVoiceModel(t *testing.T) {
	cfg := testConfig()
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Spoken: "Docs is idle.", Scheduling: voice.UrgencyIdle}, nil
	}
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	if err := s.SendText("how is docs"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "spoken reply", func() bool {
		c := d.last()
		return c != nil && len(c.events(evCommentaryAppend)) == 1
	})
	start := d.last().events(evSessionStart)[0]["session"].(map[string]any)
	if !strings.Contains(fmt.Sprint(start["input"]), "how is docs") {
		t.Fatalf("typed text not in history: %v", start["input"])
	}
}

func TestOpenFailureBacksOff(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	d.err = &HTTPError{Status: 401, Err: errors.New("unauthorized")}
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "session_error", func() bool { return len(l.find("voice.session_error")) == 1 })
	_ = s.SendAudio(tone(3000, 640))
	time.Sleep(10 * time.Millisecond)
	if n := len(l.find("voice.session_error")); n != 1 {
		t.Fatalf("retried without backoff: %d errors", n)
	}
	if !isRateLimited(&HTTPError{Status: 429}) || isRateLimited(&HTTPError{Status: 401}) {
		t.Fatal("isRateLimited")
	}
}

func TestCloseClosesLiveSessionGracefully(t *testing.T) {
	d := &fakeDialer{}
	l := &recLog{}
	s, _ := newSession(testConfig(), l, d)
	_ = s.Start(context.Background())
	go func() {
		for range s.Audio() {
		}
	}()
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cl := l.find("voice.session_close")
	if len(cl) != 1 || cl[0]["reason"] != "shutdown" || cl[0]["confirmed"] != true {
		t.Fatalf("session_close %v", cl)
	}
	for range s.Events() {
	}
}

func TestUpsampler(t *testing.T) {
	var u upsampler
	in := make([]byte, 0, 8)
	for _, v := range []int16{0, 300, 600, 900} {
		in = binary.LittleEndian.AppendUint16(in, uint16(v))
	}
	out := u.process(in)
	// 4 samples: one full pair consumed (needs a lookahead) → 3 outputs,
	// 2 samples carried.
	if len(out) != 6 {
		t.Fatalf("out %d bytes", len(out))
	}
	vals := []int16{}
	for i := 0; i < len(out); i += 2 {
		vals = append(vals, int16(binary.LittleEndian.Uint16(out[i:])))
	}
	if vals[0] != 0 || vals[1] != 200 || vals[2] != 400 {
		t.Fatalf("vals %v", vals)
	}
	// A long stream converts at 3/2 overall.
	total := 0
	for i := 0; i < 50; i++ {
		total += len(u.process(make([]byte, 1280)))
	}
	if want := (50*640 + 4) * 3 / 2 * 2; total < want-12 || total > want+12 {
		t.Fatalf("total %d want ~%d", total, want)
	}
}
