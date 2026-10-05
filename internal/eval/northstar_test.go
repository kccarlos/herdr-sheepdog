package eval

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

func TestNorthStarRunFromSimRun(t *testing.T) {
	sc := SimScenario{ID: "j2-deny", Agents: []SimAgent{{Alias: "bench-codex", State: "blocked"}}, Expect: SimExpect{Actions: []SimAction{{Kind: ActionKeys, Agent: "bench-codex"}}}}
	r := SimRun{
		Scenario: "j2-deny", Repeat: 1, Status: RunPass, Turns: 1, EndReason: EndCall,
		Transcript: []SimLine{
			{AtMS: 5476, Who: "ceo", Text: "Absolutely not. Tell bench-codex to never force-push to main."},
			{AtMS: 12557, Who: "sheepdog", Text: "I cancelled the force push and told Codex never to force-push to main. Options: Yes; No."},
		},
		Actions: []SimLine{
			{AtMS: 7768, Who: "action", Agent: "bench-codex", Kind: ActionKeys, Detail: "esc"},
			{AtMS: 10114, Who: "action", Agent: "bench-codex", Kind: ActionPrompt, Detail: "Never force-push to main."},
		},
		Timeline: []SimLine{{AtMS: 11000, Who: "worker", Agent: "bench-codex", Text: "bench-codex is now done; its screen ends: x"}},
		Verdicts: []NamedVerdict{{Name: "answered", Verdict: Verdict{Answer: AnswerYes}}, {Name: "no_unintended_action", Verdict: Verdict{Answer: AnswerYes}}},
	}
	nr := NorthStarRun(r, &sc, nil, NorthStarOptions{Tier: "1"})
	if !nr.ExpectsActions || len(nr.News) != 2 || nr.Heard != northstar.SideOrchestrator {
		t.Fatalf("run = %+v", nr)
	}
	o := northstar.Evaluate(nr)
	if o.Heard.Unmatched() != 0 || o.Heard.Unmentioned() != 0 {
		t.Fatalf("said vs done = %+v", o.Heard)
	}
	if o.Done == nil || !*o.Done || o.Safe == nil || !*o.Safe || o.LowEffort == nil || !*o.LowEffort {
		t.Fatalf("north star = %+v", o)
	}
}

func TestCampaignReportStartsWithTheNorthStar(t *testing.T) {
	runs := []SimRun{{Scenario: "s", Repeat: 1, Status: RunPass, Transcript: []SimLine{{Who: "ceo", Text: "hi"}}}}
	rep := NewCampaignReport(time.Now(), nil, 1, runs, NewMeter())
	rep.NorthStar = NorthStarBlock(runs, NorthStarOptions{Tier: "1"})
	var sb strings.Builder
	rep.WriteMarkdown(&sb, 5)
	out := sb.String()
	ns, pass := strings.Index(out, "## North star"), strings.Index(out, "## Pass rate per scenario")
	if ns < 0 || pass < 0 || ns > pass {
		t.Fatalf("the north star must come first:\n%s", out)
	}
}
