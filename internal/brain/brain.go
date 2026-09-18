package brain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// logSource is the source field of every brain.* event.
const logSource = "brain"

// Defaults for Config zero values.
const (
	DefaultModel          = "gemini-3.5-flash"
	DefaultMaxSteps       = 6
	DefaultAskTimeout     = 8 * time.Second
	DefaultProposeTimeout = 12 * time.Second
	// DefaultEnrichTimeout bounds an event turn (summarize, keep_promise):
	// on expiry nothing extra is said (the template sentence was already
	// pushed).
	DefaultEnrichTimeout   = 6 * time.Second
	DefaultWindowTokens    = 24000
	DefaultPruneAfterTurns = 3
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
	// Model is the model name passed to Model.Generate.
	Model string
	// MaxSteps bounds the model calls in one turn.
	MaxSteps int
	// AskTimeout / ProposeTimeout bound one turn including its tools;
	// EnrichTimeout bounds an event turn.
	AskTimeout, ProposeTimeout, EnrichTimeout time.Duration
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
	// overrides (docs/roles.md "Prompt files"); nil uses the embedded
	// defaults. Recomputed every turn, so a reload takes effect at once.
	Prompts *prompts.Loader
	// Now overrides the clock (tests).
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.AskTimeout <= 0 {
		c.AskTimeout = DefaultAskTimeout
	}
	if c.ProposeTimeout <= 0 {
		c.ProposeTimeout = DefaultProposeTimeout
	}
	if c.EnrichTimeout <= 0 {
		c.EnrichTimeout = DefaultEnrichTimeout
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

// Brain is the SVP (see the package doc). Safe for concurrent use: turns
// are serialised by the turn lock; ingestion may happen at any time.
type Brain struct {
	cfg        Config
	model      Model
	log        logging.Logger
	transcript *Transcript
	engine     ContextEngine
	tools      []*genai.Tool // the full surface (CEO turns)
	readTools  []*genai.Tool // the read-only surface (event turns)

	lock  turnLock
	after sync.WaitGroup  // after-turn work in flight
	ctx   context.Context // ends on Close (bounds after-turn work)
	stop  context.CancelFunc

	mu           sync.Mutex
	host         Host
	turns        int
	toolSeq      int
	inBuf        []string // voice transcript fragments not yet final
	outBuf       []string
	promptLogged bool
	userVars     map[string]string // USER.md's {chattiness}/{tz}; SetUserVars
	// lastCompaction is the most recent successful compaction (zero
	// value when none), for the Supervisor tab.
	lastCompaction CompactionInfo
}

// CompactionInfo describes one compaction for Brain.LastCompaction.
type CompactionInfo struct {
	BeforeTokens int       `json:"before_tokens"`
	AfterTokens  int       `json:"after_tokens"`
	Dropped      int       `json:"dropped"`
	SummarySeq   int       `json:"summary_seq"`
	At           time.Time `json:"at"`
}

// New builds a Brain over model. log may be nil. The Host is attached by
// the supervisor through SetHost.
func New(cfg Config, model Model, log logging.Logger) (*Brain, error) {
	if model == nil {
		return nil, errors.New("brain: model required")
	}
	if log == nil {
		log = logging.NopLogger{}
	}
	cfg = cfg.withDefaults()
	b := &Brain{cfg: cfg, model: model, log: log, tools: toolDeclarations(false), readTools: toolDeclarations(true)}
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

// SetUserVars sets USER.md's {chattiness}/{tz} placeholders; the brain has
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

// Engine returns the context engine in use.
func (b *Brain) Engine() ContextEngine { return b.engine }

// Config returns the effective configuration (defaults applied).
func (b *Brain) Config() Config { return b.cfg }

// Queued returns how many turns wait for the turn lock, CEO turns and
// event turns separately.
func (b *Brain) Queued() (ceo, event int) {
	ceo = b.lock.ceoWaiting()
	return ceo, b.lock.waiting() - ceo
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

// TranscriptPath returns the transcript file, or "" when in memory.
func (b *Brain) TranscriptPath() string {
	if b.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(b.cfg.DataDir, TranscriptDir, b.cfg.RunID+".jsonl")
}

// Close ends after-turn work, waits for it and closes the transcript.
func (b *Brain) Close() error {
	b.stop()
	b.after.Wait()
	return b.transcript.Close()
}

// Ask runs one ask turn triggered by the CEO.
func (b *Brain) Ask(ctx context.Context, req Request) (Reply, error) {
	req.Kind = KindAsk
	return b.Turn(ctx, req)
}

// Propose runs one propose turn triggered by the CEO.
func (b *Brain) Propose(ctx context.Context, req Request) (Reply, error) {
	req.Kind = KindPropose
	return b.Turn(ctx, req)
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

// Heard ingests a fragment of the CEO's speech (voice.transcript.in);
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

// Said ingests a fragment of the assistant's speech (voice.transcript.out).
// The CEO's pending fragments are flushed first: the assistant answering
// means the CEO finished.
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
func (b *Brain) Pushed(alias, text, urgency string) {
	if text == "" {
		return
	}
	content := text
	if urgency != "" {
		content = fmt.Sprintf("(%s) %s", urgency, text)
	}
	b.ingest(Message{Role: RoleUser, Kind: KindPush, Content: content, Agent: alias}, 0)
}

// WorkerEvent ingests a herdr transition.
func (b *Brain) WorkerEvent(alias, from, to string, closed bool) {
	if from == "" {
		from = "?"
	}
	content := fmt.Sprintf("%s: %s -> %s", aliasOr(alias), from, to)
	if closed {
		content = fmt.Sprintf("%s: pane closed", aliasOr(alias))
	}
	b.ingest(Message{Role: RoleUser, Kind: KindWorkerEvent, Content: content, Agent: alias}, 0)
}

// PromiseEvent ingests a promise's record outside a turn: a promise
// kept, expired or cancelled by the supervisor, or an open promise
// carried over from an earlier run (the brain's transcript is per run,
// the promise is not).
func (b *Brain) PromiseEvent(p PromiseInfo) {
	if p.ID == "" {
		return
	}
	cp := p
	b.ingest(Message{Role: RoleUser, Kind: KindPromise, Content: promiseLine(p), Agent: p.Agent, Promise: &cp}, 0)
}

// Noted ingests a note the CEO asked to be remembered.
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

// summarize is the compaction's model call: one plain text-in/text-out
// generation, no tools.
func (b *Brain) summarize(ctx context.Context, text string) (string, error) {
	prompt := "Summarise the following supervisor transcript in at most 12 short lines for your own later reference: what the CEO asked and decided, what each agent was told, did, asked and answered, and anything still open. Keep aliases; drop paths, hashes and ids.\n\n" + text
	resp, err := b.model.Generate(ctx, b.cfg.Model, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: prompt}}}}, &genai.GenerateContentConfig{})
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(textOf(resp))
	if out == "" {
		return "", errors.New("brain: empty summary")
	}
	return out, nil
}
