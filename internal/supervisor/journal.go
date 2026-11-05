package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Worker history (docs/dev/roles.md "Worker history", docs/dev/iteration-2.md
// WP4). The orchestrator used to know a worker only by its screen — tool output,
// paths, progress bars — so its lines relayed the worker's working notes
// (the 2026-09-24 baseline: about two jargon terms per orchestrator line; "why
// don't you just tell me the findings if it's finished", 00:00:18). The
// worker already writes for a human in its own session transcript, and
// herdr reports where that is (agent_session). On every finish or block
// the supervisor reads what is new there and appends one journal entry
// to the agent's row; the announce turn hears the worker's last message
// before its screen, the ledger shows the last entries, recall searches
// them all.

// The journal entry caps: small enough that the entry can sit in the
// state file and the orchestrator's prompt as is.
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
// orchestrator sends through its prompt tool (never the model: brainHost.Prompt),
// so the worker's last message — the journal's last_assistant — ends with
// what the user needs to hear. It is not the user's words: the
// pending action's summary, the "started on …" push, the current task
// and the journal all use the instruction without it (withoutSignOff).
const SignOff = "When you finish, end with three short lines for someone who cannot see the screen: what changed; what needs a decision; what is next."

// withSignOff appends SignOff to an instruction, after terminating it
// the CJK-aware way (prose.Terminate): the old check looked at the last
// byte and knew only ASCII, so a Chinese instruction reached the worker
// as "请运行 shop 相关的测试。. When you finish, …" (tier-3 bench
// 2026-10-03, finding 6; the H1 seam).
//
// A slash command (isSlashCommand: one line starting with "/", such as
// "/permissions", "/model gpt-5" or "/compact") is sent exactly as given,
// with no trailer: the worker's TUI only recognises it when the line
// starts with the command and holds nothing after it. Sentence
// punctuation the model added at the very end ("/permissions.",
// "/permissions。") is stripped too (trimCommandPunct). Multi-line text
// that starts with "/" is prose and keeps the trailer.
func withSignOff(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, SignOff) {
		return text
	}
	if picturePath.MatchString(text) {
		return text + "\n" + SignOff
	}
	if isSlashCommand(text) {
		return trimCommandPunct(text)
	}
	return prose.Terminate(text) + " " + SignOff
}

// isSlashCommand reports whether an instruction is a worker slash
// command: after trimming, a single line that starts with "/".
func isSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") && !strings.ContainsAny(text, "\r\n") && !picturePath.MatchString(text)
}

// trimCommandPunct drops sentence punctuation and quotes from the very
// end of a slash command; its arguments stay as given.
func trimCommandPunct(text string) string {
	return strings.TrimRight(text, " .。,，!！?？;；:：\"'`”’」』")
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

// processInfoTimeout bounds one pane.process_info call so a slow socket
// never stalls a transition or session read.
const processInfoTimeout = 500 * time.Millisecond

// ProcessInspector is the optional herdr capability that reports a pane's
// foreground process tree (pane.process_info on herdr, terminal.get on
// herdr-core).
type ProcessInspector interface {
	ProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
}

func isUnsupportedMethod(err error) bool {
	var he *herdr.Error
	return errors.As(err, &he) && he.Code == "invalid_request" && strings.Contains(he.Message, "unknown variant")
}

func rootsFromProcessInfo(info herdr.ProcessInfo) workerlog.ProcessRoots {
	if info.ShellForeground() {
		return workerlog.ProcessRoots{}
	}
	var fg int
	for _, p := range info.ForegroundProcesses {
		if p.PID > 0 && p.PID != info.ShellPID {
			fg = int(p.PID)
			break
		}
	}
	if fg == 0 && len(info.ForegroundProcesses) > 0 && info.ForegroundProcesses[0].PID > 0 {
		fg = int(info.ForegroundProcesses[0].PID)
	}
	return workerlog.ProcessRoots{
		PID:      fg,
		Pgrp:     int(info.ForegroundProcessGroup),
		ShellPID: int(info.ShellPID),
	}
}

func (s *Supervisor) paneRoots(ctx context.Context, row state.Agent) workerlog.ProcessRoots {
	if row.PaneID == "" || s.noProcessInfo.Load() {
		return workerlog.ProcessRoots{}
	}
	if row.Kind != "" && !workerlog.SupportsKind(row.Kind) {
		return workerlog.ProcessRoots{}
	}
	pi, ok := s.herdr.(ProcessInspector)
	if !ok {
		return workerlog.ProcessRoots{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, processInfoTimeout)
	info, err := pi.ProcessInfo(pctx, row.PaneID)
	cancel()
	if err != nil {
		if isUnsupportedMethod(err) {
			s.noProcessInfo.Store(true)
		}
		return workerlog.ProcessRoots{}
	}
	return rootsFromProcessInfo(info)
}

// workerSessionWithRoots is the row's session for workerlog, carrying the
// pane's live foreground process roots when known.
func workerSessionWithRoots(row state.Agent, roots workerlog.ProcessRoots) workerlog.Session {
	return workerlog.Session{
		Pane:   row.PaneID,
		Roots:  roots,
		Agent:  row.Kind,
		Source: row.SessionSource,
		Kind:   row.SessionKind,
		Value:  row.SessionValue,
		Cwd:    row.Cwd,
	}
}

// syncDiscoveredSession persists a PID-discovered session id onto the state
// row so later restarts and diagnostics see the active session without
// waiting for a 30s snapshot.
func (s *Supervisor) syncDiscoveredSession(paneID string) {
	if paneID == "" || s.cfg.WorkerLog == nil {
		return
	}
	disc, ok := s.cfg.WorkerLog.Discovered(paneID)
	if !ok || disc.ID == "" {
		return
	}
	row, exists := s.store.Get(paneID)
	if !exists {
		return
	}
	if row.SessionKind == herdr.SessionKindID && row.SessionValue == disc.ID {
		return
	}
	_, _ = s.store.Update(paneID, func(a *state.Agent) {
		if a.SessionKind == herdr.SessionKindID && a.SessionValue == disc.ID {
			return
		}
		a.SessionSource = disc.Source
		a.SessionKind = herdr.SessionKindID
		a.SessionValue = disc.ID
	})
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
func (s *Supervisor) recordJournal(ctx context.Context, row state.Agent, roots workerlog.ProcessRoots, event string, finish bool) *workerlog.TurnError {
	alias := aliasOr(row.Alias)
	sess := workerSessionWithRoots(row, roots)
	d, err := s.cfg.WorkerLog.Read(row.PaneID, sess)
	s.syncDiscoveredSession(row.PaneID)
	if err == nil && !d.Fresh && d.Error == nil && finish && journalRetry > 0 {
		if d.Bytes == 0 {
			// A same-PID session rotation (/new in Codex) leaves 0 new bytes
			// on the previous rollout file: drop the cached FD probe before
			// the retry so a rotated rollout is found.
			s.cfg.WorkerLog.Invalidate(row.PaneID)
			sess.Roots = s.paneRoots(ctx, row)
		}
		time.Sleep(journalRetry)
		if next, nerr := s.cfg.WorkerLog.Read(row.PaneID, sess); nerr == nil {
			d.Merge(next)
		}
		s.syncDiscoveredSession(row.PaneID)
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
			return nil
		}
	}

	e := state.JournalEntry{At: s.cfg.Now(), Event: event, Tools: d.Tools}
	if d.Fresh {
		e.LastAssistant = workerlog.Clip(scrubLines(s.cfg.Redactor.String(d.LastAssistant)), journalAssistantRunes)
	}
	if d.Error != nil {
		e.Error = d.Error.Summary()
	}
	instruction := withoutSignOff(s.cfg.Redactor.String(d.Instruction()))
	if instruction == "" {
		instruction = withoutSignOff(s.cfg.Redactor.String(row.CurrentTask))
	}
	e.Instruction = workerlog.Clip(prose.Spoken(instruction, 0), journalInstructionRunes)
	e.FilesTouched = d.Files
	if e.LastAssistant == "" && e.Instruction == "" && len(e.Tools) == 0 && len(e.FilesTouched) == 0 && e.Error == "" {
		return d.Error
	}
	if err := s.store.AppendJournal(row.PaneID, e); err != nil {
		s.log.Log(logSource, "supervisor.state_error", row.PaneID, map[string]any{"error": err.Error()})
		return d.Error
	}
	logPayload := map[string]any{"agent": alias, "event": event, "bytes": d.Bytes, "files": d.Files}
	if d.Error != nil {
		logPayload["stop_kind"] = string(d.Error.Kind)
	}
	s.log.Log(logSource, "worker.journal", row.PaneID, logPayload)
	return d.Error
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

// journalInfo converts the row's journal for the orchestrator.
func journalInfo(journal []state.JournalEntry) []brain.JournalEntry {
	if len(journal) == 0 {
		return nil
	}
	out := make([]brain.JournalEntry, 0, len(journal))
	for _, e := range journal {
		out = append(out, brain.JournalEntry{At: e.At, Event: e.Event, LastAssistant: e.LastAssistant, Instruction: e.Instruction, Error: e.Error, Files: e.FilesTouched, Tools: e.Tools})
	}
	return out
}
