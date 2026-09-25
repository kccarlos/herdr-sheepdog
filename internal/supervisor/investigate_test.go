package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// fakeInvestigator stands in for the background CLI sub-agent: it
// records every query and answers when the test lets it.
type fakeInvestigator struct {
	mu      sync.Mutex
	queries []InvestigationQuery
	answer  string
	session string
	err     error
	// gate, when set, holds every run until it is closed (or the job's
	// context ends, which is what a cancel or the ceiling does).
	gate chan struct{}
}

func (f *fakeInvestigator) Investigate(ctx context.Context, q InvestigationQuery) (InvestigationResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	gate, answer, session, err := f.gate, f.answer, f.session, f.err
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return InvestigationResult{}, ctx.Err()
		}
	}
	if err != nil {
		return InvestigationResult{}, err
	}
	return InvestigationResult{Backend: "codex", Answer: answer, SessionID: session}, nil
}

func (f *fakeInvestigator) all() []InvestigationQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InvestigationQuery(nil), f.queries...)
}

// investigateAnswer is the one-line reply the delivery turn composes in
// these tests.
const investigateAnswer = "The clamp test asserts the wrong bound, so the rerun failed the same way."

// newInvestigationHarness is a brain harness whose delivery turn answers
// with investigateAnswer and whose CLI sub-agent is fake.
func newInvestigationHarness(t *testing.T, f *fakeInvestigator, opts ...func(*Config)) *harness {
	t.Helper()
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Text: mustReply(brain.Reply{Spoken: investigateAnswer, Detail: "From the look.", Urgency: brain.UrgencyIdle})}
	h := newBrainHarness(t, m, opts...)
	h.sup.investigator = f
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	return h
}

func mustReply(r brain.Reply) string {
	b, _ := json.Marshal(r)
	return string(b)
}

// start runs the investigate tool the way the brain's Host does.
func start(t *testing.T, h *harness, question, followUpOf, asked string) brain.InvestigationStart {
	t.Helper()
	s, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: question, FollowUpOf: followUpOf, Asked: asked, Turn: 1})
	if err != nil {
		t.Fatalf("startInvestigation: %v", err)
	}
	return s
}

// TestInvestigationRunsAndDelivers is the whole path: the tool returns
// at once, the job runs in the background, and its answer comes back as
// one idle push tagged with the job.
func TestInvestigationRunsAndDelivers(t *testing.T) {
	f := &fakeInvestigator{answer: "The clamp test asserts 99, not 100.", session: "sess-1", gate: make(chan struct{})}
	h := newInvestigationHarness(t, f)

	job := start(t, h, "why did the tests fail?", "", "explain why the tests failed")
	if job.Job == "" || job.ETASeconds != 30 {
		t.Fatalf("start = %+v (want a job id and the 30 s default estimate)", job)
	}
	started := h.log.ofType("investigation.started")
	if len(started) != 1 || payload(started[0])["id"] != job.Job || payload(started[0])["source"] != state.InvestigationSourceCLI {
		t.Fatalf("investigation.started = %v", started)
	}
	if inv, ok := h.sup.investigation(job.Job); !ok || inv.Status != state.InvestigationRunning || inv.Asked != "explain why the tests failed" {
		t.Fatalf("record while running = %+v (ok %t)", inv, ok)
	}
	if len(h.pusher.all()) != 0 {
		t.Fatalf("pushed before the job finished: %v", h.pusher.all())
	}

	h.clock.Advance(3 * time.Second)
	close(f.gate)
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the answer to be pushed")

	push := h.pusher.last(t)
	if push.Text != investigateAnswer || push.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v (want the brain's paragraph at idle)", push)
	}
	if tags := h.pusher.tagsOf(0); tags == nil || tags["answers_job"] != job.Job {
		t.Errorf("push tags = %v, want answers_job %s", tags, job.Job)
	}
	done := h.log.ofType("investigation.done")
	if len(done) != 1 {
		t.Fatalf("investigation.done = %v", done)
	}
	p := payload(done[0])
	if p["id"] != job.Job || p["session_id_present"] != true || p["dur_ms"] != int64(3000) || p["bytes"] != len(f.answer) {
		t.Errorf("investigation.done payload = %v", p)
	}
	inv, ok := h.sup.investigation(job.Job)
	if !ok || inv.Status != state.InvestigationDone || inv.SessionID != "sess-1" || inv.Result != f.answer || inv.Summary != f.answer || inv.Backend != "codex" {
		t.Fatalf("record after the job = %+v", inv)
	}
	// The delivery turn is an event turn about the finished job, with the
	// answer wrapped as worker output.
	turns := h.log.ofType("brain.turn")
	if len(turns) != 1 || payload(turns[0])["kind"] != string(brain.KindInvestigation) || payload(turns[0])["trigger"] != string(brain.TriggerEvent) {
		t.Fatalf("brain.turn = %v", turns)
	}
	if q := f.all()[0]; q.Session != "" || q.Slot != "investigation:w5:p1" || q.Timeout != h.sup.cfg.InvestigateMax || q.Workspace != "/work/w5:p1" {
		t.Errorf("query = %+v", q)
	}
}

// TestInvestigationOnePerAgent: a newer question about the same agent
// replaces the running look.
func TestInvestigationOnePerAgent(t *testing.T) {
	f := &fakeInvestigator{answer: "later", gate: make(chan struct{})}
	h := newInvestigationHarness(t, f)

	first := start(t, h, "why did the tests fail?", "", "why did it fail")
	second := start(t, h, "what is it waiting for?", "", "what is it waiting for")

	cancelled := h.log.ofType("investigation.cancelled")
	if len(cancelled) != 1 || payload(cancelled[0])["id"] != first.Job || payload(cancelled[0])["reason"] != "superseded" {
		t.Fatalf("investigation.cancelled = %v", cancelled)
	}
	if inv, _ := h.sup.investigation(first.Job); inv.Status != state.InvestigationCancelled {
		t.Errorf("first record = %+v", inv)
	}
	if live, ok := h.sup.runningInvestigation("w5:p1"); !ok || live.id != second.Job {
		t.Errorf("running job = %+v (ok %t)", live, ok)
	}
	close(f.gate)
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "one answer for the second look only")
}

// TestInvestigationErrorAndCeiling: a failing sub-agent is reported and
// the daemon says so; the ceiling kills a job that never returns.
func TestInvestigationErrorAndCeiling(t *testing.T) {
	f := &fakeInvestigator{err: context.DeadlineExceeded}
	h := newInvestigationHarness(t, f)
	job := start(t, h, "why did the tests fail?", "", "why")
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the failure to be pushed")
	if got := h.pusher.last(t); got.Text != "The look into auth did not get through; ask me to try again." || got.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", got)
	}
	if errs := h.log.ofType("investigation.error"); len(errs) != 1 || payload(errs[0])["id"] != job.Job {
		t.Errorf("investigation.error = %v", errs)
	}
	if inv, _ := h.sup.investigation(job.Job); inv.Status != state.InvestigationError || inv.Err == "" {
		t.Errorf("record = %+v", inv)
	}

	// The ceiling is the only time limit: a job that never answers is
	// killed by its own context, not by cli.timeout_s.
	slow := &fakeInvestigator{gate: make(chan struct{})} // never closed
	h2 := newInvestigationHarness(t, slow, func(c *Config) { c.InvestigateMax = 20 * time.Millisecond })
	job2 := start(t, h2, "why?", "", "why")
	waitFor(t, func() bool {
		inv, ok := h2.sup.investigation(job2.Job)
		return ok && inv.Status == state.InvestigationError
	}, "the ceiling to kill the job")
	if _, running := h2.sup.runningInvestigation("w5:p1"); running {
		t.Error("the agent's slot is still claimed after the ceiling")
	}
}

// TestInvestigationFollowUpResumesSession: a follow-up within the TTL
// resumes the same sub-agent session; past it, it starts cold with the
// previous answer prepended.
func TestInvestigationFollowUpResumesSession(t *testing.T) {
	f := &fakeInvestigator{answer: "The guard stops a double write.", session: "sess-1"}
	h := newInvestigationHarness(t, f, func(c *Config) { c.InvestigateSessionTTL = time.Hour })
	first := start(t, h, "why did the tests fail?", "", "why did it fail")
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the first answer")

	h.clock.Advance(time.Minute)
	second := start(t, h, "and why does that guard exist?", first.Job, "and why does that guard exist?")
	waitFor(t, func() bool { return len(f.all()) == 2 }, "the follow-up to run")
	if q := f.all()[1]; q.Session != "sess-1" || q.Question != "and why does that guard exist?" {
		t.Errorf("follow-up query = %+v (want the same session, the question as asked)", q)
	}
	if inv, _ := h.sup.investigation(second.Job); inv.FollowUpOf != first.Job {
		t.Errorf("follow-up record = %+v", inv)
	}

	// Past the TTL the session is gone: the question carries the answer.
	waitFor(t, func() bool { return len(h.pusher.all()) == 2 }, "the follow-up answer")
	h.clock.Advance(2 * time.Hour)
	start(t, h, "and what about the other one?", first.Job, "and what about the other one?")
	waitFor(t, func() bool { return len(f.all()) == 3 }, "the cold follow-up to run")
	waitFor(t, func() bool { return len(h.pusher.all()) == 3 }, "the cold follow-up answer")
	q := f.all()[2]
	if q.Session != "" || !strings.Contains(q.Question, "The clamp test") && !strings.Contains(q.Question, "You answered") {
		t.Errorf("cold follow-up = %+v (want no session and the previous answer in the question)", q)
	}

	// A follow-up to a job that is still running is refused, not queued.
	slow := &fakeInvestigator{gate: make(chan struct{})}
	h.sup.investigator = slow
	running := start(t, h, "why is it slow?", "", "why is it slow")
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "and why?", FollowUpOf: running.Job}); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("follow-up of a running job = %v", err)
	}
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "and why?", FollowUpOf: "inv-nope"}); err == nil {
		t.Error("follow-up of an unknown job was accepted")
	}
}

// TestShutdownWaitsForInvestigationDelivery: a job that finished just
// before the daemon stops is composing its answer (the delivery turn);
// shutdown ends that turn and waits for it, so the brain the daemon
// closes next has no turn running (the race CI caught on PR #63), and
// nothing is pushed into a voice that is going down.
func TestShutdownWaitsForInvestigationDelivery(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Block: make(chan struct{})}
	h := newBrainHarness(t, m)
	f := &fakeInvestigator{answer: "It ran out of disk.", session: "sess-1"}
	h.sup.investigator = f
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	job := start(t, h, "why did the tests fail?", "", "why")
	waitFor(t, func() bool { return len(m.Calls()) == 1 }, "the delivery turn to reach the model")

	h.sup.shutdown()
	if n := len(h.pusher.all()); n != 0 {
		t.Errorf("pushed %d lines while shutting down: %v", n, h.pusher.all())
	}
	if inv, _ := h.sup.investigation(job.Job); inv.Status != state.InvestigationDone || inv.Result != "It ran out of disk." {
		t.Errorf("record = %+v (want the answer kept in the ledger)", inv)
	}
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "and now?"}); err == nil {
		t.Error("a job started after shutdown")
	}
}

// TestInvestigationPersistsAcrossRestart: the ledger is in the state
// file, and a job the daemon died on is closed out on the next start so
// it does not claim the agent's slot for good.
func TestInvestigationPersistsAcrossRestart(t *testing.T) {
	f := &fakeInvestigator{answer: "It ran out of disk.", session: "sess-9"}
	h := newInvestigationHarness(t, f)
	job := start(t, h, "why did the tests fail?", "", "why")
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the answer")
	stuck := start(t, h, "and the other one?", "", "and the other one")
	waitFor(t, func() bool { return len(h.pusher.all()) == 2 }, "the second answer")
	// A record the daemon stopped on, as the state file would hold it.
	if err := h.store.UpdateSession(func(ss *state.Session) {
		for i := range ss.Investigations {
			if ss.Investigations[i].ID == stuck.Job {
				ss.Investigations[i].Status = state.InvestigationRunning
				ss.Investigations[i].Finished = nil
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	log := &recLog{}
	sup, err := New(store, h.client, nil, h.pusher, log, Config{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	all := sup.investigations()
	if len(all) != 2 || all[0].ID != job.Job || all[0].Result != f.answer || all[0].SessionID != "sess-9" {
		t.Fatalf("ledger after the restart = %+v", all)
	}
	if all[1].Status != state.InvestigationError || all[1].Finished == nil {
		t.Errorf("a job the daemon died on stayed running: %+v", all[1])
	}
	if errs := log.ofType("investigation.error"); len(errs) != 1 || payload(errs[0])["id"] != stuck.Job {
		t.Errorf("investigation.error on restore = %v", errs)
	}
}

// TestStopCancelsInvestigation: the stop procedure's investigation
// phase, between cancelling the turn and interrupting a worker.
func TestStopCancelsInvestigation(t *testing.T) {
	f := &fakeInvestigator{gate: make(chan struct{})}
	h := newInvestigationHarness(t, f)
	job := start(t, h, "why did the tests fail?", "", "explain why auth failed")

	res := h.say("stop")

	cancelled := h.log.ofType("investigation.cancelled")
	if len(cancelled) != 1 || payload(cancelled[0])["id"] != job.Job || payload(cancelled[0])["reason"] != "stop" {
		t.Fatalf("investigation.cancelled = %v", cancelled)
	}
	stopped := h.log.ofType("supervisor.stopped")
	if len(stopped) != 1 || payload(stopped[0])["phase"] != stopPhaseInvestigation {
		t.Fatalf("supervisor.stopped = %v", stopped)
	}
	if what, _ := payload(stopped[0])["what"].(string); what != "the look into auth was called off" {
		t.Errorf("supervisor.stopped what = %q", what)
	}
	if res.Spoken == "" {
		t.Error("the stop turn said nothing")
	}
	if inv, _ := h.sup.investigation(job.Job); inv.Status != state.InvestigationCancelled {
		t.Errorf("record = %+v", inv)
	}
	// The cancelled job delivers nothing, even when its sub-agent
	// answers afterwards.
	close(f.gate)
	if n := len(h.pusher.all()); n != 0 {
		t.Errorf("a cancelled look still pushed %d lines", n)
	}
}

// TestInvestigationWithoutBrain: with the brain off the daemon still
// says the look is done and keeps the answer in the ledger.
func TestInvestigationWithoutBrain(t *testing.T) {
	f := &fakeInvestigator{answer: "It ran out of disk."}
	h := newHarness(t)
	h.sup.investigator = f
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)

	job := start(t, h, "why did the tests fail?", "", "why")
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the code sentence")
	if got := h.pusher.last(t); got.Text != "The look into auth is done; ask me what it found." || got.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", got)
	}
	if tags := h.pusher.tagsOf(0); tags == nil || tags["answers_job"] != job.Job {
		t.Errorf("push tags = %v", tags)
	}
	if inv, _ := h.sup.investigation(job.Job); inv.Result != f.answer {
		t.Errorf("the raw answer was not kept: %+v", inv)
	}
}

// TestInvestigationNeedsAQueryTool: with no CLI tool the brain is told
// so rather than left waiting.
func TestInvestigationNeedsAQueryTool(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "why?"}); err != brain.ErrNoCLI {
		t.Errorf("err = %v, want ErrNoCLI", err)
	}
}
