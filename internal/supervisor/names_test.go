package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
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

	// The CEO hears that name (a spoken push as it reaches the voice).
	h.sup.OnVoiceEvent(voice.EventPush{Text: "Claude is working on the README.", Urgency: voice.UrgencyIdle})

	// A second Claude in the same project: the first keeps the name the
	// CEO heard (pinned; phone test 2026-10-04, an auto-title renamed an
	// agent the CEO knew), and the newcomer takes the next free form.
	h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
	lines := h.namesLines()
	if len(lines) != 2 || !strings.Contains(lines[1], "Claude, sheepdog Claude") {
		t.Fatalf("names lines after a second Claude = %q", lines)
	}
	if got := h.sup.SpokenNames(); strings.Join(got, "|") != "Claude|sheepdog Claude" {
		t.Errorf("SpokenNames = %v", got)
	}
	var first brain.AgentInfo
	for _, a := range (brainHost{h.sup}).Agents() {
		if a.PaneID == "w1:p1" {
			first = a
		}
	}
	if first.Spoken != "Claude" || first.SpokenWas != "" || first.Title != "README" || first.Alias != "sheepdog-claude" {
		t.Errorf("roster row = %+v", first)
	}
	if h.log.count("supervisor.name_pinned") != 1 {
		t.Errorf("supervisor.name_pinned not logged once")
	}
	if b, _ := h.sup.briefingText(); !strings.Contains(b, "Claude is working") || !strings.Contains(b, "sheepdog Claude is working") {
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
	h.srv.Handle("agent.rename", func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	h.claudeIn("w2:p1", docsSession, herdr.StateIdle)

	res := h.say("call that one docs")
	if !strings.Contains(res.Spoken, "docs") {
		t.Errorf("result = %+v", res)
	}
	renamed := h.srv.RequestsFor("agent.rename")
	if len(renamed) != 1 {
		t.Fatalf("agent.rename requests = %d", len(renamed))
	}
	var params struct{ Target, Name string }
	if err := json.Unmarshal(renamed[0].Params, &params); err != nil || params.Target != "w2:p1" || params.Name != "docs" {
		t.Errorf("agent.rename params = %s", renamed[0].Params)
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
	if err != nil || echo != "README Claude is called front end now. herdr refused the name, so herdr's own label still shows the old name." {
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

// herdrNameRule is herdr 0.9.3's agent.rename check, for a mock herdr.
var herdrNameRule = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// The CEO's phone session, 2026-10-04 05:00:43: "kvoice explainer" went
// to herdr as typed, herdr refused it (invalid_agent_name) and the
// daemon still logged supervisor.rename and said it was done. herdr now
// gets its own spelling, the CEO's spoken form stays, and herdr echoing
// its spelling back does not replace the spoken form.
func TestRenameSendsHerdrSafeNameAndKeepsSpokenForm(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.srv.Handle("agent.rename", func(p json.RawMessage) (any, error) {
		var params struct{ Name string }
		_ = json.Unmarshal(p, &params)
		if !herdrNameRule.MatchString(params.Name) {
			return nil, &herdr.Error{Code: "invalid_agent_name", Message: "agent name must start with a lowercase letter and contain only lowercase letters, digits, '-' or '_' (1-32 characters)"}
		}
		return map[string]any{}, nil
	})
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	echo, err := h.sup.rename(context.Background(), "w1:p1", "kvoice explainer")
	if err != nil || echo != "Claude is called kvoice explainer now." {
		t.Fatalf("rename = %q, %v", echo, err)
	}
	reqs := h.srv.RequestsFor("agent.rename")
	if len(reqs) != 1 || !strings.Contains(string(reqs[0].Params), `"name":"kvoice-explainer"`) {
		t.Fatalf("agent.rename requests = %v", reqs)
	}
	if h.log.count("supervisor.rename_error") != 0 {
		t.Error("a herdr-safe name was refused")
	}
	ev := h.log.ofType("supervisor.rename")
	if len(ev) != 1 || ev[0].Payload.(map[string]any)["herdr"] != "renamed" {
		t.Errorf("supervisor.rename = %+v", ev)
	}
	// herdr's snapshot reports its spelling back: the spoken form stays.
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w1:p1", From: herdr.StateIdle, To: herdr.StateIdle, At: h.clock.Now(), Agent: "claude",
		Name: "kvoice-explainer", WorkspaceID: "w9", WorkspaceLabel: "sheepdog", Renamed: true, Source: "snapshot"})
	if row, _ := h.store.Get("w1:p1"); row.Name != "kvoice explainer" || state.Say(row) != "kvoice explainer" {
		t.Errorf("row after herdr's echo = %+v", row)
	}
	if h.log.count("supervisor.renamed") != 0 {
		t.Error("herdr's echo of the voice rename was reported as a new rename")
	}
	// A later rename in herdr's own UI still wins.
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w1:p1", From: herdr.StateIdle, To: herdr.StateIdle, At: h.clock.Now(), Agent: "claude",
		Name: "docs", WorkspaceID: "w9", WorkspaceLabel: "sheepdog", Renamed: true, Source: "snapshot"})
	if row, _ := h.store.Get("w1:p1"); state.Say(row) != "docs" {
		t.Errorf("row after a herdr UI rename = %+v", row)
	}
}

// A name herdr refuses, or cannot carry at all, is reported as such: the
// echo the brain reads says herdr kept its old label, and the log says
// refused, never a plain success.
func TestRenameReportsHerdrRefusal(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.srv.Handle("agent.rename", func(json.RawMessage) (any, error) {
		return nil, &herdr.Error{Code: "invalid_agent_name", Message: "agent name must start with a lowercase letter"}
	})
	h.claudeIn("w1:p1", readmeSession, herdr.StateIdle)
	echo, err := h.sup.rename(context.Background(), "w1:p1", "kvoice explainer")
	if err != nil || !strings.Contains(echo, "herdr refused the name") || !strings.Contains(echo, "still shows the old name") {
		t.Fatalf("rename = %q, %v", echo, err)
	}
	ev := h.log.ofType("supervisor.rename")
	if len(ev) != 1 || ev[0].Payload.(map[string]any)["herdr"] != "refused" {
		t.Errorf("supervisor.rename = %+v", ev)
	}
	// Chinese only: nothing herdr can take, so nothing is sent.
	echo, err = h.sup.rename(context.Background(), "w1:p1", "文档")
	if err != nil || !strings.Contains(echo, "herdr's own label still shows the old name") {
		t.Fatalf("rename = %q, %v", echo, err)
	}
	if n := len(h.srv.RequestsFor("agent.rename")); n != 1 {
		t.Errorf("agent.rename requests = %d, want 1 (none for a name herdr cannot carry)", n)
	}
}

// A CEO name in Chinese starts the briefing's trailer intact (the first
// rune upper-cased, never a cut byte).
func TestPendingTrailerKeepsRunes(t *testing.T) {
	agents := []state.Agent{{PaneID: "w1:p1", Alias: "x-claude", Name: "文档", Spoken: "文档", State: herdr.StateBlocked}}
	got := pendingTrailer(agents, state.PendingAction{Kind: state.PendingAnswerPrompt, PaneID: "w1:p1"}, "")
	if got != "文档 is waiting for your answer." {
		t.Errorf("trailer = %q", got)
	}
}

// The daemon's own sentences name an agent by its spoken name: an
// echo, a promise trailer, an investigation fallback.
func TestDaemonSentencesUseSpokenNames(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.claudeIn("w1:p1", readmeSession, herdr.StateWorking)
	h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "w1:p1", WorkspaceID: "w9", Agent: "claude", Status: herdr.StateWorking})
	h.srv.SetStatus("w1:p1", herdr.StateIdle) // the Ctrl-C takes
	echo, err := brainHost{h.sup}.Interrupt(context.Background(), "w1:p1")
	if err != nil || echo != "Interrupted README Claude; it stopped." {
		t.Errorf("interrupt echo = %q, %v", echo, err)
	}
	trailer := promiseTrailer(h.sup.spokenPromises([]state.Promise{{PaneID: "w2:p1", Alias: "sheepdog-claude-2", Event: brain.PromiseEventDone, Status: state.PromiseOpen}}))
	if !strings.Contains(trailer, "docs pass Claude finishes") {
		t.Errorf("promise trailer = %q", trailer)
	}
	if got := h.sup.sayPane("w9:gone", "old-alias"); got != "old-alias" {
		t.Errorf("a gone pane = %q", got)
	}
	if got := h.sup.sayOf("w9:gone"); got != "another agent" {
		t.Errorf("unknown pane = %q", got)
	}
}

// A rename in herdr's own UI reaches the daemon as a name-only
// transition (herdr.Transition.Renamed, from the snapshot diff): the
// roster and the voice's names line follow at once, and nothing else
// happens: no "started on", no announcement, no pending, no journal.
func TestHerdrRenameIsNameOnly(t *testing.T) {
	h := newHarness(t, withTitleFixtures(t))
	h.claudeIn("w1:p1", readmeSession, herdr.StateBlocked)
	before := len(h.pusher.all())
	pendingBefore, hadPending := h.store.Pending()
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w1:p1", From: herdr.StateBlocked, To: herdr.StateBlocked, At: h.clock.Now(), Agent: "claude",
		Name: "docs", WorkspaceID: "w9", WorkspaceLabel: "sheepdog", Renamed: true, Source: "snapshot"})
	row, _ := h.store.Get("w1:p1")
	if row.Name != "docs" || state.Say(row) != "docs" || row.State != herdr.StateBlocked {
		t.Fatalf("row after rename = %+v", row)
	}
	pushed := h.pusher.all()[before:]
	if len(pushed) != 1 || pushed[0].Urgency != voice.UrgencySilent || !strings.Contains(pushed[0].Text, "called: docs") {
		t.Fatalf("pushes after a rename = %+v, want the names line only", pushed)
	}
	if p, has := h.store.Pending(); has != hadPending || p.ID != pendingBefore.ID {
		t.Errorf("pending changed on a rename: %+v -> %+v", pendingBefore, p)
	}
	if h.log.count("supervisor.renamed") != 1 {
		t.Errorf("supervisor.renamed not logged once")
	}
	// The same name seen again (a voice rename echoed back by herdr's
	// snapshot) changes nothing and logs nothing.
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w1:p1", From: herdr.StateBlocked, To: herdr.StateBlocked, At: h.clock.Now(), Agent: "claude",
		Name: "docs", WorkspaceID: "w9", WorkspaceLabel: "sheepdog", Renamed: true, Source: "snapshot"})
	if h.log.count("supervisor.renamed") != 1 || len(h.pusher.all()) != before+1 {
		t.Errorf("an unchanged name was reported as a rename")
	}
}

// Tier-3 bench 2026-10-03, finding 5: on a fresh call the brain said
// "previously bench Claude" about a name the CEO had never heard. The
// roster gives the brain an old name only when it went out to the CEO
// before.
func TestOldNameOnlyWhenTheCEOHeardIt(t *testing.T) {
	spokenWas := func(h *harness) string {
		for _, a := range (brainHost{h.sup}).Agents() {
			if a.PaneID == "w1:p1" {
				return a.SpokenWas
			}
		}
		return "?"
	}
	t.Run("never said", func(t *testing.T) {
		h := newHarness(t, withTitleFixtures(t))
		h.claudeIn("w1:p1", readmeSession, herdr.StateWorking)
		h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
		if row, _ := h.store.Get("w1:p1"); row.SpokenWas != "Claude" {
			t.Fatalf("row = %+v", row)
		}
		if got := spokenWas(h); got != "" {
			t.Errorf("SpokenWas = %q, want none: the CEO never heard it", got)
		}
	})
	t.Run("said in a reply", func(t *testing.T) {
		// Heard by the CEO, the name is pinned: it never changes, so
		// there is no old name to mention.
		h := newHarness(t, withTitleFixtures(t))
		h.claudeIn("w1:p1", readmeSession, herdr.StateWorking)
		h.sup.mentionedIn("Claude is still on the README.")
		h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
		if row, _ := h.store.Get("w1:p1"); row.Spoken != "Claude" || !row.SpokenPinned {
			t.Errorf("row = %+v, want the heard name kept", row)
		}
		if got := spokenWas(h); got != "" {
			t.Errorf("SpokenWas = %q, want none", got)
		}
	})
	t.Run("said by the CEO", func(t *testing.T) {
		h := newHarness(t, withTitleFixtures(t))
		h.claudeIn("w1:p1", readmeSession, herdr.StateWorking)
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "what is Claude doing"})
		h.claudeIn("w2:p1", docsSession, herdr.StateWorking)
		if row, _ := h.store.Get("w1:p1"); row.Spoken != "Claude" {
			t.Errorf("row = %+v, want the name the CEO used kept", row)
		}
	})
}
