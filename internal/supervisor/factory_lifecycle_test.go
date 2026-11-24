package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// TestFactoryVoiceControlFinishedAndLiveStates verifies plan.md 14.4: voice stop
// ends a finished project's live final-journal coordinator, omits a finished
// project when nothing ran, and voice start skips finished projects without refusal.
func TestFactoryVoiceControlFinishedAndLiveStates(t *testing.T) {
	paths, hcSrv := factory.Paths{DataDir: t.TempDir()}, hctest.New(t)
	hcSrv.OnExec = hctest.GitExec
	client := hc.NewClient(hcSrv.Path)
	eng := factory.New(paths, client)
	eng.StartCheck = func(context.Context, factory.DoctorOptions) []onboard.Result { return nil }
	mkRepo := func(id string) string {
		r := filepath.Join(t.TempDir(), id)
		for _, args := range [][]string{{"init", "-b", "main", r}, {"-C", r, "config", "--local", "sheepdog.projectId", id}} {
			if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, b)
			}
		}
		return r
	}
	expect := func(ok bool, m string, a ...any) {
		if !ok {
			t.Errorf(m, a...)
		}
	}
	hcSrv.Add(hc.Terminal{Address: "coord-fin-live", Name: "sd-fin-live-coord", State: "live", Labels: map[string]string{"sheepdog.project": "fin-live", "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "r-fin"}})
	for _, s := range []flow.State{
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "fin-live", Folder: mkRepo("fin-live"), Goal: "done live", Started: true, Finished: true, RunID: "coord-fin-live"}},
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "fin-idle", Folder: mkRepo("fin-idle"), Goal: "done idle", Started: true, Finished: true}},
		{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "stopped-one", Folder: mkRepo("stopped-one"), Goal: "resume me", Started: false}},
	} {
		_ = factory.SaveState(paths.State(s.Project.ID), s)
	}
	h := newHarness(t, func(c *Config) { c.Factory = eng })
	res := h.say("stop the factory")
	expect(strings.Contains(res.Spoken, "fin-live") && !strings.Contains(res.Spoken, "fin-idle"), "voice stop = %q, want fin-live only", res.Spoken)
	_, getErr := client.Get(context.Background(), "coord-fin-live")
	stLive, _ := factory.LoadState(paths.State("fin-live"))
	stIdle, _ := factory.LoadState(paths.State("fin-idle"))
	expect(errors.Is(getErr, hc.ErrNotFound) && stLive.Project.Finished && stIdle.Seq == 2 && stIdle.Project.Finished, "after stop: err=%v live.fin=%v idle.seq=%d idle.fin=%v", getErr, stLive.Project.Finished, stIdle.Seq, stIdle.Project.Finished)
	h.clock.Advance(time.Second)
	res2 := h.say("stop the factory")
	expect(!strings.Contains(res2.Spoken, "fin-live") && !strings.Contains(res2.Spoken, "fin-idle"), "second stop = %q", res2.Spoken)
	h.clock.Advance(time.Second)
	res3 := h.say("start the factory")
	stStop, _ := factory.LoadState(paths.State("stopped-one"))
	stIdle, _ = factory.LoadState(paths.State("fin-idle"))
	expect(strings.Contains(res3.Spoken, "stopped-one") && !strings.Contains(res3.Spoken, "fin-") && !strings.Contains(res3.Spoken, "Could not") && stStop.Project.Started && stIdle.Project.Finished, "voice start = %q stopped=%v fin=%v", res3.Spoken, stStop.Project.Started, stIdle.Project.Finished)
}
