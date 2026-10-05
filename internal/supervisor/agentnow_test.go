package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// TestAgentState: the state sentence a briefing carries says how long
// the agent has been in its state and when it last finished work, or
// that no finish is recorded (the "has it started?" case).
func TestAgentState(t *testing.T) {
	now := time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		a    state.Agent
		want []string
	}{
		{state.Agent{State: herdr.StateIdle, StateSince: now.Add(-4 * time.Minute), LastResult: &state.Result{Spoken: "finished", At: now.Add(-4 * time.Minute)}},
			[]string{"as this reached you: idle, ready for input, for 4 min", "it last finished work 4 min ago"}},
		{state.Agent{State: herdr.StateIdle, StateSince: now.Add(-30 * time.Second)},
			[]string{"idle, ready for input, for 30 s", "no finished work recorded"}},
		{state.Agent{State: herdr.StateWorking, StateSince: now.Add(-90 * time.Minute)},
			[]string{"working for 1 h 30 min"}},
		{state.Agent{State: herdr.StateBlocked, StateSince: now.Add(-time.Minute)},
			[]string{"waiting at a prompt for an answer for 1 min"}},
		{state.Agent{State: herdr.StateUnknown}, []string{"no longer running"}},
	}
	for _, c := range cases {
		got := agentState(c.a, now)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("agentState(%s) = %q, want %q", c.a.State, got, w)
			}
		}
	}
}
