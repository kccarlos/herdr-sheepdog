package supervisor

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Agent names (docs/roles.md "Agent names"). The store computes every
// agent's spoken name (state/spoken.go); here the supervisor feeds it
// the workers' own session titles, tells the voice model the current
// names whenever they change (a silent context line, so its alias list
// is never stale), serves them to the voice sessions for the system
// prompt and the speech-to-text vocabulary, and renames an agent when
// the CEO asks (the brain's note tool with agent and name).

// namesDebounce is how long the names line waits for the roster to
// settle (the start-up snapshot reports every pane at once) before it
// is pushed. A var so tests can make it synchronous.
var namesDebounce = 2 * time.Second

// spokenWasWindow is how long the roster keeps telling the brain an
// agent's old name after its spoken name changed.
const spokenWasWindow = 10 * time.Minute

// Renamer is a Herdr that can rename an agent (herdr's agent.rename).
// A Herdr without it still renames in the state table; herdr itself
// then keeps the old name.
type Renamer interface {
	Rename(ctx context.Context, paneID, name string) error
}

var _ Renamer = (*herdr.Client)(nil)

// namesState is the names line bookkeeping.
type namesState struct {
	mu    sync.Mutex
	sent  string      // the names line last pushed
	want  string      // the line waiting for the debounce
	timer *time.Timer // the pending debounce, nil when none
}

// refreshTitle reads the worker's session title and stores it on the
// row (the store recomputes the spoken names). Any error just leaves
// the title as it was: the title is a nicety, never a reason to log.
func (s *Supervisor) refreshTitle(row state.Agent) {
	if row.SessionValue == "" || s.cfg.WorkerLog == nil {
		return
	}
	title, err := s.cfg.WorkerLog.Title(workerSession(row))
	if err != nil || title == "" {
		return
	}
	// Worker text spoken aloud: D9 applies (no PR numbers, hashes, paths).
	title = prose.Spoken(title, 0)
	if changed, err := s.store.SetTitle(row.PaneID, title); err == nil && changed {
		s.log.Log(logSource, "worker.title", row.PaneID, map[string]any{"agent": aliasOr(row.Alias), "title": state.CleanTitle(title)})
	}
}

// SpokenNames returns the spoken names of the running agents, sorted:
// the voice model's agent list and speech-to-text vocabulary.
func (s *Supervisor) SpokenNames() []string {
	return spokenNames(s.store.List())
}

func spokenNames(rows []state.Agent) []string {
	names := state.SpokenNames(rows)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// namesLine is the silent context line that tells the voice model the
// current names.
func namesLine(names []string) string {
	if len(names) == 0 {
		return "No agents are running now."
	}
	return "The agents are now called: " + strings.Join(names, ", ") + ". Use these names; never a nickname."
}

// syncNames pushes the names line, silently, when the set of spoken
// names changed since the last one, after namesDebounce.
func (s *Supervisor) syncNames() {
	line := namesLine(s.SpokenNames())
	n := &s.names
	n.mu.Lock()
	if line == n.sent && n.timer == nil {
		n.mu.Unlock()
		return
	}
	n.want = line
	if namesDebounce <= 0 {
		n.mu.Unlock()
		s.flushNames()
		return
	}
	if n.timer == nil {
		n.timer = time.AfterFunc(namesDebounce, s.flushNames)
	}
	n.mu.Unlock()
}

// flushNames pushes the waiting names line unless it was already sent.
func (s *Supervisor) flushNames() {
	n := &s.names
	n.mu.Lock()
	n.timer = nil
	line := n.want
	if line == "" || line == n.sent {
		n.mu.Unlock()
		return
	}
	n.sent = line
	n.mu.Unlock()
	s.log.Log(logSource, "supervisor.names", "", map[string]any{"text": line})
	s.push("", line, voice.UrgencySilent)
}

// stopNames cancels a pending names push (Run's exit).
func (s *Supervisor) stopNames() {
	n := &s.names
	n.mu.Lock()
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()
}

// rename gives the agent the name the CEO chose: herdr agent rename when
// the herdr client can, and the state table either way. It returns the
// spoken echo.
func (s *Supervisor) rename(ctx context.Context, paneID, name string) (string, error) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return "", errors.New("that agent is gone")
	}
	old := state.Say(row)
	name = strings.Join(strings.Fields(name), " ")
	if err := s.store.SetName(paneID, name); err != nil {
		return "", err
	}
	if r, ok := s.herdr.(Renamer); ok {
		if err := r.Rename(ctx, paneID, name); err != nil {
			// The table has the name already; herdr's own label stays
			// behind until it is renamed there.
			s.log.Log(logSource, "supervisor.rename_error", paneID, map[string]any{"name": name, "error": err.Error()})
		}
	}
	s.log.Log(logSource, "supervisor.rename", paneID, map[string]any{"from": old, "to": name})
	s.invalidateBriefing()
	s.syncNames()
	if old == "" || strings.EqualFold(old, name) {
		return "It is called " + name + " now.", nil
	}
	return old + " is called " + name + " now.", nil
}

// recentSpokenWas is the agent's old spoken name while its change is
// recent enough to mention, else "".
func recentSpokenWas(a state.Agent, now time.Time) string {
	if a.SpokenWas == "" || a.SpokenChangedAt.IsZero() || now.Sub(a.SpokenChangedAt) > spokenWasWindow || strings.EqualFold(a.SpokenWas, a.Spoken) {
		return ""
	}
	return a.SpokenWas
}
