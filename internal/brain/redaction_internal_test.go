package brain

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
