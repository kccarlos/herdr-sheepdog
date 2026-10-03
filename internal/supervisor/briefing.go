package supervisor

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Briefing shape limits.
const (
	// briefingListMax is how many agents are described one by one; with
	// more, the rest is a count (blocked first, so a question is never
	// hidden).
	briefingListMax = 3
	// briefingResultMaxAge is how long "it last …" is worth repeating.
	briefingResultMaxAge = 30 * time.Minute
	// briefingResultMaxWords trims the last result inside the sentence.
	briefingResultMaxWords = 12
	// briefingCacheMaxAge bounds how stale the age-dependent wording
	// ("it last …") can get between state changes.
	briefingCacheMaxAge = time.Minute
)

// briefingText returns the cached briefing, recomputing it from the state
// table when a state change invalidated it (or it aged past
// briefingCacheMaxAge), with the open promises appended ("You asked me
// to tell you when auth finishes."), and the panes it names. No model is
// involved.
func (s *Supervisor) briefingText() (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if s.briefing == "" || now.Sub(s.briefingAt) > briefingCacheMaxAge {
		pending, _ := s.store.Pending()
		s.briefing, s.briefingPanes = composeBriefing(s.store.List(), pending, now)
		if trailer := promiseTrailer(s.spokenPromises(s.openPromises())); trailer != "" {
			s.briefing += " " + trailer
		}
		s.briefingAt = now
	}
	return s.briefing, append([]string(nil), s.briefingPanes...)
}

// invalidateBriefing forces the next briefing to be recomputed.
func (s *Supervisor) invalidateBriefing() {
	s.mu.Lock()
	s.invalidateBriefingLocked()
	s.mu.Unlock()
}

// invalidateBriefingLocked is invalidateBriefing for a caller that
// already holds s.mu. Clearing lastBriefingText alongside the cache
// means a worker transition, a pending change or a promise change never
// leaves a stale "delivered" mark behind: supervisor.repeat_window_s
// (briefingRepeat) only ever compares against wording the state that
// produced it has not since moved on from.
func (s *Supervisor) invalidateBriefingLocked() {
	s.briefing = ""
	s.lastBriefingText, s.lastBriefingAt = "", time.Time{}
}

// briefingRepeat reports how long ago text was last delivered to the
// shepherd (markBriefingDelivered), when that was within
// Config.RepeatWindow (supervisor.repeat_window_s). It only reads: a
// roster counts as delivered once the shepherd heard an answer, never
// because a request carried it — a turn that failed delivered nothing,
// and its fallback must not then claim "no change since I last checked"
// (2026-09-24 18:16:43: said three times in reply to questions about
// details, after the brain had errored).
func (s *Supervisor) briefingRepeat(text string) (time.Duration, bool) {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastBriefingText == "" || s.lastBriefingText != text || now.Sub(s.lastBriefingAt) > s.cfg.RepeatWindow {
		return 0, false
	}
	return now.Sub(s.lastBriefingAt), true
}

// markBriefingDelivered records text as the roster the shepherd last
// heard an answer about: after a brain reply, or the roster sentence
// itself when there is no brain.
func (s *Supervisor) markBriefingDelivered(text string) {
	now := s.cfg.Now()
	s.mu.Lock()
	s.lastBriefingText, s.lastBriefingAt = text, now
	s.mu.Unlock()
}

// stateRank orders agents for the ear: blocked first, then working, then
// idle/done, then everything else.
func stateRank(st herdr.State) int {
	switch st {
	case herdr.StateBlocked:
		return 0
	case herdr.StateWorking:
		return 1
	case herdr.StateIdle, herdr.StateDone:
		return 2
	default:
		return 3
	}
}

// composeBriefing builds the status from the table (plan §5), at most two
// sentences: every agent with what it is doing or last did when there are
// at most briefingListMax of them ("Two agents: auth is asking whether it
// can create hello.txt and docs is idle; it last wrote the readme."),
// otherwise the count plus the interesting ones (blocked first) and a
// tally of the rest; then the pending question once more, if any. It
// returns the sentence and the panes it names.
func composeBriefing(agents []state.Agent, pending state.PendingAction, now time.Time) (string, []string) {
	if len(agents) == 0 {
		return "No agents are running.", nil
	}
	sort.SliceStable(agents, func(i, j int) bool {
		ri, rj := stateRank(agents[i].State), stateRank(agents[j].State)
		if ri != rj {
			return ri < rj
		}
		return state.Say(agents[i]) < state.Say(agents[j])
	})
	listed := agents
	if len(agents) > briefingListMax {
		listed = agents[:briefingListMax]
	}
	parts := make([]string, 0, len(listed))
	panes := make([]string, 0, len(listed))
	for _, a := range listed {
		parts = append(parts, describeAgent(a, now))
		panes = append(panes, a.PaneID)
	}
	head := "One agent: "
	if len(agents) > 1 {
		head = countWord(len(agents)) + " agents: "
	}
	text := head + joinSpoken(parts)
	if rest := agents[len(listed):]; len(rest) > 0 {
		text += "; " + describeRest(rest)
	}
	text += "."
	if trailer := pendingTrailer(agents, pending, text); trailer != "" {
		text += " " + trailer
	}
	return text, panes
}

// describeAgent is one agent's clause: state plus what it is doing or
// last did.
func describeAgent(a state.Agent, now time.Time) string {
	alias := state.Say(a)
	if alias == "" {
		alias = "an agent"
	}
	switch a.State {
	case herdr.StateBlocked:
		if a.LastBlock != nil && a.LastBlock.Question != "" {
			return alias + " is asking " + a.LastBlock.Question
		}
		return alias + " is blocked at a prompt"
	case herdr.StateWorking:
		if t := shortTask(a.CurrentTask); t != "" {
			return alias + " is working on " + t
		}
		return alias + " is working on something"
	case herdr.StateIdle, herdr.StateDone:
		if r := recentResult(a, now); r != "" {
			return alias + " is idle; it last " + r
		}
		return alias + " is idle"
	default:
		return alias + " has exited"
	}
}

// recentResult returns the trimmed last result when it is fresh enough
// to be worth saying, else "". The result is the announcement the
// shepherd heard about the finish, so it usually opens with the agent's
// own name; that comes off here, since the clause already says it ("auth
// is idle; it last finished the rerun you asked for").
func recentResult(a state.Agent, now time.Time) string {
	if a.LastResult == nil || a.LastResult.Spoken == "" || now.Sub(a.LastResult.At) > briefingResultMaxAge {
		return ""
	}
	spoken := strings.TrimRight(a.LastResult.Spoken, ".!?")
	for _, name := range []string{state.Say(a), a.Alias} {
		if name != "" && len(spoken) > len(name) && spoken[len(name)] == ' ' && strings.EqualFold(spoken[:len(name)], name) {
			spoken = lowerFirst(strings.TrimSpace(spoken[len(name):]))
			break
		}
	}
	return trimWords(spoken, briefingResultMaxWords)
}

// describeRest tallies the agents that were not listed: "the other two
// are working", "two more are working and one has exited".
func describeRest(rest []state.Agent) string {
	counts := map[int]int{}
	for _, a := range rest {
		counts[stateRank(a.State)]++
	}
	labels := []struct {
		rank int
		one  string
		many string
	}{{0, "is asking something", "are asking something"}, {1, "is working", "are working"}, {2, "is idle", "are idle"}, {3, "has exited", "have exited"}}
	var parts []string
	for _, l := range labels {
		n := counts[l.rank]
		if n == 0 {
			continue
		}
		verb := l.many
		if n == 1 {
			verb = l.one
		}
		if len(counts) == 1 {
			if n == 1 {
				return "the other one " + verb
			}
			return "the other " + strings.ToLower(countWord(n)) + " " + verb
		}
		parts = append(parts, strings.ToLower(countWord(n))+" more "+verb)
	}
	return joinSpoken(parts)
}

// pendingTrailer repeats the pending question at the end: the question
// itself when the sentence has not already said it.
func pendingTrailer(agents []state.Agent, pending state.PendingAction, sentence string) string {
	if pending.Kind != state.PendingAnswerPrompt {
		return ""
	}
	alias := ""
	for _, a := range agents {
		if a.PaneID == pending.PaneID {
			alias = state.Say(a)
		}
	}
	if alias == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(alias)
	name := string(unicode.ToUpper(r)) + alias[n:]
	if pending.Question == "" || strings.Contains(sentence, pending.Question) {
		return name + " is waiting for your answer."
	}
	return name + " is waiting for your answer on " + pending.Question + "."
}

// trimWords keeps the first n words of s.
func trimWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:n], " ")
}

// shortTask trims an instruction to something sayable inside a sentence.
func shortTask(t string) string {
	t = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t), ".!?"))
	if t == "" {
		return ""
	}
	words := strings.Fields(t)
	if len(words) > 10 {
		t = strings.Join(words[:10], " ") + "…"
	}
	return `"` + t + `"`
}

func joinSpoken(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		if strings.ContainsAny(parts[0]+parts[1], ",;") {
			return parts[0] + ", and " + parts[1]
		}
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

var countWords = []string{"No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten"}

func countWord(n int) string {
	if n >= 0 && n < len(countWords) {
		return countWords[n]
	}
	return strconv.Itoa(n)
}
