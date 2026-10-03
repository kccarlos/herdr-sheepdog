package brain_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// err504 is the error Vertex returned on 2026-10-02 01:01–01:07.
var err504 = genai.APIError{Code: 504, Message: "Deadline expired before operation could complete.", Status: "DEADLINE_EXCEEDED"}

// failing is a model whose every call fails with err.
func failing(err error) *braintest.Model {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Err: err}
	return m
}

// answering is a model whose every call answers spoken.
func answering(spoken string) *braintest.Model {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Text: braintest.Reply(spoken, "")}
	return m
}

// fakeClock is a settable clock for the streak and notice windows.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// notices collects what the brain asked to be told to the CEO.
type notices struct {
	ch chan string
}

func newNotices(b *Brain) *notices {
	n := &notices{ch: make(chan string, 8)}
	b.SetNotifier(func(notice Notice) { n.ch <- notice.English })
	return n
}

// next returns the notice sent so far, or "" when none was (the turn's
// after-work has been waited for).
func (n *notices) next() string {
	select {
	case s := <-n.ch:
		return s
	default:
		return ""
	}
}

// A 504 from the selected provider: the step is retried once on the next
// registered provider with that provider's model, the reply comes from
// there, brain.failover is logged, and the selection is unchanged.
func TestFailoverOn504(t *testing.T) {
	h := newHarness(t, failing(err504), func(c *Config) { c.Provider, c.Model = ProviderVertex, "gemini-3.8-flash" })
	ds := answering("Auth is running the tests.")
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Auth is running the tests." {
		t.Errorf("reply = %+v", rep)
	}
	if calls := ds.Calls(); len(calls) != 1 || calls[0].Model != DefaultDeepSeekModel {
		t.Errorf("deepseek calls = %+v", calls)
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 1 {
		t.Fatalf("brain.failover = %v", ev)
	}
	if p := payload(ev[0]); p["from"] != ProviderVertex || p["to"] != ProviderDeepSeek || p["reason"] != "http_504" || p["to_model"] != DefaultDeepSeekModel {
		t.Errorf("brain.failover = %v", p)
	}
	if h.brain.Provider() != ProviderVertex || h.brain.Model() != "gemini-3.8-flash" {
		t.Errorf("selection changed to %s/%s", h.brain.Provider(), h.brain.Model())
	}
	if p := payload(h.log.ofType("brain.step")[0]); p["provider"] != ProviderDeepSeek {
		t.Errorf("brain.step = %v", p)
	}
	if h.log.count("brain.fallback") != 0 {
		t.Error("brain.fallback logged")
	}
}

// Between the two Gemini endpoints the model id is kept; a rate limit
// is a failover reason too.
func TestFailoverKeepsGeminiModelAcrossEndpoints(t *testing.T) {
	h := newHarness(t, failing(fmt.Errorf("%w: quota", ErrAllKeysRateLimited)), func(c *Config) { c.Model = "gemini-3.8-flash" })
	vx := answering("Fine.")
	if err := h.brain.AddProvider(ProviderVertex, vx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	if calls := vx.Calls(); len(calls) != 1 || calls[0].Model != "gemini-3.8-flash" {
		t.Errorf("vertex calls = %+v", calls)
	}
	if p := payload(h.log.ofType("brain.failover")[0]); p["reason"] != "rate_limited" || p["to"] != ProviderVertex {
		t.Errorf("brain.failover = %v", p)
	}
}

// Not every error fails over: a bad request is the same anywhere, and
// with no other provider registered there is nowhere to go.
func TestNoFailover(t *testing.T) {
	t.Run("client error", func(t *testing.T) {
		h := newHarness(t, failing(genai.APIError{Code: 400, Message: "bad request", Status: "INVALID_ARGUMENT"}), nil)
		ds := answering("Fine.")
		_ = h.brain.AddProvider(ProviderDeepSeek, ds)
		if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
			t.Fatalf("err = %v, want ErrFallback", err)
		}
		if len(ds.Calls()) != 0 || h.log.count("brain.failover") != 0 {
			t.Error("failed over on a 400")
		}
	})
	t.Run("alone", func(t *testing.T) {
		h := newHarness(t, failing(err504), nil)
		if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
			t.Fatalf("err = %v, want ErrFallback", err)
		}
		if h.log.count("brain.failover") != 0 {
			t.Error("brain.failover without a second provider")
		}
	})
}

// The retry happens once per turn: when the second provider fails too,
// the turn falls back as before.
func TestFailoverOncePerTurn(t *testing.T) {
	h := newHarness(t, failing(err504), nil)
	ds := failing(errors.New("deepseek: HTTP 503: busy"))
	_ = h.brain.AddProvider(ProviderDeepSeek, ds)
	if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	if n := len(ds.Calls()); n != 1 || h.log.count("brain.failover") != 1 {
		t.Errorf("deepseek calls = %d, failovers = %d", n, h.log.count("brain.failover"))
	}
}

// Three consecutive failovers away from the selected provider within
// 10 min: the CEO is told once; not again within 30 min; again after.
// A success on the selected provider ends the streak.
func TestFailoverNotice(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 2, 1, 1, 0, 0, time.UTC)}
	primary := braintest.NewModel()
	h := newHarness(t, primary, func(c *Config) { c.Provider, c.Model, c.Now = ProviderVertex, "gemini-3.8-flash", clock.Now })
	_ = h.brain.AddProvider(ProviderDeepSeek, answering("Fine."))
	n := newNotices(h.brain)
	turn := func(fail bool) {
		t.Helper()
		if fail {
			primary.Add(braintest.Step{Err: err504})
		} else {
			primary.Add(braintest.Step{Text: braintest.Reply("Fine.", "")})
		}
		if _, err := h.ask("how is auth?", "auth"); err != nil {
			t.Fatal(err)
		}
		h.brain.Quiesce() // a notice goes out as after-turn work
		clock.Add(time.Minute)
	}

	turn(true)
	turn(true)
	turn(false) // the selected provider answered: the streak ends
	turn(true)
	turn(true)
	if s := n.next(); s != "" {
		t.Fatalf("notice before three consecutive failovers: %q", s)
	}
	turn(true)
	if s := n.next(); s != "My usual model is having trouble; a backup is answering for now." {
		t.Fatalf("notice = %q", s)
	}
	if ev := h.log.ofType("brain.failover_notice"); len(ev) != 1 || payload(ev[0])["failovers"] != 3 {
		t.Errorf("brain.failover_notice = %v", ev)
	}
	turn(true)
	turn(true)
	turn(true)
	if s := n.next(); s != "" {
		t.Fatalf("second notice within 30 min: %q", s)
	}
	// 30 min later: a fresh streak of three within 10 min is told again.
	clock.Add(30 * time.Minute)
	turn(true)
	turn(true)
	if s := n.next(); s != "" {
		t.Fatalf("notice before a fresh streak of three: %q", s)
	}
	turn(true)
	if s := n.next(); s == "" {
		t.Fatal("no notice 30 min later")
	}
	if h.brain.Provider() != ProviderVertex {
		t.Errorf("selection changed to %s", h.brain.Provider())
	}
}

// While the selected provider is on a failing streak, a step's first
// attempt leaves the reserve for the retry: a call that hangs to the
// turn's deadline (the 01:01 Vertex shape) is cut early and the retry
// still answers. Without the streak the same hang runs out the turn.
func TestFailoverReserveOnFailingStreak(t *testing.T) {
	defer SetFailoverReserve(400*time.Millisecond, 100*time.Millisecond)()
	hang := braintest.Step{FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	primary := braintest.NewModel(hang, braintest.Step{Err: err504}, hang)
	h := newHarness(t, primary, func(c *Config) {
		c.UtteranceTimeout, c.FinalGrace = 500*time.Millisecond, 500*time.Millisecond
	})
	_ = h.brain.AddProvider(ProviderDeepSeek, answering("Fine."))

	// No streak: the hang takes the whole turn, nothing is left to fail
	// over with.
	if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
		t.Fatalf("first turn err = %v, want ErrFallback", err)
	}
	// A fast 504 fails over and starts the streak.
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	// On the streak the hang is cut at deadline - reserve and retried.
	rep, err := h.ask("how is auth?", "auth")
	if err != nil {
		t.Fatalf("third turn fell back: %v", err)
	}
	if rep.Spoken != "Fine." {
		t.Errorf("reply = %+v", rep)
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 2 || payload(ev[1])["reason"] != "timeout" {
		t.Errorf("brain.failover = %v", ev)
	}
}

// The transcript stays provider-neutral: after a failover the next
// turn's window, back on the selected provider, carries the answer the
// other provider gave.
func TestFailoverTranscriptCarriesOver(t *testing.T) {
	primary := braintest.NewModel(braintest.Step{Err: err504}, braintest.Step{Text: braintest.Reply("Still fine.", "")})
	h := newHarness(t, primary, nil)
	_ = h.brain.AddProvider(ProviderDeepSeek, answering("Auth is green."))
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	calls := primary.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Text(), "Auth is green.") {
		t.Errorf("the selected provider's next call lacks the failover's answer")
	}
}

// The compaction's summary call fails over like a turn's step: a 504 on
// the selected provider, the summary written by the next one.
func TestCompactionSummaryFailsOver(t *testing.T) {
	isSummary := func(c braintest.Call) bool {
		return strings.Contains(c.Text(), "Summarise the following supervisor transcript")
	}
	primary := braintest.NewModel()
	primary.Default = &braintest.Step{Fn: func(c braintest.Call) (*genai.GenerateContentResponse, error) {
		if isSummary(c) {
			return nil, err504
		}
		return braintest.Response(braintest.Step{Text: braintest.Reply("Fine.", "")}), nil
	}}
	ds := braintest.NewModel()
	ds.Default = &braintest.Step{Text: "The CEO asked about auth many times."}
	h := newHarness(t, primary, func(c *Config) { c.WindowTokens = 300 })
	_ = h.brain.AddProvider(ProviderDeepSeek, ds)
	fillWindow(h)
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	if c, ok := h.brain.LastCompaction(); !ok || c.Dropped == 0 {
		t.Fatalf("no compaction: %+v (events %v)", c, h.log.ofType("brain.compaction"))
	}
	if calls := ds.Calls(); len(calls) != 1 || !isSummary(calls[0]) || calls[0].Model != DefaultDeepSeekModel {
		t.Errorf("deepseek calls = %d", len(calls))
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 1 {
		t.Fatalf("brain.failover = %v", ev)
	}
	if p := payload(ev[0]); p["call"] != "compaction" || p["from"] != ProviderGemini || p["to"] != ProviderDeepSeek || p["reason"] != "http_504" {
		t.Errorf("brain.failover = %v", p)
	}
	if h.brain.Provider() != ProviderGemini {
		t.Errorf("selection changed to %s", h.brain.Provider())
	}
}
