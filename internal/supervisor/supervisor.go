package supervisor

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// logSource is the source field of every event this package logs.
const logSource = "supervisor"

// Herdr is the subset of *herdr.Client the Supervisor needs.
type Herdr interface {
	Read(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error)
	Prompt(ctx context.Context, paneID, text string) (herdr.AgentInfo, error)
	SendKeys(ctx context.Context, paneID string, keys ...string) error
	Interrupt(ctx context.Context, paneID string) error
	ListAgents(ctx context.Context) ([]herdr.AgentInfo, error)
}

var _ Herdr = (*herdr.Client)(nil)

// Pusher delivers spoken text (or silent context) to the voice session;
// voice.Session satisfies it.
type Pusher interface {
	Push(text string, u voice.Urgency) error
}

// TagPusher is a Pusher that carries a tag onto the voice.push event it
// logs — answers_job, the investigation whose answer this push is, which
// the Conversation view links back to the acknowledgement. A Pusher
// without it still speaks the line; only the tag is lost.
type TagPusher interface {
	Pusher
	PushTagged(text string, u voice.Urgency, tags map[string]any) error
}

// Querier is the read-only CLI query tool of plan §3.5 (WP4). workspace
// is the agent's working directory; the result is raw text the brain
// reads through its run_cli tool. nil means there is no such tool.
type Querier interface {
	Query(ctx context.Context, workspace, question string) (string, error)
}

// Config tunes the Supervisor; zero values take the defaults.
type Config struct {
	// PendingTTL is how long a pending action stays confirmable (default
	// 120 s).
	PendingTTL time.Duration
	// ExpiryTick is the period of the expiry check in Run (default 1 s).
	ExpiryTick time.Duration
	// ReadLines is how many recent lines a pane read asks for (default
	// 200: enough to see an agent's final message above its status bar).
	ReadLines int
	// DataDir is where notes.jsonl is appended; empty keeps notes in the
	// state table only.
	DataDir string
	// Chattiness is the initial push policy ("quiet" | "normal" | "chatty",
	// default normal); the session's runtime choice, when one was made,
	// wins over it.
	Chattiness string
	// RecentWindow is how recently the user must have dealt with an agent
	// for the normal level to announce its finish (default 30 min).
	RecentWindow time.Duration
	// PromiseTTL is how long an open promise waits for its event before
	// it expires silently (default 2 h).
	PromiseTTL time.Duration
	// CancelWindow is how soon after an instruction went to a working
	// agent a stop still interrupts it (default 30 s, docs/roles.md
	// "Stop" phase 3).
	CancelWindow time.Duration
	// RepromptPoll is how long the post-answer poll watches a pane after
	// keys go to it, in case it is already showing a different prompt
	// within herdr's own poll window with no state change in between
	// (docs/roles.md "blocked -> blocked is a block", reprompt.go). Zero
	// (the default) turns the poll off: production always sets
	// supervisor.reprompt_poll_s (10 s); a test that does not exercise
	// this feature never spawns its goroutine.
	RepromptPoll time.Duration
	// RepromptPollInterval is the poll's cadence (default 1 s; tests
	// only — not in CONFIG.md, the design fixes it at 1 s).
	RepromptPollInterval time.Duration
	// SnapshotInterval is how often the reconciler (reprompt.go) sweeps
	// every blocked pane for a prompt the poll's window closed on or one
	// that appeared while the daemon was down (default 30 s: the same
	// cadence as the herdr Watcher's own session.snapshot diff, config
	// snapshot_interval_s, though the two loops are not the same one).
	SnapshotInterval time.Duration
	// InvestigateMax is the sanity ceiling of one background
	// investigation — the only time limit on its answer (default 600 s,
	// docs/roles.md "Investigations and explanations").
	InvestigateMax time.Duration
	// InvestigateSessionTTL is how long a finished investigation's
	// sub-agent session stays resumable; a follow-up past it starts cold
	// with the previous answer prepended (default 1 h).
	InvestigateSessionTTL time.Duration
	// AnnounceWait is how long a block waits for the brain's announce turn
	// before the daemon's own code sentence (the parsed prompt) is spoken
	// (default 2 s, brain.announce_wait_s). Past it the brain's line is
	// dropped unless it says the shepherd must act (docs/roles.md
	// "Pushes"). A finish does not use it: a finish is not urgent and
	// waits for the turn's own budget. Negative waits for the turn's own
	// timeout on a block too.
	AnnounceWait time.Duration
	// ReplyWait is how long a to_supervisor call waits for the brain's
	// reply before it returns a silent pending result and the reply is
	// pushed when it lands (default 2 s, brain.reply_wait_s; utterance.go).
	// The voice already said "on it" as it called the tool, so a longer
	// wait is only dead air. Negative waits for the whole turn: the reply
	// is always the tool result (--voice=off, brain-test).
	ReplyWait time.Duration
	// RepeatWindow is how long an unchanged roster sentence counts as
	// "already reported" (default 60 s, supervisor.repeat_window_s,
	// docs/history/issues-2026-09-18.md "Smaller items"): a status question
	// inside it gets a context note the brain can use to say "no change
	// since I last checked" instead of repeating the roster; with no
	// brain, the code fallback sentence itself is prefixed that way.
	RepeatWindow time.Duration
	// AuthoriseWindow is how recent a shepherd input must be to authorise
	// answering a worker's approval prompt (default 30 s,
	// supervisor.authorise_window_s, docs/roles.md "The one rail";
	// inputs.go).
	AuthoriseWindow time.Duration
	// Brain, when set, is the one model that decides and phrases
	// (docs/roles.md); without it the daemon speaks code sentences and
	// sends nothing. New attaches the Supervisor as its Host.
	Brain *brain.Brain
	// Now overrides the clock (tests).
	Now func() time.Time
	// WorkerLog reads the workers' own session transcripts for the work
	// journal (journal.go); default the real ~/.claude and ~/.codex.
	WorkerLog *workerlog.Reader

	chattiness Chattiness // parsed Chattiness

	// inputGrace is how long a to_supervisor call with no fresh shepherd
	// input waits for one (default inputGraceDefault; negative does not
	// wait). Not a knob: only tests change it (inputs.go).
	inputGrace time.Duration
	// relayAfter is how long a shepherd utterance may go unanswered
	// before the daemon relays it itself (default relayUnansweredAfter;
	// negative never relays). Not a knob: only tests change it
	// (relay.go).
	relayAfter time.Duration
}

func (c Config) withDefaults() Config {
	if c.PendingTTL == 0 {
		c.PendingTTL = 120 * time.Second
	}
	if c.ExpiryTick == 0 {
		c.ExpiryTick = time.Second
	}
	if c.ReadLines == 0 {
		c.ReadLines = 200
	}
	if c.Chattiness == "" {
		c.Chattiness = string(Normal)
	}
	if c.RecentWindow == 0 {
		c.RecentWindow = 30 * time.Minute
	}
	if c.PromiseTTL == 0 {
		c.PromiseTTL = 2 * time.Hour
	}
	if c.CancelWindow == 0 {
		c.CancelWindow = 30 * time.Second
	}
	if c.RepromptPollInterval == 0 {
		c.RepromptPollInterval = defaultRepromptPollInterval
	}
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 30 * time.Second
	}
	if c.AnnounceWait == 0 {
		c.AnnounceWait = 2 * time.Second
	}
	if c.ReplyWait == 0 {
		c.ReplyWait = 2 * time.Second
	}
	if c.RepeatWindow == 0 {
		c.RepeatWindow = 60 * time.Second
	}
	if c.AuthoriseWindow == 0 {
		c.AuthoriseWindow = 30 * time.Second
	}
	if c.inputGrace == 0 {
		c.inputGrace = inputGraceDefault
	}
	if c.relayAfter == 0 {
		c.relayAfter = relayUnansweredAfter
	}
	if c.InvestigateMax == 0 {
		c.InvestigateMax = 600 * time.Second
	}
	if c.InvestigateSessionTTL == 0 {
		c.InvestigateSessionTTL = time.Hour
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.WorkerLog == nil {
		c.WorkerLog = workerlog.New(workerlog.Options{})
	}
	return c
}

// Supervisor is safe for concurrent use: tool handlers, HandleTransition,
// OnVoiceEvent and Run may all run at once. One mutex orders every change
// to the pending slot, the briefing cache and the expired-question memory;
// model calls and pane reads happen outside it, herdr sends inside it so
// an answer, a stop and an instruction cannot interleave.
type Supervisor struct {
	store   *state.Store
	herdr   Herdr
	querier Querier
	// investigator is the querier's background half, when it has one
	// (investigate.go); nil refuses investigations.
	investigator Investigator
	pusher       Pusher
	log          logging.Logger
	cfg          Config
	brain        *brain.Brain // nil: code sentences only

	mu            sync.Mutex
	briefing      string               // cached sentence; "" means recompute
	briefingPanes []string             // panes the cached sentence names
	briefingAt    time.Time            // when the cached sentence was composed
	expired       *state.PendingAction // last expired answer_prompt (scenario 8)
	seq           uint64               // pending action ids
	interrupted   map[string]time.Time // pane -> when cancel sent Ctrl-C (suppresses the next "finished")
	lastSubject   string               // pane most recently spoken about (last push subject or tool target)
	mentioned     map[string]time.Time // pane -> when a spoken briefing last named it
	lastPrompt    map[string]string    // pane -> signature of the last answer_prompt built for it (reprompt.go)
	utterance     *inflightUtterance   // the to_supervisor call in flight (stop.go phases 1 and 2)
	// eventIDs is each pane's latest worker event id (links.go): the
	// announce turn and pushes about that event carry it, so the Context
	// tab links them to the brain's worker_event message.
	eventIDs map[string]string
	// investigating is the one running background job per pane
	// (investigate.go).
	investigating map[string]*liveInvestigation

	// startedAt is when this run's Supervisor was built: openPromises
	// created before it are from an earlier run (promises.go,
	// announceStartupPromises).
	startedAt time.Time
	// startupAnnounced guards announceStartupPromises: it runs once, the
	// first time SetVoiceConnected(true) fires.
	startupAnnounced bool
	// lastBriefingText / lastBriefingAt are the roster sentence last
	// delivered to the shepherd in a KindUtterance request, and when
	// (briefing.go's briefingRepeat, supervisor.repeat_window_s); reset
	// on any worker transition, pending change, promise change or
	// investigation event (invalidateBriefing and its callers).
	lastBriefingText string
	lastBriefingAt   time.Time

	// inputs is what the shepherd actually said, typed or tapped, newest
	// last (inputs.go): the one rail reads these, never the voice model's
	// tool argument.
	inputs []*shepherdInput
	// inputSeq numbers every input (each voice fragment bumps it);
	// relayedSeq is its value when the last to_supervisor call was
	// accepted and pushSeq its value when the last spoken push went out,
	// so a call with no input newer than both is the model's own.
	inputSeq   uint64
	relayedSeq uint64
	pushSeq    uint64
	// acceptedSeq counts the shepherd's requests the daemon took up: every
	// to_supervisor call claimInput accepted, and every tap. A late
	// utterance reply is stale once it moved on (utterance.go). Unlike
	// relayedSeq it never moves for transcript fragments that trail the
	// same relay.
	acceptedSeq uint64
	// replies tracks the detached utterance turns (utterance.go), so a test
	// can wait for a late reply to land.
	replies sync.WaitGroup
	// inputWake is closed and replaced whenever an input arrives, for a
	// call waiting out inputGrace.
	inputWake chan struct{}
	// relay is the unanswered-utterance watch (relay.go).
	relay relayWatch
	// promptSeenAt is when each pane's current prompt (lastPrompt's
	// signature) was first recorded, so a revived pending action keeps
	// the time the prompt really appeared (inputs.go).
	promptSeenAt map[string]time.Time

	// conversationStartedAt is when the current conversation began: the
	// last reset, or startedAt (reset.go, GET /api/state).
	conversationStartedAt time.Time
	// resetConfirm is the "start a new conversation?" question waiting
	// for its confirm phrase (reset.go), nil when none is open.
	resetConfirm *resetConfirmState

	// journal is the work journal's own bookkeeping (journal.go).
	journal journalState
}

// interruptGrace is how long after a user cancel a pane's working -> done
// is treated as "interrupted", not "finished".
const interruptGrace = 15 * time.Second

// New wires a Supervisor. store and h are required; brain (Config.Brain),
// querier, pusher and log may be nil (nil brain = code sentences only,
// nil pusher = no voice session, nil log discards).
func New(store *state.Store, h Herdr, querier Querier, pusher Pusher, log logging.Logger, cfg Config) (*Supervisor, error) {
	if store == nil || h == nil {
		return nil, errors.New("supervisor: store and herdr client are required")
	}
	if log == nil {
		log = logging.NopLogger{}
	}
	cfg = cfg.withDefaults()
	level, err := ParseChattiness(cfg.Chattiness)
	if err != nil {
		return nil, err
	}
	cfg.chattiness = level
	s := &Supervisor{store: store, herdr: h, querier: querier, pusher: pusher, log: log, cfg: cfg, brain: cfg.Brain, startedAt: cfg.Now(), conversationStartedAt: cfg.Now(),
		interrupted: map[string]time.Time{}, mentioned: map[string]time.Time{}, lastPrompt: map[string]string{}, promptSeenAt: map[string]time.Time{}, inputWake: make(chan struct{}), investigating: map[string]*liveInvestigation{}}
	// A querier that can keep a sub-agent session backs the investigate
	// tool as well (investigate.go); one that cannot still answers
	// run_cli.
	if inv, ok := querier.(Investigator); ok {
		s.investigator = inv
	}
	s.restoreInvestigations()
	if s.brain != nil {
		s.brain.SetHost(brainHost{s})
		s.restorePromises()
	}
	return s, nil
}

// Brain returns the attached brain, or nil.
func (s *Supervisor) Brain() *brain.Brain { return s.brain }

// Run drives pending-action and promise expiry, and the reprompt
// reconciler, until ctx ends. It is optional for tests that call
// ExpirePending / ExpirePromises / reconcileReprompts directly.
func (s *Supervisor) Run(ctx context.Context) {
	expiry := time.NewTicker(s.cfg.ExpiryTick)
	defer expiry.Stop()
	reconcile := time.NewTicker(s.cfg.SnapshotInterval)
	defer reconcile.Stop()
	// The sub-agent of a running investigation dies with the daemon, so
	// nothing is left claiming an agent's one live slot.
	defer s.cancelInvestigations("shutdown")
	// Likewise a shepherd turn still running past its tool call: its
	// reply would have nobody to reach (utterance.go).
	defer s.abandonUtterance()
	// And no unanswered utterance is relayed into a daemon going down.
	defer s.stopRelay()
	// Once at startup, the roster is already loaded (state.Open ran
	// before New): catches a pane whose prompt changed while the daemon
	// was down, which no post-answer poll ever ran for.
	s.reconcileReprompts(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			s.ExpirePending()
			s.ExpirePromises()
		case <-reconcile.C:
			s.reconcileReprompts(ctx)
		}
	}
}

// SetVoiceConnected records whether a voice session is attached
// (session.voice_connected, /api/state). Pushes are not gated on it —
// pushTagged hands them to the session regardless, which queues them
// while reconnecting (audit B3). The first time it turns on — a live
// connect event, or at once for --voice=off (docs/history/roles-status.md) —
// it also runs announceStartupPromises, once per daemon run.
func (s *Supervisor) SetVoiceConnected(connected bool) {
	_ = s.store.UpdateSession(func(ss *state.Session) { ss.VoiceConnected = connected })
	if !connected {
		return
	}
	s.mu.Lock()
	already := s.startupAnnounced
	s.startupAnnounced = true
	s.mu.Unlock()
	if !already {
		s.announceStartupPromises()
	}
}

// OnVoiceEvent feeds voice session events: connection status maintains
// session.voice_connected, a resumed connection gets the briefing pushed
// silently so the model re-learns the situation (plan §6), the
// transcripts (the CEO's verbatim words, what the assistant said) go into
// the shepherd-input ledger (inputs.go) and the brain's transcript, and
// tool calls, spoken pushes and the voice's own speech drive the
// unanswered-utterance watch (relay.go).
func (s *Supervisor) OnVoiceEvent(ev voice.Event) {
	switch e := ev.(type) {
	case voice.EventStatus:
		s.SetVoiceConnected(e.Status == voice.StatusConnected)
	case voice.EventResumed:
		s.pushBriefing()
	case voice.EventTranscript:
		if e.Dir == voice.DirIn {
			s.heard(e.Text, e.Final)
		} else {
			s.voiceSpoke(e.Text)
		}
		if s.brain != nil {
			if e.Dir == voice.DirIn {
				s.brain.Heard(e.Text, e.Final)
			} else {
				s.brain.Said(e.Text, e.Final)
			}
		}
	case voice.EventTurnComplete:
		s.closeVoiceInput()
		s.pushSpoken(false)
		if s.brain != nil {
			s.brain.TurnComplete()
		}
	case voice.EventToolCall:
		// The voice took up whatever was said: no daemon relay for it
		// (relay.go).
		s.toolCalled()
	case voice.EventPush:
		if e.Urgency != voice.UrgencySilent {
			s.pushSpoken(true)
		}
	}
}

// push delivers text to the voice session. A spoken push about a pane
// makes that pane the current subject (what "it" means in the next
// ask).
func (s *Supervisor) push(paneID, text string, u voice.Urgency) {
	s.pushTagged(paneID, text, u, nil)
}

// pushTagged is push with tags for the voice.push event (answers_job).
// It hands the push to the voice layer even while a session is
// reconnecting: the session's own queue (Gemini's pushQ, GPT-Live's
// standby queue) holds it and sends it once the connection is back, so a
// kept promise or a block line is never lost to a reconnect (audit B3).
// Only a session-less daemon (--voice=off, or no session at all) skips
// the push outright, logged as supervisor.push_skipped.
func (s *Supervisor) pushTagged(paneID, text string, u voice.Urgency, tags map[string]any) {
	if text == "" {
		return
	}
	if paneID != "" && u != voice.UrgencySilent {
		s.setSubject(paneID)
	}
	if row, ok := s.store.Get(paneID); ok && row.Alias != "" && tags["agent"] == nil {
		// The agent a push is about, on its voice.push event: the
		// conversation history (GET /api/conversation) labels it.
		tagged := map[string]any{"agent": row.Alias}
		for k, v := range tags {
			tagged[k] = v
		}
		tags = tagged
	}
	tags = s.withPushID(tags)
	if s.pusher == nil {
		payload := map[string]any{"text": text, "urgency": u.String()}
		for k, v := range tags {
			payload[k] = v
		}
		s.log.Log(logSource, "supervisor.push_skipped", paneID, payload)
		return
	}
	if err := s.pushTo(text, u, tags); err != nil {
		s.log.Log(logSource, "supervisor.push_error", paneID, map[string]any{"text": text, "urgency": u.String(), "error": err.Error()})
		return
	}
	if u != voice.UrgencySilent {
		// A silent push is context the voice never speaks, so it cannot
		// prompt a relay of its own.
		s.markPushed()
		if s.brain != nil {
			s.brain.PushedLinked(s.aliasOf(paneID), text, u.String(), pushLink(tags))
		}
	}
}

// pushTo hands the line to the pusher, with the tags when it takes them.
func (s *Supervisor) pushTo(text string, u voice.Urgency, tags map[string]any) error {
	if len(tags) > 0 {
		if tp, ok := s.pusher.(TagPusher); ok {
			return tp.PushTagged(text, u, tags)
		}
	}
	return s.pusher.Push(text, u)
}

// pushIf pushes text at idle urgency when the current chattiness level
// speaks pushes of kind k (every policy-gated push is a low-priority one;
// questions and exits bypass it); otherwise it logs supervisor.push_quiet
// so the decision stays visible in the event log.
func (s *Supervisor) pushIf(k pushKind, paneID, text string) {
	s.pushIfTagged(k, paneID, text, nil)
}

// pushIfTagged is pushIf with tags for the voice.push event.
func (s *Supervisor) pushIfTagged(k pushKind, paneID, text string, tags map[string]any) {
	level := s.chattiness()
	if !level.allows(k) {
		s.log.Log(logSource, "supervisor.push_quiet", paneID, map[string]any{"text": text, "chattiness": string(level)})
		return
	}
	s.pushTagged(paneID, text, voice.UrgencyIdle, tags)
}

// pushBriefing pushes the current briefing as silent context.
func (s *Supervisor) pushBriefing() {
	text, _ := s.briefingText()
	s.push("", text, voice.UrgencySilent)
}

// setSubject records the pane the conversation is about.
func (s *Supervisor) setSubject(paneID string) {
	s.mu.Lock()
	s.lastSubject = paneID
	s.mu.Unlock()
}

func (s *Supervisor) nextID(prefix string) string {
	s.seq++
	return prefix + "-" + strconv.FormatInt(s.cfg.Now().Unix(), 36) + "-" + strconv.FormatUint(s.seq, 10)
}
