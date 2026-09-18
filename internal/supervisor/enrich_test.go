package supervisor

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// eventReply is an EventDefault that answers every event turn with the
// given answer-contract JSON.
func eventReply(spoken, detail, relatesTo string) *braintest.Step {
	return &braintest.Step{Text: `{"spoken": ` + quote(spoken) + `, "detail": ` + quote(detail) + `, "relates_to": ` + quote(relatesTo) + `, "needs_user": false}`}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// doneAuth sends auth an instruction (so its finish is announced at
// normal) and finishes it on the after-approval screen.
func doneAuth(h *harness) {
	h.t.Helper()
	h.srv.SetScreen("w5:p1", fixture(h.t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
}

const doneSentence = "auth finished: created hello.txt with the requested line."

// The template announces at once; the brain's summarize turn then adds
// what the sentence lacked, at idle urgency, and its detail lands on
// last_result.detail for the ledger and recall.
func TestEnrichmentAddsContext(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = eventReply("That was the rerun you asked for; the same test, TestLoginRateLimit, still fails.", "The CEO asked for a rerun after the first failure; see /tmp/x.log.", "the rerun")
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 2 || got[0].Text != doneSentence || got[0].Urgency != voice.UrgencyIdle || got[1].Text != "That was the rerun you asked for; the same test, TestLoginRateLimit, still fails." || got[1].Urgency != voice.UrgencyIdle {
		t.Fatalf("pushes = %+v", got)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastResult == nil || row.LastResult.Detail != "The CEO asked for a rerun after the first failure; see a file" {
		t.Errorf("last_result = %+v", row.LastResult)
	}
	turns := h.log.ofType("brain.turn")
	if len(turns) != 1 || payload(turns[0])["kind"] != "summarize" || payload(turns[0])["trigger"] != "event" || payload(turns[0])["fallback"] != false {
		t.Errorf("brain.turn = %v", turns)
	}
	ev := h.log.ofType("brain.enrichment")
	if len(ev) != 1 || payload(ev[0])["relates_to"] != "the rerun" || payload(ev[0])["template"] != doneSentence {
		t.Errorf("brain.enrichment = %v", ev)
	}
	// The model saw the template and the screen the supervisor read; it
	// did not read again.
	call := m.Calls()[0]
	if text := call.Text(); !strings.Contains(text, `Event from the supervisor (summarize): agent="auth" event="done" template="`+doneSentence+`"`) || !strings.Contains(text, "You approved codex to run") {
		t.Errorf("model saw:\n%s", text)
	}
	if reads := h.srv.RequestsFor("pane.read"); len(reads) != 1 {
		t.Errorf("pane reads = %d", len(reads))
	}
	// The ledger carries the detail for the next turn; recall finds it.
	info, _ := brainHost{h.sup}.Resolve("auth")
	if info.LastDetail == "" {
		t.Errorf("AgentInfo.LastDetail empty: %+v", info)
	}
}

// An enrichment that mostly repeats the sentence is dropped.
func TestEnrichmentSkippedWhenDuplicate(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = eventReply("Auth finished and created hello.txt with the requested line!", "", "")
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Fatalf("pushes = %v", got)
	}
	sk := h.log.ofType("brain.enrichment_skipped")
	if len(sk) != 1 || payload(sk[0])["reason"] != "duplicate" {
		t.Errorf("enrichment_skipped = %v", sk)
	}
}

func TestTokenOverlap(t *testing.T) {
	template := "auth finished: created hello.txt with the requested line."
	// The template has 9 distinct tokens ("hello.txt" splits in two).
	cases := []struct {
		enrichment string
		want       float64
		dup        bool
	}{
		{"Auth finished and created hello.txt with the requested line!", 0.9, true},          // "and" is new: 9 of 10
		{"Auth created hello.txt with the requested line, as you asked.", 8.0 / 11.0, false}, // "as", "you", "asked" are new
		{"That was the rerun you asked for; the same test still fails.", 1.0 / 11.0, false},  // only "the"
		{"auth finished: created hello.txt with the requested line.", 1, true},               // verbatim
		{"the requested line was created in hello.txt auth finished", 0.8, false},            // exactly 0.8 is not "> 0.8"
		{"the requested line was created in hello.txt auth finished, txt, TXT", 0.8, false},  // distinct tokens only
		{"", 0, false},
	}
	for _, tc := range cases {
		if got := tokenOverlap(tc.enrichment, template); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Errorf("tokenOverlap(%q) = %v, want %v", tc.enrichment, got, tc.want)
		}
		if got := nearDuplicate(tc.enrichment, template); got != tc.dup {
			t.Errorf("nearDuplicate(%q) = %v", tc.enrichment, got)
		}
	}
}

// A slow brain adds nothing: the template sentence was already spoken,
// and the timeout is logged as brain.enrichment_skipped.
func TestEnrichmentSkippedOnTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Block: block}
	log := &recLog{}
	dir := t.TempDir()
	b, err := brain.New(brain.Config{DataDir: dir, RunID: "t", EnrichTimeout: 30 * time.Millisecond, FinalGrace: -1}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	h := newHarness(t, func(c *Config) { c.Brain = b; c.DataDir = dir })
	h.log, h.sup.log, h.client.Log = log, log, log
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	start := time.Now()
	doneAuth(h)
	if time.Since(start) > 2*time.Second {
		t.Errorf("finish took %s", time.Since(start))
	}
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Fatalf("pushes = %v", got)
	}
	sk := h.log.ofType("brain.enrichment_skipped")
	if len(sk) != 1 || payload(sk[0])["reason"] != "timeout" {
		t.Errorf("enrichment_skipped = %v", sk)
	}
	if h.log.count("brain.fallback") != 1 {
		t.Errorf("brain.fallback = %d", h.log.count("brain.fallback"))
	}
}

// Quiet never enriches, even a question the level does speak; normal
// does not enrich a finish it did not announce.
func TestEnrichmentFollowsChattiness(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = eventReply("That was the one you asked about.", "", "")
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := h.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "auth is asking") {
		t.Fatalf("pushes = %v", got)
	}
	sk := h.log.ofType("brain.enrichment_skipped")
	if len(sk) != 1 || payload(sk[0])["reason"] != "chattiness" || payload(sk[0])["chattiness"] != "quiet" || len(m.Calls()) != 0 {
		t.Errorf("enrichment_skipped = %v (model calls %d)", sk, len(m.Calls()))
	}
	// normal, an agent the user never dealt with: no announcement, no
	// enrichment.
	if err := h.sup.SetSettings(Settings{Chattiness: Normal}); err != nil {
		t.Fatal(err)
	}
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	h.srv.SetScreen("w6:p1", fixture(t, "claude-after-approval.screen.txt"))
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.push_quiet") != 1 || h.log.count("brain.enrichment_skipped") != 2 || len(m.Calls()) != 0 {
		t.Errorf("push_quiet=%d skipped=%d calls=%d", h.log.count("supervisor.push_quiet"), h.log.count("brain.enrichment_skipped"), len(m.Calls()))
	}
	// chatty: announced and enriched.
	if err := h.sup.SetSettings(Settings{Chattiness: Chatty}); err != nil {
		t.Fatal(err)
	}
	h.transition("w6:p1", herdr.StateDone, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 3 || got[2] != "That was the one you asked about." {
		t.Errorf("pushes = %v", got)
	}
}

// An event turn cannot act: a model that tries prompt or send_keys in a
// summarize turn is refused by the executor and nothing reaches herdr.
func TestEnrichmentTurnCannotAct(t *testing.T) {
	m := braintest.NewModel()
	n := 0
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		n++
		if n == 1 {
			return braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{
				braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "fix it"}),
				braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"enter"}}),
			}}), nil
		}
		return braintest.Response(*eventReply("", "", "")), nil
	}}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if len(h.promptRequests()) != 0 || len(h.sendKeysRequests()) != 0 {
		t.Errorf("an event turn acted: prompts=%v keys=%v", h.promptRequests(), h.sendKeysRequests())
	}
	if h.log.count("brain.rail_blocked") != 2 {
		t.Errorf("rail_blocked = %d", h.log.count("brain.rail_blocked"))
	}
	if p, has := h.pending(); !has || p.PaneID != "w5:p1" {
		t.Errorf("the question is not pending: %+v %v", p, has)
	}
}

// Without a brain nothing changes: one push, no events.
func TestNoBrainNoEnrichment(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Errorf("pushes = %v", got)
	}
	if h.log.count("brain.enrichment_skipped") != 0 || h.log.count("brain.turn") != 0 {
		t.Error("brain events without a brain")
	}
}
