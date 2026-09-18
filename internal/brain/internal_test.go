package brain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// recLog records structured events (internal tests).
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) ofType(typ string) []logging.Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logging.Line
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func payload(e logging.Line) map[string]any {
	m, _ := e.Payload.(map[string]any)
	return m
}

// The turn lock hands over to CEO waiters first and survives a cancelled
// waiter.
func TestTurnLock(t *testing.T) {
	var l turnLock
	if err := l.acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- l.acquire(ctx, true) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter got %v", err)
	}
	got := make(chan string, 2)
	go func() {
		if err := l.acquire(context.Background(), false); err == nil {
			got <- "event"
			l.release()
		}
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		if err := l.acquire(context.Background(), true); err == nil {
			got <- "ceo"
			l.release()
		}
	}()
	time.Sleep(5 * time.Millisecond)
	l.release()
	if a, b := <-got, <-got; a != "ceo" || b != "event" {
		t.Errorf("order = %s %s", a, b)
	}
	if err := l.acquire(context.Background(), false); err != nil {
		t.Errorf("lock not free: %v", err)
	}
	l.release()
}

// cutPoint never splits a tool call from its result.
func TestCutPoint(t *testing.T) {
	w := []entry{
		{msg: Message{Kind: KindCEOUtterance, Tokens: 10}},
		{msg: Message{Kind: KindRequest, Tokens: 10}},
		{msg: Message{Kind: KindToolCall, Tokens: 10}},
		{msg: Message{Kind: KindToolResult, Tokens: 10}},
		{msg: Message{Kind: KindReply, Tokens: 10}},
		{msg: Message{Kind: KindCEOUtterance, Tokens: 10}},
		{msg: Message{Kind: KindRequest, Tokens: 10}},
		{msg: Message{Kind: KindReply, Tokens: 10}},
	}
	if got := cutPoint(w, 25); got != 5 {
		t.Errorf("cut = %d, want 5", got)
	}
	if got := cutPoint(w, 75); got != 0 {
		t.Errorf("cut past the end = %d, want 0", got)
	}
	// A pruned stub replaces the content but keeps the metadata.
	m := Message{Kind: KindToolResult, Content: strings.Repeat("x", 100), ToolResult: &ToolResult{Name: ToolReadScreen, Summary: "screen of auth, 12 lines"}}
	if s := stubFor(m); s != "[screen of auth, 12 lines, pruned]" {
		t.Errorf("stub = %q", s)
	}
	if s := stubFor(Message{Kind: KindToolResult, ToolResult: &ToolResult{Name: ToolRunCLI}}); s != "[run_cli result, pruned]" {
		t.Errorf("stub = %q", s)
	}
}

func TestParseReplyAndHelpers(t *testing.T) {
	r, err := parseReply(`Here you go: {"spoken": "Hi.", "detail": "d", "needs_user": true, "options": ["a", "b"], "clarify": false}`, false)
	if err != nil || r.Spoken != "Hi." || !r.NeedsUser || len(r.Options) != 2 {
		t.Errorf("parse = %+v %v", r, err)
	}
	if _, err := parseReply(`{"spoken": ""}`, false); err == nil {
		t.Error("empty spoken accepted")
	}
	// An event turn may answer "nothing to add".
	if r, err := parseReply(`{"spoken": "", "detail": "sufficient", "relates_to": "the rerun"}`, true); err != nil || r.Spoken != "" || r.RelatesTo != "the rerun" {
		t.Errorf("empty spoken for an event turn = %+v %v", r, err)
	}
	if got := keysArg([]any{"Down", "enter"}); strings.Join(got, ",") != "down,enter" {
		t.Errorf("keysArg = %v", got)
	}
	if got := keysArg("y enter"); strings.Join(got, ",") != "y,enter" {
		t.Errorf("keysArg string = %v", got)
	}
	if got := ago(90 * time.Second); got != "1 min" {
		t.Errorf("ago = %s", got)
	}
	if slug("The one rail") != "rail" || slug("Answer contract") != "contract" || slug("Tools") != "tools" {
		t.Errorf("slug = %s %s %s", slug("The one rail"), slug("Answer contract"), slug("Tools"))
	}
	if d, ok := retryDelay(errors.New(`{"retryDelay": "12s"}`)); !ok || d != 12*time.Second {
		t.Errorf("retryDelay = %v %v", d, ok)
	}
	if !isRateLimited(genai.APIError{Code: 429}) || !isRateLimited(errors.New("RESOURCE_EXHAUSTED")) || isRateLimited(errors.New("nope")) {
		t.Error("isRateLimited")
	}
}
