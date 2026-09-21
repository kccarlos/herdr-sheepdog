package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// DeepSeekBaseURL is DeepSeek's API root (OpenAICompatConfig.BaseURL).
const DeepSeekBaseURL = "https://api.deepseek.com"

// ReasoningEfforts are the values DeepSeek's reasoning_effort accepts.
var ReasoningEfforts = []string{"low", "high", "max"}

// compatResponseBytes bounds a response body read.
const compatResponseBytes = 4 << 20

// OpenAICompatConfig configures an OpenAICompatModel.
type OpenAICompatConfig struct {
	// Provider is the id the adapter answers to: Message.Provider on
	// what it produces, the key of the brain's provider map
	// (ProviderDeepSeek).
	Provider string
	// BaseURL is the API root; /chat/completions and /models are
	// appended (DeepSeekBaseURL).
	BaseURL string
	// APIKey is the bearer token. Never logged.
	APIKey string
	// Thinking, when set, is sent as DeepSeek's thinking mode field
	// ({"type": "enabled"|"disabled"}); nil leaves the provider's
	// default (enabled). ReasoningEffort, when set, is sent with it
	// (one of ReasoningEfforts).
	Thinking        *bool
	ReasoningEffort string
	// HTTPClient overrides http.DefaultClient (tests). No client timeout
	// is set: the turn's context bounds every call.
	HTTPClient *http.Client
	// Log receives brain.rate_limited; nil discards.
	Log logging.Logger
}

// OpenAICompatModel is a Model over an OpenAI-compatible chat
// completions endpoint (DeepSeek), one key, no rotation: a 429 fails the
// call (logged as brain.rate_limited) and is never retried here — the
// turn falls back to the code sentence. It implements MessageModel, so
// the loop hands it the window's messages and it re-attaches the
// reasoning_content DeepSeek requires on every earlier assistant
// message while tools are declared, from Message.Raw of the messages it
// produced itself (same provider and model); Generate over Gemini
// contents serves the compaction summary and the eval harness. Safe for
// concurrent use.
type OpenAICompatModel struct {
	cfg  OpenAICompatConfig
	http *http.Client
	log  logging.Logger
}

// NewOpenAICompatModel returns an OpenAICompatModel; Provider, BaseURL
// and APIKey are required.
func NewOpenAICompatModel(cfg OpenAICompatConfig) (*OpenAICompatModel, error) {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch {
	case cfg.Provider == "":
		return nil, errors.New("brain: openai-compatible model: provider id required")
	case strings.TrimSpace(cfg.BaseURL) == "":
		return nil, fmt.Errorf("brain: %s: base URL required", cfg.Provider)
	case cfg.APIKey == "":
		return nil, fmt.Errorf("brain: %s: no API key configured", cfg.Provider)
	}
	if cfg.ReasoningEffort != "" && !validReasoningEffort(cfg.ReasoningEffort) {
		return nil, fmt.Errorf("brain: %s: reasoning effort %q: want one of %s", cfg.Provider, cfg.ReasoningEffort, strings.Join(ReasoningEfforts, ", "))
	}
	m := &OpenAICompatModel{cfg: cfg, http: cfg.HTTPClient, log: cfg.Log}
	if m.http == nil {
		m.http = http.DefaultClient
	}
	if m.log == nil {
		m.log = logging.NopLogger{}
	}
	return m, nil
}

// validReasoningEffort reports whether v is one of ReasoningEfforts.
func validReasoningEffort(v string) bool {
	for _, e := range ReasoningEfforts {
		if v == e {
			return true
		}
	}
	return false
}

// Provider returns the adapter's provider id.
func (m *OpenAICompatModel) Provider() string { return m.cfg.Provider }

// APIError is the endpoint's HTTP error: the status and the API's own
// message, verbatim (a 402 "Insufficient Balance" must reach the event
// log as such). isRateLimited is true for a 429.
type APIError struct {
	Provider string
	Status   int
	Message  string
	// RetryAfter is the Retry-After header, when one came with a 429.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// The request and response shapes (the subset the brain uses; see
// api-docs.deepseek.com/api/create-chat-completion).
type (
	chatRequest struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
		Tools    []chatTool    `json:"tools,omitempty"`
		// ToolChoice is "none" on an answer-only step; the tools stay
		// declared so the reasoning_content rule is the same on every
		// step of the loop.
		ToolChoice      string        `json:"tool_choice,omitempty"`
		Thinking        *chatThinking `json:"thinking,omitempty"`
		ReasoningEffort string        `json:"reasoning_effort,omitempty"`
		Stream          bool          `json:"stream"`
	}
	chatThinking struct {
		Type string `json:"type"`
	}
	// chatMessage is one message either way; an assistant message the
	// API returned is stored as Message.Raw in this shape and replayed
	// from it.
	chatMessage struct {
		Role             string         `json:"role"`
		Content          string         `json:"content"`
		ReasoningContent string         `json:"reasoning_content,omitempty"`
		ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string         `json:"tool_call_id,omitempty"`
	}
	chatToolCall struct {
		ID       string           `json:"id"`
		Type     string           `json:"type"`
		Function chatFunctionCall `json:"function"`
	}
	chatFunctionCall struct {
		Name string `json:"name"`
		// Arguments is the JSON object as a string.
		Arguments string `json:"arguments"`
	}
	chatTool struct {
		Type     string       `json:"type"`
		Function chatFunction `json:"function"`
	}
	chatFunction struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	}
	chatResponse struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	chatError struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	modelsResponse struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
)

// Generate implements Model over Gemini contents: text parts become
// user/assistant messages, function calls and responses tool calls and
// tool messages (ids as given; without reasoning_content, which the
// contents cannot carry — the loop uses GenerateMessages instead).
func (m *OpenAICompatModel) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	resp, _, err := m.call(ctx, model, messagesFromContents(contents), cfg)
	return resp, err
}

// GenerateMessages implements MessageModel: the request is built from
// the window's messages (messagesOf), and the assistant message the API
// returned is handed back as raw for the loop to store.
func (m *OpenAICompatModel) GenerateMessages(ctx context.Context, model string, msgs []Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	return m.call(ctx, model, m.messagesOf(msgs, model), cfg)
}

// call is one chat completion: cfg's system instruction, tools and
// answer-only tool config are translated, the response mapped onto a
// GenerateContentResponse (text part, function call parts with the
// API's ids, usage, finish reason).
func (m *OpenAICompatModel) call(ctx context.Context, model string, msgs []chatMessage, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	req := chatRequest{Model: model, Messages: msgs}
	if cfg != nil {
		if sys := systemText(cfg.SystemInstruction); sys != "" {
			req.Messages = append([]chatMessage{{Role: "system", Content: sys}}, req.Messages...)
		}
		req.Tools = toolsOf(cfg.Tools)
		if len(req.Tools) > 0 && cfg.ToolConfig != nil && cfg.ToolConfig.FunctionCallingConfig != nil && cfg.ToolConfig.FunctionCallingConfig.Mode == genai.FunctionCallingConfigModeNone {
			req.ToolChoice = "none"
		}
	}
	if m.cfg.Thinking != nil {
		req.Thinking = &chatThinking{Type: "disabled"}
		if *m.cfg.Thinking {
			req.Thinking.Type = "enabled"
			req.ReasoningEffort = m.cfg.ReasoningEffort
		}
	}
	var out chatResponse
	if err := m.post(ctx, model, "/chat/completions", req, &out); err != nil {
		return nil, nil, err
	}
	if len(out.Choices) == 0 {
		return nil, nil, fmt.Errorf("%s: response without choices", m.cfg.Provider)
	}
	choice := out.Choices[0]
	msg := choice.Message
	msg.Role = "assistant"
	var parts []*genai.Part
	if msg.Content != "" {
		parts = append(parts, &genai.Part{Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		args := map[string]any{}
		if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
			if err := json.Unmarshal([]byte(s), &args); err != nil {
				return nil, nil, fmt.Errorf("%s: tool call %s: arguments are not a JSON object: %w", m.cfg.Provider, tc.Function.Name, err)
			}
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: tc.ID, Name: tc.Function.Name, Args: args}})
	}
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Role: genai.RoleModel, Parts: parts}, FinishReason: finishReason(choice.FinishReason)}}}
	if u := out.Usage; u != nil {
		usage := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: int32(u.PromptTokens), CandidatesTokenCount: int32(u.CompletionTokens), TotalTokenCount: int32(u.TotalTokens)}
		if u.PromptTokensDetails != nil {
			usage.CachedContentTokenCount = int32(u.PromptTokensDetails.CachedTokens)
		}
		if u.CompletionTokensDetails != nil {
			usage.ThoughtsTokenCount = int32(u.CompletionTokensDetails.ReasoningTokens)
		}
		resp.UsageMetadata = usage
	}
	var raw json.RawMessage
	if msg.ReasoningContent != "" || len(msg.ToolCalls) > 0 {
		raw, _ = json.Marshal(msg)
	}
	return resp, raw, nil
}

// finishReason maps the API's finish_reason onto the SDK's.
func finishReason(s string) genai.FinishReason {
	switch s {
	case "stop", "tool_calls":
		return genai.FinishReasonStop
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	case "":
		return genai.FinishReasonUnspecified
	default:
		return genai.FinishReasonOther
	}
}

// ListModels implements ModelCatalog over GET /models: every model the
// endpoint lists, recommended (DeepSeek serves chat models only), by
// name.
func (m *OpenAICompatModel) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out modelsResponse
	if err := m.get(ctx, "/models", &out); err != nil {
		return nil, err
	}
	list := make([]ModelInfo, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID == "" {
			continue
		}
		list = append(list, ModelInfo{Name: d.ID, DisplayName: d.ID, Description: "owned by " + d.OwnedBy, Methods: []string{"chat/completions"}, Recommended: true})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// post sends body to path and decodes the response into out; model is
// for the brain.rate_limited event only.
func (m *OpenAICompatModel) post(ctx context.Context, model, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encode request: %w", m.cfg.Provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url(path), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: build request: %w", m.cfg.Provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return m.do(req, model, out)
}

// get fetches path and decodes the response into out.
func (m *OpenAICompatModel) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url(path), nil)
	if err != nil {
		return fmt.Errorf("%s: build request: %w", m.cfg.Provider, err)
	}
	return m.do(req, "", out)
}

func (m *OpenAICompatModel) url(path string) string {
	return strings.TrimSuffix(strings.TrimSpace(m.cfg.BaseURL), "/") + path
}

// do performs the request. A non-2xx status is an *APIError carrying
// the API's error.message (or the body's first line); a 429 is also
// wrapped in ErrAllKeysRateLimited (one key: nothing to rotate to) and
// logged as brain.rate_limited.
func (m *OpenAICompatModel) do(req *http.Request, model string, out any) error {
	req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", m.cfg.Provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, compatResponseBytes))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", m.cfg.Provider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Provider: m.cfg.Provider, Status: resp.StatusCode, Message: errorMessage(data, resp.Status)}
		if resp.StatusCode != http.StatusTooManyRequests {
			return apiErr
		}
		if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && secs > 0 {
			apiErr.RetryAfter = time.Duration(secs) * time.Second
		} else if wait, ok := retryDelay(apiErr); ok {
			apiErr.RetryAfter = wait
		}
		m.log.Log(logSource, "brain.rate_limited", "", map[string]any{
			"provider": m.cfg.Provider, "model": model, "key_index": 0, "keys": 1, "retry_after_ms": apiErr.RetryAfter.Milliseconds(),
		})
		return fmt.Errorf("%w: %w", ErrAllKeysRateLimited, apiErr)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: decode response: %w", m.cfg.Provider, err)
	}
	return nil
}

// errorMessage is the API's error.message, else the body's first line
// (truncated), else the HTTP status text.
func errorMessage(body []byte, status string) string {
	var e chatError
	if json.Unmarshal(body, &e) == nil && e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(body)), "\n")
	if line = prose.TruncateRunes(line, 300); line != "" {
		return line
	}
	return status
}

// systemText concatenates the system instruction's text parts.
func systemText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p != nil {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// toolsOf renders the brain's function declarations as OpenAI tools,
// their parameters as JSON Schema (schemaOf).
func toolsOf(tools []*genai.Tool) []chatTool {
	var out []chatTool
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			if d == nil {
				continue
			}
			out = append(out, chatTool{Type: "function", Function: chatFunction{Name: d.Name, Description: d.Description, Parameters: schemaOf(d.Parameters)}})
		}
	}
	return out
}

// schemaOf renders a genai.Schema as a JSON Schema object: the type
// enum lowercased ("STRING" is "string"), description, enum, items,
// properties and required carried over, nested schemas recursively. An
// object without properties gets an empty properties map so it stays a
// well-formed object schema.
func schemaOf(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}
	out := map[string]any{}
	if s.Type != "" && s.Type != genai.TypeUnspecified {
		out["type"] = strings.ToLower(string(s.Type))
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		out["enum"] = append([]string(nil), s.Enum...)
	}
	if s.Items != nil {
		out["items"] = schemaOf(s.Items)
	}
	if s.Type == genai.TypeObject || len(s.Properties) > 0 {
		props := map[string]any{}
		for name, p := range s.Properties {
			props[name] = schemaOf(p)
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = append([]string(nil), s.Required...)
	}
	if s.Nullable != nil && *s.Nullable {
		out["nullable"] = true
	}
	return out
}

// messagesOf renders the window as chat messages for model (the
// counterpart of toContents). Context messages become user lines,
// merged while consecutive; a step's tool calls become the assistant
// message the API returned (Raw: its reasoning_content and tool_calls,
// ids included) when this provider and model produced it, else one
// assistant message with the calls rebuilt from the neutral fields
// (another origin's reasoning is not replayed); a step with several
// calls is its first message's Raw, the Raw-less ones being skipped when
// it is replayed and merged when it is not. Results are tool messages
// with the call's id; replies are assistant text, with their
// reasoning_content from Raw under the same rule.
//
// DeepSeek requires the reasoning_content of every earlier assistant
// message while tools are declared. It is kept here for as long as the
// message is in the window: pruning touches only tool-result content,
// and compaction drops whole exchanges (the summary that replaces them
// is plain text the API needs nothing for). The reasoning blobs are
// large and not counted in the window's token estimate — the trade-off
// is a correct follow-up over a tight estimate; CONFIG.md [deepseek]
// says so.
func (m *OpenAICompatModel) messagesOf(msgs []Message, model string) []chatMessage {
	var out []chatMessage
	var text []string // pending user lines
	flushText := func() {
		if len(text) == 0 {
			return
		}
		out = append(out, chatMessage{Role: "user", Content: strings.Join(text, "\n")})
		text = nil
	}
	var calls []chatToolCall // pending rebuilt tool calls (no Raw)
	callText := ""
	flushCalls := func() {
		if len(calls) == 0 {
			return
		}
		out = append(out, chatMessage{Role: "assistant", Content: callText, ToolCalls: calls})
		calls, callText = nil, ""
	}
	rawCovers := false // the step's Raw was replayed: its other calls are in it
	own := func(msg Message) bool { return msg.Provider == m.cfg.Provider && msg.Model == model }

	for _, msg := range msgs {
		if msg.Kind != KindToolCall {
			rawCovers = false
		}
		switch msg.Kind {
		case KindToolCall:
			flushText()
			if len(msg.Raw) > 0 && own(msg) {
				flushCalls()
				var cm chatMessage
				if err := json.Unmarshal(msg.Raw, &cm); err == nil && len(cm.ToolCalls) > 0 {
					cm.Role, cm.ToolCallID = "assistant", ""
					out = append(out, cm)
					rawCovers = true
					continue
				}
			}
			if len(msg.Raw) > 0 {
				// Another origin's Raw: this call starts a step of its own.
				rawCovers = false
			}
			if rawCovers {
				continue
			}
			if msg.ToolCall != nil {
				args := "{}"
				if len(msg.ToolCall.Args) > 0 {
					if b, err := json.Marshal(msg.ToolCall.Args); err == nil {
						args = string(b)
					}
				}
				calls = append(calls, chatToolCall{ID: msg.ToolCall.ID, Type: "function", Function: chatFunctionCall{Name: msg.ToolCall.Name, Arguments: args}})
				if msg.Content != "" {
					callText = msg.Content
				}
			}
		case KindToolResult:
			flushText()
			flushCalls()
			cm := chatMessage{Role: "tool", Content: msg.Content}
			if msg.ToolResult != nil {
				cm.ToolCallID = msg.ToolResult.ID
				if msg.ToolResult.Error != "" {
					cm.Content = "Error: " + msg.ToolResult.Error
				}
			}
			out = append(out, cm)
		case KindReply:
			flushText()
			flushCalls()
			cm := chatMessage{Role: "assistant", Content: msg.Content}
			if len(msg.Raw) > 0 && own(msg) {
				var r chatMessage
				if json.Unmarshal(msg.Raw, &r) == nil {
					cm.ReasoningContent = r.ReasoningContent
				}
			}
			out = append(out, cm)
		default:
			flushCalls()
			text = append(text, contextLine(msg))
		}
	}
	flushText()
	flushCalls()
	return out
}

// messagesFromContents renders Gemini contents as chat messages
// (Generate): user text becomes user messages, model text assistant
// messages, function call parts an assistant message with tool calls
// (the part's id, or the name when it has none), function responses tool
// messages with the same id.
func messagesFromContents(contents []*genai.Content) []chatMessage {
	var out []chatMessage
	for _, c := range contents {
		if c == nil {
			continue
		}
		role := "user"
		if c.Role == genai.RoleModel || c.Role == "assistant" {
			role = "assistant"
		}
		var texts []string
		var calls []chatToolCall
		for _, p := range c.Parts {
			switch {
			case p == nil:
			case p.FunctionCall != nil:
				args, _ := json.Marshal(p.FunctionCall.Args)
				if len(p.FunctionCall.Args) == 0 {
					args = []byte("{}")
				}
				id := p.FunctionCall.ID
				if id == "" {
					id = p.FunctionCall.Name
				}
				calls = append(calls, chatToolCall{ID: id, Type: "function", Function: chatFunctionCall{Name: p.FunctionCall.Name, Arguments: string(args)}})
			case p.FunctionResponse != nil:
				id := p.FunctionResponse.ID
				if id == "" {
					id = p.FunctionResponse.Name
				}
				body, _ := json.Marshal(p.FunctionResponse.Response)
				out = append(out, chatMessage{Role: "tool", ToolCallID: id, Content: string(body)})
			case p.Text != "" && !p.Thought:
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 || len(calls) > 0 {
			out = append(out, chatMessage{Role: role, Content: strings.Join(texts, "\n"), ToolCalls: calls})
		}
	}
	return out
}
