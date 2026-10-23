package brain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// logSource is the source field of every brain.* event.
const logSource = "brain"

// Defaults for Config zero values.
const (
	DefaultModel    = "gemini-3.5-flash"
	DefaultMaxSteps = 6
	// DefaultUtteranceTimeout bounds a user utterance turn including its
	// tools (it may turn out to be an instruction) and an investigation
	// delivery turn. 25 s since the 2026-10-03 eval campaign: at 12 s,
	// 45/130 Vertex gemini-3.8-flash runs had a turn time out; at 25 s
	// utterance fallbacks went 44 → 0. The voice already answers
	// "pending" within ~2 s and the answer comes as a push, so a longer
	// budget only delays the slowest answers.
	DefaultUtteranceTimeout = 25 * time.Second
	// DefaultAnnounceTurnTimeout bounds an event turn (announce): on
	// expiry the supervisor speaks its code sentence instead.
	DefaultAnnounceTurnTimeout = 6 * time.Second
	// DefaultAnnounceTimeout bounds an announce turn the user is owed
	// (Request.Owed: it keeps a promise or reports on the user's own
	// request): the code sentence has already said "here's what it
	// found", so the orchestrator's line is worth waiting longer for.
	DefaultAnnounceTimeout = 15 * time.Second
	// DefaultWindowTokens and DefaultPruneAfterTurns were 24000 and 3
	// until docs/dev/iteration-2.md WP5 halved the window: prompts averaged
	// 19.8 k tokens and announce turns missed the 2 s reply window.
	DefaultWindowTokens    = 12000
	DefaultPruneAfterTurns = 2
	// DefaultCompactTimeout bounds the summary call of a compaction,
	// which runs after the reply was returned.
	DefaultCompactTimeout = 30 * time.Second
	// DefaultStepReserve is how much of the budget a tool round must leave
	// for the answer: below it the next step is answer-only (no tools).
	DefaultStepReserve = 3 * time.Second
	// DefaultFinalGrace is how far past the budget that one answer-only
	// call may run before the turn falls back.
	DefaultFinalGrace = 3 * time.Second
	// TranscriptDir is the directory under the data dir.
	TranscriptDir = "brain"
)

// Config tunes a Brain; zero values take the defaults.
type Config struct {
	// Provider is the id the Model given to New is registered under and
	// the provider of the first turn (default ProviderGemini); SetModel
	// changes it at runtime (Brain.Provider reports the current one).
	Provider string
	// Model is the initial model name passed to Model.Generate; SetModel
	// changes it at runtime (Brain.Model reports the current one).
	Model string
	// MaxSteps bounds the model calls in one turn.
	MaxSteps int
	// UtteranceTimeout bounds an utterance turn including its tools (it
	// may be an instruction) and an investigation delivery turn;
	// AnnounceTurnTimeout bounds an event turn; AnnounceTimeout bounds an announce turn the user is owed
	// (Request.Owed, or one that keeps a promise).
	UtteranceTimeout, AnnounceTurnTimeout, AnnounceTimeout time.Duration
	// WindowTokens is the estimated window size that triggers compaction.
	WindowTokens int
	// PruneAfterTurns is how many turns a tool result stays verbatim.
	PruneAfterTurns int
	// CompactTimeout bounds the compaction's model call.
	CompactTimeout time.Duration
	// StepReserve: when less of the budget remains after a tool round,
	// the next step is answer-only (tools disabled) so the turn ends with
	// an answer rather than another read.
	StepReserve time.Duration
	// FinalGrace is how far past the budget the answer-only call may run
	// (negative: none).
	FinalGrace time.Duration
	// ThinkingLevel is the model's thinking level ("low" by default;
	// "" leaves the model's default).
	ThinkingLevel string
	// DataDir is where brain/<run-id>.jsonl is written; "" keeps the
	// transcript in memory only.
	DataDir string
	// RunID names the transcript file (default: the start time).
	RunID string
	// Engine overrides the default context engine (the memory seam).
	Engine ContextEngine
	// Prompts resolves SOUL.md, RULES.md and USER.md against their
	// overrides (docs/dev/roles.md "Prompt files"); nil uses the embedded
	// defaults. Recomputed every turn, so a reload takes effect at once.
	Prompts *prompts.Loader
	// Now overrides the clock (tests).
	Now func() time.Time
	// Accounts is the daemon's set of account problems (an invalid key,
	// a used-up quota or balance), shared with the web layer that shows
	// them; nil gives the orchestrator a set of its own (Accounts).
	Accounts *AccountAlerts
	// KeyVars names the variable that holds a provider's key where it is
	// not ServiceOf's (an OpenAI-compatible entry's api_key_env: the
	// OpenAI orchestrator reads OPENAI_ORCH_API_KEY unless it reuses the
	// voice's key), so an account problem's fix line names the right one.
	KeyVars map[string]string
}

func (c Config) withDefaults() Config {
	if c.Provider == "" {
		c.Provider = ProviderGemini
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.UtteranceTimeout <= 0 {
		c.UtteranceTimeout = DefaultUtteranceTimeout
	}
	if c.AnnounceTurnTimeout <= 0 {
		c.AnnounceTurnTimeout = DefaultAnnounceTurnTimeout
	}
	if c.AnnounceTimeout <= 0 {
		c.AnnounceTimeout = DefaultAnnounceTimeout
	}
	if c.WindowTokens <= 0 {
		c.WindowTokens = DefaultWindowTokens
	}
	if c.PruneAfterTurns <= 0 {
		c.PruneAfterTurns = DefaultPruneAfterTurns
	}
	if c.CompactTimeout <= 0 {
		c.CompactTimeout = DefaultCompactTimeout
	}
	if c.StepReserve <= 0 {
		c.StepReserve = DefaultStepReserve
	}
	if c.FinalGrace < 0 {
		c.FinalGrace = 0
	} else if c.FinalGrace == 0 {
		c.FinalGrace = DefaultFinalGrace
	}
	if c.ThinkingLevel == "" {
		c.ThinkingLevel = "low"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.RunID == "" {
		c.RunID = c.Now().UTC().Format("20060102T150405Z")
	}
	return c
}

// Brain is the orchestrator (see the package doc). Safe for concurrent use: turns
// are serialised by the turn lock; ingestion may happen at any time.
type Brain struct {
	cfg        Config
	log        logging.Logger
	trace      *traceLog // log's turn-id decorator (trace.go)
	transcript *Transcript
	// requests is the Context tab's log of every model call's request.
	requests  *ctxcap.BrainLog
	engine    ContextEngine
	tools     []*genai.Tool // the full surface (user turns)
	readTools []*genai.Tool // the read-only surface (event turns)

	lock  turnLock
	after afterWork // after-turn work in flight
	// inFlight counts Turn calls in flight; Close waits for them after
	// setting closed (under mu), so a turn never runs into a closed
	// transcript and no Add on after races Close's Wait.
	inFlight sync.WaitGroup
	// activeTurns counts the same turns for Busy.
	activeTurns atomic.Int32
	closed      bool
	ctx         context.Context // ends on Close (bounds after-turn work)
	stop        context.CancelFunc

	mu     sync.Mutex
	host   Host
	models map[string]Model // the registered providers (New, AddProvider)
	// curProvider / curModel are the provider and model of the next turn
	// (Provider, Model / SetModel).
	curProvider, curModel string
	// curRunID names the transcript segment currently open: cfg.RunID
	// until Reset opens a fresh one (TranscriptPath, brain.reset).
	curRunID string
	// resetSeq counts conversation resets, for the fresh segment's name.
	resetSeq     int
	turns        int
	toolSeq      int
	inBuf        []string // voice transcript fragments not yet final
	outBuf       []string
	promptLogged bool
	userVars     map[string]string // USER.md's {chattiness}/{tz}; SetUserVars
	// lastCompaction is the most recent successful compaction (zero
	// value when none), for the Supervisor tab.
	lastCompaction CompactionInfo
	// compactCancel / compactDone are the compaction in flight (nil
	// when none): one at a time, off the turn lock (afterTurn).
	compactCancel context.CancelFunc
	compactDone   chan struct{}
	// lastModel is the model this run last selected on each provider
	// (New, SetModel): the model a failover to that provider runs.
	lastModel map[string]string
	// failover is the streak of failovers away from the selected
	// provider; notify tells the user about it (SetNotifier).
	failover failoverState
	notify   func(n Notice)
	// down are the providers that refused the daemon's credentials,
	// left out of failover until their time (markDown).
	down map[string]providerDown
	// accounts are the account problems in force (account.go); the
	// orchestrator's are told once each (tellAccount).
	accounts *AccountAlerts

	// catalogs are the cached model lists, one per provider (catalog.go).
	catalogs catalogCaches
}

// CompactionInfo describes one compaction for Brain.LastCompaction.
type CompactionInfo struct {
	BeforeTokens int       `json:"before_tokens"`
	AfterTokens  int       `json:"after_tokens"`
	Dropped      int       `json:"dropped"`
	SummarySeq   int       `json:"summary_seq"`
	At           time.Time `json:"at"`
	// CoversTo is the time of the last message the summary replaced:
	// up to then the window holds only the summary.
	CoversTo time.Time `json:"covers_to"`
}

// New builds a Brain over model, registered as provider cfg.Provider;
// AddProvider registers more. log may be nil. The Host is attached by
// the supervisor through SetHost.
func New(cfg Config, model Model, log logging.Logger) (*Brain, error) {
	if model == nil {
		return nil, errors.New("brain: model required")
	}
	if log == nil {
		log = logging.NopLogger{}
	}
	cfg = cfg.withDefaults()
	// Every event a turn logs gets the turn's ids (trace.go).
	trace := newTraceLog(log)
	log = trace
	b := &Brain{cfg: cfg, log: log, trace: trace, models: map[string]Model{cfg.Provider: model}, curProvider: cfg.Provider, curModel: cfg.Model, lastModel: map[string]string{cfg.Provider: cfg.Model}, curRunID: cfg.RunID, tools: toolDeclarations(false), readTools: toolDeclarations(true)}
	b.accounts = cfg.Accounts
	if b.accounts == nil {
		b.accounts = NewAccountAlerts()
	}
	b.ctx, b.stop = context.WithCancel(context.Background())
	path := ""
	if cfg.DataDir != "" {
		path = filepath.Join(cfg.DataDir, TranscriptDir, cfg.RunID+".jsonl")
	}
	var err error
	b.transcript, err = OpenTranscript(path, func(err error) {
		log.Log(logSource, "brain.transcript_error", "", map[string]any{"path": path, "error": err.Error()})
	})
	if err != nil {
		return nil, err
	}
	// brain.transcript is the lightweight "something was ingested" event
	// the Supervisor tab fetches incrementally on (the message itself is
	// served by the transcript API, not carried in the event).
	b.transcript.OnAppend(func(m Message) {
		log.Log(logSource, "brain.transcript", "", map[string]any{"seq": m.Seq, "kind": string(m.Kind), "turn": m.Turn})
	})
	reqDir := ""
	if cfg.DataDir != "" {
		reqDir = filepath.Join(cfg.DataDir, TranscriptDir)
	}
	b.requests, err = ctxcap.OpenBrainLog(reqDir, cfg.RunID, func(s ctxcap.BrainStep) {
		// The Context tab follows live on this (the step itself is served
		// by GET /api/context/brain, not carried in the event).
		log.Log(logSource, "context.step", "", map[string]any{"pane": "brain", "step": s.Step, "turn": s.Turn, "turn_step": s.TurnStep, "kind": s.Kind, "tokens": s.Tokens})
	})
	if err != nil {
		_ = b.transcript.Close()
		return nil, err
	}
	b.engine = cfg.Engine
	if b.engine == nil {
		b.engine = NewDefaultEngine(cfg.PruneAfterTurns, b.summarize, func(m Message) Message { return b.transcript.Append(m) })
	}
	return b, nil
}

// SetHost attaches the supervisor's Host; turns fail with ErrNoHost
// before it.
func (b *Brain) SetHost(h Host) {
	b.mu.Lock()
	b.host = h
	b.mu.Unlock()
}

// SetUserVars sets USER.md's {chattiness}/{tz} placeholders; the orchestrator has
// no view of the supervisor's runtime settings on its own. {aliases} is
// always derived fresh from the roster, so it need not be included. Safe
// to call at any time, including before SetHost.
func (b *Brain) SetUserVars(vars map[string]string) {
	b.mu.Lock()
	b.userVars = vars
	b.mu.Unlock()
}

// Transcript returns the run's transcript.
func (b *Brain) Transcript() *Transcript { return b.transcript }

// AddProvider registers model under the provider id, so SetModel can
// switch to it; an id already registered is replaced. Safe at any time
// (a turn in flight keeps the Model it started with).
func (b *Brain) AddProvider(id string, model Model) error {
	id = strings.ToLower(strings.TrimSpace(id))
	switch {
	case id == "":
		return errors.New("brain: provider id required")
	case model == nil:
		return fmt.Errorf("brain: provider %q: model required", id)
	}
	b.mu.Lock()
	b.models[id] = model
	b.mu.Unlock()
	return nil
}

// Providers lists the registered provider ids, sorted.
func (b *Brain) Providers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.providersLocked()
}

// Provider returns the provider id the next turn will use
// (Config.Provider until SetModel changes it).
func (b *Brain) Provider() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.curProvider
}

// Model returns the model name the next turn will use (Config.Model
// until SetModel changes it). A turn in flight keeps the provider and
// name it started with for every step.
func (b *Brain) Model() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.curModel
}

// current snapshots the next turn's provider, model name and Model
// implementation.
func (b *Brain) current() (provider, model string, impl Model) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.curProvider, b.curModel, b.models[b.curProvider]
}

// modelOf returns the Model registered under provider ("" for the
// current one), ok false when there is none.
func (b *Brain) modelOf(provider string) (id string, m Model, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if provider == "" {
		provider = b.curProvider
	}
	m, ok = b.models[provider]
	return provider, m, ok
}

// modelNameRE is the shape of a model id the switch accepts.
var modelNameRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// maxModelName bounds a model id.
const maxModelName = 128

// SetModel switches the provider (a registered id; "" keeps the current
// one) and model of the next turn ("models/" prefix stripped; empty or
// odd names rejected) without waiting for a running turn: that turn
// finishes on the provider and model it started with. A change is
// logged as brain.model_changed {from_provider, from, provider, to, via}
// and marked in the transcript (KindSystem, "model switched from
// gemini/X to deepseek/Y") so the Supervisor tab shows where it flipped;
// the same pair again is a no-op. Persisting the choice is the caller's
// business (the daemon keeps it in the state file).
func (b *Brain) SetModel(provider, name, via string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.TrimPrefix(strings.TrimSpace(name), "models/")
	switch {
	case name == "":
		return errors.New("brain: model name required")
	case len(name) > maxModelName:
		return fmt.Errorf("brain: model name longer than %d characters", maxModelName)
	case !modelNameRE.MatchString(name):
		return fmt.Errorf("brain: model name %q: letters, digits, '.', '_', '-' and '/' only", name)
	}
	b.mu.Lock()
	fromProvider, from := b.curProvider, b.curModel
	if provider == "" {
		provider = fromProvider
	}
	if _, ok := b.models[provider]; !ok {
		b.mu.Unlock()
		return fmt.Errorf("%w: %q (registered: %s)", ErrUnknownProvider, provider, strings.Join(b.providersLocked(), ", "))
	}
	if fromProvider == provider && from == name {
		b.mu.Unlock()
		return nil
	}
	b.curProvider, b.curModel = provider, name
	b.lastModel[provider] = name
	// Selecting a provider is the way to retry one marked down (the
	// account was fixed): it is tried again from the next turn.
	delete(b.down, provider)
	b.mu.Unlock()
	b.accounts.Clear(RoleOrchestrator, provider)
	b.log.Log(logSource, "brain.model_changed", "", map[string]any{"from_provider": fromProvider, "from": from, "provider": provider, "to": name, "via": via})
	b.ingest(Message{Role: RoleSystem, Kind: KindSystem, Content: fmt.Sprintf("model switched from %s/%s to %s/%s", fromProvider, from, provider, name)}, 0)
	// Caches are per model: write the new one's (a no-op unless it is a
	// CacheWarmer).
	b.WarmCache()
	return nil
}

// providersLocked is Providers under b.mu.
func (b *Brain) providersLocked() []string {
	out := make([]string, 0, len(b.models))
	for id := range b.models {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Requests returns the log of every model call's request (the Context
// tab).
func (b *Brain) Requests() *ctxcap.BrainLog { return b.requests }

// Engine returns the context engine in use.
func (b *Brain) Engine() ContextEngine { return b.engine }

// Config returns the effective configuration (defaults applied).
func (b *Brain) Config() Config { return b.cfg }

// Queued returns how many turns wait for the turn lock, user turns and
// event turns separately.
func (b *Brain) Queued() (ceo, event int) {
	b.lock.mu.Lock()
	defer b.lock.mu.Unlock()
	return len(b.lock.ceo), len(b.lock.event)
}

// Turns returns how many turns have started.
func (b *Brain) Turns() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.turns
}

// Window reports what the model would see on the next call, in order:
// the engine's own view when it is a WindowReporter, else one derived
// from Assemble, marking a message whose content differs from the
// transcript's as pruned.
func (b *Brain) Window() []WindowEntry {
	if wr, ok := b.engine.(WindowReporter); ok {
		return wr.Window()
	}
	full := b.transcript.Messages()
	asm := b.engine.Assemble(b.cfg.WindowTokens)
	out := make([]WindowEntry, 0, len(asm.Messages))
	for _, m := range asm.Messages {
		w := WindowEntry{Seq: m.Seq, Kind: m.Kind, Turn: m.Turn, Tokens: m.Tokens}
		if m.Seq >= 1 && m.Seq <= len(full) && full[m.Seq-1].Content != m.Content {
			w.Pruned, w.Stub = true, m.Content
		}
		out = append(out, w)
	}
	return out
}

// LastCompaction returns the most recent compaction, ok false when none
// happened yet.
func (b *Brain) LastCompaction() (CompactionInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastCompaction, !b.lastCompaction.At.IsZero()
}

// TranscriptPath returns the current transcript segment's file, or ""
// when in memory: cfg.RunID's until Reset opens a fresh one.
func (b *Brain) TranscriptPath() string {
	if b.cfg.DataDir == "" {
		return ""
	}
	b.mu.Lock()
	id := b.curRunID
	b.mu.Unlock()
	return filepath.Join(b.cfg.DataDir, TranscriptDir, id+".jsonl")
}

// Close refuses new turns (ErrClosed), ends the ones in flight and
// after-turn work, waits for them and closes the transcript. The
// supervisor joins its own turns before the daemon closes the orchestrator;
// this makes a late one (past the daemon's drain) safe as well.
func (b *Brain) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.stop()
	b.inFlight.Wait()
	b.after.Wait()
	_ = b.requests.Close()
	return b.transcript.Close()
}

// Daemon ingests a line the daemon itself produced, so the transcript
// shows what code did without a model (a fast path, a stop).
func (b *Brain) Daemon(text string) {
	if text == "" {
		return
	}
	b.ingest(Message{Role: RoleUser, Kind: KindSystem, Content: text}, 0)
}

// ingest appends m to the transcript and the window.
func (b *Brain) ingest(m Message, turn int) {
	if m.TS.IsZero() {
		m.TS = b.cfg.Now()
	}
	m.Turn = turn
	m = b.transcript.Append(m)
	b.engine.Ingest(m, turn)
}

// Heard ingests a fragment of the user's speech (voice.transcript.in);
// fragments accumulate until final.
func (b *Brain) Heard(text string, final bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if text != "" {
		b.inBuf = append(b.inBuf, text)
	}
	if final {
		b.flushInLocked()
	}
}

// Echo records that heard, transcribed as the user's words, was the
// assistant's own speech picked up by the microphone (the supervisor's
// echo guard dropped it). The words must not reach a later turn as
// something the user said, so they leave the window: still pending (the
// input transcription is often never marked final), or already ingested
// as user utterances since the last request (the voice saying
// "Checking." flushes them before its tool call). The transcript on
// disk keeps what was heard, and a system note after it says it was an
// echo, so the record stays complete and honest.
func (b *Brain) Echo(heard string) {
	heard = strings.TrimSpace(heard)
	if heard == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if pending := joinFragments(b.inBuf); pending != "" && strings.Contains(heard, pending) {
		b.inBuf = nil
	}
	if r, ok := b.engine.(Retracter); ok {
		msgs := b.transcript.Messages()
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if m.Kind == KindRequest {
				break
			}
			if m.Kind != KindCEOUtterance {
				continue
			}
			if !strings.Contains(heard, strings.TrimSpace(m.Content)) {
				break
			}
			r.Retract(m.Seq)
		}
	}
	b.ingest(Message{Role: RoleUser, Kind: KindSystem, Content: fmt.Sprintf("Echo ignored: the microphone picked up the assistant's own speech and it was transcribed as %q. The user did not say it.", heard)}, 0)
}

// Said ingests a fragment of the assistant's speech (voice.transcript.out).
// The user's pending fragments are flushed first: the assistant answering
// means the user finished.
func (b *Brain) Said(text string, final bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushInLocked()
	if text != "" {
		b.outBuf = append(b.outBuf, text)
	}
	if final {
		b.flushOutLocked()
	}
}

// SaidCut records that the assistant's speech was cut off before the user
// heard all of it (voice.EventTranscript with Cut): heard is the part
// that played, full everything the cut run said, heardMs how long it
// played. Gemini generates ~4× real time, so the run's full text was
// usually ingested already (at the model's turnComplete) by the time the
// client flushes: those "Assistant said" lines — the newest ones whose
// text the run contains — leave the window (Retracter, as Brain.Echo
// does) and one line with what was heard takes their place, rendered
// "Assistant said (cut off after N s)". The transcript on disk keeps
// both, and an orchestrator turn that ran between the two saw the full text.
func (b *Brain) SaidCut(heard, full string, heardMs int) {
	full = strings.TrimSpace(full)
	b.mu.Lock()
	defer b.mu.Unlock()
	if pending := joinFragments(b.outBuf); pending != "" && strings.Contains(full, pending) {
		// The cut run's own tail, not ingested yet: the cut line
		// replaces it. Anything else pending is the next reply's
		// start (the model spoke again before the client's report
		// came back) and flushes at that reply's own end.
		b.outBuf = nil
	}
	if r, ok := b.engine.(Retracter); ok && full != "" {
		// Unlike Echo this walks past requests: an orchestrator turn may have
		// run on the full line already, but from here on the window
		// should say what the user actually heard.
		msgs := b.transcript.Messages()
		for i, looked := len(msgs)-1, 0; i >= 0 && looked < 16; i, looked = i-1, looked+1 {
			m := msgs[i]
			if m.Kind != KindAssistantSaid || m.Cut {
				continue
			}
			said := strings.TrimSpace(m.Content)
			if said == "" || !strings.Contains(full, said) {
				break
			}
			r.Retract(m.Seq)
		}
	}
	b.ingest(Message{Role: RoleUser, Kind: KindAssistantSaid, Content: strings.TrimSpace(heard), Cut: true, CutAfterMs: heardMs}, 0)
}

// TurnComplete flushes the assistant's pending fragments (the model's
// turn ended).
func (b *Brain) TurnComplete() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushInLocked()
	b.flushOutLocked()
}

func (b *Brain) flushInLocked() {
	if text := joinFragments(b.inBuf); text != "" {
		b.ingest(Message{Role: RoleUser, Kind: KindCEOUtterance, Content: text}, 0)
	}
	b.inBuf = nil
}

func (b *Brain) flushOutLocked() {
	if text := joinFragments(b.outBuf); text != "" {
		b.ingest(Message{Role: RoleUser, Kind: KindAssistantSaid, Content: text}, 0)
	}
	b.outBuf = nil
}

// joinFragments concatenates transcription fragments (they carry their
// own spacing) and trims.
func joinFragments(frags []string) string {
	return strings.TrimSpace(strings.Join(frags, ""))
}

// Pushed ingests a push the supervisor sent to the assistant.
func (b *Brain) Pushed(alias, text, urgency string) { b.PushedLinked(alias, text, urgency, "") }

// PushedLinked is Pushed with the push's link to the voice context
// ("push:<id>", the Context tab).
func (b *Brain) PushedLinked(alias, text, urgency, link string) {
	if text == "" {
		return
	}
	content := text
	if urgency != "" {
		content = fmt.Sprintf("(%s) %s", urgency, text)
	}
	b.ingest(Message{Role: RoleUser, Kind: KindPush, Content: content, Agent: alias, Link: link}, 0)
}

// WorkerEvent ingests a herdr transition.
func (b *Brain) WorkerEvent(alias, from, to string, closed bool) {
	b.WorkerEventLinked(alias, from, to, closed, "")
}

// WorkerEventLinked is WorkerEvent with the event's link ("evt:<id>",
// shared with the announce turn and push about it; the Context tab).
func (b *Brain) WorkerEventLinked(alias, from, to string, closed bool, link string) {
	if from == "" {
		from = "?"
	}
	content := fmt.Sprintf("%s: %s -> %s", aliasOr(alias), from, to)
	if closed {
		content = fmt.Sprintf("%s: pane closed", aliasOr(alias))
	}
	b.ingest(Message{Role: RoleUser, Kind: KindWorkerEvent, Content: content, Agent: alias, Link: link}, 0)
}

// PromiseEvent ingests a promise's record outside a turn: a promise
// kept, expired or cancelled by the supervisor, or an open promise
// carried over from an earlier run (the orchestrator's transcript is per run,
// the promise is not).
func (b *Brain) PromiseEvent(p PromiseInfo) {
	if p.ID == "" {
		return
	}
	cp := p
	b.ingest(Message{Role: RoleUser, Kind: KindPromise, Content: promiseLine(p), Agent: p.Agent, Promise: &cp}, 0)
}

// Noted ingests a note the user asked to be remembered.
func (b *Brain) Noted(text string) {
	if text == "" {
		return
	}
	b.ingest(Message{Role: RoleUser, Kind: KindNote, Content: text}, 0)
}

// Seed ingests earlier messages (brain-test --transcript).
func (b *Brain) Seed(msgs []Message) {
	for _, m := range msgs {
		b.ingest(m, 0)
	}
}

// Reset is the orchestrator's half of "New conversation" (docs/dev/roles.md): the
// context window is emptied — a fresh transcript segment starts (the
// on-disk file so far is left alone, just no longer appended to) and the
// compaction summary is gone — so the next turn's prompt carries none of
// what was said before. The roster, the ledger (promises, notes,
// investigations) and the pending prompt are not in the window at all
// (sections() renders them fresh from the Host every turn), so they are
// unaffected. It waits for any turn in flight to finish first (the turn
// lock) and cancels a compaction in flight (which runs off the lock),
// waiting for it to end, so nothing from the old conversation lands in
// the new segment; ctx bounds both waits. via is logged and carried nowhere else. The
// returned id also names the new transcript segment (TranscriptPath).
func (b *Brain) Reset(ctx context.Context, via string) (string, error) {
	if _, err := b.lock.acquire(ctx, true, ""); err != nil {
		return "", fmt.Errorf("brain: reset: waiting for the turn lock: %w", err)
	}
	defer b.lock.release()
	if err := b.stopCompaction(ctx); err != nil {
		return "", fmt.Errorf("brain: reset: waiting for the compaction: %w", err)
	}

	b.mu.Lock()
	b.resetSeq++
	n := b.resetSeq
	b.mu.Unlock()
	id := fmt.Sprintf("%s-reset%d", b.cfg.RunID, n)

	path := ""
	if b.cfg.DataDir != "" {
		path = filepath.Join(b.cfg.DataDir, TranscriptDir, id+".jsonl")
	}
	if err := b.transcript.Reset(path); err != nil {
		return "", err
	}
	if r, ok := b.engine.(Resettable); ok {
		r.Reset()
	}
	b.mu.Lock()
	b.curRunID = id
	b.lastCompaction = CompactionInfo{}
	b.mu.Unlock()
	b.log.Log(logSource, "brain.reset", "", map[string]any{"via": via, "reset_id": id})
	return id, nil
}

// summarize is the compaction's model call: one plain text-in/text-out
// generation, no tools, on the current provider and model (failing over
// like a turn's step, generateSummary), thinking off
// (a summary needs no chain of thought) and short: the engine caps the
// result at summaryMaxTokens (docs/dev/iteration-2.md WP5).
func (b *Brain) summarize(ctx context.Context, text string) (string, error) {
	prompt := "Summarise the following supervisor transcript in at most 10 short lines (under 300 words) for your own later reference: what the user asked and decided, what each agent was told, did, asked and answered, and anything still open. Keep aliases; drop paths, hashes and ids.\n\n" + text
	resp, err := b.generateSummary(ctx, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: prompt}}}}, &genai.GenerateContentConfig{ThinkingConfig: thinkingOff()})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(textOf(resp))
	if out == "" {
		return "", errors.New("brain: empty summary")
	}
	return out, nil
}

// Busy reports whether a turn or its after-turn work is running now: the
// daemon does not restart itself then (internal/update).
func (b *Brain) Busy() bool {
	if b == nil {
		return false
	}
	if b.activeTurns.Load() > 0 {
		return true
	}
	b.after.mu.Lock()
	defer b.after.mu.Unlock()
	return b.after.n > 0
}

// afterWork counts after-turn work in flight. Unlike sync.WaitGroup, Add
// may run concurrently with Wait at a zero counter (Turn finishing while
// Quiesce is called), so the counter lives under a mutex with a cond.
type afterWork struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
}

func (a *afterWork) Add(d int) {
	a.mu.Lock()
	a.n += d
	if a.n <= 0 && a.cond != nil {
		a.cond.Broadcast()
	}
	a.mu.Unlock()
}

func (a *afterWork) Done() { a.Add(-1) }

// Wait blocks until the counter is zero.
func (a *afterWork) Wait() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cond == nil {
		a.cond = sync.NewCond(&a.mu)
	}
	for a.n > 0 {
		a.cond.Wait()
	}
}
