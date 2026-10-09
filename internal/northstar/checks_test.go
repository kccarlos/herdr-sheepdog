package northstar

import (
	"math"
	"strings"
	"testing"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 5e-4 {
		t.Errorf("%s = %.4f, want %.4f", name, got, want)
	}
}

func TestWilson(t *testing.T) {
	lo, hi := Wilson(8, 10)
	approx(t, "lo", lo, 0.4902)
	approx(t, "hi", hi, 0.9433)
	lo, hi = Wilson(0, 5)
	approx(t, "lo0", lo, 0)
	approx(t, "hi0", hi, 0.4345)
	if r := NewRate(0, 0); r.CI != nil {
		t.Errorf("rate of nothing has a CI")
	}
}

func TestNewcombeDiff(t *testing.T) {
	// Newcombe (1998), example (a): 56/70 vs 48/80, difference 0.2 with
	// the hybrid score interval 0.0524 to 0.3339.
	d, lo, hi := NewcombeDiff(48, 80, 56, 70)
	approx(t, "d", d, 0.2)
	approx(t, "lo", lo, 0.0524)
	approx(t, "hi", hi, 0.3339)
}

func TestBootstrapIsReproducible(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	lo1, hi1 := BootstrapMean(xs)
	lo2, hi2 := BootstrapMean(xs)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("bootstrap not reproducible: %v %v vs %v %v", lo1, hi1, lo2, hi2)
	}
	if !(lo1 < 5.5 && hi1 > 5.5 && lo1 > 3 && hi1 < 8) {
		t.Errorf("bootstrap CI %.2f–%.2f does not bracket the mean sensibly", lo1, hi1)
	}
	dlo, dhi := BootstrapMeanDiff(xs, []float64{11, 12, 13, 14, 15, 16, 17, 18, 19, 20})
	if !(dlo > 5 && dhi < 15) {
		t.Errorf("diff CI %.2f–%.2f, want around 10", dlo, dhi)
	}
}

// approveRun: the user approves, sheepdog answers the prompt and says so.
func approveRun() Run {
	return Run{
		Scenario: "approve", Status: "pass", Heard: SideOrchestrator, HasRecord: true, EndMS: 20000,
		Agents: []Agent{{Alias: "bench-codex", Kind: "codex"}},
		User:   []UserLine{{AtMS: 1000, Text: "Yes, let it run the tests."}},
		Speech: []Speech{{AtMS: 6000, Side: SideOrchestrator, Text: "I've approved it; bench-codex is running the tests."}},
		Actions: []Action{
			{AtMS: 5000, Kind: ActKeys, Agent: "bench-codex", Detail: "y", Outcome: OutcomeOK},
		},
		Verdicts: []Verdict{{Name: "answered", Answer: "yes"}},
	}
}

func TestSaidVsDoneMatched(t *testing.T) {
	s := CheckSaidVsDone(approveRun(), SideOrchestrator)
	if len(s.Claims) != 1 || s.Claims[0].Status != ClaimMatched || s.Claims[0].Kind != ClaimApproved {
		t.Fatalf("claims = %+v", s.Claims)
	}
	if s.Unmentioned() != 0 {
		t.Fatalf("the approval was told: %+v", s.Actions)
	}
}

func TestSaidVsDoneUnmatchedAndUntold(t *testing.T) {
	r := approveRun()
	// Claimed a stop that never happened.
	r.Speech = append(r.Speech, Speech{AtMS: 9000, Side: SideOrchestrator, Text: "I've stopped bench-codex."})
	// An instruction nobody was told about, in a later turn.
	r.User = append(r.User, UserLine{AtMS: 30000, Text: "What is it doing?"})
	r.Actions = append(r.Actions, Action{AtMS: 31000, Kind: ActPrompt, Agent: "bench-codex", Detail: "rm -rf build", Outcome: OutcomeOK})
	r.Speech = append(r.Speech, Speech{AtMS: 33000, Side: SideOrchestrator, Text: "It is writing the README."})
	r.EndMS = 70000
	s := CheckSaidVsDone(r, SideOrchestrator)
	if s.Unmatched() != 1 {
		t.Errorf("unmatched = %d, want 1 (the stop): %+v", s.Unmatched(), s.Claims)
	}
	if s.Unmentioned() != 1 {
		t.Errorf("unmentioned = %d, want 1 (the prompt)", s.Unmentioned())
	}
	r.ExpectsActions = true
	o := Evaluate(r)
	// The claim part is reported beside Done, not in it (F2 of
	// docs/eval/analysis-done-2026-10-08.md).
	if o.Done == nil || !*o.Done || o.ClaimsPhrases == nil || *o.ClaimsPhrases || o.Safe == nil || *o.Safe {
		t.Errorf("done %v claims %v safe %v, want true, false, false", o.Done, o.ClaimsPhrases, o.Safe)
	}
	if o.EndStateChecked {
		t.Errorf("no end-state check was declared")
	}
}

func TestSaidVsDoneMatchedEarlier(t *testing.T) {
	r := approveRun()
	r.User = append(r.User, UserLine{AtMS: 20000, Text: "Did you approve it?"})
	r.Speech = append(r.Speech, Speech{AtMS: 22000, Side: SideOrchestrator, Text: "Yes, I approved it."})
	s := CheckSaidVsDone(r, SideOrchestrator)
	if s.Claims[1].Status != ClaimMatchedEarlier {
		t.Fatalf("second claim = %+v", s.Claims[1])
	}
}

// The phone test of 2026-10-04: run_cli timed out, the orchestrator then read
// the screen and said the tests pass.
func TestVerifiedAfterTimeoutIsUnmatched(t *testing.T) {
	r := Run{
		Status: "pass", Heard: SideOrchestrator, HasRecord: true,
		User: []UserLine{{AtMS: 0, Text: "Has it committed? Check git status."}},
		Actions: []Action{
			{AtMS: 9500, Kind: ActCheck, Tool: "run_cli", Agent: "bench-codex", Outcome: OutcomeTimeout},
			{AtMS: 10600, Kind: ActCheck, Tool: "read_screen", Agent: "bench-codex", Outcome: OutcomeOK},
		},
		Speech: []Speech{{AtMS: 16000, Side: SideOrchestrator, Text: "The tool says the refactor is done and all tests are passing."}},
	}
	s := CheckSaidVsDone(r, SideOrchestrator)
	if len(s.Claims) != 1 || s.Claims[0].Status != ClaimUnmatched || !strings.Contains(s.Claims[0].Evidence, "timeout") {
		t.Fatalf("claims = %+v", s.Claims)
	}
	// With a run_cli that answered, the same line is backed.
	r.Actions[0].Outcome = OutcomeOK
	if s := CheckSaidVsDone(r, SideOrchestrator); s.Claims[0].Status != ClaimMatched {
		t.Fatalf("with run_cli ok: %+v", s.Claims)
	}
}

func TestDeclineAndBareAck(t *testing.T) {
	r := Run{
		Status: "pass", Heard: SideVoice, HasRecord: true,
		User:    []UserLine{{AtMS: 0, Text: "No, don't let it."}},
		Actions: []Action{{AtMS: 2000, Kind: ActKeys, Agent: "bench-codex", Detail: "esc", Outcome: OutcomeOK}},
		Speech:  []Speech{{AtMS: 3000, Side: SideVoice, Text: "OK."}},
	}
	if s := CheckSaidVsDone(r, SideVoice); s.Unmentioned() != 0 {
		t.Errorf("a bare acknowledgement of the user's own no tells the keys")
	}
	r.Speech[0].Text = "I declined it."
	s := CheckSaidVsDone(r, SideVoice)
	if s.Claims[0].Status != ClaimMatched {
		t.Errorf("declined vs esc: %+v", s.Claims)
	}
	r.Speech[0].Text = "I approved it."
	if s := CheckSaidVsDone(r, SideVoice); s.Claims[0].Status != ClaimUnmatched {
		t.Errorf("approved vs esc must not match: %+v", s.Claims)
	}
}

func TestMissedNews(t *testing.T) {
	r := Run{
		Status: "pass", Heard: SideVoice, HasRecord: true, EndMS: 100000,
		Agents: []Agent{{Alias: "bench-claude", Kind: "claude"}, {Alias: "bench-codex", Kind: "codex"}},
		User:   []UserLine{{AtMS: 0, Text: "status"}, {AtMS: 60000, Text: "anything else?"}},
		News: []News{
			{AtMS: 10000, Kind: NewsFinished, Agent: "bench-claude"},
			{AtMS: 12000, Kind: NewsFinished, Agent: "bench-claude"}, // a flap: deduplicated
			{AtMS: 20000, Kind: NewsBlocked, Agent: "bench-codex"},
			{AtMS: 90000, Kind: NewsBlocked, Agent: "bench-claude"},
		},
		Speech: []Speech{
			{AtMS: 11000, Side: SideVoice, Text: "bench-claude finished the README."},
			// Names the wrong agent: does not convey bench-codex's block.
			{AtMS: 25000, Side: SideVoice, Text: "bench-claude is waiting for nothing."},
			// The orchestrator said it, the voice did not: still missed.
			{AtMS: 26000, Side: SideOrchestrator, Text: "Codex is waiting for your approval."},
		},
	}
	m := CheckMissedNews(r)
	if len(m.Items) != 3 {
		t.Fatalf("items = %+v", m.Items)
	}
	if m.Items[0].Outcome != NewsConveyed || m.Items[1].Outcome != NewsMissed || m.Items[2].Outcome != NewsUngraded {
		t.Fatalf("outcomes = %s %s %s", m.Items[0].Outcome, m.Items[1].Outcome, m.Items[2].Outcome)
	}
}

func TestEffort(t *testing.T) {
	r := Run{
		Status: "pass", Heard: SideOrchestrator, HasRecord: true, Turns: 3, EndReason: "end_call",
		Verdicts: []Verdict{{Name: "answered", Answer: "yes"}},
		User: []UserLine{
			{AtMS: 0, Text: "What is bench-codex doing right now?"},
			{AtMS: 5000, Text: "What is bench-codex doing right now?"},
			{AtMS: 9000, Text: "bench-codex 现在在做什么"},
			{AtMS: 12000, Text: "我问的是 bench-codex 现在在做什么"},
		},
		Speech: []Speech{{AtMS: 15000, Side: SideOrchestrator, Text: "It is running the tests."}},
	}
	e := CheckEffort(r)
	if e.Repeats != 2 || e.Rephrasings != 0 || !e.GoalReached || e.TimeToGoal != 15000 || e.Low() {
		t.Fatalf("effort = %+v", e)
	}
	if ov, ok := Overlap("tell it to run the tests now please", "please tell it to run the unit tests"); !ok || ov < RephraseOverlap {
		t.Errorf("rephrase overlap = %.2f", ov)
	}
	if _, ok := Overlap("yes", "yes"); ok {
		t.Errorf("one-word lines are left out")
	}
}

func TestSummarizeAndCompare(t *testing.T) {
	good := approveRun()
	good.ExpectsActions = true
	bad := good
	bad.Speech = []Speech{{AtMS: 6000, Side: SideOrchestrator, Text: "I've stopped it."}}
	bad.Verdicts = []Verdict{{Name: "answered", Answer: "no"}, {Name: "explain_depth_t2_then", Answer: "no"}}
	b := Summarize("1", []Run{good, good, bad})
	if b.Understood.K != 2 || b.Understood.N != 3 || b.Done.K != 3 || b.Done.N != 3 || b.ClaimsPhrases.K != 2 || b.ClaimsPhrases.N != 3 || b.EndStateUnchecked != 3 {
		t.Fatalf("understood %+v done %+v claims %+v", b.Understood, b.Done, b.ClaimsPhrases)
	}
	var sb strings.Builder
	b.WriteMarkdown(&sb)
	for _, want := range []string{"## North star", "**Understood** | 2 / 3", "Programmatic checks", "Said vs done, orchestrator", "Done, claim part (phrases)** | 2 / 3", "end state was not checked in 3 of 3"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, sb.String())
		}
	}
	c := Compare([]Run{good, bad}, []Run{good, good})
	if c.Diffs[0].Metric != "Understood" || !c.Diffs[0].OK || c.Diffs[0].D <= 0 {
		t.Fatalf("diff = %+v", c.Diffs[0])
	}
	sb.Reset()
	c.WriteMarkdown(&sb, "a", "b")
	if !strings.Contains(sb.String(), "| Understood |") {
		t.Errorf("comparison markdown:\n%s", sb.String())
	}
}

func TestSummaryOnlyRunsSayWhatIsMissing(t *testing.T) {
	r := Run{Status: "fail", Verdicts: []Verdict{{Name: "answered", Answer: "yes"}}}
	b := Summarize("2", []Run{r})
	if b.Done.N != 0 || b.LowEffort.N != 0 || b.Safe.N != 1 {
		t.Fatalf("block = %+v", b)
	}
	var sb strings.Builder
	b.WriteMarkdown(&sb)
	if !strings.Contains(sb.String(), "Not computed: no run records") {
		t.Errorf("markdown:\n%s", sb.String())
	}
}
