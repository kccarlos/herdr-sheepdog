package brain

import (
	"strings"
	"testing"
	"time"
)

// The roster says how long a prompt has waited (docs/history/bench-2026-10-03.md
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

// The roster carries the investigation facts as information only.
func TestRosterShowsSessionFacts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	got := renderRoster([]AgentInfo{{Alias: "auth", Kind: "claude", State: "idle", Session: &SessionFacts{Tokens: 360_000, LastActive: now.Add(-3 * time.Hour), ForkUSD: 2.88, SearchUSD: 0.2}}}, now)
	for _, want := range []string{"session ~360k tokens", "last active 3 h ago", "fork ~$2.88", "search ~$0.20"} {
		if !strings.Contains(got, want) {
			t.Errorf("roster missing %q:\n%s", want, got)
		}
	}
}
