package supervisor

// PR 7.1, TDD step 1: supervisor phone card and APNs tests for override asks
// (design.md 4.3b, S0.10, plan.md 7.1).

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// TestFactoryOverridePhoneCardMultiLineCommandAndAPNsNoOptions verifies that
// an open KindOverride ask surfaces on the phone as a PendingAnswerPrompt card
// whose multi-line Command lists each target task's PR, short H, checks state
// and "has unreported work" (only for tasks with unreported work), with the
// negative option marked Recommended, and that its APNs notification omits
// quick-reply Options (S0.10, plan.md 7.1).
func TestFactoryOverridePhoneCardMultiLineCommandAndAPNsNoOptions(t *testing.T) {
	eng := newFakeFactory()
	fn := &fakeNotifier{}
	h := newHarness(t, withNotifier(fn), func(c *Config) { c.Factory = eng })

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sup.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	eng.openAsk(flow.Ask{
		ID:        "ask-demo-ovr-1",
		Project:   "demo",
		Kind:      flow.KindOverride,
		Gated:     true,
		Question:  "Merge 2 PRs now without waiting for CI or review?",
		Options:   []string{flow.OverrideOptionMerge, flow.OverrideOptionCancel},
		Recommend: flow.OverrideOptionCancel,
		Targets: []flow.OverrideTarget{
			{Task: "T1", PR: 7, Head: "abcdef1234567890", Checks: flow.ChecksPending, Unreported: true},
			{Task: "T2", PR: 8, Head: "9876543210fedcba", Checks: flow.ChecksGreen, Unreported: false},
		},
		At: t0,
	})

	waitFor(t, func() bool { return len(h.sup.FactoryQuestions()) == 1 }, "override ask to surface in FactoryQuestions")

	qs := h.sup.FactoryQuestions()
	card := qs[0]
	if card.Kind != state.PendingAnswerPrompt || card.ID != "fask-ask-demo-ovr-1" {
		t.Fatalf("card = %+v, want PendingAnswerPrompt fask-ask-demo-ovr-1", card)
	}
	lines := strings.Split(strings.TrimSpace(card.Command), "\n")
	if len(lines) < 2 {
		t.Fatalf("override card Command is not multi-line: %q", card.Command)
	}
	var t1Line, t2Line string
	for _, line := range lines {
		if strings.Contains(line, "T1") {
			t1Line = line
		}
		if strings.Contains(line, "T2") {
			t2Line = line
		}
	}
	for _, want := range []string{"#7", "abcdef1", "pending", "has unreported work"} {
		if !strings.Contains(t1Line, want) {
			t.Errorf("T1 line %q missing %q (full Command:\n%s)", t1Line, want, card.Command)
		}
	}
	for _, want := range []string{"#8", "9876543", "green"} {
		if !strings.Contains(t2Line, want) {
			t.Errorf("T2 line %q missing %q (full Command:\n%s)", t2Line, want, card.Command)
		}
	}
	if strings.Contains(t2Line, "has unreported work") {
		t.Errorf("T2 line %q unexpectedly claims unreported work", t2Line)
	}

	wantOpts := []state.Option{
		{Label: flow.OverrideOptionMerge, Recommended: false},
		{Label: flow.OverrideOptionCancel, Recommended: true},
	}
	if !reflect.DeepEqual(card.Options, wantOpts) {
		t.Fatalf("card.Options = %+v, want %+v", card.Options, wantOpts)
	}

	// APNs push for an override omits quick-reply options (S0.10, plan.md 7.1).
	notifs := fn.all()
	if len(notifs) != 1 {
		t.Fatalf("APNs notifications = %d (%+v), want 1", len(notifs), notifs)
	}
	if len(notifs[0].Options) != 0 {
		t.Fatalf("override APNs notification carried quick-reply options %+v, want omitted", notifs[0].Options)
	}
	// PR 7.1 review M1: override APNs payload must not carry APPROVAL category quick-reply actions,
	// nor send KindApproval to the push relay (which derives aps.category = APPROVAL from kind).
	aps, _ := notifs[0].Payload()["aps"].(map[string]any)
	if aps["category"] == push.CategoryApproval || aps["category"] != push.CategoryAgent {
		t.Fatalf("override APNs aps.category = %v, want %q (not %q)", aps["category"], push.CategoryAgent, push.CategoryApproval)
	}
	if aps["interruption-level"] != "time-sensitive" {
		t.Fatalf("override APNs interruption-level = %v, want time-sensitive", aps["interruption-level"])
	}
	if notifs[0].Kind == push.KindApproval {
		t.Fatalf("override push Notification.Kind = %q, must not be %q", notifs[0].Kind, push.KindApproval)
	}
}
