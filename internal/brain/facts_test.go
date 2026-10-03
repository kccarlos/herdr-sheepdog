package brain

import (
	"strings"
	"testing"
)

// An announce request states what the daemon knows and the brain cannot
// see (docs/bench-2026-10-03.md #15b): the lines already spoken about the
// event and the prompt's real state.
func TestEventRequestCarriesTheFacts(t *testing.T) {
	req := Request{Kind: KindAnnounce, Agent: "bench-claude", Event: "blocked", Template: "bench-claude is asking do you want to proceed."}
	if text := eventRequestText(req); !strings.Contains(text, "Nothing has been said about this yet.") {
		t.Errorf("no facts:\n%s", text)
	}
	for state, want := range map[string]string{
		PromptWaiting:  "still open, waiting",
		PromptAnswered: "already been answered",
		PromptGone:     "no longer open",
	} {
		req.facts = EventFacts{Spoken: []string{"bench-claude is asking do you want to proceed."}, Prompt: state}
		text := eventRequestText(req)
		if !strings.Contains(text, `word for word: "bench-claude is asking do you want to proceed."`) || !strings.Contains(text, want) || strings.Contains(text, "Nothing has been said") {
			t.Errorf("%s:\n%s", state, text)
		}
	}
	if got := factsUpdate(EventFacts{Prompt: PromptWaiting}, EventFacts{Prompt: PromptWaiting}); got != "" {
		t.Errorf("no change: %q", got)
	}
	if got := factsUpdate(EventFacts{Prompt: PromptWaiting}, EventFacts{Spoken: []string{"a"}, Prompt: PromptAnswered}); !strings.Contains(got, `said, about this event: "a"`) || !strings.Contains(got, "already been answered") {
		t.Errorf("update = %q", got)
	}
}
