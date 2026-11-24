package factory

// PR 12.3, TDD step 1: failing tests for DefaultModels[RoleRetro] (R3),
// N22 bounded FIX size_cap tightening (R5/F1.2), L6 retro SHIP merge when
// PassingCovering is empty (KB-RET-2), KB-RET-9 unrelated CI recap surviving
// SHIP, KB-RET-11 design task SHIP still hitting N31, recap wake facts retro
// line (KB-RET-8), and the R2 L3/L5 end-to-end engine histories (plan.md 12.3,
// design.md 4.4 N22, 4.6 L2–L12/M4, 4.7 N31/N42, 9.1, 10).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestDefaultModelsRoleRetroNoReviewerFallback checks plan.md 12.3 R3 and
// design.md 9.1: DefaultModels[RoleRetro] is exactly claude/opus/high with no
// fallback to settings.Models[RoleReviewer] or DefaultModels[RoleReviewer].
func TestDefaultModelsRoleRetroNoReviewerFallback(t *testing.T) {
	want := []flow.Candidate{{CLI: "claude", Model: "opus", Effort: "high"}}

	def := flow.DefaultSettings()
	if got := flow.DefaultModels(def)[flow.RoleRetro]; !slices.Equal(got, want) {
		t.Fatalf("DefaultModels(default)[RoleRetro] = %+v, want %+v", got, want)
	}
	def.SecondReviewer = "codex"
	if got := flow.DefaultModels(def)[flow.RoleRetro]; !slices.Equal(got, want) {
		t.Fatalf("DefaultModels(second_reviewer=codex)[RoleRetro] = %+v, want %+v", got, want)
	}

	// Custom settings.Models[RoleReviewer] must never leak into Candidates(RoleRetro).
	s := flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{
		flow.RoleReviewer: {{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"}},
	}
	if got := flow.Candidates(flow.RoleRetro, flow.Task{}, s); !slices.Equal(got, want) {
		t.Fatalf("Candidates(RoleRetro) with custom reviewer list = %+v, want %+v (no reviewer fallback)", got, want)
	}

	ok := flow.CLIFacts{Installed: true, SignedIn: true}
	facts := flow.ResolveFacts{
		CLIs:   map[string]flow.CLIFacts{"claude": ok, "codex": ok, "agy": ok},
		Worker: flow.Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"},
	}
	in, why := flow.Resolve(flow.RoleRetro, flow.Task{}, s, facts)
	wantIntent := flow.Intent{
		Role:     flow.RoleRetro,
		CLI:      "claude",
		Model:    "opus",
		Effort:   "high",
		Perm:     flow.PermReadOnly,
		Continue: flow.ContinueFresh,
	}
	if why != "" || in != wantIntent {
		t.Fatalf("Resolve(RoleRetro) = (%+v, %q), want (%+v, \"\")", in, why, wantIntent)
	}

	// When the worker already runs claude/opus, RoleRetro refuses rather than
	// falling back to RoleReviewer's claude/sonnet or custom reviewer list.
	facts.Worker = flow.Candidate{CLI: "claude", Model: "opus", Effort: "medium"}
	if _, why := flow.Resolve(flow.RoleRetro, flow.Task{}, s, facts); why != "the reviewer must differ from the worker" {
		t.Fatalf("Resolve(RoleRetro) with opus worker = %q, want %q (no reviewer fallback)", why, "the reviewer must differ from the worker")
	}
}

// TestN22RetroFixTightensSizeCapAndTellsWorker checks plan.md 12.3 R5 (table-audit
// F1.2), design.md 4.4 N22, and KB-RET-4: N22 after a retro FIX sets
// a.SizeCap = lines + continue_lines even when lower (and when higher), and
// tells the worker the retro's majors/report and WorkerChange without offering
// `--reject`.
func TestN22RetroFixTightensSizeCapAndTellsWorker(t *testing.T) {
	t.Run("tightens size_cap even when lower (R5/F1.2)", func(t *testing.T) {
		l := newLanding(t).withTokens()
		reportPath := l.paths.Report(l.id, "T1", 4)
		now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.SizeCap = 200 // newLanding has 1 changed line; 1 + 60 = 61 < 200
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: l.c1, Verdict: flow.VerdictFix, Majors: 2, ReportPath: reportPath},
			}
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "patch-shaped-fix",
				WorkerChange:          "handle all negative inputs in one branch",
				WorkerChangeKind:      flow.ChangeKindCheck,
				CoordinatorChange:     "list negative cases in outcomes",
				CoordinatorChangeKind: flow.ChangeKindBrief,
				At:                    now,
			}}
		})

		l.pass() // L4 -> N22 -> execTellFindings (RoundRetro branch)

		st := l.state()
		if st.Tasks[0].LastRow != "N22" {
			t.Fatalf("LastRow = %q, want N22", st.Tasks[0].LastRow)
		}
		wantCap := 1 + st.Settings.ContinueLines
		if gotCap := st.Tasks[0].Attempts[0].SizeCap; gotCap != wantCap {
			t.Fatalf("a.SizeCap after retro FIX N22 = %d, want %d (lines=1 + continue_lines=%d, tightened from 200)",
				gotCap, wantCap, st.Settings.ContinueLines)
		}
		sends := sendsTo(t, l.srv.Requests(), l.worker)
		if len(sends) != 1 {
			t.Fatalf("worker sends = %d (%q), want 1", len(sends), sends)
		}
		for _, want := range []string{reportPath, "handle all negative inputs in one branch", "61"} {
			if !strings.Contains(sends[0], want) {
				t.Errorf("N22 retro fix message missing %q:\n%s", want, sends[0])
			}
		}
		if strings.Contains(sends[0], "--reject") {
			t.Errorf("N22 retro fix message must not offer --reject:\n%s", sends[0])
		}
	})

	t.Run("sets size_cap when lines + continue_lines is higher", func(t *testing.T) {
		l := newLanding(t).withTokens()
		if err := os.WriteFile(filepath.Join(l.wt, "extra.go"), []byte(strings.Repeat("// extra line\n", 40)), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, l.wt, "add", "extra.go")
		gitIn(t, l.wt, "commit", "-m", "feat: add 40 lines")
		c2 := gitIn(t, l.wt, "rev-parse", "HEAD")
		gitIn(t, l.wt, "push", "origin", c2+":refs/heads/"+l.branch)
		writePRJSON(t, l.gh, "view.json", c2, greenRollup)

		reportPath := l.paths.Report(l.id, "T1", 4)
		now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.SizeCap = 50 // 41 <= 50 so N12 does not fire; 41 + 60 = 101 > 50
			a.Ready = append(a.Ready, flow.Ready{Head: c2})
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: c2, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: c2, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: c2, Verdict: flow.VerdictFix, Majors: 1, ReportPath: reportPath},
			}
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "patch-shaped-fix",
				WorkerChange:          "fix zero case",
				WorkerChangeKind:      flow.ChangeKindCheck,
				CoordinatorChange:     "state zero case",
				CoordinatorChangeKind: flow.ChangeKindBrief,
				At:                    now,
			}}
		})

		l.pass()

		st := l.state()
		if st.Tasks[0].LastRow != "N22" {
			t.Fatalf("LastRow = %q, want N22", st.Tasks[0].LastRow)
		}
		wantCap := 41 + st.Settings.ContinueLines
		if gotCap := st.Tasks[0].Attempts[0].SizeCap; gotCap != wantCap {
			t.Fatalf("a.SizeCap after retro FIX N22 = %d, want %d (lines=41 + continue_lines=%d)",
				gotCap, wantCap, st.Settings.ContinueLines)
		}
	})
}

// TestL6RetroShipMergesWhenPassingCoveringEmpty checks plan.md 12.3, design.md
// 4.6 L6, land.go:recordAccept, and KB-RET-2: when PassingCovering is empty and
// the newest covering round is a retro SHIP, recordAccept records that retro
// round in Accept.Rounds and N42 merges the PR pinned to H.
func TestL6RetroShipMergesWhenPassingCoveringEmpty(t *testing.T) {
	t.Run("recordAccept records L6 retro SHIP round when PassingCovering is empty", func(t *testing.T) {
		a := flow.Attempt{
			BaseCommit: "b0",
			Ready:      []flow.Ready{{Head: "c1"}},
			Rounds: []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictShip, Majors: 0},
			},
		}
		if got := flow.PassingCovering(a, "c1"); len(got) != 0 {
			t.Fatalf("precondition failed: PassingCovering = %+v, want empty", got)
		}
		msg := recordAccept(flow.Task{ID: "T1"}, &a, flow.DefaultSettings(), flow.Facts{}, "c1", false)
		if len(a.Accepts) != 1 || a.Accepts[0].Head != "c1" || a.Accepts[0].By != "bar" || !slices.Equal(a.Accepts[0].Rounds, []int{4}) {
			t.Fatalf("recordAccept L6 SHIP = %+v (msg %q), want Accept{Head:c1 By:bar Rounds:[4]}", a.Accepts, msg)
		}
		if !strings.Contains(msg, "4") || strings.Contains(msg, "rounds []") {
			t.Errorf("recordAccept L6 SHIP msg = %q, want round 4 (not empty rounds)", msg)
		}
	})

	t.Run("report retro ship -> L6 -> N42 merges PR with fake gh (KB-RET-2)", func(t *testing.T) {
		l := newLanding(t).withTokens()
		ctx := context.Background()
		retroID, err := startRun(ctx, l.client, world.RunSpec{
			Name: "retro-t1-4", Cwd: l.wt, Argv: []string{"sh"},
			Labels: map[string]string{"sheepdog.run": "run-retro-ship", "sheepdog.role": flow.RoleRetro},
		})
		if err != nil {
			t.Fatal(err)
		}
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
			}
			a.Review = &flow.ReviewRun{
				RunID:        retroID,
				RunTokenHash: tokenHash("tok-retro"),
				Run:          &flow.RunRecord{ID: "run-retro-ship", Address: retroID, Role: flow.RoleRetro, CLI: "claude", Model: "opus", State: flow.PhaseWorking},
				Round:        4,
				Kind:         flow.RoundRetro,
				Head:         l.c1,
				Code:         "retship123",
				Reviewer:     flow.Reviewer{CLI: "claude", Model: "opus", Effort: "high"},
			}
		})

		if err := l.eng.Report("tok-retro", "retro", validRetroPayload(t, flow.VerdictShip, "retship123", l.c1, "")); err != nil {
			t.Fatalf("report retro ship: %v", err)
		}
		if got := flow.PassingCovering(l.state().Tasks[0].Attempts[0], l.c1); len(got) != 0 {
			t.Fatalf("PassingCovering after retro SHIP = %+v, want empty", got)
		}

		l.pass() // observeReviewLocked stops retroID; L6 -> N42 accepts and merges

		st := l.state()
		a := st.Tasks[0].Attempts[0]
		if st.Tasks[0].LastRow != "N42" {
			t.Fatalf("LastRow = %q, want N42", st.Tasks[0].LastRow)
		}
		if len(a.Accepts) != 1 || a.Accepts[0].Head != l.c1 || a.Accepts[0].By != "bar" || !slices.Equal(a.Accepts[0].Rounds, []int{4}) {
			t.Fatalf("Accepts after L6 SHIP = %+v, want [Accept{Head:%s By:bar Rounds:[4]}]", a.Accepts, l.c1)
		}
		merges := ghCalls(t, l.gh, "pr merge")
		if len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+l.c1) {
			t.Fatalf("gh pr merge calls = %q, want 1 merge pinned to %s", merges, l.c1)
		}

		l.writeMerged()
		l.pass() // N2 marks done
		if d := l.state().Tasks[0].Done; d == nil || d.MergedCommit != mergedSHA {
			t.Fatalf("after merged snapshot: Done = %+v, want %s", d, mergedSHA)
		}
	})
}

// TestRetroShipKBRET9CIRecapSurvivesAndKBRET11DesignGateHolds checks plan.md
// 12.3's KB-RET-9 and KB-RET-11 end-to-end engine requirements:
//   - KB-RET-9: an unrelated open CI recap survives a retro SHIP (N6 holds,
//     no merge, wake facts include the retro line).
//   - KB-RET-11: a design task with design_gate=owner that receives retro SHIP
//     still holds at N31 until the owner approves Merge, then merges via N42
//     with the retro round in Accept.Rounds.
func TestRetroShipKBRET9CIRecapSurvivesAndKBRET11DesignGateHolds(t *testing.T) {
	t.Run("KB-RET-9 unrelated open CI recap survives retro SHIP", func(t *testing.T) {
		l := newLanding(t).withTokens()
		reportPath := l.paths.Report(l.id, "T1", 4)
		now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: l.c1, Verdict: flow.VerdictShip, Majors: 0, ReportPath: reportPath},
			}
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "none",
				WorkerChange:          "keep fast tests",
				WorkerChangeKind:      flow.ChangeKindCheck,
				CoordinatorChange:     "keep CI timeout guard",
				CoordinatorChangeKind: flow.ChangeKindCheck,
				At:                    now,
			}}
			s.Tasks[0].Recaps = []flow.Recap{{
				Trigger: flow.TriggerCI,
				Allowed: []string{"split", "new", "drop"},
			}}
		})

		l.pass()

		st := l.state()
		if st.Tasks[0].LastRow != "N6" {
			t.Fatalf("LastRow = %q, want N6 (open CI recap survives retro SHIP)", st.Tasks[0].LastRow)
		}
		if rc := flow.OpenRecap(st.Tasks[0]); rc == nil || rc.Trigger != flow.TriggerCI {
			t.Fatalf("open CI recap cleared by SHIP: %+v", rc)
		}
		if len(st.Tasks[0].Attempts[0].Accepts) != 0 || len(ghCalls(t, l.gh, "pr merge")) != 0 {
			t.Fatalf("merged despite open CI recap: accepts=%+v merges=%q", st.Tasks[0].Attempts[0].Accepts, ghCalls(t, l.gh, "pr merge"))
		}
		if len(st.Project.Wakes) != 1 {
			t.Fatalf("held wakes = %+v, want 1 N6 recap wake", st.Project.Wakes)
		}
		_, afterRecap, ok := strings.Cut(st.Project.Wakes[0].Facts, "; recap trigger ")
		if !ok {
			t.Fatalf("wake facts missing recap segment: %s", st.Project.Wakes[0].Facts)
		}
		for _, want := range []string{flow.VerdictShip, "none", "keep CI timeout guard", flow.ChangeKindCheck, reportPath} {
			if !strings.Contains(afterRecap, want) {
				t.Errorf("CI recap wake facts missing retro token %q:\n%s", want, st.Project.Wakes[0].Facts)
			}
		}
	})

	t.Run("KB-RET-11 design task retro SHIP still hits N31 then merges on owner Merge", func(t *testing.T) {
		l := newLanding(t).withTokens()
		l.edit(func(s *flow.State) {
			s.Settings.DesignGate = flow.GateOwner
			s.Tasks[0].Kind = flow.TaskKindDesign
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
			}
			a.Review = &flow.ReviewRun{
				RunID:        "term-retro-design",
				RunTokenHash: tokenHash("tok-retro-design"),
				Run:          &flow.RunRecord{ID: "run-retro-design", Address: "term-retro-design", Role: flow.RoleRetro, CLI: "claude", Model: "opus", State: flow.PhaseWorking},
				Round:        4,
				Kind:         flow.RoundRetro,
				Head:         l.c1,
				Code:         "retdes1234",
				Reviewer:     flow.Reviewer{CLI: "claude", Model: "opus", Effort: "high"},
			}
		})

		if err := l.eng.Report("tok-retro-design", "retro", validRetroPayload(t, flow.VerdictShip, "retdes1234", l.c1, "")); err != nil {
			t.Fatalf("report retro ship on design task: %v", err)
		}

		// Pass 1: L6 passes ladder, N31 opens owner gate ask and does not merge.
		l.pass()
		st := l.state()
		if st.Tasks[0].LastRow != "N31" {
			t.Fatalf("design task retro SHIP LastRow = %q, want N31", st.Tasks[0].LastRow)
		}
		asks := openAsksForTask(st, "T1")
		if len(asks) != 1 || asks[0].Kind != flow.KindGate || asks[0].AskedBy != flow.AskedByN31 || asks[0].Head != l.c1 {
			t.Fatalf("open gate asks = %+v, want 1 N31 gate ask at %s", asks, l.c1)
		}
		if len(st.Tasks[0].Attempts[0].Accepts) != 0 || len(ghCalls(t, l.gh, "pr merge")) != 0 {
			t.Fatalf("design task merged before N31 owner Merge")
		}

		// Owner approves Merge -> Pass 2 merges via N42 with retro round 4 accepted.
		answerOwnerAsk(t, l.eng, asks[0].ID, flow.GateOptionMerge)
		l.pass()
		st = l.state()
		a := st.Tasks[0].Attempts[0]
		if st.Tasks[0].LastRow != "N42" {
			t.Fatalf("after owner Merge: LastRow = %q, want N42", st.Tasks[0].LastRow)
		}
		if len(a.Accepts) != 1 || !slices.Equal(a.Accepts[0].Rounds, []int{4}) {
			t.Fatalf("after owner Merge: Accepts = %+v, want round [4]", a.Accepts)
		}
		if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+l.c1) {
			t.Fatalf("after owner Merge: merges = %q, want 1 pinned to %s", merges, l.c1)
		}
	})
}

// TestRecapWakeFactsCarryRetroLineKBRET8 checks plan.md 12.3, wake.go:taskFacts,
// and KB-RET-8: on an open recap, taskFacts appends one retro line carrying the
// latest retro's verdict, cause, coordinator change + kind, and report path, and
// N6 delivers that line in the coordinator's wake message.
func TestRecapWakeFactsCarryRetroLineKBRET8(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	t.Run("taskFacts appends one retro line on OpenRecap (REDESIGN, brief kind)", func(t *testing.T) {
		reportPath := "/tmp/proj/reports/T1-r4.md"
		task := flow.Task{
			ID:    "T1",
			Title: "feat: retro recap",
			Attempts: []flow.Attempt{{
				BaseCommit: "b0",
				PR:         7,
				Ready:      []flow.Ready{{Head: "c1"}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
					{N: 2, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
					{N: 3, Kind: flow.RoundDelta, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
					{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictRedesign, Majors: 1, ReportPath: reportPath},
				},
			}},
			Retros: []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "wrong-seam",
				WorkerChange:          "split parser from runner",
				WorkerChangeKind:      flow.ChangeKindCheck,
				CoordinatorChange:     "require seam check in plan",
				CoordinatorChangeKind: flow.ChangeKindBrief,
				At:                    now,
			}},
			Recaps: []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}},
		}

		got := taskFacts(task, flow.Facts{PR: flow.PR{Number: 7, Checks: flow.ChecksGreen}}, now)
		_, afterRecap, ok := strings.Cut(got, "; recap trigger ")
		if !ok {
			t.Fatalf("taskFacts missing recap trigger segment: %s", got)
		}
		for _, want := range []string{flow.VerdictRedesign, "wrong-seam", "require seam check in plan", flow.ChangeKindBrief, reportPath} {
			if !strings.Contains(afterRecap, want) {
				t.Errorf("recap wake facts missing %q after recap trigger:\n%s", want, got)
			}
		}
	})

	t.Run("taskFacts carries only the newest retro when attempt has 2 retros (check kind)", func(t *testing.T) {
		r4Path := "/tmp/proj/reports/T1-r4.md"
		r5Path := "/tmp/proj/reports/T1-r5.md"
		task := flow.Task{
			ID:    "T1",
			Title: "feat: two retros",
			Attempts: []flow.Attempt{{
				BaseCommit: "b0",
				PR:         7,
				Ready:      []flow.Ready{{Head: "c1"}, {Head: "c2"}},
				Rounds: []flow.Round{
					{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictFix, Majors: 1, ReportPath: r4Path},
					{N: 5, Kind: flow.RoundRetro, Head: "c2", Verdict: flow.VerdictFix, Majors: 1, ReportPath: r5Path},
				},
			}},
			Retros: []flow.Retro{
				{
					Attempt: 1, Round: 4, Cause: "patch-shaped-fix",
					WorkerChange: "old worker change", WorkerChangeKind: flow.ChangeKindBrief,
					CoordinatorChange: "old coordinator change", CoordinatorChangeKind: flow.ChangeKindBrief,
					At: now.Add(-10 * time.Minute),
				},
				{
					Attempt: 1, Round: 5, Cause: "task-too-big",
					WorkerChange: "handle zero", WorkerChangeKind: flow.ChangeKindCheck,
					CoordinatorChange: "split edge cases with table test", CoordinatorChangeKind: flow.ChangeKindCheck,
					At: now,
				},
			},
			Recaps: []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}},
		}

		got := taskFacts(task, flow.Facts{PR: flow.PR{Number: 7, Checks: flow.ChecksGreen}}, now)
		_, afterRecap, ok := strings.Cut(got, "; recap trigger ")
		if !ok {
			t.Fatalf("taskFacts missing recap trigger segment: %s", got)
		}
		for _, want := range []string{flow.VerdictFix, "task-too-big", "split edge cases with table test", flow.ChangeKindCheck, r5Path} {
			if !strings.Contains(afterRecap, want) {
				t.Errorf("recap wake facts missing latest retro field %q:\n%s", want, got)
			}
		}
		if strings.Contains(afterRecap, r4Path) || strings.Contains(afterRecap, "old coordinator change") {
			t.Errorf("recap wake facts should carry one retro line (latest only), not older round 4:\n%s", got)
		}
	})

	t.Run("taskFacts omits retro line without OpenRecap or without Retros", func(t *testing.T) {
		closedTask := flow.Task{
			ID:    "T1",
			Title: "feat: closed recap",
			Attempts: []flow.Attempt{{
				BaseCommit: "b0",
				Ready:      []flow.Ready{{Head: "c1"}},
				Rounds:     []flow.Round{{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictRedesign, ReportPath: "/tmp/proj/reports/T1-r4.md"}},
			}},
			Retros: []flow.Retro{{Attempt: 1, Round: 4, Cause: "wrong-seam", CoordinatorChange: "secret-coord-change", CoordinatorChangeKind: flow.ChangeKindCheck, At: now}},
			Recaps: []flow.Recap{{Trigger: flow.TriggerReview, Decided: &flow.RecapDecision{Choice: "drop", Why: "done", At: now}}},
		}
		if got := taskFacts(closedTask, flow.Facts{}, now); strings.Contains(got, "wrong-seam") || strings.Contains(got, "secret-coord-change") {
			t.Errorf("closed recap taskFacts unexpectedly carried retro line:\n%s", got)
		}

		noRetroTask := flow.Task{
			ID:     "T1",
			Title:  "feat: no retro",
			Recaps: []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}},
		}
		got := taskFacts(noRetroTask, flow.Facts{}, now)
		if _, afterRecap, ok := strings.Cut(got, "; recap trigger "); !ok || strings.Contains(strings.ToLower(afterRecap), "retro") {
			t.Errorf("no-retro recap taskFacts unexpectedly mentioned retro:\n%s", got)
		}
	})

	t.Run("N6 delivers retro line in coordinator wake message (KB-RET-8)", func(t *testing.T) {
		l := newLanding(t)
		pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
		l.srv.Finish("coord-elsewhere", "exited")
		l.srv.Add(hc.Terminal{
			Address:  "coord1",
			Name:     "coord",
			State:    "live",
			Labels:   testCoordLabels(l.id, "run-coord1"),
			Status:   &hc.Status{App: "claude-code", State: "idle"},
			StatusMS: &pastMS,
		})
		reportPath := l.paths.Report(l.id, "T1", 4)
		l.edit(func(s *flow.State) {
			s.Project.RunID = "coord1"
			s.Tasks[0].Attempts[0].Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: l.c1, Verdict: flow.VerdictRedesign, Majors: 1, ReportPath: reportPath},
			}
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt:               1,
				Round:                 4,
				Cause:                 "wrong-seam",
				WorkerChange:          "rework seam",
				WorkerChangeKind:      flow.ChangeKindBrief,
				CoordinatorChange:     "require seam check in plan",
				CoordinatorChangeKind: flow.ChangeKindCheck,
				At:                    now,
			}}
			s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}}
		})

		l.pass() // N6 queues wake, J12 delivers it to idle coord1

		sends := sendsTo(t, l.srv.Requests(), "coord1")
		if len(sends) != 1 {
			t.Fatalf("coordinator wake sends = %d (%q), want 1", len(sends), sends)
		}
		_, afterRecap, ok := strings.Cut(sends[0], "; recap trigger ")
		if !ok {
			t.Fatalf("coordinator wake missing recap trigger:\n%s", sends[0])
		}
		for _, want := range []string{"T1: N6:", flow.VerdictRedesign, "wrong-seam", "require seam check in plan", flow.ChangeKindCheck, reportPath} {
			if !strings.Contains(sends[0], want) {
				t.Errorf("coordinator wake missing %q:\n%s", want, sends[0])
			}
		}
		if !strings.Contains(afterRecap, flow.ChangeKindCheck) {
			t.Errorf("coordinator wake retro line missing change kind %q:\n%s", flow.ChangeKindCheck, sends[0])
		}
	})
}

// TestRetroHistoriesL3AndL5Engine checks the R2 L3/L5 histories end-to-end
// through Engine (hctest + fake gh + real report retro):
//  1. L11 -> FIX -> N22 (size_cap tightened) -> ready c2 -> N26 -> L5 delta
//     retro -> 2nd FIX -> L3/N25 recap with no continue even when
//     continues_per_task > 0 -> N6 wake carries round 5 retro line.
//  2. L11 -> FIX -> N22 -> ready c2 -> N26 -> L5 delta retro fails out
//     (RetroUnavailable) -> L5 skips -> L3/N25 recap with no continue -> N6
//     wake carries round 4 retro line.
//  3. L11 -> FIX -> N22 (size_cap tightened) -> worker exceeds tightened cap ->
//     N12 opens size recap with no continue (SizeRecapAllowed FIX guard, R2).
//  4. L11 -> FIX -> N22 -> ready c2 -> N26 -> L5 delta retro -> SHIP ->
//     L6/N42 merges PR with empty PassingCovering and Accept.Rounds == [5].
func TestRetroHistoriesL3AndL5Engine(t *testing.T) {
	retroCand := flow.Candidate{CLI: world.AgyCLI, Model: agyReviewModel, Effort: "high"}

	// runInitialFix runs setupRetroRig through L11/N28 -> report retro fix ->
	// L4/N22 (tightening SizeCap), returning the rig, worker token, worker ID,
	// coordinator token, and worktree path.
	runInitialFix := func(t *testing.T, edit func(*flow.State)) (*agyRig, string, string, string, string) {
		t.Helper()
		r, c1, workerTok := setupRetroRig(t, retroCand)
		coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
		st0 := r.state()
		workerID := st0.Tasks[0].Attempts[0].RunID
		wt := st0.Tasks[0].Attempts[0].Worktree
		r.edit(func(s *flow.State) {
			s.Tasks[0].Attempts[0].SizeCap = 200
			if edit != nil {
				edit(s)
			}
		})

		// Move worker status toward idle while launching round 4 retro (L11 -> N28).
		r.srv.SetStatus(workerID, &hc.Status{App: "antigravity", State: "idle"})
		*r.now = r.now.Add(5 * time.Second)
		r.pass() // L11 -> N28 launches round 4 retro

		st := r.state()
		if st.Tasks[0].LastRow != "N28" || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.Round != 4 {
			t.Fatalf("L11 initial retro did not start: LastRow=%q review=%+v", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
		}
		retroTok := r.last().Env["SHEEPDOG_RUN_TOKEN"]
		code := st.Tasks[0].Attempts[0].Review.Code

		if err := r.eng.Report(retroTok, "retro", validRetroPayload(t, flow.VerdictFix, code, c1, "major: negative n returns wrong value\n")); err != nil {
			t.Fatalf("round 4 report retro fix: %v", err)
		}

		// Advance past idle grace so worker is idle; pass stops retro 4 and runs L4 -> N22.
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		st = r.state()
		if st.Tasks[0].LastRow != "N22" {
			t.Fatalf("after round 4 FIX: LastRow = %q, want N22", st.Tasks[0].LastRow)
		}
		wantCap := 2 + st.Settings.ContinueLines // setupRetroRig wrote 2-line calc.go
		if gotCap := st.Tasks[0].Attempts[0].SizeCap; gotCap != wantCap {
			t.Fatalf("after round 4 FIX N22: SizeCap = %d, want %d (tightened from 200)", gotCap, wantCap)
		}
		return r, workerTok, workerID, coordTok, wt
	}

	commitFixAndPush := func(t *testing.T, r *agyRig, workerTok, wt, code string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt, "calc.go"), []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt, "add", "calc.go")
		gitIn(t, wt, "commit", "-m", "fix: bounded retro change")
		c2 := gitIn(t, wt, "rev-parse", "HEAD")
		if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c2+`","proofs":[{"outcome":"calc exists","artifact":"calc.go"}],"touched":"calc.go","skipped":""}`)); err != nil {
			t.Fatalf("ready c2: %v", err)
		}
		*r.now = r.now.Add(5 * time.Second)
		r.pass() // N26 pushes c2
		writePRJSON(t, r.gh, "view.json", c2, greenRollup)
		return c2
	}

	t.Run("L3 second FIX opens review recap with no continue independent of continues_per_task", func(t *testing.T) {
		r, workerTok, _, _, wt := runInitialFix(t, func(s *flow.State) {
			s.Settings.ContinuesPerTask = 3
		})
		c2 := commitFixAndPush(t, r, workerTok, wt, "package demo\nfunc Calc(n int) int { if n < 0 { return -n }; return n }\n")

		*r.now = r.now.Add(25 * time.Second)
		r.pass() // L5 -> N28 launches round 5 delta retro at c2
		st := r.state()
		if st.Tasks[0].LastRow != "N28" || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.Round != 5 {
			t.Fatalf("L5 delta retro did not start: LastRow=%q review=%+v", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
		}
		retroTok2 := r.last().Env["SHEEPDOG_RUN_TOKEN"]
		code2 := st.Tasks[0].Attempts[0].Review.Code

		body2 := mustReviewPayload(t, map[string]any{
			"verdict":                 flow.VerdictFix,
			"code":                    code2,
			"head":                    c2,
			"cause":                   "wrong-seam",
			"worker_change":           "handle zero input",
			"worker_change_kind":      flow.ChangeKindCheck,
			"coordinator_change":      "split zero edge case into separate task",
			"coordinator_change_kind": flow.ChangeKindBrief,
			"findings":                "major: Calc(0) still wrong\n",
		})
		if err := r.eng.Report(retroTok2, "retro", body2); err != nil {
			t.Fatalf("round 5 report retro fix: %v", err)
		}

		r.pass() // L3 -> N25 opens review recap (no continue)
		st = r.state()
		if st.Tasks[0].LastRow != "N25" {
			t.Fatalf("after 2nd FIX: LastRow = %q, want N25 (L3)", st.Tasks[0].LastRow)
		}
		rc := flow.OpenRecap(st.Tasks[0])
		if rc == nil || rc.Trigger != flow.TriggerReview || !slices.Equal(rc.Allowed, []string{"split", "new", "drop"}) {
			t.Fatalf("L3 2nd FIX recap = %+v, want review recap [split new drop] (no continue despite ContinuesPerTask=3)", rc)
		}
		if len(st.Tasks[0].Retros) != 2 {
			t.Fatalf("len(t.Retros) = %d, want 2", len(st.Tasks[0].Retros))
		}

		r.pass() // N6 holds wake with round 5 retro line
		st = r.state()
		if len(st.Project.Wakes) == 0 {
			t.Fatalf("expected held N6 recap wake after L3")
		}
		wakeFacts := st.Project.Wakes[len(st.Project.Wakes)-1].Facts
		_, afterRecap, ok := strings.Cut(wakeFacts, "; recap trigger ")
		if !ok {
			t.Fatalf("N6 wake facts missing recap trigger: %s", wakeFacts)
		}
		for _, want := range []string{flow.VerdictFix, "wrong-seam", "split zero edge case into separate task", flow.ChangeKindBrief, r.paths.Report(r.id, "T1", 5)} {
			if !strings.Contains(afterRecap, want) {
				t.Errorf("N6 wake facts after L3 missing %q:\n%s", want, wakeFacts)
			}
		}
	})

	t.Run("L5 delta retro fails out to L3 recap with no continue (R2)", func(t *testing.T) {
		r, workerTok, _, _, wt := runInitialFix(t, func(s *flow.State) {
			s.Settings.ReviewerRetries = 0
			s.Settings.ContinuesPerTask = 2
		})
		_ = commitFixAndPush(t, r, workerTok, wt, "package demo\nfunc Calc(n int) int { if n < 0 { return -n }; return n }\n")

		*r.now = r.now.Add(25 * time.Second)
		r.pass() // L5 -> N28 launches round 5 delta retro
		st := r.state()
		if st.Tasks[0].LastRow != "N28" || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.Round != 5 {
			t.Fatalf("L5 delta retro did not start: LastRow=%q review=%+v", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
		}
		deltaID := st.Tasks[0].Attempts[0].Review.RunID

		// Delta retro goes idle with no report -> nudged once -> stopped EndStuck.
		r.srv.SetStatus(deltaID, &hc.Status{App: "antigravity", State: "done", Msg: "No report."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass() // nudge_retro@5@0@c2
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass() // stop_retro@5@0@c2

		// Retries exhausted (0) -> RetroUnavailable -> L5 skips -> L3 -> N25.
		r.pass()
		st = r.state()
		if st.Tasks[0].LastRow != "N25" {
			t.Fatalf("after failed delta retro: LastRow = %q, want N25 (L3)", st.Tasks[0].LastRow)
		}
		rc := flow.OpenRecap(st.Tasks[0])
		if rc == nil || rc.Trigger != flow.TriggerReview || !slices.Equal(rc.Allowed, []string{"split", "new", "drop"}) {
			t.Fatalf("failed delta recap = %+v, want review recap [split new drop] (no continue)", rc)
		}
		if len(st.Tasks[0].Retros) != 1 {
			t.Fatalf("failed delta retro spent a retro record: len(t.Retros) = %d, want 1", len(st.Tasks[0].Retros))
		}

		r.pass() // N6 holds wake carrying round 4 retro line
		st = r.state()
		if len(st.Project.Wakes) == 0 {
			t.Fatalf("expected held N6 recap wake after failed delta retro")
		}
		wakeFacts := st.Project.Wakes[len(st.Project.Wakes)-1].Facts
		_, afterRecap, ok := strings.Cut(wakeFacts, "; recap trigger ")
		if !ok {
			t.Fatalf("N6 wake facts missing recap trigger: %s", wakeFacts)
		}
		for _, want := range []string{flow.VerdictFix, "patch-shaped-fix", "add negative boundary outcomes to the brief", flow.ChangeKindBrief, r.paths.Report(r.id, "T1", 4)} {
			if !strings.Contains(afterRecap, want) {
				t.Errorf("N6 wake facts after failed delta missing round 4 retro field %q:\n%s", want, wakeFacts)
			}
		}
	})

	t.Run("R2 FIX size overrun opens N12 size recap refusing continue", func(t *testing.T) {
		r, _, _, coordTok, wt := runInitialFix(t, func(s *flow.State) {
			s.Settings.ContinuesPerTask = 2
		})
		// Worker writes 80 lines (> tightened SizeCap 62) -> N12 opens size recap with no continue.
		if err := os.WriteFile(filepath.Join(wt, "big.go"), []byte(strings.Repeat("// overrun\n", 80)), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt, "add", "big.go")
		gitIn(t, wt, "commit", "-m", "feat: exceed tightened retro cap")

		r.pass() // N12
		st := r.state()
		if st.Tasks[0].LastRow != "N12" {
			t.Fatalf("size overrun during retro FIX: LastRow = %q, want N12", st.Tasks[0].LastRow)
		}
		rc := flow.OpenRecap(st.Tasks[0])
		if rc == nil || rc.Trigger != flow.TriggerSize || !slices.Equal(rc.Allowed, []string{"split", "new", "drop"}) {
			t.Fatalf("size recap during retro FIX = %+v, want size recap [split new drop] (no continue)", rc)
		}
		err := r.eng.Report(coordTok, "recap decide", json.RawMessage(`{"task":"T1","choice":"continue","why":"try to continue during retro FIX"}`))
		if got := refusalCode(err); got != "not_allowed" {
			t.Fatalf("recap decide continue during retro FIX = %v (code %q), want not_allowed", err, got)
		}
	})

	t.Run("L5 delta retro SHIP merges via L6/N42 with empty PassingCovering", func(t *testing.T) {
		r, workerTok, _, _, wt := runInitialFix(t, nil)
		c2 := commitFixAndPush(t, r, workerTok, wt, "package demo\nfunc Calc(n int) int { if n < 0 { return -n }; return n }\n")

		*r.now = r.now.Add(25 * time.Second)
		r.pass() // worker -> PhaseIdle (R20); L5 -> N28 launches round 5 delta retro at c2
		st := r.state()
		if st.Tasks[0].LastRow != "N28" || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.Round != 5 {
			t.Fatalf("L5 delta retro did not start: LastRow=%q review=%+v", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
		}
		retroTok2 := r.last().Env["SHEEPDOG_RUN_TOKEN"]
		code2 := st.Tasks[0].Attempts[0].Review.Code

		if err := r.eng.Report(retroTok2, "retro", validRetroPayload(t, flow.VerdictShip, code2, c2, "")); err != nil {
			t.Fatalf("round 5 report retro ship: %v", err)
		}
		if got := flow.PassingCovering(r.state().Tasks[0].Attempts[0], c2); len(got) != 0 {
			t.Fatalf("PassingCovering after delta SHIP = %+v, want empty", got)
		}

		*r.now = r.now.Add(5 * time.Second)
		r.pass() // observeReviewLocked stops retro 5; L6 -> N42 accepts and merges PR #7 at c2
		st = r.state()
		a := st.Tasks[0].Attempts[0]
		if st.Tasks[0].LastRow != "N42" {
			t.Fatalf("after delta SHIP: LastRow = %q, want N42", st.Tasks[0].LastRow)
		}
		if len(a.Accepts) != 1 || a.Accepts[0].Head != c2 || a.Accepts[0].By != "bar" || !slices.Equal(a.Accepts[0].Rounds, []int{5}) {
			t.Fatalf("Accepts after delta SHIP = %+v, want [Accept{Head:%s By:bar Rounds:[5]}]", a.Accepts, c2)
		}
		if merges := ghCalls(t, r.gh, "pr merge"); len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+c2) {
			t.Fatalf("gh pr merge calls after delta SHIP = %q, want 1 merge pinned to %s", merges, c2)
		}
	})
}

// TestL6RetroShipRecordsRetroRoundAfterEarlierPass checks Carbon major 2 / Neon
// high 3 (land.go:recordAccept, KB-RET-2): when an earlier pass round also
// covers H (1 <= len(PassingCovering) < ReviewRounds), L6 retro SHIP must still
// be recorded in Accept.Rounds and must not mark "review" waived on override.
func TestL6RetroShipRecordsRetroRoundAfterEarlierPass(t *testing.T) {
	s := flow.DefaultSettings() // ReviewRounds == 2
	base := flow.Attempt{
		BaseCommit: "b0",
		Ready:      []flow.Ready{{Head: "c1"}},
		Rejects:    []flow.Reject{{Head: "c1", Round: 2, Why: "false positive"}},
		Rounds: []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictPass},
			{N: 2, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictShip},
		},
	}
	if got := flow.Ladder(flow.In{Task: flow.Task{ID: "T1", Attempts: []flow.Attempt{base}}, Settings: s}); got != flow.Passed {
		t.Fatalf("precondition failed: Ladder = %q, want %q (L6)", got, flow.Passed)
	}

	bar := base
	recordAccept(flow.Task{ID: "T1"}, &bar, s, flow.Facts{}, "c1", false)
	if len(bar.Accepts) != 1 || !slices.Equal(bar.Accepts[0].Rounds, []int{1, 4}) {
		t.Errorf("bar Accept.Rounds = %v, want [1 4] (missing L6 retro SHIP round 4)", bar.Accepts)
	}

	ov := base
	ov.Overrides = []flow.Override{{Head: "c1", ProofID: "proof-1"}}
	recordAccept(flow.Task{ID: "T1"}, &ov, s, flow.Facts{PR: flow.PR{Checks: flow.ChecksPending}}, "c1", true)
	if len(ov.Accepts) != 1 {
		t.Fatalf("override Accepts = %+v, want 1", ov.Accepts)
	}
	if slices.Contains(ov.Accepts[0].Waived, "review") {
		t.Errorf("override Accept.Waived = %v, must not waive \"review\" when L6 retro SHIP passed", ov.Accepts[0].Waived)
	}
}

// TestRecapWakeFactsOmitsClosedAttemptAndSupersededRetros checks Carbon major 3
// (wake.go:taskFacts): a recap wake must not attach a retro from a closed
// attempt or one superseded by plan amend advancing Attempt.Boundary.
func TestRecapWakeFactsOmitsClosedAttemptAndSupersededRetros(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	t.Run("omits closed attempt retro", func(t *testing.T) {
		task := flow.Task{
			ID: "T1", Title: "feat: widget",
			Attempts: []flow.Attempt{
				{
					BaseCommit: "b0", Closed: "new",
					Ready:  []flow.Ready{{Head: "c1"}},
					Rounds: []flow.Round{{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictRedesign, ReportPath: "/tmp/reports/T1-r4.md"}},
				},
				{
					BaseCommit: "b0", SizeCap: 200,
					Ready:  []flow.Ready{{Head: "c2"}},
					Rounds: []flow.Round{{N: 4, Kind: flow.RoundFull, Head: "c2", Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/tmp/reports/T1-r4.md"}},
				},
			},
			Recaps: []flow.Recap{
				{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}, Decided: &flow.RecapDecision{Choice: "new", Why: "amended", At: now.Add(-time.Minute)}},
				{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}},
			},
			Retros: []flow.Retro{{
				Attempt: 1, Round: 4, Cause: "brief-ambiguity",
				WorkerChange: "stop", WorkerChangeKind: flow.ChangeKindCheck,
				CoordinatorChange: "rewrite brief", CoordinatorChangeKind: flow.ChangeKindBrief,
				At: now.Add(-2 * time.Minute),
			}},
		}
		if got := taskFacts(task, flow.Facts{}, now); strings.Contains(got, "; retro r") {
			t.Fatalf("attempt 2 recap wake leaked closed attempt 1 retro: %s", got)
		}
	})

	t.Run("omits retro superseded by plan amend boundary", func(t *testing.T) {
		task := flow.Task{
			ID: "T2", Title: "feat: gadget",
			Attempts: []flow.Attempt{{
				BaseCommit: "b0", SizeCap: 200, Boundary: 1,
				Ready:  []flow.Ready{{Head: "c1"}, {Head: "c3"}},
				Rounds: []flow.Round{{N: 4, Kind: flow.RoundRetro, Head: "c1", Verdict: flow.VerdictFix, Majors: 1, ReportPath: "/tmp/reports/T2-r4.md"}},
			}},
			Recaps: []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{"continue", "split", "new", "drop"}}},
			Retros: []flow.Retro{{
				Attempt: 1, Round: 4, Cause: "missing-context",
				WorkerChange: "fix", WorkerChangeKind: flow.ChangeKindCheck,
				CoordinatorChange: "clarify", CoordinatorChangeKind: flow.ChangeKindBrief,
				At: now.Add(-time.Minute),
			}},
		}
		if got := taskFacts(task, flow.Facts{}, now); strings.Contains(got, "; retro r") {
			t.Fatalf("post-amend recap wake leaked superseded pre-boundary retro: %s", got)
		}
	})
}

// TestN22RetroFixHeldWhenWorkerEnded checks Carbon major 1's remaining N22/R36
// contract: when the worker run has ended, N22 logs held-for-next-worker (not
// "told"), keeps the bounded-fix text in Attempt.Held, tightens SizeCap on the
// committed N22 pass, and does not persist the tightened SizeCap prematurely
// inside R36's intermediate commit before N22 commits.
func TestN22RetroFixHeldWhenWorkerEnded(t *testing.T) {
	seedEnded := func(t *testing.T) (*landing, *time.Time, string) {
		t.Helper()
		l := newLanding(t).withTokens()
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		l.eng.Now = func() time.Time { return now }
		reportPath := l.paths.Report(l.id, "T1", 4)
		l.srv.Finish(l.worker, "exited")
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.SizeCap = 200 // newLanding has 1 changed line; 1 + 60 = 61
			a.RunID = ""
			a.Run = &flow.RunRecord{
				Role: flow.RoleWorker, CLI: "claude", Address: l.worker,
				SessionID: "11111111-2222-4333-8444-555555555555", Resumable: true,
				State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: now,
			}
			a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-time.Minute)}}
			a.Rounds = []flow.Round{
				{N: 1, Kind: flow.RoundFull, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 2, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
				{N: 4, Kind: flow.RoundRetro, Head: l.c1, Verdict: flow.VerdictFix, Majors: 1, ReportPath: reportPath},
			}
			s.Tasks[0].Retros = []flow.Retro{{
				Attempt: 1, Round: 4, Cause: "missing-context",
				WorkerChange: "guard nil map", WorkerChangeKind: flow.ChangeKindCheck,
				CoordinatorChange: "name map contract", CoordinatorChangeKind: flow.ChangeKindBrief,
				At: now,
			}}
		})
		return l, &now, reportPath
	}

	t.Run("R36 holds bounded retro fix and tightens SizeCap", func(t *testing.T) {
		l, now, reportPath := seedEnded(t)
		var launched []hc.RunParams
		l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
			if p.Labels["sheepdog.role"] == flow.RoleWorker {
				launched = append(launched, p)
			}
		}

		l.pass() // inside restart backoff: N22 -> R36 holds bounded retro fix

		if r36 := logRules(t, l.paths.Log(l.id), "R36"); len(r36) != 1 || r36[0].Decision != flow.RefuseRunEnded {
			t.Errorf("R36 log = %+v, want 1 %q line", r36, flow.RefuseRunEnded)
		}
		n22 := logRules(t, l.paths.Log(l.id), "N22")
		if len(n22) != 1 || !strings.HasPrefix(n22[0].Decision, "held for the next worker:") || !strings.Contains(n22[0].Decision, "bounded retro fix") || strings.Contains(n22[0].Decision, "told") {
			t.Errorf("N22 log = %+v, want 1 held-for-next-worker bounded retro fix decision (not told)", n22)
		}
		st := l.state()
		a := st.Tasks[0].Attempts[0]
		if wantCap := 1 + st.Settings.ContinueLines; a.SizeCap != wantCap {
			t.Errorf("stored a.SizeCap = %d, want %d", a.SizeCap, wantCap)
		}
		if len(a.Held) != 1 || !strings.Contains(a.Held[0], "Retro round 4 granted one bounded fix") || !strings.Contains(a.Held[0], reportPath) || !strings.Contains(a.Held[0], "guard nil map") {
			t.Fatalf("Attempt.Held = %q, want bounded retro fix text", a.Held)
		}

		*now = now.Add(time.Minute + time.Second)
		l.pass() // past backoff: next worker gets the held bounded-fix prompt
		if len(launched) != 1 {
			t.Fatalf("launched worker count = %d, want 1", len(launched))
		}
		if prompt := launched[0].Argv[len(launched[0].Argv)-1]; !strings.Contains(prompt, "Retro round 4 granted one bounded fix") {
			t.Errorf("next worker prompt missing held retro fix:\n%s", prompt)
		}
	})

	t.Run("R36 intermediate commit must not persist tightened SizeCap before N22 commits", func(t *testing.T) {
		l, _, _ := seedEnded(t)
		tellKey := flow.AskTellFindings + "@4"
		l.eng.writeState = func(path string, s flow.State) error {
			if len(s.Tasks) > 0 && len(s.Tasks[0].Attempts) > 0 && flow.Done(s.Tasks[0].Attempts[0].Acts, tellKey) {
				return errors.New("disk full on N22 commit")
			}
			return SaveState(path, s)
		}

		l.pass()

		a := l.state().Tasks[0].Attempts[0]
		if flow.Done(a.Acts, tellKey) {
			t.Fatalf("setup bug: %s unexpectedly committed", tellKey)
		}
		if a.SizeCap != 200 {
			t.Fatalf("R36 intermediate commit prematurely persisted tightened a.SizeCap = %d before N22 committed, want 200", a.SizeCap)
		}
	})
}
