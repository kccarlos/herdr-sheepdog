package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/fsaccess"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Attention (docs/dev/design/unread-pinned-mute.md): which agents want a
// person's eyes (unread, pinned, the group they list under) and which spaces
// the user muted. The marks live in the terminal (herdr.Marker); the mutes in
// <data_dir>/space_mutes.json (attention.Mutes). This file is the daemon's
// one view of both for /api/state, the seen, pin and mute routes, the events
// they log, and the mute check the voice, the phone notifications and the
// Live Activity make.
//
// A muted space only silences what the daemon says or sends unprompted. The
// lists, the badges, the question cards and every answer to a question stay.

// AgentAttention is the attention part of one agent in /api/state and in the
// answers of the seen and pin routes.
type AgentAttention struct {
	Group  attention.Group `json:"group"`
	Unread bool            `json:"unread"`
	Pinned bool            `json:"pinned"`
	// Turn counts the finished turns; Seen is the last turn a person looked
	// at, absent when there is no record.
	Turn uint64  `json:"turn"`
	Seen *uint64 `json:"seen,omitempty"`
	// Muted is set when the agent's space is muted.
	Muted bool `json:"muted"`
	// Shared is true when the marks are hc labels every Herdr client sees.
	Shared bool `json:"shared"`
}

// AttentionCounts are the numbers the badges and the status line use.
type AttentionCounts struct {
	NeedsYou   int `json:"needs_you"`
	Working    int `json:"working"`
	DoneUnread int `json:"done_unread"`
}

// SpaceMuteInfo is one muted space in /api/state.
type SpaceMuteInfo struct {
	Path  string     `json:"path"`
	Name  string     `json:"name"`
	Until *time.Time `json:"until,omitempty"`
}

// AttentionState is everything /api/state adds.
type AttentionState struct {
	// Agents is by pane id.
	Agents     map[string]AgentAttention
	Counts     AttentionCounts
	SpaceMutes []SpaceMuteInfo
	Available  bool
}

// Via names who made a change, in the agent.attention and space.muted events.
const (
	ViaPhone   = "phone"
	ViaPanel   = "panel"
	ViaHerdr   = "herdr"
	ViaTUI     = "tui_focus"
	ViaExpired = "expired"
)

// HasAttention reports whether the daemon keeps seen and pinned marks.
func (s *Supervisor) HasAttention() bool { return s.cfg.Marker != nil }

// HasSpaceMutes reports whether the daemon keeps space mutes.
func (s *Supervisor) HasSpaceMutes() bool { return s.cfg.Mutes != nil }

// marksOf returns the marks of the pane: the marker's, else the backend's
// word alone (a pane the marker does not follow, like a synthetic one on
// herdr-core: done reads as unread, idle as read).
func marksOf(all map[string]herdr.Marks, row state.Agent) herdr.Marks {
	if m, ok := all[row.PaneID]; ok {
		return m
	}
	return herdr.Marks{Word: row.State}
}

// attentionOf builds one agent's attention from its marks.
func attentionOf(row state.Agent, m herdr.Marks, muted bool) AgentAttention {
	a := attention.Classify(row.State, m)
	out := AgentAttention{Group: a.Group, Unread: a.Unread, Pinned: a.Pinned, Turn: m.Turn, Muted: muted, Shared: m.Shared}
	if m.Seen != nil {
		seen := *m.Seen
		out.Seen = &seen
	}
	return out
}

// AttentionState computes the attention of the given agents, the counts and
// the muted spaces. It reads only the marker's cache and, when something is
// muted, the folder index.
func (s *Supervisor) AttentionState(ctx context.Context, agents []state.Agent) AttentionState {
	out := AttentionState{Agents: map[string]AgentAttention{}, SpaceMutes: []SpaceMuteInfo{}}
	if s.cfg.Marker == nil {
		return out
	}
	out.Available = true
	all, err := s.cfg.Marker.AllMarks(ctx)
	if err != nil {
		s.log.Log(logSource, "supervisor.attention_error", "", map[string]any{"stage": "marks", "error": err.Error()})
		all = nil
	}
	var idx *folderIndex
	if s.cfg.Mutes != nil && !s.cfg.Mutes.Empty() {
		idx = s.folders(ctx)
	}
	for _, row := range agents {
		a := attentionOf(row, marksOf(all, row), s.rowMuted(idx, row))
		out.Agents[row.PaneID] = a
		switch {
		case a.Group == attention.GroupNeedsYou:
			out.Counts.NeedsYou++
		case a.Group == attention.GroupWorking:
			out.Counts.Working++
		case a.Group == attention.GroupDone && a.Unread:
			out.Counts.DoneUnread++
		}
	}
	if s.cfg.Mutes != nil {
		for _, m := range s.cfg.Mutes.List() {
			out.SpaceMutes = append(out.SpaceMutes, SpaceMuteInfo{Path: m.Path, Name: idx.nameOf(m.Path), Until: m.Until})
		}
	}
	return out
}

// SpaceMutedPath reports whether the space folder is muted now (for
// GET /api/spaces).
func (s *Supervisor) SpaceMutedPath(path string) (attention.Mute, bool) {
	if s.cfg.Mutes == nil {
		return attention.Mute{}, false
	}
	return s.cfg.Mutes.Get(path)
}

// MarkSeen records that the user looked at the agent at the given turn
// (raise-only, never above the current turn), or, with unread, marks it
// unread. Exactly one of turn and unread is given.
func (s *Supervisor) MarkSeen(ctx context.Context, paneID string, turn *uint64, unread bool, via string) (AgentAttention, error) {
	row, err := s.attentionRow(paneID)
	if err != nil {
		return AgentAttention{}, err
	}
	s.seedAttention(ctx, row)
	var m herdr.Marks
	switch {
	case unread:
		m, err = s.cfg.Marker.MarkUnread(ctx, paneID)
	case turn != nil:
		m, err = s.cfg.Marker.MarkSeen(ctx, paneID, *turn)
	default:
		return AgentAttention{}, fmt.Errorf("%w: give the turn the client showed, or unread", ErrUserAction)
	}
	return s.afterMarkWrite(row, m, err, via)
}

// SetPinned pins or unpins the agent's terminal.
func (s *Supervisor) SetPinned(ctx context.Context, paneID string, pinned bool, via string) (AgentAttention, error) {
	row, err := s.attentionRow(paneID)
	if err != nil {
		return AgentAttention{}, err
	}
	s.seedAttention(ctx, row)
	m, err := s.cfg.Marker.SetPinned(ctx, paneID, pinned)
	return s.afterMarkWrite(row, m, err, via)
}

// attentionRow is the tracked agent a seen or pin write is about.
func (s *Supervisor) attentionRow(paneID string) (state.Agent, error) {
	if s.cfg.Marker == nil {
		return state.Agent{}, errors.New("seen and pinned marks are not available here")
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return state.Agent{}, fmt.Errorf("%w: %s", ErrNoSuchAgent, paneID)
	}
	return row, nil
}

// afterMarkWrite turns a marker's answer into the route's: an untracked
// terminal is a 404, a refusal by herdr stays an error (502), and a write
// that changed anything is logged as agent.attention.
func (s *Supervisor) afterMarkWrite(row state.Agent, m herdr.Marks, err error, via string) (AgentAttention, error) {
	if err != nil {
		if errors.Is(err, herdr.ErrPaneNotFound) {
			return AgentAttention{}, fmt.Errorf("%w: %s", ErrNoSuchAgent, row.PaneID)
		}
		s.log.Log(logSource, "supervisor.attention_error", row.PaneID, map[string]any{"stage": "write", "error": err.Error()})
		return AgentAttention{}, fmt.Errorf("herdr did not take the mark: %w", err)
	}
	a := attentionOf(row, m, s.paneMutedCached(row.PaneID))
	s.noteAttention(row.PaneID, a, via)
	return a, nil
}

// seedAttention remembers the attention the pane has before a write, when no
// event has said it yet, so that a write that changes nothing logs nothing.
func (s *Supervisor) seedAttention(ctx context.Context, row state.Agent) {
	s.attMu.Lock()
	_, had := s.attLast[row.PaneID]
	s.attMu.Unlock()
	if had {
		return
	}
	all, err := s.cfg.Marker.AllMarks(ctx)
	if err != nil {
		return
	}
	m, ok := all[row.PaneID]
	if !ok {
		return
	}
	s.seedAttentionWith(row.PaneID, attentionOf(row, m, false))
}

// noteAttention logs agent.attention unless a is what the last event for the
// pane already said (an own write is followed by herdr's echo of the same
// label).
func (s *Supervisor) noteAttention(paneID string, a AgentAttention, via string) {
	key := a
	key.Muted = false
	s.attMu.Lock()
	if s.attLast == nil {
		s.attLast = map[string]AgentAttention{}
	}
	prev, had := s.attLast[paneID]
	s.attLast[paneID] = key
	s.attMu.Unlock()
	if had && sameAttention(prev, key) {
		return
	}
	f := map[string]any{"group": a.Group, "unread": a.Unread, "pinned": a.Pinned, "turn": a.Turn, "via": via}
	if a.Seen != nil {
		f["seen"] = *a.Seen
	}
	s.log.Log(logSource, "agent.attention", paneID, f)
}

func sameSeen(a, b *uint64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func sameAttention(a, b AgentAttention) bool {
	if !sameSeen(a.Seen, b.Seen) {
		return false
	}
	return a.Group == b.Group && a.Unread == b.Unread && a.Pinned == b.Pinned && a.Turn == b.Turn
}

// attentionChanged handles a mark that changed outside the routes (another
// Herdr client, a turn that ended, a pane looked at in a TUI client).
func (s *Supervisor) attentionChanged(ctx context.Context, paneID string) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return
	}
	all, err := s.cfg.Marker.AllMarks(ctx)
	if err != nil {
		return
	}
	m, ok := all[paneID]
	if !ok {
		return
	}
	a := attentionOf(row, m, false)
	// A pane looked at in the original herdr's own window moves seen and
	// nothing else; any other change (a label, a turn that ended) came
	// from herdr.
	via := ViaHerdr
	s.attMu.Lock()
	prev, had := s.attLast[paneID]
	s.attMu.Unlock()
	if !m.Shared && had && prev.Turn == a.Turn && !sameSeen(prev.Seen, a.Seen) {
		via = ViaTUI
	}
	s.noteAttention(paneID, a, via)
}

// attentionAfterTransition says the agent's attention again once its state
// has moved: the group follows the state, and herdr's turn label can arrive
// before the transition that ends the turn, so the event it caused may still
// name the old group. It says nothing when the attention is what the last
// event said.
func (s *Supervisor) attentionAfterTransition(ctx context.Context, row state.Agent) {
	if s.cfg.Marker == nil {
		return
	}
	all, err := s.cfg.Marker.AllMarks(ctx)
	if err != nil {
		return
	}
	a := attentionOf(row, marksOf(all, row), false)
	s.attMu.Lock()
	_, had := s.attLast[row.PaneID]
	s.attMu.Unlock()
	if !had {
		// First sight of the pane: a baseline for the next change, not a
		// change (every agent's first transition would say so); clients
		// learn of the transition itself from herdr.transition.
		s.seedAttentionWith(row.PaneID, a)
		return
	}
	s.noteAttention(row.PaneID, a, ViaHerdr)
}

// seedAttentionWith remembers a as the attention last told for the pane,
// unless an event already did.
func (s *Supervisor) seedAttentionWith(paneID string, a AgentAttention) {
	a.Muted = false
	s.attMu.Lock()
	defer s.attMu.Unlock()
	if s.attLast == nil {
		s.attLast = map[string]AgentAttention{}
	}
	if _, had := s.attLast[paneID]; !had {
		s.attLast[paneID] = a
	}
}

// RunAttention logs the marks that change on their own and removes space
// mutes when they end, until ctx ends.
func (s *Supervisor) RunAttention(ctx context.Context) {
	var wg sync.WaitGroup
	if s.cfg.Mutes != nil {
		wg.Add(1)
		go func() { defer wg.Done(); s.cfg.Mutes.Run(ctx) }()
	}
	// While anything is muted the folder index is kept fresh, so the checks
	// that may not call herdr (a logger, the Live Activity) find it.
	warm := time.NewTicker(folderTTL / 2)
	defer warm.Stop()
	var changes <-chan string // nil (never ready) without a marker
	if s.cfg.Marker != nil {
		changes = s.cfg.Marker.MarkChanges()
	}
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case id := <-changes:
			s.attentionChanged(ctx, id)
		case <-warm.C:
			s.WarmFolders(ctx)
		}
	}
}

// muteExpired is told by the mute store when a mute ended.
func (s *Supervisor) muteExpired(m attention.Mute) {
	name := s.mutedName(m.Path)
	defer s.announceHeld(m.Path)
	s.log.Log(logSource, "space.muted", "", map[string]any{"path": m.Path, "name": name, "muted": false, "via": ViaExpired})
	s.tellBrain(fmt.Sprintf("the mute on %s's notifications ended", name))
}

// mutedName is the name a muted folder goes by: its space's name when the
// folder index knows it, else the folder's own name.
func (s *Supervisor) mutedName(path string) string {
	s.idxMu.Lock()
	idx := s.idx
	s.idxMu.Unlock()
	return idx.nameOf(path)
}

// SpaceMuteResult is the answer of SetSpaceMute.
type SpaceMuteResult struct {
	ID    string
	Path  string
	Name  string
	Muted bool
	Until *time.Time
}

// SetSpaceMute mutes or unmutes the space (a herdr workspace id) by its
// folder. until ends a mute (nil: until unmuted). A space with no folder
// cannot be muted.
func (s *Supervisor) SetSpaceMute(ctx context.Context, spaceID string, muted bool, until *time.Time, via string) (SpaceMuteResult, error) {
	if s.cfg.Mutes == nil {
		return SpaceMuteResult{}, errors.New("space mutes are not available here")
	}
	sp, err := s.space(ctx, spaceID)
	if err != nil {
		return SpaceMuteResult{}, err
	}
	if sp.Path == "" {
		return SpaceMuteResult{}, fmt.Errorf("%w: %s has no folder to mute by", ErrUserAction, sp.Name)
	}
	res := SpaceMuteResult{ID: sp.ID, Path: sp.Path, Name: sp.Name, Muted: muted}
	if muted {
		e, err := s.cfg.Mutes.Set(sp.Path, until, via)
		if errors.Is(err, attention.ErrMuteInPast) {
			return SpaceMuteResult{}, fmt.Errorf("%w: until is in the past", ErrUserAction)
		}
		if err != nil {
			return SpaceMuteResult{}, err
		}
		res.Until = e.Until
	} else if _, err := s.cfg.Mutes.Clear(sp.Path); err != nil {
		return SpaceMuteResult{}, err
	}
	// The next decisions find the folder of this space without a herdr call.
	s.folders(ctx)
	f := map[string]any{"path": sp.Path, "name": sp.Name, "muted": muted, "via": via}
	if res.Until != nil {
		f["until"] = *res.Until
	}
	s.log.Log(logSource, "space.muted", "", f)
	if muted {
		s.tellBrain(fmt.Sprintf("the user muted %s's notifications from the %s", sp.Name, viaPlace(via)))
	} else {
		s.tellBrain(fmt.Sprintf("the user turned %s's notifications back on from the %s", sp.Name, viaPlace(via)))
		s.announceHeld(sp.Path)
	}
	return res, nil
}

func viaPlace(via string) string {
	if via == ViaPanel {
		return "panel"
	}
	return "phone"
}

// folderTTL is how long the folder index is trusted before the next decision
// reads herdr's workspaces again.
const folderTTL = 10 * time.Second

// folderIndex maps panes to their space's folder: what the mute check needs.
type folderIndex struct {
	at    time.Time
	ws    map[string]string // workspace id to folder
	pane  map[string]string // pane id to workspace id
	names map[string]string // folder to the space's name
}

// nameOf is a folder's space name, else its own base name.
func (x *folderIndex) nameOf(path string) string {
	if x != nil {
		if n := x.names[path]; n != "" {
			return n
		}
	}
	return filepath.Base(path)
}

// folderOf is the folder of the pane's space, "" when it has none.
func (x *folderIndex) folderOf(row state.Agent) string {
	if x == nil {
		return ""
	}
	ws := x.pane[row.PaneID]
	if ws == "" {
		ws = row.Workspace
	}
	return x.ws[ws]
}

// rowMuted reports whether the agent's space is muted, by the index.
func (s *Supervisor) rowMuted(idx *folderIndex, row state.Agent) bool {
	if s.cfg.Mutes == nil {
		return false
	}
	f := s.folderOfPane(idx, row)
	return f != "" && s.cfg.Mutes.Muted(f)
}

// folderMissAge is how old the index may be when a pane is not in it: a
// new space is looked for at once, but not more than once a second.
const folderMissAge = time.Second

// folders returns the folder index, read again when older than folderTTL.
// A herdr that cannot say (down, or the mock) gives the last one, or nil.
func (s *Supervisor) folders(ctx context.Context) *folderIndex {
	return s.foldersWithin(ctx, folderTTL)
}

func (s *Supervisor) foldersWithin(ctx context.Context, maxAge time.Duration) *folderIndex {
	now := s.cfg.Now()
	s.idxMu.Lock()
	idx := s.idx
	s.idxMu.Unlock()
	if idx != nil && now.Sub(idx.at) < maxAge {
		return idx
	}
	fresh, err := s.readFolders(ctx)
	if err != nil {
		return idx
	}
	s.idxMu.Lock()
	s.idx = fresh
	s.idxMu.Unlock()
	return fresh
}

// readFolders reads herdr's workspaces and finds each one's folder by the
// rule GET /api/spaces uses (spaceFolder).
func (s *Supervisor) readFolders(ctx context.Context) (*folderIndex, error) {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return nil, errors.New("spaces are not available here")
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	idx := &folderIndex{at: s.cfg.Now(), ws: map[string]string{}, pane: map[string]string{}, names: map[string]string{}}
	readable := fsaccess.NewSet().OK
	for _, w := range snap.Workspaces {
		var panes []herdr.PaneInfo
		for _, p := range snap.Panes {
			if p.WorkspaceID == w.WorkspaceID {
				panes = append(panes, p)
				idx.pane[p.PaneID] = w.WorkspaceID
			}
		}
		sort.Slice(panes, func(i, j int) bool { return paneLess(panes[i].PaneID, panes[j].PaneID) })
		if path, _, _ := spaceFolder(w, panes, readable); path != "" {
			idx.ws[w.WorkspaceID] = path
			idx.names[path] = w.Label
		}
	}
	return idx, nil
}

// PaneCwd is implemented by a herdr that serves panes of its own with a
// folder and no workspace (the debug injector).
type paneCwder interface {
	PaneCwd(paneID string) (string, bool)
}

// folderOfPane is the folder of the agent's space. A pane in no workspace
// (a synthetic one) falls back to the folder it was given.
func (s *Supervisor) folderOfPane(idx *folderIndex, row state.Agent) string {
	if f := idx.folderOf(row); f != "" {
		return f
	}
	if c, ok := s.herdr.(paneCwder); ok {
		if cwd, ok := c.PaneCwd(row.PaneID); ok {
			return herdr.RealDir(cwd)
		}
	}
	return ""
}

// paneMuted reports whether the agent's space is muted. With nothing muted
// it answers before any lookup. It may read herdr's workspaces; do not call
// it with s.mu held.
func (s *Supervisor) paneMuted(ctx context.Context, paneID string) bool {
	if s.cfg.Mutes == nil || s.cfg.Mutes.Empty() {
		return false
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return false
	}
	idx := s.folders(ctx)
	if s.folderOfPane(idx, row) == "" {
		// Serialize misses so concurrent panes see the completed refresh.
		s.missMu.Lock()
		s.idxMu.Lock()
		idx = s.idx
		now := s.cfg.Now()
		refresh := s.folderOfPane(idx, row) == "" && (s.missAt.IsZero() || now.Sub(s.missAt) >= folderMissAge)
		s.idxMu.Unlock()
		if refresh {
			if fresh, err := s.readFolders(ctx); err == nil {
				s.idxMu.Lock()
				s.idx = fresh
				s.missAt = s.cfg.Now()
				s.idxMu.Unlock()
				idx = fresh
			}
		}
		s.missMu.Unlock()
	}
	return s.rowMuted(idx, row)
}

// paneMutedCached is paneMuted from the folder index as it is, never
// calling herdr: for log decoration, which runs under locks.
func (s *Supervisor) paneMutedCached(paneID string) bool {
	if s.cfg.Mutes == nil || s.cfg.Mutes.Empty() {
		return false
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return false
	}
	s.idxMu.Lock()
	idx := s.idx
	s.idxMu.Unlock()
	return s.rowMuted(idx, row)
}

// PaneMuted reports whether the agent's space is muted now. It never calls
// herdr (it uses the folder index the last decision or /api/state read), so
// it is safe in a logger.
func (s *Supervisor) PaneMuted(paneID string) bool { return s.paneMutedCached(paneID) }

// WarmFolders reads the folder index if something is muted and the index is
// stale, so a mute check under a lock finds it. The attention loop and every
// /api/state read it.
func (s *Supervisor) WarmFolders(ctx context.Context) {
	if s.cfg.Mutes != nil && !s.cfg.Mutes.Empty() {
		s.folders(ctx)
	}
}

// holdQuestion remembers a question the space mute kept out of the pending
// slot; heldQuestion asks. A held question is not the voice's to answer
// until it is tapped or the orchestrator answers it; it stays listed.
func (s *Supervisor) holdQuestion(id string) {
	s.attMu.Lock()
	defer s.attMu.Unlock()
	if s.held == nil {
		s.held = map[string]bool{}
	}
	s.held[id] = true
}

func (s *Supervisor) heldQuestion(id string) bool {
	s.attMu.Lock()
	defer s.attMu.Unlock()
	return s.held[id]
}

// heldQuestions are the open questions the space mute holds back, oldest
// first.
func (s *Supervisor) heldQuestions() []state.PendingAction {
	var out []state.PendingAction
	for _, q := range s.store.Questions() {
		if s.heldQuestion(q.ID) {
			out = append(out, q)
		}
	}
	return out
}

// heldQuestionsLine is what a bare yes or no gets when the only open
// questions are held ones: plain words, no keys sent. "" when none is held.
// Caller holds s.mu (it reads the store only).
func (s *Supervisor) heldQuestionsLine() string {
	held := s.heldQuestions()
	switch len(held) {
	case 0:
		return ""
	case 1:
		return "Nothing is waiting for an answer here. " + aliasOr(s.sayOf(held[0].PaneID)) + " has a question in a muted space; you can answer it on your phone."
	}
	return fmt.Sprintf("Nothing is waiting for an answer here. %d questions are in muted spaces; you can answer them on your phone.", len(held))
}

// promoteHeld makes a held question the voice's pending answer because it is
// being announced now (a kept promise or the finish of the user's own
// instruction on a muted space, or the space being unmuted): from here it is
// answerable by voice, as any question the user was told about. Without
// that announcement it stays out of the slot.
func (s *Supervisor) promoteHeld(a announcement) {
	if a.pending == "" || !s.heldQuestion(a.pending) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.store.Question(a.pending)
	if !ok {
		return
	}
	q.Held = false
	if err := s.store.SetQuestion(q); err != nil {
		s.log.Log(logSource, "supervisor.state_error", q.PaneID, map[string]any{"error": err.Error()})
		return
	}
	s.attMu.Lock()
	delete(s.held, a.pending)
	s.attMu.Unlock()
	if old, had := s.store.Pending(); had && old.ID != q.ID {
		s.log.Log(logSource, "pending.replaced", q.PaneID, map[string]any{"old_id": old.ID, "old_kind": old.Kind, "old_pane_id": old.PaneID, "new_id": q.ID, "new_kind": q.Kind})
	}
	s.installedLocked(q) // displaced.go
	if err := s.store.SetPending(q); err != nil {
		s.log.Log(logSource, "supervisor.state_error", q.PaneID, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateBriefingLocked()
	s.log.Log(logSource, "pending.promoted", q.PaneID, map[string]any{"id": q.ID, "agent": s.aliasOf(q.PaneID)})
}

// announceHeld announces, once, the questions that were held in the space at
// path now that it is not muted: each is spoken like a new block (and so
// becomes answerable by voice when it is heard). A question whose agent has
// moved on is dropped quietly.
func (s *Supervisor) announceHeld(path string) {
	idx := s.folders(context.Background())
	for _, q := range s.heldQuestions() {
		row, ok := s.store.Get(q.PaneID)
		if !ok || s.folderOfPane(idx, row) != path {
			continue
		}
		if row.State != herdr.StateBlocked && row.State != herdr.StateDone && row.State != herdr.StateIdle {
			continue
		}
		go func(row state.Agent, q state.PendingAction) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.announce(ctx, announcement{row: row, kind: pushQuestion, event: brain.PromiseEventBlocked, sentence: q.SpokenSummary, pending: q.ID})
		}(row, q)
	}
}

// mutedQuiet reports whether the space mute silences a: it is muted and
// nothing is owed. A kept promise and the finish of the user's own
// instruction (owed) are requests the user made, newer than the mute, and
// still speak and push.
func (s *Supervisor) mutedQuiet(ctx context.Context, a announcement, kept []state.Promise) bool {
	return len(kept) == 0 && !a.owed && s.paneMuted(ctx, a.row.PaneID)
}

// logMutedQuiet logs a push the space mute silenced, as the chattiness
// ceiling logs its own (supervisor.push_quiet), with the reason.
func (s *Supervisor) logMutedQuiet(a announcement, level Chattiness) {
	if a.kind == pushPromiseOnly {
		return
	}
	s.log.Log(logSource, "supervisor.push_quiet", a.row.PaneID, map[string]any{"text": a.sentence, "chattiness": string(level), "reason": "space_muted"})
}
