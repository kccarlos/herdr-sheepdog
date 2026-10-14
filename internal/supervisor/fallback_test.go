package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// TestFallbackAfterStatusNeverClaimsNoChange is 2026-09-24 18:16:39:
// the user heard a status answer, then asked "Um, I want to know the
// detail of the glitches"; the orchestrator errored and the daemon said "No
// change since I last checked: Four agents: …" — an answer to a question
// nobody asked. The fallback now says it could not answer, whatever was
// delivered before.
func TestFallbackAfterStatusNeverClaimsNoChange(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Text: braintest.Reply("auth is working on the tests.", "")},
		braintest.Step{Err: braintest.ErrBoom},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	if res := h.say("Hey, please check the current status of my agents"); res.Spoken != "auth is working on the tests." {
		t.Fatalf("status reply = %+v", res)
	}
	h.clock.Advance(5 * time.Second)
	res := h.say("Um, I want to know the detail of the glitches")
	if res.Spoken != brainFailedLine {
		t.Errorf("fallback = %q, want %q", res.Spoken, brainFailedLine)
	}
	for _, bad := range []string{"No change", "One agent", "auth is working", noBrainTrailer} {
		if strings.Contains(res.Spoken, bad) {
			t.Errorf("fallback %q contains %q", res.Spoken, bad)
		}
	}
}

// TestFailedTurnDoesNotMarkRosterDelivered: the roster counts as
// delivered only after a reply. A turn that failed delivered nothing, so
// the next question's request carries no "unchanged since you last
// reported it" note (the old code marked it before the turn ran).
func TestFailedTurnDoesNotMarkRosterDelivered(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: braintest.ErrBoom},
		braintest.Step{Text: braintest.Reply("auth is working.", "")},
		braintest.Step{Text: braintest.Reply("auth is still working.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	lastRequest := func() string {
		calls := m.Calls()
		text := calls[len(calls)-1].Text()
		if i := strings.LastIndex(text, "Request from the assistant (utterance)"); i >= 0 {
			return text[i:]
		}
		return text
	}

	h.say("what's going on")
	h.clock.Advance(5 * time.Second)
	h.say("what's going on")
	if strings.Contains(lastRequest(), "unchanged since you last reported") || h.log.count("supervisor.briefing_repeat") != 0 {
		t.Errorf("a failed turn marked the roster delivered:\n%s", lastRequest())
	}
	// The successful reply did deliver it: the next ask carries the note.
	h.clock.Advance(5 * time.Second)
	h.say("anything new?")
	if !strings.Contains(lastRequest(), "The roster is unchanged since you last reported it 5 s ago") {
		t.Errorf("after a reply, the repeat note is missing:\n%s", lastRequest())
	}
}

// TestBrainFailedLineFitsTheEar: the fallback is one line the ear
// contract leaves as it is (prose.EarLine: ≤ 2 sentences, ≤ 30 words,
// no dashes or semicolons), and it asks no yes/no question a bare "yes"
// could answer.
func TestBrainFailedLineFitsTheEar(t *testing.T) {
	spoken, overflow := prose.EarLine(brainFailedLine, "")
	if spoken != brainFailedLine || overflow != "" {
		t.Errorf("EarLine(%q) = %q, overflow %q", brainFailedLine, spoken, overflow)
	}
	if n := len(strings.Fields(brainFailedLine)); n > 20 {
		t.Errorf("%d words, want a short line", n)
	}
	if strings.Contains(brainFailedLine, "?") {
		t.Errorf("%q asks a question a bare yes would answer (and could approve an open worker prompt)", brainFailedLine)
	}
}

// TestFallbackNamesAccountProblem: the orchestrator's only provider has
// used up its balance. "Say it again" would fail the same way, so the
// answer names the service and the fix, fits the ear, follows the
// user's language, and its facts tell the voice a retry will not help.
func TestFallbackNamesAccountProblem(t *testing.T) {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Err: &brain.APIError{Provider: brain.ProviderDeepSeek, Status: 402, Message: "Insufficient Balance"}}
	m.EventDefault = braintest.NothingToAdd()
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	res := h.say("what's going on")
	if !strings.HasPrefix(res.Spoken, "Gemini says the account balance is used up") {
		t.Fatalf("fallback = %q", res.Spoken)
	}
	if res.Spoken == brainFailedLine || strings.Contains(res.Spoken, "say it again") {
		t.Errorf("fallback offers a retry: %q", res.Spoken)
	}
	if spoken, overflow := prose.EarLine(res.Spoken, ""); spoken != res.Spoken || overflow != "" {
		t.Errorf("EarLine(%q) = %q, overflow %q", res.Spoken, spoken, overflow)
	}
	if !strings.Contains(res.Detail, "will fail the same way") {
		t.Errorf("detail = %q", res.Detail)
	}
	h.clock.Advance(5 * time.Second)
	if zh := h.say("现在什么情况？"); !strings.Contains(zh.Spoken, "余额用完了") {
		t.Errorf("zh fallback = %q", zh.Spoken)
	}
}
