package gptlive

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestUnclaimedWordsDoNotGlueToTheNextUtterance: an utterance the model
// answered itself (no delegation) must not be prefixed to the next one —
// seen live, "Which agent was that about" + "Stop" was no bare stop, so
// stop never ran.
func TestUnclaimedWordsDoNotGlueToTheNextUtterance(t *testing.T) {
	cfg := testConfig()
	calls := make(chan voice.ToolCall, 4)
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		calls <- call
		return voice.ToolResult{Spoken: "ok", Scheduling: voice.UrgencyIdle}, nil
	}
	cfg.Classify = func(text string) (voice.ToolCall, bool) {
		if strings.EqualFold(strings.Trim(strings.TrimSpace(text), ".!"), "stop") {
			return voice.ToolCall{Name: voice.ToolStop}, true
		}
		return voice.ToolCall{Name: voice.ToolToSupervisor, Args: map[string]any{"text": text}}, true
	}
	cfg.SettleWait = 40 * time.Millisecond
	cfg.IdleClose = 10 * time.Second
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": "Which agent was that about"})
	time.Sleep(settledKeep + 200*time.Millisecond) // settled, and past settledKeep
	c.server(map[string]any{"type": evInputTranscript, "delta": " Stop"})
	select {
	case call := <-calls:
		if call.Name != voice.ToolStop {
			t.Fatalf("call %+v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop not run")
	}
}

// TestLateDelegationTakesTheSettledUtterance: a delegation that arrives
// just after the utterance settled still gets its words.
func TestLateDelegationTakesTheSettledUtterance(t *testing.T) {
	cfg := testConfig()
	got := make(chan voice.ToolCall, 1)
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		got <- call
		return voice.ToolResult{Spoken: "ok"}, nil
	}
	cfg.SettleWait = 30 * time.Millisecond
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evInputTranscript, "delta": "How is docs doing"})
	time.Sleep(200 * time.Millisecond)
	c.server(map[string]any{"type": evDelegationCreated, "delegation": map[string]any{"id": "item_late", "target": "client"}})
	select {
	case call := <-got:
		if call.Args["text"] != "How is docs doing" {
			t.Fatalf("call %+v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no call")
	}
}

// TestTurnCompleteWaitsForTheShepherdsUtterance: the model's speech
// ending while the shepherd talks must not emit EventTurnComplete (it
// closes the supervisor's voice input mid-utterance); it follows when
// the utterance settles, before the final fragment.
func TestTurnCompleteWaitsForTheShepherdsUtterance(t *testing.T) {
	cfg := testConfig()
	cfg.SettleWait = 150 * time.Millisecond
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evOutputTranscript, "delta": "Allow it?"})
	time.Sleep(30 * time.Millisecond)
	c.server(map[string]any{"type": evInputTranscript, "delta": " Yes"})
	var order []string
	waitFor(t, "final", func() bool {
		for {
			select {
			case ev := <-s.Events():
				switch e := ev.(type) {
				case voice.EventTurnComplete:
					order = append(order, "turn_complete")
				case voice.EventTranscript:
					if e.Dir == voice.DirIn {
						if e.Final {
							order = append(order, "final")
						} else {
							order = append(order, "in")
						}
					}
				}
			default:
				return len(order) > 0 && order[len(order)-1] == "final"
			}
		}
	})
	if strings.Join(order, ",") != "in,turn_complete,final" {
		t.Fatalf("order %v, want in,turn_complete,final", order)
	}
}
