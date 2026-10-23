package brain_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

const anthropicTestKey = "sk-ant-test-0000000000000000000000"

// TestMain clears every key and token the developer's shell exports, so
// no test can read, send or print a real one (the Anthropic SDK reads
// ANTHROPIC_* by default; the adapter must not).
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		secret := strings.Contains(u, "KEY") || strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET")
		for _, p := range []string{"GEMINI_", "VERTEX_", "OPENAI_", "DEEPSEEK_", "OPENROUTER_", "ANTHROPIC_", "SHEEPDOG_"} {
			secret = secret || strings.HasPrefix(u, p)
		}
		if secret {
			_ = os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

// fakeBlock is one content block the fake answers with: "thinking"
// (signed with the request's prefix), "text", "tool_use" or "fallback".
type fakeBlock struct {
	typ, text, id, name, input string
}

// fakeReply is one scripted answer: blocks and a stop reason, or an
// HTTP error (status, error type, message).
type fakeReply struct {
	blocks   []fakeBlock
	stop     string
	category string
	status   int
	errType  string
	errMsg   string
	// streamErr answers 200 and then an SSE error event of this type.
	streamErr string
}

// fakeAnthropic is an httptest Messages API that enforces preserved
// thinking itself: a thinking block's signature is a hash of the
// system prompt, the tools and every message before it (thinking
// blocks and cache_control left out), and a replayed block whose prefix
// changed is answered with the API's 400, or dropped and listed in
// input_transformations when the request sets drop_block and strict is
// off.
type fakeAnthropic struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	script  []fakeReply
	bodies  []map[string]any
	headers []http.Header
	// strict rejects a changed prefix even with drop_block (proves the
	// rendering is append-only, not rescued by the backstop).
	strict bool
	// noBetas answers any anthropic-beta header with the API's 400 (a
	// proxy that does not know them).
	noBetas bool
	dropped int
	// onRequest runs while request i is in flight (late context).
	onRequest func(i int)
	issued    map[string]bool
	// warmStatus answers warm-up requests with this HTTP error.
	warmStatus int
}

func newFakeAnthropic(t *testing.T, script ...fakeReply) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{t: t, script: script, issued: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) add(r ...fakeReply) {
	f.mu.Lock()
	f.script = append(f.script, r...)
	f.mu.Unlock()
}

func (f *fakeAnthropic) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeAnthropic) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func (f *fakeAnthropic) header(i int) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[i]
}

func (f *fakeAnthropic) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != anthropicTestKey || r.Header.Get("Authorization") != "" {
		f.t.Errorf("auth headers: x-api-key %q, Authorization %q", r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
	}
	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[`+
			`{"id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-09-01T00:00:00Z"},`+
			`{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-09-01T00:00:00Z"},`+
			`{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-09-01T00:00:00Z"}`+
			`],"has_more":false,"first_id":"claude-haiku-5-5","last_id":"claude-sonnet-5-5"}`)
		return
	}
	if r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		f.t.Errorf("request body: %v", err)
	}
	if mt, ok := body["max_tokens"].(float64); ok && mt == 0 {
		f.warm(w, r, body)
		return
	}
	f.mu.Lock()
	i := len(f.bodies)
	f.bodies = append(f.bodies, body)
	f.headers = append(f.headers, r.Header.Clone())
	hook, noBetas, strict := f.onRequest, f.noBetas, f.strict
	var reply fakeReply
	switch {
	case noBetas && r.Header.Get("anthropic-beta") != "":
		// Answered below without consuming the script.
	case len(f.script) > 0:
		reply, f.script = f.script[0], f.script[1:]
	default:
		reply = fakeReply{status: 500, errType: "api_error", errMsg: "script exhausted"}
	}
	f.mu.Unlock()
	if hook != nil {
		hook(i)
	}
	if noBetas && r.Header.Get("anthropic-beta") != "" {
		writeAnthropicError(w, 400, "invalid_request_error", "Unexpected value(s) `"+r.Header.Get("anthropic-beta")+"` for the `anthropic-beta` header.")
		return
	}
	if body["stream"] != true {
		f.t.Errorf("request %d not streamed", i)
	}
	drop := false
	if th, _ := body["thinking"].(map[string]any); th != nil {
		if bb, _ := th["block_binding"].(map[string]any); bb != nil {
			drop = bb["prefix_mismatch_behavior"] == "drop_block"
			if !strings.Contains(r.Header.Get("anthropic-beta"), "thinking-binding-controls-2026-08-01") {
				writeAnthropicError(w, 400, "invalid_request_error", "thinking.block_binding: Extra inputs are not permitted")
				return
			}
		}
	}
	var transformations []map[string]any
	msgs, _ := body["messages"].([]any)
	for mi, mm := range msgs {
		m := mm.(map[string]any)
		content, _ := m["content"].([]any)
		for bi, bb := range content {
			b := bb.(map[string]any)
			if b["type"] != "thinking" {
				continue
			}
			want := "sig:" + prefixHash(body, msgs[:mi])
			if b["signature"] == want {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d", mi, bi)
			if !drop || strict {
				writeAnthropicError(w, 400, "invalid_request_error", path+": Invalid `signature` in `thinking` block. The block is bound to a different conversation.")
				return
			}
			transformations = append(transformations, map[string]any{"type": "thinking_dropped", "path": path, "reason": "prefix_binding_mismatch"})
		}
	}
	f.mu.Lock()
	f.dropped += len(transformations)
	f.mu.Unlock()
	if reply.status != 0 {
		writeAnthropicError(w, reply.status, reply.errType, reply.errMsg)
		return
	}
	sig := "sig:" + prefixHash(body, msgs)
	f.writeStream(w, body, reply, sig, transformations)
}

// warm answers a max_tokens 0 request (the cache warm-up) the way the
// API does: no content, stop_reason max_tokens, the cache write in the
// usage. It does not consume the script.
func (f *fakeAnthropic) warm(w http.ResponseWriter, r *http.Request, body map[string]any) {
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.headers = append(f.headers, r.Header.Clone())
	status := f.warmStatus
	f.mu.Unlock()
	if body["stream"] == true {
		writeAnthropicError(w, 400, "invalid_request_error", "max_tokens: 0 is not supported with stream: true")
		return
	}
	if status != 0 {
		writeAnthropicError(w, status, "overloaded_error", "Overloaded")
		return
	}
	model, _ := body["model"].(string)
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{"id": "msg_warm", "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": "max_tokens", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 3, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 4000, "output_tokens": 0}})
	_, _ = w.Write(b)
}

func writeAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
	_, _ = w.Write(b)
}

// writeStream answers with the Messages API's SSE sequence.
func (f *fakeAnthropic) writeStream(w http.ResponseWriter, body map[string]any, reply fakeReply, sig string, transformations []map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	ev := func(typ string, v map[string]any) {
		v["type"] = typ
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	}
	if transformations == nil {
		transformations = []map[string]any{}
	}
	model, _ := body["model"].(string)
	ev("message_start", map[string]any{"message": map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "input_transformations": transformations,
		"usage": map[string]any{"input_tokens": 100, "cache_read_input_tokens": 900, "cache_creation_input_tokens": 50, "output_tokens": 1},
	}})
	if reply.streamErr != "" {
		ev("error", map[string]any{"error": map[string]any{"type": reply.streamErr, "message": "Overloaded"}})
		return
	}
	for i, b := range reply.blocks {
		switch b.typ {
		case "thinking":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "signature_delta", "signature": sig}})
			f.mu.Lock()
			f.issued[sig] = true
			f.mu.Unlock()
		case "text":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "text_delta", "text": b.text}})
		case "tool_use":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": map[string]any{}}})
			ev("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": b.input}})
		case "fallback":
			ev("content_block_start", map[string]any{"index": i, "content_block": map[string]any{"type": "fallback", "from": map[string]any{"model": "claude-opus-5-5"}, "to": map[string]any{"model": "claude-opus-5"}}})
		}
		ev("content_block_stop", map[string]any{"index": i})
	}
	stop := reply.stop
	if stop == "" {
		stop = "end_turn"
	}
	delta := map[string]any{"stop_reason": stop, "stop_sequence": nil}
	if reply.category != "" {
		delta["stop_details"] = map[string]any{"type": "refusal", "category": reply.category, "explanation": "declined"}
	}
	ev("message_delta", map[string]any{"delta": delta, "usage": map[string]any{"output_tokens": 30}})
	ev("message_stop", map[string]any{})
}

// prefixHash is the fake's binding of a thinking block: system, tools
// and the messages before it, without thinking blocks and cache_control.
func prefixHash(body map[string]any, msgs []any) string {
	clean := func(v any) any { return stripForHash(v) }
	var prefix []any
	for _, mm := range msgs {
		m := mm.(map[string]any)
		var content []any
		for _, bb := range m["content"].([]any) {
			b := bb.(map[string]any)
			if b["type"] == "thinking" || b["type"] == "redacted_thinking" {
				continue
			}
			content = append(content, clean(b))
		}
		prefix = append(prefix, map[string]any{"role": m["role"], "content": content})
	}
	data, _ := json.Marshal([]any{clean(body["system"]), clean(body["tools"]), prefix})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func stripForHash(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, vv := range x {
			if k == "cache_control" {
				continue
			}
			out[k] = stripForHash(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = stripForHash(vv)
		}
		return out
	}
	return v
}

func thinkingStep(blocks ...fakeBlock) fakeReply {
	return fakeReply{blocks: append([]fakeBlock{{typ: "thinking"}}, blocks...), stop: "tool_use"}
}

func toolUse(id, name, input string) fakeBlock {
	return fakeBlock{typ: "tool_use", id: id, name: name, input: input}
}

func answer(spoken string) fakeReply {
	return fakeReply{blocks: []fakeBlock{{typ: "thinking"}, {typ: "text", text: `{"spoken":"` + spoken + `","detail":""}`}}}
}

func newAnthropic(t *testing.T, url string, log *recLog) *AnthropicModel {
	t.Helper()
	cfg := AnthropicConfig{APIKey: anthropicTestKey, BaseURL: url}
	if log != nil {
		cfg.Log = log
	}
	m, err := NewAnthropicModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// anthropicHarness is a brain on the Anthropic adapter (the fake model
// stays registered as gemini for nothing).
func anthropicHarness(t *testing.T, f *fakeAnthropic, tweak func(*Config)) (*harness, *AnthropicModel) {
	t.Helper()
	h := newHarness(t, braintest.NewModel(), func(c *Config) {
		c.Provider, c.Model = ProviderAnthropic, DefaultAnthropicModel
		if tweak != nil {
			tweak(c)
		}
	})
	m := newAnthropic(t, f.srv.URL, h.log)
	if err := h.brain.AddProvider(ProviderAnthropic, m); err != nil {
		t.Fatal(err)
	}
	return h, m
}

// messages of request i, and the thinking signatures in them.
func requestThinking(body map[string]any) (sigs []string) {
	for _, mm := range body["messages"].([]any) {
		for _, bb := range mm.(map[string]any)["content"].([]any) {
			if b := bb.(map[string]any); b["type"] == "thinking" {
				sigs = append(sigs, b["signature"].(string))
			}
		}
	}
	return sigs
}

// A multi-step tool turn, with the state block refreshed on every step
// and a voice line arriving while a step is in flight, replays the
// turn's thinking blocks unchanged and passes a strict prefix check
// (no drop_block rescue); the next turn carries none of them, also
// after its tool results were pruned.
func TestAnthropicToolLoopPreservesThinking(t *testing.T) {
	f := newFakeAnthropic(t,
		thinkingStep(fakeBlock{typ: "text", text: "Reading the screen."}, toolUse("toolu_01", ToolReadScreen, `{"agent":"auth","lines":50}`)),
		thinkingStep(toolUse("toolu_02", ToolListAgents, `{}`)),
		thinkingStep(toolUse("toolu_03", ToolRecall, `{"query":"tests"}`)),
		answer("Auth is running the tests."),
	)
	f.strict = true
	h, _ := anthropicHarness(t, f, func(c *Config) { c.PruneAfterTurns = 1 })
	f.onRequest = func(i int) {
		switch i {
		case 0, 1:
			// Ingested while a step's request is in flight: it lands in
			// the transcript before that step's tool call, and later
			// steps follow it with no context in between.
			h.brain.Said(fmt.Sprintf("Checking %d.", i), true)
		}
	}
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil || rep.Spoken != "Auth is running the tests." {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if f.count() != 4 {
		t.Fatalf("requests = %d", f.count())
	}
	for i := 1; i <= 3; i++ {
		if got := len(requestThinking(f.body(i))); got != i {
			t.Errorf("step %d replayed %d thinking blocks, want %d", i+1, got, i)
		}
	}
	for _, s := range requestThinking(f.body(3)) {
		if !f.issued[s] {
			t.Errorf("replayed signature %q was never issued (edited)", s)
		}
	}
	// The tool results answer the API's ids.
	if !strings.Contains(mustJSONString(f.body(1)), `"tool_use_id":"toolu_01"`) || !strings.Contains(mustJSONString(f.body(2)), `"tool_use_id":"toolu_02"`) {
		t.Errorf("tool results do not carry the call ids")
	}
	// The voice line is in the window, after step 2's results.
	last := mustJSONString(f.body(3))
	for i, id := range []string{"toolu_01", "toolu_02"} {
		line := fmt.Sprintf(`Assistant said: \"Checking %d.\"`, i)
		if !strings.Contains(last, line) || strings.Index(last, line) < strings.Index(last, `"tool_use_id":"`+id+`"`) {
			t.Errorf("late voice line %d not after the step's results: %s", i, last)
		}
	}

	for range 2 {
		f.add(thinkingStep(toolUse("toolu_r", ToolRecall, `{"query":"tests"}`)), answer("Still running."))
		h.brain.Quiesce()
		if _, err := h.ask("and now?", "auth"); err != nil {
			t.Fatalf("next turn: %v", err)
		}
	}
	h.brain.Quiesce()
	n := f.count()
	if n != 8 {
		t.Fatalf("requests = %d", n)
	}
	// Each new turn's first request carries no thinking block.
	for _, i := range []int{4, 6} {
		if s := requestThinking(f.body(i)); len(s) != 0 {
			t.Errorf("request %d (a new turn) replayed %d earlier thinking blocks", i, len(s))
		}
	}
	if f.dropped != 0 || len(h.log.ofType("brain.thinking_dropped")) != 0 {
		t.Errorf("dropped %d blocks / %v: the rendering was not append-only", f.dropped, h.log.ofType("brain.thinking_dropped"))
	}
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The request shape: adaptive thinking with drop_block, effort medium
// on a user turn and low on an event turn, the betas and server-side
// fallbacks, strict tools, cache breakpoints on the system and on the
// last block before the state, and usage mapped for brain.step.
func TestAnthropicRequestShape(t *testing.T) {
	f := newFakeAnthropic(t, answer("Fine."), answer("Auth finished."))
	h, _ := anthropicHarness(t, f, nil)
	if _, err := h.ask("how is it going?", ""); err != nil {
		t.Fatal(err)
	}
	b := f.body(0)
	th := b["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["block_binding"].(map[string]any)["prefix_mismatch_behavior"] != "drop_block" {
		t.Errorf("thinking = %v", th)
	}
	if b["output_config"].(map[string]any)["effort"] != "medium" {
		t.Errorf("user turn effort = %v", b["output_config"])
	}
	if b["fallbacks"] != "default" || b["model"] != "claude-opus-5-5" {
		t.Errorf("fallbacks = %v model = %v", b["fallbacks"], b["model"])
	}
	if _, ok := b["tool_choice"]; ok {
		t.Errorf("tool_choice sent on an ordinary step: %v", b["tool_choice"])
	}
	beta := f.header(0).Get("anthropic-beta")
	if !strings.Contains(beta, "thinking-binding-controls-2026-08-01") || !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("anthropic-beta = %q", beta)
	}
	for _, tt := range b["tools"].([]any) {
		tool := tt.(map[string]any)
		schema := tool["input_schema"].(map[string]any)
		if tool["strict"] != true || schema["additionalProperties"] != false {
			t.Errorf("tool %v not strict: %v", tool["name"], schema)
		}
		if _, ok := tool["eager_input_streaming"]; ok {
			t.Errorf("eager_input_streaming on %v", tool["name"])
		}
	}
	sys := b["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Errorf("system = %v", sys)
	}
	if n := strings.Count(mustJSONString(b), `"cache_control"`); n != 2 {
		t.Errorf("cache_control markers = %d, want 2", n)
	}
	msgs := b["messages"].([]any)
	lastMsg := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	state := lastMsg[len(lastMsg)-1].(map[string]any)
	before := lastMsg[len(lastMsg)-2].(map[string]any)
	if !strings.HasPrefix(state["text"].(string), StateHeader) || state["cache_control"] != nil || before["cache_control"] == nil {
		t.Errorf("breakpoint not on the block before the state: %v / %v", before, state)
	}
	step := payload(h.log.ofType("brain.step")[0])
	if step["prompt_tokens"] != int32(1050) || step["cache_hit_tokens"] != int32(900) || step["completion_tokens"] != int32(30) {
		t.Errorf("brain.step usage = %v", step)
	}
	if step["served_model"] != "claude-opus-5-5" {
		t.Errorf("brain.step served_model = %v", step["served_model"])
	}

	// An event turn: effort low.
	h.brain.Quiesce()
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "finished"}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if got := f.body(1)["output_config"].(map[string]any)["effort"]; got != "low" {
		t.Errorf("event turn effort = %v", got)
	}
	if strings.Contains(mustJSONString(f.body(1)["tools"]), `"name":"`+ToolPrompt+`"`) {
		t.Errorf("event turn declared the write tools")
	}
}

// The answer-only step sends tool_choice none, with the tools declared.
func TestAnthropicAnswerOnlyStep(t *testing.T) {
	f := newFakeAnthropic(t, answer("Done."))
	h, _ := anthropicHarness(t, f, func(c *Config) { c.MaxSteps = 1 })
	if _, err := h.ask("status?", ""); err != nil {
		t.Fatal(err)
	}
	b := f.body(0)
	if tc, _ := b["tool_choice"].(map[string]any); tc["type"] != "none" || len(b["tools"].([]any)) == 0 {
		t.Errorf("tool_choice = %v tools = %d", b["tool_choice"], len(b["tools"].([]any)))
	}
}

// A refusal that the fallbacks did not serve fails the turn (the daemon
// answers for itself); no content is used.
func TestAnthropicRefusal(t *testing.T) {
	f := newFakeAnthropic(t, fakeReply{stop: "refusal", category: "cyber"})
	h, m := anthropicHarness(t, f, nil)
	_, err := h.ask("status?", "")
	if !errors.Is(err, ErrFallback) || !strings.Contains(err.Error(), "refused (category cyber)") {
		t.Errorf("err = %v", err)
	}
	// Direct: the error is ErrRefused.
	f.add(fakeReply{stop: "refusal"})
	_, err = m.Generate(context.Background(), DefaultAnthropicModel, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil)
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v", err)
	}
}

// After a mid-output fallback, only the text before the switch and
// everything after it count: the declined attempt's tool call is
// neither run nor stored.
func TestAnthropicFallbackBlock(t *testing.T) {
	f := newFakeAnthropic(t, fakeReply{blocks: []fakeBlock{
		{typ: "thinking"}, toolUse("toolu_x", ToolReadScreen, `{"agent":"auth"}`), {typ: "fallback"}, {typ: "text", text: `{"spoken":"Served.","detail":""}`},
	}})
	m := newAnthropic(t, f.srv.URL, nil)
	resp, raw, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, []Message{{Role: RoleUser, Kind: KindRequest, Content: "hi", Turn: 1, Seq: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.FunctionCalls()) != 0 || !strings.Contains(textOfResp(resp), "Served.") {
		t.Errorf("response = %v", textOfResp(resp))
	}
	if strings.Contains(string(raw), "toolu_x") || strings.Contains(string(raw), `"thinking"`) || strings.Contains(string(raw), `"fallback"`) {
		t.Errorf("raw kept the declined attempt: %s", raw)
	}
	// The fallback model served the response (message_start still names
	// the requested one): the response and the raw say so.
	if resp.ModelVersion != "claude-opus-5" || !strings.Contains(string(raw), `"served":"claude-opus-5"`) {
		t.Errorf("served model = %q, raw %s", resp.ModelVersion, raw)
	}
}

func textOfResp(r *genai.GenerateContentResponse) string {
	var b strings.Builder
	for _, c := range r.Candidates {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// HTTP errors map onto the brain's account problems and failover
// reasons; the key is in no error or log line.
func TestAnthropicErrors(t *testing.T) {
	cases := []struct {
		reply   fakeReply
		problem AccountProblem
		status  int
		rate    bool
	}{
		{fakeReply{status: 401, errType: "authentication_error", errMsg: "invalid x-api-key"}, ProblemInvalidKey, 401, false},
		{fakeReply{status: 402, errType: "billing_error", errMsg: "Your credit balance is too low to access the Anthropic API."}, ProblemBalance, 402, false},
		{fakeReply{status: 400, errType: "invalid_request_error", errMsg: "Your credit balance is too low to access the Anthropic API."}, ProblemBalance, 400, false},
		{fakeReply{status: 429, errType: "rate_limit_error", errMsg: "Number of requests has exceeded your rate limit"}, ProblemNone, 429, true},
		{fakeReply{status: 529, errType: "overloaded_error", errMsg: "Overloaded"}, ProblemNone, 529, false},
		{fakeReply{streamErr: "overloaded_error"}, ProblemNone, 529, false},
	}
	for _, c := range cases {
		log := &recLog{}
		f := newFakeAnthropic(t, c.reply)
		m := newAnthropic(t, f.srv.URL, log)
		_, err := m.Generate(context.Background(), DefaultAnthropicModel, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil)
		var api *APIError
		if !errors.As(err, &api) || api.Status != c.status || api.Provider != ProviderAnthropic {
			t.Errorf("%d %s: err = %v", c.status, c.reply.errType, err)
			continue
		}
		if got := AccountProblemOfErr(err); got != c.problem {
			t.Errorf("%d %s: problem = %q, want %q", c.status, c.reply.errType, got, c.problem)
		}
		if c.rate != errors.Is(err, ErrAllKeysRateLimited) || c.rate != (len(log.ofType("brain.rate_limited")) == 1) {
			t.Errorf("%d: rate limited = %v, events %v", c.status, errors.Is(err, ErrAllKeysRateLimited), log.ofType("brain.rate_limited"))
		}
		if f.count() != 1 {
			t.Errorf("%d: %d requests (the SDK must not retry)", c.status, f.count())
		}
		if strings.Contains(err.Error(), anthropicTestKey) || strings.Contains(mustJSONString(log.events), anthropicTestKey) {
			t.Errorf("%d: key leaked", c.status)
		}
	}
}

// A 401 during a turn marks the provider down with the right key named.
func TestAnthropicInvalidKeyAlert(t *testing.T) {
	f := newFakeAnthropic(t, fakeReply{status: 401, errType: "authentication_error", errMsg: "invalid x-api-key"})
	h, _ := anthropicHarness(t, f, nil)
	_, err := h.ask("status?", "")
	if err == nil {
		t.Fatal("no error")
	}
	a, ok := h.brain.AccountAlertFor(err)
	if !ok || a.Problem != ProblemInvalidKey || !strings.Contains(a.Fix, "ANTHROPIC_API_KEY") {
		t.Errorf("alert = %+v ok = %v", a, ok)
	}
	if strings.Contains(mustJSONString(h.log.events), anthropicTestKey) {
		t.Error("key in the log")
	}
}

// The model list comes from GET /v1/models, the default model first.
func TestAnthropicListModels(t *testing.T) {
	f := newFakeAnthropic(t)
	m := newAnthropic(t, f.srv.URL, nil)
	list, err := m.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, mi := range list {
		names = append(names, mi.Name)
		if !mi.Recommended || mi.InputTokenLimit != 1000000 {
			t.Errorf("model %+v", mi)
		}
	}
	if strings.Join(names, " ") != "claude-opus-5-5 claude-sonnet-5-5 claude-haiku-5-5" {
		t.Errorf("models = %v", names)
	}
}

// The SDK's environment defaults are off: ANTHROPIC_BASE_URL and
// ANTHROPIC_AUTH_TOKEN (set by Claude Code) neither redirect the daemon
// nor add a second auth header (the fake checks the headers).
func TestAnthropicIgnoresEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sentinel-token")
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-key")
	t.Setenv("HOME", t.TempDir())
	f := newFakeAnthropic(t, answer("ok"))
	m := newAnthropic(t, f.srv.URL, nil)
	if _, err := m.Generate(context.Background(), DefaultAnthropicModel, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Errorf("requests = %d", f.count())
	}
}

// A proxy that rejects the beta headers: the call is retried once
// without betas, fallbacks, block_binding and thinking blocks.
func TestAnthropicRetryWithoutBetas(t *testing.T) {
	f := newFakeAnthropic(t, answer("ok"))
	f.noBetas = true
	log := &recLog{}
	m := newAnthropic(t, f.srv.URL, log)
	if _, err := m.Generate(context.Background(), DefaultAnthropicModel, []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 || f.header(1).Get("anthropic-beta") != "" || f.body(1)["fallbacks"] != nil {
		t.Errorf("retry: %d requests, beta %q, fallbacks %v", f.count(), f.header(1).Get("anthropic-beta"), f.body(1)["fallbacks"])
	}
	if th := f.body(1)["thinking"].(map[string]any); th["block_binding"] != nil {
		t.Errorf("retry thinking = %v", th)
	}
}

// An edit the rendering cannot undo (a message taken out of the
// current turn's history) never yields a 400: drop_block drops the
// blocks (logged), and where drop_block is not honoured the call is
// retried once without thinking blocks.
func TestAnthropicEditedHistory(t *testing.T) {
	f := newFakeAnthropic(t, thinkingStep(toolUse("toolu_01", ToolListAgents, `{}`)))
	log := &recLog{}
	m := newAnthropic(t, f.srv.URL, log)
	window := []Message{
		{Role: RoleUser, Kind: KindRequest, Content: "first line", Turn: 3, Seq: 1, TS: time.Unix(0, 0)},
		{Role: RoleUser, Kind: KindRequest, Content: "what is going on?", Turn: 3, Seq: 2, TS: time.Unix(0, 0)},
	}
	_, raw, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, window, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := Message{Role: RoleAssistant, Kind: KindToolCall, Turn: 3, Seq: 3, Raw: raw, Provider: ProviderAnthropic, Model: DefaultAnthropicModel, ToolCall: &ToolCall{ID: "toolu_01", Name: ToolListAgents}}
	result := Message{Role: RoleTool, Kind: KindToolResult, Turn: 3, Seq: 4, Content: "auth: working", ToolResult: &ToolResult{ID: "toolu_01", Name: ToolListAgents}}
	edited := []Message{window[1], call, result} // "first line" removed

	f.add(answer("ok"))
	if _, _, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, edited, nil); err != nil {
		t.Fatalf("drop_block: %v", err)
	}
	if f.dropped != 1 || len(log.ofType("brain.thinking_dropped")) != 1 {
		t.Errorf("dropped %d, events %v", f.dropped, log.ofType("brain.thinking_dropped"))
	}

	f.strict = true
	f.add(answer("ok"), answer("ok"))
	if _, _, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, edited, nil); err != nil {
		t.Fatalf("strict: %v", err)
	}
	if n := f.count(); n != 4 || len(requestThinking(f.body(3))) != 0 {
		t.Errorf("strict retry: %d requests, thinking %v", n, requestThinking(f.body(n-1)))
	}

	// Unedited, the same window passes the strict check.
	f.add(answer("ok"))
	if _, _, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, append(append([]Message{}, window...), call, result), nil); err != nil {
		t.Fatalf("unedited: %v", err)
	}
	if n := f.count(); n != 5 || len(requestThinking(f.body(4))) != 1 {
		t.Errorf("unedited: %d requests, thinking %d", n, len(requestThinking(f.body(4))))
	}
}

// Another provider's tool call (a failover step) is rebuilt as a
// tool_use with an id the API accepts, the same on every request, and
// its result carries the same id.
func TestAnthropicForeignToolCall(t *testing.T) {
	f := newFakeAnthropic(t, answer("ok"))
	m := newAnthropic(t, f.srv.URL, nil)
	window := []Message{
		{Role: RoleUser, Kind: KindRequest, Content: "q", Turn: 1, Seq: 1},
		{Role: RoleAssistant, Kind: KindToolCall, Turn: 1, Seq: 2, Provider: ProviderGemini, Model: "gemini-3.5-flash", Raw: json.RawMessage(`{"parts":[]}`), ToolCall: &ToolCall{ID: "bt 1/1", Name: ToolListAgents}},
		{Role: RoleTool, Kind: KindToolResult, Turn: 1, Seq: 3, Content: "", ToolResult: &ToolResult{ID: "bt 1/1", Name: ToolListAgents}},
	}
	if _, _, err := m.GenerateMessages(context.Background(), DefaultAnthropicModel, window, nil); err != nil {
		t.Fatal(err)
	}
	s := mustJSONString(f.body(0)["messages"])
	if !strings.Contains(s, `{"id":"toolu_sd_`) || strings.Count(s, "toolu_sd_") != 2 || strings.Contains(s, "bt 1/1") {
		t.Errorf("messages = %s", s)
	}
}

// cachePrefixes are the message-cache prefixes of a request: for each
// block with cache_control, the role and blocks up to it (cache_control
// left out), as JSON.
func cachePrefixes(body map[string]any) []string {
	var seen []any
	var out []string
	for _, mm := range body["messages"].([]any) {
		m := mm.(map[string]any)
		for _, bb := range m["content"].([]any) {
			b := map[string]any{}
			for k, v := range bb.(map[string]any) {
				if k != "cache_control" {
					b[k] = v
				}
			}
			seen = append(seen, []any{m["role"], b})
			if bb.(map[string]any)["cache_control"] != nil {
				out = append(out, mustJSONString(seen))
			}
		}
	}
	return out
}

// Across turns, a message breakpoint of each new turn sits where an
// earlier request wrote one, on a prefix that pruning and the dropped
// thinking left unchanged, so the turn reads it from the cache (not
// only the tools and system); never more than 4 breakpoints.
func TestAnthropicCacheBreakpointsStableAcrossTurns(t *testing.T) {
	f := newFakeAnthropic(t)
	f.strict = true
	h, _ := anthropicHarness(t, f, nil)
	const turns = 7
	firsts := []int{}
	for i := range turns {
		f.add(thinkingStep(toolUse(fmt.Sprintf("toolu_%d", i), ToolRecall, `{"query":"tests"}`)), answer("Still running."))
		h.brain.Quiesce()
		firsts = append(firsts, f.count())
		if _, err := h.ask(fmt.Sprintf("and now, %d?", i), "auth"); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	h.brain.Quiesce()
	written := map[string]bool{}
	hits := 0
	for i := range f.count() {
		b := f.body(i)
		if n := strings.Count(mustJSONString(b), `"cache_control"`); n > 4 {
			t.Errorf("request %d has %d breakpoints, the API allows 4", i, n)
		}
		pre := cachePrefixes(b)
		if slices.Contains(firsts, i) {
			hit := false
			for _, p := range pre {
				hit = hit || written[p]
			}
			if hit {
				hits++
			} else if i >= firsts[4] {
				t.Errorf("request %d (a new turn's first) matches no earlier breakpoint", i)
			}
		}
		for _, p := range pre {
			written[p] = true
		}
	}
	if hits < turns-4 {
		t.Errorf("cross-turn cache hits = %d, want at least %d", hits, turns-4)
	}
	if f.dropped != 0 {
		t.Errorf("dropped %d thinking blocks", f.dropped)
	}
}

// The cache warm-up sends the prefix the first step of a turn sends:
// for an event turn and then a user turn, the same system text (with
// the roster's aliases), tools, thinking, effort, model, fallbacks and
// betas, byte for byte, with max_tokens 0, not streamed, and no
// breakpoint on its placeholder line. Otherwise the cache it writes is
// never read.
func TestAnthropicWarmCacheMatchesTurns(t *testing.T) {
	f := newFakeAnthropic(t, answer("Auth finished."), answer("Fine."))
	h, _ := anthropicHarness(t, f, nil)
	h.brain.WarmCache()
	h.brain.Quiesce()
	if f.count() != 2 {
		t.Fatalf("warm-up requests = %d, want 2", f.count())
	}
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "finished"}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	h.brain.Quiesce()
	if _, err := h.ask("how is it going?", ""); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int{{0, 2}, {1, 3}} {
		warm, turn := f.body(pair[0]), f.body(pair[1])
		if warm["max_tokens"] != float64(0) || warm["stream"] != nil {
			t.Errorf("warm-up %d: max_tokens %v, stream %v", pair[0], warm["max_tokens"], warm["stream"])
		}
		if strings.Contains(mustJSONString(warm["messages"]), "cache_control") {
			t.Errorf("warm-up %d: breakpoint on the placeholder: %s", pair[0], mustJSONString(warm["messages"]))
		}
		for _, k := range []string{"system", "tools", "thinking", "output_config", "model", "fallbacks"} {
			if a, b := mustJSONString(warm[k]), mustJSONString(turn[k]); a != b {
				t.Errorf("request %d vs %d: %s differs:\n%s\n%s", pair[0], pair[1], k, a, b)
			}
		}
		if a, b := f.header(pair[0]).Get("anthropic-beta"), f.header(pair[1]).Get("anthropic-beta"); a != b {
			t.Errorf("request %d vs %d: anthropic-beta %q vs %q", pair[0], pair[1], a, b)
		}
	}
	sys := mustJSONString(f.body(0)["system"])
	if !strings.Contains(sys, "auth") || !strings.Contains(sys, "docs") {
		t.Errorf("warm-up system text without the roster's aliases: %.300s", sys)
	}
	if got := f.body(0)["output_config"].(map[string]any)["effort"]; got != "low" {
		t.Errorf("event warm-up effort = %v", got)
	}
	if got := f.body(1)["output_config"].(map[string]any)["effort"]; got != "medium" {
		t.Errorf("utterance warm-up effort = %v", got)
	}
	warm := h.log.ofType("brain.warmup")
	if len(warm) != 2 {
		t.Fatalf("brain.warmup events = %d", len(warm))
	}
	for i, e := range warm {
		p := payload(e)
		if p["error"] != nil || p["cache_write_tokens"] != int64(4000) || p["provider"] != ProviderAnthropic {
			t.Errorf("brain.warmup %d = %v", i, p)
		}
	}
	if got := payload(warm[0])["kind"]; got != string(KindAnnounce) {
		t.Errorf("first warm-up kind = %v", got)
	}
	if n := h.log.count("brain.step"); n != 2 {
		t.Errorf("brain.step events = %d, want 2 (the warm-up is not a step)", n)
	}
}

// A failed warm-up is logged and changes nothing: the turn runs.
func TestAnthropicWarmCacheFailureIsHarmless(t *testing.T) {
	f := newFakeAnthropic(t, answer("Fine."))
	f.warmStatus = 529
	h, _ := anthropicHarness(t, f, nil)
	h.brain.WarmCache()
	h.brain.Quiesce()
	warm := h.log.ofType("brain.warmup")
	if len(warm) != 2 || payload(warm[0])["error"] == nil {
		t.Fatalf("brain.warmup = %v", warm)
	}
	if _, err := h.ask("how is it going?", ""); err != nil {
		t.Fatal(err)
	}
}

// Providers without a prompt cache to write ahead do nothing.
func TestWarmCacheOtherProviders(t *testing.T) {
	h := newHarness(t, braintest.NewModel(), nil)
	h.brain.WarmCache()
	h.brain.Quiesce()
	if n := h.log.count("brain.warmup"); n != 0 {
		t.Errorf("brain.warmup on a provider without CacheWarmer: %d", n)
	}
}
