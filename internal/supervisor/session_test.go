package supervisor

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// read_screen with source session reads the worker's own transcript
// through the journal's reader without moving its offset: the finish
// after it still journals the worker's final message
// (docs/dev/history/dive-deep.md WP8a).
func TestReadSessionLeavesTheJournalFresh(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "auth", "source": "session"})}},
		braintest.Step{Text: braintest.Reply("It says the total now rounds half up.", "")},
	)
	m.EventDefault = announceReply("Auth fixed the rounding.", "idle", "", "", false)
	h := newBrainHarness(t, m, withWorkerLog(t.TempDir()))
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	final := "The total now rounds half up.\nWhat changed: rounding.\nWhat needs a decision: nothing.\nWhat is next: nothing."
	claudeTurn(t, h, "w5:p1", final)

	if res := h.say("what exactly did auth say?"); !strings.Contains(res.Spoken, "rounds half up") {
		t.Fatalf("reply = %+v", res)
	}
	calls := m.Calls()
	if len(calls) < 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	text := unnonce(calls[1].Text())
	for _, want := range []string{
		"<<<BEGIN_WORKER_OUTPUT agent=auth source=session>>>",
		"Last instruction it received: Fix the rounding bug.\n",
		"What it did for it: edited total.go; tool calls: Edit 1",
		"Its last message, whole:\n" + final + "\n<<<END_WORKER_OUTPUT>>>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("session read lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, SignOff) {
		t.Error("the sign-off reached the model as the shepherd's instruction")
	}

	doneAuth(h)
	row, _ := h.store.Get("w5:p1")
	if len(row.Journal) != 1 || !strings.HasPrefix(row.Journal[0].LastAssistant, "The total now rounds half up.") {
		t.Errorf("journal = %+v: the session read moved the journal's offset", row.Journal)
	}
}

// A session that cannot be read is an error carrying the
// worker.session_missing reason.
func TestReadSessionMissingReasons(t *testing.T) {
	h := newHarness(t, withWorkerLog(t.TempDir()))
	h.addAgent("w5:p1", "auth", "claude", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "agy", herdr.StateIdle)
	if _, err := h.store.Update("w6:p1", func(a *state.Agent) {
		a.SessionSource, a.SessionKind, a.SessionValue = "herdr:antigravity_cli", herdr.SessionKindID, "70f9969a"
	}); err != nil {
		t.Fatal(err)
	}
	host := brainHost{h.sup}
	for pane, reason := range map[string]string{"w5:p1": workerlog.ReasonNotReported, "w6:p1": workerlog.ReasonUnsupported} {
		_, err := host.ReadSession(context.Background(), pane)
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("%s: err = %v, want the %s reason", pane, err, reason)
		}
	}
	if _, err := host.ReadSession(context.Background(), "w9:p9"); err == nil {
		t.Error("an unknown pane read a session")
	}
}

var reNonceAttr = regexp.MustCompile(` nonce=[0-9a-z]+`)

// unnonce drops the per-call nonce from every provenance marker, so a test
// can compare against the marker text without knowing it.
func unnonce(s string) string { return reNonceAttr.ReplaceAllString(s, "") }
