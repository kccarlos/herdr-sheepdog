package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

// Provider failover (pipecat's ServiceSwitcherStrategyFailover): when a
// model call fails with a rate limit, a server error or a timeout, the
// step is retried once on the next registered provider. The transcript
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
	// window, and the CEO is told once.
	failoverStreakWindow = 10 * time.Minute
	failoverStreakNotice = 3
	// failoverNoticeEvery bounds the notice to one per this long.
	failoverNoticeEvery = 30 * time.Minute
)

// Variables only so tests can shrink them.
var (
	// failoverReserve is what a step's first attempt leaves for the
	// retry while the selected provider is on a failing streak: the
	// 2026-10-02 01:01–01:07 Vertex calls each ran into the turn's
	// deadline before answering 504, leaving no time for anything else.
	failoverReserve = 5 * time.Second
	// failoverMinAttempt is the least the first attempt keeps; with less
	// the reserve is not taken.
	failoverMinAttempt = 2 * time.Second
)

// failoverOrder is the order candidates are tried in after the failing
// provider is left out: another vendor before the same model on another
// endpoint (Vertex failing → DeepSeek, then the Gemini key pool, whose
// quota is the scarcest); providers not listed follow, sorted.
var failoverOrder = []string{ProviderVertex, ProviderDeepSeek, ProviderGemini}

// providerSpoken is how a notice names a provider.
var providerSpoken = map[string]string{ProviderGemini: "Gemini", ProviderVertex: "Vertex", ProviderDeepSeek: "DeepSeek"}

func spokenProvider(id string) string {
	if s, ok := providerSpoken[id]; ok {
		return s
	}
	return id
}

// failoverState is the run's record of failovers away from the selected
// provider (under Brain.mu).
type failoverState struct {
	// from is the selected provider the streak is about; at holds the
	// times of its consecutive failovers, newest last.
	from string
	at   []time.Time
	// noticed is when the CEO was last told.
	noticed time.Time
}

// failoverReason classifies a model-call error as worth retrying on
// another provider: a rate limit, a 5xx (504 DEADLINE_EXCEEDED
// included) or a transport timeout. Never when the turn itself is over
// (ctx ended): that is the turn's own timeout or cancellation.
func failoverReason(ctx context.Context, err error) (string, bool) {
	if err == nil || ctx.Err() != nil {
		return "", false
	}
	if errors.Is(err, ErrAllKeysRateLimited) || isRateLimited(err) {
		return "rate_limited", true
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
	return "", false
}

// failoverTarget picks the provider and model a failed step is retried
// on: the first registered provider other than from, in failoverOrder.
// ok is false when there is none.
func (b *Brain) failoverTarget(from, fromModel string) (provider, model string, impl Model, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var cands []string
	for _, id := range failoverOrder {
		if _, reg := b.models[id]; reg && id != from {
			cands = append(cands, id)
		}
	}
	var rest []string
	for id := range b.models {
		if id != from && !contains(failoverOrder, id) {
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
	default:
		return fromModel
	}
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
// over within failoverStreakWindow: its next first attempt leaves time
// for the retry (failoverReserve).
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
// steps. While the selected provider is on a failing streak and a
// target exists, the first attempt leaves failoverReserve of the turn's
// deadline for the retry.
func (b *Brain) generateStep(ctx context.Context, t *turn, step int, msgs []Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	actx := ctx
	if !t.failedOver {
		var cancel context.CancelFunc
		actx, cancel = b.firstAttempt(ctx, t.provider, t.model)
		defer cancel()
	}
	resp, raw, err := b.generate(actx, t, msgs, cfg)
	if err == nil {
		b.failoverSucceeded(t.provider)
		return resp, raw, nil
	}
	if t.failedOver {
		return nil, nil, err
	}
	reason, ok := failoverReason(ctx, err)
	if !ok {
		return nil, nil, err
	}
	to, model, impl, ok := b.failoverTarget(t.provider, t.model)
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
	return b.generate(ctx, t, msgs, cfg)
}

// firstAttempt is the context of a call's first attempt on provider:
// ctx itself, or — while provider is on a failing streak and a failover
// target exists — ctx cut failoverReserve before its deadline, so the
// retry has time (a provider that hangs to the deadline otherwise takes
// it all).
func (b *Brain) firstAttempt(ctx context.Context, provider, model string) (context.Context, context.CancelFunc) {
	if !b.onFailingStreak(provider) {
		return ctx, func() {}
	}
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl)-failoverReserve < failoverMinAttempt {
		return ctx, func() {}
	}
	if _, _, _, ok := b.failoverTarget(provider, model); !ok {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, dl.Add(-failoverReserve))
}

// generateSummary is the compaction's summary call with the same
// failover as a turn's step: the selected provider first, then once
// the failover target (logged as brain.failover {call: compaction}).
func (b *Brain) generateSummary(ctx context.Context, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	provider, model, impl := b.current()
	actx, cancel := b.firstAttempt(ctx, provider, model)
	defer cancel()
	resp, err := impl.Generate(actx, model, contents, cfg)
	if err == nil {
		b.failoverSucceeded(provider)
		return resp, nil
	}
	reason, ok := failoverReason(ctx, err)
	if !ok {
		return nil, err
	}
	to, toModel, toImpl, ok := b.failoverTarget(provider, model)
	if !ok {
		return nil, err
	}
	b.log.Log(logSource, "brain.failover", "", map[string]any{
		"from": provider, "from_model": model, "to": to, "to_model": toModel, "reason": reason, "error": err.Error(), "call": "compaction",
	})
	b.noteFailover(provider, to)
	return toImpl.Generate(ctx, toModel, contents, cfg)
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
// CEO is told once, at most every failoverNoticeEvery.
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
	text := fmt.Sprintf("%s is failing; using %s for now.", spokenProvider(from), spokenProvider(to))
	b.log.Log(logSource, "brain.failover_notice", "", map[string]any{"from": from, "to": to, "failovers": n, "text": text})
	// Off the turn: the notice path is the supervisor's push, which must
	// not run under whatever the turn's caller holds. Tracked as
	// after-turn work, so Close waits for it before closing the
	// transcript it lands in (safe here: inside the turn's inFlight).
	b.after.Add(1)
	go func() {
		defer b.after.Done()
		notify(text)
	}()
}

// SetNotifier sets how the brain tells the CEO something about itself
// (a provider failing over and over): the daemon's spoken push. nil
// turns notices off.
func (b *Brain) SetNotifier(f func(text string)) {
	b.mu.Lock()
	b.notify = f
	b.mu.Unlock()
}
