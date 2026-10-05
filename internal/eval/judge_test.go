package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

func TestParseVerdict(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ans  Answer
		conf float64
		err  bool
	}{
		{`{"verdict":"yes","confidence":0.92}`, AnswerYes, 0.92, false},
		{"```json\n{\"verdict\": \"NO\", \"confidence\": 1.4}\n```", AnswerNo, 1, false},
		{`{"verdict":"wait"}`, AnswerWait, 0.5, false},
		{`The answer is no, it never said which test.`, AnswerNo, 0.5, false},
		{`I cannot tell.`, "", 0, true},
	} {
		ans, conf, err := parseVerdict(tc.in)
		if (err != nil) != tc.err || ans != tc.ans || conf != tc.conf {
			t.Errorf("parseVerdict(%q) = %q, %v, %v; want %q, %v, err %v", tc.in, ans, conf, err, tc.ans, tc.conf, tc.err)
		}
	}
}

// A confident yes costs one call and carries no rationale; a no gets a
// second, separate call for its rationale; a final wait becomes a no.
func TestLLMJudgeRationaleOnlyForNoOrUnsure(t *testing.T) {
	ctx := context.Background()
	in := JudgeInput{Criterion: "Did it answer?", Transcript: "CEO: why?\nSheepdog: The clamp test fails.", Final: true}

	m := braintest.NewModel(braintest.Step{Text: `{"verdict":"yes","confidence":0.95}`})
	v, err := (&LLMJudge{Model: m, ModelID: "j"}).Judge(ctx, in)
	if err != nil || v.Answer != AnswerYes || v.Rationale != "" || len(m.Calls()) != 1 {
		t.Fatalf("confident yes: %+v err %v calls %d", v, err, len(m.Calls()))
	}
	if !strings.Contains(m.Calls()[0].Text(), "CRITERION: Did it answer?") || !strings.Contains(m.Calls()[0].System(), "OVER") {
		t.Errorf("verdict prompt lacks criterion or final note:\n%s", m.Calls()[0].Text())
	}

	m = braintest.NewModel(braintest.Step{Text: `{"verdict":"no","confidence":0.9}`}, braintest.Step{Text: "It never named the test."})
	v, _ = (&LLMJudge{Model: m, ModelID: "j"}).Judge(ctx, in)
	if v.Answer != AnswerNo || v.Rationale != "It never named the test." || len(m.Calls()) != 2 {
		t.Fatalf("no: %+v calls %d", v, len(m.Calls()))
	}

	m = braintest.NewModel(braintest.Step{Text: `{"verdict":"yes","confidence":0.4}`}, braintest.Step{Text: "Probably answered."})
	v, _ = (&LLMJudge{Model: m, ModelID: "j"}).Judge(ctx, in)
	if v.Answer != AnswerYes || v.Rationale == "" {
		t.Fatalf("unsure yes needs a rationale: %+v", v)
	}

	m = braintest.NewModel(braintest.Step{Text: `{"verdict":"wait","confidence":0.8}`}, braintest.Step{Text: "Still checking."})
	v, _ = (&LLMJudge{Model: m, ModelID: "j"}).Judge(ctx, in)
	if v.Answer != AnswerNo {
		t.Fatalf("final wait = %q, want no", v.Answer)
	}
	in.Final = false
	m = braintest.NewModel(braintest.Step{Text: `{"verdict":"wait","confidence":0.8}`}, braintest.Step{Text: "Still checking."})
	v, _ = (&LLMJudge{Model: m, ModelID: "j"}).Judge(ctx, in)
	if v.Answer != AnswerWait {
		t.Fatalf("mid-run wait = %q, want wait", v.Answer)
	}
}

func TestLLMJudgeMetered(t *testing.T) {
	meter := NewMeter()
	m := braintest.NewModel(braintest.Step{Text: `{"verdict":"yes","confidence":1}`, Usage: [2]int32{1000, 10}})
	j := &LLMJudge{Model: Metered(m, meter, "judge"), ModelID: DefaultJudgeModel}
	if _, err := j.Judge(context.Background(), JudgeInput{Criterion: "c", Transcript: "t"}); err != nil {
		t.Fatal(err)
	}
	lines := meter.Lines()
	if len(lines) != 1 || lines[0].Role != "judge" || lines[0].Usage.In != 1000 || lines[0].Usage.Out != 10 || lines[0].CostUSD <= 0 {
		t.Fatalf("meter = %+v", lines)
	}
}

func TestFakeJudgeRules(t *testing.T) {
	f := &FakeJudge{Rules: []FakeRule{{Contains: "jargon", Answer: AnswerNo}, {Contains: "pending", Answer: AnswerWait}}}
	ctx := context.Background()
	if v, _ := f.Judge(ctx, JudgeInput{Criterion: "Is it free of JARGON?"}); v.Answer != AnswerNo {
		t.Errorf("jargon = %q", v.Answer)
	}
	if v, _ := f.Judge(ctx, JudgeInput{Criterion: "pending answered?"}); v.Answer != AnswerWait {
		t.Errorf("mid-run wait = %q", v.Answer)
	}
	if v, _ := f.Judge(ctx, JudgeInput{Criterion: "pending answered?", Final: true}); v.Answer != AnswerNo {
		t.Errorf("final wait = %q", v.Answer)
	}
	if v, _ := f.Judge(ctx, JudgeInput{Criterion: "anything else"}); v.Answer != AnswerYes {
		t.Errorf("default = %q", v.Answer)
	}
}

// Live with a judge, the wording checks are the judge's: a substring miss
// that the judge accepts passes, and the judge's no fails the case with
// its rationale. Without a judge the substring check decides, as before.
func TestSVPLiveWordingChecksJudged(t *testing.T) {
	c := SVPCase{
		ID: "judged", ScreenFixture: "codex-after-approval.screen.txt",
		Request: SVPRequest{Question: "what's going on?", Agent: "auth"},
		Expect:  SVPExpect{SpokenContainsAnyOf: []string{"clamp"}, SpokenNoJargon: true, Judge: []string{"Does it say what auth is doing?"}},
	}
	model := func() *braintest.Model {
		return braintest.NewModel(braintest.Step{Text: braintest.Reply("测试失败了，差一个。", "")})
	}
	ctx := context.Background()

	r := runSVPCase(ctx, c, "../../testdata/herdr", svpRun{model: model()})
	if r.Status != StatusFail || !strings.Contains(r.Detail, "contains none of: clamp") {
		t.Fatalf("no judge: %s %s", r.Status, r.Detail)
	}

	fj := &FakeJudge{}
	r = runSVPCase(ctx, c, "../../testdata/herdr", svpRun{model: model(), judge: fj})
	if r.Status != StatusPass || len(r.Verdicts) != 3 {
		t.Fatalf("judge yes: %s %s verdicts %d", r.Status, r.Detail, len(r.Verdicts))
	}
	in := fj.Inputs()[0]
	if !in.Final || !strings.Contains(in.Transcript, "CEO: what's going on?") || !strings.Contains(in.Transcript, "测试失败了") || !strings.Contains(in.Facts, "Agent auth") {
		t.Errorf("judge input:\n%s\n--\n%s", in.Transcript, in.Facts)
	}

	fj = &FakeJudge{Rules: []FakeRule{{Contains: "jargon", Answer: AnswerNo}}}
	r = runSVPCase(ctx, c, "../../testdata/herdr", svpRun{model: model(), judge: fj})
	if r.Status != StatusFail || !strings.Contains(r.Detail, reasonJudgeNo+" (spoken_no_jargon)") {
		t.Fatalf("judge no: %s %s", r.Status, r.Detail)
	}

	// Offline the judge is never asked: a scripted reply is not judged.
	fj = &FakeJudge{}
	r = runSVPCase(ctx, c, "../../testdata/herdr", svpRun{offline: true, judge: fj})
	if len(fj.Inputs()) != 0 || len(r.Verdicts) != 0 {
		t.Fatalf("offline judged: %d inputs", len(fj.Inputs()))
	}
}

// The brain's tokens are counted from its brain.step events.
func TestSVPLiveMetersBrainTokens(t *testing.T) {
	meter := NewMeter()
	c := SVPCase{ID: "m", ScreenFixture: "codex-after-approval.screen.txt", Request: SVPRequest{Question: "status?", Agent: "auth"}}
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("All quiet.", ""), Usage: [2]int32{5000, 20}})
	runSVPCase(context.Background(), c, "../../testdata/herdr", svpRun{model: m, modelName: "gemini-3.8-flash", meter: meter})
	lines := meter.Lines()
	if len(lines) != 1 || lines[0].Role != "brain" || lines[0].Usage.In != 5000 {
		t.Fatalf("meter = %+v", lines)
	}
}

// Patient retries a rate limit (the judge's and the simulated user's
// calls only) and gives up on other errors at once.
func TestPatientRetriesTransient(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Err: errors.New("Error 429, Message: Resource exhausted")},
		braintest.Step{Err: errors.New("Error 503, Message: overloaded")},
		braintest.Step{Text: "ok"},
	)
	p := Patient(m, make(chan struct{}, 1), 5, "alt").(*patient)
	p.base = time.Millisecond
	resp, err := p.Generate(context.Background(), "x", nil, nil)
	if err != nil || textOf(resp) != "ok" || len(m.Calls()) != 3 {
		t.Fatalf("resp %q err %v calls %d", textOf(resp), err, len(m.Calls()))
	}
	if ids := []string{m.Calls()[0].Model, m.Calls()[1].Model, m.Calls()[2].Model}; ids[0] != "x" || ids[1] != "alt" || ids[2] != "x" {
		t.Errorf("retries did not alternate models: %v", ids)
	}
	m = braintest.NewModel(braintest.Step{Err: errors.New("Error 400, Message: bad request")}, braintest.Step{Text: "ok"})
	p = Patient(m, nil, 5, "").(*patient)
	if _, err := p.Generate(context.Background(), "x", nil, nil); err == nil || len(m.Calls()) != 1 {
		t.Fatalf("a 400 was retried: calls %d", len(m.Calls()))
	}
}

// Every verdict carries the judge's quote as evidence.
func TestLLMJudgeEvidence(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: `{"verdict":"yes","confidence":0.9,"evidence":"Sheepdog: The clamp test fails."}`})
	v, err := (&LLMJudge{Model: m, ModelID: "j"}).Judge(context.Background(), JudgeInput{Criterion: "c", Transcript: "t", Final: true})
	if err != nil || v.Evidence != "Sheepdog: The clamp test fails." {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	if !strings.Contains(m.Calls()[0].System(), "Evidence: quote") {
		t.Errorf("system prompt does not ask for evidence")
	}
	if got := parseEvidence(`yes`); got != "" {
		t.Errorf("parseEvidence(bare) = %q", got)
	}
}

// Every rubric item states its failure in both directions (CEO rule,
// 2026-10-05), and no item tells the judge "short by default".
func TestRubricTwoSided(t *testing.T) {
	items := append(DefaultRubric(), ExplainRubric()...)
	seen := map[string]bool{}
	for _, r := range items {
		if r.ID == "" || seen[r.ID] {
			t.Errorf("rubric id %q empty or repeated", r.ID)
		}
		seen[r.ID] = true
		if r.Question == "" || r.PassWhen == "" || !strings.Contains(r.FailWhen, "; OR ") {
			t.Errorf("%s: want a question, pass_when and a two-sided fail_when (\"…; OR …\"), got %+v", r.ID, r)
		}
		if strings.Contains(r.PassWhen+r.FailWhen, "%") {
			t.Errorf("%s: pass/fail text must not contain %% (criteria are format strings)", r.ID)
		}
		if strings.Contains(strings.ToLower(r.Criterion()), "short by default") {
			t.Errorf("%s still says short by default", r.ID)
		}
	}
	if !seen["length_fits"] {
		t.Error("no length_fits item")
	}
}
