package factory

// PR 11.2, TDD step 1: hctest lifecycle tests (plan.md 11.2, design.md 6, 9.1, 9.3, 11.7).

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func seedPlanReviewRunWorld(t *testing.T, cands ...flow.Candidate) (*storeWorld, *[]hc.RunParams) {
	t.Helper()
	w, tok := seedPlanCoord(t, "Build greeting helper", 1, map[string][]flow.Candidate{flow.RolePlanReviewer: cands})
	var runs []hc.RunParams
	w.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	if err := w.eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: greet","estimate_lines":40,`+testPlanJSON+`}]}`)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	return w, &runs
}

func TestPlanReviewLaunchChoiceQuotaAndWorkingCoord(t *testing.T) {
	opus, sonnet := flow.Candidate{CLI: "claude", Model: "opus", Effort: "medium"}, flow.Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"}
	t.Run("J16 does not start while coordinator is working", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, opus, sonnet)
		w.srv.SetStatus("coord1", &hc.Status{App: "claude-code", State: "working"})
		w.edit(func(s *flow.State) { s.Project.Run.State, s.Project.Run.Status = flow.PhaseWorking, "working" })
		w.pass()
		if st := w.state(); len(*runs) != 0 || len(st.Project.PlanReviews) != 0 || len(st.Tasks[0].Attempts) != 0 || !st.Tasks[0].Unreviewed {
			t.Fatalf("working coord: runs=%d reviews=%+v attempts=%d", len(*runs), st.Project.PlanReviews, len(st.Tasks[0].Attempts))
		}
	})
	t.Run("launches read-only with labels, token, avoids coord model, blocks attempts", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, opus, sonnet)
		w.edit(func(s *flow.State) {
			s.Tasks = append([]flow.Task{{ID: "T0", Title: "feat: accepted base", EstimateLines: 20}}, s.Tasks...)
		})
		w.eng.Bin = "/opt/bin/sheepdog"
		w.pass()
		st := w.state()
		if len(*runs) != 1 || len(st.Project.PlanReviews) != 1 {
			t.Fatalf("runs=%d reviews=%+v, want 1 plan reviewer launch", len(*runs), st.Project.PlanReviews)
		}
		pr, p := st.Project.PlanReviews[0], (*runs)[0]
		if pr.Reviewer.CLI != "claude" || pr.Reviewer.Model != "sonnet" || pr.Verdict != "" || !pr.Accepted.IsZero() || pr.Run == nil || pr.Run.Kind != flow.RoundPlan || pr.Run.RunID == "" || len(st.Tasks[1].Attempts) != 0 || !st.Tasks[1].Unreviewed {
			t.Fatalf("PlanReviews[0]=%+v attempts=%d unreviewed=%v", pr, len(st.Tasks[1].Attempts), st.Tasks[1].Unreviewed)
		}
		if p.Cwd != st.Project.Folder || p.Labels["sheepdog.project"] != w.id || p.Labels["sheepdog.role"] != flow.RolePlanReviewer || p.Labels["sheepdog.model"] != "sonnet" || p.Labels["sheepdog.effort"] != "medium" || p.Labels["sheepdog.run"] == "" {
			t.Errorf("launch cwd/labels = (%q, %v)", p.Cwd, p.Labels)
		}
		if tok := p.Env["SHEEPDOG_RUN_TOKEN"]; tok == "" || tokenHash(tok) != pr.Run.RunTokenHash {
			t.Errorf("token hash mismatch (empty=%v)", tok == "")
		}
		if joined := strings.Join(p.Argv, " "); !strings.Contains(joined, "--permission-mode dontAsk") || !strings.Contains(joined, "Bash(/opt/bin/sheepdog factory report plan-review:*)") {
			t.Errorf("argv not read-only plan reviewer: %q", p.Argv)
		}
		for _, want := range []string{"Build greeting helper", "T1", "feat: accepted base", pr.Run.Code, "/opt/bin/sheepdog factory report plan-review"} {
			if !strings.Contains(p.Argv[len(p.Argv)-1], want) {
				t.Errorf("prompt missing %q", want)
			}
		}
		if launchCapability(flow.RolePlanReviewer)(flow.Candidate{CLI: world.AgyCLI, Model: "m", Effort: "medium"}).Why != "" || launchCapability(flow.RolePlanReviewer)(flow.Candidate{CLI: world.CodexCLI, Model: "m", Effort: "medium"}).Why == "" ||
			launchArgv(flow.RolePlanReviewer, flow.Candidate{CLI: world.CodexCLI, Model: "m", Effort: "medium"}, w.repo, "", false) != nil || slices.Contains(launchArgv(flow.RolePlanReviewer, flow.Candidate{CLI: world.AgyCLI, Model: "m", Effort: "medium"}, w.repo, "conv-1", true), "--conversation") {
			t.Errorf("launchCapability/launchArgv failed for RolePlanReviewer")
		}
	})
	t.Run("falls back to coordinator model when it is the only candidate", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, opus)
		w.pass()
		if st := w.state(); len(*runs) != 1 || len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Reviewer.Model != "opus" || st.Project.PlanReviews[0].Run == nil {
			t.Fatalf("fallback: runs=%d reviews=%+v, want active claude/opus", len(*runs), st.Project.PlanReviews)
		}
	})
	t.Run("out of quota is unavailable at once with no wait", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, sonnet)
		baseExec := w.srv.OnExec
		w.srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if argv := cdUnwrapExecArgv(p.Argv); len(argv) >= 2 && filepath.Base(argv[0]) == "codexbar" {
				zero := 0
				return hc.ExecResult{Code: &zero, Stdout: []byte(poolOut("claude", w.now, &poolWin{0, 2 * time.Hour}, &poolWin{80, 72 * time.Hour}, nil).Stdout)}, nil
			}
			return baseExec(p)
		}
		w.pass()
		if st := w.state(); len(*runs) != 0 || len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Verdict != flow.VerdictUnavailable || !strings.Contains(strings.ToLower(st.Project.PlanReviews[0].Why), "quota") || st.Project.PlanReviews[0].Accepted.IsZero() || st.Tasks[0].Unreviewed {
			t.Fatalf("quota fail-open: runs=%d reviews=%+v unreviewed=%v", len(*runs), st.Project.PlanReviews, st.Tasks[0].Unreviewed)
		}
	})
}

func TestPlanReviewJ15FailuresAndPrecedence(t *testing.T) {
	launchActive := func(t *testing.T) (*storeWorld, string) {
		t.Helper()
		w, _ := seedPlanReviewRunWorld(t, flow.Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"})
		w.pass()
		st := w.state()
		if len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Run == nil || st.Project.PlanReviews[0].Run.RunID == "" {
			t.Fatalf("expected launched plan reviewer, got reviews=%+v", st.Project.PlanReviews)
		}
		id := st.Project.PlanReviews[0].Run.RunID
		w.srv.SetStatus(id, &hc.Status{App: "claude-code", State: "working"})
		w.pass()
		return w, id
	}
	for _, cause := range []string{"gone", "idle", "overdue", "all_dropped", "with_j3_alert"} {
		t.Run(cause, func(t *testing.T) {
			w, id := launchActive(t)
			switch cause {
			case "gone":
				_, _ = w.client.Kill(w.ctx, id)
				_, _ = w.client.Remove(w.ctx, id)
			case "idle", "with_j3_alert":
				w.srv.SetStatus(id, &hc.Status{App: "claude-code", State: "idle"})
				w.pass()
				w.advanceMinute()
				if cause == "with_j3_alert" {
					w.edit(func(s *flow.State) {
						s.Project.RunID, s.Project.Run, s.Project.Alerts = "", nil, map[string]flow.Alert{flow.AlertDisk: {Kind: flow.AlertDisk, Cause: "disk full"}}
					})
				}
			case "overdue":
				w.edit(func(s *flow.State) {
					s.Settings.ReviewMinutes = 10
					s.Project.PlanReviews[0].Run.Age = 10*time.Minute - 10*time.Second
				})
				w.advanceMinute()
			case "all_dropped":
				w.edit(func(s *flow.State) { s.Tasks[0].Dropped = &flow.Dropped{Why: "opt"} })
			}
			w.pass()
			st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
			pr := st.Project.PlanReviews[0]
			if pr.Verdict != flow.VerdictUnavailable || pr.Why == "" || pr.Accepted.IsZero() || (pr.Run != nil && (pr.Run.RunID != "" || pr.Run.RunTokenHash != "")) || st.Tasks[0].Unreviewed {
				t.Fatalf("%s: PlanReviews[0]=%+v unreviewed=%v", cause, pr, st.Tasks[0].Unreviewed)
			}
			if rs, _ := termState(w.ctx, w.client, id); rs != flow.RunGone || len(sendsTo(t, w.srv.Requests(), id)) != 0 {
				t.Errorf("%s: terminal %s state=%s sends=%d", cause, id, rs, len(sendsTo(t, w.srv.Requests(), id)))
			}
		})
	}
	t.Run("stop failure retries without accepting", func(t *testing.T) {
		w, id := launchActive(t)
		w.srv.SetStatus(id, &hc.Status{App: "claude-code", State: "idle"})
		w.pass()
		w.advanceMinute()
		var failKill atomic.Bool
		failKill.Store(true)
		w.eng.HC = hc.NewClient(methodFailProxy(t, w.srv.Path, "terminal.kill", &failKill))
		w.pass()
		if st := w.state(); st.Project.PlanReviews[0].Verdict != "" || !st.Project.PlanReviews[0].Accepted.IsZero() || !st.Tasks[0].Unreviewed || len(st.Tasks[0].Attempts) != 0 {
			t.Fatalf("failed J15 stop mutated state: reviews=%+v", st.Project.PlanReviews)
		}
		failKill.Store(false)
		w.pass()
		if st := w.state(); st.Project.PlanReviews[0].Verdict != flow.VerdictUnavailable || st.Project.PlanReviews[0].Accepted.IsZero() || st.Tasks[0].Unreviewed {
			t.Fatalf("J15 stop retry did not accept: reviews=%+v", st.Project.PlanReviews)
		}
	})
}

func TestPlanReviewStartFailedSaveTokenRestartSweepAndStop(t *testing.T) {
	sonnet := flow.Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"}
	t.Run("failed save at start leaves no record and no token", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, sonnet)
		var failed atomic.Bool
		w.setWriteHooks(func(path string, s flow.State) error {
			if len(s.Project.PlanReviews) > 0 && s.Project.PlanReviews[0].Run != nil && failed.CompareAndSwap(false, true) {
				return errors.New("save failed before launch")
			}
			return SaveState(path, s)
		}, nil)
		w.pass()
		if st := w.state(); !failed.Load() || len(st.Project.PlanReviews) != 0 || len(*runs) != 0 || !st.Tasks[0].Unreviewed {
			t.Fatalf("failed save left state: failed=%v reviews=%+v runs=%d", failed.Load(), st.Project.PlanReviews, len(*runs))
		}
	})
	t.Run("token refused other verbs and ask; sweep owns terminal; restart keeps watching", func(t *testing.T) {
		w, runs := seedPlanReviewRunWorld(t, sonnet)
		w.pass()
		st := w.state()
		if len(*runs) != 1 || len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Run == nil {
			t.Fatalf("expected launch: runs=%d reviews=%+v", len(*runs), st.Project.PlanReviews)
		}
		prTok, runID, seq := (*runs)[0].Env["SHEEPDOG_RUN_TOKEN"], st.Project.PlanReviews[0].Run.RunID, st.Seq
		w.srv.SetStatus(runID, &hc.Status{App: "claude-code", State: "working"})
		for _, tc := range []struct{ verb, body string }{
			{"review", `{"verdict":"pass","code":"c","head":"h","self_check_misses":0,"deletable":"none"}`},
			{"retro", `{"verdict":"ship","code":"c","head":"h","cause":"none","worker_change":"w","worker_change_kind":"check","coordinator_change":"c","coordinator_change_kind":"brief"}`},
			{"plan apply", `{"tasks":[]}`}, {"task", `{"action":"drop","task":"T1","why":"no"}`},
			{"ask", `{"kind":"decision","question":"keep T1?","options":["Yes","No"],"recommend":"Yes"}`},
		} {
			if got := refusalCode(w.eng.Report(prTok, tc.verb, json.RawMessage(tc.body))); got != "kind_not_allowed" {
				t.Errorf("plan reviewer token %q = %q, want kind_not_allowed", tc.verb, got)
			}
		}
		if st := w.state(); st.Seq != seq || len(st.Asks) != 0 {
			t.Fatalf("refused reports mutated state: seq=%d/%d asks=%d", st.Seq, seq, len(st.Asks))
		}
		if probe := world.SweepTerminals(w.ctx, w.client, w.state()); probe.Unknown != "" || len(probe.Items) != 0 {
			t.Fatalf("SweepTerminals reported plan reviewer as stray: %+v", probe)
		}
		w.restart()
		w.pass()
		if st := w.state(); len(*runs) != 1 || st.Project.PlanReviews[0].Run == nil || st.Project.PlanReviews[0].Run.RunID != runID || len(st.Tasks[0].Attempts) != 0 {
			t.Fatalf("restart lost plan reviewer %s: runs=%d reviews=%+v", runID, len(*runs), st.Project.PlanReviews)
		}
	})
	t.Run("Engine.Stop stops terminal always and deletes record only without verdict", func(t *testing.T) {
		for _, verdict := range []string{"", flow.VerdictChanges} {
			w, _ := seedPlanReviewRunWorld(t, sonnet)
			w.pass()
			if len(w.state().Project.PlanReviews) != 1 || w.state().Project.PlanReviews[0].Run == nil {
				t.Fatalf("verdict=%q: no active plan reviewer: %+v", verdict, w.state().Project.PlanReviews)
			}
			id := w.state().Project.PlanReviews[0].Run.RunID
			if verdict != "" {
				w.edit(func(s *flow.State) { s.Project.PlanReviews[0].Verdict = verdict })
			}
			if err := w.eng.Stop(w.id); err != nil {
				t.Fatalf("Stop(verdict=%q): %v", verdict, err)
			}
			if rs, _ := termState(w.ctx, w.client, id); rs != flow.RunGone {
				t.Errorf("verdict=%q: terminal %s still %s after Stop", verdict, id, rs)
			}
			st := w.state()
			if verdict == "" && (len(st.Project.PlanReviews) != 0 || !st.Tasks[0].Unreviewed) {
				t.Fatalf("Stop without verdict: reviews=%+v unreviewed=%v", st.Project.PlanReviews, st.Tasks[0].Unreviewed)
			}
			if verdict != "" && (len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Verdict != verdict || (st.Project.PlanReviews[0].Run != nil && st.Project.PlanReviews[0].Run.RunID != "")) {
				t.Fatalf("Stop with verdict: reviews=%+v, want kept with cleared RunID", st.Project.PlanReviews)
			}
		}
	})
}
