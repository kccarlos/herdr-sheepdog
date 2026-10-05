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

// TestAgentStateTaskFacts: the roster line a briefing carries quotes the
// worker's task title and the instruction it is on (without the
// sign-off), and says its last activity on record; an idle agent's old
// instruction is left out.
func TestAgentStateTaskFacts(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	a := state.Agent{State: herdr.StateWorking, StateSince: now.Add(-time.Minute), Title: "README for calc",
		CurrentTask: withSignOff("Write a README for the calc repo: what it does, how to build, how to run the tests."),
		Journal:     []state.JournalEntry{{At: now.Add(-3 * time.Minute), Event: "done", LastAssistant: "Fixed Add()."}}}
	got := agentState(a, now)
	for _, w := range []string{`task title "README for calc"`, `instruction it is on "Write a README for the calc repo: what it does, how to build, how to run the tests."`, "last activity on record: done 3 min ago"} {
		if !strings.Contains(got, w) {
			t.Errorf("agentState = %q, want %q", got, w)
		}
	}
	if strings.Contains(got, SignOff) {
		t.Errorf("agentState carries the sign-off: %q", got)
	}
	a.State = herdr.StateIdle
	if got := agentState(a, now); strings.Contains(got, "instruction it is on") {
		t.Errorf("idle agentState = %q, want no current instruction", got)
	}
	if got := agentState(state.Agent{State: herdr.StateWorking}, now); strings.Contains(got, "task title") || strings.Contains(got, "last activity") {
		t.Errorf("bare agentState = %q", got)
	}
}
