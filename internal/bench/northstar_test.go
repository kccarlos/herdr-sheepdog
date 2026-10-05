package bench

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

func TestNorthStarRunHearsTheVoice(t *testing.T) {
	r := Run{
		Scenario: "approve-zh-hurried", Repeat: 1, Status: eval.RunPass, Turns: 1,
		User:     []UserLine{{AtMS: 10371, Said: "可以，让它跑吧。"}},
		Bot:      []voiceclient.BotUtterance{{StartMS: 21778, EndMS: 25681, Heard: "好的，我已经让 bench-codex 開始執行測試了。"}},
		Actions:  []eval.SimLine{{AtMS: 19818, Who: "action", Agent: "bench-codex", Kind: eval.ActionKeys, Detail: "y"}},
		Verdicts: []eval.NamedVerdict{{Name: "answered", Verdict: eval.Verdict{Answer: eval.AnswerYes}}},
	}
	evs := []northstar.Event{{AtMS: 21296, Type: "voice.tool_result", Payload: map[string]any{"spoken": "好的，我已经让 Codex 开始运行测试了。"}}}
	nr := NorthStarRun(r, nil, evs)
	if nr.Heard != northstar.SideVoice || len(nr.Speech) != 2 {
		t.Fatalf("run = %+v", nr)
	}
	o := northstar.Evaluate(nr)
	if o.Heard.Unmatched() != 0 || o.Heard.Unmentioned() != 0 || o.Orchestrator == nil || len(o.Orchestrator.Claims) != 1 {
		t.Fatalf("outcome = %+v", o)
	}
	if o.Understood == nil || !*o.Understood || o.Done == nil || !*o.Done || o.Safe == nil || !*o.Safe {
		t.Fatalf("north star = %v %v %v", o.Understood, o.Done, o.Safe)
	}
}

func TestSummaryRunReadsTheVoiceSideLabels(t *testing.T) {
	s := RunSummary{Scenario: "explain-en-flaky-test", Repeat: 1, Status: eval.RunFail, Failures: []eval.Failure{
		{Type: eval.FailJudgeNo, Detail: "explain_depth_t3_more (orchestrator): rephrased"},
	}}
	nr := SummaryRun(s, nil)
	if o := northstar.Evaluate(nr); o.Understood == nil || !*o.Understood || o.Done != nil {
		t.Fatalf("an orchestrator-side label is a diagnostic: %+v", o)
	}
	s.Failures = append(s.Failures, eval.Failure{Type: eval.FailJudgeNo, Detail: "explain_depth_t3_more (voice): rephrased"})
	if o := northstar.Evaluate(SummaryRun(s, nil)); o.Understood == nil || *o.Understood {
		t.Fatalf("the voice side's depth failure: %+v", o)
	}
}
