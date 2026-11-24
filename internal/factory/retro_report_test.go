package factory

// PR 12.3, TDD step 1: failing tests for daemon `report retro` acceptance
// (`verbRoles["retro"]`, `acceptRetroLocked`, shared `checkReviewWorktreeLocked`,
// RV1–RV8 equivalents for retro, KB-RET-3, KB-RET-12, design.md 7.2, plan.md 12.3).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// newRetroReportFixture builds on newReviewTestFixture with three prior
// reviewer rounds (majors > 0) and an active round-4 RoundRetro run on
// Attempt.Review (RoleRetro) using f.revTok as the retro run token.
func newRetroReportFixture(t *testing.T) *reviewTestFixture {
	t.Helper()
	f := newReviewTestFixture(t)
	st, err := LoadState(f.eng.Paths.State(f.id))
	if err != nil {
		t.Fatal(err)
	}
	a := &st.Tasks[0].Attempts[0]
	a.Rounds = []flow.Round{
		{N: 1, Kind: flow.RoundFull, Head: f.c1, Verdict: flow.VerdictChanges, Majors: 1},
		{N: 2, Kind: flow.RoundDelta, Head: f.c1, Verdict: flow.VerdictChanges, Majors: 1},
		{N: 3, Kind: flow.RoundDelta, Head: f.c1, Verdict: flow.VerdictChanges, Majors: 1},
	}
	a.Review.Round = 4
	a.Review.Kind = flow.RoundRetro
	a.Review.Run.ID = "run-retro-1"
	a.Review.Run.Role = flow.RoleRetro
	if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
		t.Fatal(err)
	}
	return f
}

func validRetroPayload(t *testing.T, verdict, code, head, findings string) json.RawMessage {
	t.Helper()
	return mustReviewPayload(t, map[string]any{
		"verdict":                 verdict,
		"code":                    code,
		"head":                    head,
		"cause":                   "patch-shaped-fix",
		"worker_change":           "handle all negative inputs in one branch",
		"worker_change_kind":      flow.ChangeKindCheck,
		"coordinator_change":      "add negative boundary outcomes to the brief",
		"coordinator_change_kind": flow.ChangeKindBrief,
		"findings":                findings,
	})
}

// TestReportRetroRoleIsolationAndIdentityKBRET12 checks KB-RET-12 and design.md
// 7.2 (RV1/RV2 for retro): RoleReviewer calling `report retro` is refused
// `kind_not_allowed` naming the role mismatch (not unknown verb), worker and
// coordinator tokens are refused `kind_not_allowed`, missing/unknown/other-daemon
// tokens are refused `invalid_token`, and ended task, open recap (KB-RET-12),
// or stale round are refused `already_closed`, leaving state untouched.
func TestReportRetroRoleIsolationAndIdentityKBRET12(t *testing.T) {
	t.Run("RoleReviewer refused report retro kind_not_allowed (KB-RET-12)", func(t *testing.T) {
		f := newReviewTestFixture(t) // f.revTok belongs to RoleReviewer (RoundFull)
		f.logs.Reset()
		err := f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f.code, f.c1, ""))
		if got := refusalCode(err); got != "kind_not_allowed" {
			t.Fatalf("Report(reviewerTok, retro) = %v (code %q), want kind_not_allowed", err, got)
		}
		if !strings.Contains(err.Error(), flow.RoleReviewer) || !strings.Contains(err.Error(), flow.RoleRetro) {
			t.Errorf("refusal %q must come from role isolation naming %q and %q (not unknown verb)", err.Error(), flow.RoleReviewer, flow.RoleRetro)
		}
		if strings.Contains(f.logs.String(), f.revTok) {
			t.Fatalf("reviewer token leaked into daemon log: %s", f.logs.String())
		}
		st, err := LoadState(f.eng.Paths.State(f.id))
		if err != nil {
			t.Fatal(err)
		}
		if st.Seq != 1 || len(st.Tasks[0].Attempts[0].Rounds) != 0 || len(st.Tasks[0].Retros) != 0 {
			t.Fatalf("refused reviewer report retro mutated state: seq=%d rounds=%+v retros=%+v",
				st.Seq, st.Tasks[0].Attempts[0].Rounds, st.Tasks[0].Retros)
		}
		if _, statErr := os.Stat(f.eng.Paths.Report(f.id, "T1", 1)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("refused reviewer report retro wrote report file: stat=%v", statErr)
		}
	})

	t.Run("identity and token isolation (RV1)", func(t *testing.T) {
		f := newRetroReportFixture(t)
		st, err := LoadState(f.eng.Paths.State(f.id))
		if err != nil {
			t.Fatal(err)
		}
		st.Tasks = append(st.Tasks, flow.Task{
			ID:    "T2",
			Title: "feat: already done",
			Done:  &flow.Merged{MergedCommit: f.c1},
			Attempts: []flow.Attempt{{
				Worktree: f.wt,
				Ready:    []flow.Ready{{Head: f.c1}},
				Review: &flow.ReviewRun{
					RunID:        "term-retro-done",
					RunTokenHash: tokenHash("tok-retro-ended"),
					Run:          &flow.RunRecord{Role: flow.RoleRetro, State: flow.PhaseWorking},
					Round:        1,
					Kind:         flow.RoundRetro,
					Head:         f.c1,
					Code:         "donecode00",
				},
			}},
		})
		if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
			t.Fatal(err)
		}

		payload := validRetroPayload(t, flow.VerdictShip, f.code, f.c1, "")
		for _, tc := range []struct {
			name, token, wantCode, wantInErr string
		}{
			{"missing token", "", "invalid_token", ""},
			{"unknown token", "tok-does-not-exist", "invalid_token", ""},
			{"worker token", f.workTok, "kind_not_allowed", flow.RoleWorker},
			{"coordinator token", f.crdTok, "kind_not_allowed", flow.RoleCoordinator},
			{"ended task retro token", "tok-retro-ended", "already_closed", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f.logs.Reset()
				err := f.eng.Report(tc.token, "retro", payload)
				if got := refusalCode(err); got != tc.wantCode {
					t.Fatalf("Report(%s, retro) = %v (code %q), want %q", tc.name, err, got, tc.wantCode)
				}
				if tc.wantInErr != "" && (!strings.Contains(err.Error(), tc.wantInErr) || !strings.Contains(err.Error(), flow.RoleRetro)) {
					t.Errorf("refusal %q must name %q and %q", err.Error(), tc.wantInErr, flow.RoleRetro)
				}
				if tc.token != "" && strings.Contains(f.logs.String(), tc.token) {
					t.Fatalf("token leaked into daemon log: %s", f.logs.String())
				}
			})
		}

		stAfter, err := LoadState(f.eng.Paths.State(f.id))
		if err != nil {
			t.Fatal(err)
		}
		if stAfter.Seq != 1 || len(stAfter.Tasks[0].Attempts[0].Rounds) != 3 || len(stAfter.Tasks[0].Retros) != 0 {
			t.Fatalf("refusals mutated state: seq=%d rounds=%d retros=%d",
				stAfter.Seq, len(stAfter.Tasks[0].Attempts[0].Rounds), len(stAfter.Tasks[0].Retros))
		}

		eng2 := newGitTestEngine(t, testPaths(t), nil)
		if err := eng2.Report(f.revTok, "retro", payload); refusalCode(err) != "invalid_token" {
			t.Fatalf("second daemon Report(retro) = %v, want invalid_token", err)
		}
	})

	t.Run("open recap refuses already_closed (KB-RET-12, RV2)", func(t *testing.T) {
		f := newRetroReportFixture(t)
		st, _ := LoadState(f.eng.Paths.State(f.id))
		st.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerCI, Allowed: []string{"split", "new", "drop"}}}
		if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
			t.Fatal(err)
		}
		if err := f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f.code, f.c1, "")); refusalCode(err) != "already_closed" {
			t.Fatalf("open recap retro report err = %v, want already_closed", err)
		}
		got, _ := LoadState(f.eng.Paths.State(f.id))
		if len(got.Tasks[0].Retros) != 0 || len(got.Tasks[0].Attempts[0].Rounds) != 3 {
			t.Fatalf("open recap refusal mutated state: rounds=%d retros=%d", len(got.Tasks[0].Attempts[0].Rounds), len(got.Tasks[0].Retros))
		}
	})

	t.Run("stale round refuses already_closed (RV2)", func(t *testing.T) {
		f := newRetroReportFixture(t)
		st, _ := LoadState(f.eng.Paths.State(f.id))
		st.Tasks[0].Attempts[0].Rounds = append(st.Tasks[0].Attempts[0].Rounds, flow.Round{N: 4, Kind: flow.RoundRetro, Head: f.c1, Verdict: flow.VerdictFix, Majors: 1})
		if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
			t.Fatal(err)
		}
		if err := f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f.code, f.c1, "")); refusalCode(err) != "already_closed" {
			t.Fatalf("stale round retro report err = %v, want already_closed", err)
		}
	})
}

// TestReportRetroRefusalsLeaveStateUntouchedKBRET3 checks KB-RET-3 and
// design.md 7.2 (retro validation mirroring RV3–RV6): bad cause, SHIP with
// majors, FIX with 0 majors, bad change kind, empty/overlong changes, overlong
// findings, wrong code/head, and moved head are refused without touching
// t.Retros or a.Rounds or writing a report file, and the retro run stays alive
// to submit a corrected report.
func TestReportRetroRefusalsLeaveStateUntouchedKBRET3(t *testing.T) {
	f := newRetroReportFixture(t)
	long501 := strings.Repeat("a", 501)
	over64K := strings.Repeat("x", 64*1024+1)

	patch := func(overrides map[string]any) json.RawMessage {
		m := map[string]any{
			"verdict":                 flow.VerdictFix,
			"code":                    f.code,
			"head":                    f.c1,
			"cause":                   "patch-shaped-fix",
			"worker_change":           "fix all negative cases",
			"worker_change_kind":      flow.ChangeKindCheck,
			"coordinator_change":      "list boundary outcomes",
			"coordinator_change_kind": flow.ChangeKindBrief,
			"findings":                "major: negative n returns wrong sign\n",
		}
		for k, v := range overrides {
			m[k] = v
		}
		return mustReviewPayload(t, m)
	}

	cases := []struct {
		name      string
		payload   json.RawMessage
		wantInErr string
	}{
		{"empty body", json.RawMessage("   "), ""},
		{"unknown field", patch(map[string]any{"extra": 1}), ""},
		{"bad verdict pass", patch(map[string]any{"verdict": flow.VerdictPass, "findings": ""}), "verdict"},
		{"bad verdict changes", patch(map[string]any{"verdict": flow.VerdictChanges}), "verdict"},
		{"bad verdict unknown", patch(map[string]any{"verdict": "approve"}), "verdict"},
		{"bad cause", patch(map[string]any{"cause": "not-in-set"}), "cause"},
		{"empty cause", patch(map[string]any{"cause": "   "}), "cause"},
		{"SHIP with exact major", patch(map[string]any{"verdict": flow.VerdictShip, "findings": "major: still broken\n"}), "major"},
		{"SHIP with indented major", patch(map[string]any{"verdict": flow.VerdictShip, "findings": "  \tmajor: still broken\n"}), "major"},
		{"FIX with empty findings", patch(map[string]any{"verdict": flow.VerdictFix, "findings": ""}), "major"},
		{"FIX with 0 exact majors (bullets ignored)", patch(map[string]any{
			"verdict":  flow.VerdictFix,
			"findings": "minor: nit\n- major: bullet does not count\n**major:** bold does not count\n",
		}), "major"},
		{"bad worker_change_kind", patch(map[string]any{"worker_change_kind": "prompt"}), "worker_change_kind"},
		{"empty worker_change_kind", patch(map[string]any{"worker_change_kind": ""}), "worker_change_kind"},
		{"bad coordinator_change_kind", patch(map[string]any{"coordinator_change_kind": "rule"}), "coordinator_change_kind"},
		{"empty coordinator_change_kind", patch(map[string]any{"coordinator_change_kind": ""}), "coordinator_change_kind"},
		{"empty worker_change", patch(map[string]any{"worker_change": " \t\n "}), "worker_change"},
		{"empty coordinator_change", patch(map[string]any{"coordinator_change": " \t\n "}), "coordinator_change"},
		{"worker_change over 500 runes", patch(map[string]any{"worker_change": long501}), "worker_change"},
		{"coordinator_change over 500 runes", patch(map[string]any{"coordinator_change": long501}), "coordinator_change"},
		{"findings over 64 KiB", patch(map[string]any{"findings": over64K}), "findings"},
		{"wrong code", patch(map[string]any{"code": "wrongcode0"}), "code"},
		{"wrong head", patch(map[string]any{"head": "0000000000"}), "head"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.eng.Report(f.revTok, "retro", tc.payload)
			if got := refusalCode(err); got != "invalid_report" {
				t.Fatalf("err = %v (code %q), want invalid_report", err, got)
			}
			if tc.wantInErr != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantInErr)) {
				t.Errorf("refusal %q does not name %q", err.Error(), tc.wantInErr)
			}
			st, loadErr := LoadState(f.eng.Paths.State(f.id))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			a := st.Tasks[0].Attempts[0]
			if st.Seq != 1 || len(st.Tasks[0].Retros) != 0 || len(a.Rounds) != 3 {
				t.Fatalf("refusal mutated state: seq=%d retros=%+v rounds=%d", st.Seq, st.Tasks[0].Retros, len(a.Rounds))
			}
			if a.Review == nil || a.Review.RunTokenHash == "" {
				t.Fatalf("invalid_report cleared retro token; run must stay alive to fix and re-run: %+v", a.Review)
			}
			if _, statErr := os.Stat(f.eng.Paths.Report(f.id, "T1", 4)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refusal wrote report file: stat=%v", statErr)
			}
		})
	}

	// Retro run stays alive after invalid_report refusals and can submit a corrected report.
	if err := f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictFix, f.code, f.c1, "major: handle n < 0\n")); err != nil {
		t.Fatalf("corrected retry after invalid_report failed: %v", err)
	}

	t.Run("moved head refuses head_moved and leaves state untouched (RV6)", func(t *testing.T) {
		f2 := newRetroReportFixture(t)
		if err := os.WriteFile(filepath.Join(f2.wt, "item.go"), []byte("package demo\n// v2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, f2.wt, "add", "item.go")
		gitIn(t, f2.wt, "commit", "-m", "feat: move head")

		err := f2.eng.Report(f2.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f2.code, f2.c1, ""))
		if got := refusalCode(err); got != "head_moved" {
			t.Fatalf("moved head err = %v (code %q), want head_moved", err, got)
		}
		st, _ := LoadState(f2.eng.Paths.State(f2.id))
		if len(st.Tasks[0].Retros) != 0 || len(st.Tasks[0].Attempts[0].Rounds) != 3 {
			t.Fatalf("head_moved mutated state: retros=%+v rounds=%d", st.Tasks[0].Retros, len(st.Tasks[0].Attempts[0].Rounds))
		}
		if _, statErr := os.Stat(f2.eng.Paths.Report(f2.id, "T1", 4)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("head_moved wrote report file: stat=%v", statErr)
		}
	})

	t.Run("merge_only covered head is accepted (RV6)", func(t *testing.T) {
		f3 := newRetroReportFixture(t)
		if err := os.WriteFile(filepath.Join(f3.wt, "merge.txt"), []byte("merged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, f3.wt, "add", "merge.txt")
		gitIn(t, f3.wt, "commit", "-m", "chore: merge base")
		c2 := gitIn(t, f3.wt, "rev-parse", "HEAD")

		st, _ := LoadState(f3.eng.Paths.State(f3.id))
		st.Tasks[0].Attempts[0].MergeOnly = []flow.Advance{{From: f3.c1, To: c2}}
		if err := SaveState(f3.eng.Paths.State(f3.id), st); err != nil {
			t.Fatal(err)
		}
		if err := f3.eng.Report(f3.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f3.code, f3.c1, "")); err != nil {
			t.Fatalf("merge_only covered head should accept retro, got: %v", err)
		}
	})
}

// TestReportRetroDirtyTreeVoidsRunKBRET3 checks KB-RET-3 and design.md 7.2
// (RV7 for retro): modified tracked files and untracked files refuse with
// `dirty_tree`, leave t.Retros and a.Rounds untouched without writing a report
// file, mark a.Review.Run ended(error) with RoleRetro, clear RunTokenHash while
// keeping RunID, log factory.review_void + factory.refused, wake the loop, and
// the next observeReviewLocked pass stops the terminal and clears RunID.
func TestReportRetroDirtyTreeVoidsRunKBRET3(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty func(t *testing.T, wt string)
	}{
		{"modified tracked file", func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "item.go"), []byte("package demo\n// dirty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"untracked file", func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "stray.tmp"), []byte("junk\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetroReportFixture(t)
			term, err := hc.NewClient(f.srv.Path).Run(context.Background(), hc.RunParams{
				Name: "retro-live", Cwd: f.wt, Argv: []string{"claude"},
				Labels: map[string]string{"sheepdog.run": "run-retro-1", "sheepdog.role": flow.RoleRetro},
			})
			if err != nil {
				t.Fatal(err)
			}
			st, _ := LoadState(f.eng.Paths.State(f.id))
			st.Tasks[0].Attempts[0].Review.RunID = term.Address
			st.Tasks[0].Attempts[0].Review.Run.Address = term.Address
			if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
				t.Fatal(err)
			}

			tc.dirty(t, f.wt)
			select {
			case <-f.eng.wake:
			default:
			}

			err = f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f.code, f.c1, ""))
			if got := refusalCode(err); got != "dirty_tree" {
				t.Fatalf("dirty worktree err = %v (code %q), want dirty_tree", err, got)
			}
			select {
			case <-f.eng.wake:
			default:
				t.Errorf("dirty_tree did not wake the engine loop")
			}

			st, _ = LoadState(f.eng.Paths.State(f.id))
			a := st.Tasks[0].Attempts[0]
			if len(st.Tasks[0].Retros) != 0 || len(a.Rounds) != 3 {
				t.Fatalf("dirty_tree mutated retros/rounds: retros=%+v rounds=%d", st.Tasks[0].Retros, len(a.Rounds))
			}
			if _, statErr := os.Stat(f.eng.Paths.Report(f.id, "T1", 4)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("dirty_tree wrote report file: stat=%v", statErr)
			}
			if a.Review == nil || a.Review.Run == nil || a.Review.Run.Role != flow.RoleRetro ||
				a.Review.Run.State != flow.PhaseEnded || a.Review.Run.Result != flow.EndError {
				t.Fatalf("dirty_tree retro run = %+v, want Role=retro ended(error)", a.Review)
			}
			if a.Review.RunTokenHash != "" {
				t.Errorf("dirty_tree did not clear RunTokenHash")
			}
			if a.Review.RunID != term.Address {
				t.Errorf("dirty_tree RunID = %q, want preserved %q for next pass to stop", a.Review.RunID, term.Address)
			}
			if !strings.Contains(f.logs.String(), "factory.review_void") || !strings.Contains(f.logs.String(), `"code":"dirty_tree"`) {
				t.Errorf("daemon log missing review_void/dirty_tree:\n%s", f.logs.String())
			}

			facts := flow.Facts{Tip: f.c1, TipClean: false}
			f.eng.mu.Lock()
			skip := f.eng.observeReviewLocked(context.Background(), f.id, &st, 0, &facts, 0)
			f.eng.mu.Unlock()
			if skip {
				t.Fatalf("observeReviewLocked returned skip=true after dirty_tree")
			}
			if st.Tasks[0].Attempts[0].Review.RunID != "" {
				t.Errorf("RunID not cleared after observeReviewLocked: %q", st.Tasks[0].Attempts[0].Review.RunID)
			}
			if _, getErr := hc.NewClient(f.srv.Path).Get(context.Background(), term.Address); !errors.Is(getErr, hc.ErrNotFound) {
				t.Errorf("leftover retro terminal %s still exists after observeReviewLocked: err=%v", term.Address, getErr)
			}
		})
	}
}

// TestReportRetroAcceptedWritesReportRoundAndRetro checks design.md 7.2 (RV8
// for retro) and plan.md 12.3: accepted retro writes reports/<task>-r<round>.md
// byte-exact, appends a RoundRetro to a.Rounds with engine-counted Majors,
// appends a flow.Retro to t.Retros (cleaned changes, 1-based attempt number,
// non-zero At), wakes the engine loop, logs the verdict, and refuses duplicate
// reports with already_closed.
func TestReportRetroAcceptedWritesReportRoundAndRetro(t *testing.T) {
	t.Run("FIX with hostile findings byte-exact, cleaned changes, duplicate refused", func(t *testing.T) {
		f := newRetroReportFixture(t)
		select {
		case <-f.eng.wake:
		default:
		}

		body := mustReviewPayload(t, map[string]any{
			"verdict":                 flow.VerdictFix,
			"code":                    f.code,
			"head":                    f.c1,
			"cause":                   "patch-shaped-fix",
			"worker_change":           "  handle\tnegative\ninputs  ",
			"worker_change_kind":      flow.ChangeKindCheck,
			"coordinator_change":      "  add\nboundary\toutcomes  ",
			"coordinator_change_kind": flow.ChangeKindBrief,
			"findings":                hostileFindingsText,
		})
		if err := f.eng.Report(f.revTok, "retro", body); err != nil {
			t.Fatalf("Report retro FIX failed: %v", err)
		}

		select {
		case <-f.eng.wake:
		default:
			t.Errorf("accepted retro did not wake the engine loop")
		}

		st, err := LoadState(f.eng.Paths.State(f.id))
		if err != nil {
			t.Fatal(err)
		}
		a := st.Tasks[0].Attempts[0]
		if len(a.Rounds) != 4 {
			t.Fatalf("len(a.Rounds) = %d, want 4", len(a.Rounds))
		}
		rnd := a.Rounds[3]
		wantPath := f.eng.Paths.Report(f.id, "T1", 4)
		if rnd.N != 4 || rnd.Kind != flow.RoundRetro || rnd.Head != f.c1 ||
			rnd.Verdict != flow.VerdictFix || rnd.Majors != 1 ||
			rnd.ReportPath != wantPath || rnd.Reviewer != a.Review.Reviewer {
			t.Fatalf("appended retro round = %+v, want N=4 Kind=retro Verdict=fix Majors=1 Path=%q Reviewer=%+v",
				rnd, wantPath, a.Review.Reviewer)
		}
		if len(st.Tasks[0].Retros) != 1 {
			t.Fatalf("len(t.Retros) = %d, want 1", len(st.Tasks[0].Retros))
		}
		ret := st.Tasks[0].Retros[0]
		if ret.Attempt != 1 || ret.Round != 4 || ret.Cause != "patch-shaped-fix" ||
			ret.WorkerChange != "handle negative inputs" || ret.WorkerChangeKind != flow.ChangeKindCheck ||
			ret.CoordinatorChange != "add boundary outcomes" || ret.CoordinatorChangeKind != flow.ChangeKindBrief ||
			ret.At.IsZero() {
			t.Fatalf("appended t.Retros[0] = %+v", ret)
		}
		// Handler never stops the terminal under lock: a.Review stays for woken pass.
		if a.Review == nil || a.Review.RunID != "term-rev-1" {
			t.Fatalf("a.Review = %+v, want intact ReviewRun for woken pass to stop", a.Review)
		}

		gotBytes, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("read stored retro report: %v", err)
		}
		if string(gotBytes) != hostileFindingsText {
			t.Fatalf("stored retro report differs from input findings")
		}
		if _, err := os.Stat("/tmp/srev2-should-not-exist"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("hostile findings command executed (stat err=%v)", err)
		}

		logBytes, err := os.ReadFile(f.eng.Paths.Log(f.id))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(logBytes), `"by":"retro"`) || !strings.Contains(string(logBytes), `"decision":"fix"`) {
			t.Fatalf("log.jsonl missing retro verdict entry:\n%s", string(logBytes))
		}

		// Duplicate report with same token before woken pass stops run -> already_closed.
		if err := f.eng.Report(f.revTok, "retro", validRetroPayload(t, flow.VerdictShip, f.code, f.c1, "")); refusalCode(err) != "already_closed" {
			t.Fatalf("duplicate retro report err = %v, want already_closed", err)
		}
		stDup, _ := LoadState(f.eng.Paths.State(f.id))
		if len(stDup.Tasks[0].Attempts[0].Rounds) != 4 || len(stDup.Tasks[0].Retros) != 1 {
			t.Fatalf("duplicate retro mutated state: rounds=%d retros=%d",
				len(stDup.Tasks[0].Attempts[0].Rounds), len(stDup.Tasks[0].Retros))
		}
		gotAfterDup, _ := os.ReadFile(wantPath)
		if string(gotAfterDup) != hostileFindingsText {
			t.Fatalf("duplicate retro overwrote report file")
		}
	})

	t.Run("SHIP with 0 exact majors accepted", func(t *testing.T) {
		f := newRetroReportFixture(t)
		body := mustReviewPayload(t, map[string]any{
			"verdict":                 flow.VerdictShip,
			"code":                    f.code,
			"head":                    f.c1,
			"cause":                   "none",
			"worker_change":           "keep table tests",
			"worker_change_kind":      flow.ChangeKindCheck,
			"coordinator_change":      "keep brief concise",
			"coordinator_change_kind": flow.ChangeKindBrief,
			"findings":                "minor: clean\n- major: bullet ignored\n",
		})
		if err := f.eng.Report(f.revTok, "retro", body); err != nil {
			t.Fatalf("Report retro SHIP failed: %v", err)
		}
		st, _ := LoadState(f.eng.Paths.State(f.id))
		rnd := st.Tasks[0].Attempts[0].Rounds[3]
		if rnd.Kind != flow.RoundRetro || rnd.Verdict != flow.VerdictShip || rnd.Majors != 0 {
			t.Fatalf("SHIP round = %+v, want RoundRetro ship 0 majors", rnd)
		}
		if len(st.Tasks[0].Retros) != 1 || st.Tasks[0].Retros[0].Cause != "none" || st.Tasks[0].Retros[0].Round != 4 {
			t.Fatalf("SHIP t.Retros = %+v", st.Tasks[0].Retros)
		}
	})

	t.Run("REDESIGN accepted with 0 majors and records 1-based attempt 2", func(t *testing.T) {
		f := newRetroReportFixture(t)
		st, _ := LoadState(f.eng.Paths.State(f.id))
		st.Tasks[0].Attempts = append([]flow.Attempt{{BaseCommit: "b0", Closed: "new attempt"}}, st.Tasks[0].Attempts...)
		if err := SaveState(f.eng.Paths.State(f.id), st); err != nil {
			t.Fatal(err)
		}
		body := mustReviewPayload(t, map[string]any{
			"verdict":                 flow.VerdictRedesign,
			"code":                    f.code,
			"head":                    f.c1,
			"cause":                   "wrong-seam",
			"worker_change":           "split parser from executor",
			"worker_change_kind":      flow.ChangeKindCheck,
			"coordinator_change":      "require design seam first",
			"coordinator_change_kind": flow.ChangeKindBrief,
			"findings":                "Premise analysis only; no major: lines.\n",
		})
		if err := f.eng.Report(f.revTok, "retro", body); err != nil {
			t.Fatalf("Report retro REDESIGN failed: %v", err)
		}
		st, _ = LoadState(f.eng.Paths.State(f.id))
		rnd := st.Tasks[0].Attempts[1].Rounds[3]
		if rnd.Kind != flow.RoundRetro || rnd.Verdict != flow.VerdictRedesign || rnd.Majors != 0 {
			t.Fatalf("REDESIGN round = %+v, want RoundRetro redesign 0 majors", rnd)
		}
		if len(st.Tasks[0].Retros) != 1 || st.Tasks[0].Retros[0].Attempt != 2 || st.Tasks[0].Retros[0].Round != 4 || st.Tasks[0].Retros[0].Cause != "wrong-seam" {
			t.Fatalf("REDESIGN t.Retros = %+v, want Attempt=2 Round=4 Cause=wrong-seam", st.Tasks[0].Retros)
		}
	})
}
