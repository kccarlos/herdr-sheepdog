package eval

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// alternate gives every scenario run three times both screens, and
// the fixed modes are what they say.
func TestScreenFor(t *testing.T) {
	for _, id := range []string{"workday-morning-standup", "workday-scale-ten", "workday-zh-evening"} {
		seen := map[string]bool{}
		for r := 1; r <= 3; r++ {
			seen[ScreenFor(ScreenAlternate, id, r)] = true
		}
		if !seen[ScreenVisible] || !seen[ScreenHidden] {
			t.Errorf("%s: alternate gave %v", id, seen)
		}
	}
	if ScreenFor(ScreenHidden, "x", 1) != ScreenHidden || ScreenFor(ScreenNone, "x", 1) != ScreenNone {
		t.Error("fixed modes")
	}
	if ValidScreenMode("sometimes") || !ValidScreenMode("") {
		t.Error("ValidScreenMode")
	}
}

// A card on a visible screen is seen by the user and the judge, and
// rendered as seen, not heard.
func TestSimScreenCard(t *testing.T) {
	w := &simWorld{push: &simPusher{ch: make(chan SimLine, 4)}}
	s := &simScreen{w: w, visible: true}
	if s.SendCard(supervisor.Card{Kind: "card", Title: "Agents", Markdown: "- auth: working"}) != supervisor.Presented {
		t.Fatal("not delivered")
	}
	l := <-w.push.ch
	if l.Who != "screen" || !strings.Contains(l.Text, "auth: working") {
		t.Fatalf("line = %+v", l)
	}
	if got := renderSim([]SimLine{l}); !strings.Contains(got, "seen, not heard") {
		t.Errorf("render = %q", got)
	}
	if len(s.shown()) != 1 {
		t.Error("not counted")
	}
	hidden := &simScreen{w: w}
	if hidden.SendCard(supervisor.Card{Kind: "card", Title: "x"}) != supervisor.NotDelivered {
		t.Error("a hidden screen took a card")
	}
}
