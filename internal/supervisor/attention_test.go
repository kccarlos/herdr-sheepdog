package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// fakeMarker is a herdr.Marker over a map the test edits.
type fakeMarker struct {
	mu      sync.Mutex
	marks   map[string]herdr.Marks
	err     error
	changes chan string
	writes  int
}

func newFakeMarker() *fakeMarker {
	return &fakeMarker{marks: map[string]herdr.Marks{}, changes: make(chan string, 8)}
}

func (f *fakeMarker) AllMarks(context.Context) (map[string]herdr.Marks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]herdr.Marks, len(f.marks))
	for k, v := range f.marks {
		out[k] = v
	}
	return out, nil
}

func (f *fakeMarker) get(paneID string) (herdr.Marks, error) {
	if f.err != nil {
		return herdr.Marks{}, f.err
	}
	m, ok := f.marks[paneID]
	if !ok {
		return herdr.Marks{}, herdr.ErrPaneNotFound
	}
	return m, nil
}

func (f *fakeMarker) MarkSeen(_ context.Context, paneID string, turn uint64) (herdr.Marks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.get(paneID)
	if err != nil {
		return herdr.Marks{}, err
	}
	if seen, write := m.SeenWrite(turn); write {
		m.Seen = &seen
		f.marks[paneID] = m
		f.writes++
	}
	return m, nil
}

func (f *fakeMarker) MarkUnread(_ context.Context, paneID string) (herdr.Marks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.get(paneID)
	if err != nil {
		return herdr.Marks{}, err
	}
	if seen, write := m.UnreadWrite(); write {
		m.Seen = &seen
		f.marks[paneID] = m
		f.writes++
	}
	return m, nil
}

func (f *fakeMarker) SetPinned(_ context.Context, paneID string, pinned bool) (herdr.Marks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.get(paneID)
	if err != nil {
		return herdr.Marks{}, err
	}
	m.Pinned = pinned
	f.marks[paneID] = m
	f.writes++
	return m, nil
}

func (f *fakeMarker) MarkChanges() <-chan string { return f.changes }

func u64(n uint64) *uint64 { return &n }

func newAttentionWorld(t *testing.T) (*harness, *fakeMarker) {
	t.Helper()
	fm := newFakeMarker()
	h := newHarness(t, func(c *Config) { c.Marker = fm })
	// The marks are there before the agents are: a transition's first sight of
	// a pane takes them as the baseline for its next change.
	fm.marks["w5:p1"] = herdr.Marks{Turn: 3, Seen: u64(2), Word: herdr.StateDone, Shared: true}
	fm.marks["w6:p1"] = herdr.Marks{Turn: 1, Seen: u64(1), Word: herdr.StateWorking, Pinned: true, Shared: true}
	fm.marks["w7:p1"] = herdr.Marks{Turn: 0, Word: herdr.StateIdle}
	h.addAgent("w5:p1", "auth", "codex", herdr.StateDone)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	h.addAgent("w7:p1", "ops", "codex", herdr.StateBlocked)
	return h, fm
}

// /api/state's attention: group, unread, pinned, the counts, and the
// fallback for a pane the marker does not follow.
func TestAttentionState(t *testing.T) {
	h, _ := newAttentionWorld(t)
	h.addAgent("w8:p1", "synth", "codex", herdr.StateDone) // no marks: the state's word decides
	st := h.sup.AttentionState(context.Background(), h.store.List())
	if !st.Available {
		t.Fatal("attention is not available")
	}
	tests := []struct {
		pane string
		want AgentAttention
	}{
		{"w5:p1", AgentAttention{Group: attention.GroupDone, Unread: true, Turn: 3, Seen: u64(2), Shared: true}},
		{"w6:p1", AgentAttention{Group: attention.GroupWorking, Pinned: true, Turn: 1, Seen: u64(1), Shared: true}},
		{"w7:p1", AgentAttention{Group: attention.GroupNeedsYou}},
		{"w8:p1", AgentAttention{Group: attention.GroupDone, Unread: true}},
	}
	for _, tc := range tests {
		got, ok := st.Agents[tc.pane]
		if !ok || !sameAttention(got, tc.want) || got.Shared != tc.want.Shared || got.Muted {
			t.Errorf("%s attention = %+v, want %+v", tc.pane, got, tc.want)
		}
	}
	if want := (AttentionCounts{NeedsYou: 1, Working: 1, DoneUnread: 2}); st.Counts != want {
		t.Errorf("counts = %+v, want %+v", st.Counts, want)
	}
	if len(st.SpaceMutes) != 0 {
		t.Errorf("space mutes = %+v", st.SpaceMutes)
	}
}

func TestAttentionStateWithoutAMarker(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateDone)
	if h.sup.HasAttention() || h.sup.AttentionState(context.Background(), h.store.List()).Available {
		t.Error("a supervisor without a marker claims attention")
	}
	if _, err := h.sup.SetPinned(context.Background(), "w5:p1", true, ViaPhone); err == nil {
		t.Error("pin worked without a marker")
	}
}

// The seen and pin writes: what each does, what it answers, what it logs.
func TestMarkSeenAndPin(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		pane       string
		do         func(s *Supervisor) (AgentAttention, error)
		want       *AgentAttention // nil: an error
		wantErr    error
		wantEvents int
	}{
		{
			name: "seen at the shown turn clears unread",
			pane: "w5:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w5:p1", u64(3), false, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupDone, Turn: 3, Seen: u64(3), Shared: true}, wantEvents: 1,
		},
		{
			name: "seen for an older turn leaves newer work unread",
			pane: "w5:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w5:p1", u64(1), false, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupDone, Unread: true, Turn: 3, Seen: u64(2), Shared: true},
		},
		{
			name: "seen above the turn is clamped, not refused",
			pane: "w5:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w5:p1", u64(99), false, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupDone, Turn: 3, Seen: u64(3), Shared: true}, wantEvents: 1,
		},
		{
			name: "turn 0 is a valid turn",
			pane: "w7:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w7:p1", u64(0), false, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupNeedsYou, Seen: u64(0)}, wantEvents: 1,
		},
		{
			name: "mark as unread",
			pane: "w6:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w6:p1", nil, true, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupWorking, Pinned: true, Turn: 1, Seen: u64(0), Shared: true}, wantEvents: 1,
		},
		{
			name: "mark as unread at turn 0 changes nothing",
			pane: "w7:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w7:p1", nil, true, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupNeedsYou},
		},
		{
			name:    "neither turn nor unread",
			pane:    "w5:p1",
			do:      func(s *Supervisor) (AgentAttention, error) { return s.MarkSeen(ctx, "w5:p1", nil, false, ViaPhone) },
			wantErr: ErrUserAction,
		},
		{
			name: "pin",
			pane: "w5:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.SetPinned(ctx, "w5:p1", true, ViaPanel) },
			want: &AgentAttention{Group: attention.GroupDone, Unread: true, Pinned: true, Turn: 3, Seen: u64(2), Shared: true}, wantEvents: 1,
		},
		{
			name: "unpin",
			pane: "w6:p1",
			do:   func(s *Supervisor) (AgentAttention, error) { return s.SetPinned(ctx, "w6:p1", false, ViaPhone) },
			want: &AgentAttention{Group: attention.GroupWorking, Turn: 1, Seen: u64(1), Shared: true}, wantEvents: 1,
		},
		{
			name:    "an untracked pane is not an agent",
			pane:    "w99:p1",
			do:      func(s *Supervisor) (AgentAttention, error) { return s.SetPinned(ctx, "w99:p1", true, ViaPhone) },
			wantErr: ErrNoSuchAgent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, fm := newAttentionWorld(t)
			writesBefore := fm.writes
			got, err := tc.do(h.sup)
			if tc.want == nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if fm.writes != writesBefore {
					t.Error("a refused call wrote a mark")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sameAttention(got, *tc.want) || got.Shared != tc.want.Shared {
				t.Errorf("attention = %+v, want %+v", got, *tc.want)
			}
			ev := h.log.ofType("agent.attention")
			if len(ev) != tc.wantEvents {
				t.Fatalf("agent.attention events = %d, want %d", len(ev), tc.wantEvents)
			}
			if tc.wantEvents == 1 {
				p := payload(ev[0])
				if ev[0].PaneID != tc.pane || p["group"] != got.Group || p["unread"] != got.Unread || p["pinned"] != got.Pinned || p["turn"] != got.Turn || p["via"] == "" {
					t.Errorf("agent.attention = %+v (pane %s)", p, ev[0].PaneID)
				}
			}
		})
	}
}

// A marker that cannot reach herdr is a refusal, not a 404.
func TestMarkWriteFailureIsNotMissingAgent(t *testing.T) {
	h, fm := newAttentionWorld(t)
	fm.err = errors.New("hc refused terminal.label")
	_, err := h.sup.SetPinned(context.Background(), "w5:p1", true, ViaPhone)
	if err == nil || errors.Is(err, ErrNoSuchAgent) {
		t.Errorf("error = %v, want a refusal that is not ErrNoSuchAgent", err)
	}
	if h.log.count("agent.attention") != 0 {
		t.Error("a failed write logged a change")
	}
}

// A change another client made reaches /events once, with who made it, and
// the echo of the daemon's own write does not repeat the event.
func TestMarkChangesAreLoggedOnce(t *testing.T) {
	h, fm := newAttentionWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.RunAttention(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Our own pin, then herdr's echo of the same label.
	if _, err := h.sup.SetPinned(ctx, "w5:p1", true, ViaPhone); err != nil {
		t.Fatal(err)
	}
	fm.changes <- "w5:p1"
	// Another client looks at the agent.
	fm.mu.Lock()
	m := fm.marks["w5:p1"]
	m.Seen = u64(3)
	fm.marks["w5:p1"] = m
	fm.mu.Unlock()
	fm.changes <- "w5:p1"
	// A change to a pane the daemon does not track as an agent is ignored.
	fm.changes <- "w99:p1"

	deadline := time.After(5 * time.Second)
	for h.log.count("agent.attention") < 2 {
		select {
		case <-deadline:
			t.Fatalf("agent.attention events = %d, want 2", h.log.count("agent.attention"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond)
	evs := h.log.ofType("agent.attention")
	if len(evs) != 2 {
		t.Fatalf("agent.attention events = %d, want 2 (own write, then the other client's seen)", len(evs))
	}
	if p := payload(evs[0]); p["via"] != ViaPhone || p["pinned"] != true {
		t.Errorf("own write event = %+v", p)
	}
	if p := payload(evs[1]); p["via"] != ViaHerdr || p["unread"] != false || p["seen"] != uint64(3) {
		t.Errorf("other client's event = %+v", p)
	}
}

// On a marker that is not shared (the old herdr's own store), a pane whose
// seen mark moved with no new turn was looked at in the TUI; a turn that
// ended is just a change from herdr.
func TestMarkChangeOnAnUnsharedMarkerIsTUIFocus(t *testing.T) {
	h, fm := newAttentionWorld(t)
	set := func(m herdr.Marks) {
		fm.mu.Lock()
		fm.marks["w5:p1"] = m
		fm.mu.Unlock()
		h.sup.attentionChanged(context.Background(), "w5:p1")
	}
	set(herdr.Marks{Turn: 3, Seen: u64(2), Word: herdr.StateDone}) // what the transition already took as the baseline: no event
	set(herdr.Marks{Turn: 3, Seen: u64(3), Word: herdr.StateIdle}) // focused in the TUI
	set(herdr.Marks{Turn: 4, Seen: u64(3), Word: herdr.StateDone}) // a turn ended
	var vias []any
	for _, e := range h.log.ofType("agent.attention") {
		vias = append(vias, payload(e)["via"])
	}
	if len(vias) != 2 || vias[0] != ViaTUI || vias[1] != ViaHerdr {
		t.Errorf("vias = %v, want [tui_focus herdr]", vias)
	}
}

// A muted space shows muted on its agents and in space_mutes, and the badge
// numbers do not change.
func TestAttentionStateShowsMutedSpaces(t *testing.T) {
	fm := newFakeMarker()
	w := newMuteWorld(t, nil, Chatty)
	w.sup.cfg.Marker = fm
	fm.marks["w5:p1"] = herdr.Marks{Turn: 1, Word: herdr.StateDone}
	w.srv.SetStatus("w5:p1", herdr.StateDone)
	w.transition("w5:p1", herdr.StateWorking, herdr.StateDone)

	before := w.sup.AttentionState(context.Background(), w.store.List())
	until := time.Now().Add(time.Hour)
	if _, err := w.mutes.Set(w.path, &until, ViaPhone); err != nil {
		t.Fatal(err)
	}
	after := w.sup.AttentionState(context.Background(), w.store.List())
	if a := after.Agents["w5:p1"]; !a.Muted || !a.Unread {
		t.Errorf("attention of a muted agent = %+v, want muted and still unread", a)
	}
	if before.Counts != after.Counts {
		t.Errorf("counts changed with the mute: %+v -> %+v", before.Counts, after.Counts)
	}
	if len(after.SpaceMutes) != 1 || after.SpaceMutes[0].Path != w.path || after.SpaceMutes[0].Name != "auth" || after.SpaceMutes[0].Until == nil {
		t.Errorf("space mutes = %+v", after.SpaceMutes)
	}
}

// Hearing a finish announced does not mark the agent seen: it stays under
// Done, unread (product default, design Q2).
func TestAnnouncedFinishStaysUnread(t *testing.T) {
	fm := newFakeMarker()
	h := newHarness(t, func(c *Config) { c.Marker = fm; c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	fm.marks["w5:p1"] = herdr.Marks{Turn: 1, Seen: u64(0), Word: herdr.StateDone}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 {
		t.Fatalf("the finish was not announced: %v", got)
	}
	if fm.writes != 0 {
		t.Error("announcing a finish wrote a seen mark")
	}
	if a := h.sup.AttentionState(context.Background(), h.store.List()).Agents["w5:p1"]; !a.Unread {
		t.Errorf("attention after the announcement = %+v, want unread", a)
	}
}

// herdr's turn label can arrive before the state transition that ends the
// turn: the event it caused then says working. The transition must say the
// attention again, so /events agrees with /api/state (done and unread), and
// a working transition says it too, for the panel and the phone.
func TestTransitionsSayTheAttentionAgain(t *testing.T) {
	fm := newFakeMarker()
	h := newHarness(t, func(c *Config) { c.Marker = fm })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	fm.marks["w5:p1"] = herdr.Marks{Turn: 1, Seen: u64(1), Word: herdr.StateWorking, Shared: true}
	ctx := context.Background()
	h.sup.attentionChanged(ctx, "w5:p1") // first sight

	// The turn label moves first: the store row still says working.
	fm.mu.Lock()
	fm.marks["w5:p1"] = herdr.Marks{Turn: 2, Seen: u64(1), Word: herdr.StateDone, Shared: true}
	fm.mu.Unlock()
	h.sup.attentionChanged(ctx, "w5:p1")
	if p := payload(h.log.ofType("agent.attention")[1]); p["group"] != attention.GroupWorking {
		t.Fatalf("setup: the label's event = %+v, want the stale working group", p)
	}

	h.sup.HandleTransition(ctx, herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5"})
	evs := h.log.ofType("agent.attention")
	last := payload(evs[len(evs)-1])
	if last["group"] != attention.GroupDone || last["unread"] != true || last["via"] != ViaHerdr {
		t.Errorf("event after the transition = %+v, want done and unread via herdr", last)
	}
	got := h.sup.AttentionState(ctx, h.store.List()).Agents["w5:p1"]
	if got.Group != last["group"] || got.Unread != last["unread"] {
		t.Errorf("/api/state says %+v but the last event says %+v", got, last)
	}

	// Working again: the group changes, so the event says so, once.
	n := len(evs)
	h.sup.HandleTransition(ctx, herdr.Transition{PaneID: "w5:p1", From: herdr.StateDone, To: herdr.StateWorking, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5"})
	h.sup.HandleTransition(ctx, herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateWorking, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5"})
	evs = h.log.ofType("agent.attention")
	if len(evs) != n+1 || payload(evs[n])["group"] != attention.GroupWorking {
		t.Errorf("events after working = %d (was %d), want exactly one more, group working", len(evs), n)
	}
}
