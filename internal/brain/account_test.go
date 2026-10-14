package brain_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// err402 is DeepSeek's answer once the prepaid balance is used up.
var err402 = &APIError{Provider: ProviderDeepSeek, Status: 402, Message: "Insufficient Balance"}

func TestAccountProblemOf(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		want   AccountProblem
	}{
		{401, "Authentication Fails", ProblemInvalidKey},
		{403, "PERMISSION_DENIED", ProblemInvalidKey},
		{400, "API key not valid. Please pass a valid API key.", ProblemInvalidKey},
		{400, "bad tool schema", ProblemNone},
		{402, "Insufficient Balance", ProblemBalance},
		{429, "Quota exceeded for metric", ProblemQuota},
		{429, "Rate limit reached, slow down", ProblemNone},
		{503, "overloaded", ProblemNone},
		// Google words a per-minute limit like a used-up quota; only
		// the quota id tells them apart.
		{429, "You exceeded your current quota, please check your plan and billing details. quotaId:GenerateRequestsPerMinutePerProjectPerModel-FreeTier", ProblemNone},
		{429, "You exceeded your current quota, please check your plan and billing details. quotaId:GenerateRequestsPerDayPerProjectPerModel-FreeTier", ProblemQuota},
		// Vertex's shared capacity is transient.
		{429, "Resource exhausted. Please try again later.", ProblemNone},
		// Text only: a Live close frame, a realtime error event.
		{0, "failed to receive setup complete: websocket: close 1007 (invalid payload data): API key not valid. Please pass a valid API key.", ProblemInvalidKey},
		{0, "gptlive: invalid_api_key: Incorrect API key provided", ProblemInvalidKey},
		{0, "gptlive: insufficient_quota: You exceeded your current quota", ProblemQuota},
		{0, "websocket: close 1011: internal error encountered", ProblemNone},
		{0, "websocket: bad handshake", ProblemNone},
	} {
		if got := AccountProblemOf(tc.status, tc.msg); got != tc.want {
			t.Errorf("%d %q = %q, want %q", tc.status, tc.msg, got, tc.want)
		}
	}
}

func TestAccountProblemOfErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want AccountProblem
	}{
		{nil, ProblemNone},
		{err402, ProblemBalance},
		{fmt.Errorf("%w (model_error): %w", ErrFallback, err402), ProblemBalance},
		{genai.APIError{Code: 400, Message: "API key not valid.", Status: "INVALID_ARGUMENT"}, ProblemInvalidKey},
		{err403, ProblemInvalidKey},
		{err504, ProblemNone},
		{context.DeadlineExceeded, ProblemNone},
		{errors.New("gptlive: insufficient_quota: x"), ProblemQuota},
	} {
		if got := AccountProblemOfErr(tc.err); got != tc.want {
			t.Errorf("%v = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// The alert names the service and the fix: the screen line may carry
// the variable and the console, the spoken one carries neither.
func TestNewAccountAlert(t *testing.T) {
	a := NewAccountAlert(RoleOrchestrator, ProviderDeepSeek, ProblemBalance, true)
	if !strings.Contains(a.Message, "DeepSeek") || !strings.Contains(a.Message, "orchestrator") {
		t.Errorf("message = %q", a.Message)
	}
	if a.Fix != "top up the account (https://platform.deepseek.com)" {
		t.Errorf("fix = %q", a.Fix)
	}
	if !strings.Contains(a.Spoken.English, "DeepSeek") || !strings.Contains(a.Spoken.English, "a backup model answers for now") {
		t.Errorf("spoken = %q", a.Spoken.English)
	}
	for _, s := range []string{a.Spoken.English, a.Spoken.Chinese} {
		if strings.Contains(s, "http") || strings.Contains(s, "_API_KEY") {
			t.Errorf("spoken line carries a URL or a variable: %q", s)
		}
	}
	v := NewAccountAlert(RoleVoice, "openai", ProblemInvalidKey, true)
	if !strings.Contains(v.Message, "OpenAI rejected the API key, so the voice") || strings.Contains(v.Spoken.English, "backup") {
		t.Errorf("voice alert = %+v", v)
	}
	if !strings.Contains(v.Fix, "OPENAI_API_KEY") {
		t.Errorf("voice fix = %q", v.Fix)
	}
	if x := NewAccountAlert(RoleOrchestrator, "myproxy", ProblemQuota, false); !strings.Contains(x.Message, "myproxy") || !strings.Contains(x.Fix, "myproxy's console") {
		t.Errorf("generic alert = %+v", x)
	}
	if (NewAccountAlert(RoleVoice, "gemini", ProblemNone, false) != AccountAlert{}) {
		t.Error("ProblemNone gives an alert")
	}
}

// A problem is news once per role and provider until it is cleared.
func TestAccountAlertsOnce(t *testing.T) {
	s := NewAccountAlerts()
	var seen []string
	s.Subscribe(func(a AccountAlert, active bool) {
		seen = append(seen, fmt.Sprintf("%s/%s/%s/%v", a.Role, a.Provider, a.Problem, active))
	})
	bal := NewAccountAlert(RoleOrchestrator, ProviderDeepSeek, ProblemBalance, false)
	if !s.Report(bal) || s.Report(bal) {
		t.Fatal("the same problem was news twice")
	}
	if !s.Report(NewAccountAlert(RoleVoice, ProviderGemini, ProblemQuota, false)) {
		t.Fatal("another role's problem was not news")
	}
	if got := s.Active(); len(got) != 2 || got[0].Role != RoleOrchestrator {
		t.Errorf("active = %+v", got)
	}
	if !s.Clear(RoleOrchestrator, ProviderDeepSeek) || s.Clear(RoleOrchestrator, ProviderDeepSeek) {
		t.Error("clear")
	}
	if !s.Report(bal) {
		t.Error("not news again after it was cleared")
	}
	want := "orchestrator/deepseek/balance/true voice/gemini/quota/true orchestrator/deepseek/balance/false orchestrator/deepseek/balance/true"
	if got := strings.Join(seen, " "); got != want {
		t.Errorf("subscriber saw %q", got)
	}
	var nilSet *AccountAlerts
	if nilSet.Report(bal) || nilSet.Active() != nil {
		t.Error("nil set")
	}
}

// The tier-1 shape: one orchestrator provider and its balance is used
// up. The user's own turn falls back (the supervisor's answer then says
// why, from AccountAlertFor), the notice is not spoken on top of it, and
// event turns do not tell it again; once the provider answers, a new
// failure is news again.
func TestAccountProblemAlone(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}
	ds := failing(err402)
	h := newHarness(t, ds, func(c *Config) { c.Provider, c.Model, c.Now = ProviderDeepSeek, DefaultDeepSeekModel, clock.Now })
	n := newNotices(h.brain)

	_, err := h.ask("how is auth?", "auth")
	if !errors.Is(err, ErrFallback) {
		t.Fatalf("err = %v, want ErrFallback", err)
	}
	h.brain.Quiesce()
	if s := n.next(); s != "" {
		t.Errorf("notice on top of the failed user turn: %q", s)
	}
	a, ok := h.brain.AccountAlertFor(err)
	if !ok || a.Provider != ProviderDeepSeek || a.Problem != ProblemBalance || strings.Contains(a.Spoken.English, "backup") {
		t.Fatalf("AccountAlertFor = %+v, %v", a, ok)
	}
	if ev := h.log.ofType("brain.account_problem"); len(ev) != 1 || payload(ev[0])["problem"] != "balance" {
		t.Errorf("brain.account_problem = %v", ev)
	}
	if ev := h.log.ofType("brain.provider_down"); len(ev) != 1 || payload(ev[0])["problem"] != "balance" {
		t.Errorf("brain.provider_down = %v", ev)
	}

	for i := 0; i < 3; i++ {
		clock.Add(time.Minute)
		if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "done", Template: "auth finished."}); err == nil {
			t.Fatal("event turn answered")
		}
		h.brain.Quiesce()
	}
	if s := n.next(); s != "" {
		t.Errorf("told again on event turns: %q", s)
	}
	if c := h.log.count("brain.account_problem"); c != 1 {
		t.Errorf("brain.account_problem count = %d", c)
	}

	// Topped up: it answers, the problem ends, and a later failure is
	// told again (this time from an event turn, which speaks it).
	ds.Add(braintest.Step{Text: braintest.Reply("Fine.", "")})
	if err := h.brain.SetModel(ProviderDeepSeek, "deepseek-pro", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ask("and now?", "auth"); err != nil {
		t.Fatal(err)
	}
	if got := h.brain.Accounts().Active(); len(got) != 0 {
		t.Errorf("still active after it answered: %+v", got)
	}
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "done", Template: "auth finished."}); err == nil {
		t.Fatal("event turn answered")
	}
	h.brain.Quiesce()
	if s := n.next(); !strings.HasPrefix(s, "DeepSeek says the account balance is used up") {
		t.Errorf("notice = %q", s)
	}
}

// With a backup, the user's turn is answered by it and the problem is
// told once, after the answer, saying a backup answers; the failing
// streak's vaguer notice is not added.
func TestAccountProblemWithBackup(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}
	h := newHarness(t, failing(err402), func(c *Config) { c.Provider, c.Model, c.Now = ProviderDeepSeek, DefaultDeepSeekModel, clock.Now })
	_ = h.brain.AddProvider(ProviderGemini, answering("Fine."))
	n := newNotices(h.brain)
	for i := 0; i < 4; i++ {
		if rep, err := h.ask("how is auth?", "auth"); err != nil || rep.Spoken != "Fine." {
			t.Fatalf("turn %d: %+v, %v", i, rep, err)
		}
		h.brain.Quiesce()
		clock.Add(time.Minute)
	}
	if s := n.next(); s != "DeepSeek says the account balance is used up, so the orchestrator can't use it and a backup model answers for now. Top up the DeepSeek account." {
		t.Errorf("notice = %q", s)
	}
	if s := n.next(); s != "" {
		t.Errorf("second notice: %q", s)
	}
	if ev := h.log.ofType("brain.failover"); len(ev) == 0 || payload(ev[0])["reason"] != "account_balance" {
		t.Errorf("brain.failover = %v", ev)
	}
}
