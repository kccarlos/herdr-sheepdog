package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Session ids of internal/workerlog's title fixtures: README (an
// ai-title) and "docs pass" (a custom title).
const (
	readmeSession = "22222222-3333-4444-8555-666666666666"
	docsSession   = "33333333-4444-4555-8666-777777777777"
)

// withTitleFixtures points the worker log at internal/workerlog's
// fixtures (never the real ~/.claude) and makes the names line
// synchronous.
func withTitleFixtures(t *testing.T) func(*Config) {
	t.Helper()
	old := namesDebounce
	namesDebounce = 0
	t.Cleanup(func() { namesDebounce = old })
	root := filepath.Join("..", "workerlog", "testdata")
	return func(c *Config) {
		c.WorkerLog = workerlog.New(workerlog.Options{ClaudeProjects: filepath.Join(root, "claude", "projects"), CodexSessions: filepath.Join(root, "codex", "sessions")})
	}
}

// claudeIn reports a Claude pane in the sheepdog workspace with its
// session, as the herdr watcher would.
func (h *harness) claudeIn(pane, session string, to herdr.State) {
	h.t.Helper()
	row, _ := h.store.Get(pane)
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, From: row.State, To: to, At: h.clock.Now(), Agent: "claude", WorkspaceID: "w9", WorkspaceLabel: "sheepdog",
		Session: &herdr.AgentSession{Source: "herdr:claude", Kind: "id", Value: session}, Cwd: "/tmp/work/titles"})
}

// namesLines are the silent names lines pushed so far.
func (h *harness) namesLines() []string {
	var out []string
	for _, p := range h.pusher.all() {
		if p.Urgency == voice.UrgencySilent && (strings.HasPrefix(p.Text, "The agents are now called") || strings.HasPrefix(p.Text, "No agents are running now")) {
			out = append(out, p.Text)
		}
	}
	return out
}

func TestSpokenNamesFromTitlesReachRosterAndVoice(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.claudeIn("w1:p1", readmeSession, herdr.StateWorking)
	if got := h.namesLines(); len(got) != 1 || got[0] != "The agents are now called: Claude. Use these names; never a nickname." {
		t.Fatalf("names lines after one agent = %q", got)
	}
	row, _ := h.store.Get("w1:p1")
	if row.Title != "README" || row.Spoken != "Claude" {
		t.Fatalf("row = %+v", row)
	}

	// A second Claude in the same project: both are told apart by their
	// own session titles, and the first one's old name is kept.
	h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
	lines := h.namesLines()
	if len(lines) != 2 || !strings.Contains(lines[1], "README Claude, docs pass Claude") {
		t.Fatalf("names lines after a second Claude = %q", lines)
	}
	if got := h.sup.SpokenNames(); strings.Join(got, "|") != "README Claude|docs pass Claude" {
		t.Errorf("SpokenNames = %v", got)
	}
	var first brain.AgentInfo
	for _, a := range (brainHost{h.sup}).Agents() {
		if a.PaneID == "w1:p1" {
			first = a
		}
	}
	if first.Spoken != "README Claude" || first.SpokenWas != "Claude" || first.Title != "README" || first.Alias != "sheepdog-claude" {
		t.Errorf("roster row = %+v", first)
	}
	if b, _ := h.sup.briefingText(); !strings.Contains(b, "README Claude is working") || !strings.Contains(b, "docs pass Claude is working") {
		t.Errorf("briefing = %q", b)
	}

	// A transition that changes no name sends no names line.
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	if got := h.namesLines(); len(got) != 2 {
		t.Errorf("an unchanged roster sent a names line: %q", got)
	}

	// The second one exits: the first is plain Claude again.
	h.claudeIn("w2:p1", docsSession, herdr.StateUnknown)
	lines = h.namesLines()
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "The agents are now called: Claude.") {
		t.Errorf("names lines after the second exited = %q", lines)
	}

	// The CEO's words resolve by spoken name and by title.
	rows := h.store.List()
	if r, ok := findAlias(rows, "Claude"); !ok || r.PaneID != "w1:p1" {
		t.Errorf("findAlias(Claude) = %v, %v", r.PaneID, ok)
	}
	if r, ok := findAlias(rows, "docs pass"); !ok || r.PaneID != "w2:p1" {
		t.Errorf("findAlias(docs pass) = %v, %v", r.PaneID, ok)
	}
}

// "Call that one docs": the brain's note tool with agent and name
// renames through herdr and the roster, and the voice hears the new
// name.
func TestRenameThroughNoteTool(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolNote, map[string]any{"agent": "sheepdog-claude-2", "name": "docs"})}},
		braintest.Step{Text: braintest.Reply("Docs pass Claude is called docs now.", "")},
	)
	h := newBrainHarness(t, m, withTitleFixtures(t))
	var renamed []json.RawMessage
	h.srv.Handle("agent.rename", func(p json.RawMessage) (any, error) {
		renamed = append(renamed, p)
		return map[string]any{}, nil
	})
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	h.claudeIn("w2:p1", docsSession, herdr.StateIdle)

	res := h.say("call that one docs")
	if !strings.Contains(res.Spoken, "docs") {
		t.Errorf("result = %+v", res)
	}
	if len(renamed) != 1 {
		t.Fatalf("agent.rename requests = %d", len(renamed))
	}
	var params struct{ Target, Name string }
	if err := json.Unmarshal(renamed[0], &params); err != nil || params.Target != "w2:p1" || params.Name != "docs" {
		t.Errorf("agent.rename params = %s", renamed[0])
	}
	row, _ := h.store.Get("w2:p1")
	if row.Name != "docs" || row.Spoken != "docs" || row.SpokenWas != "docs pass Claude" {
		t.Errorf("row after rename = %+v", row)
	}
	// The other Claude is the only one of its kind without a CEO name.
	if other, _ := h.store.Get("w1:p1"); other.Spoken != "Claude" {
		t.Errorf("other row = %+v", other)
	}
	lines := h.namesLines()
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "Claude, docs") {
		t.Errorf("names lines = %q", lines)
	}
	if n := h.log.count("supervisor.rename"); n != 1 {
		t.Errorf("supervisor.rename events = %d", n)
	}
}

// A rename herdr refuses still renames in the roster (herdr keeps its
// own label until renamed there), and a name another agent has is
// refused.
func TestRenameEdgeCases(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	h.claudeIn("w2:p1", docsSession, herdr.StateIdle)
	echo, err := h.sup.rename(context.Background(), "w1:p1", "  front   end ")
	if err != nil || echo != "README Claude is called front end now." {
		t.Errorf("rename = %q, %v", echo, err)
	}
	if h.log.count("supervisor.rename_error") != 1 {
		t.Errorf("herdr's refusal was not logged")
	}
	if row, _ := h.store.Get("w1:p1"); state.Say(row) != "front end" {
		t.Errorf("row = %+v", row)
	}
	if _, err := h.sup.rename(context.Background(), "w2:p1", "Front End"); err == nil {
		t.Error("a name another agent has was accepted")
	}
}
