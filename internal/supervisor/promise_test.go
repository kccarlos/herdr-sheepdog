package supervisor

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// promiseModel scripts one user turn that sends an instruction and
// records a promise (the combined "run the tests and let me know when
// it's done"), and answers announce turns that keep a promise with one
// line (idle, the promise's id) and the others with nothing to add.
func promiseModel() *braintest.Model {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "run the tests"}),
			braintest.FC(brain.ToolPromise, map[string]any{"agent": "auth", "text": "let me know when it's done", "event": "done"}),
		}},
		braintest.Step{Text: braintest.Reply("Telling auth to run the tests. I will tell you when auth finishes.", "")},
	)
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		if strings.Contains(call.Text(), "Promises this event keeps") {
			return braintest.Response(braintest.Step{Text: `{"spoken": "You asked me to tell you when auth was done: it created hello.txt with the requested line, see /tmp/out.", "urgency": "idle", "keeps_promise_ids": ["pr-x"]}`}), nil
		}
		return braintest.Response(*braintest.NothingToAdd()), nil
	}}
	return m
}

// combined has the user say the combined utterance and checks the
// promise was recorded.
func combined(h *harness) state.Promise {
	h.t.Helper()
	res := h.say("run the tests and let me know when it's done")
	if res.Spoken != "Telling auth to run the tests. I will tell you when auth finishes." {
		h.t.Fatalf("reply = %+v", res)
	}
	open := h.sup.openPromises()
	if len(open) != 1 {
		h.t.Fatalf("open promises = %+v", open)
	}
	return open[0]
}

// A user turn records the promise through the promise tool: persisted in
// the state file, logged, in the orchestrator's transcript, and the instruction
// went in the same turn.
func TestPromiseCreatedFromCEOTurn(t *testing.T) {
	h := newBrainHarness(t, promiseModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	if p.PaneID != "w5:p1" || p.Alias != "auth" || p.Text != "let me know when it's done" || p.Event != "done" || p.Status != state.PromiseOpen || p.CreatedTurn != 1 || !p.CreatedAt.Equal(t0) {
		t.Errorf("promise = %+v", p)
	}
	if got := h.promptRequests(); len(got) != 1 || got[0][1] != withSignOff("run the tests") {
		t.Errorf("prompts = %v", got)
	}
	ev := h.log.ofType("promise.created")
	if len(ev) != 1 || payload(ev[0])["id"] != p.ID || payload(ev[0])["agent"] != "auth" || payload(ev[0])["event"] != "done" {
		t.Errorf("promise.created = %v", ev)
	}
	// On disk.
	st, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Snapshot().Session.Promises; len(got) != 1 || got[0].ID != p.ID {
		t.Errorf("persisted promises = %+v", got)
	}
	var kinds []string
	for _, m := range h.sup.Brain().Transcript().Messages() {
		kinds = append(kinds, string(m.Kind))
	}
	if got := strings.Join(kinds, " "); got != "worker_event ceo_utterance request brain_tool_call brain_tool_call brain_tool_result brain_tool_result promise reply" {
		t.Errorf("transcript = %s", got)
	}
	// The ledger of the next turn lists it.
	if !strings.Contains(brainHost{h.sup}.Promises()[0].Text, "let me know") {
		t.Error("host does not list the promise")
	}
}

// The promise is kept at the finish whatever the chattiness level: the
// orchestrator's announce line (D9-scrubbed) is the one line spoken for the
// finish — the template sentence is not spoken as well — at idle
// urgency, marked kept, logged.
func TestPromiseKeptAtEveryLevel(t *testing.T) {
	for _, level := range Levels {
		t.Run(string(level), func(t *testing.T) {
			m := promiseModel()
			h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(level) })
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			p := combined(h)
			h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
			h.clock.Advance(time.Minute)
			doneAuth(h)
			got := h.pusher.all()
			want := "You asked me to tell you when auth was done: it created hello.txt with the requested line, see a file."
			last := got[len(got)-1]
			if last.Text != want || last.Urgency != voice.UrgencyIdle {
				t.Errorf("promise push = %+v", last)
			}
			// One line per finish: the template sentence is never spoken
			// next to the promise line. Only the promise line is spoken at
			// every level ("started on" is chatty, and silent there: the
			// instruction was just sent).
			texts := h.pushTexts()
			if len(texts) != 1 || slices.Contains(texts, doneSentence) {
				t.Errorf("%s pushes = %v", level, texts)
			}
			if n := h.log.count("supervisor.push_quiet"); n != map[Chattiness]int{Quiet: 1, Normal: 1, Chatty: 0}[level] { // "started on"
				t.Errorf("push_quiet = %d", n)
			}
			kept := h.log.ofType("promise.kept")
			if len(kept) != 1 || payload(kept[0])["id"] != p.ID || payload(kept[0])["via"] != "brain" || payload(kept[0])["urgency"] != "idle" || payload(kept[0])["chattiness"] != string(level) || payload(kept[0])["spoken"] != want {
				t.Errorf("promise.kept = %v", kept)
			}
			d := h.log.ofType("brain.push_decision")
			if len(d) != 1 || payload(d[0])["urgency_requested"] != "idle" || payload(d[0])["urgency_applied"] != "idle" || payload(d[0])["reason"] != "brain" || payload(d[0])["ceiling"] != string(level) || payload(d[0])["spoken"] != want {
				t.Errorf("push_decision = %v", d)
			}
			// The promise was already kept when the orchestrator saw it, with
			// its id in the request.
			calls := m.Calls()
			if text := calls[len(calls)-1].Text(); !strings.Contains(text, `Promise (kept): "let me know when it's done", when auth finishes`) || !strings.Contains(text, "- "+p.ID+`: "let me know when it's done" (when auth finishes)`) {
				t.Errorf("announce request:\n%s", text)
			}
			if open := h.sup.openPromises(); len(open) != 0 {
				t.Errorf("still open: %+v", open)
			}
			all := h.store.Snapshot().Session.Promises
			if len(all) != 1 || all[0].Status != state.PromiseKept || all[0].ResolvedAt == nil {
				t.Errorf("record = %+v", all)
			}
			// brain.turn kinds: the utterance, then one announce for the finish.
			var kinds []string
			for _, ev := range h.log.ofType("brain.turn") {
				kinds = append(kinds, payload(ev)["kind"].(string))
			}
			if strings.Join(kinds, " ") != "utterance announce" {
				t.Errorf("brain.turn kinds = %v", kinds)
			}
			// The transcript has the kept record after the made one.
			var statuses []string
			for _, m := range h.sup.Brain().Transcript().Messages() {
				if m.Kind == brain.KindPromise {
					statuses = append(statuses, m.Promise.Status)
				}
			}
			if strings.Join(statuses, " ") != "open kept" {
				t.Errorf("promise messages = %v", statuses)
			}
		})
	}
}

// Without an orchestrator (or when its announce turn fails) the template
// sentence is spoken with "As you asked:", still at every level.
func TestPromiseKeptByTemplateWithoutBrain(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+doneSentence {
		t.Errorf("pushes = %v", got)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 1 || payload(kept[0])["via"] != "template" {
		t.Errorf("promise.kept = %v", kept)
	}
	// A orchestrator that falls back on the announce turn: same line.
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Err: braintest.ErrBoom}
	h = newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+doneSentence {
		t.Errorf("pushes with a failing brain = %v", got)
	}
}

// A promise past its due time interrupts; one for a block is kept on the
// question; any_change is kept on the first change; a promise for
// another agent is left alone. working -> idle is a finish (herdr
// reports idle instead of done when the pane was focused), so it keeps
// a "done" promise for that agent too, in the same line.
func TestPromiseMatchingAndUrgency(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	due := t0.Add(-time.Second)
	mk := func(pane, text, event string, due *time.Time) state.Promise {
		p, err := h.sup.createPromise(pane, brain.PromiseRequest{Text: text, Event: event, Due: due})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	urgent := mk("w5:p1", "interrupt me when it's done", "done", &due)
	blocked := mk("w5:p1", "tell me if it asks", "blocked", nil)
	any := mk("w6:p1", "tell me if anything happens with docs", "any_change", nil)
	other := mk("w6:p1", "tell me when docs is done", "done", nil)

	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.pusher.all()
	// One line for the block: the kept promise carries the question.
	if len(got) != 1 || got[0].Text != `As you asked: auth asks to run printf.` || got[0].Urgency != voice.UrgencyIdle {
		t.Fatalf("block pushes = %+v", got)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	doneAuth(h)
	got = h.pusher.all()
	last := got[len(got)-1]
	if last.Text != "As you asked: "+doneSentence || last.Urgency != voice.UrgencyInterrupt {
		t.Errorf("due promise push = %+v", last)
	}
	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)
	got = h.pusher.all()
	if last := got[len(got)-1]; last.Text != "As you asked: docs finished." {
		t.Errorf("finish push = %+v", last)
	}
	status := map[string]string{}
	for _, p := range h.store.Snapshot().Session.Promises {
		status[p.ID] = p.Status
	}
	if status[urgent.ID] != state.PromiseKept || status[blocked.ID] != state.PromiseKept || status[any.ID] != state.PromiseKept || status[other.ID] != state.PromiseKept {
		t.Errorf("statuses = %v", status)
	}
	if h.log.count("promise.kept") != 4 {
		t.Errorf("promise.kept = %d", h.log.count("promise.kept"))
	}
}

// A finish after the user's own "stop" is not a kept promise.
func TestPromiseNotKeptOnInterruptedFinish(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.send("w5:p1", "run the tests")
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	h.say("stop")
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-ctrl-c.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.finished_suppressed") != 1 || h.log.count("promise.kept") != 0 || len(h.sup.openPromises()) != 1 {
		t.Errorf("suppressed=%d kept=%d open=%d", h.log.count("supervisor.finished_suppressed"), h.log.count("promise.kept"), len(h.sup.openPromises()))
	}
}

// Open promises survive a restart (state file), are re-ingested into the
// new run's transcript, and are kept as usual.
func TestPromiseSurvivesRestart(t *testing.T) {
	h := newBrainHarness(t, promiseModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)

	// A new supervisor and orchestrator over the same state file.
	store, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{Aliases: map[string]string{"w5:p1": "auth"}})
	if err != nil {
		t.Fatal(err)
	}
	m := promiseModel()
	log := &recLog{}
	b, err := brain.New(brain.Config{DataDir: t.TempDir(), RunID: "run2"}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	sup, err := New(store, h.client, nil, h.pusher, log, Config{Now: h.clock.Now, Brain: b, Chattiness: string(Quiet)})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	if open := sup.openPromises(); len(open) != 1 || open[0].ID != p.ID {
		t.Fatalf("open after restart = %+v", open)
	}
	msgs := b.Transcript().Messages()
	if len(msgs) != 1 || msgs[0].Kind != brain.KindPromise || msgs[0].Promise.ID != p.ID || msgs[0].Promise.Status != "open" {
		t.Errorf("restored transcript = %+v", msgs)
	}
	// The roster mentions it; the finish keeps it.
	if text, _ := sup.briefingText(); !strings.HasSuffix(text, " You asked me to tell you when auth finishes.") {
		t.Errorf("roster = %q", text)
	}
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now()})
	if last := h.pusher.last(t); !strings.HasPrefix(last.Text, "You asked me to tell you when auth was done") {
		t.Errorf("push after restart = %+v", last)
	}
	if got := store.Snapshot().Session.Promises; len(got) != 1 || got[0].Status != state.PromiseKept {
		t.Errorf("record = %+v", got)
	}
	if text, _ := sup.briefingText(); strings.Contains(text, "You asked me to tell you when auth finishes.") {
		t.Errorf("the roster still mentions the kept promise: %q", text)
	}
}

// A promise expires after PromiseTTL: logged, no speech, out of the
// briefing.
func TestPromiseExpires(t *testing.T) {
	h := newBrainHarness(t, promiseModel(), func(c *Config) { c.PromiseTTL = 2 * time.Hour })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	h.clock.Advance(119 * time.Minute)
	h.sup.ExpirePromises()
	if len(h.sup.openPromises()) != 1 {
		t.Fatal("expired early")
	}
	h.clock.Advance(2 * time.Minute)
	before := len(h.pusher.all())
	h.sup.ExpirePromises()
	if len(h.sup.openPromises()) != 0 || len(h.pusher.all()) != before {
		t.Errorf("open=%d pushes=%d", len(h.sup.openPromises()), len(h.pusher.all())-before)
	}
	ev := h.log.ofType("promise.expired")
	if len(ev) != 1 || payload(ev[0])["id"] != p.ID || payload(ev[0])["age_s"] != 121*60 {
		t.Errorf("promise.expired = %v", ev)
	}
	// The finish now keeps nothing.
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	doneAuth(h)
	if h.log.count("promise.kept") != 0 {
		t.Error("an expired promise was kept")
	}
	if text := h.roster(); strings.Contains(text, "You asked me") {
		t.Errorf("roster = %q", text)
	}
}

// "Stop" right after the combined utterance interrupts the instruction
// it sent, and the promise stays: stop withdraws work, not a reminder
// (the old cancel tool's "Cancelled the reminder" is gone with it).
func TestPromiseCancel(t *testing.T) {
	h := newBrainHarness(t, promiseModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	h.say("stop")
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "ctrl+c"} {
		t.Errorf("send_keys = %v", keys)
	}
	if open := h.sup.openPromises(); len(open) != 1 || open[0].ID != p.ID {
		t.Fatalf("stop with a pending instruction touched the promise: %+v", open)
	}
	// A second stop has nothing in flight and still leaves the promise.
	h.say("stop")
	if len(h.sup.openPromises()) != 1 || h.log.count("promise.cancelled") != 0 {
		t.Errorf("open=%d cancelled=%d", len(h.sup.openPromises()), h.log.count("promise.cancelled"))
	}
}

// The briefing lists open promises (cache only), and recall finds them.
func TestPromiseInBriefingAndRecall(t *testing.T) {
	m := promiseModel()
	m.Add(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRecall, map[string]any{"query": "let me know"})}},
		braintest.Step{Text: braintest.Reply("You asked to be told when auth finishes.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	combined(h)
	if _, err := h.sup.createPromise("w6:p1", brain.PromiseRequest{Text: "tell me if docs asks", Event: "blocked"}); err != nil {
		t.Fatal(err)
	}
	if text := h.roster(); !strings.HasSuffix(text, " You asked me to tell you when auth finishes and when docs asks something.") {
		t.Errorf("roster = %q", text)
	}
	res := h.say("did I ask you to tell me anything?")
	if res.Spoken != "You asked to be told when auth finishes." {
		t.Errorf("reply = %+v", res)
	}
	calls := m.Calls()
	if out := calls[len(calls)-1].Text(); !strings.Contains(out, `Promise (open): "let me know when it's done", when auth finishes`) {
		t.Errorf("recall result:\n%s", out)
	}
}

// The reflex model (--brain=fake) records a promise from the words with
// no agent named and a single agent running: the whole path the demo
// exercises.
func TestPromiseWithReflexModel(t *testing.T) {
	h := newBrainHarness(t, braintest.Reflex())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	res := h.say("run the tests and let me know when it's done")
	if res.Spoken != "Telling auth to run the tests. I will tell you when auth finishes." {
		t.Errorf("reply = %+v", res)
	}
	if len(h.sup.openPromises()) != 1 || len(h.promptRequests()) != 1 {
		t.Fatalf("open=%d prompts=%v", len(h.sup.openPromises()), h.promptRequests())
	}
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	doneAuth(h)
	// One line for the finish: the reflex keeps the promise with it.
	if texts := h.pushTexts(); len(texts) != 1 || texts[0] != "As you asked: "+doneSentence {
		t.Errorf("pushes = %v", texts)
	}
	if kept := h.log.ofType("promise.kept"); len(kept) != 1 || payload(kept[0])["via"] != "brain" {
		t.Errorf("promise.kept = %v", kept)
	}
	// A bare promise with no instruction in it is recorded alone.
	res = h.say("tell me when auth is done")
	if res.Spoken != "I will tell you when auth finishes." || len(h.promptRequests()) != 1 {
		t.Errorf("bare promise = %+v (prompts %v)", res, h.promptRequests())
	}
}

// restartOver builds a second Supervisor over h's own state file, as if
// the daemon had just restarted: the same herdr client and fake server
// (so a pane h already registered still reads), a fresh log and pusher.
// cfg.Now is always h.clock.Now; set the rest (Brain, Chattiness, …) as
// a test needs.
func restartOver(t *testing.T, h *harness, cfg Config) (*Supervisor, *recLog, *fakePusher) {
	t.Helper()
	store, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{Aliases: map[string]string{"w5:p1": "auth", "w6:p1": "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	log := &recLog{}
	pusher := &fakePusher{}
	cfg.Now = h.clock.Now
	sup, err := New(store, h.client, nil, pusher, log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sup, log, pusher
}

// TestStartupPromisesAnnounced covers announceStartupPromises (the
// "Smaller items" of docs/history/issues-2026-09-18.md): 0 old promises say
// nothing; 1 or 4 are spoken once as "Still watching for: …" (up to
// three named, the rest tallied), pushed idle and tagged
// startup_promises, logged as promise.carried_over, flagged
// carried_over in the state file, and never announced again on a second
// start over the same file.
func TestStartupPromisesAnnounced(t *testing.T) {
	for _, n := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("%d old promises", n), func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
			for i := 0; i < n; i++ {
				// One per event: a second open promise for the same event
				// is a duplicate (TestDuplicatePromiseIsAFact).
				if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: fmt.Sprintf("thing %d is done", i), Event: brain.PromiseEvents[i]}); err != nil {
					t.Fatal(err)
				}
			}
			h.clock.Advance(time.Hour) // the next daemon run starts later

			sup, log, pusher := restartOver(t, h, Config{})
			sup.SetVoiceConnected(true)

			carried := log.ofType("promise.carried_over")
			pushes := pusher.all()
			if n == 0 {
				if len(carried) != 0 || len(pushes) != 0 {
					t.Fatalf("0 promises: carried=%v pushes=%v", carried, pushes)
				}
				return
			}
			if len(carried) != 1 {
				t.Fatalf("promise.carried_over = %d, want 1", len(carried))
			}
			ids, _ := payload(carried[0])["ids"].([]string)
			if len(ids) != n {
				t.Errorf("carried ids = %v, want %d", ids, n)
			}
			if len(pushes) != 1 || pushes[0].Urgency != voice.UrgencyIdle {
				t.Fatalf("pushes = %+v", pushes)
			}
			if !strings.HasPrefix(pushes[0].Text, "Still watching for: ") {
				t.Errorf("text = %q", pushes[0].Text)
			}
			if tags := pusher.tagsOf(0); tags["startup_promises"] != true {
				t.Errorf("tags = %v", tags)
			}
			named := n
			if named > briefingListMax {
				named = briefingListMax
			}
			for i := 0; i < named; i++ {
				if !strings.Contains(pushes[0].Text, fmt.Sprintf("thing %d is done (auth)", i)) {
					t.Errorf("text missing item %d: %q", i, pushes[0].Text)
				}
			}
			if n > briefingListMax {
				want := fmt.Sprintf("and %d more.", n-briefingListMax)
				if !strings.HasSuffix(pushes[0].Text, want) {
					t.Errorf("text = %q, want suffix %q", pushes[0].Text, want)
				}
			}
			for _, p := range store2Promises(t, h) {
				if !p.CarriedOver {
					t.Errorf("promise %s not flagged carried_over", p.ID)
				}
			}

			// A second daemon over the same file never announces again.
			sup2, log2, pusher2 := restartOver(t, h, Config{})
			sup2.SetVoiceConnected(true)
			if len(pusher2.all()) != 0 || log2.count("promise.carried_over") != 0 {
				t.Errorf("re-announced on restart: pushes=%v carried=%d", pusher2.all(), log2.count("promise.carried_over"))
			}
			if open := sup2.openPromises(); len(open) != n {
				t.Errorf("open after restart = %d, want %d", len(open), n)
			}
		})
	}
}

// store2Promises reads the promises straight off h's state file (a fresh
// state.Open, so it reflects a later Supervisor's writes rather than h's
// own in-memory copy).
func store2Promises(t *testing.T, h *harness) []state.Promise {
	t.Helper()
	store, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return store.Snapshot().Session.Promises
}

// A carried-over promise is still kept normally at its matching event
// (docs/roles.md "Promises"): the startup announcement only replaces the
// briefing trailer, not the promise mechanism.
func TestStartupPromiseStillKeptAtItsEvent(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: brain.PromiseEventDone})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)

	sup, log, _ := restartOver(t, h, Config{Chattiness: string(Quiet)})
	sup.SetVoiceConnected(true)
	if n := log.count("promise.carried_over"); n != 1 {
		t.Fatalf("promise.carried_over = %d, want 1", n)
	}

	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateIdle, To: herdr.StateWorking, At: h.clock.Now(), Agent: "codex"})
	sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now(), Agent: "codex"})

	kept := log.ofType("promise.kept")
	if len(kept) != 1 || payload(kept[0])["id"] != p.ID {
		t.Errorf("promise.kept = %v", kept)
	}
	if all := store2Promises(t, h); len(all) != 1 || all[0].Status != state.PromiseKept || !all[0].CarriedOver {
		t.Errorf("record = %+v", all)
	}
}

// The carried-over promise is left out of the briefing trailer (the
// startup announcement already said it) but still listed in the orchestrator's
// ledger section, noted "(from an earlier run)".
func TestCarriedOverPromiseInLedgerNotBriefing(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "let me know when it's done", Event: brain.PromiseEventDone}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)

	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("auth is idle.", "")})
	b, err := brain.New(brain.Config{DataDir: h.dir, RunID: "run2"}, m, &recLog{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	sup, _, _ := restartOver(t, h, Config{Brain: b})
	sup.SetVoiceConnected(true)

	if text, _ := sup.briefingText(); strings.Contains(text, "let me know") {
		t.Errorf("the roster still mentions the carried-over promise: %q", text)
	}

	sup.ShepherdInput(InputTyped, "what's going on")
	if _, err := sup.Handler()(context.Background(), voice.ToolCall{ID: "c", Name: ToolToSupervisor, Args: map[string]any{"text": "what's going on"}}); err != nil {
		t.Fatal(err)
	}
	calls := m.Calls()
	sys := calls[len(calls)-1].System()
	if !strings.Contains(sys, `"let me know when it's done": when auth finishes`) || !strings.Contains(sys, "(from an earlier run)") {
		t.Errorf("ledger:\n%s", sys)
	}
}

// A promise the orchestrator repeats is not recorded twice: the tool's error is
// the fact that it is already open and in which turn it was made
// (tier-2 bench 2026-10-03, collision-finish-push: "Noted. I'll tell
// you…" confirmations counted as unprompted). A promise for another
// event, or once the first was kept, is recorded as usual.
func TestDuplicatePromiseIsAFact(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	first, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when auth is done", Event: brain.PromiseEventDone, Turn: 3})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(20 * time.Second)
	_, err = h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "let the user know when auth finishes", Event: brain.PromiseEventDone, Turn: 5})
	want := `already promised at ` + first.CreatedAt.Format("15:04:05") + `: "tell me when auth is done" is still open, nothing new was recorded; it was made in turn 3, and whether that turn's reply told the user is in your transcript`
	if err == nil || err.Error() != want {
		t.Fatalf("duplicate: err = %v\nwant %s", err, want)
	}
	if open := h.sup.openPromises(); len(open) != 1 {
		t.Errorf("open promises = %d", len(open))
	}
	if h.log.count("promise.duplicate") != 1 {
		t.Error("no promise.duplicate event")
	}
	// Repeated inside the same turn: no earlier turn to point to.
	_, err = h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when auth is done", Event: brain.PromiseEventDone, Turn: 3})
	if err == nil || strings.Contains(err.Error(), "made in turn") {
		t.Errorf("same turn: err = %v", err)
	}
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me if it asks", Event: brain.PromiseEventBlocked, Turn: 5}); err != nil {
		t.Errorf("another event: %v", err)
	}
	doneAuth(h)
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when auth is done again", Event: brain.PromiseEventDone, Turn: 6}); err != nil {
		t.Errorf("after the first was kept: %v", err)
	}
}
