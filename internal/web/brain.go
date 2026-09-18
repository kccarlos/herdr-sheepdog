package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// defaultTranscriptN is how many transcript messages GET
// /api/brain/transcript returns without ?n=.
const defaultTranscriptN = 500

// catalogTimeout bounds one model-list fetch behind GET /api/brain/models
// so a hung provider cannot hang /debug.
const catalogTimeout = 15 * time.Second

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
	// Promise is the record a promise message is about (the Supervisor
	// tab's promise card).
	Promise *brain.PromiseInfo `json:"promise,omitempty"`
	Tokens  int                `json:"tokens,omitempty"`
}

func transcriptMessage(m brain.Message) TranscriptMessage {
	return TranscriptMessage{Seq: m.Seq, TS: m.TS, Turn: m.Turn, Role: m.Role, Kind: m.Kind, Content: m.Content, Agent: m.Agent, ToolCall: m.ToolCall, ToolResult: m.ToolResult, Promise: m.Promise, Tokens: m.Tokens}
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
		SystemPrompt: promptInfo(b.PromptSections(), false), Model: b.Model(),
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

// ProviderInfo is one model provider the picker may offer.
type ProviderInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// providers lists the providers the brain can run on: Gemini only for
// now; the API carries the id so more can be added without changing its
// shape.
var providers = []ProviderInfo{{ID: brain.ProviderGemini, Label: "Gemini"}}

// BrainInfo is the brain field of GET /api/state: the provider and the
// model the next turn runs on (omitted when the brain is off).
type BrainInfo struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (s *Server) brainInfo() *BrainInfo {
	if s.deps.Brain == nil {
		return nil
	}
	return &BrainInfo{Provider: brain.ProviderGemini, Model: s.deps.Brain.Model()}
}

// BrainModelsResponse is the body of GET /api/brain/models and PUT
// /api/brain/model: the current provider and model, the providers to
// choose from, and the discovered catalog. A discovery failure is not
// an HTTP error: Models is what is known (the last fetched list, or
// empty) and Error says why it is not fresh, so the picker can still
// show the current model.
type BrainModelsResponse struct {
	Provider  string            `json:"provider"`
	Providers []ProviderInfo    `json:"providers"`
	Model     string            `json:"model"`
	Models    []brain.ModelInfo `json:"models"`
	FetchedAt *time.Time        `json:"fetched_at,omitempty"`
	Stale     bool              `json:"stale"`
	Error     string            `json:"error,omitempty"`
}

func brainModelsResponse(b *brain.Brain, c brain.Catalog) BrainModelsResponse {
	resp := BrainModelsResponse{Provider: brain.ProviderGemini, Providers: providers, Model: b.Model(), Models: c.Models, Stale: c.Stale}
	if !c.FetchedAt.IsZero() {
		at := c.FetchedAt.UTC()
		resp.FetchedAt = &at
	}
	if c.Err != nil {
		resp.Error = c.Err.Error()
	}
	return resp
}

// handleBrainModels answers the model catalog (cached for
// brain.CatalogTTL; ?refresh=1 fetches again) with the current choice.
func (s *Server) handleBrainModels(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), catalogTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, brainModelsResponse(b, b.Models(ctx, refresh)))
}

// SetBrainModelRequest is the body of PUT /api/brain/model.
type SetBrainModelRequest struct {
	// Provider defaults to gemini, the only one so far.
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleSetBrainModel switches the brain's model for the next turn and
// keeps the choice in the state file (session.brain_model), so a
// restart uses it over the configured brain.model. A name the catalog
// does not list is accepted (the list may be stale); the picker marks
// it. Answers as GET /api/brain/models, from the cached catalog.
func (s *Server) handleSetBrainModel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	var req SetBrainModelRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if p := strings.ToLower(strings.TrimSpace(req.Provider)); p != "" && p != brain.ProviderGemini {
		writeError(w, http.StatusBadRequest, "provider "+strconv.Quote(req.Provider)+" is not supported: only gemini for now")
		return
	}
	if err := b.SetModel(req.Model, "debug"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := b.Model()
	if err := s.deps.Store.UpdateSession(func(ss *state.Session) { ss.BrainModel = model }); err != nil {
		writeError(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, brainModelsResponse(b, b.CachedModels()))
}
