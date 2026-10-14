package brain

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/genai"
)

// Provider failover (pipecat's ServiceSwitcherStrategyFailover): when a
// model call fails with a rate limit, a server error, a timeout or a
// broken connection (after one retry on the same provider), the step is
// retried once on the next registered provider. The transcript
// is provider-neutral and sameOrigin gates what provider-native data is
// replayed, so the other provider can take the window as it is. The
// switch lasts for the rest of the turn only; the selected provider
// (SetModel, the state file) is never changed.
const (
	// DefaultDeepSeekModel is the DeepSeek model a failover uses when
	// this run has not used DeepSeek yet.
	DefaultDeepSeekModel = "deepseek-flash"
	// failoverStreakWindow and failoverStreakNotice: this many
	// consecutive failovers away from the selected provider within the
	// window, and the user is told once.
	failoverStreakWindow = 10 * time.Minute
	failoverStreakNotice = 3
	// failoverNoticeEvery bounds the notice to one per this long.
	failoverNoticeEvery = 30 * time.Minute
)

// Variables only so tests can shrink them.
var (
	// failoverReserve is what a step's first attempt leaves for the
	// retry on another provider: the fallback's typical step. Measured
	// on the daemon's brain.step events (2026-09-18 – 10-03): DeepSeek
	// deepseek-flash p50 2.3 s, p90 6.7 s, p95 8.3 s, max 12.6 s (486
	// steps); Vertex gemini-3.8-flash p50 2.3 s, p90 6.7 s, p95 8.6 s,
	// max 15.6 s (144). 8 s is about the p95 of either. Tier-3 bench
	// 2026-10-03 #7: Vertex answered 504 after about 20 s and the
	// DeepSeek retry had about 1 s left.
	failoverReserve = 8 * time.Second
	// failoverMinAttempt is the least the first attempt keeps while the
	// selected provider is on a failing streak (it has just failed, so
	// its attempt may be short); with less the reserve is not taken.
	failoverMinAttempt = 2 * time.Second
	// failoverHealthyAttempt is the least the first attempt keeps when
	// the provider is not on a streak: as much as the reserve, so a
	// healthy but slow first call is not cut for a retry it does not
	// need (an owed announce turn of 15 s + 3 s grace keeps 10 s; the
	// 25 s utterance turn's first step keeps 20 s, above every step
	// measured).
	failoverHealthyAttempt = 8 * time.Second
	// providerDownFor is how long a provider that refused the daemon's
	// credentials (401, 403) is left out of failover and routed around
	// (brain.provider_down). 2026-10-08 00:16: DeepSeek timed out, the
	// step failed over to Vertex, whose project was blocked ("403
	// PERMISSION_DENIED: Request rejected in accordance with our terms of
	// use"), and the turn fell back although the Gemini key pool would
	// have answered. Such a refusal does not clear within seconds; half
	// an hour keeps every turn in between off it, and selecting the
	// provider again (SetModel) retries it at once.
	providerDownFor = 30 * time.Minute
)

// failoverOrder is the order candidates are tried in after the failing
// provider is left out: another vendor before the same model on another
// endpoint (Vertex failing → DeepSeek, then the Gemini key pool, whose
// quota is the scarcest); providers not listed follow, sorted.
var failoverOrder = []string{ProviderVertex, ProviderDeepSeek, ProviderGemini}

// Notice is something the orchestrator tells the user about itself, in both
// languages the daemon speaks: the notifier picks the one the user last
// spoke (supervisor.ceoLanguage).
type Notice struct {
	English string
	Chinese string
}

// failoverNotice is the line for a failing streak. It names no vendor
// (tier-2 bench 2026-10-03 #7: "DeepSeek is failing; using Vertex for
// now." made the simulated user ask about "the Vertex worker"): which
// model answers is the daemon's business, that answers may differ is
// the user's.
var failoverNotice = Notice{
	English: "My usual model is having trouble; a backup is answering for now.",
	Chinese: "我平时用的模型出了点问题，现在先由备用模型回答。",
}

// providerDown is one provider left out of failover until Until, with
// the error that put it there (Brain.down).
type providerDown struct {
	Until  time.Time
	Reason string
}

// failoverState is the run's record of failovers away from the selected
// provider (under Brain.mu).
type failoverState struct {
	// from is the selected provider the streak is about; at holds the
	// times of its consecutive failovers, newest last.
	from string
	at   []time.Time
	// noticed is when the user was last told.
	noticed time.Time
}

// failoverReason classifies a model-call error as worth retrying on
// another provider: a rate limit, a 5xx (504 DEADLINE_EXCEEDED
// included), a transport timeout or another transport error (a broken
// connection, isTransportError; generateStep retries that once on the
// same provider first). Never when the turn itself is over
// (ctx ended): that is the turn's own timeout or cancellation.
func failoverReason(ctx context.Context, err error) (string, bool) {
	if err == nil || ctx.Err() != nil {
		return "", false
	}
	if errors.Is(err, ErrAllKeysRateLimited) || isRateLimited(err) {
		return "rate_limited", true
	}
	if code := refusedCode(err); code != 0 {
		return fmt.Sprintf("http_%d", code), true
	}
	var api genai.APIError
	if errors.As(err, &api) && api.Code >= 500 {
		return fmt.Sprintf("http_%d", api.Code), true
	}
	var compat *APIError
	if errors.As(err, &compat) && compat.Status >= 500 {
		return fmt.Sprintf("http_%d", compat.Status), true
	}
	var nerr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()) {
		return "timeout", true
	}
	if isTransportError(err) {
		return "transport", true
	}
	return "", false
}

// isTransportError reports a request that broke on the wire before any
// answer came back: a connection reset, a closed or broken connection
// (EOF, broken pipe), a failed dial, DNS lookup or TLS handshake, or a
// response body cut off mid-read. 2026-10-08 03:04:59: DeepSeek's
// "read tcp …: read: connection reset by peer" 20 ms into an utterance
// turn fell straight back to "Sorry, I couldn't get an answer just now"
// although the next request would most likely have gone through. Such
// an error says nothing about the provider's answer, so the step is
// retried once on the same provider (a fresh connection), then fails
// over like a timeout (generateStep). Timeouts are classified before
// this (failoverReason), and an HTTP status is never one.
func isTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var api genai.APIError
	var compat *APIError
	if errors.As(err, &api) || errors.As(err, &compat) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && !nerr.Timeout() {
		// *url.Error, *net.OpError, *net.DNSError: whatever the HTTP
		// client returns when the request never got an answer.
		return true
	}
	var rec tls.RecordHeaderError
	if errors.As(err, &rec) {
		return true
	}
	// Errors that lost their chain (a provider SDK formatting with %v).
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"connection reset", "broken pipe", "unexpected eof", "connection refused", "tls handshake", "use of closed network connection"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// refusedCode is 401 or 403 when err is the provider refusing the
// daemon's credentials or project (UNAUTHENTICATED, PERMISSION_DENIED):
// the same request fails the same way until someone fixes the account,
// so the provider is marked down (markDown) and the next one is tried.
// 0 otherwise.
func refusedCode(err error) int {
	var api genai.APIError
	if errors.As(err, &api) && (api.Code == 401 || api.Code == 403) {
		return api.Code
	}
	var compat *APIError
	if errors.As(err, &compat) && (compat.Status == 401 || compat.Status == 403) {
		return compat.Status
	}
	return 0
}

// markDown leaves provider out of failover, and routes turns around it
// when it is the selected one, for providerDownFor (brain.provider_down).
func (b *Brain) markDown(provider, model string, err error) {
	b.mu.Lock()
	until := b.cfg.Now().Add(providerDownFor)
	if b.down == nil {
		b.down = map[string]providerDown{}
	}
	b.down[provider] = providerDown{Until: until, Reason: oneLine(err.Error())}
	b.mu.Unlock()
	b.log.Log(logSource, "brain.provider_down", "", map[string]any{
		"provider": provider, "model": model, "error": err.Error(), "until": until.UTC().Format(time.RFC3339), "for_s": int(providerDownFor.Seconds()),
	})
}

// isDownLocked reports whether provider is marked down now (b.mu held).
func (b *Brain) isDownLocked(provider string) bool {
	d, ok := b.down[provider]
	return ok && b.cfg.Now().Before(d.Until)
}

// ProviderDown reports whether provider is marked down (it refused the
// daemon's credentials), why and until when, for the model picker.
func (b *Brain) ProviderDown(provider string) (reason string, until time.Time, down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isDownLocked(provider) {
		return "", time.Time{}, false
	}
	d := b.down[provider]
	return d.Reason, d.Until, true
}

// isCancelled499 reports a request the model's server cancelled (HTTP
// 499 CANCELLED: "The operation was cancelled."): seen three times on
// Vertex in the 2026-10-05 baseline, each time falling back to the
// template text. A transport failure, retried once per turn
// (generateStep); not a failover reason.
func isCancelled499(err error) bool {
	var api genai.APIError
	if errors.As(err, &api) && api.Code == 499 {
		return true
	}
	var compat *APIError
	return errors.As(err, &compat) && compat.Status == 499
}

// retryReason is why a failed call is retried once on the same
// provider (brain.retry reason): cancelled_499 or transport; "" when it
// is not.
func retryReason(err error) string {
	switch {
	case err == nil:
		return ""
	case isCancelled499(err):
		return "cancelled_499"
	case isTransportError(err):
		return "transport"
	}
	return ""
}

// failoverTarget picks the provider and model a failed step is retried
// on: the first registered provider other than from and the ones in
// tried, and not marked down, in failoverOrder. ok is false when there
// is none.
func (b *Brain) failoverTarget(from, fromModel string, tried ...string) (provider, model string, impl Model, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	skip := func(id string) bool { return id == from || contains(tried, id) || b.isDownLocked(id) }
	var cands []string
	for _, id := range failoverOrder {
		if _, reg := b.models[id]; reg && !skip(id) {
			cands = append(cands, id)
		}
	}
	var rest []string
	for id := range b.models {
		if !skip(id) && !contains(failoverOrder, id) {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	cands = append(cands, rest...)
	if len(cands) == 0 {
		return "", "", nil, false
	}
	provider = cands[0]
	return provider, b.failoverModelLocked(provider, fromModel), b.models[provider], true
}

// failoverModelLocked is the model a failover to provider runs: the
// same id between the two Gemini endpoints, else the model this run
// last used on provider, else the provider's default.
func (b *Brain) failoverModelLocked(provider, fromModel string) string {
	gemini := provider == ProviderGemini || provider == ProviderVertex
	if gemini && strings.HasPrefix(fromModel, "gemini-") {
		return fromModel
	}
	if m := b.lastModel[provider]; m != "" {
		return m
	}
	switch {
	case gemini:
		return DefaultModel
	case provider == ProviderDeepSeek:
		return DefaultDeepSeekModel
	}
	if d, ok := b.models[provider].(interface{ DefaultModel() string }); ok && d.DefaultModel() != "" {
		return d.DefaultModel()
	}
	return fromModel
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// onFailingStreak reports whether provider (the selected one) failed
// over within failoverStreakWindow: its next first attempt may be cut
// to failoverMinAttempt to leave the retry its reserve.
func (b *Brain) onFailingStreak(provider string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := &b.failover
	if f.from != provider || len(f.at) == 0 {
		return false
	}
	return b.cfg.Now().Sub(f.at[len(f.at)-1]) < failoverStreakWindow
}

// generateStep is one model call of the turn with failover: the turn's
// provider first, then — once per turn, on a failoverReason error — the
// failover target, which stays the turn's provider for its remaining
// steps. A provider that refuses the credentials (401, 403) is marked
// down and the next candidate is tried as well, at any attempt: a
// refusal is not a slow or busy provider, and the turn's deadline still
// bounds the chain. When a target exists, the first attempt leaves
// failoverReserve of the turn's deadline for the retry (firstAttempt).
func (b *Brain) generateStep(ctx context.Context, t *turn, step int, msgs []Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	actx := ctx
	if !t.failedOver {
		var cancel context.CancelFunc
		actx, cancel = b.firstAttempt(ctx, t.provider, t.model, t.tried...)
		defer cancel()
	}
	resp, raw, err := b.generate(actx, t, msgs, cfg)
	if reason := retryReason(err); reason != "" && !t.retried && ctx.Err() == nil && actx.Err() == nil {
		// One retry on the same provider, within the attempt's budget
		// (rail / robustness): a 499 or a broken connection is a
		// dropped request, not an answer, and the next try usually
		// works. A transport error that repeats then fails over below.
		t.retried = true
		b.log.Log(logSource, "brain.retry", "", map[string]any{
			"provider": t.provider, "model": t.model, "reason": reason, "error": err.Error(), "turn": t.n, "step": step,
		})
		resp, raw, err = b.generate(actx, t, msgs, cfg)
	}
	for {
		if err == nil {
			b.failoverSucceeded(t.provider)
			return resp, raw, nil
		}
		reason, ok := failoverReason(ctx, err)
		if !ok {
			return nil, nil, err
		}
		refused := refusedCode(err) != 0
		if refused {
			b.markDown(t.provider, t.model, err)
		}
		if t.failedOver && !refused {
			return nil, nil, err
		}
		t.tried = append(t.tried, t.provider)
		to, model, impl, ok := b.failoverTarget(t.provider, t.model, t.tried...)
		if !ok {
			return nil, nil, err
		}
		from, fromModel := t.provider, t.model
		b.log.Log(logSource, "brain.failover", "", map[string]any{
			"from": from, "from_model": fromModel, "to": to, "to_model": model, "reason": reason, "error": err.Error(),
			"turn": t.n, "step": step, "call": "turn",
		})
		t.provider, t.model, t.impl, t.failedOver = to, model, impl, true
		b.noteFailover(from, to)
		resp, raw, err = b.generate(ctx, t, msgs, cfg)
	}
}

// routeAroundDown moves a turn whose selected provider is marked down
// onto the failover target before its first step (brain.failover
// {reason: provider_down}), so every turn in the down window does not
// first pay for the refusal. The turn may still fail over once more.
func (b *Brain) routeAroundDown(t *turn) {
	b.mu.Lock()
	down := b.isDownLocked(t.provider)
	b.mu.Unlock()
	if !down {
		return
	}
	to, model, impl, ok := b.failoverTarget(t.provider, t.model)
	if !ok {
		return
	}
	from, fromModel := t.provider, t.model
	b.log.Log(logSource, "brain.failover", "", map[string]any{
		"from": from, "from_model": fromModel, "to": to, "to_model": model, "reason": "provider_down", "turn": t.n, "step": 0, "call": "turn",
	})
	t.tried = append(t.tried, from)
	t.provider, t.model, t.impl = to, model, impl
	b.noteFailover(from, to)
}

// firstAttempt is the context of a call's first attempt on provider:
// ctx cut failoverReserve before its deadline when a failover target is
// registered and the attempt still keeps its minimum — failoverMinAttempt
// while provider is on a failing streak, failoverHealthyAttempt otherwise
// — so a provider that hangs until it answers 504 leaves the retry a
// real budget; else ctx itself.
func (b *Brain) firstAttempt(ctx context.Context, provider, model string, tried ...string) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	least := failoverHealthyAttempt
	if b.onFailingStreak(provider) {
		least = failoverMinAttempt
	}
	if time.Until(dl)-failoverReserve < least {
		return ctx, func() {}
	}
	if _, _, _, ok := b.failoverTarget(provider, model, tried...); !ok {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, dl.Add(-failoverReserve))
}

// generateSummary is the compaction's summary call with the same
// failover as a turn's step: the selected provider first (its target
// when it is marked down), then once the failover target (logged as
// brain.failover {call: compaction}); a refusal (401, 403) marks the
// provider down and moves on to the next candidate.
func (b *Brain) generateSummary(ctx context.Context, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	provider, model, impl := b.current()
	var tried []string
	b.mu.Lock()
	down := b.isDownLocked(provider)
	b.mu.Unlock()
	if down {
		if to, toModel, toImpl, ok := b.failoverTarget(provider, model); ok {
			tried = append(tried, provider)
			provider, model, impl = to, toModel, toImpl
		}
	}
	actx, cancel := b.firstAttempt(ctx, provider, model, tried...)
	defer cancel()
	resp, err := impl.Generate(actx, model, contents, cfg)
	failedOver := false
	for {
		if err == nil {
			b.failoverSucceeded(provider)
			return resp, nil
		}
		reason, ok := failoverReason(ctx, err)
		if !ok {
			return nil, err
		}
		refused := refusedCode(err) != 0
		if refused {
			b.markDown(provider, model, err)
		}
		if failedOver && !refused {
			return nil, err
		}
		tried = append(tried, provider)
		to, toModel, toImpl, ok := b.failoverTarget(provider, model, tried...)
		if !ok {
			return nil, err
		}
		b.log.Log(logSource, "brain.failover", "", map[string]any{
			"from": provider, "from_model": model, "to": to, "to_model": toModel, "reason": reason, "error": err.Error(), "call": "compaction",
		})
		b.noteFailover(provider, to)
		provider, model, impl, failedOver = to, toModel, toImpl, true
		resp, err = impl.Generate(ctx, model, contents, cfg)
	}
}

// failoverSucceeded ends the failing streak when the selected provider
// answered.
func (b *Brain) failoverSucceeded(provider string) {
	b.mu.Lock()
	if b.failover.from == provider {
		b.failover.at = nil
	}
	b.mu.Unlock()
}

// noteFailover records a failover away from from (to is where the step
// went). A failover away from a provider that is not the selected one
// (the selection changed during the turn) does not count. After
// failoverStreakNotice consecutive ones within failoverStreakWindow the
// user is told once, at most every failoverNoticeEvery.
func (b *Brain) noteFailover(from, to string) {
	b.mu.Lock()
	now := b.cfg.Now()
	f := &b.failover
	if from != b.curProvider {
		b.mu.Unlock()
		return
	}
	if f.from != from {
		f.from, f.at = from, nil
	}
	f.at = append(f.at, now)
	if len(f.at) > failoverStreakNotice {
		f.at = f.at[len(f.at)-failoverStreakNotice:]
	}
	due := len(f.at) == failoverStreakNotice && now.Sub(f.at[0]) <= failoverStreakWindow &&
		(f.noticed.IsZero() || now.Sub(f.noticed) >= failoverNoticeEvery) && b.notify != nil
	notify, n := b.notify, len(f.at)
	if due {
		f.noticed = now
	}
	b.mu.Unlock()
	if !due {
		return
	}
	notice := failoverNotice
	b.log.Log(logSource, "brain.failover_notice", "", map[string]any{"from": from, "to": to, "failovers": n, "text": notice.English})
	// Off the turn: the notice path is the supervisor's push, which must
	// not run under whatever the turn's caller holds. Tracked as
	// after-turn work, so Close waits for it before closing the
	// transcript it lands in (safe here: inside the turn's inFlight).
	b.after.Add(1)
	go func() {
		defer b.after.Done()
		notify(notice)
	}()
}

// SetNotifier sets how the orchestrator tells the user something about itself
// (a provider failing over and over): the daemon's spoken push, in the
// user's language. nil turns notices off.
func (b *Brain) SetNotifier(f func(n Notice)) {
	b.mu.Lock()
	b.notify = f
	b.mu.Unlock()
}

// FailureReason is a turn's failure in plain words, for the voice to
// tell the user (the supervisor's fallback facts): what broke, never the
// vendor, the endpoint or the raw error.
func FailureReason(err error) string {
	var api genai.APIError
	var compat *APIError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAllKeysRateLimited) || isRateLimited(err):
		return "the orchestrator's model is rate-limited"
	case refusedCode(err) != 0:
		return "the orchestrator's model refused the daemon's credentials"
	case isTransportError(err):
		return "the connection to the orchestrator's model broke"
	case errors.Is(err, context.DeadlineExceeded):
		return "the orchestrator's model did not answer in time"
	case (errors.As(err, &api) && api.Code >= 500) || (errors.As(err, &compat) && compat.Status >= 500):
		return "the orchestrator's model had a server error"
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return "the orchestrator's model did not answer in time"
	}
	return "the orchestrator's model returned an error"
}
