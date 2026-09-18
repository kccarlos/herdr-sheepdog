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
	Provider        string                `json:"provider"`
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
		SystemPrompt: promptInfo(b.PromptSections(), false), Provider: b.Provider(), Model: b.Model(),
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

// ProviderInfo is one model provider the picker may offer: registered
// with the brain (Available) or not, with the reason ("no
// DEEPSEEK_API_KEY") so /debug can list it greyed out rather than hide
// it.
type ProviderInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// providerLabels are the display names of the known provider ids; an
// unknown id is shown as itself.
var providerLabels = map[string]string{brain.ProviderGemini: "Gemini", brain.ProviderDeepSeek: "DeepSeek"}

// ProviderLabel returns the display name of a provider id.
func ProviderLabel(id string) string {
	if l, ok := providerLabels[id]; ok {
		return l
	}
	return id
}

// providers lists the providers for the API: Deps.BrainProviders when
// the daemon set it (it knows why a provider is missing), else every
// provider registered with the brain, available.
func (s *Server) providers(b *brain.Brain) []ProviderInfo {
	if len(s.deps.BrainProviders) > 0 {
		return append([]ProviderInfo(nil), s.deps.BrainProviders...)
	}
	var out []ProviderInfo
	for _, id := range b.Providers() {
		out = append(out, ProviderInfo{ID: id, Label: ProviderLabel(id), Available: true})
	}
	return out
}

// providerReason says why provider cannot be switched to: the reason
// the daemon listed it with, or that it is unknown.
func (s *Server) providerReason(b *brain.Brain, provider string) string {
	for _, p := range s.providers(b) {
		if p.ID == provider && !p.Available {
			if p.Reason != "" {
				return p.Reason
			}
			return "not configured"
		}
	}
	return "unknown provider (registered: " + strings.Join(b.Providers(), ", ") + ")"
}

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
	return &BrainInfo{Provider: s.deps.Brain.Provider(), Model: s.deps.Brain.Model()}
}

// BrainModelsResponse is the body of GET /api/brain/models and PUT
// /api/brain/model: the current provider and model, the providers to
// choose from (unavailable ones included, with the reason), and the
// discovered catalog of ModelsProvider (the current provider, or the one
// ?provider= asked for). A discovery failure is not an HTTP error:
// Models is what is known (the last fetched list, or empty) and Error
// says why it is not fresh, so the picker can still show the current
// model.
type BrainModelsResponse struct {
	Provider       string            `json:"provider"`
	Providers      []ProviderInfo    `json:"providers"`
	Model          string            `json:"model"`
	ModelsProvider string            `json:"models_provider"`
	Models         []brain.ModelInfo `json:"models"`
	FetchedAt      *time.Time        `json:"fetched_at,omitempty"`
	Stale          bool              `json:"stale"`
	Error          string            `json:"error,omitempty"`
}

func (s *Server) brainModelsResponse(b *brain.Brain, c brain.Catalog) BrainModelsResponse {
	resp := BrainModelsResponse{Provider: b.Provider(), Providers: s.providers(b), Model: b.Model(), ModelsProvider: c.Provider, Models: c.Models, Stale: c.Stale}
	if resp.Models == nil {
		resp.Models = []brain.ModelInfo{}
	}
	if !c.FetchedAt.IsZero() {
		at := c.FetchedAt.UTC()
		resp.FetchedAt = &at
	}
	if c.Err != nil {
		resp.Error = c.Err.Error()
	}
	return resp
}

// handleBrainModels answers the model catalog of ?provider= (default:
// the current provider; 400 with the reason for one that is not
// registered), cached for brain.CatalogTTL, ?refresh=1 fetching again,
// with the current choice.
func (s *Server) handleBrainModels(w http.ResponseWriter, r *http.Request) {
	b, ok := s.brainOrUnavailable(w)
	if !ok {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provider != "" && !registered(b, provider) {
		writeError(w, http.StatusBadRequest, "provider "+strconv.Quote(provider)+": "+s.providerReason(b, provider))
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), catalogTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, s.brainModelsResponse(b, b.Models(ctx, provider, refresh)))
}

// registered reports whether provider is one the brain can run on.
func registered(b *brain.Brain, provider string) bool {
	for _, id := range b.Providers() {
		if id == provider {
			return true
		}
	}
	return false
}

// SetBrainModelRequest is the body of PUT /api/brain/model.
type SetBrainModelRequest struct {
	// Provider defaults to the current one.
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleSetBrainModel switches the brain's provider and model for the
// next turn and keeps the choice in the state file
// (session.brain_provider, session.brain_model), so a restart uses it
// over the configured brain.provider / brain.model. A provider that is
// not registered is a 400 with the reason; a name the catalog does not
// list is accepted (the list may be stale); the picker marks it.
// Answers as GET /api/brain/models, from the new provider's cached
// catalog.
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
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "" && !registered(b, provider) {
		writeError(w, http.StatusBadRequest, "provider "+strconv.Quote(provider)+": "+s.providerReason(b, provider))
		return
	}
	if err := b.SetModel(provider, req.Model, "debug"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, model := b.Provider(), b.Model()
	if err := s.deps.Store.UpdateSession(func(ss *state.Session) { ss.BrainProvider, ss.BrainModel = provider, model }); err != nil {
		writeError(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.brainModelsResponse(b, b.CachedModels(provider)))
}
