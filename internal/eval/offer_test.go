package eval

import (
	"context"
	"testing"
)

func TestOffersToFindOut(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  bool
	}{
		{"That is all its screen shows. Want me to ask it?", true},
		{"That's everything it reported. Shall I have it dig deeper?", true},
		{"Would you like me to look into why it broke?", true},
		{"屏幕上就这些了。要不要我问问它？", true},
		{"它只写了这些。我可以让它查一下原因吗？", true},
		{"The fix is three lines. Clearer?", false},
		{"Want me to ask it? Anyway, it is idle.", false}, // the reply goes on past the offer
		{"Shall I have it run the tests?", false},
		{"bench-codex is done.", false},
		{"清楚了吗？", false},
	} {
		if got := OffersToFindOut(c.reply); got != c.want {
			t.Errorf("OffersToFindOut(%q) = %v, want %v", c.reply, got, c.want)
		}
	}
}

// A fixed script answers an offer once with OfferYes, waits once, then
// goes on with its next line; a reply without an offer goes straight on.
func TestScriptedUserAnswersOffer(t *testing.T) {
	sc := SimScenario{Fixed: true, Opening: "Explain it.", Script: []string{"Explain it.", "Why?", "An example?"}, OfferYes: "Yes, ask it."}
	next := func(convo []SimLine) SimTurn {
		t.Helper()
		turn, err := ScriptedSimUser{}.Next(context.Background(), sc, convo)
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
	convo := []SimLine{{Who: "ceo", Text: "Explain it."}, {Who: "sheepdog", Text: "It found a race. Clearer?"}}
	if got := next(convo); got.Text != "Why?" || got.Tag != "" {
		t.Fatalf("no offer: got %+v, want the next script line", got)
	}
	convo = append(convo, SimLine{Who: "ceo", Text: "Why?"}, SimLine{Who: "sheepdog", Text: "That is all it shows. Want me to ask it?"})
	yes := next(convo)
	if yes.Kind != TurnSay || yes.Text != "Yes, ask it." || yes.Tag != TagAnswer {
		t.Fatalf("offer: got %+v, want the yes", yes)
	}
	convo = append(convo, SimLine{Who: "ceo", Text: yes.Text, Tag: yes.Tag}, SimLine{Who: "sheepdog", Text: "Asking it now. Want me to ask it?"})
	wait := next(convo)
	if wait.Kind != TurnWait || wait.Tag != TagAnswer {
		t.Fatalf("after the yes: got %+v, want one wait", wait)
	}
	convo = append(convo, SimLine{Who: "system", Text: simWaitMarker, Tag: TagAnswer}, SimLine{Who: "push", Text: "It says the race started three weeks ago."})
	if got := next(convo); got.Text != "An example?" {
		t.Fatalf("after the wait: got %+v, want the next script line", got)
	}
	if n := scriptTurn(append(convo, SimLine{Who: "ceo", Text: "An example?"})); n != 3 {
		t.Errorf("scriptTurn = %d, want 3 (the yes is not a script line)", n)
	}
	convo = append(convo, SimLine{Who: "ceo", Text: "An example?"}, SimLine{Who: "sheepdog", Text: "Want me to ask it?"})
	if got := next(convo); got.Text != "Yes, ask it." {
		t.Fatalf("offer on the last line: got %+v, want the yes", got)
	}
	sc.OfferYes = ""
	if got := next(convo); got.Kind != TurnEnd {
		t.Errorf("without offer_yes: got %+v, want the end of the script", got)
	}
}
