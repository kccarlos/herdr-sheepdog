package supervisor

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// announceReply is an EventDefault that answers every announce turn with
// the given line and urgency.
func announceReply(spoken, urgency, detail, relatesTo string, needsUser bool) *braintest.Step {
	nu := "false"
	if needsUser {
		nu = "true"
	}
	return &braintest.Step{Text: `{"spoken": ` + quote(spoken) + `, "urgency": ` + quote(urgency) + `, "detail": ` + quote(detail) + `, "relates_to": ` + quote(relatesTo) + `, "needs_user": ` + nu + `}`}
}

// withAsk adds the ear contract's ask to an announce reply.
func withAsk(step *braintest.Step, ask string) *braintest.Step {
	step.Text = `{"ask": ` + quote(ask) + `, ` + strings.TrimPrefix(step.Text, "{")
	return step
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// doneAuth finishes auth on the after-approval screen.
func doneAuth(h *harness) {
	h.t.Helper()
	h.srv.SetScreen("w5:p1", fixture(h.t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
}

// doneSentence is the daemon's own sentence for a finish: with the
// template LLM gone, nothing but the brain phrases anything more.
const doneSentence = "auth finished."

// decisions returns the brain.push_decision payloads.
func (h *harness) decisions() []map[string]any {
	var out []map[string]any
	for _, ev := range h.log.ofType("brain.push_decision") {
		out = append(out, payload(ev))
	}
	return out
}

// The brain's line replaces the template sentence: one push for the
// finish, at the urgency the brain asked for, its detail on
// last_result.detail for the ledger and recall, one push_decision.
func TestAnnounceReplacesTemplate(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth finished the rerun you asked for. The same test, TestLoginRateLimit, still fails.", "idle", "The user asked for a rerun after the first failure; see /tmp/x.log.", "the rerun", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Text != "Auth finished the rerun you asked for. The same test, TestLoginRateLimit, still fails." || got[0].Urgency != voice.UrgencyIdle {
		t.Fatalf("pushes = %+v", got)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastResult == nil || row.LastResult.Detail != "The user asked for a rerun after the first failure; see a file" {
		t.Errorf("last_result = %+v", row.LastResult)
	}
	turns := h.log.ofType("brain.turn")
	if len(turns) != 1 || payload(turns[0])["kind"] != "announce" || payload(turns[0])["trigger"] != "event" || payload(turns[0])["fallback"] != false {
		t.Errorf("brain.turn = %v", turns)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["via"] != "brain" || d[0]["urgency_requested"] != "idle" || d[0]["urgency_applied"] != "idle" || d[0]["reason"] != "brain" || d[0]["ceiling"] != "normal" || d[0]["relates_to"] != "the rerun" || d[0]["template"] != doneSentence || d[0]["late"] != false {
		t.Errorf("push_decision = %v", d)
	}
	// The model saw the template and the screen the supervisor read; it
	// did not read again.
	call := m.Calls()[0]
	if text := call.Text(); !strings.Contains(text, `Event from the supervisor (announce): agent="auth" event="done" template="`+doneSentence+`"`) || !strings.Contains(text, "You approved codex to run") {
		t.Errorf("model saw:\n%s", text)
	}
	if reads := h.srv.RequestsFor("pane.read"); len(reads) != 1 {
		t.Errorf("pane reads = %d", len(reads))
	}
	info, _ := brainHost{h.sup}.Resolve("auth")
	if info.LastDetail == "" {
		t.Errorf("AgentInfo.LastDetail empty: %+v", info)
	}
	if h.log.count("supervisor.push_quiet") != 0 {
		t.Error("push_quiet logged for a spoken finish")
	}
}

// skip: nothing is spoken, the decision is logged; silent: a context
// push only (no subject change, not in the brain's transcript as a
// spoken push).
func TestAnnounceSkipAndSilent(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("", "skip", "The user just heard this from the assistant.", "", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	if got := h.pusher.all(); len(got) != 0 {
		t.Fatalf("pushes = %+v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["urgency_requested"] != "skip" || d[0]["urgency_applied"] != "skip" || d[0]["reason"] != "brain" || d[0]["spoken"] != "" {
		t.Errorf("push_decision = %v", d)
	}
	if h.log.count("supervisor.push_quiet") != 0 {
		t.Error("a brain skip is not the ceiling's doing")
	}
	// An empty spoken with any other urgency is a skip too.
	m.EventDefault = announceReply("", "idle", "", "", false)
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	if got := h.pusher.all(); len(got) != 0 {
		t.Fatalf("pushes = %+v", got)
	}
	if d := h.decisions(); len(d) != 2 || d[1]["urgency_requested"] != "skip" {
		t.Errorf("push_decision = %v", d)
	}
	// silent: context for the assistant, not speech.
	m.EventDefault = announceReply("Auth is done. The user has not asked about it.", "silent", "", "", false)
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Urgency != voice.UrgencySilent || got[0].Text != "Auth is done. The user has not asked about it." {
		t.Fatalf("pushes = %+v", got)
	}
	if d := h.decisions(); len(d) != 3 || d[2]["urgency_applied"] != "silent" {
		t.Errorf("push_decision = %v", d)
	}
	for _, msg := range h.sup.Brain().Transcript().Messages() {
		if msg.Kind == brain.KindPush {
			t.Errorf("a silent push reached the brain's transcript: %+v", msg)
		}
	}
}

// The ceiling table: what each level does with what the brain asked
// for, for a finish the CEO dealt with (involved), one they did not
// (other), a kept promise and an addition to a question.
func TestApplyCeilingTable(t *testing.T) {
	type tc struct {
		level     Chattiness
		kind      pushKind
		requested string
		in        ceilingInput
		applied   string
		reason    string
	}
	cases := []tc{
		// skip and silent pass at every level.
		{Quiet, pushDoneInvolved, "skip", ceilingInput{}, "skip", "brain"},
		{Chatty, pushDoneOther, "silent", ceilingInput{}, "silent", "brain"},
		// quiet: no finish is spoken, whatever the brain asked.
		{Quiet, pushDoneInvolved, "idle", ceilingInput{}, "skip", "ceiling"},
		{Quiet, pushDoneInvolved, "interrupt", ceilingInput{needsUser: true}, "skip", "ceiling"},
		// normal: the recent_minutes rule.
		{Normal, pushDoneInvolved, "idle", ceilingInput{}, "idle", "brain"},
		{Normal, pushDoneOther, "idle", ceilingInput{}, "skip", "ceiling"},
		{Normal, pushDoneOther, "interrupt", ceilingInput{needsUser: true}, "skip", "ceiling"},
		// chatty: anything the brain says, but interrupt only for a decision.
		{Chatty, pushDoneOther, "idle", ceilingInput{}, "idle", "brain"},
		{Chatty, pushDoneOther, "interrupt", ceilingInput{}, "idle", "ceiling"},
		{Chatty, pushDoneOther, "interrupt", ceilingInput{needsUser: true}, "interrupt", "brain"},
		{Normal, pushDoneInvolved, "interrupt", ceilingInput{}, "idle", "ceiling"},
		{Normal, pushDoneInvolved, "interrupt", ceilingInput{needsUser: true}, "interrupt", "brain"},
		// a kept promise is spoken at every level; interrupt past due
		// or for a decision, else idle.
		{Quiet, pushDoneOther, "idle", ceilingInput{kept: true}, "idle", "brain"},
		{Quiet, pushDoneOther, "interrupt", ceilingInput{kept: true}, "idle", "ceiling"},
		{Quiet, pushDoneOther, "idle", ceilingInput{kept: true, duePassed: true}, "interrupt", "promise_due"},
		{Normal, pushDoneOther, "interrupt", ceilingInput{kept: true, duePassed: true}, "interrupt", "brain"},
		{Normal, pushDoneOther, "interrupt", ceilingInput{kept: true, needsUser: true}, "interrupt", "brain"},
		{Chatty, pushPromiseOnly, "idle", ceilingInput{kept: true}, "idle", "brain"},
		// The finish of the CEO's own instruction is owed like a promise.
		{Quiet, pushDoneInvolved, "idle", ceilingInput{owed: true}, "idle", "brain"},
		{Normal, pushDoneOther, "idle", ceilingInput{owed: true}, "idle", "brain"},
		{Normal, pushDoneOther, "skip", ceilingInput{owed: true}, "skip", "brain"},
		{Quiet, pushDoneOther, "interrupt", ceilingInput{owed: true}, "idle", "ceiling"},
		// a question is spoken at every level; interrupt only for a
		// decision the shepherd must take.
		{Quiet, pushQuestion, "idle", ceilingInput{}, "idle", "brain"},
		{Quiet, pushQuestion, "interrupt", ceilingInput{needsUser: true}, "interrupt", "brain"},
		{Quiet, pushBlockedInfo, "idle", ceilingInput{}, "skip", "ceiling"},
		{Normal, pushBlockedInfo, "idle", ceilingInput{}, "idle", "brain"},
	}
	for _, c := range cases {
		d := applyCeiling(c.level, c.kind, c.requested, c.in)
		if d.applied != c.applied || d.reason != c.reason || d.requested != c.requested {
			t.Errorf("applyCeiling(%s, kind %d, %s, %+v) = %s/%s, want %s/%s", c.level, c.kind, c.requested, c.in, d.applied, d.reason, c.applied, c.reason)
		}
	}
	if requestedUrgency("INTERRUPT ", "x") != "interrupt" || requestedUrgency("loud", "x") != "idle" || requestedUrgency("", "x") != "idle" || requestedUrgency("idle", "") != "skip" || requestedUrgency("silent", "") != "skip" {
		t.Error("requestedUrgency")
	}
}

// The ceiling in the running supervisor: the brain asks for interrupt
// on a finish and gets idle (push_decision reason ceiling); at normal a
// finish of an agent the CEO never dealt with is skipped without a
// model call (push_quiet as before); at quiet even a kept promise stays
// idle unless it is due.
func TestAnnounceCeilingApplied(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth is done with the tests. Look now.", "interrupt", "", "", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Urgency != voice.UrgencyIdle {
		t.Fatalf("pushes = %+v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["urgency_requested"] != "interrupt" || d[0]["urgency_applied"] != "idle" || d[0]["reason"] != "ceiling" || d[0]["ceiling"] != "chatty" {
		t.Errorf("push_decision = %v", d)
	}
	// normal, docs never dealt with: no turn, no push, push_quiet.
	if err := h.sup.SetSettings(Settings{Chattiness: Normal}); err != nil {
		t.Fatal(err)
	}
	h.srv.SetScreen("w6:p1", fixture(t, "claude-after-approval.screen.txt"))
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if len(h.pusher.all()) != 1 || len(m.Calls()) != 1 {
		t.Errorf("pushes=%d calls=%d", len(h.pusher.all()), len(m.Calls()))
	}
	d = h.decisions()
	if len(d) != 2 || d[1]["urgency_applied"] != "skip" || d[1]["reason"] != "ceiling" || d[1]["ceiling"] != "normal" || d[1]["urgency_requested"] != "" || d[1]["via"] != "ceiling" {
		t.Errorf("push_decision = %v", d)
	}
	if pq := h.log.ofType("supervisor.push_quiet"); len(pq) != 1 || payload(pq[0])["chattiness"] != "normal" || payload(pq[0])["text"] != "docs finished." {
		t.Errorf("push_quiet = %v", pq)
	}
	// quiet, a kept promise: spoken at idle although the brain asked to
	// interrupt; due → interrupt whatever the brain asked.
	if err := h.sup.SetSettings(Settings{Chattiness: Quiet}); err != nil {
		t.Fatal(err)
	}
	m.EventDefault = announceReply("You asked to be told: auth finished the tests.", "interrupt", "", "", false)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	got = h.pusher.all()
	if len(got) != 2 || got[1].Text != "You asked to be told: auth finished the tests." || got[1].Urgency != voice.UrgencyIdle {
		t.Fatalf("pushes = %+v", got)
	}
	if d = h.decisions(); d[2]["urgency_applied"] != "idle" || d[2]["reason"] != "ceiling" {
		t.Errorf("push_decision = %v", d[2])
	}
	m.EventDefault = announceReply("You asked to be told: auth finished the tests.", "idle", "", "", false)
	due := h.clock.Now().Add(-time.Second)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "interrupt me when it's done", Event: "done", Due: &due}); err != nil {
		t.Fatal(err)
	}
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	got = h.pusher.all()
	if len(got) != 3 || got[2].Urgency != voice.UrgencyInterrupt {
		t.Fatalf("pushes = %+v", got)
	}
	if d = h.decisions(); d[3]["urgency_applied"] != "interrupt" || d[3]["reason"] != "promise_due" {
		t.Errorf("push_decision = %v", d[3])
	}
	// A due promise is the exception that keeps INTERRUPT scheduling
	// even while the model is speaking (docs/roles.md "Pushes"); the
	// gemini session reads this tag off the voice.push event.
	if tags := h.pusher.tagsOf(2); tags["due_promise"] != true {
		t.Errorf("tags = %v, want due_promise: true", tags)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 2 || payload(kept[1])["urgency"] != "interrupt" || payload(kept[1])["via"] != "brain" {
		t.Errorf("promise.kept = %v", kept)
	}
}

// The brain may not silence a promise: a skip while a promise is kept
// speaks the template's "As you asked" line, still one line.
func TestAnnounceSkipCannotSilencePromise(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("", "skip", "", "", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	p, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"})
	if err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+doneSentence {
		t.Fatalf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["urgency_requested"] != "skip" || d[0]["urgency_applied"] != "skip" || d[0]["reason"] != "promise" || d[0]["via"] != "template" {
		t.Errorf("push_decision = %v", d)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 1 || payload(kept[0])["id"] != p.ID || payload(kept[0])["via"] != "template" {
		t.Errorf("promise.kept = %v", kept)
	}
}

// slowAnnounce is an announce turn that takes `took` on the harness clock
// (and longer than AnnounceWait in real time) before it answers rep.
func slowAnnounce(h **harness, took time.Duration, rep *braintest.Step) *braintest.Step {
	return &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		time.Sleep(60 * time.Millisecond)
		(*h).clock.Advance(took)
		return braintest.Response(*rep), nil
	}}
}

// The ear contract applies to announcements too (iteration 2 WP1): the
// brain's finish line has its dash-joined clauses split and no PR number,
// its ask is heard last, and nothing short of a runaway (bench #13, one
// high cap for every line) is cut.
func TestAnnounceLineIsCappedForTheEar(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = withAsk(announceReply("Auth merged the lag fix you asked for as #49 — the tests pass — and the docs are updated too.", "idle", "A merge puts the change into the shared code.", "", false), "Shall I have it start the next one?")
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	got := h.pusher.all()
	const want = "Auth merged the lag fix you asked for as. The tests pass. And the docs are updated too. Shall I have it start the next one?"
	if len(got) != 1 || got[0].Text != want {
		t.Fatalf("pushes = %+v, want one line %q", got, want)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastResult == nil || row.LastResult.Spoken != want || row.LastResult.Detail != "A merge puts the change into the shared code." {
		t.Errorf("last_result = %+v", row.LastResult)
	}
}

// A finish waits for the brain's line (iteration 2 WP2; eval
// finish-line-needs-user-spoken): at 2026-09-24 00:52:37 the shepherd
// heard "discoursecopilot-claude has finished its task." and the brain's
// line with the finding and a question for them, 4.5 s later, was
// dropped. Now the finish is not spoken at AnnounceWait: the brain's line
// is spoken alone whenever its turn answers, for any finish the level
// speaks — nobody's in particular, the shepherd's own instruction, a kept
// promise (at every level) — with no push_late and the wait logged.
func TestFinishWaitsForTheBrainsLine(t *testing.T) {
	line := "Version 2.1.0 is finished and pushed to main. Shall I have it push the tag now, or wait until you've had a look?"
	cases := []struct {
		name  string
		level Chattiness
		setup func(h *harness)
		want  string
	}{
		{"nobody's, chatty", Chatty, func(*harness) {}, line},
		{"the user's instruction, normal", Normal, func(h *harness) {
			now := h.sup.cfg.Now()
			if _, err := h.store.Update("w5:p1", func(a *state.Agent) { a.CurrentTask, a.LastInteraction = "cut the release", &now }); err != nil {
				t.Fatal(err)
			}
		}, line},
		{"a kept promise, quiet", Quiet, func(h *harness) {
			if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when the release is out", Event: "done"}); err != nil {
				t.Fatal(err)
			}
		}, line},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var h *harness
			m := braintest.NewModel()
			m.EventDefault = slowAnnounce(&h, 6*time.Second, announceReply(line, "idle", "", "", true))
			h = newBrainHarness(t, m, func(cfg *Config) { cfg.AnnounceWait = 10 * time.Millisecond; cfg.Chattiness = string(c.level) })
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			c.setup(h)
			doneAuth(h)
			got := h.pusher.all()
			if len(got) != 1 || got[0].Text != c.want || got[0].Urgency != voice.UrgencyIdle {
				t.Fatalf("pushes = %+v", got)
			}
			d := h.decisions()
			if len(d) != 1 || d[0]["via"] != "brain" || d[0]["late"] != false || d[0]["wait"] != waitTurn || d[0]["waited_ms"] != int64(6000) {
				t.Errorf("push_decision = %v", d)
			}
			if n := h.log.count("brain.push_late"); n != 0 {
				t.Errorf("brain.push_late = %d", n)
			}
			if row, _ := h.store.Get("w5:p1"); row.LastResult == nil || row.LastResult.Spoken != line {
				t.Errorf("last_result = %+v", row.LastResult)
			}
		})
	}
}

// A finish whose turn times out speaks the code sentence once, after the
// turn's own budget, never before it; an owed finish (the shepherd's own
// instruction, a kept promise) says in the same sentence that its
// findings could not be had.
func TestFinishTimeoutSpeaksTheCodeSentenceOnce(t *testing.T) {
	cases := []struct {
		name string
		owed bool
		want string
	}{
		{"not owed", false, doneSentence},
		{"a kept promise", true, "As you asked: auth finished, but I couldn't get its findings in time. Ask me and I'll read its screen."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block := make(chan struct{})
			defer close(block)
			m := braintest.NewModel()
			m.EventDefault = &braintest.Step{Block: block}
			log := &recLog{}
			dir := t.TempDir()
			const budget = 80 * time.Millisecond
			b, err := brain.New(brain.Config{DataDir: dir, RunID: "t", AnnounceTurnTimeout: budget, AnnounceTimeout: budget, FinalGrace: -1}, m, log)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = b.Close() })
			h := newHarness(t, func(cfg *Config) {
				cfg.Brain, cfg.DataDir, cfg.AnnounceWait, cfg.Chattiness = b, dir, 5*time.Millisecond, string(Chatty)
			})
			h.log, h.sup.log, h.client.Log = log, log, log
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			if c.owed {
				if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
					t.Fatal(err)
				}
			}
			// Nothing is spoken at AnnounceWait: the finish waits.
			pusher := h.pusher
			early := make(chan int, 1)
			go func() {
				time.Sleep(budget / 2)
				early <- len(pusher.all())
			}()
			start := time.Now()
			doneAuth(h)
			if n := <-early; n != 0 {
				t.Errorf("%d pushes before the turn's budget ran out", n)
			}
			if el := time.Since(start); el < budget {
				t.Errorf("finish spoke after %s, before the budget %s", el, budget)
			}
			if got := h.pushTexts(); len(got) != 1 || got[0] != c.want {
				t.Fatalf("pushes = %v", got)
			}
			d := h.decisions()
			if len(d) != 1 || d[0]["via"] != "template" || d[0]["reason"] != "timeout" || d[0]["wait"] != waitTurn {
				t.Errorf("push_decision = %v", d)
			}
		})
	}
}

// A late line about a block: the parsed prompt is spoken at AnnounceWait;
// a late line that says the shepherd must act is added once at idle —
// also for a block that created no pending action (widened from "open
// prompt only"); a late line without needs_user is dropped; a late line
// about a prompt answered meanwhile is dropped as stale.
func TestAnnounceLateBlockLines(t *testing.T) {
	const needs = "Auth wants to write hello.txt. That is the file you said to keep small."
	cases := []struct {
		name      string
		screen    string
		needsUser bool
		answer    bool
		first     string
		added     bool
		reason    string
	}{
		{"needs_user on an open prompt", "codex-approval-prompt.screen.txt", true, false, "auth asks to run", true, "needs_user"},
		{"needs_user on a prompt with nothing to answer", "codex-after-ctrl-c.screen.txt", true, false, "auth is waiting at a prompt.", true, "needs_user"},
		{"no needs_user", "codex-approval-prompt.screen.txt", false, false, "auth asks to run", false, "late"},
		{"prompt answered meanwhile", "codex-approval-prompt.screen.txt", true, true, "auth asks to run", false, "stale"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			release := make(chan struct{})
			m := braintest.NewModel()
			var h *harness
			m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
				<-release
				if c.answer {
					h.say("yes")
				}
				return braintest.Response(*announceReply(needs, "interrupt", "", "", c.needsUser)), nil
			}}
			// The "yes" is said from inside the event turn, which holds the
			// brain's turn lock: its call must not wait for its own turn.
			h = newBrainHarness(t, m, func(cfg *Config) { cfg.AnnounceWait = 30 * time.Millisecond; cfg.ReplyWait = 20 * time.Millisecond })
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			pusher := h.pusher
			go func() {
				deadline := time.Now().Add(5 * time.Second)
				for len(pusher.all()) < 1 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				close(release)
			}()
			h.block("w5:p1", c.screen)
			got := h.pushTexts()
			if len(got) == 0 || !strings.HasPrefix(got[0], c.first) {
				t.Fatalf("pushes = %v", got)
			}
			var spoke int
			for _, p := range h.pusher.all() {
				if p.Text == needs {
					spoke++
					if p.Urgency != voice.UrgencyIdle {
						t.Errorf("late line urgency = %v", p.Urgency)
					}
				}
			}
			if want := map[bool]int{true: 1, false: 0}[c.added]; spoke != want {
				t.Errorf("late line spoken %d times, want %d: %v", spoke, want, got)
			}
			late := h.log.ofType("brain.push_late")
			if len(late) != 1 || payload(late[0])["added"] != c.added || payload(late[0])["needs_user"] != c.needsUser {
				t.Errorf("brain.push_late = %v", late)
			}
			d := h.decisions()
			if len(d) != 2 || d[0]["reason"] != "wait" || d[0]["wait"] != waitAnnounce || d[0]["waited_ms"] != int64(30) || d[1]["late"] != true || d[1]["reason"] != c.reason {
				t.Errorf("push_decision = %v", d)
			}
		})
	}
}

// At quiet a finish nobody is owed is not spoken, so nothing waits for
// the brain: no model call, no push, the transition returns at once.
func TestQuietFinishDoesNotWait(t *testing.T) {
	var h *harness
	m := braintest.NewModel()
	m.EventDefault = slowAnnounce(&h, 6*time.Second, announceReply("Auth finished the refactor.", "idle", "", "the refactor", false))
	h = newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	start := time.Now()
	doneAuth(h)
	if el := time.Since(start); el > 50*time.Millisecond {
		t.Errorf("a quiet finish took %s", el)
	}
	if got := h.pusher.all(); len(got) != 0 {
		t.Fatalf("pushes = %+v", got)
	}
	if len(m.Calls()) != 0 {
		t.Errorf("model calls = %d", len(m.Calls()))
	}
	if d := h.decisions(); len(d) != 1 || d[0]["via"] != "ceiling" || d[0]["wait"] != nil {
		t.Errorf("push_decision = %v", d)
	}
}

// "<alias> started on …" for the brain's own prompt is silent context:
// the tool result already said it was sent (the duplicate speech of
// 2026-09-24 00:13:02). An instruction picked up long after it went out
// is news and spoken at chatty as before; a worker started from the
// terminal gets no "started on" at all (there is no instruction of ours).
func TestStartedPushSilentForTheBrainsOwnPrompt(t *testing.T) {
	h := threeAgents(t, func(c *Config) { c.Chattiness = string(Chatty) })
	started := `vps started on "run the tests".`
	h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	h.send("w7:p1", "run the tests")
	h.clock.Advance(time.Second)
	h.transition("w7:p1", herdr.StateDone, herdr.StateWorking)
	pushes := h.pusher.all()
	last := pushes[len(pushes)-1]
	if last.Text != started || last.Urgency != voice.UrgencySilent {
		t.Fatalf("just sent: last push = %+v", last)
	}
	if slices.Contains(h.pushTexts(), started) {
		t.Errorf("spoken: %v", h.pushTexts())
	}
	// Picked up past the window (the worker was busy): spoken.
	h.transition("w7:p1", herdr.StateWorking, herdr.StateDone)
	h.send("w7:p1", "run the tests")
	h.clock.Advance(h.sup.cfg.CancelWindow + time.Second)
	h.transition("w7:p1", herdr.StateDone, herdr.StateWorking)
	if got := h.pushTexts(); got[len(got)-1] != started {
		t.Errorf("picked up late: pushes = %v", got)
	}
	// Started from the terminal: nothing.
	n := len(h.pusher.all())
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	if got := h.pusher.all(); len(got) != n {
		t.Errorf("terminal start pushed %+v", got[n:])
	}
}

// Brain off, timing out, or falling back: the daemon's own sentence
// when the level allows it, "As you asked: …" for a promise at every
// level, nothing else.
func TestAnnounceFallsBackToTheCodeSentence(t *testing.T) {
	// Off.
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	// One line for the event: the kept promise carries the news.
	if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+doneSentence {
		t.Errorf("no brain: pushes = %v", got)
	}
	if h.log.count("brain.push_decision") != 0 || h.log.count("brain.turn") != 0 {
		t.Error("brain events without a brain")
	}
	// Timeout.
	block := make(chan struct{})
	defer close(block)
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Block: block}
	log := &recLog{}
	dir := t.TempDir()
	b, err := brain.New(brain.Config{DataDir: dir, RunID: "t", AnnounceTurnTimeout: 30 * time.Millisecond, FinalGrace: -1}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	h = newHarness(t, func(c *Config) { c.Brain = b; c.DataDir = dir; c.AnnounceWait = -1 })
	h.log, h.sup.log, h.client.Log = log, log, log
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	start := time.Now()
	doneAuth(h)
	if time.Since(start) > 2*time.Second {
		t.Errorf("finish took %s", time.Since(start))
	}
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Fatalf("timeout: pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["via"] != "template" || d[0]["reason"] != "timeout" || d[0]["urgency_applied"] != "idle" {
		t.Errorf("push_decision = %v", d)
	}
	if h.log.count("brain.fallback") != 1 {
		t.Errorf("brain.fallback = %d", h.log.count("brain.fallback"))
	}
	// Model error, with a promise, at quiet: the promise line alone.
	m = braintest.NewModel()
	m.EventDefault = &braintest.Step{Err: braintest.ErrBoom}
	h = newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+doneSentence {
		t.Errorf("fallback: pushes = %v", got)
	}
	if d := h.decisions(); len(d) != 1 || d[0]["reason"] != "fallback" || d[0]["urgency_applied"] != "skip" || d[0]["via"] != "template" {
		t.Errorf("push_decision = %v", d)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 1 || payload(kept[0])["via"] != "template" {
		t.Errorf("promise.kept = %v", kept)
	}
}

// A block waits for the brain like a finish does and speaks one line:
// the brain's when it lands in time, the parsed prompt's sentence when
// it does not. A line about a prompt that was answered meanwhile is
// dropped (brain.push_stale), which is what made the assistant tell the
// CEO to approve a prompt already gone (2026-09-18).
func TestAnnounceBlockOneLineAndStale(t *testing.T) {
	m := braintest.NewModel()
	// The ear contract: the decision is the reply's ask, heard last.
	m.EventDefault = withAsk(announceReply("Auth wants to write hello.txt. That is the file you said to keep small.", "interrupt", "", "", true), "Approve?")
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.pusher.all()
	if len(got) != 1 || got[0].Text != "Auth wants to write hello.txt. That is the file you said to keep small. Approve?" || got[0].Urgency != voice.UrgencyInterrupt {
		t.Fatalf("pushes = %+v", got)
	}
	// A decision on a block whose pending action this event created is
	// the other exception that keeps INTERRUPT while the model is
	// speaking, so the gemini session must see needs_user on it.
	if tags := h.pusher.tagsOf(0); tags["needs_user"] != true {
		t.Errorf("tags = %v, want needs_user: true", tags)
	}
	if p, has := h.pending(); !has || p.PaneID != "w5:p1" {
		t.Errorf("the prompt is not pending: %+v %v", p, has)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["urgency_applied"] != "interrupt" || d[0]["needs_user"] != true || d[0]["template"] == "" {
		t.Errorf("push_decision = %v", d)
	}
	// The question answered before the brain spoke: nothing is said
	// about a prompt that is gone.
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		h.say("yes")
		return braintest.Response(*announceReply("Auth wants to write hello.txt; say yes and it will.", "interrupt", "", "", true)), nil
	}}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := h.pusher.all(); len(got) != 1 {
		t.Errorf("a stale line was spoken: %+v", got[1:])
	}
	if stale := h.log.ofType("brain.push_stale"); len(stale) != 1 {
		t.Errorf("brain.push_stale = %v", stale)
	}
	if d := h.decisions(); d[len(d)-1]["reason"] != "stale" {
		t.Errorf("push_decision = %v", d[len(d)-1])
	}
}

// Quiet speaks a question but never enriches a finish (no model call);
// normal does not run a turn for a finish it would not announce; chatty
// announces and lets the brain phrase it.
func TestAnnounceFollowsChattiness(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Docs finished the readme you asked for.", "idle", "", "the readme", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	// A question is spoken at every level, so the brain phrases it.
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := h.pushTexts(); len(got) != 1 || got[0] != "Docs finished the readme you asked for." {
		t.Fatalf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["reason"] != "brain" || d[0]["ceiling"] != "quiet" || d[0]["urgency_applied"] != "idle" || len(m.Calls()) != 1 {
		t.Errorf("push_decision = %v (model calls %d)", d, len(m.Calls()))
	}
	if err := h.sup.SetSettings(Settings{Chattiness: Normal}); err != nil {
		t.Fatal(err)
	}
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	h.srv.SetScreen("w6:p1", fixture(t, "claude-after-approval.screen.txt"))
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.push_quiet") != 1 || len(h.decisions()) != 2 || len(m.Calls()) != 1 {
		t.Errorf("push_quiet=%d decisions=%d calls=%d", h.log.count("supervisor.push_quiet"), len(h.decisions()), len(m.Calls()))
	}
	if err := h.sup.SetSettings(Settings{Chattiness: Chatty}); err != nil {
		t.Fatal(err)
	}
	h.transition("w6:p1", herdr.StateDone, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 2 || got[1] != "Docs finished the readme you asked for." {
		t.Errorf("pushes = %v", got)
	}
	if len(m.Calls()) != 2 {
		t.Errorf("model calls = %d", len(m.Calls()))
	}
}

// A CEO turn queued while an announce turn runs: the announce turn
// yields, the template is spoken, and the CEO turn runs next.
func TestAnnounceYieldsToCEO(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel()
	first := true
	var mu sync.Mutex
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		mu.Lock()
		f := first
		first = false
		mu.Unlock()
		if f {
			<-release
			return braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRecall, map[string]any{"query": "auth"})}}), nil
		}
		return braintest.Response(*announceReply("Auth finished the rerun.", "idle", "", "", false)), nil
	}}
	m.Default = &braintest.Step{Text: braintest.Reply("Auth is done.", "")}
	// ReplyWait -1: the shepherd's turn waits behind the announce turn,
	// and its reply must be the tool result however long that takes.
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = -1; c.ReplyWait = -1 })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		doneAuth(h)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	var res voice.ToolResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		res = h.say("is auth done?")
	}()
	for ceo, _ := h.sup.Brain().Queued(); ceo == 0 && time.Now().Before(deadline); ceo, _ = h.sup.Brain().Queued() {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	h.sup.Brain().Quiesce()
	if res.Spoken != "Auth is done." {
		t.Errorf("reply = %+v", res)
	}
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Errorf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["via"] != "template" || d[0]["reason"] != "yielded" {
		t.Errorf("push_decision = %v", d)
	}
	if h.log.count("brain.yielded") != 1 {
		t.Errorf("brain.yielded = %d", h.log.count("brain.yielded"))
	}
	var trig []string
	for _, ev := range h.log.ofType("brain.turn") {
		trig = append(trig, payload(ev)["kind"].(string))
	}
	if strings.Join(trig, " ") != "announce utterance" {
		t.Errorf("brain.turn kinds = %v", trig)
	}
}

// Two finishes of the same agent queued behind a busy brain: the older
// announce turn is coalesced into the newer and speaks its template.
func TestAnnounceCoalesces(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("Docs is done.", "")})
	m.EventDefault = announceReply("Auth finished. The tests pass now.", "idle", "", "", false)
	// ReplyWait -1: the shepherd's turn is held on purpose; its reply is
	// the tool result, not a later push.
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = -1; c.Chattiness = string(Chatty); c.ReplyWait = -1 })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.say("is docs done?")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5:p1"})
	}()
	for _, ev := h.sup.Brain().Queued(); ev < 1 && time.Now().Before(deadline); _, ev = h.sup.Brain().Queued() {
		time.Sleep(time.Millisecond)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5:p1"})
	}()
	for h.log.count("brain.event_coalesced") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	h.sup.Brain().Quiesce()
	got := h.pushTexts()
	if len(got) != 2 || got[0] != doneSentence || got[1] != "Auth finished. The tests pass now." {
		t.Errorf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 2 || d[0]["reason"] != "coalesced" || d[0]["via"] != "template" || d[1]["via"] != "brain" {
		t.Errorf("push_decision = %v", d)
	}
}

// An event turn cannot act: a model that tries prompt or send_keys in an
// announce turn is refused by the executor and nothing reaches herdr.
func TestAnnounceTurnCannotAct(t *testing.T) {
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
		return braintest.Response(*braintest.NothingToAdd()), nil
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

// A working/idle promise is kept through an announce turn too (one
// line; the sentence itself is never announced), and by the template
// when the brain skips. done -> idle ("idle again") is the non-finish
// path this exercises; working -> idle is a finish (TestFinishEdges)
// and goes through onDone instead.
func TestAnnouncePromiseOnlyEvents(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Docs is idle now, as you asked.", "idle", "", "", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w6:p1", "docs", "claude", herdr.StateDone)
	if _, err := h.sup.createPromise("w6:p1", brain.PromiseRequest{Text: "tell me when docs is idle", Event: "idle"}); err != nil {
		t.Fatal(err)
	}
	h.transition("w6:p1", herdr.StateDone, herdr.StateIdle)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "Docs is idle now, as you asked." {
		t.Errorf("pushes = %v", got)
	}
	if h.log.count("supervisor.push_quiet") != 0 {
		t.Error("push_quiet for a promise-only sentence")
	}
	// No promise: working -> idle now goes through announce() as a
	// finish (it is no longer the promise-only path above), but at
	// quiet with nothing kept it logs one push_decision via the
	// ceiling and never calls the model.
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)
	if len(m.Calls()) != 1 || len(h.decisions()) != 2 {
		t.Errorf("calls=%d decisions=%d", len(m.Calls()), len(h.decisions()))
	}
	if d := h.decisions()[1]; d["via"] != "ceiling" || d["reason"] != "ceiling" {
		t.Errorf("second decision = %v", d)
	}
	if st := h.store.Snapshot().Session.Promises; len(st) != 1 || st[0].Status != state.PromiseKept {
		t.Errorf("promises = %+v", st)
	}
}

// TestKeptPromiseOnAStaleBlockIsLoggedDropped: docs/timelines.md C20 —
// a promise kept by a block whose prompt was answered while the brain
// wrote about it is marked kept, and its line is dropped with the stale
// block. The drop is logged (voice.push_dropped {reason: stale_block})
// instead of vanishing.
func TestKeptPromiseOnAStaleBlockIsLoggedDropped(t *testing.T) {
	m := braintest.NewModel()
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	p, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it asks", Event: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		h.say("yes")
		return braintest.Response(*announceReply("Auth wants to write hello.txt.", "interrupt", "", "", true)), nil
	}}
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if len(h.log.ofType("brain.push_stale")) != 1 {
		t.Fatalf("brain.push_stale = %v", h.log.ofType("brain.push_stale"))
	}
	d := h.log.ofType("voice.push_dropped")
	if len(d) != 1 {
		t.Fatalf("voice.push_dropped = %v", d)
	}
	pl := payload(d[0])
	ids, _ := pl["keeps_promise_ids"].([]string)
	if pl["reason"] != "stale_block" || len(ids) != 1 || ids[0] != p.ID || pl["text"] != "Auth wants to write hello.txt." {
		t.Errorf("voice.push_dropped = %v", pl)
	}
}

// TestKeptPromiseLineIsTaggedOwed: the line that keeps a promise carries
// keeps_promise_ids, so the voice queue lets it pass plain idle news
// (docs/timelines.md C29).
func TestKeptPromiseLineIsTaggedOwed(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth finished: all tests pass.", "idle", "", "", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	p, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"})
	if err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 {
		t.Fatalf("pushes = %+v", got)
	}
	ids, _ := h.pusher.tagsOf(0)["keeps_promise_ids"].([]string)
	if len(ids) != 1 || ids[0] != p.ID || !voice.PushOwed(h.pusher.tagsOf(0)) {
		t.Errorf("tags = %v, want keeps_promise_ids [%s]", h.pusher.tagsOf(0), p.ID)
	}
}

// An owed line is never suppressed by chattiness (tier-2 bench
// 2026-10-03 #6: the finish the CEO asked about was skipped by the
// ceiling, brain.push_decision {"via":"ceiling","urgency_applied":"skip"},
// supervisor.push_quiet). A kept promise was already spoken at every
// level; the finish of the CEO's own instruction now is too — at quiet,
// and at normal past the recent window — with the brain's line, or with
// the code sentence when the turn fails.
func TestOwedFinishBypassesCeiling(t *testing.T) {
	for _, level := range []Chattiness{Quiet, Normal} {
		t.Run(string(level), func(t *testing.T) {
			m := braintest.NewModel()
			m.EventDefault = announceReply("Auth finished the tests you asked for.", "idle", "", "the tests", false)
			h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(level) })
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			h.send("w5:p1", "run the tests")
			h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
			h.clock.Advance(31 * time.Minute) // past normal's recent window
			doneAuth(h)
			if got := h.pushTexts(); len(got) != 1 || got[0] != "Auth finished the tests you asked for." {
				t.Fatalf("pushes = %q", got)
			}
			for _, pq := range h.log.ofType("supervisor.push_quiet") {
				if strings.Contains(payload(pq)["text"].(string), "finished") {
					t.Errorf("push_quiet = %v", pq)
				}
			}
			if d := h.decisions(); len(d) != 1 || d[0]["via"] != "brain" || d[0]["urgency_applied"] != "idle" {
				t.Errorf("push_decision = %v", d)
			}
		})
	}
	// Without a brain the code sentence is the owed line, at quiet too.
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.send("w5:p1", "run the tests")
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != doneSentence {
		t.Fatalf("no brain, quiet: pushes = %q", got)
	}
	// A finish nobody asked for stays under the ceiling.
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 {
		t.Fatalf("an unowed finish at quiet was spoken: %q", got)
	}
}
