package supervisor

import (
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Worker history (docs/roles.md "Worker history", docs/iteration-2.md
// WP4). The brain used to know a worker only by its screen — tool output,
// paths, progress bars — so its lines relayed the worker's working notes
// (the 2026-09-24 baseline: about two jargon terms per brain line; "why
// don't you just tell me the findings if it's finished", 00:00:18). The
// worker already writes for a human in its own session transcript, and
// herdr reports where that is (agent_session). On every finish or block
// the supervisor reads what is new there and appends one journal entry
// to the agent's row; the announce turn hears the worker's last message
// before its screen, the ledger shows the last entries, recall searches
// them all.

// The journal entry caps: small enough that the entry can sit in the
// state file and the brain's prompt as is.
const (
	journalAssistantRunes   = 600
	journalInstructionRunes = 200
)

// journalRetry is how long a finish waits, once, for the worker's final
// message when the transcript has none yet: herdr can see the finish on
// the screen a moment before the line lands in the file. A var so tests
// can shorten it.
var journalRetry = 300 * time.Millisecond

// SignOff is the trailer the supervisor appends to every instruction the
// brain sends through its prompt tool (never the model: brainHost.Prompt),
// so the worker's last message — the journal's last_assistant — ends with
// what the shepherd needs to hear. It is not the shepherd's words: the
// pending action's summary, the "started on …" push, the current task
// and the journal all use the instruction without it (withoutSignOff).
const SignOff = "When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next."

// withSignOff appends SignOff to an instruction.
func withSignOff(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, SignOff) {
		return text
	}
	if !strings.ContainsAny(text[len(text)-1:], ".!?") {
		text += "."
	}
	return text + " " + SignOff
}

// withoutSignOff strips SignOff from an instruction, wherever the
// instruction was read back from (the pending payload, the row, the
// worker's own transcript).
func withoutSignOff(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndex(text, SignOff); i >= 0 {
		text = strings.TrimSpace(text[:i] + text[i+len(SignOff):])
	}
	return text
}

// journalState is the journal's bookkeeping: the last reason each pane's
// transcript could not be read, so worker.session_missing is logged once
// per pane and reason rather than at every transition.
type journalState struct {
	mu      sync.Mutex
	missing map[string]string
	// signOff logs the trailer (supervisor.signoff) once per run, the
	// first time an instruction carries it.
	signOff sync.Once
}

// unsignTask strips the trailer from the row's current task after the
// send path recorded the instruction as sent.
func (s *Supervisor) unsignTask(paneID string) {
	_, err := s.store.Update(paneID, func(a *state.Agent) { a.CurrentTask = withoutSignOff(a.CurrentTask) })
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// noteMissing reports whether reason is new for paneID.
func (j *journalState) noteMissing(paneID, reason string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.missing == nil {
		j.missing = map[string]string{}
	}
	if j.missing[paneID] == reason {
		return false
	}
	j.missing[paneID] = reason
	return true
}

func (j *journalState) clearMissing(paneID string) {
	j.mu.Lock()
	delete(j.missing, paneID)
	j.mu.Unlock()
}

// workerSession is the row's session for workerlog.
func workerSession(row state.Agent) workerlog.Session {
	return workerlog.Session{Agent: row.Kind, Source: row.SessionSource, Kind: row.SessionKind, Value: row.SessionValue, Cwd: row.Cwd}
}

// recordJournal reads what is new in the worker's transcript and appends
// one journal entry for the transition to event (done, idle or blocked).
// It runs before the announce turn starts, so that turn can carry the
// worker's own last message (brain.workerWrote). row is the state row as
// it was at the transition (its current task not yet moved to
// last_task). finish marks the end of a working turn: when the
// transcript has no new message for it yet, the read is retried once
// after journalRetry. A transcript that cannot be read still leaves an
// entry when the row knows the instruction.
func (s *Supervisor) recordJournal(row state.Agent, event string, finish bool) {
	alias := aliasOr(row.Alias)
	d, err := s.cfg.WorkerLog.Read(row.PaneID, workerSession(row))
	if err == nil && !d.Fresh && finish && journalRetry > 0 {
		time.Sleep(journalRetry)
		if next, nerr := s.cfg.WorkerLog.Read(row.PaneID, workerSession(row)); nerr == nil {
			d.Merge(next)
		}
	}
	if err != nil {
		reason := workerlog.Reason(err)
		if s.journal.noteMissing(row.PaneID, reason) {
			s.log.Log(logSource, "worker.session_missing", row.PaneID, map[string]any{"agent": alias, "reason": reason})
		}
	} else {
		s.journal.clearMissing(row.PaneID)
		if d.Bytes == 0 {
			// Nothing new since the last entry (a re-prompt, a block
			// right after a finish): no entry repeats the one before.
			return
		}
	}

	e := state.JournalEntry{At: s.cfg.Now(), Event: event, Tools: d.Tools}
	if d.Fresh {
		e.LastAssistant = workerlog.Clip(scrubLines(d.LastAssistant), journalAssistantRunes)
	}
	instruction := withoutSignOff(d.Instruction())
	if instruction == "" {
		instruction = withoutSignOff(row.CurrentTask)
	}
	e.Instruction = workerlog.Clip(prose.Spoken(instruction, 0), journalInstructionRunes)
	e.FilesTouched = d.Files
	if e.LastAssistant == "" && e.Instruction == "" && len(e.Tools) == 0 && len(e.FilesTouched) == 0 {
		return
	}
	if err := s.store.AppendJournal(row.PaneID, e); err != nil {
		s.log.Log(logSource, "supervisor.state_error", row.PaneID, map[string]any{"error": err.Error()})
		return
	}
	s.log.Log(logSource, "worker.journal", row.PaneID, map[string]any{"agent": alias, "event": event, "bytes": d.Bytes, "files": d.Files})
}

// scrubLines applies D9 (prose.Spoken) line by line, keeping the line
// breaks, so the worker's three sign-off lines stay three lines for the
// announce turn.
func scrubLines(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = prose.Spoken(line, 0); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// journalInfo converts the row's journal for the brain.
func journalInfo(journal []state.JournalEntry) []brain.JournalEntry {
	if len(journal) == 0 {
		return nil
	}
	out := make([]brain.JournalEntry, 0, len(journal))
	for _, e := range journal {
		out = append(out, brain.JournalEntry{At: e.At, Event: e.Event, LastAssistant: e.LastAssistant, Instruction: e.Instruction, Files: e.FilesTouched, Tools: e.Tools})
	}
	return out
}
