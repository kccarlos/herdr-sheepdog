package brain_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// askAll runs one scripted turn per utterance (typed: no Heard, so the
// transcript holds each only as a request).
func askAll(t *testing.T, qs ...string) *harness {
	t.Helper()
	var steps []braintest.Step
	for range qs {
		steps = append(steps, braintest.Step{Text: braintest.Reply("ok", "")})
	}
	h := newHarness(t, braintest.NewModel(steps...), nil)
	for _, q := range qs {
		if _, err := h.ask(q, ""); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// A typed request is recalled in the user's own words, without the
// request's framing.
func TestRecallReturnsUserRequests(t *testing.T) {
	h := askAll(t, "walk me through the day", "is docs idle?")
	out := h.brain.Recall("walk through", "")
	if !strings.Contains(out, `request: "walk me through the day"`) {
		t.Errorf("recall missed the user's request:\n%s", out)
	}
	if strings.Contains(out, "Request from the assistant") || strings.Contains(out, "is docs idle") {
		t.Errorf("recall returned framing or a non-match:\n%s", out)
	}
}

// A Chinese query finds a Chinese request: no spaces between words.
func TestRecallChineseQuery(t *testing.T) {
	h := askAll(t, "帮我看看那个不稳定的测试", "db-codex 还在等吗？我说了明天。")
	out := h.brain.Recall("我最开始问的不稳定测试是什么", "")
	if first, _, _ := strings.Cut(out, "\n"); !strings.Contains(first, "帮我看看那个不稳定的测试") {
		t.Errorf("Chinese query did not rank the matching request first:\n%s", out)
	}
	if out := h.brain.Recall("明天", ""); !strings.Contains(out, "我说了明天") {
		t.Errorf("Chinese query missed:\n%s", out)
	}
}

// from start returns the first lines of the record in order, and a cut
// list says how many lines there were.
func TestRecallFromStart(t *testing.T) {
	var qs []string
	for i := 1; i <= 12; i++ {
		qs = append(qs, fmt.Sprintf("question number %d", i))
	}
	h := askAll(t, qs...)
	h.host.AddNote("an undated note")
	out := h.brain.Recall("", RecallFromStart)
	lines := strings.Split(out, "\n")
	if len(lines) != 8+1 {
		t.Fatalf("got %d lines, want %d plus the count:\n%s", len(lines), 8, out)
	}
	if !strings.Contains(lines[0], `request: "question number 1"`) {
		t.Errorf("first line is not the first request:\n%s", out)
	}
	// Oldest first: the requests come in the order they were asked.
	last := -1
	for i := 1; i <= 12; i++ {
		j := strings.Index(out, fmt.Sprintf(`"question number %d"`, i))
		if j < 0 {
			continue
		}
		if j < last {
			t.Errorf("question %d out of order:\n%s", i, out)
		}
		last = j
	}
	if strings.Contains(out, "undated note") {
		t.Errorf("an undated note came before the dated lines:\n%s", out)
	}
	if !strings.HasPrefix(lines[len(lines)-1], fmt.Sprintf("(%d of ", 8)) || !strings.Contains(lines[len(lines)-1], "the earliest, in order") {
		t.Errorf("no count line: %q", lines[len(lines)-1])
	}
	// With a query, the earliest matches in order.
	out = h.brain.Recall("number", RecallFromStart)
	if !strings.HasPrefix(out, `[`) || !strings.Contains(strings.Split(out, "\n")[0], `"question number 1"`) {
		t.Errorf("from start with a query:\n%s", out)
	}
	// The default is still the best matches, newest first.
	if out := h.brain.Recall("number", ""); !strings.Contains(strings.Split(out, "\n")[0], "question number 12") {
		t.Errorf("default order changed:\n%s", out)
	}
}

// The state block says where the record begins, and after a compaction
// up to when the window holds only the summary.
func TestRecordLineInState(t *testing.T) {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		if strings.Contains(call.Text(), "Summarise the following supervisor transcript") {
			return braintest.Response(braintest.Step{Text: "The user asked about auth many times."}), nil
		}
		return braintest.Response(braintest.Step{Text: braintest.Reply("ok", "")}), nil
	}}
	h := newHarness(t, m, func(c *Config) { c.WindowTokens = 300 })
	if _, err := h.ask("hello", ""); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	started := h.brain.Transcript().Started()
	want := "Record: this conversation started at " + started.Format("15:04:05") + " and the transcript above is complete since then"
	state := h.brain.StateNow()
	if i, j := strings.Index(state, "## Now"), strings.Index(state, want); i < 0 || j < i {
		t.Errorf("state has no record line after ## Now (want %q):\n%s", want, state)
	}
	if first := m.Calls()[0].Text(); !strings.Contains(first, want) {
		t.Errorf("the model call did not carry the record line:\n%s", first)
	}

	for i := 0; i < 40; i++ {
		h.brain.Heard(fmt.Sprintf("utterance number %d about auth and its tests", i), true)
		h.brain.Said(fmt.Sprintf("reply number %d", i), true)
	}
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "and now?"}); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	c, ok := h.brain.LastCompaction()
	if !ok || c.CoversTo.IsZero() {
		t.Fatalf("no compaction: %+v", c)
	}
	state = h.brain.StateNow()
	want = "Record: this conversation started at " + started.Format("15:04:05") + ". Up to " + c.CoversTo.Format("15:04:05") + " the transcript above holds only the compaction summary (its first message); every line since then is complete"
	if !strings.Contains(state, want) {
		t.Errorf("state after a compaction lacks %q:\n%s", want, state)
	}
	if strings.Contains(state, "nothing of it is missing") {
		t.Errorf("state still says the record is complete:\n%s", state)
	}
}
