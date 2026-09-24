package brain_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Golden files of the DeepSeek adapter under testdata/brain (-update
// rewrites them, like the sample run).
const (
	toolsGoldenPath   = "../../testdata/brain/deepseek-tools.golden.json"
	requestGoldenPath = "../../testdata/brain/deepseek-request.golden.json"
	// responseFixturePath is a chat completion with two tool calls and
	// reasoning, in the API's shape.
	responseFixturePath = "../../testdata/brain/deepseek-response-toolcalls.json"
)

// compatServer is an httptest endpoint that records every request body
// and answers from a script of raw responses (status + body).
type compatServer struct {
	srv *httptest.Server
	mu  sync.Mutex
	// requests are the decoded bodies of every /chat/completions call.
	requests []map[string]any
	script   []compatReply
	paths    []string
}

type compatReply struct {
	status int
	body   string
	header http.Header
}

func newCompatServer(t *testing.T, script ...compatReply) *compatServer {
	t.Helper()
	c := &compatServer{script: script}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.paths = append(c.paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer k-test" {
			http.Error(w, `{"error":{"message":"Authentication Fails"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			data, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(data, &body); err != nil {
				http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
				return
			}
			c.requests = append(c.requests, body)
		}
		if len(c.script) == 0 {
			http.Error(w, `{"error":{"message":"unscripted call"}}`, http.StatusInternalServerError)
			return
		}
		rep := c.script[0]
		c.script = c.script[1:]
		for k, v := range rep.header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *compatServer) add(replies ...compatReply) {
	c.mu.Lock()
	c.script = append(c.script, replies...)
	c.mu.Unlock()
}

func (c *compatServer) request(i int) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[i]
}

func (c *compatServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// ok is a 200 chat completion with one text answer.
func ok(text string) compatReply {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text, "reasoning_content": "thinking about " + text[:min(len(text), 12)]}}},
		"usage":   map[string]any{"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150, "completion_tokens_details": map[string]any{"reasoning_tokens": 20}},
	})
	return compatReply{status: 200, body: string(body)}
}

// toolCalls is a 200 chat completion asking for the given calls (id,
// name, args).
func toolCalls(reasoning string, calls ...[3]string) compatReply {
	var tcs []map[string]any
	for _, c := range calls {
		tcs = append(tcs, map[string]any{"id": c[0], "type": "function", "function": map[string]any{"name": c[1], "arguments": c[2]}})
	}
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "", "reasoning_content": reasoning, "tool_calls": tcs}}},
		"usage":   map[string]any{"prompt_tokens": 200, "completion_tokens": 40, "total_tokens": 240},
	})
	return compatReply{status: 200, body: string(body)}
}

func newCompat(t *testing.T, url string, thinking bool, effort string, log *recLog) *OpenAICompatModel {
	t.Helper()
	cfg := OpenAICompatConfig{Provider: ProviderDeepSeek, BaseURL: url + "/", APIKey: "k-test", Thinking: &thinking, ReasoningEffort: effort}
	if log != nil {
		cfg.Log = log
	}
	m, err := NewOpenAICompatModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// golden compares got with the file at path, rewriting it with -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the generated request (run with -update if it changed on purpose):\n%s", path, got)
	}
}

// NewOpenAICompatModel validates its config and never accepts an
// unknown reasoning effort.
func TestOpenAICompatConfig(t *testing.T) {
	for _, cfg := range []OpenAICompatConfig{
		{BaseURL: "http://x", APIKey: "k"},
		{Provider: "deepseek", APIKey: "k"},
		{Provider: "deepseek", BaseURL: "http://x"},
		{Provider: "deepseek", BaseURL: "http://x", APIKey: "k", ReasoningEffort: "medium"},
	} {
		if _, err := NewOpenAICompatModel(cfg); err == nil {
			t.Errorf("config %+v accepted", cfg)
		}
	}
	m, err := NewOpenAICompatModel(OpenAICompatConfig{Provider: " DeepSeek ", BaseURL: "http://x", APIKey: "k", ReasoningEffort: "max"})
	if err != nil || m.Provider() != ProviderDeepSeek {
		t.Errorf("model = %v %v", m, err)
	}
}

// The request the adapter sends for a window: the system message, the
// brain's tools as JSON Schema (the golden tools file), user lines
// merged, an own two-call step replayed from Raw (ids included, its
// second Raw-less message skipped; its reasoning_content dropped, the
// step belonging to an earlier turn — docs/iteration-2.md WP5), tool
// messages with the calls' ids, a reply of an earlier turn without its
// reasoning_content, a step by another model rebuilt from the neutral
// fields without reasoning, an answer-only step as tool_choice none, and
// DeepSeek's thinking fields.
func TestOpenAICompatRequestGolden(t *testing.T) {
	srv := newCompatServer(t, ok(`{"spoken":"ok","detail":""}`), ok(`{"spoken":"ok","detail":""}`))
	m := newCompat(t, srv.srv.URL, true, "low", nil)
	ts := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	ownRaw := json.RawMessage(`{"role":"assistant","content":"","reasoning_content":"I should look at the screen and the roster.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_screen","arguments":"{\"agent\":\"auth\"}"}},{"id":"call_2","type":"function","function":{"name":"list_agents","arguments":"{}"}}]}`)
	replyRaw := json.RawMessage(`{"role":"assistant","content":"{\"spoken\":\"Auth is running the tests.\",\"detail\":\"\"}","reasoning_content":"The screen shows go test running."}`)
	foreignRaw := json.RawMessage(`{"parts":[{"functionCall":{"name":"recall","args":{"query":"tests"}},"thoughtSignature":"c2ln"}],"role":"model"}`)
	msgs := []Message{
		{TS: ts, Role: RoleUser, Kind: KindWorkerEvent, Content: "auth: ? -> working", Agent: "auth"},
		{TS: ts, Role: RoleUser, Kind: KindCEOUtterance, Content: "what is auth doing?"},
		{TS: ts, Role: RoleUser, Kind: KindRequest, Content: `Request from the assistant (ask): question="what is auth doing?" agent="auth"`, Turn: 1},
		{TS: ts, Role: RoleAssistant, Kind: KindToolCall, Turn: 1, Provider: ProviderDeepSeek, Model: "deepseek-flash", Raw: ownRaw, ToolCall: &ToolCall{ID: "call_1", Name: ToolReadScreen, Args: map[string]any{"agent": "auth"}}},
		{TS: ts, Role: RoleAssistant, Kind: KindToolCall, Turn: 1, Provider: ProviderDeepSeek, Model: "deepseek-flash", ToolCall: &ToolCall{ID: "call_2", Name: ToolListAgents, Args: map[string]any{}}},
		{TS: ts, Role: RoleTool, Kind: KindToolResult, Turn: 1, Content: "<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\n$ go test ./...\n<<<END_WORKER_OUTPUT>>>", ToolResult: &ToolResult{ID: "call_1", Name: ToolReadScreen}},
		{TS: ts, Role: RoleTool, Kind: KindToolResult, Turn: 1, Content: `[{"alias":"auth","kind":"codex","state":"working"}]`, ToolResult: &ToolResult{ID: "call_2", Name: ToolListAgents}},
		{TS: ts, Role: RoleAssistant, Kind: KindReply, Turn: 1, Provider: ProviderDeepSeek, Model: "deepseek-flash", Raw: replyRaw, Content: `{"spoken":"Auth is running the tests.","detail":""}`},
		{TS: ts, Role: RoleSystem, Kind: KindSystem, Content: "model switched from gemini/gemini-3.5-flash to deepseek/deepseek-flash"},
		{TS: ts, Role: RoleUser, Kind: KindRequest, Content: `Request from the assistant (ask): question="did it pass?" agent="auth"`, Turn: 2},
		{TS: ts, Role: RoleAssistant, Kind: KindToolCall, Turn: 2, Provider: ProviderGemini, Model: "gemini-3.5-flash", Raw: foreignRaw, ToolCall: &ToolCall{ID: "bt-2-3", Name: ToolRecall, Args: map[string]any{"query": "tests"}}},
		{TS: ts, Role: RoleTool, Kind: KindToolResult, Turn: 2, ToolResult: &ToolResult{ID: "bt-2-3", Name: ToolRecall, Error: "nothing in the record matches"}},
		{TS: ts, Role: RoleAssistant, Kind: KindReply, Turn: 2, Provider: ProviderGemini, Model: "gemini-3.5-flash", Raw: json.RawMessage(`{"parts":[{"text":"x"}]}`), Content: `{"spoken":"Not yet.","detail":""}`},
		{TS: ts, Role: RoleUser, Kind: KindRequest, Content: "Time is up: answer now.", Turn: 3},
	}
	cfg := &genai.GenerateContentConfig{SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "You are the SVP."}}}, Tools: ToolDeclarations(false)}
	if _, _, err := m.GenerateMessages(context.Background(), "deepseek-flash", msgs, cfg); err != nil {
		t.Fatal(err)
	}
	forced := *cfg
	forced.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}
	if _, _, err := m.GenerateMessages(context.Background(), "deepseek-flash", msgs[:3], &forced); err != nil {
		t.Fatal(err)
	}
	req := srv.request(0)
	tools, _ := json.MarshalIndent(req["tools"], "", "  ")
	golden(t, toolsGoldenPath, append(tools, '\n'))
	delete(req, "tools")
	body, _ := json.MarshalIndent(req, "", "  ")
	golden(t, requestGoldenPath, append(body, '\n'))
	if second := srv.request(1); second["tool_choice"] != "none" || second["tools"] == nil {
		t.Errorf("answer-only step: tool_choice = %v tools = %v", second["tool_choice"], second["tools"] != nil)
	}
	if req["tool_choice"] != nil {
		t.Errorf("tool_choice sent on a normal step: %v", req["tool_choice"])
	}
}

// Thinking off sends the disabled type and no effort; nil leaves both
// out.
func TestOpenAICompatThinkingFields(t *testing.T) {
	srv := newCompatServer(t, ok("a"), ok("b"))
	off := newCompat(t, srv.srv.URL, false, "high", nil)
	if _, err := off.Generate(context.Background(), "deepseek-flash", []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	none, err := NewOpenAICompatModel(OpenAICompatConfig{Provider: "other", BaseURL: srv.srv.URL, APIKey: "k-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Generate(context.Background(), "m", []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}}}, nil); err != nil {
		t.Fatal(err)
	}
	r0, r1 := srv.request(0), srv.request(1)
	if th, _ := r0["thinking"].(map[string]any); th["type"] != "disabled" || r0["reasoning_effort"] != nil {
		t.Errorf("thinking off: %v %v", r0["thinking"], r0["reasoning_effort"])
	}
	if r1["thinking"] != nil || r1["reasoning_effort"] != nil || r1["messages"].([]any)[0].(map[string]any)["content"] != "hi" {
		t.Errorf("thinking unset: %v", r1)
	}
}

// A response with two tool calls becomes two function call parts with
// the API's ids and decoded arguments, the usage is mapped (reasoning
// tokens as thoughts), the finish reason too, and the assistant message
// comes back as raw to replay.
func TestOpenAICompatResponseParsing(t *testing.T) {
	data, err := os.ReadFile(responseFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	srv := newCompatServer(t, compatReply{status: 200, body: string(data)})
	m := newCompat(t, srv.srv.URL, true, "low", nil)
	resp, raw, err := m.GenerateMessages(context.Background(), "deepseek-flash", []Message{{Role: RoleUser, Kind: KindRequest, Content: "q"}}, &genai.GenerateContentConfig{Tools: ToolDeclarations(false)})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.FunctionCalls()
	if len(calls) != 2 || calls[0].ID != "call_0_9f1e" || calls[0].Name != ToolPrompt || calls[0].Args["agent"] != "auth" || calls[0].Args["text"] != "run the tests" || calls[1].ID != "call_1_a2b3" || calls[1].Name != ToolPromise || calls[1].Args["event"] != "done" || calls[1].Args["due_in_minutes"] != float64(0) {
		t.Errorf("calls = %+v", calls)
	}
	if len(resp.Candidates[0].Content.Parts) != 2 || resp.Candidates[0].FinishReason != genai.FinishReasonStop {
		t.Errorf("parts = %d finish = %q", len(resp.Candidates[0].Content.Parts), resp.Candidates[0].FinishReason)
	}
	u := resp.UsageMetadata
	if u == nil || u.PromptTokenCount != 2211 || u.CandidatesTokenCount != 187 || u.ThoughtsTokenCount != 121 || u.CachedContentTokenCount != 2048 || u.TotalTokenCount != 2398 {
		t.Errorf("usage = %+v", u)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil || stored["reasoning_content"] != "The CEO wants the tests run and to be told when they finish: prompt, then promise." || len(stored["tool_calls"].([]any)) != 2 || stored["role"] != "assistant" {
		t.Errorf("raw = %s (%v)", raw, err)
	}
	// A plain answer carries its reasoning as raw too; one without
	// reasoning nor calls has nothing to replay.
	srv.add(ok(`{"spoken":"done"}`), compatReply{status: 200, body: `{"choices":[{"message":{"role":"assistant","content":"plain"},"finish_reason":"length"}]}`})
	resp, raw, err = m.GenerateMessages(context.Background(), "deepseek-flash", nil, nil)
	if err != nil || resp.Text() != `{"spoken":"done"}` || len(raw) == 0 {
		t.Errorf("answer = %q raw = %s err = %v", resp.Text(), raw, err)
	}
	resp, raw, err = m.GenerateMessages(context.Background(), "deepseek-flash", nil, nil)
	if err != nil || resp.Text() != "plain" || raw != nil || resp.Candidates[0].FinishReason != genai.FinishReasonMaxTokens || resp.UsageMetadata != nil {
		t.Errorf("plain = %q raw = %s err = %v finish = %q", resp.Text(), raw, err, resp.Candidates[0].FinishReason)
	}
	// Arguments that are not a JSON object fail the call, visibly.
	srv.add(compatReply{status: 200, body: `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"read_screen","arguments":"not json"}}]}}]}`})
	if _, _, err := m.GenerateMessages(context.Background(), "deepseek-flash", nil, nil); err == nil || !strings.Contains(err.Error(), "arguments are not a JSON object") {
		t.Errorf("bad arguments: %v", err)
	}
}

// HTTP errors: a 402 (insufficient balance) and a 5xx surface the
// API's message verbatim as an *APIError; a 429 is that plus
// ErrAllKeysRateLimited (one key, nothing to rotate to) and a
// brain.rate_limited event with the Retry-After.
func TestOpenAICompatErrors(t *testing.T) {
	srv := newCompatServer(t,
		compatReply{status: 402, body: `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`},
		compatReply{status: 503, body: "<html>Server Overloaded</html>"},
		compatReply{status: 429, body: `{"error":{"message":"Rate limit reached for requests"}}`, header: http.Header{"Retry-After": []string{"12"}}},
		compatReply{status: 400, body: `{"error":{"message":"The reasoning_content of the assistant message is missing"}}`},
	)
	log := &recLog{}
	m := newCompat(t, srv.srv.URL, true, "low", log)
	gen := func() error {
		_, _, err := m.GenerateMessages(context.Background(), "deepseek-flash", []Message{{Role: RoleUser, Kind: KindRequest, Content: "q"}}, nil)
		return err
	}
	var api *APIError
	err := gen()
	if !errors.As(err, &api) || api.Status != 402 || api.Message != "Insufficient Balance" || err.Error() != "deepseek: HTTP 402: Insufficient Balance" || errors.Is(err, ErrAllKeysRateLimited) {
		t.Errorf("402 = %v", err)
	}
	if err := gen(); !errors.As(err, &api) || api.Status != 503 || api.Message != "<html>Server Overloaded</html>" {
		t.Errorf("503 = %v", err)
	}
	err = gen()
	if !errors.Is(err, ErrAllKeysRateLimited) || !errors.As(err, &api) || api.Status != 429 || api.RetryAfter != 12*time.Second || !strings.Contains(err.Error(), "Rate limit reached") {
		t.Errorf("429 = %v", err)
	}
	if rl := log.ofType("brain.rate_limited"); len(rl) != 1 || payload(rl[0])["retry_after_ms"] != int64(12000) || payload(rl[0])["keys"] != 1 || payload(rl[0])["provider"] != "deepseek" || payload(rl[0])["model"] != "deepseek-flash" {
		t.Errorf("brain.rate_limited = %v", rl)
	}
	if err := gen(); !errors.As(err, &api) || api.Status != 400 || !strings.Contains(err.Error(), "reasoning_content") {
		t.Errorf("400 = %v", err)
	}
	// The context bounds the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.GenerateMessages(ctx, "deepseek-flash", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
}

// ListModels maps GET /models onto the catalog, every model recommended.
func TestOpenAICompatListModels(t *testing.T) {
	srv := newCompatServer(t, compatReply{status: 200, body: `{"object":"list","data":[{"id":"deepseek-v4-pro","object":"model","owned_by":"deepseek"},{"id":"deepseek-flash","object":"model","owned_by":"deepseek"}]}`}, compatReply{status: 401, body: `{"error":{"message":"Authentication Fails"}}`})
	m := newCompat(t, srv.srv.URL, true, "low", nil)
	list, err := m.ListModels(context.Background())
	if err != nil || len(list) != 2 || list[0].Name != "deepseek-flash" || !list[0].Recommended || list[1].Name != "deepseek-v4-pro" || !list[1].Recommended || list[0].Methods[0] != "chat/completions" {
		t.Errorf("list = %+v err = %v", list, err)
	}
	var api *APIError
	if _, err := m.ListModels(context.Background()); !errors.As(err, &api) || api.Status != 401 {
		t.Errorf("401 = %v", err)
	}
	if strings.Join(srv.paths, ",") != "GET /models,GET /models" {
		t.Errorf("paths = %v", srv.paths)
	}
}

// Generate over Gemini contents (the compaction summary) sends text as
// user/assistant messages and the system instruction first.
func TestOpenAICompatGenerateContents(t *testing.T) {
	srv := newCompatServer(t, ok("summary"))
	m := newCompat(t, srv.srv.URL, true, "low", nil)
	resp, err := m.Generate(context.Background(), "deepseek-flash", []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "Summarise the following"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "earlier"}, {FunctionCall: &genai.FunctionCall{Name: "recall", Args: map[string]any{"query": "x"}}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "recall", Response: map[string]any{"output": "y"}}}}},
	}, &genai.GenerateContentConfig{SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "sys"}}}})
	if err != nil || resp.Text() != "summary" || resp.UsageMetadata.PromptTokenCount != 120 {
		t.Fatalf("resp = %v err = %v", resp, err)
	}
	msgs := srv.request(0)["messages"].([]any)
	roles := make([]string, 0, len(msgs))
	for _, mm := range msgs {
		roles = append(roles, mm.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, " ") != "system user assistant tool" || srv.request(0)["tools"] != nil {
		t.Errorf("messages = %v", msgs)
	}
	if tc := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any); tc["id"] != "recall" || msgs[3].(map[string]any)["tool_call_id"] != "recall" {
		t.Errorf("tool call ids = %v / %v", tc["id"], msgs[3])
	}
}

// The brain loop over the adapter: a two-call step is executed as two
// tools with the API's ids, recorded as calls-then-results, and the
// next request replays one assistant message (reasoning_content and
// both tool_calls) followed by two tool messages with matching ids and
// the state block; the reply's reasoning is stored but not replayed on
// the next turn (only the current turn's is, docs/iteration-2.md WP5);
// tokens reach brain.step and brain.turn. After a switch to another
// provider the same step is rebuilt as plain function calls for Gemini;
// switched back, the DeepSeek adapter gets the Gemini step rebuilt and
// its own replayed from Raw, both without reasoning (earlier turns).
func TestBrainOverOpenAICompat(t *testing.T) {
	srv := newCompatServer(t,
		toolCalls("Read the screen and check the roster.", [3]string{"call_a", ToolReadScreen, `{"agent":"auth","lines":50}`}, [3]string{"call_b", ToolListAgents, `{}`}),
		ok(`{"spoken":"Auth is running the tests; docs is idle.","detail":"go test is running"}`),
	)
	ds := newCompat(t, srv.srv.URL, true, "low", nil)
	fake := braintest.NewModel()
	h := newHarness(t, fake, func(c *Config) { c.Provider = ProviderDeepSeek; c.Model = "deepseek-flash"; c.Now = time.Now })
	// The harness registered the fake under deepseek: re-register the
	// adapter there and the fake as gemini.
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	if err := h.brain.AddProvider(ProviderGemini, fake); err != nil {
		t.Fatal(err)
	}
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil || rep.Spoken != "Auth is running the tests; docs is idle." {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if got := joinKinds(h.kinds()); got != "request brain_tool_call brain_tool_call brain_tool_result brain_tool_result reply" {
		t.Errorf("transcript = %s", got)
	}
	msgs := h.brain.Transcript().Messages()
	if msgs[1].ToolCall.ID != "call_a" || msgs[2].ToolCall.ID != "call_b" || msgs[3].ToolResult.ID != "call_a" || msgs[4].ToolResult.ID != "call_b" || len(msgs[1].Raw) == 0 || len(msgs[2].Raw) != 0 || msgs[1].Provider != ProviderDeepSeek || msgs[1].Model != "deepseek-flash" {
		t.Errorf("ids / raw: %+v %+v %+v %+v", msgs[1].ToolCall, msgs[2].ToolCall, msgs[3].ToolResult, msgs[4].ToolResult)
	}
	if len(msgs[5].Raw) == 0 || msgs[5].Provider != ProviderDeepSeek || msgs[5].Kind != KindReply {
		t.Errorf("reply raw not stored: %+v", msgs[5])
	}
	second := srv.request(1)["messages"].([]any)
	var shape []string
	for _, mm := range second {
		m := mm.(map[string]any)
		s := m["role"].(string)
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				s += "+" + tc.(map[string]any)["id"].(string)
			}
		}
		if id, ok := m["tool_call_id"].(string); ok {
			s += "@" + id
		}
		if rc, ok := m["reasoning_content"].(string); ok && rc != "" {
			s += "(r)"
		}
		shape = append(shape, s)
	}
	if strings.Join(shape, " ") != "system user assistant+call_a+call_b(r) tool@call_a tool@call_b user" {
		t.Errorf("second request = %v", shape)
	}
	if tool := second[3].(map[string]any); !strings.Contains(tool["content"].(string), "BEGIN_WORKER_OUTPUT agent=auth source=screen") {
		t.Errorf("tool result content = %v", tool["content"])
	}
	steps := h.log.ofType("brain.step")
	if len(steps) != 2 || payload(steps[0])["provider"] != "deepseek" || payload(steps[0])["prompt_tokens"] != int32(200) || payload(steps[0])["tool_calls"] != 2 || payload(steps[1])["completion_tokens"] != int32(30) {
		t.Errorf("brain.step = %v", steps)
	}
	if turn := payload(h.log.ofType("brain.turn")[0]); turn["tokens_in"] != 320 || turn["tokens_out"] != 70 || turn["provider"] != "deepseek" || turn["model"] != "deepseek-flash" {
		t.Errorf("brain.turn = %v", turn)
	}

	// Turn 2, still DeepSeek: the earlier turn's reasoning_content stays
	// on disk and out of the request.
	srv.add(ok(`{"spoken":"Still running.","detail":""}`))
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	third := srv.request(2)["messages"].([]any)
	if reply := third[5].(map[string]any); reply["role"] != "assistant" || reply["reasoning_content"] != nil || !strings.Contains(reply["content"].(string), "Auth is running the tests") {
		t.Errorf("earlier reply replayed with its reasoning: %v", reply)
	}
	for _, mm := range third {
		if rc, _ := mm.(map[string]any)["reasoning_content"].(string); rc != "" {
			t.Errorf("an earlier turn's reasoning replayed: %v", mm)
		}
	}

	// Turn 3 on the fake "gemini": the DeepSeek steps are rebuilt as
	// plain function calls, nothing of DeepSeek's Raw is parsed.
	if err := h.brain.SetModel(ProviderGemini, "gemini-3.5-flash", "test"); err != nil {
		t.Fatal(err)
	}
	fake.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolRecall, map[string]any{"query": "tests"})}}, braintest.Step{Text: braintest.Reply("Nothing new.", "")})
	if _, err := h.ask("anything new?", "auth"); err != nil {
		t.Fatal(err)
	}
	var fcs, signed int
	for _, c := range fake.Calls()[0].Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				fcs++
				if len(p.ThoughtSignature) > 0 {
					signed++
				}
			}
		}
	}
	if fcs != 2 || signed != 0 {
		t.Errorf("gemini saw %d function calls, %d signed", fcs, signed)
	}

	// Turn 4 back on DeepSeek: its own step replays from Raw (both
	// calls, reasoning dropped: an earlier turn), the Gemini step is
	// rebuilt (bt- id, no reasoning).
	if err := h.brain.SetModel(ProviderDeepSeek, "deepseek-flash", "test"); err != nil {
		t.Fatal(err)
	}
	srv.add(ok(`{"spoken":"Fine.","detail":""}`))
	if _, err := h.ask("ok?", "auth"); err != nil {
		t.Fatal(err)
	}
	fourth := srv.request(3)["messages"].([]any)
	var own, rebuilt, geminiReply int
	for _, mm := range fourth {
		m := mm.(map[string]any)
		if m["role"] != "assistant" {
			continue
		}
		tcs, _ := m["tool_calls"].([]any)
		rc, _ := m["reasoning_content"].(string)
		switch {
		case len(tcs) == 2 && rc == "":
			own++
		case len(tcs) == 1 && strings.HasPrefix(tcs[0].(map[string]any)["id"].(string), "bt-") && rc == "":
			rebuilt++
		case len(tcs) == 0 && strings.Contains(m["content"].(string), "Nothing new.") && rc == "":
			geminiReply++
		}
	}
	if own != 1 || rebuilt != 1 || geminiReply != 1 {
		t.Errorf("fourth request: own %d rebuilt %d gemini reply %d: %v", own, rebuilt, geminiReply, fourth)
	}
	if srv.count() != 4 {
		t.Errorf("requests = %d", srv.count())
	}
}

// A 402 from the provider ends the turn as a model_error whose message
// is the API's own, in brain.step and brain.fallback; a 429 is
// rate_limited.
func TestBrainOverOpenAICompatErrors(t *testing.T) {
	srv := newCompatServer(t,
		compatReply{status: 402, body: `{"error":{"message":"Insufficient Balance"}}`},
		compatReply{status: 429, body: `{"error":{"message":"Rate limit reached for requests"}}`},
	)
	ds := newCompat(t, srv.srv.URL, true, "low", nil)
	h := newHarness(t, braintest.NewModel(), func(c *Config) { c.Provider = ProviderDeepSeek; c.Model = "deepseek-flash" })
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ask("q", "auth"); !errors.Is(err, ErrFallback) || !strings.Contains(err.Error(), "Insufficient Balance") {
		t.Errorf("402 turn = %v", err)
	}
	if _, err := h.ask("q", "auth"); !errors.Is(err, ErrFallback) || !strings.Contains(err.Error(), "Rate limit reached") {
		t.Errorf("429 turn = %v", err)
	}
	steps, falls := h.log.ofType("brain.step"), h.log.ofType("brain.fallback")
	if len(steps) != 2 || payload(steps[0])["error"] != "deepseek: HTTP 402: Insufficient Balance" || !strings.Contains(payload(steps[1])["error"].(string), "HTTP 429: Rate limit reached") {
		t.Errorf("brain.step = %v", steps)
	}
	if len(falls) != 2 || payload(falls[0])["reason"] != "model_error" || payload(falls[0])["error"] != "deepseek: HTTP 402: Insufficient Balance" || payload(falls[1])["reason"] != "rate_limited" {
		t.Errorf("brain.fallback = %v", falls)
	}
}

// The provider switch: an unregistered id is refused, a registered one
// flips provider and model together (marker and event carry both), a
// switch mid-turn keeps the turn on the provider it started with, and
// the per-provider catalogs are independent.
func TestSetModelProvider(t *testing.T) {
	m1, m2 := braintest.NewModel(), braintest.NewModel()
	m1.Models, m2.Models = braintest.Catalog(), braintest.Catalog2()
	h := newHarness(t, m1, nil)
	if err := h.brain.SetModel("deepseek", "deepseek-flash", "test"); !errors.Is(err, ErrUnknownProvider) || !strings.Contains(err.Error(), "registered: gemini") {
		t.Errorf("unregistered = %v", err)
	}
	if err := h.brain.AddProvider("", m2); err == nil {
		t.Error("empty id accepted")
	}
	if err := h.brain.AddProvider("DeepSeek", m2); err != nil {
		t.Fatal(err)
	}
	if got := h.brain.Providers(); strings.Join(got, ",") != "deepseek,gemini" {
		t.Errorf("providers = %v", got)
	}
	if err := h.brain.SetModel("deepseek", "deepseek-flash", "debug"); err != nil {
		t.Fatal(err)
	}
	if h.brain.Provider() != "deepseek" || h.brain.Model() != "deepseek-flash" {
		t.Errorf("current = %s/%s", h.brain.Provider(), h.brain.Model())
	}
	ev := payload(h.log.ofType("brain.model_changed")[0])
	if ev["from_provider"] != "gemini" || ev["from"] != DefaultModel || ev["provider"] != "deepseek" || ev["to"] != "deepseek-flash" {
		t.Errorf("event = %v", ev)
	}
	if msgs := h.brain.Transcript().Messages(); msgs[0].Content != "model switched from gemini/"+DefaultModel+" to deepseek/deepseek-flash" {
		t.Errorf("marker = %q", msgs[0].Content)
	}
	// Same pair again: no-op; provider "" keeps the current one.
	if err := h.brain.SetModel("", "deepseek-flash", "debug"); err != nil || h.log.count("brain.model_changed") != 1 {
		t.Errorf("no-op: %v %d", err, h.log.count("brain.model_changed"))
	}
	// Catalogs per provider.
	ctx := context.Background()
	if c := h.brain.Models(ctx, "gemini", false); c.Provider != "gemini" || len(c.Models) != len(braintest.Catalog()) || c.Err != nil {
		t.Errorf("gemini catalog = %+v", c)
	}
	if c := h.brain.Models(ctx, "", false); c.Provider != "deepseek" || len(c.Models) != len(braintest.Catalog2()) || c.Models[0].Name != "deepseek-flash" {
		t.Errorf("deepseek catalog = %+v", c)
	}
	if c := h.brain.CachedModels("gemini"); len(c.Models) != len(braintest.Catalog()) || c.Err != nil {
		t.Errorf("cached gemini = %+v", c)
	}
	if c := h.brain.CachedModels("nope"); !errors.Is(c.Err, ErrUnknownProvider) {
		t.Errorf("cached unknown = %+v", c)
	}

	// Mid-turn: the running turn stays on deepseek (m2), the next one
	// runs on gemini (m1).
	release := make(chan struct{})
	m2.Add(braintest.Step{Block: release, Text: braintest.Reply("from m2", "")})
	m1.Add(braintest.Step{Text: braintest.Reply("from m1", "")})
	done := make(chan Reply, 1)
	go func() {
		rep, _ := h.ask("q1", "auth")
		done <- rep
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(m2.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := h.brain.SetModel("gemini", "gemini-3.8-flash", "debug"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if rep := <-done; rep.Spoken != "from m2" {
		t.Errorf("turn 1 = %+v", rep)
	}
	if rep, err := h.ask("q2", "auth"); err != nil || rep.Spoken != "from m1" {
		t.Errorf("turn 2 = %+v %v", rep, err)
	}
	var stepProviders []string
	for _, e := range h.log.ofType("brain.step") {
		stepProviders = append(stepProviders, payload(e)["provider"].(string)+"/"+payload(e)["model"].(string))
	}
	if strings.Join(stepProviders, " ") != "deepseek/deepseek-flash gemini/gemini-3.8-flash" {
		t.Errorf("brain.step providers = %v", stepProviders)
	}
}

// Persistence round-trip of a message with a provider: the JSONL line
// carries provider and model with raw, and reads back the same.
func TestMessageProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	tr, err := OpenTranscript(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.Append(Message{Role: RoleAssistant, Kind: KindToolCall, Provider: ProviderDeepSeek, Model: "deepseek-flash", Raw: json.RawMessage(`{"role":"assistant","reasoning_content":"r"}`), ToolCall: &ToolCall{ID: "call_1", Name: ToolRecall}})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	msgs, err := ReadTranscript(path)
	if err != nil || len(msgs) != 1 || msgs[0].Provider != ProviderDeepSeek || msgs[0].Model != "deepseek-flash" || string(msgs[0].Raw) != `{"role":"assistant","reasoning_content":"r"}` {
		t.Errorf("read back = %+v %v", msgs, err)
	}
}

// TestVoiceLineDuringToolCall is the failure of 2026-09-24 18:16:43
// through the real path: the voice speaks its filler ("On it.") while
// the brain's read_screen runs, Brain.Said ingests it between the call
// and its result, and the next request must still put the tool message
// right after the assistant's tool_calls (DeepSeek answered HTTP 400
// otherwise, and every later turn failed on the same window). The line
// is kept, after the result; the next turn (the stop turn that failed
// in the log) is valid too.
func TestVoiceLineDuringToolCall(t *testing.T) {
	srv := newCompatServer(t,
		toolCalls("Read more of the screen.", [3]string{"call_00_Bk", ToolReadScreen, `{"agent":"auth","lines":200}`}),
		ok(`{"spoken":"Two glitches: a stale listening state and a late error banner.","detail":""}`),
		ok(`{"spoken":"Nothing was in flight.","detail":""}`),
	)
	ds := newCompat(t, srv.srv.URL, true, "low", nil)
	h := newHarness(t, braintest.NewModel(), func(c *Config) { c.Provider = ProviderDeepSeek; c.Model = "deepseek-flash" })
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	h.host.ReadHook = func(context.Context) error {
		h.brain.Said("On it.", true)
		return nil
	}
	if _, err := h.ask("Um, I want to know the detail of the glitches", ""); err != nil {
		t.Fatalf("turn with a voice line mid-step: %v", err)
	}
	if got := joinKinds(h.kinds()); got != "request brain_tool_call assistant_said brain_tool_result reply" {
		t.Fatalf("transcript = %s (the test needs the line between the call and its result)", got)
	}
	h.brain.Quiesce()
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindStop, Stopped: "nothing was in flight"}); err != nil {
		t.Fatalf("next turn: %v", err)
	}
	for i := 1; i < srv.count(); i++ {
		shape := requestShape(srv.request(i))
		if !strings.Contains(shape, "assistant+call_00_Bk tool@call_00_Bk user") {
			t.Errorf("request %d = %s", i, shape)
		}
	}
	// The line is still there, after the result.
	msgs := srv.request(1)["messages"].([]any)
	for i, mm := range msgs {
		if m := mm.(map[string]any); m["tool_call_id"] == "call_00_Bk" {
			next, _ := msgs[i+1].(map[string]any)["content"].(string)
			if !strings.Contains(next, `Assistant said: "On it."`) {
				t.Errorf("message after the result = %q", next)
			}
		}
	}
}

// requestShape is a chat request's roles with tool call ids ("+id") and
// tool_call_ids ("@id").
func requestShape(req map[string]any) string {
	var shape []string
	for _, mm := range req["messages"].([]any) {
		m := mm.(map[string]any)
		s := m["role"].(string)
		if tcs, ok := m["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				s += "+" + tc.(map[string]any)["id"].(string)
			}
		}
		if id, ok := m["tool_call_id"].(string); ok {
			s += "@" + id
		}
		shape = append(shape, s)
	}
	return strings.Join(shape, " ")
}
