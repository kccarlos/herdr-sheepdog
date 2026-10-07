package supervisor

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Tier-3 bench 2026-10-03, finding 3: every Claude Code approval got two
// pushes. The block template went out at announce_wait, the orchestrator's
// recheck was told so (the fact already in place), and it added its own
// line every time, because the template it was shown said nothing:
// "Claude is asking do you want to proceed." The command sat between
// dashed rules the parser did not read. Now the template carries the
// command, and the recheck shows the orchestrator that it was already said, so
// "nothing to add" is a judgement the model can make on the facts.
func TestClaudeBashBlockTemplateCarriesTheCommandIntoTheRecheck(t *testing.T) {
	const template = "auth asks to run go test."
	release := make(chan struct{})
	m := braintest.NewModel()
	m.Default = braintest.NothingToAdd()
	var calls int
	var recheck string
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		calls++
		if calls == 1 {
			<-release
			return braintest.Response(*announceReply("It wants to run the shop package tests. Approve?", "interrupt", "", "", true)), nil
		}
		recheck = call.Text()
		return braintest.Response(*braintest.NothingToAdd()), nil
	}}
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 20 * time.Millisecond })
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	pusher := h.pusher
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for len(pusher.all()) < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	h.block("w5:p1", "claude-bash-prompt.screen.txt")
	if got := h.pushTexts(); len(got) != 1 || got[0] != template {
		t.Fatalf("pushes = %v, want the template alone: %q", got, template)
	}
	if !strings.Contains(recheck, `"`+template+`"`) {
		t.Errorf("recheck does not quote the spoken template:\n%s", recheck)
	}
	p, _ := h.pending()
	if p.Command != "go test ./shop -v" {
		t.Errorf("pending command = %q", p.Command)
	}
}
