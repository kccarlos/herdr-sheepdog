package attention

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// fleet is a Source the test edits.
type fleet struct {
	mu   sync.Mutex
	snap herdr.Snapshot
	err  error
}

func (f *fleet) Snapshot(context.Context) (herdr.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.err
}

// set replaces the fleet with one agent per pane spec.
func (f *fleet) set(panes ...pane) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = herdr.Snapshot{}
	for _, p := range panes {
		f.snap.Panes = append(f.snap.Panes, herdr.PaneInfo{PaneID: p.id, TerminalID: p.term})
		if p.state != "" {
			f.snap.Agents = append(f.snap.Agents, herdr.AgentInfo{
				PaneID: p.id, TerminalID: p.term, Status: p.state, CompletionSeq: p.cs,
			})
		}
	}
}

type pane struct {
	id, term string
	state    herdr.State
	cs       *uint64
}

func cs(n uint64) *uint64 { return &n }

func newLocal(t *testing.T, f *fleet) (*Local, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	l, err := NewLocal(path, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return l, path
}

func refresh(t *testing.T, l *Local) {
	t.Helper()
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// marksOf is the marks of the one pane most tests use, w1:p1.
func marksOf(t *testing.T, l *Local) herdr.Marks {
	t.Helper()
	all, err := l.AllMarks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := all["w1:p1"]
	if !ok {
		t.Fatalf("no marks for w1:p1 in %+v", all)
	}
	return m
}

func signals(l *Local) []string {
	var out []string
	for {
		select {
		case id := <-l.MarkChanges():
			out = append(out, id)
		default:
			return out
		}
	}
}

func TestTurnIsTheHighestCompletionSeqSeen(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)

	f.set(pane{"w1:p1", "term_1", herdr.StateWorking, nil})
	refresh(t, l)
	if m := marksOf(t, l); m.Turn != 0 || m.Word != herdr.StateWorking {
		t.Fatalf("working before any turn = %+v", m)
	}

	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(1)})
	refresh(t, l)
	if m := marksOf(t, l); m.Turn != 1 || !m.Unread() {
		t.Fatalf("after a turn = %+v", m)
	}

	// completion_seq is null while the agent works: the store remembers.
	f.set(pane{"w1:p1", "term_1", herdr.StateWorking, nil})
	refresh(t, l)
	if m := marksOf(t, l); m.Turn != 1 {
		t.Fatalf("turn while working = %d, want 1 kept", m.Turn)
	}
}

func TestTurnMovesAreSignalled(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	refresh(t, l)
	if got := signals(l); len(got) != 0 {
		t.Fatalf("signals for an unchanged pane = %v", got)
	}

	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(1)})
	refresh(t, l)
	if got := signals(l); len(got) != 1 || got[0] != "w1:p1" {
		t.Fatalf("signals after a turn = %v", got)
	}
	refresh(t, l)
	if got := signals(l); len(got) != 0 {
		t.Fatalf("signals for a snapshot with no change = %v", got)
	}
}

// done turning idle with no work in between is a person focusing the pane
// in a TUI client: seen.
func TestDoneTurningIdleIsSeen(t *testing.T) {
	tests := []struct {
		name     string
		next     pane
		wantSeen bool
	}{
		{"focused", pane{"w1:p1", "term_1", herdr.StateIdle, cs(2)}, true},
		{"worked and finished again", pane{"w1:p1", "term_1", herdr.StateIdle, cs(3)}, false},
		{"went blocked", pane{"w1:p1", "term_1", herdr.StateBlocked, cs(2)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fleet{}
			l, _ := newLocal(t, f)
			f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(2)})
			refresh(t, l)
			signals(l)
			if m := marksOf(t, l); !m.Unread() {
				t.Fatalf("a done pane nobody focused = %+v, want unread", m)
			}

			f.set(tc.next)
			refresh(t, l)
			m := marksOf(t, l)
			if got := m.Seen != nil && *m.Seen == 2; got != tc.wantSeen {
				t.Fatalf("marks = %+v, want seen at 2: %v", m, tc.wantSeen)
			}
			if tc.wantSeen && (m.Unread() || len(signals(l)) != 1) {
				t.Fatalf("focus not seen and signalled: %+v", m)
			}
		})
	}
}

func TestFirstSnapshotNeverGuessesAFocus(t *testing.T) {
	// After a daemon restart the previous word is unknown, so an idle agent
	// with a turn is simply read-by-word, not "focused".
	f := &fleet{}
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, cs(4)})
	l, _ := newLocal(t, f)
	refresh(t, l)
	if m := marksOf(t, l); m.Seen != nil || m.Turn != 4 || m.Unread() {
		t.Fatalf("marks = %+v, want turn 4, no record, read by its word", m)
	}
}

func TestCounterResetDropsTurnAndSeen(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(7)})
	refresh(t, l)
	if _, err := l.MarkSeen(context.Background(), "w1:p1", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetPinned(context.Background(), "w1:p1", true); err != nil {
		t.Fatal(err)
	}

	// herdr restarted: its counters start again, below the stored turn.
	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(1)})
	refresh(t, l)
	m := marksOf(t, l)
	if m.Turn != 1 || m.Seen != nil || !m.Unread() {
		t.Fatalf("after a reset: %+v, want turn 1, no seen, unread by its word", m)
	}
	if !m.Pinned {
		t.Fatal("a counter reset must not unpin")
	}
}

func TestMarksAreKeyedByTerminalNotByReusedPaneID(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	refresh(t, l)
	if _, err := l.SetPinned(context.Background(), "w1:p1", true); err != nil {
		t.Fatal(err)
	}

	// The terminal closes and herdr hands the pane id to a new terminal.
	f.set(pane{"w1:p1", "term_2", herdr.StateIdle, nil})
	refresh(t, l)
	if m := marksOf(t, l); m.Pinned {
		t.Fatalf("a new terminal in a reused pane id is pinned: %+v", m)
	}
	if got := len(l.recs); got != 0 {
		t.Fatalf("store holds %d records after the pinned terminal closed, want 0", got)
	}
}

func TestPanesWithoutATerminalIDAreKeyedByPaneID(t *testing.T) {
	f := &fleet{}
	l, path := newLocal(t, f)
	f.set(pane{"w1:p1", "", herdr.StateIdle, nil})
	refresh(t, l)
	if _, err := l.SetPinned(context.Background(), "w1:p1", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pane:w1:p1"`) {
		t.Fatalf("store file has no pane: key:\n%s", data)
	}
}

func TestPruneKeepsAShellButDropsAClosedTerminal(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil}, pane{"w1:p2", "term_2", herdr.StateIdle, nil})
	refresh(t, l)
	for _, id := range []string{"w1:p1", "w1:p2"} {
		if _, err := l.SetPinned(context.Background(), id, true); err != nil {
			t.Fatal(err)
		}
	}

	// p1's agent exits (the shell stays); p2's terminal closes.
	f.set(pane{"w1:p1", "term_1", "", nil})
	refresh(t, l)
	if m := marksOf(t, l); !m.Pinned {
		t.Fatal("a pin did not survive its agent")
	}
	if all, _ := l.AllMarks(context.Background()); len(all) != 1 {
		t.Fatalf("AllMarks = %+v, want only the shell", all)
	}
	if _, ok := l.recs["term_2"]; ok {
		t.Fatal("the closed terminal's record is still stored")
	}
}

func TestWritesFollowMarkSeenAndUnread(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(3)})
	refresh(t, l)
	ctx := context.Background()

	writes := l.Writes()
	m, err := l.MarkSeen(ctx, "w1:p1", 2)
	if err != nil || m.Seen == nil || *m.Seen != 2 || !m.Unread() {
		t.Fatalf("MarkSeen(2) = %+v, %v", m, err)
	}
	if l.Writes() != writes+1 {
		t.Fatalf("writes = %d, want one more", l.Writes())
	}
	if m, _ = l.MarkSeen(ctx, "w1:p1", 1); *m.Seen != 2 || l.Writes() != writes+1 {
		t.Fatalf("an older turn changed something: %+v writes %d", m, l.Writes())
	}
	if m, _ = l.MarkSeen(ctx, "w1:p1", 9); *m.Seen != 3 || m.Unread() {
		t.Fatalf("MarkSeen(9) = %+v, want clamped to 3", m)
	}
	if m, _ = l.MarkUnread(ctx, "w1:p1"); *m.Seen != 2 || !m.Unread() {
		t.Fatalf("MarkUnread = %+v, want seen 2", m)
	}
}

func TestUnpinningTheLastMarkLeavesNoRecord(t *testing.T) {
	f := &fleet{}
	l, path := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	refresh(t, l)
	ctx := context.Background()

	if _, err := l.SetPinned(ctx, "w1:p1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetPinned(ctx, "w1:p1", false); err != nil {
		t.Fatal(err)
	}
	if len(l.recs) != 0 {
		t.Fatalf("records = %+v, want none", l.recs)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "term_1") {
		t.Fatalf("the file still names the terminal:\n%s", data)
	}
	// Unpinning something that is not pinned writes nothing.
	writes := l.Writes()
	if _, err := l.SetPinned(ctx, "w1:p1", false); err != nil || l.Writes() != writes {
		t.Fatalf("a no-op unpin wrote: %v writes %d -> %d", err, writes, l.Writes())
	}
}

func TestUnknownPaneIsNotFound(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	ctx := context.Background()
	if _, err := l.MarkSeen(ctx, "w9:p9", 1); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("MarkSeen of an unknown pane = %v", err)
	}
	// A pane the store has not seen yet is found by looking again.
	if _, err := l.SetPinned(ctx, "w1:p1", true); err != nil {
		t.Fatalf("SetPinned of a new pane: %v", err)
	}
	f.mu.Lock()
	f.err = errors.New("herdr is down")
	f.mu.Unlock()
	if _, err := l.MarkSeen(ctx, "w5:p5", 1); err == nil || errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("MarkSeen with herdr down = %v, want the snapshot error", err)
	}
}

func TestAgentStartedMarksTheTurnSeenOnce(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	ctx := context.Background()
	f.set(pane{"w1:p1", "term_1", "", nil}) // a fresh shell
	if err := l.AgentStarted(ctx, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if m := marksOf(t, l); m.Seen == nil || *m.Seen != 0 || m.Unread() {
		t.Fatalf("a started agent = %+v, want seen 0", m)
	}

	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(4)})
	refresh(t, l)
	writes := l.Writes()
	if err := l.AgentStarted(ctx, "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if m := marksOf(t, l); *m.Seen != 0 || l.Writes() != writes {
		t.Fatalf("a pane that has a seen mark was marked again: %+v", m)
	}
	if err := l.AgentStarted(ctx, "w9:p9"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("AgentStarted of an unknown pane = %v", err)
	}
}

func TestStoreFile(t *testing.T) {
	f := &fleet{}
	l, path := newLocal(t, f)
	f.set(pane{"w1:p1", "term_65bb68", herdr.StateDone, cs(7)})
	refresh(t, l)
	ctx := context.Background()
	if _, err := l.MarkSeen(ctx, "w1:p1", 6); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetPinned(ctx, "w1:p1", true); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("data dir holds %d entries after saving, want only the store (no temp file left)", len(entries))
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{`"version": 1`, `"term_65bb68"`, `"pane_id": "w1:p1"`, `"turn": 7`, `"seen": 6`, `"seen_at"`, `"pinned": true`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("store file lacks %s:\n%s", want, data)
		}
	}

	// A new daemon opens the same file.
	again, err := NewLocal(path, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	refresh(t, again)
	m := marksOf(t, again)
	if m.Turn != 7 || m.Seen == nil || *m.Seen != 6 || !m.Pinned {
		t.Fatalf("reloaded marks = %+v", m)
	}
}

func TestStoreInTheDataDirIsCreatedPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", FileName)
	f := &fleet{}
	l, err := NewLocal(path, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	refresh(t, l)
	if _, err := l.SetPinned(context.Background(), "w1:p1", true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("data dir mode = %o, want 700", perm)
	}
}

func TestUnreadableStoreIsMovedAside(t *testing.T) {
	tests := map[string]string{
		"garbage":       "not json",
		"wrong version": `{"version": 2, "terminals": {"term_1": {"pinned": true}}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			var warn bytes.Buffer
			l, err := NewLocal(path, &fleet{}, Options{Warn: &warn})
			if err != nil {
				t.Fatal(err)
			}
			if len(l.recs) != 0 || !strings.Contains(warn.String(), path+".corrupt") {
				t.Fatalf("records %v, warning %q", l.recs, warn.String())
			}
			if got, err := os.ReadFile(path + ".corrupt"); err != nil || string(got) != body {
				t.Fatalf("aside file = %q, %v", got, err)
			}
		})
	}
}

func TestFailedSaveRollsTheWriteBack(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "data")
	f := &fleet{}
	l, err := NewLocal(filepath.Join(blocker, FileName), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.set(pane{"w1:p1", "term_1", herdr.StateIdle, nil})
	refresh(t, l)
	// The store's directory becomes a file, so saving cannot work.
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := l.SetPinned(context.Background(), "w1:p1", true); err == nil {
		t.Fatal("SetPinned succeeded though the store cannot be saved")
	}
	if m := marksOf(t, l); m.Pinned {
		t.Fatal("a write that failed to save is still in memory")
	}
}

func TestInMemoryStoreAndNoSource(t *testing.T) {
	// The mock has no herdr to ask and no data directory worth keeping.
	l, err := NewLocal("", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh without a source = %v", err)
	}
	if _, err := l.SetPinned(context.Background(), "w1:p1", true); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("SetPinned with nothing to pin = %v", err)
	}
	if err := l.Observe(herdr.Snapshot{Panes: []herdr.PaneInfo{{PaneID: "mock:1"}}}); err != nil {
		t.Fatal(err)
	}
	if m, err := l.SetPinned(context.Background(), "mock:1", true); err != nil || !m.Pinned {
		t.Fatalf("SetPinned = %+v, %v", m, err)
	}
}

func TestRunRefreshesOnPokeAndStopsWithItsContext(t *testing.T) {
	f := &fleet{}
	l, _ := newLocal(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx, time.Hour) // only a poke can refresh within this test
	}()

	f.set(pane{"w1:p1", "term_1", herdr.StateDone, cs(1)})
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.Poke()
		all, _ := l.AllMarks(ctx)
		if all["w1:p1"].Turn == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never refreshed after a poke")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}
