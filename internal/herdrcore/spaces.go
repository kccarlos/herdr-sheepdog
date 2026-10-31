package herdrcore

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// herdr-app shows a terminal in a space and tab when it carries these
// labels, and makes a new space or tab of an id it does not know. sheepdog
// maps a space to a workspace and a tab to a tab without reading or
// writing herdr-app's document.
const (
	labelSpace = "herdr.space"
	labelTab   = "herdr.tab"
)

// newID is a fresh space or tab id: eight random hex digits. herdr-app
// accepts any id it does not know, and ids it writes itself are only
// compared, so no format is mimicked.
func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// spaceOf is the space t is labelled into, "" for a terminal herdr-app does
// not show.
func spaceOf(t hc.Terminal) string { return t.Labels[labelSpace] }

// space is one herdr-app space as the labels show it.
type space struct {
	first  hc.Terminal // the terminal that names it
	panes  int
	status herdr.State // the most urgent of its agents
}

// spaceSet collects the spaces of the terminals added to it, in order of
// first appearance.
type spaceSet struct {
	order []string
	byID  map[string]*space
}

func newSpaceSet() *spaceSet { return &spaceSet{byID: map[string]*space{}} }

// add counts t, a live local terminal in state, into its space. A terminal
// with no space label is no one's.
func (s *spaceSet) add(t hc.Terminal, state herdr.State) {
	id := spaceOf(t)
	if id == "" {
		return
	}
	sp, ok := s.byID[id]
	if !ok {
		sp = &space{first: t, status: herdr.StateUnknown}
		s.byID[id] = sp
		s.order = append(s.order, id)
	}
	if t.CreatedMS < sp.first.CreatedMS {
		sp.first = t
	}
	sp.panes++
	if isAgent(t) && urgency(state) > urgency(sp.status) {
		sp.status = state
	}
}

// urgency orders states as herdr does for a workspace: blocked, done,
// working, idle, then anything else.
func urgency(state herdr.State) int {
	switch state {
	case herdr.StateBlocked:
		return 4
	case herdr.StateDone:
		return 3
	case herdr.StateWorking:
		return 2
	case herdr.StateIdle:
		return 1
	}
	return 0
}

// workspaces lists the spaces as workspaces, numbered from 1.
func (s *spaceSet) workspaces() []herdr.WorkspaceInfo {
	out := make([]herdr.WorkspaceInfo, 0, len(s.order))
	for i, id := range s.order {
		sp := s.byID[id]
		out = append(out, herdr.WorkspaceInfo{
			WorkspaceID: id,
			Number:      i + 1,
			Label:       spaceName(sp.first),
			PaneCount:   sp.panes,
			Status:      sp.status,
		})
	}
	return out
}

// spaceName names a space as herdr-app names one nobody named: after where
// its first terminal is now, its repo, else its folder (~ at home). A name
// given in herdr-app is not seen: that is in its document.
func spaceName(first hc.Terminal) string {
	dir := cwdOf(first)
	if dir == "" {
		return ""
	}
	if g, ok := herdr.GitInfo(dir); ok {
		return g.Repo
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(dir) == filepath.Clean(home) {
		return "~"
	}
	return filepath.Base(dir)
}
