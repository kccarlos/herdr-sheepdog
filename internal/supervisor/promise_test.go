package supervisor

import (
	"context"
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

// promiseModel scripts one CEO turn that sends an instruction and
// records a promise (the combined "run the tests and let me know when
// it's done"), and answers announce turns that keep a promise with one
// line (idle, the promise's id) and the others with nothing to add.
func promiseModel() *braintest.Model {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "run the tests"}),
			braintest.FC(brain.ToolPromise, map[string]any{"agent": "auth", "text": "let me know when it's done", "event": "done"}),
		}},
		braintest.Step{Text: braintest.Reply("Telling auth to run the tests. Sent; I will tell you when auth finishes.", "")},
	)
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		if strings.Contains(call.Text(), "Promises this event keeps") {
			return braintest.Response(braintest.Step{Text: `{"spoken": "You asked me to tell you when auth was done: it created hello.txt with the requested line, see /tmp/out.", "urgency": "idle", "keeps_promise_ids": ["pr-x"]}`}), nil
		}
		return braintest.Response(*braintest.NothingToAdd()), nil
	}}
	return m
}

// combined runs the combined utterance through propose and checks the
// promise was recorded.
func combined(h *harness) state.Promise {
	h.t.Helper()
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "run the tests and let me know when it's done", Final: true})
	res := h.call(ToolPropose, map[string]any{"instruction": "run the tests and let me know when it's done"})
	if res.Spoken != "Telling auth to run the tests. Sent; I will tell you when auth finishes." {
		h.t.Fatalf("propose = %+v", res)
	}
	open := h.sup.openPromises()
	if len(open) != 1 {
		h.t.Fatalf("open promises = %+v", open)
	}
	return open[0]
}

// A CEO turn records the promise through the promise tool: persisted in
// the state file, logged, in the brain's transcript, and the instruction
// went in the same turn.
func TestPromiseCreatedFromCEOTurn(t *testing.T) {
	h := newBrainHarness(t, promiseModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	if p.PaneID != "w5:p1" || p.Alias != "auth" || p.Text != "let me know when it's done" || p.Event != "done" || p.Status != state.PromiseOpen || p.CreatedTurn != 1 || !p.CreatedAt.Equal(t0) {
		t.Errorf("promise = %+v", p)
	}
	if got := h.promptRequests(); len(got) != 1 || got[0][1] != "run the tests" {
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
	if got := strings.Join(kinds, " "); got != "worker_event ceo_utterance request brain_tool_call brain_tool_result brain_tool_call brain_tool_result promise reply" {
		t.Errorf("transcript = %s", got)
	}
	// The ledger of the next turn lists it.
	if !strings.Contains(brainHost{h.sup}.Promises()[0].Text, "let me know") {
		t.Error("host does not list the promise")
	}
}

// The promise is kept at the finish whatever the chattiness level: the
// brain's announce line (D9-scrubbed) is the one line spoken for the
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
			// next to the promise line. quiet/normal: only the promise line
			// ("started on" is chatty); chatty: "started on", then it.
			texts := h.pushTexts()
			wantN := 1
			if level == Chatty {
				wantN = 2
			}
			if len(texts) != wantN || slices.Contains(texts, doneSentence) {
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
			// The promise was already kept when the brain saw it, with
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
			// brain.turn kinds: propose, then one announce for the finish.
			var kinds []string
			for _, ev := range h.log.ofType("brain.turn") {
				kinds = append(kinds, payload(ev)["kind"].(string))
			}
			if strings.Join(kinds, " ") != "propose announce" {
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

// Without a brain (or when its announce turn fails) the template
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
	// A brain that falls back on the announce turn: same line.
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
// another agent or event is left alone.
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
	if len(got) != 2 || got[1].Text != "As you asked: auth is asking whether it can run a command to create hello.txt." || got[1].Urgency != voice.UrgencyIdle {
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
	if last := got[len(got)-1]; last.Text != "As you asked: docs is idle now." {
		t.Errorf("any_change push = %+v", last)
	}
	status := map[string]string{}
	for _, p := range h.store.Snapshot().Session.Promises {
		status[p.ID] = p.Status
	}
	if status[urgent.ID] != state.PromiseKept || status[blocked.ID] != state.PromiseKept || status[any.ID] != state.PromiseKept || status[other.ID] != state.PromiseOpen {
		t.Errorf("statuses = %v", status)
	}
	if h.log.count("promise.kept") != 3 {
		t.Errorf("promise.kept = %d", h.log.count("promise.kept"))
	}
}

// A finish after the user's own "stop" is not a kept promise.
func TestPromiseNotKeptOnInterruptedFinish(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.call(ToolPropose, map[string]any{"instruction": "tell auth to run the tests"})
	if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
		t.Fatal(err)
	}
	h.call(ToolCancel, nil)
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

	// A new supervisor and brain over the same state file.
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
	sup, err := New(store, h.client, h.llm, nil, h.pusher, log, Config{Now: h.clock.Now, Brain: b, Chattiness: string(Quiet)})
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
	// Briefing mentions it; the finish keeps it.
	res, _ := sup.Briefing(context.Background())
	if !strings.HasSuffix(res.Spoken, " You asked me to tell you when auth finishes.") {
		t.Errorf("briefing = %q", res.Spoken)
	}
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now()})
	if last := h.pusher.last(t); !strings.HasPrefix(last.Text, "You asked me to tell you when auth was done") {
		t.Errorf("push after restart = %+v", last)
	}
	if got := store.Snapshot().Session.Promises; len(got) != 1 || got[0].Status != state.PromiseKept {
		t.Errorf("record = %+v", got)
	}
	res, _ = sup.Briefing(context.Background())
	if strings.Contains(res.Spoken, "You asked me") {
		t.Errorf("briefing still mentions the kept promise: %q", res.Spoken)
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
	if res, _ := h.sup.Briefing(context.Background()); strings.Contains(res.Spoken, "You asked me") {
		t.Errorf("briefing = %q", res.Spoken)
	}
}

// cancel with nothing pending cancels the latest open promise; with an
// instruction pending it interrupts that instead (and the promise stays).
func TestPromiseCancel(t *testing.T) {
	h := newBrainHarness(t, promiseModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	p := combined(h)
	// The combined utterance left a send_instruction pending: cancel
	// interrupts it, the promise stays.
	if res := h.call(ToolCancel, nil); res.Spoken != "Cancelled." {
		t.Errorf("cancel with pending = %+v", res)
	}
	if len(h.sup.openPromises()) != 1 {
		t.Fatal("cancel with a pending instruction cancelled the promise")
	}
	if res := h.call(ToolCancel, nil); res.Spoken != "Cancelled the reminder." {
		t.Errorf("cancel = %+v", res)
	}
	if len(h.sup.openPromises()) != 0 {
		t.Error("promise still open")
	}
	ev := h.log.ofType("promise.cancelled")
	if len(ev) != 1 || payload(ev[0])["id"] != p.ID {
		t.Errorf("promise.cancelled = %v", ev)
	}
	if res := h.call(ToolCancel, nil); res.Spoken != "Nothing to cancel." {
		t.Errorf("cancel again = %+v", res)
	}
	// Ingested as cancelled.
	var statuses []string
	for _, m := range h.sup.Brain().Transcript().Messages() {
		if m.Kind == brain.KindPromise {
			statuses = append(statuses, m.Promise.Status)
		}
	}
	if strings.Join(statuses, " ") != "open cancelled" {
		t.Errorf("promise messages = %v", statuses)
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
	res := h.call(ToolBriefing, nil)
	if !strings.HasSuffix(res.Spoken, " You asked me to tell you when auth finishes and when docs asks something.") {
		t.Errorf("briefing = %q", res.Spoken)
	}
	if h.llm.count("summarize_done")+h.llm.count("spoken_answer") != 0 {
		t.Error("briefing made a model call")
	}
	res = h.call(ToolAsk, map[string]any{"question": "did I ask you to tell me anything?"})
	if res.Spoken != "You asked to be told when auth finishes." {
		t.Errorf("ask = %+v", res)
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
	res := h.call(ToolPropose, map[string]any{"instruction": "run the tests and let me know when it's done"})
	if res.Spoken != "Telling auth to run the tests. Sent; I will tell you when auth finishes." {
		t.Errorf("propose = %+v", res)
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
	// A bare promise with no instruction text at all still reaches the
	// brain, which reads the CEO's words from the transcript.
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "tell me when auth is done", Final: true})
	res = h.call(ToolPropose, map[string]any{"instruction": ""})
	if res.Spoken != "I will tell you when auth finishes." || len(h.promptRequests()) != 1 {
		t.Errorf("bare promise = %+v (prompts %v)", res, h.promptRequests())
	}
}
