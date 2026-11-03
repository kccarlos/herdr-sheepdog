package conformance

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// markScenarios are the seen and pinned behaviours both backends share.
// Each name is what QA greps `go test -v` for.
func markScenarios() []Scenario {
	return []Scenario{
		{Name: "a finished turn is unread until seen", Run: finishedTurnIsUnreadUntilSeen},
		{Name: "seen never goes down", Run: seenNeverGoesDown},
		{Name: "seen for an older turn leaves newer work unread", Run: seenForAnOlderTurn},
		{Name: "mark unread", Run: markUnread},
		{Name: "another client's seen clears unread", Run: anotherClientsSeen},
		{Name: "no record falls back to the backend's word", Run: noRecordUsesTheWord},
		{Name: "blocked and working are never unread", Run: blockedAndWorkingAreNeverUnread},
		{Name: "a blocked→idle with no work keeps the turn", Run: blockedToIdleKeepsTheTurn},
		{Name: "pin survives the agent, not the terminal", Run: pinSurvivesTheAgentNotTheTerminal},
		{Name: "a resumed session in the same terminal keeps its marks", Run: resumedSessionKeepsItsMarks},
		{Name: "marks survive a daemon restart", Run: marksSurviveARestart},
		{Name: "another client's pin shows up", Run: anotherClientsPin},
		{Name: "sheepdog-started agents start seen", Run: startedAgentsStartSeen},
	}
}

const markWait = 3 * time.Second

// marksFor reads the marks of one pane after the marker settled.
func marksFor(ctx context.Context, d Driver, paneID string) (herdr.Marks, bool, error) {
	if err := d.Settle(ctx); err != nil {
		return herdr.Marks{}, false, err
	}
	all, err := d.Marker().AllMarks(ctx)
	if err != nil {
		return herdr.Marks{}, false, err
	}
	m, ok := all[paneID]
	return m, ok, nil
}

// waitMarks polls until the pane's marks satisfy ok. hc learns of a change
// from its event stream, so a read right after a write can be a moment
// early.
func waitMarks(t *testing.T, ctx context.Context, d Driver, paneID, what string, ok func(herdr.Marks) bool) herdr.Marks {
	t.Helper()
	deadline := time.Now().Add(markWait)
	for {
		m, found, err := marksFor(ctx, d, paneID)
		if err != nil {
			t.Fatal(err)
		}
		if found && ok(m) {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("marks of %s never became %s; last (found=%v) %s", paneID, what, found, describe(m))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func describe(m herdr.Marks) string {
	seen := "none"
	if m.Seen != nil {
		seen = strconv.FormatUint(*m.Seen, 10)
	}
	return fmt.Sprintf("turn %d, seen %s, pinned %v, word %s", m.Turn, seen, m.Pinned, m.Word)
}

func hasTurn(n uint64) func(herdr.Marks) bool {
	return func(m herdr.Marks) bool { return m.Turn == n }
}

func isSeen(m herdr.Marks, n uint64) bool { return m.Seen != nil && *m.Seen == n }

// agentState is the state ListAgents reports for one pane.
func agentState(t *testing.T, ctx context.Context, d Driver, paneID string) herdr.State {
	t.Helper()
	agents, err := d.Backend().ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.PaneID == paneID {
			return a.Status
		}
	}
	t.Fatalf("no agent in %s: %+v", paneID, agents)
	return ""
}

// classified is the attention the list shows for the pane.
func classified(t *testing.T, ctx context.Context, d Driver, paneID string, m herdr.Marks) attention.Attention {
	t.Helper()
	return attention.Classify(agentState(t, ctx, d, paneID), m)
}

// startedAgent is a claude that sheepdog started in a new folder, idle.
func startedAgent(t *testing.T, ctx context.Context, d Driver) herdr.PaneInfo {
	t.Helper()
	pane, err := d.StartAgent(ctx, t.TempDir(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "known", func(herdr.Marks) bool { return true })
	return pane
}

// finishTurn ends one turn and waits until the marks are at turn want.
func finishTurn(t *testing.T, ctx context.Context, d Driver, paneID string, want uint64) herdr.Marks {
	t.Helper()
	if err := d.EndTurn(ctx, paneID); err != nil {
		t.Fatal(err)
	}
	return waitMarks(t, ctx, d, paneID, fmt.Sprintf("turn %d", want), hasTurn(want))
}

func markSeen(t *testing.T, ctx context.Context, d Driver, paneID string, turn uint64) herdr.Marks {
	t.Helper()
	m, err := d.Marker().MarkSeen(ctx, paneID, turn)
	if err != nil {
		t.Fatalf("MarkSeen(%d): %v", turn, err)
	}
	return m
}

func wantSeen(t *testing.T, m herdr.Marks, n uint64) {
	t.Helper()
	if !isSeen(m, n) {
		t.Fatalf("marks = %s, want seen %d", describe(m), n)
	}
}

func wantUnread(t *testing.T, m herdr.Marks, want bool) {
	t.Helper()
	if got := m.Unread(); got != want {
		t.Fatalf("unread = %v, want %v; marks %s", got, want, describe(m))
	}
}

// expectChange waits for MarkChanges to name paneID.
func expectChange(t *testing.T, d Driver, paneID string) {
	t.Helper()
	timeout := time.After(markWait)
	for {
		select {
		case got := <-d.Marker().MarkChanges():
			if got == paneID {
				return
			}
		case <-timeout:
			t.Fatalf("MarkChanges never named %s", paneID)
		}
	}
}

// drainChanges drops the signals already queued.
func drainChanges(d Driver) {
	for {
		select {
		case <-d.Marker().MarkChanges():
		default:
			return
		}
	}
}

func finishedTurnIsUnreadUntilSeen(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)

	m := finishTurn(t, ctx, d, pane.PaneID, 1)
	wantUnread(t, m, true)
	if got := classified(t, ctx, d, pane.PaneID, m); got.Group != attention.GroupDone || !got.Unread {
		t.Fatalf("after a turn = %+v, want done and unread", got)
	}

	m = markSeen(t, ctx, d, pane.PaneID, 1)
	wantSeen(t, m, 1)
	wantUnread(t, m, false)

	m = finishTurn(t, ctx, d, pane.PaneID, 2)
	wantUnread(t, m, true)
}

func seenNeverGoesDown(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	finishTurn(t, ctx, d, pane.PaneID, 1)
	finishTurn(t, ctx, d, pane.PaneID, 2)

	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 2), 2)
	writes := d.MarkWrites()

	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 1), 2)
	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 2), 2)
	if got := d.MarkWrites(); got != writes {
		t.Fatalf("writes = %d after seeing an older or the same turn, want %d", got, writes)
	}
	m, _, err := marksFor(ctx, d, pane.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	wantSeen(t, m, 2)
}

func seenForAnOlderTurn(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	finishTurn(t, ctx, d, pane.PaneID, 1)
	finishTurn(t, ctx, d, pane.PaneID, 2)

	m := markSeen(t, ctx, d, pane.PaneID, 1)
	wantSeen(t, m, 1)
	wantUnread(t, m, true)

	// A turn above the current one is clamped to it, not refused.
	m = markSeen(t, ctx, d, pane.PaneID, 5)
	wantSeen(t, m, 2)
	wantUnread(t, m, false)
}

func markUnread(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)

	// Turn 0: no turn has finished, so there is nothing to mark.
	writes := d.MarkWrites()
	m, err := d.Marker().MarkUnread(ctx, pane.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	wantSeen(t, m, 0)
	wantUnread(t, m, false)
	if got := d.MarkWrites(); got != writes {
		t.Fatalf("writes = %d after marking unread at turn 0, want %d", got, writes)
	}

	finishTurn(t, ctx, d, pane.PaneID, 1)
	finishTurn(t, ctx, d, pane.PaneID, 2)
	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 2), 2)

	m, err = d.Marker().MarkUnread(ctx, pane.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	wantSeen(t, m, 1)
	wantUnread(t, m, true)

	// Already unread: marking again writes nothing.
	writes = d.MarkWrites()
	if _, err := d.Marker().MarkUnread(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	if got := d.MarkWrites(); got != writes {
		t.Fatalf("writes = %d after marking an unread agent unread, want %d", got, writes)
	}
}

func anotherClientsSeen(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	m := finishTurn(t, ctx, d, pane.PaneID, 1)
	wantUnread(t, m, true)
	drainChanges(d)

	if err := d.OtherClientSeen(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "read", func(m herdr.Marks) bool { return !m.Unread() })
	expectChange(t, d, pane.PaneID)
}

func noRecordUsesTheWord(t *testing.T, newDriver NewDriver) {
	for _, tc := range []struct {
		state      herdr.State
		wantUnread bool
	}{
		{herdr.StateIdle, false},
		{herdr.StateDone, true},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			d := newDriver(t)
			ctx := testContext(t)
			pane, err := d.ForeignAgent(ctx, t.TempDir(), "codex", tc.state)
			if err != nil {
				t.Fatal(err)
			}
			m := waitMarks(t, ctx, d, pane.PaneID, "known", func(m herdr.Marks) bool { return m.Word == tc.state })
			if m.Seen != nil {
				t.Fatalf("a terminal nobody marked has seen %d, want no record", *m.Seen)
			}
			wantUnread(t, m, tc.wantUnread)
			if got := classified(t, ctx, d, pane.PaneID, m); got.Group != attention.GroupDone || got.Unread != tc.wantUnread {
				t.Fatalf("attention = %+v, want done with unread %v", got, tc.wantUnread)
			}
		})
	}
}

func blockedAndWorkingAreNeverUnread(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	finishTurn(t, ctx, d, pane.PaneID, 1)

	step := func(state herdr.State, group attention.Group, unread bool) {
		t.Helper()
		if err := d.SetState(ctx, pane.PaneID, state); err != nil {
			t.Fatal(err)
		}
		m := waitMarks(t, ctx, d, pane.PaneID, string(state), func(m herdr.Marks) bool { return m.Word == state })
		got := classified(t, ctx, d, pane.PaneID, m)
		if got.Group != group || got.Unread != unread {
			t.Fatalf("while %s: %+v, want group %s unread %v (marks %s)", state, got, group, unread, describe(m))
		}
	}
	step(herdr.StateBlocked, attention.GroupNeedsYou, false)
	step(herdr.StateIdle, attention.GroupDone, true) // unread comes back
	step(herdr.StateWorking, attention.GroupWorking, false)

	m := finishTurn(t, ctx, d, pane.PaneID, 2)
	if got := classified(t, ctx, d, pane.PaneID, m); got.Group != attention.GroupDone || !got.Unread {
		t.Fatalf("after the turn: %+v, want done and unread", got)
	}
}

func blockedToIdleKeepsTheTurn(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	finishTurn(t, ctx, d, pane.PaneID, 1)
	wantUnread(t, markSeen(t, ctx, d, pane.PaneID, 1), false)

	for _, state := range []herdr.State{herdr.StateBlocked, herdr.StateIdle} {
		if err := d.SetState(ctx, pane.PaneID, state); err != nil {
			t.Fatal(err)
		}
		waitMarks(t, ctx, d, pane.PaneID, string(state), func(m herdr.Marks) bool { return m.Word == state })
	}
	m, _, err := marksFor(ctx, d, pane.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Turn != 1 {
		t.Fatalf("turn = %d after an approval that ended without work, want 1", m.Turn)
	}
	wantUnread(t, m, false)
}

func pinSurvivesTheAgentNotTheTerminal(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	dir := t.TempDir()
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	m, err := d.Marker().SetPinned(ctx, pane.PaneID, true)
	if err != nil || !m.Pinned {
		t.Fatalf("SetPinned = %s, %v", describe(m), err)
	}

	if err := d.Exit(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "pinned with no agent", func(m herdr.Marks) bool { return m.Pinned })
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "pinned under a different agent", func(m herdr.Marks) bool { return m.Pinned })

	// The pin is the terminal's: closing it ends it, and a new terminal in
	// the same folder starts without.
	if err := shells.Close(ctx, d.ShellHost(), pane.PaneID); err != nil {
		t.Fatal(err)
	}
	fresh, reused, err := shells.Open(ctx, d.ShellHost(), dir)
	if err != nil || reused {
		t.Fatalf("new shell = %+v reused=%v err=%v", fresh, reused, err)
	}
	m = waitMarks(t, ctx, d, fresh.PaneID, "known", func(herdr.Marks) bool { return true })
	if m.Pinned {
		t.Fatalf("a new terminal in %s is pinned: %s", dir, describe(m))
	}
}

func resumedSessionKeepsItsMarks(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	finishTurn(t, ctx, d, pane.PaneID, 1)
	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 1), 1)
	if m, err := d.Marker().SetPinned(ctx, pane.PaneID, true); err != nil || !m.Pinned {
		t.Fatalf("SetPinned = %s, %v", describe(m), err)
	}

	if err := d.Exit(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{
		Kind: "claude", App: "claude-code", Args: []string{"--resume"}, Reported: true, State: herdr.StateIdle,
	}); err != nil {
		t.Fatal(err)
	}
	m := waitMarks(t, ctx, d, pane.PaneID, "kept", func(m herdr.Marks) bool { return m.Pinned && isSeen(m, 1) })
	wantUnread(t, m, false)
}

func marksSurviveARestart(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	finishTurn(t, ctx, d, pane.PaneID, 1)
	wantSeen(t, markSeen(t, ctx, d, pane.PaneID, 1), 1)
	if m, err := d.Marker().SetPinned(ctx, pane.PaneID, true); err != nil || !m.Pinned {
		t.Fatalf("SetPinned = %s, %v", describe(m), err)
	}
	finishTurn(t, ctx, d, pane.PaneID, 2)

	again := d.RestartDaemon(t)
	m := waitMarks(t, ctx, again, pane.PaneID, "the same", func(m herdr.Marks) bool { return m.Pinned && isSeen(m, 1) && m.Turn == 2 })
	wantUnread(t, m, true)
}

func anotherClientsPin(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane := startedAgent(t, ctx, d)
	m := waitMarks(t, ctx, d, pane.PaneID, "known", func(herdr.Marks) bool { return true })
	drainChanges(d)

	if err := d.OtherClientPin(ctx, pane.PaneID, true); err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "pinned", func(m herdr.Marks) bool { return m.Pinned })
	if m.Shared {
		// Marks every client shares: the pin reaches this one as a change.
		expectChange(t, d, pane.PaneID)
	}

	if err := d.OtherClientPin(ctx, pane.PaneID, false); err != nil {
		t.Fatal(err)
	}
	waitMarks(t, ctx, d, pane.PaneID, "unpinned", func(m herdr.Marks) bool { return !m.Pinned })
}

func startedAgentsStartSeen(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	pane, err := d.StartAgent(ctx, t.TempDir(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	m := waitMarks(t, ctx, d, pane.PaneID, "started", func(m herdr.Marks) bool { return m.Seen != nil })
	wantSeen(t, m, 0)
	wantUnread(t, m, false)
}
