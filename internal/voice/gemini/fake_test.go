package gemini

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// sent is one recorded client message on a fakeConn.
type sent struct {
	kind    string // audio, audioEnd, text, content, tool
	audio   []byte
	text    string
	content genai.LiveClientContentInput
	tool    genai.LiveToolResponseInput
}

// fakeConn is a scriptable Conn: the test feeds server messages through
// incoming and reads what the session sent.
type fakeConn struct {
	name     string
	mu       sync.Mutex
	sent     []sent
	incoming chan *genai.LiveServerMessage
	closed   chan struct{}
	once     sync.Once
	readErr  error // returned by Receive once incoming is closed
	// contentErr, when set, fails every SendClientContent (a push whose
	// send fails, without the connection going away).
	contentErr error
}

func newFakeConn(name string) *fakeConn {
	return &fakeConn{name: name, incoming: make(chan *genai.LiveServerMessage, 64), closed: make(chan struct{}), readErr: errors.New("fake: server closed")}
}

func (c *fakeConn) record(m sent) error {
	select {
	case <-c.closed:
		return errors.New("fake: send on closed conn")
	default:
	}
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) SendRealtimeInput(in genai.LiveRealtimeInput) error {
	switch {
	case in.Audio != nil:
		return c.record(sent{kind: "audio", audio: in.Audio.Data})
	case in.AudioStreamEnd:
		return c.record(sent{kind: "audioEnd"})
	case in.Text != "":
		return c.record(sent{kind: "text", text: in.Text})
	}
	return c.record(sent{kind: "other"})
}

func (c *fakeConn) SendClientContent(in genai.LiveClientContentInput) error {
	c.mu.Lock()
	err := c.contentErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.record(sent{kind: "content", content: in})
}

func (c *fakeConn) SendToolResponse(in genai.LiveToolResponseInput) error {
	return c.record(sent{kind: "tool", tool: in})
}

func (c *fakeConn) Receive() (*genai.LiveServerMessage, error) {
	select {
	case m, ok := <-c.incoming:
		if !ok {
			return nil, c.readErr
		}
		return m, nil
	case <-c.closed:
		return nil, errors.New("fake: use of closed network connection")
	}
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// serve pushes a server message; it never blocks the test.
func (c *fakeConn) serve(m *genai.LiveServerMessage) {
	select {
	case c.incoming <- m:
	case <-time.After(time.Second):
		panic("fakeConn.serve: incoming full")
	}
}

// fail makes Receive return err (after any queued messages).
func (c *fakeConn) fail(err error) {
	c.readErr = err
	close(c.incoming)
}

func (c *fakeConn) sentMsgs() []sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sent(nil), c.sent...)
}

// waitSent polls until pred matches one sent message.
func (c *fakeConn) waitSent(t *testing.T, what string, pred func(sent) bool) sent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range c.sentMsgs() {
			if pred(m) {
				return m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: %s not sent; sent=%s", c.name, what, describe(c.sentMsgs()))
	return sent{}
}

func (c *fakeConn) count(kind string) int {
	n := 0
	for _, m := range c.sentMsgs() {
		if m.kind == kind {
			n++
		}
	}
	return n
}

func describe(ms []sent) string {
	out := ""
	for _, m := range ms {
		switch m.kind {
		case "content":
			out += fmt.Sprintf("[content %q tc=%v] ", m.content.Turns[0].Parts[0].Text, *m.content.TurnComplete)
		case "tool":
			out += fmt.Sprintf("[tool %s %s] ", m.tool.FunctionResponses[0].Name, m.tool.FunctionResponses[0].Scheduling)
		case "audio":
			out += fmt.Sprintf("[audio %d] ", len(m.audio))
		default:
			out += "[" + m.kind + "] "
		}
	}
	return out
}

// dialRecord is one Dial call seen by fakeDialer.
type dialRecord struct {
	key    string
	handle string
}

// fakeDialer hands out scripted results per dial, in order.
type fakeDialer struct {
	mu      sync.Mutex
	dials   []dialRecord
	results []func() (Conn, error) // consumed in order; last one repeats
	gate    chan struct{}          // when non-nil, dials after the first block until it is closed
}

func (d *fakeDialer) Dial(ctx context.Context, key, _ string, cfg *genai.LiveConnectConfig) (Conn, error) {
	d.mu.Lock()
	d.dials = append(d.dials, dialRecord{key: key, handle: cfg.SessionResumption.Handle})
	n := len(d.dials)
	gate := d.gate
	var next func() (Conn, error)
	if len(d.results) > 0 {
		next = d.results[0]
		if len(d.results) > 1 {
			d.results = d.results[1:]
		}
	}
	d.mu.Unlock()
	if gate != nil && n > 1 {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if next == nil {
		return nil, errors.New("fake: no more conns")
	}
	return next()
}

func (d *fakeDialer) records() []dialRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]dialRecord(nil), d.dials...)
}

func ok(c Conn) func() (Conn, error)      { return func() (Conn, error) { return c, nil } }
func fail(err error) func() (Conn, error) { return func() (Conn, error) { return nil, err } }

// recLogger records event types for assertions.
type recLogger struct {
	mu    sync.Mutex
	lines []logLine
}

type logLine struct {
	typ     string
	payload any
}

func (l *recLogger) Log(_, typ, _ string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, logLine{typ, payload})
}

func (l *recLogger) has(typ string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ln := range l.lines {
		if ln.typ == typ {
			return true
		}
	}
	return false
}

func (l *recLogger) payloads(typ string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, ln := range l.lines {
		if ln.typ == typ {
			if m, ok := ln.payload.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// harness bundles a started session with its fakes.
type harness struct {
	t      *testing.T
	s      *Session
	dialer *fakeDialer
	log    *recLogger
	events []voice.Event
	evMu   sync.Mutex
}

func newHarness(t *testing.T, cfg Config, dialer *fakeDialer) *harness {
	t.Helper()
	if cfg.APIKeys == nil {
		cfg.APIKeys = []string{"key-0", "key-1", "key-2"}
	}
	if cfg.PushGap == 0 {
		cfg.PushGap = 100 * time.Millisecond
	}
	if cfg.SpeechHold == 0 {
		cfg.SpeechHold = 150 * time.Millisecond
	}
	if cfg.ReconnectBackoff == 0 {
		cfg.ReconnectBackoff = 10 * time.Millisecond
	}
	if cfg.RateLimitBackoff == 0 {
		cfg.RateLimitBackoff = 20 * time.Millisecond
	}
	if cfg.ToolResponseWait == 0 {
		cfg.ToolResponseWait = 60 * time.Millisecond
	}
	log := &recLogger{}
	s, err := newSession(cfg, log, dialer)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	h := &harness{t: t, s: s, dialer: dialer, log: log}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range s.Events() {
			h.evMu.Lock()
			h.events = append(h.events, ev)
			h.evMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		<-done
	})
	return h
}

// waitEvent polls until an event satisfying pred was seen.
func (h *harness) waitEvent(what string, pred func(voice.Event) bool) voice.Event {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.evMu.Lock()
		for _, ev := range h.events {
			if pred(ev) {
				h.evMu.Unlock()
				return ev
			}
		}
		h.evMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	h.evMu.Lock()
	defer h.evMu.Unlock()
	h.t.Fatalf("event %s not seen; events=%v", what, h.events)
	return nil
}

func (h *harness) countEvents(pred func(voice.Event) bool) int {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	n := 0
	for _, ev := range h.events {
		if pred(ev) {
			n++
		}
	}
	return n
}

// Message builders.

func audioMsg(data []byte) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{ModelTurn: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "audio/pcm;rate=24000", Data: data}}}}}}
}

func interruptedMsg() *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{Interrupted: true}}
}

func turnCompleteMsg() *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{TurnComplete: true}, UsageMetadata: &genai.UsageMetadata{PromptTokenCount: 10, TotalTokenCount: 12}}
}

func inputTranscriptMsg(text string, finished bool) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{InputTranscription: &genai.Transcription{Text: text, Finished: finished}}}
}

func outputTranscriptMsg(text string) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{OutputTranscription: &genai.Transcription{Text: text}}}
}

func toolCallMsg(id, name string, args map[string]any) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ToolCall: &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{{ID: id, Name: name, Args: args}}}}
}

func toolCancelMsg(ids ...string) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ToolCallCancellation: &genai.LiveServerToolCallCancellation{IDs: ids}}
}

func handleMsg(handle string) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{SessionResumptionUpdate: &genai.LiveServerSessionResumptionUpdate{NewHandle: handle, Resumable: true}}
}

func goAwayMsg(left time.Duration) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{GoAway: &genai.LiveServerGoAway{TimeLeft: left}}
}

func isContent(text string) func(sent) bool {
	return func(m sent) bool {
		return m.kind == "content" && len(m.content.Turns) > 0 && len(m.content.Turns[0].Parts) > 0 &&
			strings.Contains(m.content.Turns[0].Parts[0].Text, text)
	}
}
