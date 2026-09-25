package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// Session ids as herdr reports them: both backends use UUIDs.
const (
	workerClaudeSession = "366f482c-d489-4dda-9ba6-52d26f9069b5"
	workerCodexSession  = "01a0b1cb-a237-7722-8eea-8db9f1dd95b6"
	forkSession         = "218f5ac7-2267-4c4b-a809-b20ff8a53f3f"
)

// checkingInvestigator is a fakeInvestigator that also says which
// backends were detected (the daemon's lazyQuerier does).
type checkingInvestigator struct {
	*fakeInvestigator
	has map[string]bool
}

func (c checkingInvestigator) HasBackend(backend string) bool { return c.has[backend] }

// newWorkerInvestigationHarness is an investigation harness whose
// worker log reads a temp directory, with pane w5:p1 ("auth") a Claude
// worker whose session is on disk.
func newWorkerInvestigationHarness(t *testing.T, f *fakeInvestigator, opts ...func(*Config)) (*harness, string) {
	t.Helper()
	dir := t.TempDir()
	h := newInvestigationHarness(t, f, append([]func(*Config){withWorkerLog(dir)}, opts...)...)
	setWorkerSession(t, h, "claude", "herdr:claude", herdr.SessionKindID, workerClaudeSession)
	writeFile(t, filepath.Join(dir, "projects", "-work-w5-p1", workerClaudeSession+".jsonl"))
	return h, dir
}

func setWorkerSession(t *testing.T, h *harness, kind, source, sessKind, value string) {
	t.Helper()
	if _, err := h.store.Update("w5:p1", func(a *state.Agent) {
		a.Kind, a.SessionSource, a.SessionKind, a.SessionValue = kind, source, sessKind, value
	}); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startWorker(h *harness, question, followUpOf string) (brain.InvestigationStart, error) {
	return h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: question, FollowUpOf: followUpOf, Source: brain.InvestigationSourceWorker, Asked: question, Turn: 1})
}

// source worker forks the worker's own session on its own backend: the
// query carries the worker's session with Fork set, the record never
// holds the worker's id, and once the run reports the fork's id that is
// the job's session (docs/dive-deep.md §3, docs/cli-query-tools.md §2c).
func TestWorkerInvestigationForksTheWorkersSession(t *testing.T) {
	f := &fakeInvestigator{answer: "I skipped the migration because the schema was frozen.", session: forkSession, backend: "claude", gate: make(chan struct{})}
	h, _ := newWorkerInvestigationHarness(t, f)

	job, err := startWorker(h, "why did you skip the migration?", "")
	if err != nil {
		t.Fatalf("startInvestigation: %v", err)
	}
	started := h.log.ofType("investigation.started")
	if len(started) != 1 {
		t.Fatalf("investigation.started = %v", started)
	}
	if p := payload(started[0]); p["source"] != state.InvestigationSourceWorker || p["resumed"] != true || p["forked"] != true || p["backend"] != "claude" {
		t.Errorf("investigation.started = %v (want source worker, resumed, forked, on claude)", p)
	}
	waitFor(t, func() bool { return len(f.all()) == 1 }, "the fork to run")
	q := f.all()[0]
	if !q.Fork || q.Session != workerClaudeSession || q.Backend != "claude" || q.Question != "why did you skip the migration?" || q.Slot != "investigation:w5:p1" || q.Timeout != h.sup.cfg.InvestigateMax {
		t.Errorf("query = %+v (want a fork of the worker's session on claude)", q)
	}
	if inv, _ := h.sup.investigation(job.Job); inv.SessionID != "" || inv.Source != state.InvestigationSourceWorker {
		t.Errorf("record while running = %+v (the worker's session must not be the job's)", inv)
	}

	close(f.gate)
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the answer to be pushed")
	inv, _ := h.sup.investigation(job.Job)
	if inv.Status != state.InvestigationDone || inv.SessionID != forkSession || inv.Backend != "claude" {
		t.Fatalf("record after the job = %+v (want the fork's own session)", inv)
	}
}

// A follow-up of a worker job resumes the fork, not the worker: plain
// resume of the fork's id on the fork's backend. Past the session TTL it
// forks the worker again, with the earlier answer in the question.
func TestWorkerInvestigationFollowUpResumesTheFork(t *testing.T) {
	f := &fakeInvestigator{answer: "The schema was frozen for the release.", session: forkSession, backend: "claude"}
	h, _ := newWorkerInvestigationHarness(t, f, func(c *Config) { c.InvestigateSessionTTL = time.Hour })
	first, err := startWorker(h, "why did you skip the migration?", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the first answer")

	// The follow-up names no source: it continues the worker job.
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "who froze it?", FollowUpOf: first.Job, Turn: 2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 2 }, "the follow-up answer")
	q := f.all()[1]
	if q.Fork || q.Session != forkSession || q.Backend != "claude" || q.Question != "who froze it?" {
		t.Errorf("follow-up query = %+v (want a resume of the fork on claude)", q)
	}

	h.clock.Advance(2 * time.Hour)
	if _, err := startWorker(h, "and when does it thaw?", first.Job); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 3 }, "the cold follow-up answer")
	q = f.all()[2]
	if !q.Fork || q.Session != workerClaudeSession || !strings.Contains(q.Question, "You answered") {
		t.Errorf("expired follow-up = %+v (want a new fork of the worker carrying the earlier answer)", q)
	}
}

// A fork that reported no session leaves the job with none: the worker's
// id never becomes the job's, so a follow-up forks the worker again
// instead of resuming (and writing into) its session.
func TestWorkerInvestigationWithoutForkSessionKeepsNone(t *testing.T) {
	f := &fakeInvestigator{answer: "Because the schema was frozen.", backend: "claude"}
	h, _ := newWorkerInvestigationHarness(t, f, func(c *Config) { c.InvestigateSessionTTL = time.Hour })
	first, err := startWorker(h, "why did you skip the migration?", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the answer")
	if inv, _ := h.sup.investigation(first.Job); inv.SessionID != "" {
		t.Fatalf("record = %+v (want no session)", inv)
	}
	if _, err := startWorker(h, "who froze it?", first.Job); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.all()) == 2 }, "the follow-up")
	if q := f.all()[1]; !q.Fork || q.Session != workerClaudeSession {
		t.Errorf("follow-up = %+v (want a fork of the worker, never a resume of it)", q)
	}
}

// A Codex worker reported by transcript path: the thread id comes from
// the rollout's name and the fork runs on codex.
func TestWorkerInvestigationCodexByPath(t *testing.T) {
	f := &fakeInvestigator{answer: "It tried the fixture first.", session: forkSession}
	h, dir := newWorkerInvestigationHarness(t, f)
	path := filepath.Join(dir, "sessions", "2026", "09", "25", "rollout-2026-09-25T01-20-07-"+workerCodexSession+".jsonl")
	writeFile(t, path)
	setWorkerSession(t, h, "codex", "herdr:codex", herdr.SessionKindPath, path)
	if _, err := startWorker(h, "what did you try first?", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.all()) == 1 }, "the fork to run")
	if q := f.all()[0]; !q.Fork || q.Backend != "codex" || q.Session != workerCodexSession {
		t.Errorf("query = %+v (want a codex fork of the rollout's thread)", q)
	}
}

// Asking the worker itself is refused, with a reason the model reads and
// a pointer to the default source, when there is no session sheepdog can
// fork: none reported, a kind that keeps none (agy), a file that is not
// there, an id that is not one, or no CLI for that backend. Nothing runs
// and no job is recorded.
func TestWorkerInvestigationRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, h *harness, dir string)
		has     map[string]bool
		wantErr string
	}{
		{"not reported", func(t *testing.T, h *harness, _ string) {
			setWorkerSession(t, h, "claude", "", "", "")
		}, nil, workerlog.ReasonNotReported},
		{"agy", func(t *testing.T, h *harness, _ string) {
			setWorkerSession(t, h, "agy", "herdr:antigravity_cli", herdr.SessionKindID, "70f9969a-0000-4000-8000-000000000000")
		}, nil, workerlog.ReasonUnsupported},
		{"file missing", func(t *testing.T, h *harness, _ string) {
			setWorkerSession(t, h, "codex", "herdr:codex", herdr.SessionKindID, workerCodexSession)
		}, nil, workerlog.ReasonNotFound},
		{"not a uuid", func(t *testing.T, h *harness, dir string) {
			path := filepath.Join(dir, "transcript.jsonl")
			writeFile(t, path)
			setWorkerSession(t, h, "claude", "herdr:claude", herdr.SessionKindPath, path)
		}, nil, "not one the claude CLI can resume"},
		{"backend missing", func(*testing.T, *harness, string) {}, map[string]bool{"codex": true}, "claude CLI that keeps its session is not installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInvestigator{answer: "x"}
			h, dir := newWorkerInvestigationHarness(t, f)
			if tc.has != nil {
				h.sup.investigator = checkingInvestigator{f, tc.has}
			}
			tc.setup(t, h, dir)
			_, err := startWorker(h, "why?", "")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "investigate without source") {
				t.Fatalf("err = %v, want %q and a pointer to the default source", err, tc.wantErr)
			}
			if len(h.sup.investigations()) != 0 || len(f.all()) != 0 {
				t.Errorf("a refused job was recorded or run: %v", h.sup.investigations())
			}
		})
	}
}

// An unknown source is refused, and a follow-up cannot switch source:
// it continues the earlier job's conversation.
func TestInvestigationSourceValidation(t *testing.T) {
	f := &fakeInvestigator{answer: "The clamp test asserts 99.", session: "sess-1"}
	h, _ := newWorkerInvestigationHarness(t, f, func(c *Config) { c.InvestigateSessionTTL = time.Hour })
	if _, err := h.sup.startInvestigation(context.Background(), "w5:p1", brain.InvestigationRequest{Question: "q", Source: "pane"}); err == nil || !strings.Contains(err.Error(), "want cli") {
		t.Errorf("unknown source: err = %v", err)
	}
	first := start(t, h, "why did the tests fail?", "", "why")
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "the answer")
	if _, err := startWorker(h, "and why did you write it so?", first.Job); err == nil || !strings.Contains(err.Error(), "leave out follow_up_of") {
		t.Errorf("switching source on a follow-up: err = %v", err)
	}
	if n := len(f.all()); n != 1 {
		t.Errorf("runs = %d, want only the first", n)
	}
}
