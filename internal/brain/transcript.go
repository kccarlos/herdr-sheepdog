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
	// KindCEOUtterance is what the CEO said (voice.transcript.in).
	KindCEOUtterance MessageKind = "ceo_utterance"
	// KindAssistantSaid is what the assistant said (voice.transcript.out),
	// merged per turn.
	KindAssistantSaid MessageKind = "assistant_said"
	// KindPush is a push the supervisor sent to the assistant.
	KindPush MessageKind = "push"
	// KindWorkerEvent is a herdr transition.
	KindWorkerEvent MessageKind = "worker_event"
	// KindNote is a note the CEO asked to be remembered.
	KindNote MessageKind = "note"
	// KindRequest is the voice tool call as received (ask / propose).
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
)

// Message is one transcript line.
type Message struct {
	TS      time.Time   `json:"ts"`
	Role    Role        `json:"role"`
	Kind    MessageKind `json:"kind"`
	Content string      `json:"content,omitempty"`
	// Agent is the alias the message is about, when one.
	Agent string `json:"agent,omitempty"`
	// Turn is the brain turn that produced the message; 0 for ingested
	// context.
	Turn       int         `json:"turn,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	// Raw is the model's own content for the first tool call of a step
	// (function call parts with their thought signatures), replayed
	// verbatim so the provider accepts the follow-up.
	Raw json.RawMessage `json:"raw,omitempty"`
	// Tokens is the estimated size of the message.
	Tokens int `json:"tokens,omitempty"`
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

// Append records m (Tokens is filled in when zero) and returns it.
func (t *Transcript) Append(m Message) Message {
	if m.Tokens == 0 {
		m.Tokens = EstimateTokens(m.Content)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.msgs = append(t.msgs, m)
	if t.f == nil {
		return m
	}
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
	return m
}

// Messages returns a copy of every message, oldest first.
func (t *Transcript) Messages() []Message {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Message(nil), t.msgs...)
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
