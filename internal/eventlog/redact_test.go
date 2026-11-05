package eventlog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// A secret in an event (a provider's error echoing a key, a CLI's stderr)
// must not reach the day file, the ring, an SSE subscriber or a tap: the
// redactor runs once, before any of them see the event.
func TestRedactorMasksEveryOutlet(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)}
	l, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "k3y-value-0123456789"
	const bearer = "abcdef0123456789xyz"
	l.SetRedactor(redact.New(secret).String)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := l.Subscribe(ctx)
	tap := l.Tap(8)

	l.Log("cli", "cli.run", "", map[string]any{
		"error":  "401 for key " + secret + ` and "quoted" text`,
		"output": "Authorization: Bearer " + bearer,
		"dur_ms": 7,
		"nested": []any{map[string]any{"s": secret}},
	})
	l.Log("cli", "cli.run", "", map[string]any{"error": "nothing secret", "n": 1})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "events", "2026-09-17.jsonl")
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first := l.Tail(2)[0]
	for name, text := range map[string]string{
		"file":       string(file),
		"ring":       string(first.Encoded()),
		"subscriber": string((<-sub).Encoded()),
		"tap":        string((<-tap.C()).Encoded()),
	} {
		if strings.Contains(text, secret) || strings.Contains(text, bearer) {
			t.Errorf("%s holds a secret: %s", name, text)
		}
	}
	if !strings.Contains(string(first.Payload), redact.Mask) {
		t.Errorf("payload was not masked: %s", first.Payload)
	}
	lines := readLines(t, path)
	if len(lines) != 2 || lines[0]["dur_ms"] != float64(7) {
		t.Errorf("redacted lines are still valid events: %v", lines)
	}
	if p := lines[1]["payload"].(map[string]any); p["error"] != "nothing secret" || p["n"] != float64(1) {
		t.Errorf("an event without a secret changed: %v", p)
	}
}
