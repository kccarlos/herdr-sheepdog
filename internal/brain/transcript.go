package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Role of a transcript message, in the model's terms.
type Role string

// The roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// MessageKind says where a transcript message came from.
type MessageKind string

// The message kinds.
const (
	// KindCEOUtterance is what the user said (voice.transcript.in).
	KindCEOUtterance MessageKind = "ceo_utterance"
	// KindAssistantSaid is what the assistant said (voice.transcript.out),
	// merged per turn.
	KindAssistantSaid MessageKind = "assistant_said"
	// KindPush is a push the supervisor sent to the assistant.
	KindPush MessageKind = "push"
	// KindWorkerEvent is a herdr transition.
	KindWorkerEvent MessageKind = "worker_event"
	// KindNote is a note the user asked to be remembered.
	KindNote MessageKind = "note"
	// KindRequest is the request as received (utterance, stop or event;
	// transcripts from before hold ask / propose ones too).
	KindRequest MessageKind = "request"
	// KindToolCall is one function call the model made.
	KindToolCall MessageKind = "brain_tool_call"
	// KindToolResult is the result of one function call.
	KindToolResult MessageKind = "brain_tool_result"
	// KindReply is the model's JSON answer.
	KindReply MessageKind = "reply"
	// KindCompaction is a summary that replaced older messages in the
	// window.
	KindCompaction MessageKind = "compaction_summary"
	// KindPromise is a promise's record: made by the promise tool in a
	// user turn, then kept, expired or cancelled by the supervisor (one
	// message per transition; Promise carries the status).
	KindPromise MessageKind = "promise"
	// KindEventScreen is the pane read the supervisor took for the event
	// an event turn is about, put in the window so the turn need not read
	// again; pruned to a stub like a tool result.
	KindEventScreen MessageKind = "event_screen"
	// KindSystem is a note from the daemon about the run itself, not the
	// work: today the model switch ("model switched from gemini/X to
	// deepseek/Y"), so the Supervisor tab shows where the model flipped.
	KindSystem MessageKind = "system"
	// KindState is the per-call state block — roster, ledger,
	// investigations, time — the loop appends after the window on every
	// model call so the prefix before it stays byte-stable
	// (docs/iteration-2.md WP5). Never ingested: not in the transcript
	// and not in the window.
	KindState MessageKind = "state"
)

// Message is one transcript line.
type Message struct {
	// Seq is the message's position in the run's transcript, 1-based and
	// monotonically increasing; assigned by Transcript.Append and kept
	// in memory only (the on-disk line order is the seq). The context
	// window's copies carry it, which is how the Supervisor tab maps the
	// window back onto the transcript.
	Seq     int         `json:"-"`
	TS      time.Time   `json:"ts"`
	Role    Role        `json:"role"`
	Kind    MessageKind `json:"kind"`
	Content string      `json:"content,omitempty"`
	// Agent is the alias the message is about, when one.
	Agent string `json:"agent,omitempty"`
	// Turn is the orchestrator turn that produced the message; 0 for ingested
	// context.
	Turn     int       `json:"turn,omitempty"`
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	// ToolResult describes a function result; a KindEventScreen message
	// carries one too (its Summary is the pruning stub).
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Promise is the record a KindPromise message is about.
	Promise *PromiseInfo `json:"promise,omitempty"`
	// Raw is the provider's own record of the model's message, in the
	// provider's native shape: for Gemini the content of the first tool
	// call of a step (function call parts with their thought signatures),
	// for an OpenAI-compatible provider the assistant message (its
	// reasoning_content and tool_calls). Each adapter parses it only when
	// Provider and Model are its own and replays it so the provider
	// accepts the follow-up; any other adapter rebuilds the message from
	// the neutral fields (ToolCall, Content).
	Raw json.RawMessage `json:"raw,omitempty"`
	// Provider and Model name who produced Raw: after a switch to another
	// provider or model it is not replayed (a signature or a reasoning
	// blob of one model is not accepted by another).
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Tokens is the estimated size of the message.
	Tokens int `json:"tokens,omitempty"`
	// Link ties the message to its counterpart in the voice model's
	// context (the Context tab): "call:<id>" on a request made by the
	// voice's tool call, "push:<id>" on a push, "evt:<id>" on a worker
	// event and on the announce request about it.
	Link string `json:"link,omitempty"`
	// Cut marks a KindAssistantSaid message whose speech the client cut
	// off (a barge-in, the stop button, an interrupting push): Content
	// is only the part the user heard, and CutAfterMs how much of the
	// speech played. See Brain.SaidCut.
	Cut        bool `json:"cut,omitempty"`
	CutAfterMs int  `json:"cut_after_ms,omitempty"`
}

// ToolCall is one function call the model made.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// ToolResult is what one function call returned; the text itself is the
// message Content (so pruning can replace it with a stub).
type ToolResult struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
	DurMS int64  `json:"dur_ms"`
	// Summary is the one-line description a pruned result is replaced
	// with ("screen of auth, 200 lines").
	Summary string `json:"summary,omitempty"`
}

// EstimateTokens is the rough token count of text (4 chars per token).
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// Transcript is the complete, append-only record of one daemon run: in
// memory, and on disk as JSONL when a path is given. Safe for concurrent
// use.
type Transcript struct {
	mu   sync.Mutex
	f    *os.File
	msgs []Message
	// err is the first write error (reported once through onErr).
	err   error
	onErr func(error)
	// onAppend, when set, receives every appended message after the
	// lock is released (the orchestrator logs brain.transcript from it).
	onAppend func(Message)
}

// OpenTranscript creates (or appends to) path; "" keeps the transcript in
// memory only. onErr receives the first write failure and may be nil.
func OpenTranscript(path string, onErr func(error)) (*Transcript, error) {
	t := &Transcript{onErr: onErr}
	if path == "" {
		return t, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("brain: transcript: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("brain: transcript: %w", err)
	}
	t.f = f
	return t, nil
}

// OnAppend sets the hook that receives every appended message (with its
// Seq), called outside the transcript lock. Not safe to call
// concurrently with Append; the orchestrator sets it once at construction.
func (t *Transcript) OnAppend(fn func(Message)) { t.onAppend = fn }

// Append records m (Tokens is filled in when zero, Seq always) and
// returns it.
func (t *Transcript) Append(m Message) Message {
	if m.Tokens == 0 {
		m.Tokens = EstimateTokens(m.Content)
	}
	t.mu.Lock()
	m.Seq = len(t.msgs) + 1
	t.msgs = append(t.msgs, m)
	if t.f != nil {
		line, err := json.Marshal(m)
		if err == nil {
			_, err = t.f.Write(append(line, '\n'))
		}
		if err != nil && t.err == nil {
			t.err = err
			if t.onErr != nil {
				t.onErr(err)
			}
		}
	}
	t.mu.Unlock()
	if t.onAppend != nil {
		t.onAppend(m)
	}
	return m
}

// Reset closes the current file (if any) and starts a fresh, empty
// transcript at newPath ("" keeps it in memory only): a "New
// conversation" (Brain.Reset, docs/roles.md). The old file on disk is
// left as is — it is simply no longer appended to. Safe for concurrent
// use, though the caller (Brain.Reset) holds the turn lock across it so
// no turn's messages land on the wrong side of the reset.
func (t *Transcript) Reset(newPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
	}
	t.msgs = nil
	t.err = nil
	if newPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return fmt.Errorf("brain: transcript: %w", err)
	}
	f, err := os.OpenFile(newPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("brain: transcript: %w", err)
	}
	t.f = f
	return nil
}

// Messages returns a copy of every message, oldest first.
func (t *Transcript) Messages() []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Message(nil), t.msgs...)
}

// Since returns a copy of every message with Seq > seq, oldest first
// (the Supervisor tab's incremental fetch); seq 0 is everything.
func (t *Transcript) Since(seq int) []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	if seq < 0 {
		seq = 0
	}
	if seq >= len(t.msgs) {
		return nil
	}
	return append([]Message(nil), t.msgs[seq:]...)
}

// Len returns the number of messages.
func (t *Transcript) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.msgs)
}

// Close flushes and closes the file.
func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f == nil {
		return nil
	}
	err := t.f.Close()
	t.f = nil
	return err
}

// ReadTranscript parses a JSONL transcript file (brain-test --transcript).
func ReadTranscript(path string) ([]Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Message
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var m Message
		if err := dec.Decode(&m); err != nil {
			return out, fmt.Errorf("brain: transcript %s: line %d: %w", path, len(out)+1, err)
		}
		out = append(out, m)
	}
	return out, nil
}
