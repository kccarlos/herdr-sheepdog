package gptlive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestSeedRecapFromGeminiOpensWithIt: a switch from Gemini hands GPT-Live
// the recent conversation; its next billed session opens with it as
// history — the spoken push as a developer note — and says it took the
// conversation over, not that the line reopened after idle.
func TestSeedRecapFromGeminiOpensWithIt(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	s.SeedRecap([]voice.RecapEntry{
		{Role: voice.RecapUser, Text: "why did the auth tests fail?"},
		{Role: voice.RecapAssistant, Text: "Two tests time out on the token refresh."},
		{Role: voice.RecapSupervisor, Text: "docs finished the readme."},
	})
	s.ClientAttached(true)
	if err := s.SendText("and docs?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "open", func() bool { return d.last() != nil && len(d.last().events(evSessionStart)) == 1 })
	input := fmt.Sprint(d.last().events(evSessionStart)[0]["session"].(map[string]any)["input"])
	for _, want := range []string{"why did the auth tests fail?", "Two tests time out", "Supervisor update, already spoken to the shepherd: docs finished the readme.", "taken over this voice conversation", "developer"} {
		if !strings.Contains(input, want) {
			t.Errorf("history lacks %q: %s", want, input)
		}
	}
	if strings.Contains(input, "closed while idle") {
		t.Errorf("history says the line reopened after idle: %s", input)
	}
	if strings.Contains(input, "role:supervisor") {
		t.Errorf("a supervisor role reached GPT-Live: %s", input)
	}
}

// TestRecapEntriesIncludeTheUtteranceInProgress: what GPT-Live hands
// the next provider includes the words and reply not yet settled.
func TestRecapEntriesIncludeTheUtteranceInProgress(t *testing.T) {
	s, _, _ := startSession(t, testConfig())
	s.mu.Lock()
	s.recap.Add(voice.RecapUser, "what is auth doing?")
	s.recap.Add(voice.RecapAssistant, "Auth is running the tests.")
	s.inText, s.outText = "and docs?", "Docs is idle"
	s.mu.Unlock()
	got := voice.RecapLines(s.RecapEntries())
	want := "Shepherd: what is auth doing?\nYou: Auth is running the tests.\nShepherd: and docs?\nYou: Docs is idle"
	if got != want {
		t.Errorf("entries =\n%s\nwant\n%s", got, want)
	}
}

// TestFocusLineOpensTheNextSession: the supervisor's current-focus line
// (voice.TagFocus) pushed in standby is not piled into the held
// background like other silent context; the latest one alone opens the
// next billed session, after the recap. A later focus replaces it, and
// a provider switch carries it (RecapFocus / SeedFocus).
func TestFocusLineOpensTheNextSession(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	focusTags := map[string]any{voice.TagFocus: true}
	if err := s.PushTagged("Current focus: agent in focus: auth.", voice.UrgencySilent, focusTags); err != nil {
		t.Fatal(err)
	}
	if err := s.PushTagged("Current focus: agent in focus: docs.", voice.UrgencySilent, focusTags); err != nil {
		t.Fatal(err)
	}
	if err := s.Push("auth is idle.", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "standby context", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.pushQ) == 0 })
	s.mu.Lock()
	held := append([]string(nil), s.held...)
	s.mu.Unlock()
	if len(held) != 1 || held[0] != "auth is idle." {
		t.Fatalf("held = %q, want the briefing only", held)
	}
	if got := s.RecapFocus(); got != "Current focus: agent in focus: docs." {
		t.Fatalf("RecapFocus = %q", got)
	}
	s.ClientAttached(true)
	if err := s.SendText("what is it doing?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "open", func() bool { return d.last() != nil && len(d.last().events(evSessionStart)) == 1 })
	input := fmt.Sprint(d.last().events(evSessionStart)[0]["session"].(map[string]any)["input"])
	if !strings.Contains(input, "agent in focus: docs.") || strings.Contains(input, "agent in focus: auth.") {
		t.Errorf("history = %s, want the latest focus only", input)
	}

	other, _, _ := startSession(t, testConfig())
	other.SeedFocus(s.RecapFocus())
	if other.RecapFocus() != "Current focus: agent in focus: docs." {
		t.Errorf("seeded focus = %q", other.RecapFocus())
	}
}
