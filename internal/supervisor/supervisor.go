package supervisor

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
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

// Querier is the read-only CLI query tool of plan §3.5 (WP4). workspace
// is the agent's working directory; the result is raw text that goes
// through SpokenAnswer. nil disables the deep path of ask.
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
	// BriefingInterval is how often Run re-pushes the briefing silently as
	// a fallback for context compression (default 10 min; < 0 disables).
	BriefingInterval time.Duration
	// ReadLines is how many recent lines a pane read asks for (default 80).
	ReadLines int
	// DataDir is where notes.jsonl is appended; empty keeps notes in the
	// state table only.
	DataDir string
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.PendingTTL == 0 {
		c.PendingTTL = 120 * time.Second
	}
	if c.ExpiryTick == 0 {
		c.ExpiryTick = time.Second
	}
	if c.BriefingInterval == 0 {
		c.BriefingInterval = 10 * time.Minute
	}
	if c.ReadLines == 0 {
		c.ReadLines = 80
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
	llm     llm.Templates
	querier Querier
	pusher  Pusher
	log     logging.Logger
	cfg     Config

	mu       sync.Mutex
	briefing string               // cached sentence; "" means recompute
	expired  *state.PendingAction // last expired answer_prompt (scenario 8)
	seq      uint64               // pending action ids
}

// New wires a Supervisor. store, h and templates are required; querier,
// pusher and log may be nil (nil pusher = no voice session; nil log
// discards).
func New(store *state.Store, h Herdr, templates llm.Templates, querier Querier, pusher Pusher, log logging.Logger, cfg Config) (*Supervisor, error) {
	if store == nil || h == nil || templates == nil {
		return nil, errors.New("supervisor: store, herdr client and llm templates are required")
	}
	if log == nil {
		log = logging.NopLogger{}
	}
	return &Supervisor{store: store, herdr: h, llm: templates, querier: querier, pusher: pusher, log: log, cfg: cfg.withDefaults()}, nil
}

// Run drives pending-action expiry and the periodic silent briefing until
// ctx ends. It is optional for tests that call ExpirePending directly.
func (s *Supervisor) Run(ctx context.Context) {
	expiry := time.NewTicker(s.cfg.ExpiryTick)
	defer expiry.Stop()
	var briefing <-chan time.Time
	if s.cfg.BriefingInterval > 0 {
		t := time.NewTicker(s.cfg.BriefingInterval)
		defer t.Stop()
		briefing = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			s.ExpirePending()
		case <-briefing:
			s.pushBriefing()
		}
	}
}

// SetVoiceConnected records whether a voice session is attached; pushes
// are skipped (and logged as supervisor.push_skipped) while it is not.
func (s *Supervisor) SetVoiceConnected(connected bool) {
	_ = s.store.UpdateSession(func(ss *state.Session) { ss.VoiceConnected = connected })
}

// OnVoiceEvent feeds voice session events: connection status maintains
// session.voice_connected, and a resumed connection gets the briefing
// pushed silently so the model re-learns the situation (plan §6).
func (s *Supervisor) OnVoiceEvent(ev voice.Event) {
	switch e := ev.(type) {
	case voice.EventStatus:
		s.SetVoiceConnected(e.Status == voice.StatusConnected)
	case voice.EventResumed:
		s.pushBriefing()
	}
}

func (s *Supervisor) voiceConnected() bool {
	return s.store.Snapshot().Session.VoiceConnected
}

// push delivers text to the voice session when one is connected.
func (s *Supervisor) push(paneID, text string, u voice.Urgency) {
	if text == "" {
		return
	}
	if s.pusher == nil || !s.voiceConnected() {
		s.log.Log(logSource, "supervisor.push_skipped", paneID, map[string]any{"text": text, "urgency": u.String()})
		return
	}
	if err := s.pusher.Push(text, u); err != nil {
		s.log.Log(logSource, "supervisor.push_error", paneID, map[string]any{"text": text, "urgency": u.String(), "error": err.Error()})
	}
}

// pushBriefing pushes the current briefing as silent context and records
// it in the session.
func (s *Supervisor) pushBriefing() {
	text := s.briefingText()
	now := s.cfg.Now()
	_ = s.store.UpdateSession(func(ss *state.Session) { ss.LastBriefing, ss.LastBriefingAt = text, &now })
	s.push("", text, voice.UrgencySilent)
}

func (s *Supervisor) nextID(prefix string) string {
	s.seq++
	return prefix + "-" + strconv.FormatInt(s.cfg.Now().Unix(), 36) + "-" + strconv.FormatUint(s.seq, 10)
}
