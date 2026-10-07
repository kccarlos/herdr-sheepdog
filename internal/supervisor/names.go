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
// the user asks (the orchestrator's note tool with agent and name).

// namesDebounce is how long the names line waits for the roster to
// settle (the start-up snapshot reports every pane at once) before it
// is pushed. A var so tests can make it synchronous.
var namesDebounce = 2 * time.Second

// spokenWasWindow is how long the roster keeps telling the orchestrator an
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
	// said holds the spoken names (lower-cased) that went out in a
	// line meant for the user's ear (noteNamesSaid).
	said map[string]bool
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

// rename gives the agent the name the user chose: the state table keeps
// the spoken form ("kvoice explainer"), and herdr gets the spelling its
// agent names allow (state.HerdrName: "kvoice-explainer") when the
// herdr client can rename. It returns the spoken echo, which says so
// when herdr refused or could not take the name: the user and the orchestrator
// hear what happened, not a success that did not (2026-10-04 05:00:43,
// "kvoice explainer" was refused by herdr while the daemon reported the
// rename done).
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
	fields := map[string]any{"from": old, "to": name}
	var herdrNote string
	if r, ok := s.herdr.(Renamer); ok {
		hname, valid := state.HerdrName(name)
		fields["herdr_name"] = hname
		switch {
		case !valid:
			fields["herdr"] = "not_sent"
			herdrNote = " herdr only takes names in lowercase English letters and digits, so herdr's own label still shows the old name."
		default:
			if err := r.Rename(ctx, paneID, hname); err != nil {
				// The table has the name already; herdr's own label
				// stays behind until it is renamed there.
				s.log.Log(logSource, "supervisor.rename_error", paneID, map[string]any{"name": name, "herdr_name": hname, "error": err.Error()})
				fields["herdr"] = "refused"
				fields["herdr_error"] = err.Error()
				// Spoken (the fallback path reads the echo aloud): the
				// fact, not herdr's rule text, which stays in the log.
				herdrNote = " herdr refused the name, so herdr's own label still shows the old name."
			} else {
				fields["herdr"] = "renamed"
			}
		}
	}
	s.log.Log(logSource, "supervisor.rename", paneID, fields)
	s.invalidateBriefing()
	s.syncNames()
	if old == "" || strings.EqualFold(old, name) {
		return "It is called " + name + " now." + herdrNote, nil
	}
	return old + " is called " + name + " now." + herdrNote, nil
}

// sayOf is how a pane's agent is named aloud: its spoken name, or
// "another agent" when the pane is unknown (never a pane id: D9). The
// alias stays for tool plumbing, tags and logs (aliasOf).
func (s *Supervisor) sayOf(paneID string) string {
	if a, ok := s.store.Get(paneID); ok {
		if n := state.Say(a); n != "" {
			return n
		}
	}
	return "another agent"
}

// sayPane is sayOf for a record that kept the alias of a pane that may
// be gone (a promise, an investigation): the pane's current spoken name
// while it exists, else that alias.
func (s *Supervisor) sayPane(paneID, alias string) string {
	if a, ok := s.store.Get(paneID); ok {
		if n := state.Say(a); n != "" {
			return n
		}
	}
	return aliasOr(alias)
}

// spokenPromises returns the promises with each Alias replaced by the
// pane's current spoken name, for the sentences that name them.
func (s *Supervisor) spokenPromises(ps []state.Promise) []state.Promise {
	out := make([]state.Promise, len(ps))
	for i, p := range ps {
		p.Alias = s.sayPane(p.PaneID, p.Alias)
		out[i] = p
	}
	return out
}

// recentSpokenWas is the agent's old spoken name while its change is
// recent enough to mention, else "".
func recentSpokenWas(a state.Agent, now time.Time) string {
	if a.SpokenWas == "" || a.SpokenChangedAt.IsZero() || now.Sub(a.SpokenChangedAt) > spokenWasWindow || strings.EqualFold(a.SpokenWas, a.Spoken) {
		return ""
	}
	return a.SpokenWas
}

// spokenWasToMention is the old name the roster tells the orchestrator about:
// recentSpokenWas, and only when the user was actually told that name
// before. Tier-3 bench 2026-10-03, finding 5: on a fresh call the orchestrator
// said "previously bench Claude" about a name the user had never heard
// (the rename happened before the call).
func (s *Supervisor) spokenWasToMention(a state.Agent) string {
	was := recentSpokenWas(a, s.cfg.Now())
	if was == "" {
		return ""
	}
	n := &s.names
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.said[strings.ToLower(was)] {
		return ""
	}
	return was
}

// noteNamesSaid records which agents' names a line meant for the user's
// ear carries — a spoken push as it went to the voice, a reply, the
// voice's own final output transcript — or the user's own final words
// carry, and pins those names (state.PinSpoken). The names checked are each row's
// current and previous spoken name, longest first, each matched as whole
// words where no longer name already matched ("Claude" is not said by
// "bench Claude 2").
func (s *Supervisor) noteNamesSaid(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	var names []string
	for _, a := range s.store.List() {
		for _, n := range []string{state.Say(a), a.SpokenWas} {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, strings.ToLower(n))
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	lower := []byte(strings.ToLower(text))
	var hits []string
	for _, n := range names {
		for from := 0; ; {
			i := strings.Index(string(lower[from:]), n)
			if i < 0 {
				break
			}
			i += from
			end := i + len(n)
			if wordEdge(lower, i-1) && wordEdge(lower, end) {
				hits = append(hits, n)
				for k := i; k < end; k++ {
					lower[k] = 0 // a shorter name cannot match inside it
				}
			}
			from = end
		}
	}
	if len(hits) == 0 {
		return
	}
	n := &s.names
	n.mu.Lock()
	if n.said == nil {
		n.said = map[string]bool{}
	}
	for _, h := range hits {
		n.said[h] = true
	}
	n.mu.Unlock()
	// A name the user has heard or said stays: a later session title or
	// a new agent of the same kind no longer renames it (state.PinSpoken).
	if panes, err := s.store.PinSpoken(hits); err == nil && len(panes) > 0 {
		s.log.Log(logSource, "supervisor.name_pinned", "", map[string]any{"panes": panes, "names": hits})
	}
}

// wordEdge reports whether position i of b is outside the text or not
// an ASCII letter or digit (a name ends at a word boundary).
func wordEdge(b []byte, i int) bool {
	if i < 0 || i >= len(b) {
		return true
	}
	c := b[i]
	return (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != 0
}
