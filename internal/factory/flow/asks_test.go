package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAskTableTransitionsA2ToA5 exercises every row of design.md 7.1's pure ask
// table without delegation (A1 is slice 14): A2 opens decision and gated asks,
// A3 answers run-opened and rule-opened asks, A4 cancels open asks, and A5
// refuses answer and cancel on answered or cancelled asks (KB-ASK-4, KB-ASK-10,
// KB-ASK-15).
func TestAskTableTransitionsA2ToA5(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(2 * time.Minute)

	// A2 on a decision ask: status becomes open, gated=false, wakes coordinator.
	decAsk, row, dec := StepAsk(Ask{
		ID:        "ask-dec-1",
		Project:   "proj-demo",
		Kind:      KindDecision,
		Question:  "Keep cache in SQLite or Plain JSON?",
		Options:   []string{"SQLite", "Plain JSON"},
		Recommend: "SQLite",
		Task:      "T1",
		Head:      "c1",
		AskedBy:   RoleWorker,
		RunID:     "worker-t1",
	}, AskEvent{Kind: EvAskOpen}, t0)
	if row != "A2" || decAsk.Status != AskStatusOpen || decAsk.Gated || !decAsk.At.Equal(t0) || !slices.Equal(dec.Asked, []string{AskWake}) {
		t.Fatalf("A2 decision = (row=%q, status=%q, gated=%v, at=%v, asked=%v), want (A2, open, false, %v, [%s])",
			row, decAsk.Status, decAsk.Gated, decAsk.At, dec.Asked, t0, AskWake)
	}

	// A2 on every gated owner kind: status becomes open, gated=true, notifies owner.
	for _, kind := range []string{KindGate, KindRelease, KindPaidKey, KindDelete, KindInstall, KindReset, KindRule} {
		ga, grow, gdec := StepAsk(Ask{
			ID:        "ask-" + kind,
			Project:   "proj-demo",
			Kind:      kind,
			Question:  "Proceed with " + kind + "?",
			Options:   []string{"Yes, proceed", "Not now"},
			Recommend: "Yes, proceed",
			AskedBy:   RoleCoordinator,
			RunID:     "coord-1",
		}, AskEvent{Kind: EvAskOpen}, t0)
		if grow != "A2" || ga.Status != AskStatusOpen || !ga.Gated || !ga.At.Equal(t0) || !slices.Equal(gdec.Asked, []string{AskNotifyOwner}) {
			t.Fatalf("A2 %s = (row=%q, status=%q, gated=%v, asked=%v), want (A2, open, true, [%s])",
				kind, grow, ga.Status, ga.Gated, gdec.Asked, AskNotifyOwner)
		}
	}

	// A3 on a run-opened decision ask: records coordinator answer and asks to deliver to the run.
	ansDec, row, dec := StepAsk(decAsk, AskEvent{
		Kind:   EvAskAnswer,
		Answer: "Plain JSON",
		Why:    "fewer dependencies",
		By:     RoleCoordinator,
	}, t1)
	if row != "A3" || ansDec.Status != AskStatusAnswered || ansDec.Answer != "Plain JSON" || ansDec.Why != "fewer dependencies" ||
		ansDec.AnsweredBy != RoleCoordinator || !ansDec.AnsweredAt.Equal(t1) || !slices.Equal(dec.Asked, []string{AskDeliverAnswer}) {
		t.Fatalf("A3 run-opened = (row=%q, ask=%+v, asked=%v), want A3 answered with [%s]", row, ansDec, dec.Asked, AskDeliverAnswer)
	}

	// A3 on a rule-opened gate ask: records owner answer + proof/device and wakes coordinator.
	ruleOpen, _, _ := StepAsk(Ask{
		ID:        "ask-gate-1",
		Project:   "proj-demo",
		Kind:      KindGate,
		Question:  "Accept T1 at c1?",
		Options:   []string{"Yes, accept", "No, hold"},
		Recommend: "Yes, accept",
		Task:      "T1",
		Head:      "c1",
		AskedBy:   "engine",
	}, AskEvent{Kind: EvAskOpen}, t0)
	ansGate, row, dec := StepAsk(ruleOpen, AskEvent{
		Kind:     EvAskAnswer,
		Answer:   "Yes, accept",
		By:       "owner",
		ProofID:  "prf-112233445566",
		DeviceID: "dev-phone-1",
	}, t1)
	if row != "A3" || ansGate.Status != AskStatusAnswered || ansGate.Answer != "Yes, accept" || ansGate.AnsweredBy != "owner" ||
		ansGate.ProofID != "prf-112233445566" || ansGate.DeviceID != "dev-phone-1" || !ansGate.AnsweredAt.Equal(t1) || !slices.Equal(dec.Asked, []string{AskWake}) {
		t.Fatalf("A3 rule-opened = (row=%q, ask=%+v, asked=%v), want A3 answered with [%s]", row, ansGate, dec.Asked, AskWake)
	}

	// A4 on an open ask: transitions to cancelled with no side-effect asks.
	cancelled, row, dec := StepAsk(decAsk, AskEvent{Kind: EvAskCancel}, t1)
	if row != "A4" || cancelled.Status != AskStatusCancelled || len(dec.Asked) != 0 {
		t.Fatalf("A4 cancel = (row=%q, status=%q, asked=%v), want (A4, cancelled, [])", row, cancelled.Status, dec.Asked)
	}

	// A5 on answered or cancelled asks: both answer and cancel are refused already_closed without mutating the ask.
	for _, closed := range []Ask{ansDec, cancelled} {
		for _, ev := range []AskEvent{
			{Kind: EvAskAnswer, Answer: "SQLite", By: RoleCoordinator},
			{Kind: EvAskCancel},
		} {
			got, a5Row, a5Dec := StepAsk(closed, ev, t1.Add(time.Minute))
			if a5Row != "A5" || a5Dec.Why != RefuseClosed || got.Status != closed.Status || got.Answer != closed.Answer || !got.AnsweredAt.Equal(closed.AnsweredAt) {
				t.Fatalf("A5 on %s + %s = (row=%q, why=%q, got=%+v), want (A5, %q, unchanged)",
					closed.Status, ev.Kind, a5Row, a5Dec.Why, got, RefuseClosed)
			}
		}
	}
}

// TestValidateAskRules checks KB-ASK-6, KB-ASK-9 (parentheses), KB-ASK-15 and
// design.md 7.1 AQ3 in pure flow: question bounds and single-line rule, closed
// kind enum, 2..9 distinct non-blank options, recommendation membership, and
// parenthetical key rejection.
func TestValidateAskRules(t *testing.T) {
	nineOpts := []string{"Opt 1", "Opt 2", "Opt 3", "Opt 4", "Opt 5", "Opt 6", "Opt 7", "Opt 8", "Opt 9"}
	tenOpts := append(slices.Clone(nineOpts), "Opt 10")

	validCases := []struct {
		name      string
		ask       Ask
		wantGated bool
	}{
		{
			name: "neutral two-choice decision ask",
			ask: Ask{
				Kind:      KindDecision,
				Question:  "  Keep offline cache in SQLite or Plain JSON?  ",
				Options:   []string{" SQLite ", "Plain JSON"},
				Recommend: "SQLite",
			},
			wantGated: false,
		},
		{
			name: "nine-option gated release ask at 300-rune question cap",
			ask: Ask{
				Kind:      KindRelease,
				Question:  strings.Repeat("q", MaxAskQuestionLen),
				Options:   nineOpts,
				Recommend: "Opt 1",
			},
			wantGated: true,
		},
		{
			name: "negative or wait recommendation paired with a Yes option",
			ask: Ask{
				Kind:      KindRelease,
				Question:  "Ship v1.4.0 now?",
				Options:   []string{"Yes, ship v1.4.0", "Wait for review"},
				Recommend: "Wait for review",
			},
			wantGated: true,
		},
		{
			name: "keep-style recommendation that is neither negative nor wait",
			ask: Ask{
				Kind:      KindDelete,
				Question:  "Delete stale remote branch experiment/old-prototype?",
				Options:   []string{"Delete branch", "Keep branch"},
				Recommend: "Keep branch",
			},
			wantGated: true,
		},
		{
			name: "recommended wait without a Yes option (AQ3)",
			ask: Ask{
				Kind:      KindRelease,
				Question:  "Ship now?",
				Options:   []string{"Ship release", "Wait for review"},
				Recommend: "Wait for review",
			},
			wantGated: true,
		},
		{
			name: "recommended negative without a Yes option (AQ3)",
			ask: Ask{
				Kind:      KindRelease,
				Question:  "Ship now?",
				Options:   []string{"Ship release", "Do not ship"},
				Recommend: "Do not ship",
			},
			wantGated: true,
		},
		{
			name: "recommended negative first without a Yes option (AQ3)",
			ask: Ask{
				Kind:      KindRelease,
				Question:  "Ship now?",
				Options:   []string{"Do not ship", "Ship release"},
				Recommend: "Do not ship",
			},
			wantGated: true,
		},
	}
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			got, code, why := ValidateAsk(tc.ask)
			if code != "" || why != "" || got.Question == "" || got.Gated != tc.wantGated {
				t.Fatalf("ValidateAsk(%+v) = (got=%+v, code=%q, why=%q), want valid gated=%v", tc.ask, got, code, why, tc.wantGated)
			}
		})
	}

	invalidCases := []struct {
		name     string
		ask      Ask
		wantCode string
	}{
		{
			name:     "empty kind",
			ask:      Ask{Kind: "", Question: "Use SQLite?", Options: []string{"Yes", "No"}, Recommend: "Yes"},
			wantCode: "unknown_kind",
		},
		{
			name:     "unknown kind",
			ask:      Ask{Kind: "deploy", Question: "Deploy now?", Options: []string{"Yes", "No"}, Recommend: "Yes"},
			wantCode: "unknown_kind",
		},
		{
			name:     "empty question",
			ask:      Ask{Kind: KindDecision, Question: "   \t  ", Options: []string{"Yes", "No"}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "multi-line question",
			ask:      Ask{Kind: KindDecision, Question: "First line?\nSecond line?", Options: []string{"Yes", "No"}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "question over 300 characters",
			ask:      Ask{Kind: KindDecision, Question: strings.Repeat("q", MaxAskQuestionLen+1), Options: []string{"Yes", "No"}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "zero options",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: nil, Recommend: ""},
			wantCode: "invalid_ask",
		},
		{
			name:     "only one option",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: []string{"Yes"}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "ten options (over max 9)",
			ask:      Ask{Kind: KindDecision, Question: "Pick one?", Options: tenOpts, Recommend: "Opt 1"},
			wantCode: "invalid_ask",
		},
		{
			name:     "blank option",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: []string{"Yes", "   "}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "duplicate options after trimming",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: []string{"Yes", " Yes ", "No"}, Recommend: "Yes"},
			wantCode: "invalid_ask",
		},
		{
			name:     "missing recommendation",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: []string{"Yes", "No"}, Recommend: ""},
			wantCode: "invalid_ask",
		},
		{
			name:     "recommendation not among options",
			ask:      Ask{Kind: KindDecision, Question: "Use SQLite?", Options: []string{"Yes", "No"}, Recommend: "Maybe"},
			wantCode: "invalid_ask",
		},
		{
			name:     "option holding short word in parentheses (KB-ASK-9)",
			ask:      Ask{Kind: KindDecision, Question: "Switch protocol?", Options: []string{"Switch (v2)", "Stay"}, Recommend: "Stay"},
			wantCode: "invalid_ask",
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, code, why := ValidateAsk(tc.ask)
			if code != tc.wantCode || strings.TrimSpace(why) == "" {
				t.Fatalf("ValidateAsk(%+v) = (code=%q, why=%q), want code=%q with plain reason", tc.ask, code, why, tc.wantCode)
			}
		})
	}
}
