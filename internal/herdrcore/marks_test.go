package herdrcore_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

// startMarks runs the backend's marks subscription until the test ends and
// waits for it to be up.
func startMarks(t *testing.T, b *herdrcore.Backend, srv *hctest.Server) {
	t.Helper()
	before := srv.Subscribers()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.RunMarks(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the marks subscription", func() bool { return srv.Subscribers() > before })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func allMarks(t *testing.T, b *herdrcore.Backend) map[string]herdr.Marks {
	t.Helper()
	m, err := b.AllMarks(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// expectSignal waits for MarkChanges to name address.
func expectSignal(t *testing.T, b *herdrcore.Backend, address string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case got := <-b.MarkChanges():
			if got == address {
				return
			}
		case <-timeout:
			t.Fatalf("MarkChanges never named %s", address)
		}
	}
}

// expectNoSignal fails when MarkChanges names anything within a short wait.
func expectNoSignal(t *testing.T, b *herdrcore.Backend) {
	t.Helper()
	select {
	case got := <-b.MarkChanges():
		t.Fatalf("MarkChanges named %s, want nothing", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func seenPtr(n uint64) *uint64 { return &n }

func TestMarksFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		turn   uint64
		status string
		want   herdr.Marks
	}{
		{"no labels is no record, not turn 0", nil, 4, "idle", herdr.Marks{Turn: 4, Word: herdr.StateIdle, Shared: true}},
		{"seen and pinned", map[string]string{"herdr.seen": "3", "herdr.pinned": "1"}, 4, "done",
			herdr.Marks{Turn: 4, Seen: seenPtr(3), Pinned: true, Word: herdr.StateDone, Shared: true}},
		{"seen 0 is a record", map[string]string{"herdr.seen": "0"}, 0, "idle",
			herdr.Marks{Seen: seenPtr(0), Word: herdr.StateIdle, Shared: true}},
		{"unparseable seen counts as missing", map[string]string{"herdr.seen": "abc"}, 2, "done",
			herdr.Marks{Turn: 2, Word: herdr.StateDone, Shared: true}},
		{"negative seen counts as missing", map[string]string{"herdr.seen": "-1"}, 2, "done",
			herdr.Marks{Turn: 2, Word: herdr.StateDone, Shared: true}},
		{"empty seen counts as missing", map[string]string{"herdr.seen": ""}, 2, "done",
			herdr.Marks{Turn: 2, Word: herdr.StateDone, Shared: true}},
		{"any non-empty pinned value pins", map[string]string{"herdr.pinned": "yes"}, 0, "idle",
			herdr.Marks{Pinned: true, Word: herdr.StateIdle, Shared: true}},
		{"error status needs you and reads as done", nil, 1, "error",
			herdr.Marks{Turn: 1, Word: herdr.StateDone, Error: true, Shared: true}},
		{"blocked", nil, 1, "blocked", herdr.Marks{Turn: 1, Word: herdr.StateBlocked, Shared: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, srv := newBackend(t)
			term := reported("t1", "claude-code", tc.status)
			term.Turn, term.Labels = tc.turn, tc.labels
			srv.Add(term)
			// A remote terminal and a finished one are not panes.
			srv.Add(reported("mac/t7", "claude-code", "done"))
			finished := reported("t2", "claude-code", "done")
			finished.State = "finished"
			srv.Add(finished)

			got := allMarks(t, b)
			if len(got) != 1 {
				t.Fatalf("AllMarks = %+v, want only t1", got)
			}
			if !reflect.DeepEqual(got["t1"], tc.want) {
				t.Fatalf("marks = %+v, want %+v", got["t1"], tc.want)
			}
		})
	}
}

// AllMarks agrees whether it reads the cache or asks herdr-core.
func TestAllMarksFromTheCacheMatchesAReadThrough(t *testing.T) {
	b, srv := newBackend(t)
	term := reported("t1", "claude-code", "done")
	term.Turn, term.Labels = 3, map[string]string{"herdr.seen": "1", "herdr.pinned": "1"}
	srv.Add(term)

	cold := allMarks(t, b)
	startMarks(t, b, srv)
	waitFor(t, "the cache", func() bool { return reflect.DeepEqual(allMarks(t, b), cold) })
}

func TestLabelWritesSendOnlyTheKeysThatChange(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	term := reported("t1", "claude-code", "done")
	term.Turn, term.Labels = 5, map[string]string{"herdr.seen": "1", "herdr.space": "s1"}
	srv.Add(term)

	labelCalls := func() []map[string]string {
		var out []map[string]string
		for _, p := range requestParams[struct {
			Terminal string            `json:"terminal"`
			Labels   map[string]string `json:"labels"`
		}](t, srv, "terminal.label") {
			out = append(out, p.Labels)
		}
		return out
	}
	step := func(name string, do func() (herdr.Marks, error), want []map[string]string) {
		t.Helper()
		if _, err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := labelCalls(); !reflect.DeepEqual(got, want) {
			t.Fatalf("after %s the label requests are %v, want %v", name, got, want)
		}
	}

	var want []map[string]string
	step("seen older", func() (herdr.Marks, error) { return b.MarkSeen(ctx, "t1", 0) }, want)
	want = append(want, map[string]string{"herdr.seen": "3"})
	step("seen 3", func() (herdr.Marks, error) { return b.MarkSeen(ctx, "t1", 3) }, want)
	want = append(want, map[string]string{"herdr.seen": "5"})
	step("seen beyond the turn", func() (herdr.Marks, error) { return b.MarkSeen(ctx, "t1", 99) }, want)
	step("seen again", func() (herdr.Marks, error) { return b.MarkSeen(ctx, "t1", 5) }, want)
	want = append(want, map[string]string{"herdr.seen": "4"})
	step("mark unread", func() (herdr.Marks, error) { return b.MarkUnread(ctx, "t1") }, want)
	step("mark unread again", func() (herdr.Marks, error) { return b.MarkUnread(ctx, "t1") }, want)
	want = append(want, map[string]string{"herdr.pinned": "1"})
	step("pin", func() (herdr.Marks, error) { return b.SetPinned(ctx, "t1", true) }, want)
	step("pin again", func() (herdr.Marks, error) { return b.SetPinned(ctx, "t1", true) }, want)
	want = append(want, map[string]string{"herdr.pinned": ""})
	step("unpin", func() (herdr.Marks, error) { return b.SetPinned(ctx, "t1", false) }, want)

	// The labels sheepdog does not own are untouched.
	got, err := (hc.NewClient(srv.Path)).Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Labels, map[string]string{"herdr.seen": "4", "herdr.space": "s1"}) {
		t.Fatalf("labels = %v", got.Labels)
	}
}

func TestOwnWritesDoNotSignalButOtherClientsDo(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	term := reported("t1", "claude-code", "done")
	term.Turn, term.Labels = 2, map[string]string{"herdr.seen": "0"}
	srv.Add(term)
	startMarks(t, b, srv)

	if _, err := b.MarkSeen(ctx, "t1", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetPinned(ctx, "t1", true); err != nil {
		t.Fatal(err)
	}
	expectNoSignal(t, b)

	srv.SetLabels("t1", map[string]string{"herdr.pinned": ""})
	expectSignal(t, b, "t1")
	if m := allMarks(t, b)["t1"]; m.Pinned || m.Seen == nil || *m.Seen != 2 {
		t.Fatalf("marks after another client's unpin = %+v", m)
	}
}

// The labels events that echo a burst of writes arrive after the writes: the
// cache must not slip back to an older state, nor take them for another
// client's.
func TestABurstOfOwnWritesStaysQuietAndKeepsTheLatestState(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	term := reported("t1", "claude-code", "done")
	term.Turn, term.Labels = 40, map[string]string{"herdr.seen": "0"}
	srv.Add(term)
	startMarks(t, b, srv)

	for i := uint64(1); i <= 20; i++ {
		if _, err := b.MarkSeen(ctx, "t1", i); err != nil {
			t.Fatal(err)
		}
		if _, err := b.SetPinned(ctx, "t1", i%2 == 1); err != nil {
			t.Fatal(err)
		}
		if m := allMarks(t, b)["t1"]; m.Seen == nil || *m.Seen != i || m.Pinned != (i%2 == 1) {
			t.Fatalf("round %d: cache = %+v, want seen %d pinned %v", i, m, i, i%2 == 1)
		}
	}
	expectNoSignal(t, b)
	if m := allMarks(t, b)["t1"]; m.Seen == nil || *m.Seen != 20 || m.Pinned {
		t.Fatalf("after the burst: %+v", m)
	}

	// Another client's write after the burst still gets through.
	srv.SetLabels("t1", map[string]string{"herdr.pinned": "1"})
	expectSignal(t, b, "t1")
	waitFor(t, "the pin", func() bool { return allMarks(t, b)["t1"].Pinned })
}

func TestATurnThatEndsSignalsAndRaisesTheTurn(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "working"))
	startMarks(t, b, srv)

	srv.SetStatus("t1", &hc.Status{State: "done", App: "claude-code"})
	expectSignal(t, b, "t1")
	waitFor(t, "turn 1", func() bool { return allMarks(t, b)["t1"].Turn == 1 })
	if m := allMarks(t, b)["t1"]; m.Word != herdr.StateDone {
		t.Fatalf("word = %s, want done", m.Word)
	}
}

func TestAStatusChangeAloneIsNotASignal(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	startMarks(t, b, srv)

	srv.SetStatus("t1", &hc.Status{State: "blocked", App: "claude-code"})
	waitFor(t, "the new word", func() bool { return allMarks(t, b)["t1"].Word == herdr.StateBlocked })
	expectNoSignal(t, b)
}

func TestMarksFollowTerminalsCreatedAndGone(t *testing.T) {
	b, srv := newBackend(t)
	startMarks(t, b, srv)

	created, err := hc.NewClient(srv.Path).Run(testContext(t), hc.RunParams{Argv: []string{"zsh"}, Labels: map[string]string{"herdr.seen": "0"}})
	if err != nil {
		t.Fatal(err)
	}
	expectSignal(t, b, created.Address)
	waitFor(t, "the new terminal", func() bool { _, ok := allMarks(t, b)[created.Address]; return ok })

	srv.Finish(created.Address, "exited")
	waitFor(t, "the terminal to leave", func() bool { _, ok := allMarks(t, b)[created.Address]; return !ok })
}

// After the connection drops, or herdr-core says the subscriber missed
// events, the next snapshot replaces the cache and tells MarkChanges what
// moved while nobody listened.
func TestMarksResyncAfterMissedAndAfterAReconnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		cut  func(*hctest.Server)
	}{
		{"missed", func(s *hctest.Server) { s.Missed() }},
		{"reconnect", func(s *hctest.Server) { s.DropSubscribers() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, srv := newBackend(t)
			term := reported("t1", "claude-code", "done")
			term.Turn, term.Labels = 2, map[string]string{"herdr.seen": "2"}
			srv.Add(term)
			startMarks(t, b, srv)

			// A change the event stream never carried (Add emits nothing).
			term.Labels = map[string]string{"herdr.seen": "2", "herdr.pinned": "1"}
			srv.Add(term)
			tc.cut(srv)

			expectSignal(t, b, "t1")
			waitFor(t, "the pin", func() bool { return allMarks(t, b)["t1"].Pinned })

			// The stream is back: a new change arrives as an event again.
			srv.SetLabels("t1", map[string]string{"herdr.pinned": ""})
			waitFor(t, "the unpin", func() bool { return !allMarks(t, b)["t1"].Pinned })
		})
	}
}

func TestMarkWritesRefuseTerminalsThatAreNotLocalPanes(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	srv.Add(reported("mac/t7", "claude-code", "done"))
	finished := reported("t2", "claude-code", "done")
	finished.State = "finished"
	srv.Add(finished)

	for _, pane := range []string{"mac/t7", "t2", "t99"} {
		if _, err := b.MarkSeen(ctx, pane, 1); !errors.Is(err, herdr.ErrPaneNotFound) {
			t.Errorf("MarkSeen(%s) error = %v, want ErrPaneNotFound", pane, err)
		}
		if _, err := b.SetPinned(ctx, pane, true); !errors.Is(err, herdr.ErrPaneNotFound) {
			t.Errorf("SetPinned(%s) error = %v, want ErrPaneNotFound", pane, err)
		}
	}
	if n := len(requestParams[map[string]any](t, srv, "terminal.label")); n != 0 {
		t.Fatalf("%d terminal.label requests for panes that do not exist", n)
	}
}

// An agent sheepdog starts in a terminal that has no herdr.seen joins the
// others at its current turn, so it is not unread before its first turn.
func TestStartAgentMarksAMissingSeen(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	b.LookPath = func(string) (string, error) { return "/bin/true", nil }
	bare := liveTerminal("t1", "zsh")
	bare.Turn = 3
	srv.Add(bare)
	labelled := liveTerminal("t2", "zsh")
	labelled.Turn, labelled.Labels = 3, map[string]string{"herdr.seen": "1"}
	srv.Add(labelled)

	for _, pane := range []string{"t1", "t2"} {
		if err := b.StartAgent(ctx, pane, "", "claude", nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	calls := requestParams[struct {
		Terminal string            `json:"terminal"`
		Labels   map[string]string `json:"labels"`
	}](t, srv, "terminal.label")
	if len(calls) != 1 || calls[0].Terminal != "t1" || !reflect.DeepEqual(calls[0].Labels, map[string]string{"herdr.seen": "3"}) {
		t.Fatalf("terminal.label requests = %+v, want one: t1 herdr.seen=3", calls)
	}
}

func TestConcurrentSeenWritesNeverLowerTheMark(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	term := reported("t1", "claude-code", "done")
	term.Turn, term.Labels = 50, map[string]string{"herdr.seen": "0"}
	srv.Add(term)
	startMarks(t, b, srv)

	done := make(chan error)
	for turn := uint64(50); turn > 0; turn-- {
		go func() {
			_, err := b.MarkSeen(ctx, "t1", turn)
			done <- err
		}()
	}
	for range 50 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, err := hc.NewClient(srv.Path).Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["herdr.seen"] != "50" {
		t.Fatalf("herdr.seen = %q after concurrent writes of 1..50, want 50", got.Labels["herdr.seen"])
	}
}
