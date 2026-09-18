package supervisor

import (
	"sort"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// briefingText returns the cached briefing, recomputing it from the state
// table when a state change invalidated it. No model is involved.
func (s *Supervisor) briefingText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.briefing == "" {
		pending, _ := s.store.Pending()
		s.briefing = composeBriefing(s.store.List(), pending)
	}
	return s.briefing
}

// invalidateBriefing forces the next briefing to be recomputed.
func (s *Supervisor) invalidateBriefing() {
	s.mu.Lock()
	s.briefing = ""
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

// composeBriefing builds the one-sentence status from the table (plan §5):
// "Three agents: auth is blocked, asking whether it can create hello.txt
// and waiting for your answer, docs is working on the readme, and vps is
// idle."
func composeBriefing(agents []state.Agent, pending state.PendingAction) string {
	if len(agents) == 0 {
		return "No agents are running."
	}
	sort.SliceStable(agents, func(i, j int) bool {
		ri, rj := stateRank(agents[i].State), stateRank(agents[j].State)
		if ri != rj {
			return ri < rj
		}
		return agents[i].Alias < agents[j].Alias
	})
	parts := make([]string, 0, len(agents))
	for _, a := range agents {
		parts = append(parts, describeAgent(a, pending))
	}
	head := "One agent: "
	if len(agents) > 1 {
		head = countWord(len(agents)) + " agents: "
	}
	return head + joinSpoken(parts) + "."
}

func describeAgent(a state.Agent, pending state.PendingAction) string {
	alias := a.Alias
	if alias == "" {
		alias = "an agent"
	}
	switch a.State {
	case herdr.StateBlocked:
		s := alias + " is blocked"
		if a.LastBlock != nil && a.LastBlock.Question != "" {
			s += ", asking " + a.LastBlock.Question
		} else {
			s += " at a prompt"
		}
		if pending.Kind == state.PendingAnswerPrompt && pending.PaneID == a.PaneID {
			s += " and waiting for your answer"
		}
		return s
	case herdr.StateWorking:
		if t := shortTask(a.CurrentTask); t != "" {
			return alias + " is working on " + t
		}
		return alias + " is working"
	case herdr.StateIdle, herdr.StateDone:
		return alias + " is idle"
	default:
		return alias + " is not running"
	}
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
		if strings.Contains(parts[0], ", ") || strings.Contains(parts[1], ", ") {
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
