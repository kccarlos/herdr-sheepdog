package factory

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func TestCodexCoordinatorRecoversInterruptedWake(t *testing.T) {
	c := newCodexRoles(t, map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "medium"}},
	}, true)
	c.pass()
	st := c.state()
	original := st.Project.RunID
	assertCodexUnattended(t, "coordinator", c.last(), st.Project.Folder, "medium")

	c.edit(func(s *flow.State) {
		s.Project.Wakes = []flow.Wake{{Task: "T1", Key: "worker_failed", Why: "worker needs help", At: *c.now}}
	})
	c.srv.SetScreen(original, codexIdleScreen("Initial goal", 15))
	c.turnEnds()
	if sends := sendsTo(t, c.srv.Requests(), original); len(sends) != 1 {
		t.Fatalf("first coordinator received %d sends, want one wake", len(sends))
	}
	c.srv.SetScreen(original, codexWorkingScreen("First wake"))
	c.pass()
	c.restart()
	c.srv.Finish(original, "killed")
	*c.now = c.now.Add(11 * time.Minute)
	c.pass()
	replacement := c.state().Project.RunID
	if replacement == "" || replacement == original {
		t.Fatalf("replacement address = %q, original %q", replacement, original)
	}

	c.srv.SetScreen(replacement, codexIdleScreen("Initial goal", 18))
	c.turnEnds()
	sends := sendsTo(t, c.srv.Requests(), replacement)
	if len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") {
		t.Fatalf("replacement sends = %q, want the interrupted wake once", sends)
	}
}
