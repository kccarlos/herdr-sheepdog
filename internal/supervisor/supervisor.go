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
	// AnnounceWait is how long a block or a finish waits for the brain's
	// announce turn before the daemon's own code sentence is spoken
	// (default 2 s). Past it the brain's line is dropped, never spoken
	// after the sentence. Negative waits for the turn's own timeout.
	AnnounceWait time.Duration
	// Brain, when set, is the one model that decides and phrases
	// (docs/roles.md); without it the daemon speaks code sentences and
	// sends nothing. New attaches the Supervisor as its Host.
	Brain *brain.Brain
	// Now overrides the clock (tests).
	Now func() time.Time

	chattiness Chattiness // parsed Chattiness
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
	if c.InvestigateMax == 0 {
		c.InvestigateMax = 600 * time.Second
	}
	if c.InvestigateSessionTTL == 0 {
		c.InvestigateSessionTTL = time.Hour
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Supervisor is safe for concurrent use: tool handlers, HandleTransition,
// OnVoiceEvent and Run may all run at once. One mutex orders every change
// to the pending slot, the briefing cache and the expired-question memory;
// model calls and pane reads happen outside it, herdr sends inside it so
// confirm/cancel/propose cannot interleave.
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
	// investigating is the one running background job per pane
	// (investigate.go).
	investigating map[string]*liveInvestigation
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
	s := &Supervisor{store: store, herdr: h, querier: querier, pusher: pusher, log: log, cfg: cfg, brain: cfg.Brain,
		interrupted: map[string]time.Time{}, mentioned: map[string]time.Time{}, lastPrompt: map[string]string{}, investigating: map[string]*liveInvestigation{}}
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

// SetVoiceConnected records whether a voice session is attached; pushes
// are skipped (and logged as supervisor.push_skipped) while it is not.
func (s *Supervisor) SetVoiceConnected(connected bool) {
	_ = s.store.UpdateSession(func(ss *state.Session) { ss.VoiceConnected = connected })
}

// OnVoiceEvent feeds voice session events: connection status maintains
// session.voice_connected, a resumed connection gets the briefing pushed
// silently so the model re-learns the situation (plan §6), and the
// transcripts (the CEO's verbatim words, what the assistant said) go into
// the brain's transcript.
func (s *Supervisor) OnVoiceEvent(ev voice.Event) {
	switch e := ev.(type) {
	case voice.EventStatus:
		s.SetVoiceConnected(e.Status == voice.StatusConnected)
	case voice.EventResumed:
		s.pushBriefing()
	case voice.EventTranscript:
		if s.brain != nil {
			if e.Dir == voice.DirIn {
				s.brain.Heard(e.Text, e.Final)
			} else {
				s.brain.Said(e.Text, e.Final)
			}
		}
	case voice.EventTurnComplete:
		if s.brain != nil {
			s.brain.TurnComplete()
		}
	}
}

func (s *Supervisor) voiceConnected() bool {
	return s.store.Snapshot().Session.VoiceConnected
}

// push delivers text to the voice session when one is connected. A
// spoken push about a pane makes that pane the current subject (what
// "it" means in the next ask).
func (s *Supervisor) push(paneID, text string, u voice.Urgency) {
	s.pushTagged(paneID, text, u, nil)
}

// pushTagged is push with tags for the voice.push event (answers_job).
func (s *Supervisor) pushTagged(paneID, text string, u voice.Urgency, tags map[string]any) {
	if text == "" {
		return
	}
	if paneID != "" && u != voice.UrgencySilent {
		s.setSubject(paneID)
	}
	if s.pusher == nil || !s.voiceConnected() {
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
	if s.brain != nil && u != voice.UrgencySilent {
		s.brain.Pushed(s.aliasOf(paneID), text, u.String())
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
	level := s.chattiness()
	if !level.allows(k) {
		s.log.Log(logSource, "supervisor.push_quiet", paneID, map[string]any{"text": text, "chattiness": string(level)})
		return
	}
	s.push(paneID, text, voice.UrgencyIdle)
}

// pushBriefing pushes the current briefing as silent context and records
// it in the session.
func (s *Supervisor) pushBriefing() {
	text, _ := s.briefingText()
	now := s.cfg.Now()
	_ = s.store.UpdateSession(func(ss *state.Session) { ss.LastBriefing, ss.LastBriefingAt = text, &now })
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
