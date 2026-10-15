package brain

import (
	"strings"
	"testing"
	"time"
)

// The roster says how long a prompt has waited (docs/dev/history/bench-2026-10-03.md
// #8): past the fast-path minute the user's words reach the orchestrator, which
// weighs that fact itself.
func TestRosterSaysHowLongAPromptWaited(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 3, 13, 0, time.UTC)
	agents := []AgentInfo{{Alias: "bench-claude", Kind: "claude", State: "blocked", Pending: &PendingPrompt{ID: "ap-1", Question: "do you want to proceed", AskedAt: now.Add(-7*time.Minute - 26*time.Second)}}}
	got := renderRoster(agents, now)
	if !strings.Contains(got, "WAITING FOR AN ANSWER (asked 7 min ago): do you want to proceed") {
		t.Errorf("roster:\n%s", got)
	}
}
