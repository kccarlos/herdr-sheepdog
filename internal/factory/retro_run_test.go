package factory

// PR 12.2, TDD step 1: failing tests for retro run lifecycle on the shared
// Attempt.Review slot, launchCapability/launchArgv/ClaudeRetroArgv,
// runByTokenLocked role isolation (r2 B1, KB-RET-12), view "In retro" +
// TaskView.Rounds exclusion, N28 launch/nudge/stop/retry -> M4 fallback,
// acceptAmendLocked, and answerAskLocked (plan.md 12.2, design.md 4.5, 4.6,
// 4.8, 7.2, 9.2, 9.3, KB-RET-1, KB-RET-9, KB-RET-12, KB-RET-15).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestLaunchCapabilityAndWiringForRoleRetro checks plan.md 12.2, design.md 9.2,
// and KB-RET-1/KB-RET-15: launchCapability accepts claude and agy for RoleRetro
// and refuses codex (until S-REV-2); launchArgv clears agy session on RoleRetro;
// taskExecutors, actOutage and stopReviewRec all handle retro runs.
func TestLaunchCapabilityAndWiringForRoleRetro(t *testing.T) {
	for _, tc := range []struct {
		cli    string
		wantOK bool
	}{
		{"claude", true},
		{world.AgyCLI, true},
		{world.CodexCLI, false},
		{"unknown", false},
	} {
		got := launchCapability(flow.RoleRetro)(flow.Candidate{CLI: tc.cli, Model: "m", Effort: "high"})
		if (got.Why == "") != tc.wantOK {
			t.Errorf("launchCapability(retro)(%s) = %q, want ok=%v", tc.cli, got.Why, tc.wantOK)
		}
	}

	// agy retro runs are always fresh (never --conversation even if resume=true).
	agyArgv := launchArgv(flow.RoleRetro, flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"}, "/wt", agyHistoryConvID, true)
	if slices.Contains(agyArgv, "--conversation") || slices.Contains(agyArgv, agyHistoryConvID) {
		t.Errorf("launchArgv(retro, agy, resume=true) = %q, must stay fresh without --conversation", agyArgv)
	}

	// B1: launchArgv for RoleRetro on Codex must fail closed (return nil),
	// never falling through to CodexWorkArgv (--dangerously-bypass-approvals-and-sandbox).
	if got := launchArgv(flow.RoleRetro, flow.Candidate{CLI: world.CodexCLI, Model: "gpt-6.2-sol", Effort: "high"}, "/wt", "", false); got != nil {
		t.Fatalf("launchArgv(retro, codex) = %q, want nil (must fail closed, never CodexWorkArgv)", got)
	}
	// RoleReviewer on Codex must stay read-only and never emit CodexWorkArgv.
	if got := launchArgv(flow.RoleReviewer, flow.Candidate{CLI: world.CodexCLI, Model: "gpt-6.2-sol", Effort: "high"}, "/wt", "", false); len(got) == 0 || slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") || !slices.Contains(got, "read-only") {
		t.Fatalf("launchArgv(reviewer, codex) = %q, want read-only CodexArgv without --dangerously-bypass-approvals-and-sandbox", got)
	}
	// Every other disallowed (CLI, role) pair must fail closed (return nil).
	for _, tc := range []struct {
		cli, role, session string
		resume             bool
	}{
		{world.CodexCLI, flow.RoleRetro, "sess-1", true},
		{world.CodexCLI, "", "", false},
		{world.CodexCLI, "unknown", "", false},
		{world.AgyCLI, "", "", false},
		{world.AgyCLI, "unknown", "", false},
		{"claude", flow.RoleRetro, "", false},
		{"claude", flow.RoleReviewer, "", false},
		{"claude", "", "", false},
		{"claude", "unknown", "", false},
		{"unknown", flow.RoleWorker, "", false},
		{"unknown", flow.RoleRetro, "", false},
		{"", flow.RoleRetro, "", false},
	} {
		got := launchArgv(tc.role, flow.Candidate{CLI: tc.cli, Model: "m", Effort: "high"}, "/wt", tc.session, tc.resume)
		if got != nil {
			t.Errorf("launchArgv(%q, %q, resume=%v) = %q, want nil (must fail closed)", tc.role, tc.cli, tc.resume, got)
		}
	}

	// taskExecutors and actOutage wire AskStartRetro, AskNudgeRetro, AskStopRetro.
	for _, verb := range []string{flow.AskStartRetro, flow.AskNudgeRetro, flow.AskStopRetro} {
		if taskExecutors[verb] == nil {
			t.Errorf("taskExecutors[%q] is nil", verb)
		}
		if !actOutage(verb, flow.Facts{HCUnavailable: true}, nil) {
			t.Errorf("actOutage(%q, HCUnavailable=true) = false, want true", verb)
		}
	}

	// stopReviewRec preserves RoleRetro when rev.Kind == RoundRetro.
	rev := &flow.ReviewRun{Kind: flow.RoundRetro, Round: 4, Head: "c1", RunID: "term-retro"}
	stopReviewRec(rev, flow.DefaultSettings(), time.Now().UTC())
	if rev.Run == nil || rev.Run.Role != flow.RoleRetro || rev.Run.State != flow.PhaseEnded || rev.Run.Result != flow.EndStopped {
		t.Fatalf("stopReviewRec on RoundRetro = %+v, want Role=retro ended(stopped)", rev.Run)
	}
}

// TestRetroTokenRoleIsolationKBRET12 checks r2 B1, plan.md 12.2, design.md 7.2,
// and KB-RET-12: runByTokenLocked returns flow.RoleRetro for a retro run token
// (via a.Review.Role()), so report review is refused kind_not_allowed at
// report.go:65 without recording a round or writing a report file.
func TestRetroTokenRoleIsolationKBRET12(t *testing.T) {
	f := newReviewTestFixture(t)
	st, err := LoadState(f.eng.Paths.State(f.id))
	if err != nil {
		t.Fatal(err)
	}
	st.Tasks[0].Attempts[0].Review.Kind = flow.RoundRetro
	st.Tasks[0].Attempts[0].Review.Run.Role = flow.RoleRetro
	if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
		t.Fatal(err)
	}

	t.Run("runByTokenLocked returns RoleRetro", func(t *testing.T) {
		f.eng.mu.Lock()
		pid, _, role, ti, runID := f.eng.runByTokenLocked(f.revTok)
		f.eng.mu.Unlock()
		if pid != f.id || role != flow.RoleRetro || ti != 0 || runID != "term-rev-1" {
			t.Fatalf("runByTokenLocked(retroTok) = (%q, %q, %d, %q), want (%q, %q, 0, %q)",
				pid, role, ti, runID, f.id, flow.RoleRetro, "term-rev-1")
		}
	})

	t.Run("retro token refused report review pass (KB-RET-12, r2 B1)", func(t *testing.T) {
		f.logs.Reset()
		err := f.eng.Report(f.revTok, "review", validPassPayload(t, f.code, f.c1))
		if got := refusalCode(err); got != "kind_not_allowed" {
			t.Fatalf("Report(retroTok, review pass) = %v (code %q), want kind_not_allowed", err, got)
		}
		if !strings.Contains(err.Error(), flow.RoleRetro) {
			t.Errorf("refusal %q should name role %q", err.Error(), flow.RoleRetro)
		}
		gotState, err := LoadState(f.eng.Paths.State(f.id))
		if err != nil {
			t.Fatal(err)
		}
		if gotState.Seq != 1 || len(gotState.Tasks[0].Attempts[0].Rounds) != 0 || len(gotState.Tasks[0].Retros) != 0 {
			t.Fatalf("refused retro report review mutated state: seq=%d rounds=%+v retros=%+v",
				gotState.Seq, gotState.Tasks[0].Attempts[0].Rounds, gotState.Tasks[0].Retros)
		}
		if _, statErr := os.Stat(f.eng.Paths.Report(f.id, "T1", 1)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("refused retro report review wrote report file: stat=%v", statErr)
		}
	})

	t.Run("retro token refused report review changes with real hostile capture", func(t *testing.T) {
		body := mustReviewPayload(t, map[string]any{
			"verdict":           flow.VerdictChanges,
			"code":              f.code,
			"head":              f.c1,
			"self_check_misses": 1,
			"deletable":         "none",
			"findings":          hostileFindingsText,
		})
		err := f.eng.Report(f.revTok, "review", body)
		if got := refusalCode(err); got != "kind_not_allowed" {
			t.Fatalf("Report(retroTok, review changes) = %v (code %q), want kind_not_allowed", err, got)
		}
		gotState, _ := LoadState(f.eng.Paths.State(f.id))
		if gotState.Seq != 1 || len(gotState.Tasks[0].Attempts[0].Rounds) != 0 {
			t.Fatalf("refused changes mutated state: seq=%d rounds=%+v", gotState.Seq, gotState.Tasks[0].Attempts[0].Rounds)
		}
	})

	t.Run("retro token refused report ready names retro role", func(t *testing.T) {
		err := f.eng.Report(f.revTok, "ready", json.RawMessage(`{"head":"`+f.c1+`"}`))
		if got := refusalCode(err); got != "kind_not_allowed" {
			t.Fatalf("Report(retroTok, ready) = %v (code %q), want kind_not_allowed", err, got)
		}
		if !strings.Contains(err.Error(), flow.RoleRetro) {
			t.Errorf("refusal %q should name role %q", err.Error(), flow.RoleRetro)
		}
	})
}

// TestRetroViewInRetroAndRoundsExclusion checks plan.md 12.2 and KB-RET-9:
// view.go shows "In retro" for an active RoundRetro run and excludes RoundRetro
// from TaskView.Rounds alongside RoundSendBack.
func TestRetroViewInRetroAndRoundsExclusion(t *testing.T) {
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-retro-view", Folder: "/tmp/demo", Started: true},
		Tasks: []flow.Task{
			{
				ID:      "T1",
				Title:   "feat: active retro",
				LastRow: "N28",
				Attempts: []flow.Attempt{{
					BaseCommit: "000",
					RunID:      "worker-t1",
					Ready:      []flow.Ready{{Head: "c1"}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
						{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
						{N: 3, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
					},
					Review: &flow.ReviewRun{
						RunID:    "rev-demo-t1-1-4",
						Round:    4,
						Kind:     flow.RoundRetro,
						Head:     "c1",
						Reviewer: flow.Reviewer{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"},
					},
				}},
			},
			{
				ID:      "T2",
				Title:   "feat: completed retro excluded from Rounds",
				LastRow: "N22",
				Attempts: []flow.Attempt{{
					BaseCommit: "000",
					RunID:      "worker-t2",
					Ready:      []flow.Ready{{Head: "c1"}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
						{N: 2, Kind: flow.RoundSendBack, Head: "c0", Verdict: flow.VerdictChanges, Why: "missing case"},
						{N: 3, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
						{N: 4, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
						{N: 5, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictFix, Majors: 1},
					},
				}},
			},
		},
	}

	g := GraphViewOf(st, nil)
	if len(g.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2", len(g.Tasks))
	}
	t1, t2 := g.Tasks[0], g.Tasks[1]
	if t1.Stage != "working" || t1.WhyWaiting != "In retro" || t1.Model != agyReviewModel || t1.Terminal != "rev-demo-t1-1-4" || t1.Rounds != 3 {
		t.Errorf("T1 active retro view = %+v, want stage=working why=%q model=%q terminal=rev-demo-t1-1-4 rounds=3",
			t1, "In retro", agyReviewModel)
	}
	if t2.Rounds != 3 {
		t.Errorf("T2 Rounds = %d, want 3 (RoundRetro and RoundSendBack excluded)", t2.Rounds)
	}
}

// setupRetroRig prepares an agyRig with task T1 pushed to PR #1 at c1 and
// three prior reviewer rounds (rounds 2 and 3 with majors > 0) so the next
// pass triggers L11 -> N28 (retro).
func setupRetroRig(t *testing.T, retroCand flow.Candidate) (*agyRig, string, string) {
	t.Helper()
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}},
		flow.RoleWorker:      {{CLI: world.AgyCLI, Model: agyWorkerModel, Effort: "medium"}},
		flow.RoleReviewer:    {{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}},
		flow.RoleRetro:       {retroCand},
	})
	r.pass() // J7 coordinator
	coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: retro target","brief":"Add calc.go handling all integers.",` + testScopeJSON + `,"outcomes":[{"text":"calc exists","proved_by":"manual: calc.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 worker
	wt := r.state().Tasks[0].Attempts[0].Worktree
	workerTok := r.last().Env["SHEEPDOG_RUN_TOKEN"]
	if err := os.WriteFile(filepath.Join(wt, "calc.go"), []byte("package demo\nfunc Calc(n int) int { return n }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "calc.go")
	gitIn(t, wt, "commit", "-m", "feat: add calc")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"calc exists","artifact":"calc.go"}],"touched":"calc.go","skipped":""}`)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	r.pass() // N26 pushes and opens PR
	writePRJSON(t, r.gh, "view.json", c1, greenRollup)

	r1Path := r.paths.Report(r.id, "T1", 1)
	r2Path := r.paths.Report(r.id, "T1", 2)
	r3Path := r.paths.Report(r.id, "T1", 3)
	r.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: c1, Reviewer: flow.Reviewer{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: r1Path},
			{N: 2, Kind: flow.RoundDelta, Head: c1, Reviewer: flow.Reviewer{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: r2Path},
			{N: 3, Kind: flow.RoundDelta, Head: c1, Reviewer: flow.Reviewer{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}, Verdict: flow.VerdictChanges, Majors: 2, ReportPath: r3Path},
		}
	})
	return r, c1, workerTok
}

// TestRetroRunLaunchClaudeAndAgy checks plan.md 12.2, design.md 4.5 N28, 9.2,
// 9.3, KB-RET-1, and KB-RET-15: N28 launches a retro run on Attempt.Review with
// Kind=RoundRetro, Role=RoleRetro, retroPrompt (case file + scratch findings
// path outside worktree + report retro command + no <report>), ClaudeRetroArgv
// for Claude, agy read-only prefix for agy, "In retro" view, and L5 delta mode.
func TestRetroRunLaunchClaudeAndAgy(t *testing.T) {
	t.Run("agy initial retro launch via L11 and N28", func(t *testing.T) {
		r, c1, _ := setupRetroRig(t, flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"})
		runsBefore := len(*r.runs)
		r.pass() // L11 -> N28 launches retro

		st := r.state()
		if st.Tasks[0].LastRow != "N28" {
			t.Fatalf("LastRow = %q, want N28 (recaps=%+v)", st.Tasks[0].LastRow, st.Tasks[0].Recaps)
		}
		if len(*r.runs) != runsBefore+1 {
			t.Fatalf("runs = %d, want %d (retro launch)", len(*r.runs), runsBefore+1)
		}
		a := st.Tasks[0].Attempts[0]
		if a.Review == nil || a.Review.Kind != flow.RoundRetro || a.Review.Role() != flow.RoleRetro ||
			a.Review.Round != 4 || a.Review.Head != c1 || a.Review.RunID == "" {
			t.Fatalf("a.Review = %+v, want active RoundRetro round 4 at %s", a.Review, c1)
		}
		if a.Review.Run == nil || a.Review.Run.Role != flow.RoleRetro {
			t.Fatalf("a.Review.Run = %+v, want Role=%q", a.Review.Run, flow.RoleRetro)
		}
		runParams := r.last()
		if runParams.Labels["sheepdog.role"] != flow.RoleRetro {
			t.Errorf("terminal label sheepdog.role = %q, want %q", runParams.Labels["sheepdog.role"], flow.RoleRetro)
		}
		assertAgyFreshArgv(t, "retro", agyReviewModel, runParams,
			"Do not modify, create or delete any files; read and run tests only",
			"factory report retro",
			"--code "+a.Review.Code,
			"--head "+c1,
			r.paths.Report(r.id, "T1", 3),
		)
		prompt, _ := strings.CutPrefix(runParams.Argv[len(runParams.Argv)-1], "-i=")
		if strings.Contains(prompt, "<report>") || strings.Contains(prompt, "factory report review") {
			t.Errorf("retro prompt must use report retro, not <report> or report review:\n%s", prompt)
		}
		findingsPath := extractFindingsPath(prompt)
		if findingsPath == "" || !filepath.IsAbs(findingsPath) {
			t.Fatalf("retro prompt missing absolute --findings <path>:\n%s", prompt)
		}
		assertScratchPathOutsideWorktree(t, findingsPath, a.Worktree, r.paths.Report(r.id, "T1", 4))

		gv := GraphViewOf(st, nil)
		if gv.Tasks[0].WhyWaiting != "In retro" {
			t.Errorf("GraphViewOf WhyWaiting = %q, want %q", gv.Tasks[0].WhyWaiting, "In retro")
		}
	})

	t.Run("claude retro launch uses ClaudeRetroArgv and report retro prompt", func(t *testing.T) {
		r, c1, _ := setupRetroRig(t, flow.Candidate{CLI: "claude", Model: "opus", Effort: "high"})
		r.eng.Bin = "/opt/bin/sheepdog"
		runsBefore := len(*r.runs)
		r.pass() // L11 -> N28 launches Claude retro

		st := r.state()
		if st.Tasks[0].LastRow != "N28" || len(*r.runs) != runsBefore+1 {
			t.Fatalf("LastRow=%q runs=%d (before=%d), want N28 with Claude retro launch", st.Tasks[0].LastRow, len(*r.runs), runsBefore)
		}
		runParams := r.last()
		if world.ArgvCLI(runParams.Argv) != "claude" {
			t.Fatalf("expected claude retro argv, got %q", runParams.Argv)
		}
		prompt := runParams.Argv[len(runParams.Argv)-1]
		if !strings.Contains(prompt, "/opt/bin/sheepdog factory report retro") || !strings.Contains(prompt, c1) {
			t.Fatalf("claude retro prompt missing `/opt/bin/sheepdog factory report retro` or head %s:\n%s", c1, prompt)
		}
		if strings.Contains(prompt, "<report>") {
			t.Errorf("claude retro prompt still contains <report>:\n%s", prompt)
		}
		findingsPath := extractFindingsPath(prompt)
		if findingsPath == "" || !filepath.IsAbs(findingsPath) {
			t.Fatalf("claude retro prompt missing absolute --findings <path>:\n%s", prompt)
		}
		assertScratchPathOutsideWorktree(t, findingsPath, st.Tasks[0].Attempts[0].Worktree, r.paths.Report(r.id, "T1", 4))

		allowed := ""
		for _, arg := range runParams.Argv[:len(runParams.Argv)-1] {
			if v, ok := strings.CutPrefix(arg, "--allowedTools="); ok {
				allowed = v
			}
		}
		if !strings.Contains(allowed, "Bash(/opt/bin/sheepdog factory report retro:*)") {
			t.Errorf("claude retro argv missing Bash(/opt/bin/sheepdog factory report retro:*):\n%s", allowed)
		}
		if strings.Contains(allowed, "report review") || strings.Contains(allowed, "Write(") {
			t.Errorf("claude retro argv contains forbidden rule:\n%s", allowed)
		}
		wantEditA := "Edit(//" + strings.TrimPrefix(findingsPath, "/") + ")"
		wantEditB := "Edit(//" + findingsPath + ")"
		if !strings.Contains(allowed, wantEditA) && !strings.Contains(allowed, wantEditB) {
			t.Errorf("claude retro argv Edit rule does not match %q:\n%s", findingsPath, allowed)
		}
	})

	t.Run("L5 delta check retro launch after prior FIX", func(t *testing.T) {
		r, c1, workerTok := setupRetroRig(t, flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"})
		wt := r.state().Tasks[0].Attempts[0].Worktree
		// Seed a prior retro FIX at round 4 on c1, then worker commits c2 and reports ready.
		r.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = append(a.Rounds, flow.Round{
				N: 4, Kind: flow.RoundRetro, Head: c1,
				Reviewer:   flow.Reviewer{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"},
				Verdict:    flow.VerdictFix,
				Majors:     1,
				ReportPath: r.paths.Report(r.id, "T1", 4),
			})
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "patch-shaped-fix",
				WorkerChange:          "handle negative inputs in one branch",
				WorkerChangeKind:      flow.ChangeKindCheck,
				CoordinatorChange:     "include negative cases in outcomes",
				CoordinatorChangeKind: flow.ChangeKindBrief,
				At:                    *r.now,
			}}
		})
		if err := os.WriteFile(filepath.Join(wt, "calc.go"), []byte("package demo\nfunc Calc(n int) int { if n < 0 { return -n }; return n }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt, "add", "calc.go")
		gitIn(t, wt, "commit", "-m", "fix: handle negative inputs")
		c2 := gitIn(t, wt, "rev-parse", "HEAD")
		if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c2+`","proofs":[{"outcome":"calc exists","artifact":"calc.go"}],"touched":"calc.go","skipped":""}`)); err != nil {
			t.Fatalf("ready c2: %v", err)
		}
		r.pass() // N26 pushes c2
		writePRJSON(t, r.gh, "view.json", c2, greenRollup)
		runsBefore := len(*r.runs)
		r.pass() // L5 -> N28 delta retro launch

		st := r.state()
		a := st.Tasks[0].Attempts[0]
		if st.Tasks[0].LastRow != "N28" || len(*r.runs) != runsBefore+1 {
			t.Fatalf("L5 delta retro: LastRow=%q runs=%d (before=%d)", st.Tasks[0].LastRow, len(*r.runs), runsBefore)
		}
		if a.Review == nil || a.Review.Kind != flow.RoundRetro || a.Review.Round != 5 || a.Review.Head != c2 {
			t.Fatalf("L5 delta retro Review = %+v, want RoundRetro round 5 at %s", a.Review, c2)
		}
		prompt, _ := strings.CutPrefix(r.last().Argv[len(r.last().Argv)-1], "-i=")
		if !strings.Contains(strings.ToLower(prompt), "delta") || !strings.Contains(prompt, c2) {
			t.Errorf("L5 delta retro prompt missing delta mode or head %s:\n%s", c2, prompt)
		}
	})
}

// TestRetroRunNudgeStopRetryToM4Fallback checks plan.md 12.2, design.md 4.6 M4,
// 4.8, and section 5 R20: an idle agy retro run with no report is nudged once
// naming `factory report retro`, stopped as EndStuck, retried up to
// reviewer_retries (repeating a single candidate in a fresh session via
// reviewChoiceReason), and once retries are exhausted observeReviewLocked sets
// Facts.RetroUnavailable so the task falls through L12 -> N25 (open_recap@review
// with [split, new, drop]) -> N6, leaving zero retro rounds in state.json.
func TestRetroRunNudgeStopRetryToM4Fallback(t *testing.T) {
	r, c1, _ := setupRetroRig(t, flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"})
	r.edit(func(s *flow.State) { s.Settings.ReviewerRetries = 1 })

	// Start 1 (initial): L11 -> N28 launches retro.
	r.pass()
	st := r.state()
	if st.Tasks[0].LastRow != "N28" || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.RunID == "" {
		t.Fatalf("initial retro did not start: LastRow=%q review=%+v", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}
	firstID := st.Tasks[0].Attempts[0].Review.RunID

	// Retro finishes turn without calling report retro -> R9 -> R20 -> N28 nudges once.
	r.srv.SetStatus(firstID, &hc.Status{App: "antigravity", State: "done", Msg: "Done looking."})
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
	*r.now = r.now.Add(25 * time.Second)
	r.pass()

	sends := sendsTo(t, r.srv.Requests(), firstID)
	if len(sends) != 1 {
		t.Fatalf("expected 1 N28 nudge send to %s, got %d: %q", firstID, len(sends), sends)
	}
	if !strings.Contains(sends[0], "factory report retro") || strings.Contains(sends[0], "factory report review") || strings.Contains(sends[0], "<report>") {
		t.Errorf("N28 nudge = %q, want `factory report retro` (not report review or <report>)", sends[0])
	}

	// Still idle after nudge -> N28 stops retro as EndStuck.
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
	*r.now = r.now.Add(25 * time.Second)
	r.pass()

	if _, err := r.cl.Get(context.Background(), firstID); !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("first retro terminal %s still exists after stop_retro: err=%v", firstID, err)
	}
	st = r.state()
	if rv := st.Tasks[0].Attempts[0].Review; rv == nil || rv.Run == nil || rv.Run.State != flow.PhaseEnded || rv.Run.Result != flow.EndStuck || rv.Run.Role != flow.RoleRetro {
		t.Fatalf("after stop_retro: Review=%+v, want Role=retro ended(stuck)", rv)
	}

	// Start 2 (retry 1 of 1): reviewChoiceReason repeats the single retro candidate in a fresh session.
	r.pass()
	st = r.state()
	rv := st.Tasks[0].Attempts[0].Review
	if st.Tasks[0].LastRow != "N28" || rv == nil || rv.RunID == "" || rv.RunID == firstID || rv.Retries != 1 {
		t.Fatalf("retry retro = LastRow=%q Review=%+v, want fresh RunID with Retries=1", st.Tasks[0].LastRow, rv)
	}
	if !slices.Contains(rv.FailedModels, world.AgyCLI+"/"+agyReviewModel) {
		t.Errorf("FailedModels = %v, want %q recorded", rv.FailedModels, world.AgyCLI+"/"+agyReviewModel)
	}
	secondID := rv.RunID

	// Retry also goes idle -> nudged once -> stopped as EndStuck (retries now exhausted).
	r.srv.SetStatus(secondID, &hc.Status{App: "antigravity", State: "done", Msg: "Still no report."})
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // nudge_retro@4@1@c1
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // stop_retro@4@1@c1

	// Next pass: retries exhausted -> observeReviewLocked sets RetroUnavailable (M4) -> L12 -> N25.
	r.pass()
	st = r.state()
	if st.Tasks[0].LastRow != "N25" {
		t.Fatalf("after exhausted retro retries: LastRow = %q, want N25 (M4 fallback)", st.Tasks[0].LastRow)
	}
	rc := flow.OpenRecap(st.Tasks[0])
	if rc == nil || rc.Trigger != flow.TriggerReview || !slices.Equal(rc.Allowed, []string{"split", "new", "drop"}) {
		t.Fatalf("M4 fallback recap = %+v, want open review recap [split new drop]", rc)
	}
	if len(st.Tasks[0].Retros) != 0 {
		t.Errorf("exhausted retro recorded t.Retros = %+v, want empty", st.Tasks[0].Retros)
	}
	for _, rnd := range st.Tasks[0].Attempts[0].Rounds {
		if rnd.Kind == flow.RoundRetro {
			t.Errorf("exhausted retro recorded RoundRetro in a.Rounds: %+v", rnd)
		}
	}
	// Log records N25 with open_recap@review and retro_unavailable:true.
	logBytes, err := os.ReadFile(r.paths.Log(r.id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBytes), `"rule":"N25"`) ||
		!strings.Contains(string(logBytes), `"retro_unavailable":true`) ||
		!strings.Contains(string(logBytes), "open_recap@review") {
		t.Errorf("log.jsonl missing N25 M4 fallback markers for head %s:\n%s", c1, string(logBytes))
	}
}

// TestRetroAmendRequestAndAnswerAsk checks plan.md 12.2's remaining two shared-slot
// hooks: acceptAmendLocked compares t.Request[a.Review.Role()] (so amending
// request.retro stops a live retro run and amending request.reviewer does not),
// and answerAskLocked delivers answers to a live retro run when AskedBy == RoleRetro.
func TestRetroAmendRequestAndAnswerAsk(t *testing.T) {
	t.Run("acceptAmendLocked stops live retro only on request.retro change", func(t *testing.T) {
		l := newLanding(t).withTokens()
		ctx := context.Background()
		retroID, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1-4", Cwd: l.wt, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		l.srv.SetStatus(retroID, &hc.Status{App: "claude-code", State: "working"})
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Review = &flow.ReviewRun{
				RunID: retroID, Round: 4, Kind: flow.RoundRetro, Head: l.c1, Code: "retcode123",
				Run: &flow.RunRecord{Address: retroID, Role: flow.RoleRetro, CLI: "claude", State: flow.PhaseWorking},
			}
		})

		// Amending request.reviewer must NOT stop the live RoundRetro run.
		l.report("tok-coord", "plan amend", `{"why":"change reviewer","tasks":[{"id":"T1","request":{"reviewer":[{"cli":"claude","model":"opus","effort":"high"}]}}]}`, "")
		if rs, err := termState(ctx, l.client, retroID); err != nil || rs == flow.RunGone {
			t.Fatalf("amending request.reviewer stopped live RoundRetro run: state=%s err=%v", rs, err)
		}

		// Amending request.retro MUST stop the live RoundRetro run.
		l.report("tok-coord", "plan amend", `{"why":"change retro model","tasks":[{"id":"T1","request":{"retro":[{"cli":"claude","model":"opus","effort":"high"}]}}]}`, "")
		if rs, err := termState(ctx, l.client, retroID); err != nil || rs != flow.RunGone {
			t.Fatalf("amending request.retro did not stop live RoundRetro run: state=%s err=%v", rs, err)
		}
		if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.RunID != "" || rv.Run == nil || rv.Run.Role != flow.RoleRetro {
			t.Fatalf("after retro request amend: Review=%+v, want stopped RoleRetro", rv)
		}
	})

	t.Run("answerAskLocked delivers RoleRetro ask to retro terminal, not worker", func(t *testing.T) {
		l := newLanding(t).withTokens()
		ctx := context.Background()
		retroID, err := startRun(ctx, l.client, world.RunSpec{
			Name: "rev-t1-ask", Cwd: l.wt, Argv: []string{"sh"},
			Labels: map[string]string{"sheepdog.run": "run-retro-ask"},
		})
		if err != nil {
			t.Fatal(err)
		}
		l.srv.SetStatus(retroID, &hc.Status{App: "claude-code", State: "idle"})
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle}
			a.Review = &flow.ReviewRun{
				RunID: retroID, Round: 4, Kind: flow.RoundRetro, Head: l.c1, Code: "retcode123",
				Run: &flow.RunRecord{ID: "run-retro-ask", Address: retroID, Role: flow.RoleRetro, CLI: "claude", State: flow.PhaseIdle},
			}
			s.Asks = []flow.Ask{{
				ID:        "ask-retro-1",
				Project:   l.id,
				Kind:      flow.KindDecision,
				Task:      "T1",
				Question:  "Should retro inspect generated fixtures too?",
				Options:   []string{"Yes", "No"},
				Recommend: "No",
				Status:    flow.AskStatusOpen,
				AskedBy:   flow.RoleRetro,
				RunID:     retroID,
			}}
		})

		l.report("tok-coord", "answer", `{"ask":"ask-retro-1","option":"No","why":"source files only"}`, "")

		if got := sendsTo(t, l.srv.Requests(), l.worker); len(got) != 0 {
			t.Fatalf("RoleRetro ask answer leaked to worker: %q", got)
		}
		if got := sendsTo(t, l.srv.Requests(), retroID); len(got) != 1 || !strings.Contains(got[0], "Answer to ask-retro-1: No") {
			t.Fatalf("RoleRetro ask answer sends to retro = %q, want 1 delivery", got)
		}
	})
}

// TestFactoryRetroScriptLaunchModeImplemented checks PR 12.2 B2 (plan.md 447, 485):
// scripts/e2e/factory-retro.sh --mode launch must be implemented (no TODO(12.2)
// stub exiting 2), configure models.retro with agy/gemini-4-carbon-high, and
// assert the 12.2 launch -> "In retro" -> M4 N25/N6 path.
func TestFactoryRetroScriptRecapAndShipModesImplemented(t *testing.T) {
	// 12.3 replaced --mode regression and --mode launch with --mode recap and --mode ship.
	scriptPath := filepath.Join("..", "..", "scripts", "e2e", "factory-retro.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	src := string(raw)
	for _, stub := range []string{"TODO(12.2)", "TODO(12.3)"} {
		if strings.Contains(src, stub) {
			t.Errorf("%s still contains %s stub", scriptPath, stub)
		}
	}
	for _, want := range []string{"start_retro", "In retro", "open_recap@review", "N22"} {
		if !strings.Contains(src, want) {
			t.Errorf("%s missing %q required for --mode recap and --mode ship", scriptPath, want)
		}
	}
	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		return
	}
	for _, mode := range []string{"recap", "ship"} {
		out, runErr := exec.Command(zshPath, scriptPath, "--mode", mode, t.TempDir()).CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("factory-retro.sh --mode %s on an empty dir: err=%v out=%s", mode, runErr, out)
		}
		if exitErr.ExitCode() == 2 || strings.Contains(string(out), "not implemented") {
			t.Errorf("--mode %s is not implemented: exit %d out=%s", mode, exitErr.ExitCode(), out)
		}
	}
	out, runErr := exec.Command(zshPath, scriptPath, "--mode", "launch", t.TempDir()).CombinedOutput()
	if runErr == nil || !strings.Contains(string(out), "Unknown mode") {
		t.Errorf("--mode launch should be refused as unknown after 12.3: err=%v out=%s", runErr, out)
	}
}

// TestRetroStartDoesNotAdoptLiveReviewerTerminal checks the defense-in-depth
// cross-role terminal adoption guard flagged in both R1 reviews: when a live
// terminal with the same round name carries sheepdog.role=reviewer, N28's
// AskStartRetro must not adopt it as a RoundRetro run.
func TestRetroStartDoesNotAdoptLiveReviewerTerminal(t *testing.T) {
	r, _, _ := setupRetroRig(t, flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"})
	staleRevAddr := "term-stale-reviewer-r4"
	r.srv.Add(hc.Terminal{
		Address: staleRevAddr,
		Name:    reviewerName(r.id, "T1", 1, 4),
		State:   "live",
		Argv:    world.AgyArgv(agyReviewModel, "", ""),
		Labels: map[string]string{
			"sheepdog.project": r.id,
			"sheepdog.role":    flow.RoleReviewer,
			"sheepdog.task":    "T1",
			"sheepdog.model":   agyReviewModel,
			"sheepdog.effort":  "high",
		},
	})
	runsBefore := len(*r.runs)
	r.pass() // L11 -> N28 launches retro

	st := r.state()
	a := st.Tasks[0].Attempts[0]
	if a.Review == nil || a.Review.RunID == "" {
		t.Fatalf("expected active retro run, got Review=%+v", a.Review)
	}
	if a.Review.RunID == staleRevAddr {
		t.Fatalf("AskStartRetro adopted live sheepdog.role=reviewer terminal %s instead of launching a fresh retro terminal", staleRevAddr)
	}
	if len(*r.runs) != runsBefore+1 {
		t.Fatalf("runs = %d, want %d (fresh retro launch after rejecting opposite-role terminal)", len(*r.runs), runsBefore+1)
	}
}

// TestObserveReviewLockedNeverReadsCodexReportForRoundRetro checks the
// defense-in-depth Codex guard flagged in R1: a RoundRetro slot must never
// enter ReadCodexReport -> acceptVerdictLocked even if Reviewer.CLI is codex.
func TestObserveReviewLockedNeverReadsCodexReportForRoundRetro(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	codexAddr := "term-codex-retro-1"
	l.srv.Add(hc.Terminal{
		Address: codexAddr,
		Name:    reviewerName(l.id, "T1", 1, 1),
		State:   "live",
	})
	l.srv.SetScreen(codexAddr, "› Retro round 1\n\n• self-check misses: 0\n• deletable: none\n\n  <report>{\"code\":\"retcode123\",\"round\":1,\"head\":\""+l.c1+"\",\"verdict\":\"pass\",\"majors\":0}</report>\n")
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = nil
		a.Review = &flow.ReviewRun{
			RunID:    codexAddr,
			Round:    1,
			Kind:     flow.RoundRetro,
			Head:     l.c1,
			Code:     "retcode123",
			Reviewer: flow.Reviewer{CLI: world.CodexCLI, Model: "gpt-6.2-sol", Effort: "high"},
			Run:      &flow.RunRecord{Address: codexAddr, Role: flow.RoleRetro, CLI: world.CodexCLI, State: flow.PhaseIdle},
		}
	})

	st := l.state()
	facts := flow.Facts{Tip: l.c1, TipRel: flow.RelSame}
	l.eng.mu.Lock()
	_ = l.eng.observeReviewLocked(ctx, l.id, &st, 0, &facts, 0)
	l.eng.mu.Unlock()

	a := st.Tasks[0].Attempts[0]
	if len(a.Rounds) != 0 || a.Review == nil {
		t.Fatalf("observeReviewLocked read Codex <report> for RoundRetro and recorded reviewer verdict: rounds=%+v review=%+v", a.Rounds, a.Review)
	}
}

// TestFactoryRetroScriptMatchesRealRecapWakeKey checks Neon high major 2:
// scripts/e2e/factory-retro.sh's state.json recap-wake matcher must match the
// real N6 wake key produced by flow/rows.go (wake_coordinator@recap@1) and
// wake.go:holdWake (Key: "recap@1").
func TestFactoryRetroScriptMatchesRealRecapWakeKey(t *testing.T) {
	pyPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found")
	}
	scriptPath := filepath.Join("..", "..", "scripts", "e2e", "factory-retro.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	_, afterStart, ok := strings.Cut(string(raw), "WAKE_TEXT=$(python3 - \"$J\" \"$TID\" <<'PY'\n")
	if !ok {
		t.Fatalf("could not locate WAKE_TEXT python matcher in %s", scriptPath)
	}
	pySrc, _, ok := strings.Cut(afterStart, "\nPY\n")
	if !ok {
		t.Fatalf("could not locate end of WAKE_TEXT python matcher in %s", scriptPath)
	}

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	st := flow.State{
		Project:  flow.Project{ID: "proj-test"},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: retro recap",
			Attempts: []flow.Attempt{{
				BaseCommit: "b0", PR: 7,
				Ready:  []flow.Ready{{Head: "c1"}},
				Rounds: []flow.Round{{N: 5, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictFix, Majors: 1, ReportPath: "/tmp/reports/T1-r5.md"}},
			}},
			Retros: []flow.Retro{{
				Attempt: 1, Round: 5, Cause: "wrong-seam",
				WorkerChange: "fix", WorkerChangeKind: flow.ChangeKindCheck,
				CoordinatorChange: "split task", CoordinatorChangeKind: flow.ChangeKindBrief,
				At: now,
			}},
			Recaps: []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}},
		}},
	}
	row, dec := flow.FirstMatch(flow.TaskRows, flow.In{Task: st.Tasks[0], Settings: st.Settings, Now: now})
	if row.ID != "N6" {
		t.Fatalf("FirstMatch = %q, want N6", row.ID)
	}
	var wakeAsk string
	for _, ask := range dec.Asked {
		if strings.HasPrefix(ask, flow.AskWake+"@") {
			wakeAsk = ask
			break
		}
	}
	if wakeAsk == "" {
		t.Fatalf("N6 Asked = %v, missing %s ask", dec.Asked, flow.AskWake)
	}
	holdWake(&st, 0, row.ID, dec.Why, wakeAsk, flow.Facts{}, "", now)
	if len(st.Project.Wakes) != 1 {
		t.Fatalf("held wakes = %+v, want 1", st.Project.Wakes)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(statePath, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	cmd := exec.Command(pyPath, "-", statePath, "T1")
	cmd.Stdin = strings.NewReader(pySrc)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running extracted matcher: %v\nout=%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != st.Project.Wakes[0].Facts {
		t.Fatalf("factory-retro.sh matcher returned %q for real N6 wake key %q, want facts %q", got, st.Project.Wakes[0].Key, st.Project.Wakes[0].Facts)
	}
}
