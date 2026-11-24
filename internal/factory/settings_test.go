package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestSettingsRowsAndProposeRefusals verifies Engine.SettingRows (defaults for
// no project and per-project rows) and Engine.ProposeSetting refusals (SE2, SE4).
func TestSettingsRowsAndProposeRefusals(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	defRows, err := r.eng.SettingRows("")
	if err != nil || len(defRows) != len(flow.DefaultSettings().Rows()) {
		t.Fatalf("SettingRows(\"\") = (%d, %v)", len(defRows), err)
	}
	if rows, err := r.eng.SettingRows(r.id); err != nil || len(rows) != len(defRows) {
		t.Fatalf("SettingRows(%q) = (%d, %v)", r.id, len(rows), err)
	}
	if _, err := r.eng.SettingRows("no-such-project"); !errors.Is(err, ErrProjectNotFound) && refusalCode(err) != "not_found" {
		t.Fatalf("SettingRows(unknown) = %v, want not_found", err)
	}
	for _, tc := range []struct{ key, val, dev, want string }{
		{"review_rounds", "3", "", "proof_required"},
		{"review_rounds", "3", "local", "proof_required"},
		{"review_rounds", "0", "", "proof_required"},
		{"review_rounds", "2", "dev-browser-1", "unchanged"},
		{"review_rounds", "0", "dev-browser-1", "out_of_range"},
		{"no_such_setting", "1", "dev-browser-1", "unknown_setting"},
	} {
		if _, err := r.eng.ProposeSetting(r.id, tc.key, tc.val, tc.dev); refusalCode(err) != tc.want {
			t.Errorf("ProposeSetting(%q,%q,%q) = %v, want %q", tc.key, tc.val, tc.dev, err, tc.want)
		}
	}
	if _, err := r.eng.ProposeSetting("no-such-project", "review_rounds", "3", "dev-browser-1"); !errors.Is(err, ErrProjectNotFound) && refusalCode(err) != "not_found" {
		t.Errorf("unknown project ProposeSetting = %v, want not_found", err)
	}
	if len(r.state().Asks) != 0 {
		t.Fatalf("refused proposals mutated Asks: %+v", r.state().Asks)
	}
}

// TestSettingsProposeStoppedFinishedAndCancelPrevious covers SE5 and SE13/M4.
func TestSettingsProposeStoppedFinishedAndCancelPrevious(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stopped", "finished"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := newAskRig(t)
			r.edit(func(s *flow.State) { s.Project.Started, s.Project.Finished = mode == "finished", mode == "finished" })
			a1, err := r.eng.ProposeSetting(r.id, "review_rounds", "3", "dev-browser-1")
			if err != nil {
				t.Fatalf("ProposeSetting on %s: %v", mode, err)
			}
			a2, err := r.eng.ProposeSetting(r.id, "review_rounds", "4", "dev-browser-1")
			if err != nil {
				t.Fatalf("second ProposeSetting on %s: %v", mode, err)
			}
			if st := r.state(); len(st.Asks) != 2 || st.Asks[0].ID != a1.ID || st.Asks[0].Status != flow.AskStatusCancelled ||
				st.Asks[1].ID != a2.ID || st.Asks[1].Status != flow.AskStatusOpen ||
				st.Asks[1].Set == nil || *st.Asks[1].Set != (flow.SettingChange{Name: "review_rounds", Value: "4", Was: "2"}) {
				t.Fatalf("asks after replacement = %+v", st.Asks)
			}
			if prf, err := r.eng.MintProof(a2.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1"); err != nil ||
				r.eng.Answer(a2.ID, flow.RuleOptionSave, prf) != nil || r.state().Settings.ReviewRounds != 4 {
				t.Fatalf("Save on %s failed: err=%v rounds=%d", mode, err, r.state().Settings.ReviewRounds)
			}
		})
	}
}

// TestSettingsSaveKeepLogsAndFailedWrite verifies single-commit Save, log lines
// (engine/A2/notify_owner; owner/A3), failed writeState across restart, no wake, and Keep current.
func TestSettingsSaveKeepLogsAndFailedWrite(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	r.edit(func(s *flow.State) { s.Settings.MaxRunning = 1 })
	ask, err := r.eng.ProposeSetting(r.id, "max_running", "2", "dev-browser-1")
	if err != nil {
		t.Fatalf("ProposeSetting: %v", err)
	}
	wantQ := flow.RuleQuestion(flow.SettingChange{Name: "max_running", Value: "2", Was: "1"})
	if ask.Kind != flow.KindRule || !ask.Gated || ask.Question != wantQ || ask.Recommend != flow.RuleOptionSave ||
		!slices.Equal(ask.Options, []string{flow.RuleOptionSave, flow.RuleOptionKeep}) || ask.Set == nil {
		t.Fatalf("proposed ask = %+v", ask)
	}
	lines := readRawLogLines(t, r.paths.Log(r.id))
	if pl := lines[len(lines)-1]; pl.By != "engine" || pl.Rule != "A2" || !slices.Contains(pl.Asked, flow.AskNotifyOwner) {
		t.Fatalf("proposal log = %+v, want by=engine rule=A2 asked=[notify_owner]", pl)
	}
	prf, err := r.eng.MintProof(ask.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	seqBefore := r.state().Seq
	r.eng.writeState = func(string, flow.State) error { return errors.New("disk write failed") }
	if err := r.eng.Answer(ask.ID, flow.RuleOptionSave, prf); err == nil {
		t.Fatal("Answer(Save) succeeded despite writeState failure")
	}
	r.restart()
	// The failed commit's log line is trimmed on the restarted engine's next read (S4), so read state.json alone here.
	if st := r.state(); st.Seq != seqBefore || st.Settings.MaxRunning != 1 || st.Asks[0].Status != flow.AskStatusOpen {
		t.Fatalf("state mutated after failed writeState: seq=%d max=%d ask=%+v", st.Seq, st.Settings.MaxRunning, st.Asks[0])
	}
	coordBefore := len(sendsTo(t, r.srv.Requests(), r.coordAddr))
	if err := r.eng.Answer(ask.ID, flow.RuleOptionSave, prf); err != nil {
		t.Fatalf("Answer(Save): %v", err)
	}
	stSaved, linesAfter := assertLogInvariants(t, r.paths.State(r.id), r.paths.Log(r.id))
	if stSaved.Seq != seqBefore+1 || stSaved.Settings.MaxRunning != 2 || stSaved.Asks[0].Status != flow.AskStatusAnswered || len(stSaved.Project.Wakes) != 0 {
		t.Fatalf("after Save: seq=%d max=%d ask=%+v wakes=%+v", stSaved.Seq, stSaved.Settings.MaxRunning, stSaved.Asks[0], stSaved.Project.Wakes)
	}
	sl := linesAfter[len(linesAfter)-1]
	if inBytes, _ := json.Marshal(sl.Input); sl.By != "owner" || sl.Rule != "A3" || !strings.Contains(sl.Decision, "max_running") ||
		!strings.Contains(sl.Decision, "2") || !strings.Contains(sl.Decision, "1") || !strings.Contains(string(inBytes), "dev-browser-1") {
		t.Fatalf("save log = %+v (input=%s)", sl, inBytes)
	}
	r.pass()
	if sends := sendsTo(t, r.srv.Requests(), r.coordAddr)[coordBefore:]; len(sends) != 0 {
		t.Fatalf("saved setting woke coordinator: %q", sends)
	}
	askKeep, err := r.eng.ProposeSetting(r.id, "max_running", "4", "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	if prfKeep, err := r.eng.MintProof(askKeep.ID, flow.RuleOptionKeep, ProofInputTap, "dev-browser-1"); err != nil ||
		r.eng.Answer(askKeep.ID, flow.RuleOptionKeep, prfKeep) != nil {
		t.Fatalf("Answer(Keep current) failed: %v", err)
	}
	if st := r.state(); st.Settings.MaxRunning != 2 || len(st.Project.Wakes) != 0 {
		t.Fatalf("Keep current mutated settings/wakes: max=%d wakes=%+v", st.Settings.MaxRunning, st.Project.Wakes)
	}
}

// TestSettingsGuardsCoordinatorTableMovedAndJ4Cancel verifies coordinator Save
// proof_required, run rule ask rejecting set, free-text rule ask Save no-op,
// Save after table moved staying open, and J4 cancelling open proposals.
func TestSettingsGuardsCoordinatorTableMovedAndJ4Cancel(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	badSet := `{"kind":"rule","question":"Change?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"review_rounds","value":"4","was":"2"}}`
	if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(badSet)); refusalCode(err) != "invalid_report" {
		t.Fatalf("coordinator rule ask with set = %v, want invalid_report", err)
	}
	freeRule := `{"kind":"rule","question":"This project: review rounds 4 (now 2). Save?","options":["Save","Keep current"],"recommend":"Save"}`
	if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(freeRule)); err != nil {
		t.Fatalf("coordinator free-text rule ask: %v", err)
	}
	freeAsk := r.state().Asks[0]
	if prfFree, _ := r.eng.MintProof(freeAsk.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1"); freeAsk.Set != nil ||
		r.eng.Answer(freeAsk.ID, flow.RuleOptionSave, prfFree) != nil || r.state().Settings.ReviewRounds != 2 {
		t.Fatalf("free-text rule ask Save mutated state: set=%+v rounds=%d", freeAsk.Set, r.state().Settings.ReviewRounds)
	}
	prop, err := r.eng.ProposeSetting(r.id, "review_rounds", "4", "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Save","why":"ok"}`, prop.ID))); refusalCode(err) != "proof_required" {
		t.Fatalf("coordinator answering setting ask = %v, want proof_required", err)
	}
	r.edit(func(s *flow.State) { s.Settings.MaxRounds = 3 })
	prfMoved, _ := r.eng.MintProof(prop.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1")
	if err := r.eng.Answer(prop.ID, flow.RuleOptionSave, prfMoved); refusalCode(err) != "out_of_range" || !strings.Contains(err.Error(), "review rounds") {
		t.Fatalf("Save after table moved = %v, want out_of_range naming review rounds", err)
	}
	if st := r.state(); st.Settings.ReviewRounds != 2 || st.Asks[1].Status != flow.AskStatusOpen {
		t.Fatalf("Save after table moved mutated state: rounds=%d ask=%+v", st.Settings.ReviewRounds, st.Asks[1])
	}
	h := newProjectHarness(t, "Finish cancels proposal")
	h.pass()
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: done", Done: &flow.Merged{MergedCommit: "m1"}}}
	})
	finProp, err := h.eng.ProposeSetting(h.id, "review_rounds", "3", "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	h.finishThroughJ4(coord1)
	if finSt := h.state(); len(finSt.Asks) != 1 || finSt.Asks[0].ID != finProp.ID || finSt.Asks[0].Status != flow.AskStatusCancelled {
		t.Fatalf("proposal ask after J4 finish = %+v, want cancelled", finSt.Asks)
	}
	for _, a := range h.eng.Asks(true) {
		if a.Set != nil && a.Set.Name == "review_rounds" {
			t.Fatalf("open ask still waiting after J4 finish: %+v", a)
		}
	}
}

func TestSettingsSaveEmptyValueLogDecision(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	a1, err := r.eng.ProposeSetting(r.id, "design_gate", "owner", "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	p1, err := r.eng.MintProof(a1.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Answer(a1.ID, flow.RuleOptionSave, p1); err != nil {
		t.Fatal(err)
	}
	lines := readRawLogLines(t, r.paths.Log(r.id))
	if got := lines[len(lines)-1].Decision; got != "saved design_gate owner (was empty)" {
		t.Errorf("save from empty decision = %q, want %q", got, "saved design_gate owner (was empty)")
	}
	a2, err := r.eng.ProposeSetting(r.id, "design_gate", "", "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.eng.MintProof(a2.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Answer(a2.ID, flow.RuleOptionSave, p2); err != nil {
		t.Fatal(err)
	}
	lines = readRawLogLines(t, r.paths.Log(r.id))
	if got := lines[len(lines)-1].Decision; got != "saved design_gate empty (was owner)" {
		t.Errorf("save to empty decision = %q, want %q", got, "saved design_gate empty (was owner)")
	}
}

func TestSettingsRowsNilOnError(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	if rows, err := r.eng.SettingRows("no-such-project"); err == nil || rows != nil {
		t.Fatalf("SettingRows(unknown) = (len=%d, %v), want (nil, error)", len(rows), err)
	}
}
