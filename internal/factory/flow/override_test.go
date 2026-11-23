package flow

// PR 7.1, TDD step 1: pure flow table tests for KindOverride, ValidateAsk,
// StepAsk (A2–A5), and the not_delegable rail (design.md 4.3b, 7.1; plan.md 7.1).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestOverrideAskValidateAndTable verifies that KindOverride is a valid gated
// ask kind in ValidateAsk, opens via A2 notifying the owner, answers via A3
// for the owner, cancels via A4, refuses closed via A5, and round-trips its
// OverrideTarget list and Attempt.Overrides JSON without ask_id (KB-ASK-15,
// KB-ASK-16, design.md 3, 4.3b, 7.1).
func TestOverrideAskValidateAndTable(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	targets := []OverrideTarget{
		{Task: "T1", PR: 7, Head: "abcdef123456", Checks: ChecksPending, Unreported: true},
		{Task: "T2", PR: 8, Head: "9876543210ab", Checks: ChecksGreen, Unreported: false},
	}

	raw := Ask{
		ID:        "ask-ovr-1",
		Project:   "proj-demo",
		Kind:      KindOverride,
		Question:  "Merge T1 and T2 now without waiting for CI or review?",
		Options:   []string{OverrideOptionMerge, OverrideOptionCancel},
		Recommend: OverrideOptionCancel,
		Targets:   targets,
		AskedBy:   RoleCoordinator,
	}

	valid, code, why := ValidateAsk(raw)
	if code != "" || why != "" {
		t.Fatalf("ValidateAsk(KindOverride) = (code=%q, why=%q), want valid", code, why)
	}
	if !valid.Gated || valid.Kind != KindOverride || valid.Recommend != OverrideOptionCancel || !slices.Equal(valid.Options, []string{OverrideOptionMerge, OverrideOptionCancel}) {
		t.Fatalf("ValidateAsk(KindOverride) = %+v, want gated override with negative recommendation", valid)
	}
	if !slices.Equal(valid.Targets, targets) {
		t.Fatalf("ValidateAsk(KindOverride).Targets = %+v, want %+v", valid.Targets, targets)
	}

	// A2: opens as gated and notifies owner.
	open, row, dec := StepAsk(valid, AskEvent{Kind: EvAskOpen}, t0)
	if row != "A2" || open.Status != AskStatusOpen || !open.Gated || !slices.Equal(dec.Asked, []string{AskNotifyOwner}) {
		t.Fatalf("A2 override = (row=%q, status=%q, gated=%v, asked=%v), want (A2, open, true, [%s])",
			row, open.Status, open.Gated, dec.Asked, AskNotifyOwner)
	}

	// A3: owner answer with proof records answer and wakes coordinator.
	answered, row, dec := StepAsk(open, AskEvent{
		Kind:     EvAskAnswer,
		Answer:   OverrideOptionMerge,
		By:       "owner",
		ProofID:  "prf-112233445566",
		DeviceID: "dev-phone-1",
	}, t1)
	if row != "A3" || answered.Status != AskStatusAnswered || answered.Answer != OverrideOptionMerge ||
		answered.AnsweredBy != "owner" || answered.ProofID != "prf-112233445566" || answered.DeviceID != "dev-phone-1" ||
		!slices.Equal(dec.Asked, []string{AskWake}) {
		t.Fatalf("A3 owner override = (row=%q, ask=%+v, asked=%v), want A3 answered with [%s]", row, answered, dec.Asked, AskWake)
	}

	// A4 and A5: cancel open, refuse closed.
	cancelled, row, _ := StepAsk(open, AskEvent{Kind: EvAskCancel}, t1)
	if row != "A4" || cancelled.Status != AskStatusCancelled {
		t.Fatalf("A4 override = (row=%q, status=%q), want (A4, cancelled)", row, cancelled.Status)
	}
	if _, row, dec := StepAsk(answered, AskEvent{Kind: EvAskAnswer, Answer: OverrideOptionCancel, By: "owner"}, t1.Add(time.Second)); row != "A5" || dec.Why != RefuseClosed {
		t.Fatalf("A5 on answered override = (row=%q, why=%q), want (A5, %q)", row, dec.Why, RefuseClosed)
	}

	// Attempt.Overrides JSON contract (plan.md 7.1): head, checks_shown, spent, proof_id; no ask_id.
	att := Attempt{
		BaseCommit: "base0",
		Overrides: []Override{
			{Head: "abcdef123456", ChecksShown: ChecksPending, Spent: false, ProofID: "prf-112233445566"},
		},
	}
	b, err := json.Marshal(att)
	if err != nil {
		t.Fatalf("marshal Attempt: %v", err)
	}
	if strings.Contains(string(b), "ask_id") {
		t.Fatalf("Attempt.Overrides JSON unexpectedly contains ask_id: %s", b)
	}
	var roundTrip Attempt
	if err := json.Unmarshal(b, &roundTrip); err != nil {
		t.Fatalf("unmarshal Attempt: %v", err)
	}
	if !slices.Equal(roundTrip.Overrides, att.Overrides) {
		t.Fatalf("Attempt.Overrides round-trip = %+v, want %+v", roundTrip.Overrides, att.Overrides)
	}
}

// TestOverrideNotDelegableInFlow checks plan.md 7.1 unit test (2) and design.md
// 4.3b / 7.1: an answer with answered_by="rule" (delegation rule) to an
// override ask is refused with not_delegable in flow without mutating the ask.
// Slice 14's A1 must call the same check.
func TestOverrideNotDelegableInFlow(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	open := Ask{
		ID:        "ask-ovr-2",
		Project:   "proj-demo",
		Kind:      KindOverride,
		Gated:     true,
		Question:  "Merge T1 now without waiting for CI or review?",
		Options:   []string{OverrideOptionMerge, OverrideOptionCancel},
		Recommend: OverrideOptionCancel,
		Targets:   []OverrideTarget{{Task: "T1", PR: 7, Head: "c2", Checks: ChecksPending}},
		AskedBy:   RoleCoordinator,
		At:        t0,
		Status:    AskStatusOpen,
	}

	cases := []struct {
		name   string
		option string
	}{
		{name: "rule_attempts_merge_option", option: OverrideOptionMerge},
		{name: "rule_attempts_cancel_option", option: OverrideOptionCancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, dec := StepAsk(open, AskEvent{
				Kind:   EvAskAnswer,
				Answer: tc.option,
				By:     "rule",
			}, t1)
			if dec.Why != RefuseNotDelegable {
				t.Fatalf("StepAsk(override, By=rule, option=%q) why = %q, want %q", tc.option, dec.Why, RefuseNotDelegable)
			}
			if got.Status != AskStatusOpen || got.Answer != "" || got.AnsweredBy != "" || !got.AnsweredAt.IsZero() {
				t.Fatalf("refused rule answer mutated override ask: %+v", got)
			}
			if len(dec.Asked) != 0 {
				t.Fatalf("refused rule answer requested side effects: %v", dec.Asked)
			}
		})
	}
}
