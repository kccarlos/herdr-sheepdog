package factory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestLaunchProbeUsesHCAndRoleCandidates(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // the daemon cannot find either CLI
	for _, tc := range []struct {
		name, cli string
		result    hc.ExecResult
		err       error
		why       string
	}{
		{name: "signed in", cli: "codex", result: hc.ExecResult{Code: new(0), Stdout: []byte("Logged in using ChatGPT")}},
		{name: "missing", cli: "claude", result: hc.ExecResult{Code: new(127)}, why: "claude is not installed"},
		{name: "rpc not found", cli: "claude", err: &hc.Error{Code: 404, Kind: "not_found", Message: "fixture RPC error"}},
		{name: "timeout", cli: "claude", result: hc.ExecResult{Code: new(127), TimedOut: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := hctest.New(t)
			var calls atomic.Int32
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				calls.Add(1)
				if !slices.Contains(p.Argv, tc.cli) || p.Env["PATH"] != "" {
					t.Errorf("machine.exec must probe %s with the session PATH", tc.cli)
				}
				return tc.result, tc.err
			}
			eng := New(testPaths(t), hc.NewClient(srv.Path))
			s := flow.DefaultSettings()
			s.Models = map[string][]flow.Candidate{flow.RoleCoordinator: {{CLI: tc.cli, Model: "m", Effort: "medium"}}}
			facts := eng.probeLaunchFacts(context.Background(), flow.RoleCoordinator, flow.Task{}, s)
			_, why := flow.Resolve(flow.RoleCoordinator, flow.Task{}, s, facts)
			cliFacts, known := facts.CLIs[tc.cli]
			switch tc.name {
			case "signed in":
				if !known || !cliFacts.Installed || !cliFacts.SignedIn {
					t.Errorf("signed-in CLI facts = %+v, known=%t", cliFacts, known)
				}
			case "missing":
				if !known || cliFacts.Installed {
					t.Errorf("missing CLI facts = %+v, known=%t", cliFacts, known)
				}
			default:
				if known {
					t.Errorf("inconclusive probe recorded facts: %+v", cliFacts)
				}
			}
			if calls.Load() != 1 || why != tc.why {
				t.Fatalf("machine.exec calls=%d, Resolve reason=%q, want 1 and %q", calls.Load(), why, tc.why)
			}
		})
	}
}

func TestLaunchProbeIdleAndTTL(t *testing.T) {
	_, eng, paths, id, _, _, _, _ := codexSetup(t)
	now := time.Now()
	eng.Now = func() time.Time { return now }
	calls := 0
	eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts {
		calls++
		return flow.ResolveFacts{}
	}
	eng.passProject(context.Background(), id, false) // N26 pushes; no launch
	if calls != 0 {
		t.Errorf("no-launch pass made %d probe calls, want 0", calls)
	}
	eng.passProject(context.Background(), id, false) // N29
	if calls != 1 {
		t.Fatalf("first reviewer launch made %d total probe calls, want 1", calls)
	}
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	f := flow.Facts{Ask: flow.AskStartReview}
	for _, tc := range []struct {
		after time.Duration
		want  int
	}{{time.Minute, 1}, {time.Minute, 2}} {
		now = now.Add(tc.after)
		// Resolve for a fresh reviewer launch, restoring the attempt before each call.
		attempt := flow.OpenAttempt(st.Tasks[0])
		attempt.Review.Run.State = flow.PhaseEnded
		state := st
		state.Tasks = slices.Clone(st.Tasks)
		state.Tasks[0].Attempts = slices.Clone(st.Tasks[0].Attempts)
		if _, _, err := eng.execStartReviewer(context.Background(), id, &state, 0, f); err != nil {
			t.Fatal(err)
		}
		if calls != tc.want {
			t.Errorf("after %v: %d probe calls, want %d", tc.after, calls, tc.want)
		}
	}
}

func TestLegacyReviewerWorkerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		worker       flow.RunRecord
		model, basis string
	}{
		{name: "attempt run", worker: flow.RunRecord{CLI: "claude", Model: "opus"}, model: "sonnet"},
		{name: "unknown", model: "haiku", basis: "Worker identity is unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, eng, paths, id, _, _, _, _ := codexSetup(t)
			st, err := LoadState(paths.State(id))
			if err != nil {
				t.Fatal(err)
			}
			task := &st.Tasks[0]
			task.Request = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "claude", Model: "opus", Effort: "low"}}}
			st.Settings.Models = map[string][]flow.Candidate{
				flow.RoleWorker:   {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
				flow.RoleReviewer: {{CLI: "claude", Model: "opus", Effort: "high"}, {CLI: "claude", Model: "sonnet", Effort: "medium"}, {CLI: "claude", Model: "haiku", Effort: "low"}},
			}
			a := flow.OpenAttempt(*task)
			a.Run.CLI, a.Run.Model, a.Rounds = tc.worker.CLI, tc.worker.Model, nil
			if got := GraphViewOf(st, nil).Tasks[0].Model; got != tc.worker.Model {
				t.Errorf("graph model = %q, want %q", got, tc.worker.Model)
			}
			decision, _, err := eng.execStartReviewer(context.Background(), id, &st, 0, flow.Facts{Ask: flow.AskStartReview})
			if err != nil || a.Review == nil {
				t.Fatalf("reviewer did not launch: %v", err)
			}
			if a.Review.Reviewer.Model != tc.model || !strings.Contains(decision, tc.basis) {
				t.Fatalf("reviewer model=%q decision=%q, want %s and %q", a.Review.Reviewer.Model, decision, tc.model, tc.basis)
			}
		})
	}
}

func TestReviewerFallbackLogsReason(t *testing.T) {
	_, eng, paths, id, _, _, _, logs := codexSetup(t)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "missing"))
	eng.passProject(context.Background(), id, false) // N26
	eng.passProject(context.Background(), id, false) // N29
	logFile, err := os.Open(paths.Log(id))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	scanner := bufio.NewScanner(logFile)
	for scanner.Scan() {
		var line LogLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line.Decision, "Repeating a reviewer") {
			if !strings.Contains(line.Decision, "Codex is not signed in") {
				t.Errorf("repeat decision %q omitted the reason", line.Decision)
			}
			if !strings.Contains(logs.String(), "factory.codex_home_error") {
				t.Error("Codex home error was not logged")
			}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("no repeat decision")
}

func TestProbeOmitsUnsupportedAndInconclusiveCLIs(t *testing.T) {
	facts := probeResolveFacts(context.Background(), []string{"agy", "codex"}, func(_ context.Context, _ string, _ []string, argv ...string) CommandOutput {
		if argv[0] == "agy" {
			t.Error("invoked unsupported agy")
		}
		return CommandOutput{Exit: 1, Stderr: "You've hit your usage limit"}
	})
	if len(facts.CLIs) != 0 {
		t.Fatalf("probe produced facts from inconclusive output: %+v", facts.CLIs)
	}
}

func TestLaunchRunRemovesFinishedSameNameTerminal(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	eng := New(testPaths(t), hc.NewClient(srv.Path))
	spec := world.RunSpec{Name: "worker-reuse", Cwd: t.TempDir(), Argv: []string{"fixture"}, Labels: map[string]string{}}
	first, _, err := world.StartTerminal(ctx, eng.HC, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Finish(first.Address, "exited")
	term, err := eng.lookupLaunchTerminal(ctx, spec.Name, true)
	if err != nil {
		t.Fatal(err)
	}
	st := flow.State{Project: flow.Project{ID: "p1"}, Settings: flow.DefaultSettings()}
	var hash string
	var run *flow.RunRecord
	addr, _, _, err := eng.launchRunLocked(ctx, "p1", &st, &hash, &run, true, flow.RunRecord{Role: flow.RoleWorker}, spec, term)
	if err != nil || addr == first.Address || addr == "" {
		t.Fatalf("replacement terminal = %q, err = %v", addr, err)
	}
	if _, err := eng.HC.Get(ctx, first.Address); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("finished terminal %s was not removed by the engine: err=%v", first.Address, err)
	}
}
