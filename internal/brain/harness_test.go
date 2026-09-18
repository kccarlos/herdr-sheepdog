package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// recLog records structured events.
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) ofType(typ string) []logging.Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logging.Line
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (l *recLog) count(typ string) int { return len(l.ofType(typ)) }

// payload returns the event's payload map.
func payload(e logging.Line) map[string]any {
	m, _ := e.Payload.(map[string]any)
	return m
}

// harness bundles a brain over the scripted model and the fake host.
type harness struct {
	t     *testing.T
	brain *Brain
	model *braintest.Model
	host  *braintest.Host
	log   *recLog
	dir   string
}

// newHarness builds a brain with one agent "auth" (pane w5:p1, working)
// and one "docs" (w6:p1, idle), the codex approval screen on auth.
func newHarness(t *testing.T, model *braintest.Model, tweak func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, model: model, host: braintest.NewHost(), log: &recLog{}, dir: t.TempDir()}
	cfg := Config{DataDir: h.dir, RunID: "run1", Now: time.Now}
	if tweak != nil {
		tweak(&cfg)
	}
	var err error
	h.brain, err = New(cfg, model, h.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.brain.Close() })
	h.host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", Kind: "codex", State: "working", Task: "run the tests"})
	h.host.SetAgent(AgentInfo{PaneID: "w6:p1", Alias: "docs", Kind: "claude", State: "idle", LastResult: "wrote the readme (success)"})
	h.host.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	h.brain.SetHost(h.host)
	return h
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// approvalPrompt is the codex approval prompt as the supervisor records it.
func approvalPrompt() *PendingPrompt {
	return &PendingPrompt{ID: "ap-1", Question: "whether it can create hello.txt",
		Options: []Option{{Label: "Yes, proceed", Keys: []string{"enter"}}, {Label: "Yes, and don't ask again", Keys: []string{"p"}}, {Label: "No", Keys: []string{"esc"}}},
		YesKeys: []string{"enter"}, NoKeys: []string{"esc"}}
}

// kinds returns the transcript's message kinds in order.
func (h *harness) kinds() []string {
	var out []string
	for _, m := range h.brain.Transcript().Messages() {
		out = append(out, string(m.Kind))
	}
	return out
}

func (h *harness) ask(q, agent string) (Reply, error) {
	return h.brain.Ask(context.Background(), Request{Question: q, Agent: agent})
}

func joinKinds(k []string) string { return strings.Join(k, " ") }
