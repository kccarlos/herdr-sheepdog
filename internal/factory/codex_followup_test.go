package factory

// PR 5b.5b: the two review follow-ups on #740 (opus n1, sonnet m1 and m3) as
// failing tests, built from the real captures in testdata/.

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestCodexFinishedReplyQuotingInterruptReadsIdle: a finished turn whose reply
// text quotes "esc to interrupt" (transcript text, not a status line) right
// above the composer reads idle and counts the turn. The real working screen
// still reads working: the signal is the "• Working (" status line shape.
func TestCodexFinishedReplyQuotingInterruptReadsIdle(t *testing.T) {
	idle := readFixture(t, "codex-idle.screen.txt")
	composer := "› Ask Codex to do anything"
	if !strings.Contains(idle, composer) {
		t.Fatal("fixture lacks the composer")
	}
	quotes := map[string]string{
		"plain sentence":               "• The check treats \"esc to interrupt\" as the working hint.\n\n",
		"diff line":                    "• Ran git diff\n  └ +\t\tif strings.Contains(line, \"esc to interrupt\") {\n\n",
		"status look-alike in a quote": "• The line looks like: • Working (23s • esc to interrupt) in the transcript.\n\n",
	}
	for name, reply := range quotes {
		t.Run(name, func(t *testing.T) {
			screen := strings.Replace(idle, composer, reply+composer, 1)
			if strings.Count(screen, "esc to interrupt") != 1 {
				t.Fatal("test screen should quote the phrase once")
			}
			c := newCodexRoles(t, codexWorkerOnly, true)
			w := c.planAndStartWorker()
			c.srv.SetScreen(w, readFixture(t, "codex-working.screen.txt"))
			c.pass()
			c.srv.SetScreen(w, screen)
			c.turnEnds()
			if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != 1 {
				t.Fatalf("finished turn quoting the phrase: state %s, turn %d, want idle and one turn", r.State, r.Turn)
			}
		})
	}
	t.Run("real working screen still works", func(t *testing.T) {
		c := newCodexRoles(t, codexWorkerOnly, true)
		w := c.planAndStartWorker()
		c.srv.SetScreen(w, readFixture(t, "codex-working.screen.txt"))
		c.turnEnds()
		*c.now = c.now.Add(time.Minute)
		c.turnEnds()
		if r := c.workerRun(); r.State != flow.PhaseWorking || r.Turn != 0 {
			t.Fatalf("real working screen: state %s, turn %d, want working", r.State, r.Turn)
		}
	})
}

// TestCodexUnrecognisedScreenDoesNotBounceIdleRun: a non-empty screen Codex's
// reader cannot place (an overlay, a redraw) is not a fact. An idle run stays
// idle, and the next idle read is not a second turn.
func TestCodexUnrecognisedScreenDoesNotBounceIdleRun(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.srv.SetScreen(w, readFixture(t, "codex-working.screen.txt"))
	c.pass()
	c.srv.SetScreen(w, readFixture(t, "codex-idle.screen.txt"))
	c.turnEnds()
	if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != 1 {
		t.Fatalf("setup: state %s, turn %d, want idle and one turn", r.State, r.Turn)
	}
	for _, overlay := range []string{
		"  Update available! 0.171.0 -> 0.172.0\n\n  1. Update now\n  2. Skip\n",
		"/model\n\n  Select a model\n",
	} {
		c.srv.SetScreen(w, overlay)
		c.turnEnds()
		if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != 1 {
			t.Fatalf("unrecognised screen %q: state %s, turn %d, want idle and one turn", overlay, r.State, r.Turn)
		}
	}
	c.srv.SetScreen(w, readFixture(t, "codex-idle.screen.txt"))
	c.turnEnds()
	if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != 1 {
		t.Fatalf("back to the idle screen: state %s, turn %d, want idle and still one turn", r.State, r.Turn)
	}
}
