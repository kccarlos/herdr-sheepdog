// Package ctxcap captures what each model is given, for the /debug
// Context tab: the brain's exact request at every model call (BrainLog)
// and, for the voice model, a reconstruction of everything the daemon
// sent it (VoiceLog). Both are rendered the same way: the raw text as the
// model receives it, cut into labelled Sections that cover it end to end,
// with the provider-native body alongside, and a Diff against the
// previous point. Link ids on the sections tie one piece in the brain's
// request to its counterpart in the voice context (docs/architecture.md
// "Context capture").
//
// The package depends on nothing else in the daemon, so the brain, the
// voice providers and the web server can all import it.
package ctxcap

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The section classes: the colour a piece gets on the Context tab. The
// same class means the same colour in both panes.
const (
	ClassSystem     = "system"      // the system prompt / instructions
	ClassTools      = "tools"       // tool declarations
	ClassMeta       = "meta"        // request-level settings (model, tool choice, thinking)
	ClassShepherd   = "shepherd"    // what the shepherd said (utterances, notes)
	ClassVoice      = "voice"       // what the voice model said
	ClassBrain      = "brain"       // the brain's own replies and promises
	ClassRequest    = "request"     // a turn's request to the brain
	ClassWorker     = "worker"      // worker events and screens (flock output)
	ClassUntrusted  = "untrusted"   // worker output inside the provenance delimiters
	ClassToolCall   = "tool_call"   // a function call a model made
	ClassToolResult = "tool_result" // a function result sent back
	ClassPush       = "push"        // a push to the voice model
	ClassCompaction = "compaction"  // a compaction summary
	ClassPruned     = "pruned"      // a tool result pruned to its stub
	ClassMarker     = "marker"      // session open/close, reset, switch (voice)
	ClassAudio      = "audio"       // audio placeholders (voice)
)

// Section is one labelled piece of a context's raw text. Start and End
// are byte offsets into Snapshot.Text (UTF-8); the sections of a
// snapshot are sorted and contiguous: the first starts at 0, each starts
// where the previous ended, and the last ends at len(Text).
type Section struct {
	// Key identifies the piece across points (the diff): "prompt:roster",
	// "tool:read_screen", "seq:12" (a brain transcript message; "seq:12#2"
	// for the second piece of one), "v:37" (a voice item).
	Key string `json:"key"`
	// Label names the piece for the gutter ("roster", "shepherd
	// utterance", "brain tool result").
	Label string `json:"label"`
	// Class is its colour (the Class constants).
	Class string `json:"class"`
	// Group is the collapsible block it belongs to ("system", "tools",
	// "messages", or a voice session).
	Group  string `json:"group,omitempty"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Tokens int    `json:"tokens"`
	// Path locates the piece in the body ("contents[3].parts[0]",
	// "messages[5]", "config.system_instruction"), for its raw JSON.
	Path string `json:"path,omitempty"`
	// Role is the message role the piece is sent under, when one.
	Role string `json:"role,omitempty"`
	// Seq is the brain transcript message the piece renders (0: none).
	Seq int `json:"seq,omitempty"`
	// Links are the ids shared with the other pane ("call:…", "push:…",
	// "evt:…").
	Links []string `json:"links,omitempty"`
	// Pruned marks a tool result the window holds only as its stub.
	Pruned bool `json:"pruned,omitempty"`
	// Current marks the request of the turn the step belongs to.
	Current bool `json:"current,omitempty"`
	// Text is the piece's text: filled in by the API (Snapshot.Text
	// sliced), not stored.
	Text string `json:"text,omitempty"`
	// Raw is the piece's own JSON, when it has one of its own (a voice
	// item's outbound message); a brain piece's raw JSON is found in the
	// body by Path.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Snapshot is one context as a model received it: the raw text, its
// section map and the provider-native body.
type Snapshot struct {
	Text     string          `json:"text,omitempty"`
	Sections []Section       `json:"sections,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
}

// EstimateTokens is the rough token count of text (4 characters per
// token), the same estimate the brain's window uses.
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// Builder assembles a Snapshot's text and sections together, so the map
// covers the text by construction.
type Builder struct {
	b    strings.Builder
	secs []Section
}

// Add appends text as the section s (Start, End and Tokens are set here).
// An empty text adds nothing.
func (bd *Builder) Add(s Section, text string) {
	if text == "" {
		return
	}
	s.Start = bd.b.Len()
	bd.b.WriteString(text)
	s.End = bd.b.Len()
	s.Tokens = EstimateTokens(text)
	bd.secs = append(bd.secs, s)
}

// Extend appends text to the last section (a separator or a header that
// belongs to it); with no section yet it starts one of class meta.
func (bd *Builder) Extend(text string) {
	if text == "" {
		return
	}
	if len(bd.secs) == 0 {
		bd.Add(Section{Key: "frame", Label: "frame", Class: ClassMeta}, text)
		return
	}
	bd.b.WriteString(text)
	last := &bd.secs[len(bd.secs)-1]
	last.End = bd.b.Len()
	last.Tokens = EstimateTokens(bd.b.String()[last.Start:last.End])
}

// Len is the text length so far.
func (bd *Builder) Len() int { return bd.b.Len() }

// Snapshot returns the text and sections built so far.
func (bd *Builder) Snapshot(body json.RawMessage) Snapshot {
	return Snapshot{Text: bd.b.String(), Sections: append([]Section(nil), bd.secs...), Body: body}
}

// Tokens is the snapshot's estimated size: the sum of its sections.
func (s Snapshot) Tokens() int {
	n := 0
	for _, sec := range s.Sections {
		n += sec.Tokens
	}
	return n
}

// WithTexts returns the sections with Text filled in from s.Text (the
// API form).
func (s Snapshot) WithTexts() []Section {
	out := make([]Section, len(s.Sections))
	for i, sec := range s.Sections {
		if sec.Start >= 0 && sec.End <= len(s.Text) && sec.Start <= sec.End {
			sec.Text = s.Text[sec.Start:sec.End]
		}
		out[i] = sec
	}
	return out
}

// CheckCoverage reports the first gap or overlap in the section map, or
// nil when the sections cover the text exactly.
func (s Snapshot) CheckCoverage() error {
	pos := 0
	for i, sec := range s.Sections {
		if sec.Start != pos {
			return &CoverageError{Index: i, Want: pos, Got: sec.Start}
		}
		if sec.End <= sec.Start {
			return &CoverageError{Index: i, Want: sec.Start + 1, Got: sec.End}
		}
		pos = sec.End
	}
	if pos != len(s.Text) {
		return &CoverageError{Index: len(s.Sections), Want: len(s.Text), Got: pos}
	}
	return nil
}

// CoverageError is a gap or overlap in a section map.
type CoverageError struct {
	Index     int
	Want, Got int
}

func (e *CoverageError) Error() string {
	return fmt.Sprintf("ctxcap: section map: section %d at %d, want %d", e.Index, e.Got, e.Want)
}
