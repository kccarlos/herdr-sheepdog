package supervisor

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The screens of campaign D's t3-stop-queued-instruction (tier 3, Claude
// Haiku): working with "esc to interrupt" in the footer, then
// "Interrupted" once a Ctrl-C took.
const (
	claudeWorkingScreen = "⏺ Now let me run the tests for Task 2:\n\n✶ Flibbertigibbeting… (28s · ↓ 3.1k tokens)\n\n────────\n❯ \n────────\n  ⏵⏵ accept edits on (shift+tab to cycle) · esc to interrupt · ← for agents\n"
	claudeStoppedScreen = "⏺ Now for Task 3: Add ApplyDiscount method with validation:\n  ⎿  Interrupted · What should Claude do instead?\n\n────────\n❯ \n────────\n  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents\n"
)

// ctrlCStopsAfter makes the fake herdr stop the pane at the nth Ctrl-C
// (0: never) and counts them.
func ctrlCStopsAfter(h *harness, pane string, n int32) *atomic.Int32 {
	var sent atomic.Int32
	h.srv.Handle("pane.send_keys", func(p json.RawMessage) (any, error) {
		var params struct {
			Keys []string `json:"keys"`
		}
		_ = json.Unmarshal(p, &params)
		if len(params.Keys) == 1 && params.Keys[0] == "ctrl+c" {
			if sent.Add(1) == n {
				h.srv.SetScreen(pane, claudeStoppedScreen)
				h.srv.SetStatus(pane, herdr.StateIdle)
			}
		}
		return map[string]any{"type": "ok"}, nil
	})
	return &sent
}

// Campaign D N5: the user said stop, the orchestrator's interrupt sent one Ctrl-C
// to Claude busy with a queued instruction, the result "Interrupted
// Claude." went back at once, and the user heard "I stopped Claude" while
// it carried on working. Now the interrupt checks: a second Ctrl-C when
// the first did not take, and a result that says which happened.
func TestBrainInterruptChecksTheStop(t *testing.T) {
	cases := []struct {
		name      string
		stopsAt   int32
		wantSent  int32
		wantEcho  string
		finishMsg bool // the next done is announced (the stop did not take)
	}{
		{"the first Ctrl-C takes", 1, 1, "Interrupted Claude; it stopped.", false},
		{"t3-stop-queued-instruction: the second takes", 2, 2, "The first Ctrl-C did not stop Claude; a second one did, and it stopped.", false},
		{"it never stops", 0, 2, "Sent Ctrl-C to Claude twice, and it is still working: it has not stopped.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInterrupt, map[string]any{"agent": "Claude"})}},
				braintest.Step{Text: braintest.Reply("Done.", "")},
			)
			h := newBrainHarness(t, m)
			h.addAgent("w1:p1", "Claude", "claude", herdr.StateWorking)
			h.srv.SetScreen("w1:p1", claudeWorkingScreen)
			sent := ctrlCStopsAfter(h, "w1:p1", tc.stopsAt)
			h.say("Actually no. Stop the Claude worker now, and don't do the README either.")
			if got := sent.Load(); got != tc.wantSent {
				t.Errorf("Ctrl-C sent %d times, want %d", got, tc.wantSent)
			}
			if text := lastCall(t, m); !strings.Contains(text, tc.wantEcho) {
				t.Errorf("the brain did not get %q:\n%s", tc.wantEcho, text)
			}
			if tc.stopsAt == 0 {
				if h.log.count("supervisor.interrupt_failed") != 1 {
					t.Error("no supervisor.interrupt_failed")
				}
				h.srv.SetStatus("w1:p1", herdr.StateWorking)
			}
			h.transition("w1:p1", herdr.StateWorking, herdr.StateDone)
			if suppressed := h.log.count("supervisor.finished_suppressed") == 1; suppressed == tc.finishMsg {
				t.Errorf("finished_suppressed = %v, want %v", suppressed, !tc.finishMsg)
			}
		})
	}
}

// A second Ctrl-C goes out only while the screen still shows a running
// turn: a CLI back at its input takes it as half of "exit".
func TestBrainInterruptNoSecondCtrlCWithoutARunningTurn(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w1:p1", "Claude", "claude", herdr.StateWorking)
	h.srv.SetScreen("w1:p1", claudeStoppedScreen)
	sent := ctrlCStopsAfter(h, "w1:p1", 0)
	echo, err := brainHost{h.sup}.Interrupt(t.Context(), "w1:p1")
	if err != nil || sent.Load() != 1 || !strings.Contains(echo, "its screen shows no running turn") {
		t.Errorf("echo = %q, %v; Ctrl-C sent %d times", echo, err, sent.Load())
	}
}

// The result never claims more than was seen: a screen that cannot be
// read is said to be unread, with no second Ctrl-C, and the finish that
// follows is announced.
func TestBrainInterruptUnreadableScreen(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w1:p1", "Claude", "claude", herdr.StateWorking)
	sent := ctrlCStopsAfter(h, "w1:p1", 0)
	h.srv.Handle("pane.read", func(json.RawMessage) (any, error) { return nil, errors.New("boom") })
	echo, err := brainHost{h.sup}.Interrupt(t.Context(), "w1:p1")
	if err != nil || sent.Load() != 1 || !strings.Contains(echo, "could not read its screen to confirm") {
		t.Errorf("echo = %q, %v; Ctrl-C sent %d times", echo, err, sent.Load())
	}
	h.transition("w1:p1", herdr.StateWorking, herdr.StateDone)
	if h.log.count("supervisor.finished_suppressed") != 0 {
		t.Error("the finish after an unconfirmed stop was suppressed")
	}
}
