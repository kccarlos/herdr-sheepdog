package brain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
)

// sectionsWith returns the sections whose label is label.
func sectionsWith(snap ctxcap.Snapshot, label string) []ctxcap.Section {
	var out []ctxcap.Section
	for _, s := range snap.Sections {
		if s.Label == label {
			out = append(out, s)
		}
	}
	return out
}

func hasLink(s ctxcap.Section, link string) bool {
	for _, l := range s.Links {
		if l == link {
			return true
		}
	}
	return false
}

// itemPaths lists the top-level items of a body ("contents[3]",
// "messages[5]") so a test can check every one is mapped.
var itemPathRE = regexp.MustCompile(`^(contents|messages)\[\d+\]`)

func assertCovers(t *testing.T, step ctxcap.BrainStep, items string, n int) {
	t.Helper()
	if err := step.CheckCoverage(); err != nil {
		t.Fatalf("step %d: %v", step.Step, err)
	}
	mapped := map[string]bool{}
	for _, s := range step.Sections {
		if m := itemPathRE.FindString(s.Path); m != "" {
			mapped[m] = true
		}
	}
	for i := 0; i < n; i++ {
		if p := fmt.Sprintf("%s[%d]", items, i); !mapped[p] {
			t.Errorf("step %d: %s has no section", step.Step, p)
		}
	}
}

// One utterance turn over the Gemini path: every model call is recorded
// with a section map that covers the text exactly, maps every content,
// labels the prompt sections, the tools and every message kind, cuts the
// worker output out as untrusted, and links the utterance, its request,
// the push and the worker event. The requests file has a line per step.
func TestCaptureGemini(t *testing.T) {
	model := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"}), braintest.FC(ToolListAgents, nil)}, Usage: [2]int32{900, 20}},
		braintest.Step{Text: braintest.Reply("Auth is at an approval prompt.", "")},
	)
	h := newHarness(t, model, nil)
	h.brain.WorkerEventLinked("auth", "working", "blocked", false, "evt:e1")
	h.brain.PushedLinked("auth", "auth is asking something.", "interrupt", "push:p1")
	h.brain.Heard("what is auth doing?", true)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "what is auth doing?", Link: "call:c1"}); err != nil {
		t.Fatal(err)
	}
	log := h.brain.Requests()
	if log.Len() != 2 {
		t.Fatalf("steps = %d, want 2", log.Len())
	}
	first, _, hasPrev, err := log.Get(1)
	if err != nil || hasPrev {
		t.Fatalf("get 1: %v %v", err, hasPrev)
	}
	last, prev, hasPrev, err := log.Get(0)
	if err != nil || !hasPrev || prev.Step != 1 || last.Step != 2 {
		t.Fatalf("latest = %d prev = %d (%v) err %v", last.Step, prev.Step, hasPrev, err)
	}
	for _, st := range []ctxcap.BrainStep{first, last} {
		var body struct {
			Contents []json.RawMessage `json:"contents"`
			Config   map[string]any    `json:"config"`
		}
		if err := json.Unmarshal(st.Body, &body); err != nil {
			t.Fatal(err)
		}
		if st.Format != FormatGemini || st.Turn != 1 || st.Kind != "utterance" || body.Config["systemInstruction"] == nil {
			t.Errorf("step %d meta = %+v", st.Step, st.Meta())
		}
		assertCovers(t, st, "contents", len(body.Contents))
	}
	if first.Usage == nil || first.Usage.PromptTokens != 900 || first.TurnStep != 1 || last.TurnStep != 2 {
		t.Errorf("usage / turn step: %+v %d %d", first.Usage, first.TurnStep, last.TurnStep)
	}

	// Labels: the prompt sections, the tools, every message kind.
	for _, want := range []string{"request settings", "role (SOUL.md)", "USER.md", "roster", "ledger", "investigations", "time", "tool read_screen", "tool send_keys",
		"worker event", "push", "user utterance", "current request", "orchestrator tool call", "orchestrator tool result", "worker output (untrusted)"} {
		if len(sectionsWith(last.Snapshot, want)) == 0 {
			t.Errorf("no %q section in step 2", want)
		}
	}
	// Links.
	checks := map[string]string{"user utterance": "call:c1", "current request": "call:c1", "push": "push:p1", "worker event": "evt:e1", "orchestrator tool call": "call:c1", "orchestrator tool result": "call:c1"}
	for label, link := range checks {
		secs := sectionsWith(last.Snapshot, label)
		if len(secs) == 0 || !hasLink(secs[0], link) {
			t.Errorf("%s: links %v, want %s", label, secs, link)
		}
	}
	// Untrusted worker output sits inside its delimiters.
	for _, s := range sectionsWith(last.Snapshot, "worker output (untrusted)") {
		text := strings.TrimRight(last.Text[s.Start:s.End], "\n")
		if !strings.HasPrefix(text, "<<<BEGIN_WORKER_OUTPUT") || !strings.HasSuffix(unnonce(text), "<<<END_WORKER_OUTPUT>>>") {
			t.Errorf("untrusted piece not delimited: %.80q … %.40q", text, text[max(0, len(text)-40):])
		}
	}
	// The diff: step 2 adds the tool call and the results.
	d := ctxcap.DiffBrain(last, prev, true)
	if len(d.Added) == 0 || len(d.Removed) != 0 {
		t.Errorf("diff = %+v", d)
	}

	// The requests file: one line per step.
	data, err := os.ReadFile(filepath.Join(h.dir, TranscriptDir, "run1"+ctxcap.RequestsSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n != 2 {
		t.Errorf("requests file lines = %d", n)
	}
	if n := len(h.log.ofType("context.step")); n != 2 {
		t.Errorf("context.step events = %d", n)
	}
}

// The DeepSeek path: the captured body is exactly what reached the
// endpoint, every message is mapped, the reasoning is in it, and the
// API key is not.
func TestCaptureDeepSeek(t *testing.T) {
	srv := newCompatServer(t,
		toolCalls("Read the screen.", [3]string{"call_a", ToolReadScreen, `{"agent":"auth","lines":50}`}),
		ok(`{"spoken":"Auth is asking.","detail":""}`),
	)
	ds := newCompat(t, srv.srv.URL, true, "low", nil)
	h := newHarness(t, braintest.NewModel(), func(c *Config) { c.Provider = ProviderDeepSeek; c.Model = "deepseek-flash" })
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	if n := h.brain.Requests().Len(); n != 2 || srv.count() != 2 {
		t.Fatalf("steps = %d, requests = %d", n, srv.count())
	}
	for i := 1; i <= 2; i++ {
		st, _, _, err := h.brain.Requests().Get(i)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(st.Body, &got); err != nil {
			t.Fatal(err)
		}
		if want := srv.request(i - 1); !reflect.DeepEqual(got, want) {
			t.Errorf("step %d body differs from the request sent:\n got %v\nwant %v", i, got, want)
		}
		if st.Format != FormatOpenAI || strings.Contains(string(st.Body), "k-test") || strings.Contains(st.Text, "k-test") {
			t.Errorf("step %d: format %s / key material in the capture", i, st.Format)
		}
		assertCovers(t, st, "messages", len(got["messages"].([]any)))
		if i == 2 && !strings.Contains(st.Text, "(reasoning_content) Read the screen.") {
			t.Errorf("step 2 does not show the replayed reasoning:\n%s", st.Text)
		}
	}
}

// Pruning and compaction show in the diff: a read_screen result pruned
// to its stub is "pruned", and a compaction's summary is "compacted"
// with what it replaced "removed: compacted". A reset in between is
// "removed: reset".
func TestCaptureDiffPruneCompactReset(t *testing.T) {
	model := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply("Auth is asking.", "")},
	)
	model.Default = &braintest.Step{Text: braintest.Reply("Fine.", "")}
	h := newHarness(t, model, func(c *Config) { c.PruneAfterTurns = 1 })
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	for _, q := range []string{"and docs?", "anything else?"} {
		if _, err := h.ask(q, "docs"); err != nil {
			t.Fatal(err)
		}
		h.brain.Quiesce()
	}
	cur, prev, _, err := h.brain.Requests().Get(0)
	if err != nil {
		t.Fatal(err)
	}
	d := ctxcap.DiffBrain(cur, prev, true)
	if len(d.Pruned) == 0 {
		t.Errorf("no pruned piece in %+v", d)
	}
	pruned := false
	for _, s := range cur.Sections {
		if s.Pruned && s.Class == ctxcap.ClassPruned && strings.Contains(cur.Text[s.Start:s.End], "pruned]") {
			pruned = true
		}
	}
	if !pruned {
		t.Error("no pruned stub section")
	}
	for _, key := range d.Changed {
		if !strings.HasPrefix(key, "prompt:") && key != "meta" {
			t.Errorf("message %s changed in place", key)
		}
	}

	// Compaction: a tiny budget makes the next turn's after-work compact.
	h2 := newHarness(t, braintest.NewModel(), func(c *Config) { c.WindowTokens = 60 })
	h2.model.Default = &braintest.Step{Text: braintest.Reply("Fine.", "")}
	for _, q := range []string{"first question", "second question", "third"} {
		if _, err := h2.ask(q, ""); err != nil {
			t.Fatal(err)
		}
		h2.brain.Quiesce()
	}
	cur, prev, _, _ = h2.brain.Requests().Get(0)
	d = ctxcap.DiffBrain(cur, prev, true)
	if len(d.Compacted) == 0 {
		t.Fatalf("no compaction in the diff: %+v", d)
	}
	compacted := 0
	for _, r := range d.Removed {
		if r.Reason == ctxcap.ReasonCompacted {
			compacted++
		}
	}
	if compacted == 0 {
		t.Errorf("removed = %+v", d.Removed)
	}

	// Reset: what was there is "removed: reset".
	if _, err := h2.brain.Reset(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.ask("after the reset", ""); err != nil {
		t.Fatal(err)
	}
	cur, prev, _, _ = h2.brain.Requests().Get(0)
	d = ctxcap.DiffBrain(cur, prev, true)
	if len(d.Removed) == 0 || d.Removed[0].Reason != ctxcap.ReasonReset || cur.Segment == prev.Segment {
		t.Errorf("reset diff = %+v (segments %s / %s)", d.Removed, prev.Segment, cur.Segment)
	}
}

// A restart keeps the steps: a new run loads the previous run's tail and
// numbers on from it.
func TestCaptureRestart(t *testing.T) {
	dir := t.TempDir()
	model := braintest.NewModel()
	model.Default = &braintest.Step{Text: braintest.Reply("Fine.", "")}
	b, err := New(Config{DataDir: dir, RunID: "20260923T100000Z", Now: time.Now}, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.SetHost(braintest.NewHost())
	for i := 0; i < 3; i++ {
		if _, err := b.Turn(context.Background(), Request{Kind: KindUtterance, Question: "status?"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = b.Close()
	b2, err := New(Config{DataDir: dir, RunID: "20260923T110000Z", Now: time.Now}, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b2.Close() }()
	b2.SetHost(braintest.NewHost())
	if n := b2.Requests().Len(); n != 3 {
		t.Fatalf("loaded %d steps, want 3", n)
	}
	if _, err := b2.Turn(context.Background(), Request{Kind: KindUtterance, Question: "status?"}); err != nil {
		t.Fatal(err)
	}
	cur, prev, _, _ := b2.Requests().Get(0)
	if cur.Step != 4 || cur.Run != "20260923T110000Z" || prev.Run != "20260923T100000Z" {
		t.Errorf("step %d run %s prev run %s", cur.Step, cur.Run, prev.Run)
	}
}
