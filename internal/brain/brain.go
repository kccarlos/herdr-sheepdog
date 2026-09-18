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
)

// logSource is the source field of every brain.* event.
const logSource = "brain"

// Defaults for Config zero values.
const (
	DefaultModel           = "gemini-3.5-flash"
	DefaultMaxSteps        = 6
	DefaultAskTimeout      = 8 * time.Second
	DefaultProposeTimeout  = 12 * time.Second
	DefaultWindowTokens    = 24000
	DefaultPruneAfterTurns = 3
	// DefaultCompactTimeout bounds the summary call of a compaction,
	// which runs after the reply was returned.
	DefaultCompactTimeout = 30 * time.Second
	// TranscriptDir is the directory under the data dir.
	TranscriptDir = "brain"
)

// Config tunes a Brain; zero values take the defaults.
type Config struct {
	// Model is the model name passed to Model.Generate.
	Model string
	// MaxSteps bounds the model calls in one turn.
	MaxSteps int
	// AskTimeout / ProposeTimeout bound one turn including its tools.
	AskTimeout, ProposeTimeout time.Duration
	// WindowTokens is the estimated window size that triggers compaction.
	WindowTokens int
	// PruneAfterTurns is how many turns a tool result stays verbatim.
	PruneAfterTurns int
	// CompactTimeout bounds the compaction's model call.
	CompactTimeout time.Duration
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
	if c.WindowTokens <= 0 {
		c.WindowTokens = DefaultWindowTokens
	}
	if c.PruneAfterTurns <= 0 {
		c.PruneAfterTurns = DefaultPruneAfterTurns
	}
	if c.CompactTimeout <= 0 {
		c.CompactTimeout = DefaultCompactTimeout
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
	stable     []section
	tools      []*genai.Tool

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
	stable, err := stableSections()
	if err != nil {
		return nil, err
	}
	b := &Brain{cfg: cfg, model: model, log: log, stable: stable, tools: toolDeclarations()}
	b.ctx, b.stop = context.WithCancel(context.Background())
	path := ""
	if cfg.DataDir != "" {
		path = filepath.Join(cfg.DataDir, TranscriptDir, cfg.RunID+".jsonl")
	}
	b.transcript, err = OpenTranscript(path, func(err error) {
		log.Log(logSource, "brain.transcript_error", "", map[string]any{"path": path, "error": err.Error()})
	})
	if err != nil {
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

// Transcript returns the run's transcript.
func (b *Brain) Transcript() *Transcript { return b.transcript }

// Engine returns the context engine in use.
func (b *Brain) Engine() ContextEngine { return b.engine }

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
