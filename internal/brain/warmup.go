package brain

import (
	"context"
	"time"

	"google.golang.org/genai"
)

// CacheWarmer is a Model whose provider caches the prompt prefix only
// once a request has written it at a breakpoint (Anthropic): WarmCache
// sends one request that writes the prefix of cfg (tools + system) and
// generates nothing, so the first real turn reads it instead of paying
// for the write in its latency (docs/dev/anthropic-adapter.md section 4).
// cfg is built exactly as a turn's step builds it.
type CacheWarmer interface {
	WarmCache(ctx context.Context, model string, cfg *genai.GenerateContentConfig) (WarmUsage, error)
}

// WarmUsage is what a warm-up request wrote to and read from the cache.
type WarmUsage struct {
	CacheWriteTokens int64
	CacheReadTokens  int64
}

// warmTimeout bounds one warm-up request.
const warmTimeout = 30 * time.Second

// WarmCache writes the current provider's prompt cache in the
// background, when its Model is a CacheWarmer (the Anthropic adapter;
// for every other provider it does nothing): first the prefix of an
// event turn (read-only tools, the event effort), which the first
// announce of a run reads, then that of an utterance turn. The request
// is built by the same functions as a turn's (systemPrompt, turnConfig,
// systemWith), so the system text and tools are the same bytes. It
// returns at once; Close and Quiesce wait for it. A failure is harmless
// and is logged as brain.warmup with its error (a debug-level event in
// the runtime log). Call it once the Host is attached: the system text
// names the roster's aliases.
func (b *Brain) WarmCache() {
	provider, model, impl := b.current()
	w, ok := impl.(CacheWarmer)
	if !ok {
		return
	}
	b.after.Add(1)
	go func() {
		defer b.after.Done()
		for _, k := range []struct {
			kind    Kind
			trigger Trigger
		}{{KindAnnounce, TriggerEvent}, {KindUtterance, TriggerCEO}} {
			if b.ctx.Err() != nil {
				return
			}
			cfg := b.warmConfig(k.kind, k.trigger)
			ctx, cancel := context.WithTimeout(b.ctx, warmTimeout)
			t0 := time.Now()
			u, err := w.WarmCache(ctx, model, cfg)
			cancel()
			payload := map[string]any{"provider": provider, "model": model, "kind": string(k.kind), "dur_ms": time.Since(t0).Milliseconds()}
			if err != nil {
				payload["error"] = err.Error()
			} else {
				payload["cache_write_tokens"], payload["cache_read_tokens"] = u.CacheWriteTokens, u.CacheReadTokens
			}
			b.log.Log(logSource, "brain.warmup", "", payload)
		}
	}()
}

// warmConfig is the config the first step of a turn of this kind would
// send now.
func (b *Brain) warmConfig(kind Kind, trigger Trigger) *genai.GenerateContentConfig {
	system, _, _ := b.systemPrompt()
	cfg := b.turnConfig(kind, trigger)
	text := systemWith(system, b.engine.Assemble(b.cfg.WindowTokens).SystemAddition)
	cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: text}}}
	return &cfg
}
