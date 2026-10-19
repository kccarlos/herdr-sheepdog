package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// The Anthropic orchestrator (docs/dev/anthropic-adapter.md): a
// MessageModel over the official Go SDK's Messages API.
const (
	// ProviderAnthropic is the native Anthropic provider.
	ProviderAnthropic = "anthropic"
	// DefaultAnthropicModel is what brain.provider = "anthropic" runs
	// without brain.model, and what a failover to Anthropic uses.
	DefaultAnthropicModel = "claude-opus-5-5"
	// AnthropicBaseURL is the API root.
	AnthropicBaseURL = "https://api.anthropic.com"
	// anthropicMaxTokens bounds one response, thinking included (it
	// counts toward max_tokens even when its text is not returned).
	anthropicMaxTokens = 16000
)

// AnthropicEfforts are the values output_config.effort accepts.
var AnthropicEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// AnthropicConfig configures an AnthropicModel.
type AnthropicConfig struct {
	// APIKey is sent as x-api-key, nothing else. Never logged.
	APIKey string
	// BaseURL is the API root (AnthropicBaseURL when empty).
	BaseURL string
	// DefaultModel is what a failover to this provider runs
	// (DefaultAnthropicModel when empty).
	DefaultModel string
	// Effort is output_config.effort on utterance turns ("medium" when
	// empty); EventEffort on event turns and the compaction summary
	// (the calls the loop marks with the minimal thinking level; "low"
	// when empty).
	Effort      string
	EventEffort string
	// NoFallbacks turns the server-side refusal fallbacks off (they are
	// on by default: fallbacks "default").
	NoFallbacks bool
	// HTTPClient overrides the SDK's client (tests). No client timeout:
	// the turn's context bounds every call.
	HTTPClient *http.Client
	// Log receives brain.rate_limited and brain.thinking_dropped; nil
	// discards.
	Log logging.Logger
}

// AnthropicModel is a Model over the Anthropic Messages API, one key, no
// SDK retries (a 429, a 529 or a 5xx fails the call and the brain's
// failover decides). It implements MessageModel so it can replay its own
// thinking blocks for the current turn (section 2 of the design note),
// and ModelCatalog over GET /v1/models. Safe for concurrent use.
type AnthropicModel struct {
	cfg    AnthropicConfig
	client anthropic.Client
	log    logging.Logger
}

// NewAnthropicModel returns an AnthropicModel; APIKey is required. The
// SDK client reads nothing from the environment (no ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL or ant profile): the key and
// base URL are the config's alone.
func NewAnthropicModel(cfg AnthropicConfig) (*AnthropicModel, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("brain: anthropic: no API key configured")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = AnthropicBaseURL
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultAnthropicModel
	}
	if cfg.Effort == "" {
		cfg.Effort = "medium"
	}
	if cfg.EventEffort == "" {
		cfg.EventEffort = "low"
	}
	for _, e := range []string{cfg.Effort, cfg.EventEffort} {
		if !ValidAnthropicEffort(e) {
			return nil, fmt.Errorf("brain: anthropic: effort %q: want one of %s", e, strings.Join(AnthropicEfforts, ", "))
		}
	}
	m := &AnthropicModel{cfg: cfg, log: cfg.Log}
	if m.log == nil {
		m.log = logging.NopLogger{}
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(strings.TrimSpace(cfg.BaseURL)),
		option.WithMaxRetries(0),
		option.WithoutOpenTelemetry(),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	m.client = anthropic.NewClient(opts...)
	return m, nil
}

// ValidAnthropicEffort reports whether e is one of AnthropicEfforts.
func ValidAnthropicEffort(e string) bool {
	for _, v := range AnthropicEfforts {
		if e == v {
			return true
		}
	}
	return false
}

// Provider returns ProviderAnthropic.
func (m *AnthropicModel) Provider() string { return ProviderAnthropic }

// DefaultModel is what a failover to Anthropic runs.
func (m *AnthropicModel) DefaultModel() string { return m.cfg.DefaultModel }

// currentModel matches the model families that always think and take
// effort, block_binding and adaptive thinking (Opus, Sonnet, Haiku,
// Fable and Mythos 5.x). Another id gets a plain request.
var currentModel = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-5`)

// fallbackModels take server-side fallbacks "default" (the Claude API
// guide: Fable 5.1, Opus 5.5, Opus 5, Sonnet 5.5; Haiku 5.5 has none).
var fallbackModels = map[string]bool{"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true}

// anthropicRaw is what Message.Raw holds for a message this adapter
// produced: the content blocks as request params (replayed verbatim in
// the same turn, without their thinking blocks after it), the state
// text sent just before it, and the highest window Seq of the request
// (the turn is kept append-only with both; design note section 2).
type anthropicRaw struct {
	Content []json.RawMessage `json:"content"`
	State   string            `json:"state,omitempty"`
	Seen    int               `json:"seen,omitempty"`
	// Served is the model that answered (a fallback model, when one
	// served the response).
	Served string `json:"served,omitempty"`
}

// ablock is one content block of a request message: a replayed block
// (raw) or one built here (p).
type ablock struct {
	raw json.RawMessage
	p   anthropic.BetaContentBlockParamUnion
	// state marks the state block (the cache breakpoint goes before
	// the current one).
	state bool
}

type amsg struct {
	role   string
	blocks []ablock
}

// Generate implements Model over genai contents (the compaction summary,
// the eval harness): text, function calls and responses, no thinking
// replay.
func (m *AnthropicModel) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	resp, _, err := m.call(ctx, model, func(bool) []amsg { return amsgsFromContents(contents) }, cfg, nil)
	return resp, err
}

// GenerateMessages implements MessageModel: the request is built from
// the window (messagesOf), and the response's blocks come back as raw
// for the loop to store.
func (m *AnthropicModel) GenerateMessages(ctx context.Context, model string, msgs []Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	meta := &anthropicRaw{}
	for _, msg := range msgs {
		meta.Seen = max(meta.Seen, msg.Seq)
		if msg.Kind == KindState {
			meta.State = msg.Content
		}
	}
	build := func(thinking bool) []amsg { return m.messagesOf(msgs, model, thinking) }
	return m.call(ctx, model, build, cfg, meta)
}

// call is one Messages API request, retried once without thinking blocks
// and betas when the API refuses a replayed block or a beta (backstop 2).
func (m *AnthropicModel) call(ctx context.Context, model string, build func(thinking bool) []amsg, cfg *genai.GenerateContentConfig, meta *anthropicRaw) (*genai.GenerateContentResponse, json.RawMessage, error) {
	params, err := m.params(model, build(true), cfg, true)
	if err != nil {
		return nil, nil, err
	}
	msg, err := m.stream(ctx, model, params)
	if err != nil && strippable(err) && ctx.Err() == nil {
		m.log.Log(logSource, "brain.thinking_dropped", "", map[string]any{"provider": ProviderAnthropic, "model": model, "reason": "retry_without_thinking", "error": err.Error()})
		if params, err = m.params(model, build(false), cfg, false); err != nil {
			return nil, nil, err
		}
		msg, err = m.stream(ctx, model, params)
	}
	if err != nil {
		return nil, nil, err
	}
	return m.response(model, msg, meta)
}

// strippable is a 400 that a request without thinking blocks and
// without betas avoids: an invalid or foreign thinking signature, or a
// beta header the endpoint does not know (a proxy at a custom base URL).
func strippable(err error) bool {
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusBadRequest {
		return false
	}
	s := strings.ToLower(api.Message)
	return (strings.Contains(s, "signature") && strings.Contains(s, "thinking")) ||
		strings.Contains(s, "different conversation") || strings.Contains(s, "anthropic-beta") || strings.Contains(s, "block_binding")
}

// params builds the request. full sends the betas, server-side fallbacks
// and block_binding; the retry (backstop 2) sends none of them.
func (m *AnthropicModel) params(model string, msgs []amsg, cfg *genai.GenerateContentConfig, full bool) (anthropic.BetaMessageNewParams, error) {
	p := anthropic.BetaMessageNewParams{Model: model, MaxTokens: anthropicMaxTokens}
	if cfg != nil {
		if sys := systemText(cfg.SystemInstruction); sys != "" {
			p.System = []anthropic.BetaTextBlockParam{{Text: sys, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}}
		}
		p.Tools = anthropicTools(cfg.Tools)
		if len(p.Tools) > 0 && cfg.ToolConfig != nil && cfg.ToolConfig.FunctionCallingConfig != nil && cfg.ToolConfig.FunctionCallingConfig.Mode == genai.FunctionCallingConfigModeNone {
			// The answer-only step: the tools stay declared (the current
			// turn's thinking is bound to them); forced any/tool is a
			// 400 on Opus 5.5 and is never sent.
			p.ToolChoice = anthropic.BetaToolChoiceUnionParam{OfNone: &anthropic.BetaToolChoiceNoneParam{}}
		}
	}
	if currentModel.MatchString(model) {
		adaptive := anthropic.BetaThinkingConfigAdaptiveParam{}
		if full {
			adaptive.BlockBinding = anthropic.BetaThinkingBlockBindingParam{PrefixMismatchBehavior: anthropic.BetaThinkingPrefixMismatchBehaviorDropBlock}
			p.Betas = append(p.Betas, anthropic.AnthropicBetaThinkingBindingControls2026_08_01)
		}
		p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &adaptive}
		p.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(m.effortFor(cfg))}
		if full && fallbackModels[model] && !m.cfg.NoFallbacks {
			p.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
			p.Betas = append(p.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
		}
	}
	out, err := renderMessages(msgs)
	if err != nil {
		return p, err
	}
	p.Messages = out
	return p, nil
}

// effortFor is the effort of a call: EventEffort for the calls marked
// with the minimal thinking level (event turns, the compaction
// summary), Effort otherwise.
func (m *AnthropicModel) effortFor(cfg *genai.GenerateContentConfig) string {
	if cfg != nil && cfg.ThinkingConfig != nil && cfg.ThinkingConfig.ThinkingLevel == genai.ThinkingLevelMinimal {
		return m.cfg.EventEffort
	}
	return m.cfg.Effort
}

// renderMessages turns the request messages into SDK params: consecutive
// messages of one role merged, the cache breakpoint on the last block
// before the current state (else the last block), and a check that the
// request starts with the user and does not end on an assistant
// message (a prefill: a 400 on the current models).
func renderMessages(msgs []amsg) ([]anthropic.BetaMessageParam, error) {
	var merged []amsg
	for _, a := range msgs {
		if len(a.blocks) == 0 {
			continue
		}
		if n := len(merged); n > 0 && merged[n-1].role == a.role {
			merged[n-1].blocks = append(merged[n-1].blocks, a.blocks...)
			continue
		}
		merged = append(merged, amsg{role: a.role, blocks: append([]ablock(nil), a.blocks...)})
	}
	if len(merged) == 0 {
		return nil, errors.New("brain: anthropic: empty request")
	}
	if merged[0].role != "user" {
		merged = append([]amsg{{role: "user", blocks: []ablock{{p: anthropic.NewBetaTextBlock("(The conversation so far.)")}}}}, merged...)
	}
	last := &merged[len(merged)-1]
	if last.role != "user" {
		return nil, errors.New("brain: anthropic: the request would end on an assistant message (a prefill)")
	}
	mark := len(last.blocks) - 1
	if last.blocks[mark].state && mark > 0 {
		mark--
	}
	setCacheControl(&last.blocks[mark])
	out := make([]anthropic.BetaMessageParam, 0, len(merged))
	for _, a := range merged {
		parts := make([]json.RawMessage, 0, len(a.blocks))
		for _, b := range a.blocks {
			if b.raw != nil {
				parts = append(parts, b.raw)
				continue
			}
			data, err := json.Marshal(b.p)
			if err != nil {
				return nil, fmt.Errorf("brain: anthropic: encode block: %w", err)
			}
			parts = append(parts, data)
		}
		body, err := json.Marshal(struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		}{a.role, parts})
		if err != nil {
			return nil, fmt.Errorf("brain: anthropic: encode message: %w", err)
		}
		out = append(out, param.Override[anthropic.BetaMessageParam](json.RawMessage(body)))
	}
	return out, nil
}

// setCacheControl puts the ephemeral breakpoint on a built text or
// tool_result block (a replayed block is never a user block).
func setCacheControl(b *ablock) {
	switch {
	case b.raw != nil:
	case b.p.OfText != nil:
		b.p.OfText.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
	case b.p.OfToolResult != nil:
		b.p.OfToolResult.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
	}
}

// stream sends params and accumulates the streamed message. An error is
// the brain's *APIError (anthropicError), a 429 also wrapped in
// ErrAllKeysRateLimited and logged as brain.rate_limited.
func (m *AnthropicModel) stream(ctx context.Context, model string, params anthropic.BetaMessageNewParams) (anthropic.BetaMessage, error) {
	s := m.client.Beta.Messages.NewStreaming(ctx, params)
	defer func() { _ = s.Close() }()
	var msg anthropic.BetaMessage
	for s.Next() {
		if err := msg.Accumulate(s.Current()); err != nil {
			return msg, fmt.Errorf("brain: anthropic: stream: %w", err)
		}
	}
	if err := s.Err(); err != nil {
		return msg, m.mapError(model, err)
	}
	return msg, nil
}

// mapError converts the SDK's error into the brain's *APIError so
// AccountProblemOfErr, failoverReason and isRateLimited classify it:
// 401/403 an invalid key, 402 (billing_error) the balance, 429 a rate
// limit, 529 (overloaded_error) and 5xx a failover. A stream error
// event carries no HTTP status: its error type gives one.
func (m *AnthropicModel) mapError(model string, err error) error {
	var sdk *anthropic.Error
	if !errors.As(err, &sdk) {
		return fmt.Errorf("%s: %w", ProviderAnthropic, err)
	}
	typ, message := anthropicErrorBody(sdk.RawJSON())
	if typ == "" {
		typ = string(sdk.Type())
	}
	status := sdk.StatusCode
	if status < 400 {
		// A stream error event after a 200.
		status = anthropicStatusOf(typ)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if typ != "" && !strings.Contains(message, typ) {
		message = typ + ": " + message
	}
	apiErr := &APIError{Provider: ProviderAnthropic, Status: status, Message: message}
	if status != http.StatusTooManyRequests {
		return apiErr
	}
	if sdk.Response != nil {
		if secs, perr := strconv.Atoi(sdk.Response.Header.Get("Retry-After")); perr == nil && secs > 0 {
			apiErr.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	m.log.Log(logSource, "brain.rate_limited", "", map[string]any{
		"provider": ProviderAnthropic, "model": model, "key_index": 0, "keys": 1, "retry_after_ms": apiErr.RetryAfter.Milliseconds(),
	})
	return fmt.Errorf("%w: %w", ErrAllKeysRateLimited, apiErr)
}

// anthropicErrorBody is the error type and message of the API's error
// envelope ({"type":"error","error":{"type":…,"message":…}}).
func anthropicErrorBody(raw string) (typ, message string) {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &env) != nil {
		return "", ""
	}
	return env.Error.Type, env.Error.Message
}

// anthropicStatusOf is the HTTP status of an error type (a stream error
// event has none of its own).
func anthropicStatusOf(typ string) int {
	switch typ {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "billing_error":
		return 402
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "overloaded_error":
		return 529
	}
	return 500
}

// ErrRefused is a response that ended in stop_reason "refusal" after the
// server-side fallbacks (if any) also declined: no content is used.
var ErrRefused = errors.New("brain: anthropic: refused")

// response maps the streamed message onto a GenerateContentResponse and
// the raw to store. Blocks are read by type: text parts, function calls
// from tool_use blocks, thinking kept for the raw only. Before the last
// fallback block (a mid-output switch), only text is kept, as the API
// asks for the echo.
func (m *AnthropicModel) response(model string, msg anthropic.BetaMessage, meta *anthropicRaw) (*genai.GenerateContentResponse, json.RawMessage, error) {
	m.noteDropped(model, msg)
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		cat := string(msg.StopDetails.Category)
		if cat == "" {
			cat = "none"
		}
		return nil, nil, fmt.Errorf("%w (category %s)", ErrRefused, cat)
	}
	cut := -1
	for i, b := range msg.Content {
		if b.Type == "fallback" {
			cut = i
		}
	}
	var parts []*genai.Part
	var text []string
	var keep []json.RawMessage
	for i, b := range msg.Content {
		if b.Type == "fallback" || (i < cut && b.Type != "text") {
			continue
		}
		bp := b.ToParam()
		if bp.OfToolUse != nil && len(b.Input) > 0 {
			// The input as the API sent it, not re-marshaled from a map
			// (key order): the replay must be the same bytes.
			bp.OfToolUse.Input = b.Input
		}
		data, err := json.Marshal(bp)
		if err != nil {
			return nil, nil, fmt.Errorf("brain: anthropic: encode block: %w", err)
		}
		keep = append(keep, data)
		switch v := b.AsAny().(type) {
		case anthropic.BetaTextBlock:
			if i > cut && v.Text != "" {
				text = append(text, v.Text)
			}
		case anthropic.BetaToolUseBlock:
			args := map[string]any{}
			if len(b.Input) > 0 {
				if err := json.Unmarshal(b.Input, &args); err != nil {
					return nil, nil, fmt.Errorf("%s: tool call %s: input is not a JSON object: %w", ProviderAnthropic, v.Name, err)
				}
			}
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: v.ID, Name: v.Name, Args: args}})
		}
	}
	if len(text) > 0 {
		parts = append([]*genai.Part{{Text: strings.Join(text, "")}}, parts...)
	}
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Role: genai.RoleModel, Parts: parts}, FinishReason: anthropicFinish(msg.StopReason)}}}
	u := msg.Usage
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	resp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: int32(prompt), CandidatesTokenCount: int32(u.OutputTokens),
		TotalTokenCount: int32(prompt + u.OutputTokens), CachedContentTokenCount: int32(u.CacheReadInputTokens),
	}
	if meta == nil || len(keep) == 0 {
		return resp, nil, nil
	}
	r := *meta
	r.Content, r.Served = keep, msg.Model
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, nil, fmt.Errorf("brain: anthropic: encode raw: %w", err)
	}
	return resp, raw, nil
}

// noteDropped logs brain.thinking_dropped when the API dropped replayed
// thinking blocks (drop_block, or a model switch): the leftover cases
// the append-only rendering did not cover are counted, not assumed.
func (m *AnthropicModel) noteDropped(model string, msg anthropic.BetaMessage) {
	n, reasons := 0, map[string]int{}
	for _, t := range msg.InputTransformations {
		if t.Type != "thinking_dropped" {
			continue
		}
		n++
		reasons[t.Reason]++
	}
	if n > 0 {
		m.log.Log(logSource, "brain.thinking_dropped", "", map[string]any{"provider": ProviderAnthropic, "model": model, "dropped": n, "reasons": reasons})
	}
}

// anthropicFinish maps stop_reason onto the genai finish reason.
func anthropicFinish(s anthropic.BetaStopReason) genai.FinishReason {
	switch s {
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonToolUse, anthropic.BetaStopReasonStopSequence:
		return genai.FinishReasonStop
	case anthropic.BetaStopReasonMaxTokens:
		return genai.FinishReasonMaxTokens
	case anthropic.BetaStopReasonRefusal:
		return genai.FinishReasonSafety
	case "":
		return genai.FinishReasonUnspecified
	}
	return genai.FinishReasonOther
}

// ListModels implements ModelCatalog over GET /v1/models (free): every
// claude model, recommended, DefaultAnthropicModel first, then by id
// newest first.
func (m *AnthropicModel) ListModels(ctx context.Context) ([]ModelInfo, error) {
	pager := m.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var out []ModelInfo
	for pager.Next() {
		mi := pager.Current()
		if mi.ID == "" {
			continue
		}
		out = append(out, ModelInfo{
			Name: mi.ID, DisplayName: mi.DisplayName,
			InputTokenLimit: int(mi.MaxInputTokens), OutputTokenLimit: int(mi.MaxTokens),
			Methods: []string{"messages"}, Recommended: strings.HasPrefix(mi.ID, "claude-"),
		})
	}
	if err := pager.Err(); err != nil {
		return nil, m.mapError("", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Name == m.cfg.DefaultModel) != (out[j].Name == m.cfg.DefaultModel) {
			return out[i].Name == m.cfg.DefaultModel
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// anthropicTools renders the function declarations as strict custom
// tools (strictSchema).
func anthropicTools(tools []*genai.Tool) []anthropic.BetaToolUnionParam {
	var out []anthropic.BetaToolUnionParam
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			if d == nil {
				continue
			}
			schema := strictSchema(schemaOf(d.Parameters))
			in := anthropic.BetaToolInputSchemaParam{Properties: map[string]any{}}
			if props, ok := schema["properties"].(map[string]any); ok {
				in.Properties = props
			}
			if req, ok := schema["required"].([]string); ok {
				in.Required = req
			}
			in.ExtraFields = map[string]any{"additionalProperties": false}
			tool := anthropic.BetaToolParam{Name: d.Name, InputSchema: in, Strict: param.NewOpt(true)}
			if d.Description != "" {
				tool.Description = param.NewOpt(d.Description)
			}
			out = append(out, anthropic.BetaToolUnionParam{OfTool: &tool})
		}
	}
	return out
}

// strictSchema makes a JSON Schema (schemaOf's output) acceptable to
// strict tool use: additionalProperties false on every object, nullable
// as a ["T", "null"] type, and nothing strict mode refuses (numeric and
// length constraints) carried over.
func strictSchema(s map[string]any) map[string]any {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "properties":
			props := map[string]any{}
			if in, ok := v.(map[string]any); ok {
				for name, p := range in {
					ps, _ := p.(map[string]any)
					props[name] = strictSchema(ps)
				}
			}
			out[k] = props
		case "items":
			is, _ := v.(map[string]any)
			out[k] = strictSchema(is)
		case "type", "description", "enum", "required":
			out[k] = v
		}
	}
	if s["nullable"] == true {
		if t, ok := out["type"].(string); ok {
			out["type"] = []string{t, "null"}
		}
	}
	if out["type"] == "object" || out["properties"] != nil {
		if out["properties"] == nil {
			out["properties"] = map[string]any{}
		}
		out["additionalProperties"] = false
	}
	return out
}

// toolUseIDRE is what the API accepts as a tool_use id.
var toolUseIDRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// anthropicToolID is the id a call or result goes to the API with: its
// own when the API accepts it, else a stable one derived from it (and
// the call's name when it has no id), so every request of the turn
// carries the same bytes.
func anthropicToolID(id, name string) string {
	if toolUseIDRE.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id + "\x00" + name))
	return "toolu_sd_" + hex.EncodeToString(sum[:8])
}

// messagesOf renders the window for model. thinking false (the retry)
// replays no thinking block at all. See the design note, section 2:
//
//   - thinking blocks are replayed only from this provider and model's
//     own Raw on messages of the newest turn; every earlier turn goes
//     back as its text and tool_use blocks (a leading run of thinking
//     removed, which the API allows);
//   - the state text a current-turn message was produced after is put
//     back just before it, and the current state goes last;
//   - context that reached the window while a current-turn step was in
//     flight (Seq above the step's Raw.Seen) and so sits before its
//     assistant message moves after the step's results.
func (m *AnthropicModel) messagesOf(msgs []Message, model string, thinking bool) []amsg {
	cur := 0
	for _, msg := range msgs {
		cur = max(cur, msg.Turn)
	}
	own := func(msg Message) (anthropicRaw, bool) {
		var r anthropicRaw
		if len(msg.Raw) == 0 || msg.Provider != ProviderAnthropic || msg.Model != model {
			return r, false
		}
		if json.Unmarshal(msg.Raw, &r) != nil || len(r.Content) == 0 {
			return r, false
		}
		return r, true
	}
	current := func(msg Message) bool { return cur > 0 && msg.Turn == cur }

	var out []amsg
	user := func(b ablock) { out = append(out, amsg{role: "user", blocks: []ablock{b}}) }
	var pending []int // context lines not yet emitted
	var held []int    // late context, emitted after the step
	flushLines := func() {
		if len(pending) == 0 {
			return
		}
		lines := make([]string, len(pending))
		for i, mi := range pending {
			lines[i] = contextLine(msgs[mi])
		}
		user(ablock{p: anthropic.NewBetaTextBlock(strings.Join(lines, "\n"))})
		pending = nil
	}
	release := func() {
		pending = append(pending, held...)
		held = nil
	}
	inStep, rawCovers, afterResult := false, false, false
	var calls []ablock // pending rebuilt tool_use blocks
	callText := ""
	flushCalls := func() {
		if len(calls) == 0 {
			return
		}
		blocks := calls
		if callText != "" {
			blocks = append([]ablock{{p: anthropic.NewBetaTextBlock(callText)}}, calls...)
		}
		out = append(out, amsg{role: "assistant", blocks: blocks})
		calls, callText = nil, ""
	}
	// assistant emits an own message's blocks: with its thinking in the
	// current turn, without before. In the current turn, the context
	// that reached the window after its request was built is held back
	// until the step's results, and the state copy it followed is put
	// back just before it.
	assistant := func(r anthropicRaw, keepThinking bool) {
		if keepThinking && r.Seen > 0 {
			var stay []int
			for _, mi := range pending {
				if msgs[mi].Seq > r.Seen {
					held = append(held, mi)
				} else {
					stay = append(stay, mi)
				}
			}
			pending = stay
		}
		flushLines()
		if keepThinking && r.State != "" {
			user(ablock{p: anthropic.NewBetaTextBlock(r.State), state: true})
		}
		var blocks []ablock
		for _, b := range r.Content {
			t := blockType(b)
			if !keepThinking && (t == "thinking" || t == "redacted_thinking") {
				continue
			}
			blocks = append(blocks, ablock{raw: b})
		}
		out = append(out, amsg{role: "assistant", blocks: blocks})
	}

	for _, mi := range modelOrder(msgs) {
		msg := msgs[mi]
		stepItem := msg.Kind == KindToolCall || msg.Kind == KindToolResult
		if !stepItem && inStep {
			inStep, rawCovers, afterResult = false, false, false
			flushCalls()
			release()
		}
		switch msg.Kind {
		case KindToolCall:
			if inStep && afterResult {
				// The next step of the same turn: the late context held at
				// the last one goes after its results, as it was sent.
				flushCalls()
				release()
				rawCovers = false
			}
			inStep, afterResult = true, false
			if r, ok := own(msg); ok {
				flushCalls()
				assistant(r, thinking && current(msg))
				rawCovers = true
				continue
			}
			if len(msg.Raw) > 0 {
				rawCovers = false
			}
			if rawCovers || msg.ToolCall == nil {
				continue
			}
			flushLines()
			input := json.RawMessage("{}")
			if len(msg.ToolCall.Args) > 0 {
				if b, err := json.Marshal(msg.ToolCall.Args); err == nil {
					input = b
				}
			}
			calls = append(calls, ablock{p: anthropic.NewBetaToolUseBlock(anthropicToolID(msg.ToolCall.ID, msg.ToolCall.Name), input, msg.ToolCall.Name)})
			if msg.Content != "" {
				callText = msg.Content
			}
		case KindToolResult:
			afterResult = true
			flushCalls()
			content, isErr := msg.Content, false
			id := ""
			if msg.ToolResult != nil {
				id = anthropicToolID(msg.ToolResult.ID, msg.ToolResult.Name)
				if msg.ToolResult.Error != "" {
					content, isErr = msg.ToolResult.Error, true
				}
			}
			if content == "" {
				content = "(no output)"
			}
			user(ablock{p: anthropic.NewBetaToolResultBlock(id, content, isErr)})
		case KindReply:
			if r, ok := own(msg); ok && thinking && current(msg) {
				assistant(r, true)
				continue
			}
			flushLines()
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, amsg{role: "assistant", blocks: []ablock{{p: anthropic.NewBetaTextBlock(msg.Content)}}})
			}
		case KindState:
			flushLines()
			if msg.Content != "" {
				user(ablock{p: anthropic.NewBetaTextBlock(msg.Content), state: true})
			}
		default:
			pending = append(pending, mi)
		}
	}
	flushCalls()
	release()
	flushLines()
	return out
}

// blockType is a content block's "type".
func blockType(b json.RawMessage) string {
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b, &t)
	return t.Type
}

// amsgsFromContents renders genai contents as request messages
// (Generate): text, function calls as tool_use blocks, function
// responses as tool_result blocks, thought parts left out.
func amsgsFromContents(contents []*genai.Content) []amsg {
	var out []amsg
	for _, c := range contents {
		if c == nil {
			continue
		}
		role := "user"
		if c.Role == genai.RoleModel || c.Role == "assistant" {
			role = "assistant"
		}
		var blocks []ablock
		for _, p := range c.Parts {
			switch {
			case p == nil:
			case p.FunctionCall != nil:
				input := json.RawMessage("{}")
				if len(p.FunctionCall.Args) > 0 {
					if b, err := json.Marshal(p.FunctionCall.Args); err == nil {
						input = b
					}
				}
				blocks = append(blocks, ablock{p: anthropic.NewBetaToolUseBlock(anthropicToolID(p.FunctionCall.ID, p.FunctionCall.Name), input, p.FunctionCall.Name)})
			case p.FunctionResponse != nil:
				body, _ := json.Marshal(p.FunctionResponse.Response)
				out = append(out, amsg{role: "user", blocks: []ablock{{p: anthropic.NewBetaToolResultBlock(anthropicToolID(p.FunctionResponse.ID, p.FunctionResponse.Name), string(body), false)}}})
			case p.Text != "" && !p.Thought:
				blocks = append(blocks, ablock{p: anthropic.NewBetaTextBlock(p.Text)})
			}
		}
		if len(blocks) > 0 {
			out = append(out, amsg{role: role, blocks: blocks})
		}
	}
	return out
}
