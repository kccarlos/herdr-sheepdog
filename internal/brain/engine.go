package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// ContextEngine decides what part of the transcript the model sees. The
// names are OpenClaw's; this is the seam the memory system (plan P6)
// plugs into (see the package doc).
type ContextEngine interface {
	// Ingest receives every transcript message right after it was
	// appended (turn is the current turn number, 0 outside a turn).
	Ingest(m Message, turn int)
	// Assemble returns the window for the next model call: the messages
	// in order, and an optional addition to the system prompt (a
	// memory-backed engine puts recalled facts there).
	Assemble(budget int) Assembly
	// Compact shrinks the window when it exceeds budget (estimated
	// tokens); ok is false when nothing was done. The default engine
	// summarises the oldest half with one model call. The orchestrator calls
	// it without the turn lock (a turn, Ingest, Retract or Reset may run
	// meanwhile), so an engine must be safe for that and must not splice
	// a summary over messages that changed while it was written
	// (ErrCompactionStale).
	Compact(ctx context.Context, budget int) (Compaction, bool, error)
	// AfterTurn runs bookkeeping at the end of turn (the default engine
	// prunes tool results older than PruneAfterTurns turns).
	AfterTurn(turn int)
	// Tokens is the estimated size of the window.
	Tokens() int
}

// Assembly is what Assemble returns.
type Assembly struct {
	Messages []Message
	// SystemAddition is appended to the system prompt when non-empty.
	SystemAddition string
}

// Compaction reports one compaction for brain.compaction.
type Compaction struct {
	BeforeTokens, AfterTokens int
	// Dropped is how many messages the summary replaced.
	Dropped int
	// Summary is the message that replaced them.
	Summary Message
}

// WindowEntry is one message of the window as the Supervisor tab sees
// it: which transcript message (Seq), its size in the window, and whether
// the window holds only a stub of it.
type WindowEntry struct {
	Seq    int         `json:"seq"`
	Kind   MessageKind `json:"kind"`
	Turn   int         `json:"turn"`
	Tokens int         `json:"tokens"`
	// Pruned means the window's copy is the one-line Stub, not the
	// transcript's full content.
	Pruned bool   `json:"pruned"`
	Stub   string `json:"stub,omitempty"`
}

// WindowReporter is the optional view a ContextEngine may offer of its
// window (the default engine does); Brain.Window derives one from
// Assemble for engines that do not.
type WindowReporter interface {
	Window() []WindowEntry
}

// Resettable is the optional part of a ContextEngine that empties the
// window for a "New conversation" (docs/roles.md, Brain.Reset): the
// default engine implements it. An engine that does not (a future
// memory-backed one, plan P6) is simply left as is — Brain.Reset always
// starts a fresh transcript segment regardless.
type Resettable interface {
	Reset()
}

// Retracter is the optional part of a ContextEngine that takes one
// message back out of the window (Brain.Echo: words transcribed as the
// user's that were the assistant's own speech). The default engine
// implements it; an engine that does not keeps the message, and the
// note Brain.Echo adds after it still says it was an echo.
type Retracter interface {
	Retract(seq int) bool
}

// ErrCompactionStale is returned by Compact when the messages it
// summarised changed while the summary was being written (a Retract, a
// Reset or another compaction): the summary is discarded and the window
// left as it is, for the next compaction to try again.
var ErrCompactionStale = errors.New("brain: compaction discarded: the summarised messages changed meanwhile")

// Summarizer turns a rendering of old messages into a summary: the
// default engine's one model call per compaction.
type Summarizer func(ctx context.Context, text string) (string, error)

// DefaultEngine keeps the run's messages in memory, prunes tool results
// to stubs after pruneAfter turns and compacts the oldest half into one
// summary when over budget. Safe for concurrent use.
type DefaultEngine struct {
	pruneAfter int
	summarize  Summarizer
	// record receives the compaction summary so the transcript on disk
	// has it too.
	record func(Message) Message

	mu     sync.Mutex
	window []entry
	// nextID numbers the entries (entry.id).
	nextID uint64
}

type entry struct {
	msg    Message
	turn   int
	pruned bool
	// id is unique within the engine: Compact checks by it that the
	// prefix it summarised is still there.
	id uint64
}

// NewDefaultEngine builds the default engine. summarize is required for
// Compact (nil makes it a no-op); record may be nil.
func NewDefaultEngine(pruneAfter int, summarize Summarizer, record func(Message) Message) *DefaultEngine {
	if pruneAfter <= 0 {
		pruneAfter = DefaultPruneAfterTurns
	}
	return &DefaultEngine{pruneAfter: pruneAfter, summarize: summarize, record: record}
}

// Ingest implements ContextEngine. A bulky tool result or event screen
// enters the window capped to its last windowResultBytes (capResult);
// the transcript on disk keeps it whole.
func (e *DefaultEngine) Ingest(m Message, turn int) {
	if prunable(m) {
		if c, ok := capResult(m.Content); ok {
			m.Content, m.Tokens = c, EstimateTokens(c)
		}
	}
	e.mu.Lock()
	e.nextID++
	e.window = append(e.window, entry{msg: m, turn: turn, id: e.nextID})
	e.mu.Unlock()
}

// windowResultBytes caps a live tool result (or event screen) in the
// window: the tail, the part that holds the latest output
// (docs/iteration-2.md WP5; a 200-line screen was ~16 KB, 4 k tokens of
// every step for two turns). Fixed, not a knob.
const windowResultBytes = 4 << 10

// resultCutNote marks where capResult cut a result.
const resultCutNote = "[earlier output cut for the window; read_screen or recall for more]"

// capResult returns content cut to its last windowResultBytes and true,
// or content and false when it fits. Provenance delimiters survive: a
// wrapped result keeps its first (BEGIN) and last (END) lines and only
// the text between is tailed.
func capResult(content string) (string, bool) {
	if len(content) <= windowResultBytes {
		return content, false
	}
	head, body, tail := "", content, ""
	if strings.HasPrefix(content, "<<<BEGIN_WORKER_OUTPUT") {
		if i := strings.IndexByte(content, '\n'); i > 0 {
			head, body = content[:i+1], content[i+1:]
		}
		if j := strings.LastIndex(body, "\n"+endWorkerPrefix); j >= 0 {
			body, tail = body[:j], body[j:]
		}
	}
	room := windowResultBytes - len(head) - len(tail) - len(resultCutNote) - 1
	if room < 512 {
		room = 512 // an absurdly long BEGIN line still leaves some output
	}
	return head + resultCutNote + "\n" + prose.TailBytes(body, room) + tail, true
}

// Assemble implements ContextEngine.
func (e *DefaultEngine) Assemble(int) Assembly {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Message, 0, len(e.window))
	for _, en := range e.window {
		out = append(out, en.msg)
	}
	return Assembly{Messages: out}
}

// Window implements WindowReporter: the window in order, one entry per
// message, pruned ones with their stub.
func (e *DefaultEngine) Window() []WindowEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]WindowEntry, 0, len(e.window))
	for _, en := range e.window {
		w := WindowEntry{Seq: en.msg.Seq, Kind: en.msg.Kind, Turn: en.turn, Tokens: en.msg.Tokens, Pruned: en.pruned}
		if en.pruned {
			w.Stub = en.msg.Content
		}
		out = append(out, w)
	}
	return out
}

// Reset implements Resettable: the window is emptied (the compaction
// summary included), so the next Assemble starts from nothing.
func (e *DefaultEngine) Reset() {
	e.mu.Lock()
	e.window = nil
	e.mu.Unlock()
}

// Retract implements Retracter: the entry holding message seq leaves
// the window; false when it is not there (pruned into a summary, or
// never ingested).
func (e *DefaultEngine) Retract(seq int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, en := range e.window {
		if en.msg.Seq == seq {
			e.window = append(e.window[:i:i], e.window[i+1:]...)
			return true
		}
	}
	return false
}

// Tokens implements ContextEngine.
func (e *DefaultEngine) Tokens() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tokensLocked()
}

func (e *DefaultEngine) tokensLocked() int {
	n := 0
	for _, en := range e.window {
		n += en.msg.Tokens
	}
	return n
}

// AfterTurn implements ContextEngine: tool results (and the screens
// event turns were handed) ingested pruneAfter or more turns ago are
// replaced by their one-line stub.
func (e *DefaultEngine) AfterTurn(turn int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.window {
		en := &e.window[i]
		if en.pruned || !prunable(en.msg) || turn-en.turn < e.pruneAfter {
			continue
		}
		en.msg.Content = stubFor(en.msg)
		en.msg.Tokens = EstimateTokens(en.msg.Content)
		en.pruned = true
	}
}

// prunable reports whether m is bulky data a stub can stand in for: a
// tool result, or an event turn's screen.
func prunable(m Message) bool {
	return m.Kind == KindToolResult || (m.Kind == KindEventScreen && m.ToolResult != nil)
}

// stubFor is the one-line replacement of a pruned tool result.
func stubFor(m Message) string {
	if m.ToolResult != nil && m.ToolResult.Summary != "" {
		return "[" + m.ToolResult.Summary + ", pruned]"
	}
	name := "tool result"
	if m.ToolResult != nil {
		name = m.ToolResult.Name + " result"
	}
	return "[" + name + ", pruned]"
}

// Compact implements ContextEngine: when the window is over budget, the
// oldest half (cut at a message boundary that keeps every tool call with
// its result) is rendered, summarised and replaced by one
// compaction_summary message.
func (e *DefaultEngine) Compact(ctx context.Context, budget int) (Compaction, bool, error) {
	e.mu.Lock()
	before := e.tokensLocked()
	if before <= budget || e.summarize == nil || len(e.window) < 2 {
		e.mu.Unlock()
		return Compaction{}, false, nil
	}
	cut := cutPoint(e.window, before/2)
	if cut <= 0 {
		e.mu.Unlock()
		return Compaction{}, false, nil
	}
	old := append([]entry(nil), e.window[:cut]...)
	e.mu.Unlock()

	text := renderForSummary(old)
	summary, err := e.summarize(ctx, text)
	if err != nil {
		return Compaction{}, false, err
	}
	summary = capSummary(summary)
	// The summary call ran without the orchestrator's turn lock: whatever
	// changed the summarised prefix meanwhile (Retract, Reset, another
	// compaction) wins, and the summary is dropped before it reaches the
	// transcript. Checked again after recording, which runs outside the
	// engine's lock (the transcript's append hook logs).
	e.mu.Lock()
	same := samePrefix(e.window, old)
	e.mu.Unlock()
	if !same {
		return Compaction{}, false, ErrCompactionStale
	}
	m := Message{TS: old[len(old)-1].msg.TS, Role: RoleUser, Kind: KindCompaction, Content: summary}
	if e.record != nil {
		m = e.record(m)
	} else {
		m.Tokens = EstimateTokens(m.Content)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !samePrefix(e.window, old) {
		return Compaction{}, false, ErrCompactionStale
	}
	// Messages ingested during the model call stay; only the ones
	// summarised go.
	rest := append([]entry(nil), e.window[cut:]...)
	e.nextID++
	e.window = append([]entry{{msg: m, turn: old[len(old)-1].turn, id: e.nextID}}, rest...)
	return Compaction{BeforeTokens: before, AfterTokens: e.tokensLocked(), Dropped: cut, Summary: m}, true, nil
}

// samePrefix reports whether window still starts with exactly the
// entries of old, by entry id: pruning a result to its stub meanwhile
// does not count as a change, the summary covers it either way.
func samePrefix(window, old []entry) bool {
	if len(window) < len(old) {
		return false
	}
	for i, en := range old {
		if window[i].id != en.id {
			return false
		}
	}
	return true
}

// summaryMaxTokens caps a compaction summary (estimated tokens): a
// 12 k window cannot afford a summary that grows with every compaction
// (docs/iteration-2.md WP5).
const summaryMaxTokens = 600

// capSummary cuts a summary to summaryMaxTokens at a line boundary when
// it can.
func capSummary(s string) string {
	s = strings.TrimSpace(s)
	limit := summaryMaxTokens * 4
	if len(s) <= limit {
		return s
	}
	cut := strings.ToValidUTF8(s[:limit-len("…")], "")
	if i := strings.LastIndexByte(cut, '\n'); i > limit/2 {
		return strings.TrimSpace(cut[:i])
	}
	return strings.TrimSpace(cut) + "…"
}

// cutPoint returns the index of the first message kept: the smallest
// boundary at or past half the tokens where the next message starts a
// fresh exchange (never between a tool call and its result, never right
// before a reply). A context message ingested while a step's tools ran
// (the assistant's "On it.") sits between the call and its result, so a
// boundary kind is not enough: no call may still wait for its result.
func cutPoint(window []entry, half int) int {
	acc := 0
	open := map[string]bool{} // calls before the cut still waiting for a result
	for i, en := range window {
		acc += en.msg.Tokens
		trackCalls(open, en.msg)
		if acc < half {
			continue
		}
		for j := i + 1; j < len(window); j++ {
			if len(open) == 0 && boundary(window[j].msg.Kind) {
				return j
			}
			trackCalls(open, window[j].msg)
		}
		return 0
	}
	return 0
}

// trackCalls updates the set of calls waiting for a result with m.
func trackCalls(open map[string]bool, m Message) {
	switch m.Kind {
	case KindToolCall:
		open[callKey(m)] = true
	case KindToolResult:
		delete(open, resultKey(m))
	}
}

// boundary reports whether a message of kind k may open the window.
func boundary(k MessageKind) bool {
	switch k {
	case KindCEOUtterance, KindAssistantSaid, KindPush, KindWorkerEvent, KindNote, KindRequest, KindCompaction, KindPromise, KindSystem:
		return true
	default:
		return false
	}
}

// renderForSummary is the text the summariser reads.
func renderForSummary(old []entry) string {
	var b strings.Builder
	for _, en := range old {
		m := en.msg
		line := m.Content
		if m.Kind == KindToolCall && m.ToolCall != nil {
			line = fmt.Sprintf("%s(%v)", m.ToolCall.Name, m.ToolCall.Args)
		}
		if len(line) > 600 {
			line = line[:600] + "…"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.TS.Format("15:04"), m.Kind, line)
	}
	return b.String()
}
