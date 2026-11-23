package web

// PR 7.1, TDD step 1: web bridge tests for override cards on /api/state and
// owner-proof enforcement on POST /api/pending/answer (KB-ASK-19, S0.10,
// plan.md 7.1).

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func TestFactoryOverrideCardStateAndProofGuard(t *testing.T) {
	eng := newWebFakeFactory()
	h := newHarness(t, harnessOpts{factory: eng})
	startSupervisorRun(t, h)

	eng.openAsk(flow.Ask{
		ID:        "ask-demo-ovr-web",
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
		At: time.Now().UTC(),
	})

	const cardID = "fask-ask-demo-ovr-web"
	var got PendingView
	waitFor(t, 500*time.Millisecond, func() bool {
		var st StateResponse
		if h.getJSON("/api/state", &st) != http.StatusOK {
			return false
		}
		q, ok := findQuestion(st, cardID)
		if ok {
			got = q
		}
		return ok
	}, "override ask "+cardID+" to appear in /api/state")

	if got.Kind != state.PendingAnswerPrompt {
		t.Fatalf("PendingView.Kind = %q, want %q", got.Kind, state.PendingAnswerPrompt)
	}
	if !strings.Contains(got.Command, "\n") {
		t.Fatalf("PendingView.Command is not multi-line: %q", got.Command)
	}
	for _, want := range []string{"T1", "#7", "abcdef1", "pending", "has unreported work", "T2", "#8", "9876543", "green"} {
		if !strings.Contains(got.Command, want) {
			t.Errorf("PendingView.Command missing %q:\n%s", want, got.Command)
		}
	}
	if len(got.Options) != 2 || got.Options[0].Recommended || !got.Options[1].Recommended {
		t.Fatalf("PendingView.Options = %+v, want negative option 2 recommended", got.Options)
	}

	// Ticketless loopback POST /api/pending/answer is refused 403 proof_required.
	var errBody map[string]any
	code, raw := h.postJSON("/api/pending/answer", map[string]any{"id": cardID, "option": 1}, &errBody)
	if code != http.StatusForbidden || !strings.Contains(raw, "proof_required") {
		t.Fatalf("loopback ticketless override tap = %d %s, want 403 proof_required", code, raw)
	}

	// Paired device tap answers the override ask and records device_id.
	dev := h.pairDevice()
	req := h.authedRequest(http.MethodPost, "/api/pending/answer", dev.ticket, nil, map[string]any{"id": cardID, "option": 1})
	var okBody map[string]any
	code, _ = h.do(req, &okBody)
	if code != http.StatusOK || okBody["ok"] != true {
		t.Fatalf("paired device override tap = %d %v, want 200 ok", code, okBody)
	}
	if a := eng.firstAsk(); a.Status != flow.AskStatusAnswered || a.Answer != flow.OverrideOptionMerge || a.DeviceID != dev.id || a.ProofID == "" {
		t.Fatalf("override ask after paired tap = %+v, want answered by %s with proof", a, dev.id)
	}
}
