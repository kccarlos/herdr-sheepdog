package supervisor

import (
	"context"
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

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// doneAuth finishes auth on the after-approval screen.
func doneAuth(h *harness) {
	h.t.Helper()
	h.srv.SetScreen("w5:p1", fixture(h.t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
}

const doneSentence = "auth finished: created hello.txt with the requested line."

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
	m.EventDefault = announceReply("Auth finished the rerun you asked for; the same test, TestLoginRateLimit, still fails.", "idle", "The CEO asked for a rerun after the first failure; see /tmp/x.log.", "the rerun", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Text != "Auth finished the rerun you asked for; the same test, TestLoginRateLimit, still fails." || got[0].Urgency != voice.UrgencyIdle {
		t.Fatalf("pushes = %+v", got)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastResult == nil || row.LastResult.Detail != "The CEO asked for a rerun after the first failure; see a file" {
		t.Errorf("last_result = %+v", row.LastResult)
	}
	turns := h.log.ofType("brain.turn")
	if len(turns) != 1 || payload(turns[0])["kind"] != "announce" || payload(turns[0])["trigger"] != "event" || payload(turns[0])["fallback"] != false {
		t.Errorf("brain.turn = %v", turns)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["via"] != "brain" || d[0]["urgency_requested"] != "idle" || d[0]["urgency_applied"] != "idle" || d[0]["reason"] != "brain" || d[0]["ceiling"] != "normal" || d[0]["relates_to"] != "the rerun" || d[0]["template"] != doneSentence || d[0]["template_spoken"] != false || d[0]["late"] != false {
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
	m.EventDefault = announceReply("", "skip", "The CEO just heard this from the assistant.", "", false)
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
	m.EventDefault = announceReply("Auth is done; the CEO has not asked about it.", "silent", "", "", false)
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Urgency != voice.UrgencySilent || got[0].Text != "Auth is done; the CEO has not asked about it." {
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
		// an addition to a question: never at quiet; interrupt only for
		// a decision while the question is still open.
		{Quiet, pushQuestion, "idle", ceilingInput{addition: true}, "skip", "ceiling"},
		{Normal, pushQuestion, "idle", ceilingInput{addition: true}, "idle", "brain"},
		{Normal, pushQuestion, "interrupt", ceilingInput{addition: true, needsUser: true, pendingOpen: true}, "interrupt", "brain"},
		{Normal, pushQuestion, "interrupt", ceilingInput{addition: true, needsUser: true}, "idle", "ceiling"},
		{Chatty, pushQuestion, "interrupt", ceilingInput{addition: true, pendingOpen: true}, "idle", "ceiling"},
		{Quiet, pushBlockedInfo, "idle", ceilingInput{addition: true}, "skip", "ceiling"},
		{Normal, pushBlockedInfo, "idle", ceilingInput{addition: true}, "idle", "brain"},
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
	m.EventDefault = announceReply("Auth is done with the tests; look now.", "interrupt", "", "", false)
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
	if len(d) != 2 || d[1]["urgency_applied"] != "skip" || d[1]["reason"] != "ceiling" || d[1]["ceiling"] != "normal" || d[1]["urgency_requested"] != "" {
		t.Errorf("push_decision = %v", d)
	}
	if pq := h.log.ofType("supervisor.push_quiet"); len(pq) != 1 || payload(pq[0])["chattiness"] != "normal" || !strings.HasPrefix(payload(pq[0])["text"].(string), "docs finished:") {
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

// The first audible announcement never waits for the brain beyond
// AnnounceWait: the template is spoken when the wait elapses (with the
// promise line as before), and the brain's line follows as an addition
// when it adds something — or is dropped as a duplicate.
func TestAnnounceWaitElapses(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel()
	var pusher *fakePusher
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		<-release
		return braintest.Response(*announceReply("That was the rerun you asked for; the same test still fails.", "idle", "", "the rerun", false)), nil
	}}
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 30 * time.Millisecond })
	pusher = h.pusher
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	// Release the model once the template has been spoken.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for len(pusher.all()) < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	doneAuth(h)
	got := h.pushTexts()
	if len(got) != 3 || got[0] != doneSentence || got[1] != "As you asked: "+doneSentence || got[2] != "That was the rerun you asked for; the same test still fails." {
		t.Fatalf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 2 || d[0]["via"] != "template" || d[0]["reason"] != "wait" || d[0]["urgency_applied"] != "idle" || d[1]["via"] != "brain" || d[1]["late"] != true || d[1]["urgency_applied"] != "idle" || d[1]["reason"] != "brain" {
		t.Errorf("push_decision = %v", d)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 1 || payload(kept[0])["via"] != "template" {
		t.Errorf("promise.kept = %v", kept)
	}
	// A late line that repeats the sentence is dropped.
	release2 := make(chan struct{})
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		<-release2
		return braintest.Response(*announceReply("Auth finished and created hello.txt with the requested line!", "idle", "", "", false)), nil
	}}
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for len(pusher.all()) < 4 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release2)
	}()
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 4 || got[3] != doneSentence {
		t.Fatalf("pushes = %v", got)
	}
	if d := h.decisions(); len(d) != 4 || d[3]["urgency_applied"] != "skip" || d[3]["reason"] != "duplicate" || d[3]["late"] != true {
		t.Errorf("push_decision = %v", d)
	}
}

// Brain off, timing out, or falling back: exactly the phase-2 template
// path — the sentence when the level allows it, "As you asked: …" for a
// promise at every level, nothing else.
func TestAnnounceFallsBackToTemplate(t *testing.T) {
	// Off.
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 2 || got[0] != doneSentence || got[1] != "As you asked: "+doneSentence {
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
	b, err := brain.New(brain.Config{DataDir: dir, RunID: "t", EnrichTimeout: 30 * time.Millisecond, FinalGrace: -1}, m, log)
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

// A question is never delayed: its push precedes the brain's first
// step, and the brain's line is an addition — interrupt only while the
// question is still pending and the brain says the CEO must decide.
func TestAnnounceBlockNeverDelayed(t *testing.T) {
	m := braintest.NewModel()
	var pusher *fakePusher
	var mu sync.Mutex
	pushesAtStep := -1
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		mu.Lock()
		pushesAtStep = len(pusher.all())
		mu.Unlock()
		return braintest.Response(*announceReply("Auth wants to write hello.txt; that is the file you said to keep small.", "interrupt", "", "", true)), nil
	}}
	h := newBrainHarness(t, m)
	pusher = h.pusher
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.pusher.all()
	if len(got) != 2 || !strings.HasPrefix(got[0].Text, "auth is asking") || got[0].Urgency != voice.UrgencyIdle || got[1].Text != "Auth wants to write hello.txt; that is the file you said to keep small." || got[1].Urgency != voice.UrgencyInterrupt {
		t.Fatalf("pushes = %+v", got)
	}
	mu.Lock()
	n := pushesAtStep
	mu.Unlock()
	if n != 1 {
		t.Errorf("the brain's first step saw %d pushes; the question must be out first", n)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["template_spoken"] != true || d[0]["urgency_applied"] != "interrupt" || d[0]["needs_user"] != true {
		t.Errorf("push_decision = %v", d)
	}
	// The question answered before the brain spoke: interrupt is capped.
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		h.call(ToolConfirm, map[string]any{"answer": "yes"})
		return braintest.Response(*announceReply("Auth wants to write hello.txt; that is the file you said to keep small.", "interrupt", "", "", true)), nil
	}}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got = h.pusher.all()
	if last := got[len(got)-1]; last.Urgency != voice.UrgencyIdle {
		t.Errorf("addition after the answer = %+v", last)
	}
	if d := h.decisions(); d[len(d)-1]["reason"] != "ceiling" {
		t.Errorf("push_decision = %v", d[len(d)-1])
	}
}

// Quiet never adds to a question (no model call); normal does not run a
// turn for a finish it would not announce; chatty announces and lets the
// brain phrase it.
func TestAnnounceFollowsChattiness(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Docs finished the readme you asked for.", "idle", "", "the readme", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := h.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "auth is asking") {
		t.Fatalf("pushes = %v", got)
	}
	d := h.decisions()
	if len(d) != 1 || d[0]["reason"] != "ceiling" || d[0]["ceiling"] != "quiet" || d[0]["urgency_applied"] != "skip" || len(m.Calls()) != 0 {
		t.Errorf("push_decision = %v (model calls %d)", d, len(m.Calls()))
	}
	if err := h.sup.SetSettings(Settings{Chattiness: Normal}); err != nil {
		t.Fatal(err)
	}
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	h.srv.SetScreen("w6:p1", fixture(t, "claude-after-approval.screen.txt"))
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.push_quiet") != 1 || len(h.decisions()) != 2 || len(m.Calls()) != 0 {
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
	if len(m.Calls()) != 1 {
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
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = -1 })
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
		res = h.call(ToolAsk, map[string]any{"question": "is auth done?", "agent": "auth"})
	}()
	for ceo, _ := h.sup.Brain().Queued(); ceo == 0 && time.Now().Before(deadline); ceo, _ = h.sup.Brain().Queued() {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	h.sup.Brain().Quiesce()
	if res.Spoken != "Auth is done." {
		t.Errorf("ask = %+v", res)
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
	if strings.Join(trig, " ") != "announce ask" {
		t.Errorf("brain.turn kinds = %v", trig)
	}
}

// Two finishes of the same agent queued behind a busy brain: the older
// announce turn is coalesced into the newer and speaks its template.
func TestAnnounceCoalesces(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("Docs is done.", "")})
	m.EventDefault = announceReply("Auth finished; the tests pass now.", "idle", "", "", false)
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = -1; c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.call(ToolAsk, map[string]any{"question": "is docs done?", "agent": "docs"})
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
	if len(got) != 2 || got[0] != doneSentence || got[1] != "Auth finished; the tests pass now." {
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
// when the brain skips.
func TestAnnouncePromiseOnlyEvents(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Docs is idle now, as you asked.", "idle", "", "", false)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	if _, err := h.sup.createPromise("w6:p1", brain.PromiseRequest{Text: "tell me when docs is idle", Event: "idle"}); err != nil {
		t.Fatal(err)
	}
	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "Docs is idle now, as you asked." {
		t.Errorf("pushes = %v", got)
	}
	if h.log.count("supervisor.push_quiet") != 0 {
		t.Error("push_quiet for a promise-only sentence")
	}
	// No promise: no turn at all.
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)
	if len(m.Calls()) != 1 || len(h.decisions()) != 1 {
		t.Errorf("calls=%d decisions=%d", len(m.Calls()), len(h.decisions()))
	}
	row, _ := h.store.Get("w6:p1")
	_ = row
	if st := h.store.Snapshot().Session.Promises; len(st) != 1 || st[0].Status != state.PromiseKept {
		t.Errorf("promises = %+v", st)
	}
}

func TestTokenOverlap(t *testing.T) {
	template := "auth finished: created hello.txt with the requested line."
	// The template has 9 distinct tokens ("hello.txt" splits in two).
	cases := []struct {
		line string
		want float64
		dup  bool
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
		if got := tokenOverlap(tc.line, template); got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Errorf("tokenOverlap(%q) = %v, want %v", tc.line, got, tc.want)
		}
		if got := nearDuplicate(tc.line, template); got != tc.dup {
			t.Errorf("nearDuplicate(%q) = %v", tc.line, got)
		}
	}
}
