package factory

// PR 11.3, TDD step 1: failing tests for plan-review report acceptance, finding
// closing, plan reject, J15 post-report stop, 2x heartbeat_minutes time bound,
// status/wake/coordSummary lines, and store commit contract (plan.md 11.3, design.md 6, 7.2, 11.7).

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func launchPlanReview(t *testing.T, tasksJSON string) (w *storeWorld, coordTok, prTok, code, runID string) {
	t.Helper()
	w, coordTok = seedPlanCoord(t, "Build greeting helper", 1, map[string][]flow.Candidate{
		flow.RolePlanReviewer: {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
	})
	w.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		if p.Labels["sheepdog.role"] == flow.RolePlanReviewer {
			prTok = p.Env["SHEEPDOG_RUN_TOKEN"]
		}
	}
	if err := w.eng.Report(coordTok, "plan apply", json.RawMessage(tasksJSON)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	w.pass()
	st := w.state()
	if len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Run == nil || prTok == "" {
		t.Fatalf("expected launched plan reviewer: reviews=%+v tokEmpty=%v", st.Project.PlanReviews, prTok == "")
	}
	rev := st.Project.PlanReviews[0].Run
	w.srv.SetStatus(rev.RunID, &hc.Status{App: "claude-code", State: "working"})
	return w, coordTok, prTok, rev.Code, rev.RunID
}

func planReviewJSON(t *testing.T, body map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPlanReviewReportValidationAndRoles(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: greet","estimate_lines":40,` + testPlanJSON + `}]}`
	w, coordTok, prTok, code, _ := launchPlanReview(t, plan)
	w.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks,
			flow.Task{ID: "TW", Title: "feat: w", Attempts: []flow.Attempt{{RunID: "w1", RunTokenHash: tokenHash("tok-w")}}},
			flow.Task{ID: "TR", Title: "feat: r", Attempts: []flow.Attempt{{Review: &flow.ReviewRun{Round: 1, Kind: flow.RoundFull, RunID: "r1", RunTokenHash: tokenHash("tok-r")}}}},
			flow.Task{ID: "TX", Title: "feat: x", Attempts: []flow.Attempt{{Review: &flow.ReviewRun{Round: 4, Kind: flow.RoundRetro, RunID: "x1", RunTokenHash: tokenHash("tok-ret")}}}},
		)
	})
	validPass := planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "findings": ""})
	for _, tc := range []struct {
		name, tok, verb, wantRole, wantOwner string
		body                                 json.RawMessage
	}{
		{"worker plan-review", "tok-w", "plan-review", flow.RoleWorker, flow.RolePlanReviewer, validPass},
		{"reviewer plan-review", "tok-r", "plan-review", flow.RoleReviewer, flow.RolePlanReviewer, validPass},
		{"retro plan-review", "tok-ret", "plan-review", flow.RoleRetro, flow.RolePlanReviewer, validPass},
		{"coordinator plan-review", coordTok, "plan-review", flow.RoleCoordinator, flow.RolePlanReviewer, validPass},
		{"plan reviewer plan reject", prTok, "plan reject", flow.RolePlanReviewer, flow.RoleCoordinator, json.RawMessage(`{"task":"T1","why":"keep"}`)},
	} {
		err := w.eng.Report(tc.tok, tc.verb, tc.body)
		if got := refusalCode(err); got != "kind_not_allowed" || !strings.Contains(err.Error(), tc.wantRole) || !strings.Contains(err.Error(), tc.wantOwner) {
			t.Errorf("%s: err=%v (code %q), want kind_not_allowed naming %s/%s", tc.name, err, got, tc.wantRole, tc.wantOwner)
		}
	}
	for _, tok := range []string{"", "tok-unknown"} {
		if got := refusalCode(w.eng.Report(tok, "plan-review", validPass)); got != "invalid_token" {
			t.Errorf("token %q plan-review = %q, want invalid_token", tok, got)
		}
	}
	seqBefore := w.state().Seq
	over64K := "delete: T1 " + strings.Repeat("a", 64*1024)
	for _, tc := range []struct {
		name string
		body json.RawMessage
	}{
		{"wrong code", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": "wrongcode0", "findings": ""})},
		{"task outside review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "delete: T999 outside review\n"})},
		{"unknown kind", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "rewrite: T1 unknown kind\n"})},
		{"pass with finding", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "findings": "delete: T1 optional\n"})},
		{"changes with no finding", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "just prose\n"})},
		{"findings over 64 KiB", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": over64K})},
		{"strict json unknown field", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "extra": 1})},
	} {
		if got := refusalCode(w.eng.Report(prTok, "plan-review", tc.body)); got != "invalid_report" {
			t.Errorf("%s: code=%q, want invalid_report", tc.name, got)
		}
		if st := w.state(); st.Seq != seqBefore || st.Project.PlanReviews[0].Verdict != "" || !st.Project.PlanReviews[0].Accepted.IsZero() || len(st.Project.PlanReviews[0].Findings) != 0 || st.Project.PlanReviews[0].Run.RunTokenHash == "" {
			t.Fatalf("%s mutated record: seq=%d/%d pr=%+v", tc.name, st.Seq, seqBefore, st.Project.PlanReviews[0])
		}
	}
	if err := w.eng.Report(prTok, "plan-review", validPass); err != nil {
		t.Fatalf("valid pass after refusals failed: %v", err)
	}
}

func TestPlanReviewPassScopedClearingAndJ15Stop(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: first","estimate_lines":40,` + testPlanJSON + `}]}`
	w, coordTok, prTok, code, runID := launchPlanReview(t, plan)
	amend := `{"why":"add second while review runs","tasks":[{"id":"T2","title":"feat: second","estimate_lines":30,` + testPlanJSON + `}]}`
	if err := w.eng.Report(coordTok, "plan amend", json.RawMessage(amend)); err != nil {
		t.Fatalf("plan amend during review: %v", err)
	}
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "findings": ""})); err != nil {
		t.Fatalf("plan-review pass: %v", err)
	}
	st := w.state()
	if pr := st.Project.PlanReviews[0]; pr.Verdict != flow.VerdictPass || pr.Accepted.IsZero() || pr.Run == nil || pr.Run.RunTokenHash != "" || pr.Run.RunID != runID || st.Tasks[0].Unreviewed || !st.Tasks[1].Unreviewed {
		t.Fatalf("after pass: pr=%+v T1.unrev=%v T2.unrev=%v", pr, st.Tasks[0].Unreviewed, st.Tasks[1].Unreviewed)
	}
	if got := refusalCode(w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code}))); got != "invalid_token" {
		t.Fatalf("second report with old token = %q, want invalid_token", got)
	}
	var failKill atomic.Bool
	failKill.Store(true)
	w.eng.HC = hc.NewClient(methodFailProxy(t, w.srv.Path, "terminal.kill", &failKill))
	w.pass()
	if pr := w.state().Project.PlanReviews[0]; pr.Verdict != flow.VerdictPass || pr.Accepted.IsZero() || pr.Run == nil || pr.Run.RunID != runID {
		t.Fatalf("failed J15 stop touched pass record: %+v", pr)
	}
	failKill.Store(false)
	w.pass()
	st = w.state()
	if pr, rs, probe := st.Project.PlanReviews[0], func() flow.RunState { r, _ := termState(w.ctx, w.client, runID); return r }(), world.SweepTerminals(w.ctx, w.client, st); pr.Verdict != flow.VerdictPass || (pr.Run != nil && pr.Run.RunID != "") || rs != flow.RunGone || probe.Unknown != "" || len(probe.Items) != 0 {
		t.Fatalf("after J15 stop: pr=%+v term=%s sweep=%+v", pr, rs, probe)
	}
	w.pass()
	st = w.state()
	if len(st.Tasks[0].Attempts) != 1 || len(st.Tasks[1].Attempts) != 0 || !st.Tasks[1].Unreviewed || len(st.Project.PlanReviews) != 2 || !slices.Equal(st.Project.PlanReviews[1].Tasks, []string{"T2"}) {
		t.Fatalf("after pass+J15: T1.att=%d T2.att=%d T2.unrev=%v reviews=%+v", len(st.Tasks[0].Attempts), len(st.Tasks[1].Attempts), st.Tasks[1].Unreviewed, st.Project.PlanReviews)
	}
}

func TestPlanReviewChangesWakeClosingAndAccept(t *testing.T) {
	plan := `{"tasks":[{"id":"T0","title":"feat: ended","brief":"b0","estimate_lines":20,` + testPlanJSON + `},{"id":"T1","title":"feat: opt","brief":"b1","estimate_lines":40,` + testPlanJSON + `},{"id":"T2","title":"feat: core","brief":"b2","estimate_lines":50,` + testPlanJSON + `},{"id":"T3","title":"feat: big","brief":"b3","estimate_lines":80,` + testPlanJSON + `}]}`
	w, coordTok, prTok, code, runID := launchPlanReview(t, plan)
	if err := w.eng.Report(coordTok, "task", json.RawMessage(`{"action":"drop","task":"T0","why":"dropped mid-review"}`)); err != nil {
		t.Fatalf("drop T0: %v", err)
	}
	findings := "delete: T0 already dropped\ndelete: T1 optional backlog item\nshrink: T1 also oversized\nshrink: T2 narrow scope\ndelete: T2 could also drop\nsplit: T3 split into two\n"
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": findings})); err != nil {
		t.Fatalf("plan-review changes: %v", err)
	}
	st := w.state()
	pr := st.Project.PlanReviews[0]
	if pr.Verdict != flow.VerdictChanges || !pr.Accepted.IsZero() || len(pr.Findings) != 6 || pr.Findings[0].Closed == "" || pr.Run == nil || pr.Run.RunTokenHash != "" || pr.Run.RunID != runID {
		t.Fatalf("after changes: pr=%+v", pr)
	}
	if got := refusalCode(w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code}))); got != "invalid_token" {
		t.Fatalf("second report after changes = %q, want invalid_token", got)
	}
	if len(st.Project.Wakes) != 1 {
		t.Fatalf("wakes after changes = %+v, want 1 project wake", st.Project.Wakes)
	}
	wakeText := st.Project.Wakes[0].Why + " " + st.Project.Wakes[0].Facts
	for _, want := range []string{"T1", "T2", "T3", "task drop", "plan amend", "plan reject", "brief", "outcomes"} {
		if !strings.Contains(wakeText, want) {
			t.Errorf("plan review wake missing %q:\n%s", want, wakeText)
		}
	}
	if got := ProjectsView([]flow.State{st})[0].Status; got != "5 plan findings open, waiting on the coordinator" {
		t.Errorf("ProjectsView Status = %q, want %q", got, "5 plan findings open, waiting on the coordinator")
	}
	for _, hb := range []bool{true, false} {
		if sum := coordSummary(st, "sheepdog", hb); !strings.Contains(sum, "5") || !strings.Contains(strings.ToLower(sum), "plan finding") {
			t.Errorf("coordSummary(hb=%v) missing open findings line:\n%s", hb, sum)
		}
	}
	w.pass() // J15 stops terminal after changes without touching verdict
	st = w.state()
	if pr, rs, probe := st.Project.PlanReviews[0], func() flow.RunState { r, _ := termState(w.ctx, w.client, runID); return r }(), world.SweepTerminals(w.ctx, w.client, st); pr.Verdict != flow.VerdictChanges || !pr.Accepted.IsZero() || (pr.Run != nil && pr.Run.RunID != "") || rs != flow.RunGone || probe.Unknown != "" || len(probe.Items) != 0 {
		t.Fatalf("after J15 on changes: pr=%+v term=%s sweep=%+v", pr, rs, probe)
	}
	if got := refusalCode(w.eng.Report(coordTok, "plan reject", json.RawMessage(`{"task":"T99","why":"x"}`))); got != "nothing_to_reject" {
		t.Errorf("reject no finding = %q, want nothing_to_reject", got)
	}
	if got := refusalCode(w.eng.Report(coordTok, "plan reject", json.RawMessage(`{"task":"T2","why":"  "}`))); got != "why_missing" {
		t.Errorf("reject blank why = %q, want why_missing", got)
	}
	if err := w.eng.Report(coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"optional"}`)); err != nil {
		t.Fatalf("task drop T1: %v", err)
	}
	for _, f := range w.state().Project.PlanReviews[0].Findings[1:3] {
		if f.Closed != "dropped" {
			t.Errorf("T1 finding %+v not closed as dropped", f)
		}
	}
	if err := w.eng.Report(coordTok, "plan amend", json.RawMessage(`{"why":"no-op","tasks":[{"id":"T2","brief":"b2"}]}`)); err != nil {
		t.Fatalf("no-op amend: %v", err)
	}
	if fs := w.state().Project.PlanReviews[0].Findings; fs[3].Closed != "" || fs[4].Closed != "" {
		t.Fatalf("no-op amend closed findings: %+v", fs[3:5])
	}
	if err := w.eng.Report(coordTok, "plan amend", json.RawMessage(`{"why":"narrow T2","tasks":[{"id":"T2","brief":"b2 smaller"}]}`)); err != nil {
		t.Fatalf("real amend T2: %v", err)
	}
	if fs := w.state().Project.PlanReviews[0].Findings; fs[3].Closed != "amended" || fs[4].Closed != "" {
		t.Fatalf("after amend T2: shrink=%+v delete=%+v, want amended/open", fs[3], fs[4])
	}
	if err := w.eng.Report(coordTok, "plan reject", json.RawMessage(`{"task":"T2","why":"core feature needed"}`)); err != nil {
		t.Fatalf("plan reject T2: %v", err)
	}
	if f := w.state().Project.PlanReviews[0].Findings[4]; f.Closed != "rejected" || f.RejectWhy != "core feature needed" {
		t.Fatalf("rejected finding = %+v", f)
	}
	if got := refusalCode(w.eng.Report(coordTok, "plan reject", json.RawMessage(`{"task":"T2","why":"again"}`))); got != "nothing_to_reject" {
		t.Errorf("second plan reject T2 = %q, want nothing_to_reject", got)
	}
	w.pass() // 1 finding (T3) still open -> must not accept
	if !w.state().Project.PlanReviews[0].Accepted.IsZero() {
		t.Fatal("accepted with 1 open finding remaining")
	}
	if err := w.eng.Report(coordTok, "plan amend", json.RawMessage(`{"why":"trim T3","tasks":[{"id":"T3","estimate_lines":40}]}`)); err != nil {
		t.Fatalf("amend T3: %v", err)
	}
	w.pass() // J16 accepts once last finding closed; keeps rejected finding; no second review
	st = w.state()
	if pr := st.Project.PlanReviews[0]; len(st.Project.PlanReviews) != 1 || pr.Accepted.IsZero() || pr.Findings[4].Closed != "rejected" || pr.Findings[4].RejectWhy != "core feature needed" || st.Tasks[2].Unreviewed || st.Tasks[3].Unreviewed {
		t.Fatalf("after J16 accept: reviews=%+v T2.unrev=%v T3.unrev=%v", st.Project.PlanReviews, st.Tasks[2].Unreviewed, st.Tasks[3].Unreviewed)
	}
	if got := ProjectsView([]flow.State{st})[0].Status; got != "" {
		t.Errorf("status after accept = %q, want empty", got)
	}
}

func TestPlanReviewTimeBoundUnanswered(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: held","brief":"b1","estimate_lines":40,` + testPlanJSON + `}]}`
	w, _, prTok, code, _ := launchPlanReview(t, plan)
	w.edit(func(s *flow.State) { s.Settings.HeartbeatMinutes = 10 })
	t0 := w.now
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "shrink: T1 too broad\n"})); err != nil {
		t.Fatalf("plan-review changes: %v", err)
	}
	w.pass() // J15 stops terminal
	w.pass() // J12 delivers wake
	w.now = t0.Add(11 * time.Minute)
	w.pass() // J13 heartbeat (CoordLastWakeAt = t0+11m); live, idle coordinator
	w.now = t0.Add(19 * time.Minute)
	w.pass() // one pass earlier than 2*10m (J13 not due): must not accept
	if st := w.state(); !st.Project.PlanReviews[0].Accepted.IsZero() || st.Project.PlanReviews[0].Findings[0].Closed != "" || !st.Tasks[0].Unreviewed {
		t.Fatalf("accepted one pass early at +19m: pr=%+v unrev=%v", st.Project.PlanReviews[0], st.Tasks[0].Unreviewed)
	}
	w.restart() // survives daemon restart: bound read from record, not memory
	w.now = t0.Add(20 * time.Minute)
	w.pass()
	st, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if pr := st.Project.PlanReviews[0]; pr.Accepted.IsZero() || pr.Findings[0].Closed != "unanswered" || st.Tasks[0].Unreviewed {
		t.Fatalf("at 2x heartbeat_minutes: pr=%+v unrev=%v", pr, st.Tasks[0].Unreviewed)
	}
	countBound := func(ls []LogLine) (n int) {
		for _, l := range ls {
			if l.Rule == "J16" && strings.Contains(strings.ToLower(l.Decision+" "+l.Why), "unanswered") {
				n++
			}
		}
		return n
	}
	if n := countBound(lines); n != 1 {
		t.Fatalf("unanswered J16 log lines = %d, want 1", n)
	}
	w.pass() // fires once
	if _, lines2 := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id)); countBound(lines2) != 1 {
		t.Fatalf("after extra pass unanswered J16 log lines = %d, want 1", countBound(lines2))
	}
}

func TestStore_PlanReviewAcceptRejectAndCloseCommitContract(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: one","brief":"b1","estimate_lines":40,` + testPlanJSON + `},{"id":"T2","title":"feat: two","brief":"b2","estimate_lines":40,` + testPlanJSON + `},{"id":"T3","title":"feat: three","brief":"b3","estimate_lines":40,` + testPlanJSON + `}]}`
	failOnce := func(w *storeWorld) *atomic.Bool {
		var failed atomic.Bool
		w.setWriteHooks(func(path string, s flow.State) error {
			if failed.CompareAndSwap(false, true) {
				return errors.New("injected state write failure")
			}
			return SaveState(path, s)
		}, nil)
		return &failed
	}
	w, coordTok, prTok, code, _ := launchPlanReview(t, plan)
	seq := w.state().Seq
	body := planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "delete: T1 opt\ndelete: T2 opt2\nshrink: T3 big\n"})
	f0 := failOnce(w)
	if err := w.eng.Report(prTok, "plan-review", body); err == nil || !f0.Load() {
		t.Fatalf("acceptPlanReviewLocked failed write: err=%v failed=%v", err, f0.Load())
	}
	if st := w.state(); st.Seq != seq || st.Project.PlanReviews[0].Verdict != "" || st.Project.PlanReviews[0].Run.RunTokenHash == "" || len(st.Project.Wakes) != 0 {
		t.Fatalf("failed acceptPlanReviewLocked mutated state: seq=%d/%d pr=%+v wakes=%d", st.Seq, seq, st.Project.PlanReviews[0], len(st.Project.Wakes))
	}
	if visible, _, err := ProjectLog(w.ctx, w.paths, w.repo, 0); err != nil || slices.ContainsFunc(visible, func(l LogLine) bool { return l.Rule == "plan-review" }) {
		t.Fatalf("ProjectLog exposed uncommitted plan-review line: err=%v", err)
	}
	w.setWriteHooks(nil, nil)
	if err := w.eng.Report(prTok, "plan-review", body); err != nil {
		t.Fatalf("retry plan-review: %v", err)
	}
	for _, tc := range []struct {
		name, verb, arg, wantClosed string
		idx                         int
		unchanged                   func(flow.State) bool
	}{
		{"acceptPlanRejectLocked", "plan reject", `{"task":"T1","why":"needed"}`, "rejected", 0, func(s flow.State) bool { return s.Project.PlanReviews[0].Findings[0].RejectWhy == "" }},
		{"closePlanFindings drop", "task", `{"action":"drop","task":"T2","why":"opt2"}`, "dropped", 1, func(s flow.State) bool { return s.Tasks[1].Dropped == nil }},
		{"closePlanFindings amend", "plan amend", `{"why":"shrink","tasks":[{"id":"T3","brief":"b3 smaller"}]}`, "amended", 2, func(s flow.State) bool { return s.Tasks[2].Brief == "b3" }},
	} {
		seq = w.state().Seq
		f := failOnce(w)
		if err := w.eng.Report(coordTok, tc.verb, json.RawMessage(tc.arg)); err == nil || !f.Load() {
			t.Fatalf("%s failed write: err=%v failed=%v", tc.name, err, f.Load())
		}
		if st := w.state(); st.Seq != seq || st.Project.PlanReviews[0].Findings[tc.idx].Closed != "" || !tc.unchanged(st) {
			t.Fatalf("%s mutated state on failed write: seq=%d/%d f=%+v", tc.name, st.Seq, seq, st.Project.PlanReviews[0].Findings[tc.idx])
		}
		w.setWriteHooks(nil, nil)
		if err := w.eng.Report(coordTok, tc.verb, json.RawMessage(tc.arg)); err != nil {
			t.Fatalf("%s retry: %v", tc.name, err)
		}
		if st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id)); st.Project.PlanReviews[0].Findings[tc.idx].Closed != tc.wantClosed {
			t.Fatalf("%s after retry closed=%q, want %q", tc.name, st.Project.PlanReviews[0].Findings[tc.idx].Closed, tc.wantClosed)
		}
	}
}
