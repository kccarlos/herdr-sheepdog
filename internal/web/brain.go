package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// defaultTranscriptN is how many transcript messages GET
// /api/brain/transcript returns without ?n=.
const defaultTranscriptN = 500

// TranscriptMessage is one row of GET /api/brain/transcript: a
// brain.Message with its in-memory Seq made explicit (the on-disk line
// has none; its order is the seq) and without the provider's raw content.
type TranscriptMessage struct {
	Seq        int               `json:"seq"`
	TS         time.Time         `json:"ts"`
	Turn       int               `json:"turn"`
	Role       brain.Role        `json:"role"`
	Kind       brain.MessageKind `json:"kind"`
	Content    string            `json:"content,omitempty"`
	Agent      string            `json:"agent,omitempty"`
	ToolCall   *brain.ToolCall   `json:"tool_call,omitempty"`
	ToolResult *brain.ToolResult `json:"tool_result,omitempty"`
	Tokens     int               `json:"tokens,omitempty"`
}

func transcriptMessage(m brain.Message) TranscriptMessage {
	return TranscriptMessage{Seq: m.Seq, TS: m.TS, Turn: m.Turn, Role: m.Role, Kind: m.Kind, Content: m.Content, Agent: m.Agent, ToolCall: m.ToolCall, ToolResult: m.ToolResult, Tokens: m.Tokens}
}

// PromptSectionInfo is one system prompt section as GET /api/brain/context
// (sizes only) and GET /api/brain/system_prompt (with the text) report
// it.
type PromptSectionInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Chars  int    `json:"chars"`
	Tokens int    `json:"tokens"`
	Text   string `json:"text,omitempty"`
}

// PromptInfo is the system prompt summary of both endpoints.
type PromptInfo struct {
	Sections    []PromptSectionInfo `json:"sections"`
	TotalChars  int                 `json:"total_chars"`
	TotalTokens int                 `json:"total_tokens"`
	Text        string              `json:"text,omitempty"`
}

// ContextResponse is the body of GET /api/brain/context: what is in the
// model's window right now, against the budget, plus the last
// compaction and the prompt sections' sizes.
type ContextResponse struct {
	// WindowTokens is the configured budget (brain.window_tokens); it
	// bounds the transcript window only, the system prompt is on top.
	WindowTokens int `json:"window_tokens"`
	// EstimatedTokens is the window's estimated size.
	EstimatedTokens int                   `json:"estimated_tokens"`
	Window          []brain.WindowEntry   `json:"window"`
	LastCompaction  *brain.CompactionInfo `json:"last_compaction"`
	SystemPrompt    PromptInfo            `json:"system_prompt"`
	Model           string                `json:"model"`
	Turns           int                   `json:"turns"`
	TranscriptLen   int                   `json:"transcript_len"`
	TranscriptPath  string                `json:"transcript_path,omitempty"`
	Now             time.Time             `json:"now"`
}

// brainOrUnavailable answers 503 with the reason when no brain is
// attached.
func (s *Server) brainOrUnavailable(w http.ResponseWriter) (*brain.Brain, bool) {
	if s.deps.Brain != nil {
		return s.deps.Brain, true
	}
	reason := s.deps.BrainOffReason
	if reason == "" {
		reason = "not configured"
	}
	writeError(w, http.StatusServiceUnavailable, "the supervisor brain is off ("+reason+")")
	return nil, false
}

// handleBrainTranscript answers the run's transcript messages, oldest
// first: ?since=<seq> for everything after seq (incremental fetch),
// ?n= for at most the last n (default 500).
func (s *Server) handleBrainTranscript(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = defaultTranscriptN
	}
	msgs := b.Transcript().Since(since)
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	out := make([]TranscriptMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, transcriptMessage(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func promptInfo(secs []brain.PromptSection, withText bool) PromptInfo {
	info := PromptInfo{Sections: make([]PromptSectionInfo, 0, len(secs))}
	var text []byte
	for i, sec := range secs {
		row := PromptSectionInfo{Name: sec.Name, Source: sec.Source, Chars: len(sec.Text), Tokens: brain.EstimateTokens(sec.Text)}
		if withText {
			row.Text = sec.Text
		}
		info.Sections = append(info.Sections, row)
		if i > 0 {
			text = append(text, "\n\n"...)
		}
		text = append(text, sec.Text...)
	}
	info.TotalChars = len(text)
	info.TotalTokens = brain.EstimateTokens(string(text))
	if withText {
		info.Text = string(text)
	}
	return info
}

// handleBrainContext answers what the model's window holds right now.
func (s *Server) handleBrainContext(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	win := b.Window()
	if win == nil {
		win = []brain.WindowEntry{}
	}
	est := 0
	for _, e := range win {
		est += e.Tokens
	}
	resp := ContextResponse{
		WindowTokens: b.Config().WindowTokens, EstimatedTokens: est, Window: win,
		SystemPrompt: promptInfo(b.PromptSections(), false), Model: b.Config().Model,
		Turns: b.Turns(), TranscriptLen: b.Transcript().Len(), TranscriptPath: b.TranscriptPath(), Now: s.deps.Now().UTC(),
	}
	if c, ok := b.LastCompaction(); ok {
		resp.LastCompaction = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleBrainSystemPrompt answers the assembled system prompt, section by
// section with its text and source, as the next model call would get it.
func (s *Server) handleBrainSystemPrompt(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, promptInfo(b.PromptSections(), true))
}
