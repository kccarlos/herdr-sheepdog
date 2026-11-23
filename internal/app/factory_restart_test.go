package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

// TestFactoryQuietDaemonRestartRecovery verifies that terminal labels survive a
// daemon restart through state.json and Watcher seeding: when a busy factory
// worker or blocked factory coordinator closes while the daemon is down, or a
// factory reviewer blocks across a restart, Run 2 stays completely silent for
// factory quiet transitions while still announcing a closed busy non-factory
// worker.
func TestFactoryQuietDaemonRestartRecovery(t *testing.T) {
	cfg := testConfig(t)
	cfg.DebounceMS = 20
	cfg.SnapshotIntervalS = 1
	cfg.Supervisor.Chattiness = "chatty"
	cfg.Aliases = map[string]string{
		"w5:p1": "fworker",
		"w5:p2": "freviewer",
		"w5:p3": "fcoord",
		"w6:p1": "nonfactory",
	}

	srv := herdrtest.New(t)
	if err := os.Symlink(srv.Path, cfg.Herdr.Socket); err != nil {
		t.Fatal(err)
	}
	approvalScreen, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", "codex-approval-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}

	workerLabels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "worker"}
	reviewerLabels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "reviewer"}
	coordLabels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "coordinator"}

	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "demo"})
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w6", Label: "app"})
	srv.SetPane(herdr.PaneInfo{PaneID: "w5:p1", WorkspaceID: "w5"})
	srv.SetPane(herdr.PaneInfo{PaneID: "w5:p2", WorkspaceID: "w5"})
	srv.SetPane(herdr.PaneInfo{PaneID: "w5:p3", WorkspaceID: "w5"})
	srv.SetPane(herdr.PaneInfo{PaneID: "w6:p1", WorkspaceID: "w6"})

	srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p1", WorkspaceID: "w5", Agent: "codex", Status: herdr.StateWorking, Labels: workerLabels})
	srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p2", WorkspaceID: "w5", Agent: "codex", Status: herdr.StateWorking, Labels: reviewerLabels})
	srv.SetScreen("w5:p3", string(approvalScreen))
	srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p3", WorkspaceID: "w5", Agent: "claude", Status: herdr.StateBlocked, Labels: coordLabels})
	srv.SetAgent(herdr.AgentInfo{PaneID: "w6:p1", WorkspaceID: "w6", Agent: "codex", Status: herdr.StateWorking})

	// Run 1: discover all panes, wait until the blocked coordinator creates its
	// pending prompt and nonfactory is recorded, then shut the daemon down.
	a1, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrSocket, CLI: CLIOff, Brain: BrainOff, Drain: time.Second, WatcherRetry: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := a1.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- a1.Run(ctx1) }()

	waitFor(t, 5*time.Second, func() bool {
		sawCoordPending, sawNonFactory := false, false
		for _, ev := range a1.elog.Tail(0) {
			if ev.Type == "pending.created" && ev.PaneID == "w5:p3" {
				sawCoordPending = true
			}
			if ev.Type == "herdr.transition" && ev.PaneID == "w6:p1" {
				sawNonFactory = true
			}
		}
		return sawCoordPending && sawNonFactory
	}, "run 1 initial transitions and coordinator pending prompt")

	cancel1()
	if err := <-done1; err != nil {
		t.Fatalf("Run 1: %v", err)
	}

	// While the daemon is down:
	// - busy factory worker w5:p1 closes;
	// - blocked factory coordinator w5:p3 closes (Closed -> StateUnknown, which is quiet for coordinator);
	// - factory reviewer w5:p2 blocks on an approval prompt;
	// - busy non-factory worker w6:p1 closes (must still announce).
	srv.RemoveAgent("w5:p1")
	srv.RemovePane("w5:p1")
	srv.RemoveAgent("w5:p3")
	srv.RemovePane("w5:p3")
	srv.SetScreen("w5:p2", string(approvalScreen))
	srv.SetStatus("w5:p2", herdr.StateBlocked)
	srv.RemoveAgent("w6:p1")
	srv.RemovePane("w6:p1")

	// Run 2: restart on the same state.json and socket.
	a2, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrSocket, CLI: CLIOff, Brain: BrainOff, Drain: time.Second, WatcherRetry: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := a2.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- a2.Run(ctx2) }()

	run2Events := func() []eventlog.Event {
		all := a2.elog.Tail(0)
		startIdx := 0
		for i, ev := range all {
			if ev.Type == "daemon.start" {
				startIdx = i
			}
		}
		return all[startIdx:]
	}

	waitFor(t, 5*time.Second, func() bool {
		sawNonFactoryPush, sawReviewerBlocked := false, false
		for _, ev := range run2Events() {
			if ev.Type == "voice.push" && strings.Contains(string(ev.Payload), "nonfactory has gone away") {
				sawNonFactoryPush = true
			}
			if ev.Type == "herdr.transition" && ev.PaneID == "w5:p2" && strings.Contains(string(ev.Payload), `"to":"blocked"`) {
				sawReviewerBlocked = true
			}
		}
		return sawNonFactoryPush && sawReviewerBlocked
	}, "run 2 snapshot recovery for closed nonfactory and blocked reviewer")

	cancel2()
	if err := <-done2; err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	for _, ev := range run2Events() {
		if ev.Type == "pending.created" && (ev.PaneID == "w5:p1" || ev.PaneID == "w5:p2" || ev.PaneID == "w5:p3") {
			t.Errorf("run 2 created pending prompt for quiet factory pane %s: %s", ev.PaneID, ev.Payload)
		}
		if ev.Type == "voice.push" {
			var pl struct {
				Text    string `json:"text"`
				Urgency string `json:"urgency"`
			}
			if json.Unmarshal(ev.Payload, &pl) == nil && pl.Urgency != "silent" {
				for _, quietAlias := range []string{"fworker", "freviewer", "fcoord"} {
					if strings.Contains(pl.Text, quietAlias) {
						t.Errorf("run 2 spoke push for quiet factory terminal %s after restart: %q", quietAlias, pl.Text)
					}
				}
			}
		}
	}
}
