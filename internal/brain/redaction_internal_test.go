package brain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
	"google.golang.org/genai"
)

func TestIngestRedactsStructuredValues(t *testing.T) {
	secret := "custom-secret-123456789"
	tr, err := OpenTranscript("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	b := &Brain{cfg: Config{Redactor: redact.New(secret), Now: time.Now}, transcript: tr, engine: NewDefaultEngine(0, nil, nil)}
	m := Message{ToolCall: &ToolCall{Args: map[string]any{"nested": []any{secret}}}, Raw: json.RawMessage(`{"arguments":"{\"token\":\"` + secret + `\"}","number":42}`)}
	b.ingest(m, 1)
	got := tr.Messages()[0]
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Error("structured secret persisted")
	}
	if !json.Valid(got.Raw) {
		t.Error("raw JSON invalid")
	}
	if got.ToolCall.Args["nested"].([]any)[0] == secret {
		t.Error("argument not redacted")
	}
}

type redactionTestModel struct{}

func (redactionTestModel) Generate(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return nil, nil
}

func TestCompactionRecordRedactsSummary(t *testing.T) {
	secret := "custom-secret-123456789"
	b, err := New(Config{Redactor: redact.New(secret)}, redactionTestModel{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	e := b.engine.(*DefaultEngine)
	e.summarize = func(context.Context, string) (string, error) { return "summary " + secret, nil }
	for i := 0; i < 20; i++ {
		b.ingest(Message{Kind: KindCEOUtterance, Content: strings.Repeat("word ", 20), Tokens: 25}, i+1)
	}
	result, ok, err := e.Compact(context.Background(), 100)
	if err != nil || !ok {
		t.Fatalf("compact: %v %v", ok, err)
	}
	if strings.Contains(result.Summary.Content, secret) {
		t.Error("summary contains secret")
	}
	for _, m := range b.transcript.Messages() {
		if strings.Contains(m.Content, secret) {
			t.Error("transcript contains secret")
		}
	}
}

func TestLiveIngestAndCaptureStepRedactAllPaths(t *testing.T) {
	cfgSecret := `my"quoted\secret&key123`
	dir := t.TempDir()
	trPath := filepath.Join(dir, "run1.jsonl")
	tr, err := OpenTranscript(trPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	reqLog, err := ctxcap.OpenBrainLog(dir, "run1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reqLog.Close() }()

	b := &Brain{
		cfg:        Config{RunID: "run1", WindowTokens: 16000, Redactor: redact.New(cfgSecret), Now: time.Now},
		curRunID:   "run1",
		transcript: tr,
		requests:   reqLog,
		engine:     NewDefaultEngine(0, nil, nil),
	}

	// 1. Promise record with a secret in Promise.Text.
	pInfo := &PromiseInfo{ID: "p1", Text: "tell me when " + cfgSecret + " finishes", Agent: "auth", Event: PromiseEventDone}
	b.ingest(Message{Role: RoleUser, Kind: KindPromise, Content: "promise with " + cfgSecret, Agent: "auth", Promise: pInfo}, 1)

	// 2. ToolCall with structured credential keys and nested JSON arguments containing quotes/backslashes/ampersands.
	nestedArgs, err := json.Marshal(map[string]any{
		"cmd":      "export K=" + cfgSecret,
		"password": "live-password-456789",
	})
	if err != nil {
		t.Fatal(err)
	}
	rawMsg, err := json.Marshal(map[string]any{
		"role": "assistant",
		"tool_calls": []map[string]any{{
			"id":       "c1",
			"type":     "function",
			"function": map[string]any{"name": "send_keys", "arguments": string(nestedArgs)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.ingest(Message{
		Role: RoleAssistant, Kind: KindToolCall, Provider: "openai", Model: "gpt-test",
		ToolCall: &ToolCall{ID: "c1", Name: "send_keys", Args: map[string]any{"api_key": "live-cred-value-123456"}},
		Raw:      rawMsg,
	}, 1)

	// 3. JSON reply with structured credential key in Content.
	b.ingest(Message{Role: RoleAssistant, Kind: KindReply, Content: `{"spoken":"done","access_token":"live-access-789012"}`}, 1)

	// 4. captureStep with callErr and addition containing secrets.
	trTurn := &turn{n: 1, provider: "gemini", model: "gemini-test", impl: redactionTestModel{}, req: Request{Kind: KindUtterance, Trigger: TriggerCEO}}
	callErr := errors.New("upstream 401: key=" + cfgSecret + " and sk-liveerror1234567890abcdef")
	b.captureStep(trTurn, 1, false, []PromptSection{{Name: "sys", Text: "You are the orchestrator."}}, "addition "+cfgSecret, tr.Messages(), &genai.GenerateContentConfig{}, time.Now(), 10*time.Millisecond, nil, callErr)

	// 5. redactAgents also covers LastDetail and Journal.Error before clipping.
	agents := b.redactAgents([]AgentInfo{{
		Alias: "auth", LastDetail: "detail " + cfgSecret,
		Journal: []JournalEntry{{Error: "err " + cfgSecret}},
	}})
	if strings.Contains(agents[0].LastDetail, "key123") || strings.Contains(agents[0].Journal[0].Error, "key123") {
		t.Errorf("redactAgents leaked secret: %+v", agents[0])
	}

	for _, p := range []string{trPath, reqLog.Path()} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{
			"quoted",
			"key123",
			"live-cred-value-123456",
			"live-password-456789",
			"live-access-789012",
			"sk-liveerror1234567890abcdef",
		} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s leaked %q:\n%s", filepath.Base(p), leak, raw)
			}
		}
	}

	msgs, err := ReadTranscript(trPath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if !json.Valid(msgs[1].Raw) {
		t.Fatalf("msgs[1].Raw invalid JSON: %s", msgs[1].Raw)
	}
}
