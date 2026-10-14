package web

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// nextControl reads the next control message, skipping binary frames.
func (c *wsClient) nextControl() wsproto.Message {
	c.t.Helper()
	for {
		m, data, err := c.read(3 * time.Second)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		if data == nil {
			return m
		}
	}
}

// The voice's own account problem: shown once as a plain error naming
// the service and the fix (the raw error is not repeated on every
// retry), shown again to a client that attaches while it lasts, and over
// once a session really opens.
func TestWSVoiceAccountProblem(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, switcher: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	bad := errors.New("failed to receive setup complete: websocket: close 1007 (invalid payload data): API key not valid. Please pass a valid API key.")
	h.session.emit(voice.EventError{Err: bad})
	e := c.expect(wsproto.TypeError)
	if e.Problem != "invalid_key" || e.Role != "voice" || e.Provider != "gemini" ||
		!strings.HasPrefix(e.Message, "Gemini (AI Studio) rejected the API key") || !strings.Contains(e.Fix, "GEMINI_API_KEY") {
		t.Fatalf("account error = %+v", e)
	}
	if strings.Contains(e.Message, "1007") {
		t.Errorf("raw error in the message: %q", e.Message)
	}
	h.session.emit(voice.EventError{Err: bad})
	h.session.emit(voice.EventError{Err: errors.New("send failed")})
	if m := c.nextControl(); m.Type != wsproto.TypeError || m.Message != "send failed" {
		t.Fatalf("after a repeated account error, next = %+v (want the next plain error)", m)
	}
	n := 0
	for _, typ := range h.types() {
		if typ == "web.account_problem" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("web.account_problem count = %d", n)
	}

	// A client attaching now is shown it after its status.
	c2 := dialWS(t, h)
	if m := c2.nextControl(); m.Type != wsproto.TypeStatus {
		t.Fatalf("first = %+v", m)
	}
	if m := c2.nextControl(); m.Type != wsproto.TypeError || m.Problem != "invalid_key" {
		t.Fatalf("second = %+v", m)
	}

	// GPT-Live's standby proves nothing; a real connect ends it.
	h.session.emit(voice.EventStatus{Status: voice.StatusConnected, Reason: "standby"})
	c2.expect(wsproto.TypeStatus)
	if len(h.srv.deps.Accounts.Active()) != 1 {
		t.Fatal("standby cleared the problem")
	}
	h.session.emit(voice.EventStatus{Status: voice.StatusConnected})
	c2.expect(wsproto.TypeStatus)
	if got := h.srv.deps.Accounts.Active(); len(got) != 0 {
		t.Fatalf("still active after a connect: %+v", got)
	}
}

// An orchestrator problem reported to the shared set reaches the client.
func TestWSOrchestratorAccountProblem(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	h.srv.deps.Accounts.Report(brain.NewAccountAlert(brain.RoleOrchestrator, brain.ProviderDeepSeek, brain.ProblemBalance, false))
	e := c.expect(wsproto.TypeError)
	if e.Problem != "balance" || e.Role != "orchestrator" || e.Provider != "deepseek" || e.Fix != "top up the account (https://platform.deepseek.com)" {
		t.Fatalf("account error = %+v", e)
	}
}
