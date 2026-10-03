package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Investigations (docs/roles.md "Investigations and explanations"): the
// shepherd cannot see the screen, so "why did that fail" is a first-class
// request the screen often cannot answer. The brain's investigate tool
// starts a background job here and returns at once with its id and a
// rough estimate; the job runs the read-only CLI sub-agent in the
// worker's repository with no time limit beyond Config.InvestigateMax,
// and its answer comes back as one KindInvestigation event turn whose
// line is pushed at the next pause, tagged with the job it answers.
//
// The lifecycle lives here rather than in a package of its own for the
// same reason promises do: the roster, the working directory, the state
// file, the stop procedure and the push are all the supervisor's, and a
// separate package would need an interface for each of them. What is
// genuinely CLI-shaped — capturing the sub-agent's session and resuming
// it for a follow-up — lives in internal/cli, with the argv templates.
//
// One job per agent: a newer request about the same agent cancels the
// running one (investigation.cancelled {reason: superseded}), as does a
// stop and the daemon shutting down.
//
// Two sources (docs/dive-deep.md §3). The default, cli, starts a fresh
// read-only sub-agent in the worker's repository: it has not heard the
// conversation and never saw what the worker tried. worker asks the
// worker's own session instead, through a read-only fork of it (claude
// --fork-session, codex exec fork; docs/cli-query-tools.md §2c): the
// fork starts with everything the worker saw and did, answers from that
// memory, and writes only a session of its own, never into the one the
// worker's TUI has open and never into its pane. The fork's id is the
// job's session, so a follow-up resumes the fork, not the worker.

// defaultInvestigateETA is the estimate the acknowledgement carries
// before any job of that backend has finished.
const defaultInvestigateETA = 30 * time.Second

// investigateSummaryRunes bounds the one line the ledger keeps of an
// answer (the full text stays in the record for the delivery turn and
// for recall).
const investigateSummaryRunes = 200

// liveInvestigation is one running job: enough to cancel it and to know
// what it was about.
type liveInvestigation struct {
	id, paneID, alias string
	// asked is what the shepherd said that started it (the stop
	// procedure matches the withdrawn request against it).
	asked   string
	started time.Time
	cancel  context.CancelFunc
	// cancelled is set (under s.mu) when the job was cancelled, so its
	// goroutine delivers nothing.
	cancelled bool
}

// InvestigationQuery is one background run of the read-only CLI tool.
type InvestigationQuery struct {
	// Workspace is the agent's working directory.
	Workspace string
	// Question is what the sub-agent is asked, self-contained.
	Question string
	// Session, when set, resumes that sub-agent session instead of
	// starting a new one; with Fork, it is the worker's session to fork.
	Session string
	// Backend pins the CLI backend ("" applies cli.prefer). A fork runs
	// on the worker's own backend, and a follow-up on the one that kept
	// the session.
	Backend string
	// Fork asks Session through a read-only fork of it: the worker's own
	// session answers and is not written to (source worker).
	Fork bool
	// Timeout is the sanity ceiling of this run.
	Timeout time.Duration
	// Slot is the one-at-a-time slot the run claims, so a minutes-long
	// job does not lock out a short run_cli lookup in the same
	// repository.
	Slot string
}

// InvestigationResult is what one background run produced.
type InvestigationResult struct {
	// Backend is the CLI backend that ran it (codex, claude).
	Backend string
	// Answer is the sub-agent's answer verbatim.
	Answer string
	// SessionID is the session a follow-up resumes; "" when the backend
	// reported none.
	SessionID string
}

// backendChecker is the optional half of an Investigator that says
// whether a backend was detected, so asking the worker itself on a
// machine without that worker's CLI is refused at once with a reason
// instead of failing in the background.
type backendChecker interface {
	HasBackend(backend string) bool
}

// Investigator is the background half of the CLI query tool: the same
// read-only sub-agent, with its own time limit and its session kept so a
// follow-up can resume it. The daemon wires *cli.Runner behind it; a
// Querier without one still answers run_cli, and investigations are
// refused with brain.ErrNoCLI.
type Investigator interface {
	Investigate(ctx context.Context, q InvestigationQuery) (InvestigationResult, error)
}

// startInvestigation starts a background job about paneID and returns
// its id and estimate at once (the brain's investigate tool). ctx is the
// brain turn's: it bounds the roster lookup only, never the job, which
// runs on its own ceiling and outlives the turn.
func (s *Supervisor) startInvestigation(ctx context.Context, paneID string, req brain.InvestigationRequest) (brain.InvestigationStart, error) {
	if s.investigator == nil {
		return brain.InvestigationStart{}, brain.ErrNoCLI
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return brain.InvestigationStart{}, errors.New("that agent is gone")
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return brain.InvestigationStart{}, errors.New("investigate needs a question")
	}
	plan, err := s.planInvestigation(row, req, question)
	if err != nil {
		return brain.InvestigationStart{}, err
	}
	workspace := s.workspaceDir(ctx, row)
	if workspace == "" {
		workspace = row.Cwd
	}

	// A newer question about the same agent replaces the older look.
	s.cancelInvestigationFor(paneID, "superseded")

	now := s.cfg.Now()
	inv := state.Investigation{
		ID: s.nextID("inv"), PaneID: paneID, Alias: row.Alias, Workspace: workspace,
		Question: plan.question, Asked: strings.TrimSpace(req.Asked), FollowUpOf: req.FollowUpOf,
		Source: plan.source, Backend: plan.backend, SessionID: plan.jobSession(), Status: state.InvestigationRunning, Started: now,
	}
	if err := s.appendInvestigation(inv); err != nil {
		return brain.InvestigationStart{}, err
	}
	jobCtx, cancel := context.WithTimeout(context.Background(), s.cfg.InvestigateMax)
	live := &liveInvestigation{id: inv.ID, paneID: paneID, alias: aliasOr(row.Alias), asked: inv.Asked, started: now, cancel: cancel}
	s.mu.Lock()
	s.investigating[paneID] = live
	s.mu.Unlock()
	s.touch(paneID)
	backend := plan.backend
	if backend == "" {
		backend = s.investigatorBackend()
	}
	s.log.Log(logSource, "investigation.started", paneID, map[string]any{
		"id": inv.ID, "agent": aliasOr(row.Alias), "source": inv.Source, "backend": backend,
		"question": inv.Question, "follow_up_of": inv.FollowUpOf, "resumed": plan.session != "", "forked": plan.fork,
	})
	// An investigation event resets the "delivered" mark of
	// supervisor.repeat_window_s too: what the shepherd was last told
	// has moved on, even though the roster's own wording has not.
	s.invalidateBriefing()
	if !s.track() {
		// The daemon began shutting down meanwhile: the job is closed
		// out, not started.
		s.cancelInvestigationFor(paneID, "shutdown")
		return brain.InvestigationStart{}, errors.New("the daemon is shutting down")
	}
	go func() {
		defer s.replies.Done()
		s.runInvestigation(jobCtx, live, inv, InvestigationQuery{
			Workspace: inv.Workspace, Question: inv.Question, Session: plan.session,
			Backend: plan.backend, Fork: plan.fork,
			Timeout: s.cfg.InvestigateMax, Slot: "investigation:" + inv.PaneID,
		})
	}()
	return brain.InvestigationStart{Job: inv.ID, ETASeconds: int(s.investigateETA(backend).Seconds())}, nil
}

// investigationPlan is how one job runs: which source, on which backend
// and session, with what question.
type investigationPlan struct {
	source, backend, question string
	// session is the session the run resumes, or with fork the worker's
	// session it forks.
	session string
	fork    bool
}

// jobSession is the session the record starts with: a resumed session
// is the job's own, but a forked one is the worker's and must never
// become the job's (a follow-up would resume the worker and write into
// it); the fork's own id replaces it when the run reports one.
func (p investigationPlan) jobSession() string {
	if p.fork {
		return ""
	}
	return p.session
}

// planInvestigation resolves the source and follow_up_of. A follow-up
// continues the earlier job's source and resumes its session while it
// is still resumable (the fork, for a worker job); otherwise the
// question carries that job's answer so a cold start has what the
// shepherd already heard, and a worker job forks the worker again.
func (s *Supervisor) planInvestigation(row state.Agent, req brain.InvestigationRequest, question string) (investigationPlan, error) {
	source, err := investigationSource(req.Source)
	if err != nil {
		return investigationPlan{}, err
	}
	p := investigationPlan{source: source, question: question}
	if req.FollowUpOf != "" {
		prev, err := s.followUp(req.FollowUpOf)
		if err != nil {
			return investigationPlan{}, err
		}
		prevSource := prev.Source
		if prevSource == "" {
			prevSource = state.InvestigationSourceCLI
		}
		if strings.TrimSpace(req.Source) != "" && source != prevSource {
			return investigationPlan{}, fmt.Errorf("investigation %s asked %s, and a follow-up continues it; leave out follow_up_of to ask %s", prev.ID, sourceWhat(prevSource), sourceWhat(source))
		}
		p.source = prevSource
		if prev.SessionID != "" && s.resumable(prev) {
			p.session, p.backend = prev.SessionID, prev.Backend
			return p, nil
		}
		// No session to resume (the backend reported none, or it is too
		// old to be there): the sub-agent starts cold with what it said
		// before.
		p.question = fmt.Sprintf("Earlier you were asked: %s\nYou answered:\n%s\n\nNow: %s", prev.Question, prev.Result, question)
	}
	if p.source == state.InvestigationSourceWorker {
		backend, session, err := s.workerFork(row)
		if err != nil {
			return investigationPlan{}, err
		}
		p.backend, p.session, p.fork = backend, session, true
	}
	return p, nil
}

// investigationSource normalises the tool's source argument.
func investigationSource(source string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(source)); v {
	case "", state.InvestigationSourceCLI:
		return state.InvestigationSourceCLI, nil
	case state.InvestigationSourceWorker:
		return v, nil
	default:
		return "", fmt.Errorf("investigate source %q: want %s (the default, the repository) or %s (the worker's own session)", source, state.InvestigationSourceCLI, state.InvestigationSourceWorker)
	}
}

// sourceWhat names a source for an error the model reads.
func sourceWhat(source string) string {
	if source == state.InvestigationSourceWorker {
		return "the worker itself"
	}
	return "the repository"
}

// followUp returns the earlier job follow_up_of names, when it can be
// followed up.
func (s *Supervisor) followUp(id string) (state.Investigation, error) {
	prev, ok := s.investigation(id)
	if !ok {
		return state.Investigation{}, fmt.Errorf("there is no investigation %q; the ledger is in your prompt", id)
	}
	switch prev.Status {
	case state.InvestigationRunning:
		return state.Investigation{}, fmt.Errorf("investigation %s is still running; its answer comes on its own", id)
	case state.InvestigationDone:
		return prev, nil
	default:
		return state.Investigation{}, fmt.Errorf("investigation %s did not finish, so there is nothing to follow up on", id)
	}
}

// sessionUUID matches the session ids both backends use: claude's
// session_id, codex's thread id. herdr reports them; they reach an argv,
// so nothing else is accepted.
var sessionUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// workerFork resolves the worker's own session for source worker: the
// backend that keeps it and the id its fork takes. A session sheepdog
// cannot find is refused with the worker.session_missing reason, for the
// model to read, pointing it at the default source.
func (s *Supervisor) workerFork(row state.Agent) (backend, session string, err error) {
	alias := aliasOr(row.Alias)
	refuse := func(why string) error {
		return fmt.Errorf("cannot ask %s itself: %s; investigate without source asks a read-only agent in its repository instead", alias, why)
	}
	sess := workerSession(row)
	if _, err := s.cfg.WorkerLog.Locate(sess); err != nil {
		reason := workerlog.Reason(err)
		return "", "", refuse(reason + " (" + sessionMissingWhy[reason] + ")")
	}
	backend = workerBackend(sess)
	session = sess.Value
	if sess.Kind == herdr.SessionKindPath {
		// Both backends name the transcript after the session: claude
		// <id>.jsonl, codex rollout-<timestamp>-<id>.jsonl.
		stem := strings.TrimSuffix(filepath.Base(sess.Value), ".jsonl")
		session = stem[max(0, len(stem)-36):]
	}
	if !sessionUUID.MatchString(session) {
		return "", "", refuse(workerlog.ReasonNotFound + " (its session id is not one the " + backend + " CLI can resume)")
	}
	if c, ok := s.investigator.(backendChecker); ok && !c.HasBackend(backend) {
		return "", "", refuse("the " + backend + " CLI that keeps its session is not installed here")
	}
	return backend, session, nil
}

// workerBackend names the CLI that keeps a worker's session, from the
// reporting integration or the pane's kind (workerlog.Locate has already
// refused any other).
func workerBackend(sess workerlog.Session) string {
	for _, v := range []string{sess.Source, sess.Agent} {
		v = strings.ToLower(v)
		switch {
		case strings.Contains(v, "claude"):
			return "claude"
		case strings.Contains(v, "codex"):
			return "codex"
		}
	}
	return ""
}

// resumable reports whether a finished job's sub-agent session is still
// within Config.InvestigateSessionTTL.
func (s *Supervisor) resumable(inv state.Investigation) bool {
	if inv.SessionID == "" || inv.Finished == nil {
		return false
	}
	return s.cfg.Now().Sub(*inv.Finished) < s.cfg.InvestigateSessionTTL
}

// runInvestigation runs one job and delivers what it found. It never
// holds s.mu across the run: the job takes minutes and everything else
// (a shepherd turn, a worker event, a stop) must keep working.
func (s *Supervisor) runInvestigation(ctx context.Context, live *liveInvestigation, inv state.Investigation, q InvestigationQuery) {
	defer live.cancel()
	res, err := s.investigator.Investigate(ctx, q)
	s.mu.Lock()
	cancelled := live.cancelled
	if s.investigating[inv.PaneID] == live {
		delete(s.investigating, inv.PaneID)
	}
	s.mu.Unlock()
	if cancelled {
		// cancelInvestigation already closed the record and said so.
		return
	}
	now := s.cfg.Now()
	dur := now.Sub(inv.Started)
	if err != nil {
		inv.Status, inv.Err, inv.Finished = state.InvestigationError, err.Error(), &now
		s.saveInvestigation(inv)
		s.log.Log(logSource, "investigation.error", inv.PaneID, map[string]any{"id": inv.ID, "agent": aliasOr(inv.Alias), "dur_ms": dur.Milliseconds(), "error": err.Error()})
		s.invalidateBriefing()
		s.pushInvestigation(inv, "The look into "+aliasOr(inv.Alias)+" did not get through; ask me to try again.")
		return
	}
	inv.Status, inv.Finished = state.InvestigationDone, &now
	inv.Result, inv.Backend = strings.TrimSpace(res.Answer), res.Backend
	inv.Summary = investigationSummary(inv.Result)
	if res.SessionID != "" || q.Fork {
		// A fork's session is only ever the one it reported: the
		// worker's own id never becomes the job's.
		inv.SessionID = res.SessionID
	}
	s.saveInvestigation(inv)
	s.log.Log(logSource, "investigation.done", inv.PaneID, map[string]any{
		"id": inv.ID, "agent": aliasOr(inv.Alias), "backend": inv.Backend, "dur_ms": dur.Milliseconds(),
		"bytes": len(inv.Result), "session_id_present": inv.SessionID != "",
	})
	s.invalidateBriefing()
	s.deliverInvestigation(inv)
}

// deliverInvestigation runs the delivery event turn and pushes its line
// at the next pause (docs/roles.md: the shepherd is expecting it, so it
// never interrupts). With no brain, or one that could not answer, the
// daemon says the look is done and keeps the answer in the ledger.
func (s *Supervisor) deliverInvestigation(inv state.Investigation) {
	fallback := "The look into " + aliasOr(inv.Alias) + " is done; ask me what it found."
	if s.brain == nil {
		s.pushInvestigation(inv, fallback)
		return
	}
	info := investigationInfo(inv, s.resumable(inv))
	req := brain.Request{
		Kind: brain.KindInvestigation, Trigger: brain.TriggerEvent, Agent: aliasOr(inv.Alias),
		Event: "investigation.done", Question: inv.Question, Template: fallback,
		Result: inv.Result, Investigation: &info,
		Facts: func() brain.EventFacts { return brain.EventFacts{Language: s.ceoLanguage()} },
	}
	// The turn runs under s.life: shutdown ends it and waits for this
	// goroutine (replies) before the brain closes.
	rep, err := s.brain.Turn(s.life, req)
	if errors.Is(err, brain.ErrYielded) {
		// The shepherd spoke while the answer was being composed: an
		// event turn gives way, but this one is the answer they are
		// waiting for, so ask once more behind their turn.
		rep, err = s.brain.Turn(s.life, req)
	}
	if s.life.Err() != nil {
		// The daemon is going down and the voice with it: the answer
		// stays in the ledger (saved above), nothing is pushed.
		return
	}
	if err != nil {
		s.pushInvestigation(inv, fallback)
		return
	}
	// The finding is heard once, like any line: the ear caps apply and
	// the ask is the last sentence (prose.EarLine). What the cap cut
	// stays in the brain's own transcript, where a follow-up and recall
	// find it.
	spoken, _ := earLine(rep)
	if spoken == "" {
		spoken = fallback
	}
	s.mentionedIn(spoken)
	s.pushInvestigation(inv, spoken)
}

// pushInvestigation speaks one line about a job, tagged with the job it
// answers (voice.push {answers_job}, which the Conversation view links
// to the acknowledgement). It waits for a pause and is not gated by the
// chattiness ceiling: the shepherd asked for this answer.
func (s *Supervisor) pushInvestigation(inv state.Investigation, text string) {
	s.pushTagged(inv.PaneID, text, voice.UrgencyIdle, map[string]any{"answers_job": inv.ID})
}

// cancelInvestigationFor cancels the running job of a pane, if any, and
// returns it. Reasons: superseded (a newer question about the same
// agent), stop, shutdown.
func (s *Supervisor) cancelInvestigationFor(paneID, reason string) (state.Investigation, bool) {
	s.mu.Lock()
	live := s.investigating[paneID]
	if live == nil {
		s.mu.Unlock()
		return state.Investigation{}, false
	}
	live.cancelled = true
	delete(s.investigating, paneID)
	s.mu.Unlock()
	return s.finishCancelled(live, reason)
}

// cancelLatestInvestigation cancels the most recently started running
// job, whichever agent it is about (the stop procedure with nothing else
// to go on).
func (s *Supervisor) cancelLatestInvestigation(reason string) (state.Investigation, bool) {
	s.mu.Lock()
	var newest *liveInvestigation
	for _, live := range s.investigating {
		if newest == nil || live.started.After(newest.started) {
			newest = live
		}
	}
	if newest == nil {
		s.mu.Unlock()
		return state.Investigation{}, false
	}
	newest.cancelled = true
	delete(s.investigating, newest.paneID)
	s.mu.Unlock()
	return s.finishCancelled(newest, reason)
}

// finishCancelled stops the child, closes the record and logs
// investigation.cancelled.
func (s *Supervisor) finishCancelled(live *liveInvestigation, reason string) (state.Investigation, bool) {
	live.cancel()
	now := s.cfg.Now()
	inv, ok := s.investigation(live.id)
	if !ok {
		return state.Investigation{}, false
	}
	inv.Status, inv.Err, inv.Finished = state.InvestigationCancelled, reason, &now
	s.saveInvestigation(inv)
	s.log.Log(logSource, "investigation.cancelled", inv.PaneID, map[string]any{"id": inv.ID, "agent": aliasOr(inv.Alias), "reason": reason, "dur_ms": now.Sub(inv.Started).Milliseconds()})
	s.invalidateBriefing()
	return inv, true
}

// cancelInvestigations cancels every running job (the daemon is going
// down).
func (s *Supervisor) cancelInvestigations(reason string) {
	for {
		if _, ok := s.cancelLatestInvestigation(reason); !ok {
			return
		}
	}
}

// appendInvestigation records a new job, dropping the oldest past
// state.MaxInvestigations.
func (s *Supervisor) appendInvestigation(inv state.Investigation) error {
	return s.store.UpdateSession(func(ss *state.Session) {
		ss.Investigations = append(ss.Investigations, inv)
		if n := len(ss.Investigations) - state.MaxInvestigations; n > 0 {
			ss.Investigations = append([]state.Investigation(nil), ss.Investigations[n:]...)
		}
	})
}

// saveInvestigation writes a changed record back (a job that fell off
// the ledger is not resurrected).
func (s *Supervisor) saveInvestigation(inv state.Investigation) {
	if err := s.store.UpdateSession(func(ss *state.Session) {
		for i := range ss.Investigations {
			if ss.Investigations[i].ID == inv.ID {
				ss.Investigations[i] = inv
			}
		}
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", inv.PaneID, map[string]any{"error": err.Error(), "via": "investigation"})
	}
}

// investigation returns one record by id.
func (s *Supervisor) investigation(id string) (state.Investigation, bool) {
	for _, inv := range s.store.Snapshot().Session.Investigations {
		if inv.ID == id {
			return inv, true
		}
	}
	return state.Investigation{}, false
}

// investigations returns the ledger, oldest first.
func (s *Supervisor) investigations() []state.Investigation {
	return s.store.Snapshot().Session.Investigations
}

// restoreInvestigations closes jobs the state file says were running
// when the daemon stopped: their sub-agent is gone with the process, and
// a ghost would block that agent's one live slot for good. Called once
// from New.
func (s *Supervisor) restoreInvestigations() {
	now := s.cfg.Now()
	var closed []state.Investigation
	if err := s.store.UpdateSession(func(ss *state.Session) {
		for i := range ss.Investigations {
			if ss.Investigations[i].Status != state.InvestigationRunning {
				continue
			}
			ss.Investigations[i].Status = state.InvestigationError
			ss.Investigations[i].Err = "the daemon restarted while it was running"
			ss.Investigations[i].Finished = &now
			closed = append(closed, ss.Investigations[i])
		}
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", "", map[string]any{"error": err.Error(), "via": "investigation_restore"})
	}
	for _, inv := range closed {
		s.log.Log(logSource, "investigation.error", inv.PaneID, map[string]any{"id": inv.ID, "agent": aliasOr(inv.Alias), "error": inv.Err})
	}
}

// investigateETA is the estimate the acknowledgement carries: the median
// of that backend's finished jobs, or defaultInvestigateETA before there
// are any (backends differ enough that one's history says little about
// the other's).
func (s *Supervisor) investigateETA(backend string) time.Duration {
	var durs []time.Duration
	for _, inv := range s.investigations() {
		if inv.Status == state.InvestigationDone && inv.Finished != nil && (backend == "" || inv.Backend == backend) {
			durs = append(durs, inv.Finished.Sub(inv.Started))
		}
	}
	if len(durs) == 0 {
		return defaultInvestigateETA
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return durs[len(durs)/2]
}

// investigatorBackend names the backend investigations will use, for the
// started event (the result carries the one that actually ran).
func (s *Supervisor) investigatorBackend() string {
	if b, ok := s.investigator.(interface{ ResolveBackend() (string, error) }); ok {
		if name, err := b.ResolveBackend(); err == nil {
			return name
		}
	}
	return ""
}

// investigationSummary is the one line the ledger and recall keep of an
// answer: its first sentence, bounded.
func investigationSummary(result string) string {
	line := strings.TrimSpace(result)
	if i := strings.IndexAny(line, "\n"); i > 0 {
		line = line[:i]
	}
	return prose.TruncateRunes(strings.TrimSpace(line), investigateSummaryRunes)
}

// investigationInfo converts a record for the brain.
func investigationInfo(inv state.Investigation, resumable bool) brain.InvestigationInfo {
	return brain.InvestigationInfo{
		ID: inv.ID, Agent: inv.Alias, Question: inv.Question, Asked: inv.Asked, FollowUpOf: inv.FollowUpOf,
		Source: inv.Source, Backend: inv.Backend, Status: inv.Status, Started: inv.Started, Finished: inv.Finished,
		Result: inv.Result, Summary: inv.Summary, Err: inv.Err, Resumable: resumable,
	}
}
