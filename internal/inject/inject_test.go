package inject

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// recLog records events.
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.events))
	for _, e := range l.events {
		out = append(out, e.Type)
	}
	return out
}

type fakePusher struct {
	mu     sync.Mutex
	pushes []string
}

func (p *fakePusher) Push(text string, _ voice.Urgency) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pushes = append(p.pushes, text)
	return nil
}

func codexScreen(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", "codex-approval-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type harness struct {
	store *state.Store
	herdr *Herdr
	sup   *supervisor.Supervisor
	inj   *Injector
	log   *recLog
	push  *fakePusher
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	log := &recLog{}
	store, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHerdr(nil, log)
	push := &fakePusher{}
	sup, err := supervisor.New(store, h, nil, push, log, supervisor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	inj := NewInjector(store, h, sup, log, func() time.Time { return now })
	return &harness{store: store, herdr: h, sup: sup, inj: inj, log: log, push: push}
}

func TestBlockedRunsFullChain(t *testing.T) {
	h := newHarness(t)
	row, err := h.inj.Blocked(context.Background(), BlockedRequest{PaneID: "fake:1", Alias: "auth", Screen: codexScreen(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !row.Synthetic || row.Alias != "auth" || row.State != herdr.StateBlocked || row.Kind != "codex" {
		t.Fatalf("row = %+v", row)
	}
	types := strings.Join(h.log.types(), " ")
	for _, want := range []string{"web.inject", "herdr.state", "herdr.transition", "herdr.read", "pending.created"} {
		if !strings.Contains(types, want) {
			t.Errorf("missing %s in %s", want, types)
		}
	}
	if p, ok := h.store.Pending(); !ok || p.PaneID != "fake:1" || p.Kind != state.PendingAnswerPrompt {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	if len(h.push.pushes) != 1 || !strings.Contains(h.push.pushes[0], "auth asks to run") {
		t.Fatalf("pushes = %v", h.push.pushes)
	}

	// The read served the injected screen.
	read, err := h.herdr.Read(context.Background(), "fake:1", herdr.SourceRecentUnwrapped, 5)
	if err != nil || strings.Count(read.Text, "\n") > 5 || !read.Truncated {
		t.Fatalf("read = %+v err=%v", read, err)
	}

	// A typed "yes" (the bare-answer fast path) → send_keys to the
	// synthetic pane → pending.confirmed.
	h.sup.ShepherdInput(supervisor.InputTyped, "yes")
	res, err := h.sup.ToSupervisor(context.Background(), "yes")
	if err != nil || !strings.Contains(res.Spoken, "Told auth yes") {
		t.Fatalf("answer = %+v err=%v", res, err)
	}
	if h.herdr.Sends("fake:1") != 1 {
		t.Fatalf("sends = %d", h.herdr.Sends("fake:1"))
	}
	types = strings.Join(h.log.types(), " ")
	if !strings.Contains(types, "herdr.send pending.confirmed") {
		t.Fatalf("confirm chain missing in %s", types)
	}

	// done → the row moves on; a herdr-sourced close cannot remove it.
	row, err = h.inj.Done(context.Background(), "fake:1")
	if err != nil || row.State != herdr.StateDone {
		t.Fatalf("done = %+v err=%v", row, err)
	}
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "fake:1", To: herdr.StateUnknown, Closed: true, Source: "snapshot"})
	if _, ok := h.store.Get("fake:1"); !ok {
		t.Fatal("snapshot close removed the synthetic pane")
	}
	if err := h.inj.Remove(context.Background(), "fake:1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.store.Get("fake:1"); ok || h.herdr.IsSynthetic("fake:1") {
		t.Fatal("remove did not drop the pane")
	}
}

// TestScreenReplacesTextWithoutTransition: Screen swaps a synthetic
// pane's text but never touches the supervisor — no pending action, no
// push, and Read serves the new text — the mock counterpart of a worker
// showing a new prompt with herdr's own status report unchanged
// (docs/dev/roles.md "blocked -> blocked is a block").
func TestScreenReplacesTextWithoutTransition(t *testing.T) {
	h := newHarness(t)
	if _, err := h.inj.Blocked(context.Background(), BlockedRequest{PaneID: "fake:1", Alias: "auth", Screen: codexScreen(t)}); err != nil {
		t.Fatal(err)
	}
	before := len(h.push.pushes)

	row, err := h.inj.Screen("fake:1", "a different screen entirely")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != herdr.StateBlocked {
		t.Errorf("Screen moved the state: %+v", row)
	}
	if len(h.push.pushes) != before {
		t.Errorf("Screen pushed something: %v", h.push.pushes)
	}
	read, err := h.herdr.Read(context.Background(), "fake:1", herdr.SourceRecentUnwrapped, 0)
	if err != nil || read.Text != "a different screen entirely" {
		t.Fatalf("read = %+v err=%v", read, err)
	}

	if _, err := h.inj.Screen("fake:1", ""); err == nil {
		t.Error("empty screen accepted")
	}
	if _, err := h.inj.Screen("nope", "x"); !errors.Is(err, ErrNotSynthetic) {
		t.Errorf("unknown pane: %v", err)
	}
}

func TestInjectorRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.inj.Blocked(ctx, BlockedRequest{Screen: "x"}); err == nil {
		t.Error("missing pane_id accepted")
	}
	if _, err := h.inj.Blocked(ctx, BlockedRequest{PaneID: "p"}); err == nil {
		t.Error("missing screen accepted")
	}
	if _, err := h.inj.Done(ctx, "nope"); !errors.Is(err, ErrNotSynthetic) {
		t.Errorf("done of unknown pane: %v", err)
	}
	if _, err := h.inj.SetState(ctx, "p", "weird", ""); err == nil {
		t.Error("invalid state accepted")
	}
	// A pane herdr owns cannot be faked.
	if _, _, err := h.store.Apply(herdr.Transition{PaneID: "w1:p1", To: herdr.StateWorking, Agent: "codex", Source: "socket"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.inj.Blocked(ctx, BlockedRequest{PaneID: "w1:p1", Screen: "x"}); !errors.Is(err, ErrNotSynthetic) {
		t.Errorf("real pane faked: %v", err)
	}
}

func TestMockHerdrWithoutInner(t *testing.T) {
	h := NewHerdr(nil, nil)
	ctx := context.Background()
	if _, err := h.Read(ctx, "x", herdr.SourceVisible, 0); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("read unknown: %v", err)
	}
	if _, err := h.Prompt(ctx, "x", "hi"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("prompt unknown: %v", err)
	}
	if err := h.SendKeys(ctx, "x", "enter"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("keys unknown: %v", err)
	}
	if err := h.SendKeys(ctx, "x"); err == nil {
		t.Error("no keys accepted")
	}
	h.SetScreen("a", "claude", "line1\nline2\n", herdr.StateWorking)
	h.SetScreen("b", "", "screen b", herdr.StateBlocked)
	list, err := h.ListAgents(ctx)
	if err != nil || len(list) != 2 || list[0].PaneID != "a" || list[0].Agent != "claude" || list[1].Status != herdr.StateBlocked {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if _, err := h.Prompt(ctx, "a", "do it"); err != nil {
		t.Fatal(err)
	}
	read, _ := h.Read(ctx, "a", herdr.SourceRecentUnwrapped, 0)
	if !strings.Contains(read.Text, "› do it") {
		t.Fatalf("prompt not appended: %q", read.Text)
	}
	if err := h.Interrupt(ctx, "a"); err != nil || h.Sends("a") != 2 {
		t.Fatalf("interrupt: err=%v sends=%d", err, h.Sends("a"))
	}
	if !h.Mock() {
		t.Error("Mock() should be true without inner")
	}
}

// innerHerdr is a real-client stand-in for the decorator tests.
type innerHerdr struct {
	reads int
	list  []herdr.AgentInfo
	err   error
}

func (i *innerHerdr) Read(context.Context, string, herdr.ReadSource, int) (herdr.ReadResult, error) {
	i.reads++
	return herdr.ReadResult{Text: "real"}, nil
}
func (i *innerHerdr) Prompt(context.Context, string, string) (herdr.AgentInfo, error) {
	return herdr.AgentInfo{}, nil
}
func (i *innerHerdr) SendKeys(context.Context, string, ...string) error { return nil }
func (i *innerHerdr) Interrupt(context.Context, string) error           { return nil }
func (i *innerHerdr) ListAgents(context.Context) ([]herdr.AgentInfo, error) {
	return i.list, i.err
}

func TestDecoratorDelegatesRealPanes(t *testing.T) {
	inner := &innerHerdr{list: []herdr.AgentInfo{{PaneID: "w1:p1", Agent: "codex"}}}
	h := NewHerdr(inner, nil)
	ctx := context.Background()
	if r, err := h.Read(ctx, "w1:p1", herdr.SourceVisible, 0); err != nil || r.Text != "real" || inner.reads != 1 {
		t.Fatalf("read = %+v err=%v", r, err)
	}
	h.SetScreen("fake:1", "claude", "fake", herdr.StateBlocked)
	if r, _ := h.Read(ctx, "fake:1", herdr.SourceVisible, 0); r.Text != "fake" || inner.reads != 1 {
		t.Fatalf("synthetic read went to inner: %+v", r)
	}
	list, err := h.ListAgents(ctx)
	if err != nil || len(list) != 2 || list[0].PaneID != "fake:1" || list[1].PaneID != "w1:p1" {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	inner.err = errors.New("down")
	if list, err := h.ListAgents(ctx); err != nil || len(list) != 1 {
		t.Fatalf("inner error with synthetic panes: %v %v", list, err)
	}
	h.Remove("fake:1")
	if _, err := h.ListAgents(ctx); err == nil {
		t.Fatal("inner error must surface when nothing is synthetic")
	}
	if h.Mock() {
		t.Error("Mock() should be false with inner")
	}
}

func TestRouter(t *testing.T) {
	var r Router
	cases := []struct {
		in, tool string
		arg      string // expected argument value (any key), "" to skip
	}{
		// The router classifies nothing any more: the words go over
		// verbatim, exactly as the voice model would relay them, and
		// the supervisor's own fast paths and orchestrator decide what they
		// are. Only a bare stop word is routed, and only as a shortcut.
		{"yes", voice.ToolToSupervisor, "yes"},
		{"Yes, go ahead.", voice.ToolToSupervisor, "Yes, go ahead."},
		{"option 2", voice.ToolToSupervisor, "option 2"},
		{"stop", voice.ToolStop, ""},
		{"Cancel", voice.ToolStop, ""},
		{"no no", voice.ToolStop, ""},
		// audit B8: "abort" and "halt" used to be in the router's own
		// stop regex but not in the supervisor's bareStopWords, so the
		// same word stopped GPT-Live (routed here) but became an
		// ordinary orchestrator turn relayed through to_supervisor on Gemini.
		// Both now read supervisor.BareStopWords.
		{"abort", voice.ToolStop, ""},
		{"Halt.", voice.ToolStop, ""},
		{"No!", voice.ToolToSupervisor, "No!"}, // an answer, not a stop
		{"what's going on?", voice.ToolToSupervisor, "what's going on?"},
		{"tell auth to skip the flaky test", voice.ToolToSupervisor, "tell auth to skip the flaky test"},
		{"remember that docs is on hold", voice.ToolToSupervisor, "remember that docs is on hold"},
		{"blah blah", voice.ToolToSupervisor, "blah blah"},
		{"", "", ""},
		{"   ", "", ""},
	}
	for _, tc := range cases {
		call, ok := r.Route(tc.in)
		if tc.tool == "" {
			if ok {
				t.Errorf("Route(%q) = %+v, want rejected", tc.in, call)
			}
			continue
		}
		if !ok || call.Name != tc.tool {
			t.Errorf("Route(%q) = %+v ok=%v, want %s", tc.in, call, ok, tc.tool)
			continue
		}
		if tc.arg != "" {
			found := false
			for _, v := range call.Args {
				if v == tc.arg {
					found = true
				}
			}
			if !found {
				t.Errorf("Route(%q) args = %v, want %q", tc.in, call.Args, tc.arg)
			}
		}
		if call.ID == "" {
			t.Errorf("Route(%q) has no id", tc.in)
		}
	}
}
