package brain_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// SetModel validates the name, strips "models/", is a no-op for the
// same name, and on a change logs brain.model_changed and marks the
// transcript with a system message the window carries.
func TestSetModel(t *testing.T) {
	m := braintest.NewModel()
	h := newHarness(t, m, nil)
	if got := h.brain.Model(); got != DefaultModel {
		t.Fatalf("initial model = %q", got)
	}
	for _, bad := range []string{"", "   ", "models/", "gemini flash", "a;b", strings.Repeat("x", 129)} {
		if err := h.brain.SetModel("", bad, "test"); err == nil {
			t.Errorf("SetModel(%q) accepted", bad)
		}
	}
	if err := h.brain.SetModel(" Gemini ", " models/"+DefaultModel+" ", "test"); err != nil || h.brain.Model() != DefaultModel {
		t.Errorf("same model: err = %v model = %q", err, h.brain.Model())
	}
	if h.log.count("brain.model_changed") != 0 || h.brain.Transcript().Len() != 0 {
		t.Errorf("no-op switch logged or marked: %d events, %d messages", h.log.count("brain.model_changed"), h.brain.Transcript().Len())
	}
	if err := h.brain.SetModel("", "models/gemini-3.8-flash", "debug"); err != nil {
		t.Fatal(err)
	}
	if got := h.brain.Model(); got != "gemini-3.8-flash" {
		t.Errorf("model = %q", got)
	}
	ev := h.log.ofType("brain.model_changed")
	if len(ev) != 1 || payload(ev[0])["from"] != DefaultModel || payload(ev[0])["to"] != "gemini-3.8-flash" || payload(ev[0])["via"] != "debug" || payload(ev[0])["from_provider"] != ProviderGemini || payload(ev[0])["provider"] != ProviderGemini {
		t.Errorf("brain.model_changed = %v", ev)
	}
	msgs := h.brain.Transcript().Messages()
	if len(msgs) != 1 || msgs[0].Kind != KindSystem || msgs[0].Role != RoleSystem || msgs[0].Content != "model switched from gemini/"+DefaultModel+" to gemini/gemini-3.8-flash" || msgs[0].Turn != 0 {
		t.Errorf("marker = %+v", msgs)
	}
	if win := h.brain.Window(); len(win) != 1 || win[0].Kind != KindSystem {
		t.Errorf("window = %+v", win)
	}
	// The next turn runs on the new model and sees the note.
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	calls := m.Calls()
	if len(calls) != 1 || calls[0].Model != "gemini-3.8-flash" || !strings.Contains(calls[0].Text(), "Note from the daemon: model switched from") {
		t.Errorf("call = %+v", calls)
	}
}

// A switch during a turn does not land between its steps: every model
// call of the running turn uses the name it started with (and the
// brain.step events say so); the next turn uses the new one.
func TestSetModelMidTurnKeepsSnapshot(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Block: release, Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply("Auth is waiting for approval.", "")},
	)
	m.Default = &braintest.Step{Text: braintest.Reply("done", "")}
	h := newHarness(t, m, nil)
	type result struct {
		rep Reply
		err error
	}
	done := make(chan result, 1)
	go func() {
		rep, err := h.ask("what is auth doing?", "auth")
		done <- result{rep, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// The setter does not wait for the turn lock.
	switched := make(chan error, 1)
	go func() { switched <- h.brain.SetModel("", "gemini-3.8-flash", "debug") }()
	select {
	case err := <-switched:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SetModel blocked behind the running turn")
	}
	if got := h.brain.Model(); got != "gemini-3.8-flash" {
		t.Errorf("Model() during the turn = %q", got)
	}
	close(release)
	res := <-done
	if res.err != nil || !strings.HasPrefix(res.rep.Spoken, "Auth is waiting") {
		t.Fatalf("turn 1 = %+v %v", res.rep, res.err)
	}
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	calls := m.Calls()
	if len(calls) != 3 || calls[0].Model != DefaultModel || calls[1].Model != DefaultModel || calls[2].Model != "gemini-3.8-flash" {
		var names []string
		for _, c := range calls {
			names = append(names, c.Model)
		}
		t.Errorf("models per call = %v", names)
	}
	// Turn 1's tool call was signed by the old model: turn 2 replays it as
	// a plain function call, without the signature the new model would
	// reject (the unchanged case keeps replaying Raw: TestReadScreenThenAnswer).
	var signed, plainCalls int
	for _, c := range calls[2].Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				plainCalls++
				if len(p.ThoughtSignature) > 0 {
					signed++
				}
			}
		}
	}
	if signed != 0 || plainCalls != 1 {
		t.Errorf("turn 2 replay: %d function calls, %d signed", plainCalls, signed)
	}
	if msgs := h.brain.Transcript().Messages(); msgs[2].Kind != KindToolCall || msgs[2].Model != DefaultModel || msgs[2].Provider != ProviderGemini {
		t.Errorf("tool call message origin = %q/%q", msgs[2].Provider, msgs[2].Model)
	}
	var stepModels []string
	for _, e := range h.log.ofType("brain.step") {
		stepModels = append(stepModels, payload(e)["model"].(string))
	}
	if strings.Join(stepModels, " ") != DefaultModel+" "+DefaultModel+" gemini-3.8-flash" {
		t.Errorf("brain.step models = %v", stepModels)
	}
	// The marker is where the switch happened (during the blocked first
	// step, before its tool call was recorded), on turn 0.
	var seq []string
	for _, msg := range h.brain.Transcript().Messages() {
		if msg.Kind == KindSystem {
			seq = append(seq, "SYSTEM")
		} else {
			seq = append(seq, string(msg.Kind))
		}
	}
	if got := strings.Join(seq, " "); got != "request SYSTEM brain_tool_call brain_tool_result reply request reply" {
		t.Errorf("transcript = %s", got)
	}
}

// Models serves the catalog from the cache within CatalogTTL, fetches on
// refresh or expiry, keeps the last list when a fetch fails (with the
// error and stale set), and reports ErrNoCatalog for a model without one.
func TestModelsCatalogCache(t *testing.T) {
	m := braintest.NewModel()
	m.Models = braintest.Catalog()
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	h := newHarness(t, m, func(c *Config) { c.Now = func() time.Time { return now } })
	ctx := context.Background()

	if c := h.brain.CachedModels(""); len(c.Models) != 0 || c.Err != nil || c.Stale || !c.FetchedAt.IsZero() {
		t.Errorf("empty cache = %+v", c)
	}
	c := h.brain.Models(ctx, "", false)
	if c.Err != nil || c.Provider != ProviderGemini || len(c.Models) != len(braintest.Catalog()) || !c.FetchedAt.Equal(now) || c.Stale || c.Models[0].Name != "gemini-3.8-flash" {
		t.Fatalf("first fetch = %+v", c)
	}
	// Within the TTL: served from the cache even though the source now
	// fails.
	m.ListErr = errors.New("boom")
	now = now.Add(CatalogTTL - time.Second)
	if c := h.brain.Models(ctx, "", false); c.Err != nil || c.Stale || len(c.Models) == 0 {
		t.Errorf("cached = %+v", c)
	}
	// Refresh bypasses the cache: the failure is reported with the last
	// list.
	c = h.brain.Models(ctx, "", true)
	if c.Err == nil || !c.Stale || len(c.Models) != len(braintest.Catalog()) || c.Err.Error() != "boom" {
		t.Errorf("failed refresh = %+v", c)
	}
	if h.log.count("brain.catalog_error") != 1 || h.log.count("brain.catalog") != 1 {
		t.Errorf("catalog events = %d ok / %d error", h.log.count("brain.catalog"), h.log.count("brain.catalog_error"))
	}
	// Past the TTL a plain call fetches again; back to fresh once it
	// works.
	now = now.Add(2 * time.Second)
	if c := h.brain.Models(ctx, "", false); c.Err == nil || !c.Stale {
		t.Errorf("expired, still failing = %+v", c)
	}
	m.ListErr = nil
	m.Models = braintest.Catalog()[:2]
	c = h.brain.Models(ctx, "", false)
	if c.Err != nil || c.Stale || len(c.Models) != 2 || !c.FetchedAt.Equal(now) {
		t.Errorf("refetched = %+v", c)
	}
	if c := h.brain.CachedModels(""); len(c.Models) != 2 || c.Err != nil {
		t.Errorf("CachedModels = %+v", c)
	}
}

// noCatalog is a brain.Model without a catalog.
type noCatalog struct{ m *braintest.Model }

func (n noCatalog) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return n.m.Generate(ctx, model, contents, cfg)
}

// A model without a catalog reports ErrNoCatalog and no list.
func TestModelsWithoutCatalog(t *testing.T) {
	b, err := New(Config{}, noCatalog{braintest.NewModel()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if c := b.Models(context.Background(), "", true); !errors.Is(c.Err, ErrNoCatalog) || len(c.Models) != 0 || c.Stale {
		t.Errorf("no catalog = %+v", c)
	}
	if c := b.Models(context.Background(), "nope", true); !errors.Is(c.Err, ErrUnknownProvider) || len(c.Models) != 0 || c.Provider != "nope" {
		t.Errorf("unknown provider = %+v", c)
	}
}
