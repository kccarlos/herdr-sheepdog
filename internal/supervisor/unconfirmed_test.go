package supervisor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// A prompt written to herdr whose reply never comes (live log 2026-10-27)
// is not reported as "could not send": the daemon looks at the screen and
// says it arrived, or that it may have and to check and resend.
func TestPromptNoReplyChecksTheScreen(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateIdle)
	h.client.Timeout = 100 * time.Millisecond
	h.srv.Handle("agent.prompt", func(json.RawMessage) (any, error) {
		time.Sleep(400 * time.Millisecond)
		return map[string]any{}, nil
	})

	h.srv.SetScreen("w5:p1", "idle, nothing yet\n")
	res := h.send("w5:p1", "run the tests")
	if !strings.Contains(res.Spoken, "may have arrived") || !strings.Contains(res.Spoken, "read_screen") || !strings.Contains(res.Spoken, "send it again") {
		t.Errorf("not on screen = %q", res.Spoken)
	}
	if strings.Contains(res.Spoken, "could not send") {
		t.Errorf("a written prompt was reported as not sent: %q", res.Spoken)
	}
	if _, ok := h.pending(); ok {
		t.Error("unconfirmed send left a pending action")
	}

	h.srv.SetScreen("w5:p1", "you said: run the\ntests\nworking...\n")
	res = h.send("w5:p1", "run the tests")
	if !strings.Contains(res.Spoken, "is on auth's screen, so it arrived") {
		t.Errorf("on screen = %q", res.Spoken)
	}
	// Duplicates are allowed: nothing blocks the resend.
	if got := h.promptRequests(); len(got) != 2 {
		t.Errorf("agent.prompt requests = %v, want both sends made", got)
	}
}
