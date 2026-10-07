package brain_test

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// The roster gives the orchestrator each agent's spoken name next to its alias,
// the old name right after a change, and the worker's session title.
func TestRosterCarriesSpokenNames(t *testing.T) {
	h := newHarness(t, braintest.NewModel(braintest.Step{Text: braintest.Reply("README Claude is working.", "")}), nil)
	h.host.SetAgent(AgentInfo{PaneID: "w7:p1", Alias: "sheepdog-claude", Spoken: "README Claude", SpokenWas: "Claude", Title: "README", Kind: "claude", State: "working"})
	h.host.SetAgent(AgentInfo{PaneID: "w8:p1", Alias: "sheepdog-codex", Spoken: "Codex", Title: "Run suite and report failure", Kind: "codex", State: "idle"})
	if _, err := h.ask("what's going on?", ""); err != nil {
		t.Fatal(err)
	}
	sys := h.model.Calls()[0].System()
	for _, want := range []string{
		`- sheepdog-claude (claude), say "README Claude" (just renamed from "Claude"): working`,
		`- sheepdog-codex (codex), say "Codex", session "Run suite and report failure": idle`,
		// A CEO-given alias is its own spoken name: nothing added.
		"- auth (codex): working",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("roster lacks %q:\n%s", want, sys)
		}
	}
}

// "Call that one docs" is the note tool with agent and name: a rename
// through the Host, not a note.
func TestNoteToolRenames(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolNote, map[string]any{"agent": "auth", "name": "backend"})}},
		braintest.Step{Text: braintest.Reply("Auth is called backend now.", "")},
	), nil)
	rep, err := h.ask("call auth backend", "auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.host.Renamed) != 1 || h.host.Renamed[0] != [2]string{"w5:p1", "backend"} {
		t.Errorf("renamed = %v", h.host.Renamed)
	}
	if len(h.host.Noted) != 0 {
		t.Errorf("a rename also wrote a note: %v", h.host.Noted)
	}
	if rep.Spoken != "Auth is called backend now." {
		t.Errorf("reply = %+v", rep)
	}
	if !strings.Contains(h.model.Calls()[1].Text(), "It is called backend now.") {
		t.Errorf("the model did not see the rename echo:\n%s", h.model.Calls()[1].Text())
	}
}

// A rename without a known agent is an error the model reads, and a
// plain note still needs its text.
func TestNoteToolRenameNeedsAnAgent(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolNote, map[string]any{"name": "backend"}), braintest.FC(ToolNote, map[string]any{})}},
		braintest.Step{Text: braintest.Reply("Which one?", "")},
	), nil)
	if _, err := h.ask("call it backend", ""); err != nil {
		t.Fatal(err)
	}
	text := h.model.Calls()[1].Text()
	if !strings.Contains(text, "to rename, name the agent") || !strings.Contains(text, "note needs the text") || len(h.host.Renamed) != 0 {
		t.Errorf("model saw:\n%s", text)
	}
}
