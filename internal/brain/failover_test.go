package brain_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
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

// notices collects what the orchestrator asked to be told to the user.
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
// 10 min: the user is told once; not again within 30 min; again after.
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
// attempt leaves the reserve for the retry even when it keeps only the
// streak minimum: a call that hangs to the turn's deadline (the 01:01
// Vertex shape) is cut early and the retry still answers. Without the
// streak, a turn too short to keep the healthy minimum is not cut, and
// the same hang runs out the turn.
func TestFailoverReserveOnFailingStreak(t *testing.T) {
	defer SetFailoverReserve(400*time.Millisecond, 100*time.Millisecond, 800*time.Millisecond)()
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

// Tier-3 bench 2026-10-03 #7: Vertex answered 504 after about 20 s and
// the DeepSeek retry had about 1 s left. With another provider
// registered, a step's first attempt now leaves the reserve for the
// retry outside a failing streak too, whenever the attempt keeps the
// healthy minimum: the hang is cut, the retry answers with the reserve
// left, and a healthy call that answers inside its cut is untouched.
func TestFailoverReserveWithoutStreak(t *testing.T) {
	defer SetFailoverReserve(400*time.Millisecond, 100*time.Millisecond, 400*time.Millisecond)()
	var cut time.Duration
	hang := braintest.Step{FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
		start := time.Now()
		<-ctx.Done()
		cut = time.Since(start)
		return nil, err504 // Vertex's answer when its own deadline expires
	}}
	slowOK := braintest.Step{FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
		select {
		case <-time.After(300 * time.Millisecond):
			return braintest.Response(braintest.Step{Text: braintest.Reply("Slow but fine.", "")}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	primary := braintest.NewModel(hang, slowOK)
	h := newHarness(t, primary, func(c *Config) {
		c.Provider, c.Model = ProviderVertex, "gemini-3.8-flash"
		c.UtteranceTimeout, c.FinalGrace = 500*time.Millisecond, 500*time.Millisecond
	})
	var retryLeft time.Duration
	ds := braintest.NewModel(braintest.Step{FnCtx: func(ctx context.Context, _ braintest.Call) (*genai.GenerateContentResponse, error) {
		if dl, ok := ctx.Deadline(); ok {
			retryLeft = time.Until(dl)
		}
		return braintest.Response(braintest.Step{Text: braintest.Reply("Fine.", "")}), nil
	}})
	if err := h.brain.AddProvider(ProviderDeepSeek, ds); err != nil {
		t.Fatal(err)
	}
	rep, err := h.ask("how is auth?", "auth")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Fine." {
		t.Errorf("reply = %+v", rep)
	}
	if cut > 800*time.Millisecond {
		t.Errorf("first attempt ran %s, want cut at about 600 ms", cut)
	}
	if retryLeft < 300*time.Millisecond {
		t.Errorf("retry had %s left, want about the 400 ms reserve", retryLeft)
	}
	if ev := h.log.ofType("brain.failover"); len(ev) != 1 || payload(ev[0])["from"] != ProviderVertex {
		t.Errorf("brain.failover = %v", ev)
	}
	// A healthy call that answers inside its cut is not affected (the
	// streak the first turn started does not matter here: it answers).
	rep, err = h.ask("how is auth?", "auth")
	if err != nil || rep.Spoken != "Slow but fine." {
		t.Errorf("healthy slow call: %+v, %v", rep, err)
	}
	// Alone, a provider's first attempt keeps the whole turn.
	h2 := newHarness(t, braintest.NewModel(slowOK), func(c *Config) {
		c.UtteranceTimeout, c.FinalGrace = 500*time.Millisecond, 500*time.Millisecond
	})
	if rep, err := h2.ask("how is auth?", "auth"); err != nil || rep.Spoken != "Slow but fine." {
		t.Errorf("single provider: %+v, %v", rep, err)
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

// err499 is the transport error Vertex returned three times in the
// 2026-10-05 baseline (deep-ask-detail 08:43:08Z, …).
var err499 = genai.APIError{Code: 499, Message: "The operation was cancelled.", Status: "CANCELLED"}

// A 499 CANCELLED is retried once on the same provider before anything
// else: the reply comes from the retry, brain.retry is logged, no
// failover and no fallback.
func TestRetryOnce499(t *testing.T) {
	m := braintest.NewModel()
	m.Add(braintest.Step{Err: err499})
	m.Default = &braintest.Step{Text: braintest.Reply("Auth is running the tests.", "")}
	h := newHarness(t, m, func(c *Config) { c.Provider, c.Model = ProviderVertex, "gemini-3.5-flash" })
	ds := answering("from deepseek")
	_ = h.brain.AddProvider(ProviderDeepSeek, ds)
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Auth is running the tests." {
		t.Errorf("reply = %+v", rep)
	}
	if n := len(m.Calls()); n != 2 {
		t.Errorf("vertex calls = %d, want 2", n)
	}
	if len(ds.Calls()) != 0 || h.log.count("brain.failover") != 0 {
		t.Error("failed over on a 499")
	}
	ev := h.log.ofType("brain.retry")
	if len(ev) != 1 || payload(ev[0])["reason"] != "cancelled_499" {
		t.Errorf("brain.retry = %v", ev)
	}
}

// Only one retry per turn: a second 499 falls back as before.
func TestRetry499OncePerTurn(t *testing.T) {
	h := newHarness(t, failing(err499), nil)
	if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	if n := h.log.count("brain.retry"); n != 1 {
		t.Errorf("brain.retry = %d, want 1", n)
	}
}

// err403 is what Vertex answered on 2026-10-08 00:16 while its project
// was blocked.
var err403 = genai.APIError{Code: 403, Message: "Request rejected in accordance with our terms of use and policies.", Status: "PERMISSION_DENIED"}

// 2026-10-08 00:16: DeepSeek (selected) timed out, the step failed over
// to Vertex, which refused with 403, and the turn fell back although the
// Gemini key pool was registered and working. Now the refusal marks
// Vertex down and the step goes on to Gemini; the turns after it skip
// Vertex until the window passes, and then it is tried again.
func TestFailoverPastRefusedProvider(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 8, 0, 16, 0, 0, time.UTC)}
	primary := braintest.NewModel(braintest.Step{Err: context.DeadlineExceeded}, braintest.Step{Err: err504}, braintest.Step{Err: err504})
	h := newHarness(t, primary, func(c *Config) { c.Provider, c.Model, c.Now = ProviderDeepSeek, DefaultDeepSeekModel, clock.Now })
	vx := failing(err403)
	gm := answering("Forumind is waiting on your answer.")
	_ = h.brain.AddProvider(ProviderVertex, vx)
	_ = h.brain.AddProvider(ProviderGemini, gm)

	rep, err := h.ask("what is forumind asking?", "auth")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Forumind is waiting on your answer." {
		t.Errorf("reply = %+v", rep)
	}
	if len(vx.Calls()) != 1 || len(gm.Calls()) != 1 {
		t.Errorf("vertex calls = %d, gemini calls = %d", len(vx.Calls()), len(gm.Calls()))
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 2 || payload(ev[0])["to"] != ProviderVertex || payload(ev[1])["from"] != ProviderVertex ||
		payload(ev[1])["to"] != ProviderGemini || payload(ev[1])["reason"] != "http_403" {
		t.Errorf("brain.failover = %v", ev)
	}
	down := h.log.ofType("brain.provider_down")
	if len(down) != 1 || payload(down[0])["provider"] != ProviderVertex || !strings.Contains(fmt.Sprint(payload(down[0])["error"]), "PERMISSION_DENIED") {
		t.Errorf("brain.provider_down = %v", down)
	}
	if reason, _, ok := h.brain.ProviderDown(ProviderVertex); !ok || !strings.Contains(reason, "PERMISSION_DENIED") {
		t.Errorf("ProviderDown(vertex) = %q, %v", reason, ok)
	}
	if h.brain.Provider() != ProviderDeepSeek {
		t.Errorf("selection changed to %s", h.brain.Provider())
	}

	// Within the window Vertex is skipped: DeepSeek fails over straight
	// to Gemini.
	clock.Add(10 * time.Minute)
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	if len(vx.Calls()) != 1 || len(gm.Calls()) != 2 {
		t.Errorf("in the window: vertex calls = %d, gemini calls = %d", len(vx.Calls()), len(gm.Calls()))
	}

	// After it Vertex is tried again (and refuses again).
	clock.Add(25 * time.Minute)
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	if len(vx.Calls()) != 2 || len(gm.Calls()) != 3 {
		t.Errorf("after the window: vertex calls = %d, gemini calls = %d", len(vx.Calls()), len(gm.Calls()))
	}
	if n := h.log.count("brain.provider_down"); n != 2 {
		t.Errorf("brain.provider_down count = %d, want 2", n)
	}
}

// A selected provider that refuses is routed around for the window: the
// turn after the refusal starts on the target without calling it, and
// selecting it again retries it at once.
func TestSelectedProviderDown(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 8, 0, 16, 0, 0, time.UTC)}
	vx := failing(err403)
	h := newHarness(t, vx, func(c *Config) { c.Provider, c.Model, c.Now = ProviderVertex, "gemini-3.5-flash", clock.Now })
	gm := answering("Fine.")
	_ = h.brain.AddProvider(ProviderGemini, gm)

	for i := 0; i < 2; i++ {
		if _, err := h.ask("how is auth?", "auth"); err != nil {
			t.Fatalf("turn %d fell back: %v", i, err)
		}
		clock.Add(time.Minute)
	}
	if n := len(vx.Calls()); n != 1 {
		t.Errorf("vertex calls = %d, want 1 (the second turn routes around it)", n)
	}
	if calls := gm.Calls(); len(calls) != 2 || calls[1].Model != "gemini-3.5-flash" {
		t.Errorf("gemini calls = %+v", calls)
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 2 || payload(ev[1])["reason"] != "provider_down" {
		t.Errorf("brain.failover = %v", ev)
	}

	if err := h.brain.SetModel(ProviderVertex, "gemini-3.8-flash", "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := h.brain.ProviderDown(ProviderVertex); ok {
		t.Error("still down after it was selected again")
	}
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	if n := len(vx.Calls()); n != 2 {
		t.Errorf("vertex calls after reselecting = %d, want 2", n)
	}
}

// A 401/403 with no other provider falls back as before, and a 400 is
// still not a refusal.
func TestRefusedAlone(t *testing.T) {
	h := newHarness(t, failing(err403), nil)
	if _, err := h.ask("how is auth?", "auth"); !errors.Is(err, ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	if h.log.count("brain.failover") != 0 {
		t.Error("brain.failover without a second provider")
	}
}

// errReset is the error DeepSeek's endpoint gave on 2026-10-08
// 03:04:59, 20 ms into an utterance turn, built with the real chain the
// HTTP client returns (openaicompat wraps it with %w).
var errReset = fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://api.deepseek.com/chat/completions",
	Err: &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}})

// 2026-10-08 03:04:59: a connection reset fell straight back to the
// fallback line. Now a transport error is retried once on the same
// provider and the reply comes from the retry: brain.retry {reason:
// transport}, no failover, no fallback.
func TestRetryOnceOnTransportError(t *testing.T) {
	for name, cause := range map[string]error{
		"reset":       errReset,
		"eof":         fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://x", Err: io.EOF}),
		"broken pipe": fmt.Errorf("deepseek: %w", &net.OpError{Op: "write", Net: "tcp", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}}),
		"dial":        fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}),
		"tls":         fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://x", Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}}),
		"body read":   fmt.Errorf("deepseek: read response: %w", io.ErrUnexpectedEOF),
	} {
		t.Run(name, func(t *testing.T) {
			m := braintest.NewModel()
			m.Add(braintest.Step{Err: cause})
			m.Default = &braintest.Step{Text: braintest.Reply("Fork it only if you must.", "")}
			h := newHarness(t, m, func(c *Config) { c.Provider, c.Model = ProviderDeepSeek, DefaultDeepSeekModel })
			other := answering("from gemini")
			_ = h.brain.AddProvider(ProviderGemini, other)
			rep, err := h.ask("is there a better way than forking?", "")
			if err != nil {
				t.Fatalf("turn fell back: %v", err)
			}
			if rep.Spoken != "Fork it only if you must." {
				t.Errorf("reply = %+v", rep)
			}
			if n := len(m.Calls()); n != 2 {
				t.Errorf("deepseek calls = %d, want 2", n)
			}
			if len(other.Calls()) != 0 || h.log.count("brain.failover") != 0 || h.log.count("brain.fallback") != 0 {
				t.Error("failed over or fell back on one transport error")
			}
			ev := h.log.ofType("brain.retry")
			if len(ev) != 1 || payload(ev[0])["reason"] != "transport" || payload(ev[0])["provider"] != ProviderDeepSeek {
				t.Errorf("brain.retry = %v", ev)
			}
		})
	}
}

// A transport error that repeats on the retry fails over like a
// timeout: brain.retry, then brain.failover {reason: transport}, and
// the other provider answers.
func TestTransportErrorFailsOverAfterRetry(t *testing.T) {
	h := newHarness(t, failing(errReset), func(c *Config) { c.Provider, c.Model = ProviderDeepSeek, DefaultDeepSeekModel })
	other := answering("Fork it only if you must.")
	_ = h.brain.AddProvider(ProviderGemini, other)
	rep, err := h.ask("is there a better way than forking?", "")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Fork it only if you must." {
		t.Errorf("reply = %+v", rep)
	}
	if h.log.count("brain.retry") != 1 {
		t.Errorf("brain.retry = %d, want 1", h.log.count("brain.retry"))
	}
	ev := h.log.ofType("brain.failover")
	if len(ev) != 1 || payload(ev[0])["reason"] != "transport" || payload(ev[0])["to"] != ProviderGemini {
		t.Errorf("brain.failover = %v", ev)
	}
}

// With nowhere to fail over to, a repeated transport error falls back
// with reason transport, after one retry.
func TestTransportErrorAloneFallsBack(t *testing.T) {
	h := newHarness(t, failing(errReset), func(c *Config) { c.Provider, c.Model = ProviderDeepSeek, DefaultDeepSeekModel })
	if _, err := h.ask("is there a better way than forking?", ""); !errors.Is(err, ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	if h.log.count("brain.retry") != 1 {
		t.Errorf("brain.retry = %d, want 1", h.log.count("brain.retry"))
	}
	ev := h.log.ofType("brain.fallback")
	if len(ev) != 1 || payload(ev[0])["reason"] != "transport" {
		t.Errorf("brain.fallback = %v", ev)
	}
}

// Not a transport error: an HTTP status, a client error, the turn's own
// cancellation or deadline.
func TestTransportErrorClassification(t *testing.T) {
	for name, err := range map[string]error{
		"400":       genai.APIError{Code: 400, Message: "bad request"},
		"compat":    fmt.Errorf("deepseek: %w", &APIError{Provider: "deepseek", Status: 400}),
		"cancelled": fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://x", Err: context.Canceled}),
		"deadline":  fmt.Errorf("deepseek: %w", &url.Error{Op: "Post", URL: "https://x", Err: context.DeadlineExceeded}),
	} {
		if IsTransportError(err) {
			t.Errorf("%s: classified as transport: %v", name, err)
		}
	}
	if !IsTransportError(errReset) {
		t.Errorf("reset not classified as transport")
	}
}

// FailureReason names what broke in plain words, never the vendor or
// the raw error.
func TestFailureReason(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errReset, "the connection to the orchestrator's model broke"},
		{context.DeadlineExceeded, "the orchestrator's model did not answer in time"},
		{err504, "the orchestrator's model had a server error"},
		{err403, "the orchestrator's model provider refused the daemon's key or credentials; the user must fix the account, a retry will not help"},
		{err402, "the orchestrator's model provider says the account balance is used up; the user must top it up, a retry will not help"},
		{errors.New("bad reply"), "the orchestrator's model returned an error"},
		{fmt.Errorf("%w: q", ErrAllKeysRateLimited), "the orchestrator's model is rate-limited"},
	} {
		if got := FailureReason(fmt.Errorf("%w (model_error): %w", ErrFallback, c.err)); got != c.want {
			t.Errorf("FailureReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
