package conformance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

func foregroundProcessTreeIdentifiesLiveSessions(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	sharedDir := t.TempDir()
	logRoot := t.TempDir()

	writeTranscript := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	children := map[int][]int{}
	openByPID := map[int][]string{}

	reader := workerlog.New(workerlog.Options{
		ClaudeProjects: filepath.Join(logRoot, "claude", "projects"),
		CodexSessions:  filepath.Join(logRoot, "codex", "sessions"),
		TreePIDs: func(roots workerlog.ProcessRoots) []int {
			mu.Lock()
			defer mu.Unlock()
			var out []int
			for _, p := range roots.Direct() {
				out = append(out, p)
				out = append(out, children[p]...)
			}
			return out
		},
		OpenFiles: func(pids []int) []string {
			mu.Lock()
			defer mu.Unlock()
			var out []string
			for _, p := range pids {
				out = append(out, openByPID[p]...)
			}
			return out
		},
	})

	rootsOf := func(b herdr.Backend, paneID string) workerlog.ProcessRoots {
		t.Helper()
		pi, ok := b.(interface {
			ProcessInfo(context.Context, string) (herdr.ProcessInfo, error)
		})
		if !ok {
			t.Fatalf("backend %T does not implement ProcessInfo", b)
		}
		info, err := pi.ProcessInfo(ctx, paneID)
		if err != nil {
			t.Fatalf("ProcessInfo(%s): %v", paneID, err)
		}
		if info.ShellForeground() {
			return workerlog.ProcessRoots{}
		}
		var fg int
		for _, p := range info.ForegroundProcesses {
			if p.PID > 0 && p.PID != info.ShellPID {
				fg = int(p.PID)
				break
			}
		}
		if fg == 0 && len(info.ForegroundProcesses) > 0 {
			fg = int(info.ForegroundProcesses[0].PID)
		}
		return workerlog.ProcessRoots{
			PID:      fg,
			Pgrp:     int(info.ForegroundProcessGroup),
			ShellPID: int(info.ShellPID),
		}
	}

	// Open three panes in the same folder: two Claude workers and one Codex worker.
	p1, err := d.NewAgent(ctx, sharedDir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := d.NewAgent(ctx, sharedDir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	p3, err := d.NewAgent(ctx, sharedDir, "codex")
	if err != nil {
		t.Fatal(err)
	}

	r1 := rootsOf(d.Backend(), p1.PaneID)
	r2 := rootsOf(d.Backend(), p2.PaneID)
	r3 := rootsOf(d.Backend(), p3.PaneID)
	if r1.PID <= 0 || r2.PID <= 0 || r3.PID <= 0 || r1.PID == r2.PID || r1.PID == r3.PID || r2.PID == r3.PID {
		t.Fatalf("distinct foreground PIDs required per pane: p1=%+v p2=%+v p3=%+v", r1, r2, r3)
	}

	claudeID1 := "11111111-0000-4000-8000-000000000001"
	claudeID1AfterClear := "11111111-0000-4000-8000-000000000099"
	claudeID2 := "22222222-0000-4000-8000-000000000002"
	codexID1 := "33333333-0000-7000-8000-000000000003"
	codexIDAfterExit := "44444444-0000-7000-8000-000000000004"

	slugDir := filepath.Join(logRoot, "claude", "projects", workerlog.ClaudeSlug(sharedDir))
	writeTranscript(filepath.Join(slugDir, claudeID1+".jsonl"), `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Pane one before clear."}]}}`+"\n")
	writeTranscript(filepath.Join(slugDir, claudeID2+".jsonl"), `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Pane two in shared folder."}]}}`+"\n")
	rollout1 := filepath.Join(logRoot, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-00-00-"+codexID1+".jsonl")
	writeTranscript(rollout1, `{"type":"event_msg","payload":{"type":"agent_message","message":"Pane three codex summary."}}`+"\n")

	// Pane 1 runs claude as a child of its foreground wrapper PID; pane 2 is direct; pane 3 holds rollout1 open.
	claudeChildPID := r1.PID + 50000
	mu.Lock()
	children[r1.PID] = []int{claudeChildPID}
	openByPID[r3.PID] = []string{rollout1}
	mu.Unlock()
	writeTranscript(filepath.Join(logRoot, "claude", "sessions", fmt.Sprintf("%d.json", claudeChildPID)), fmt.Sprintf(`{"sessionId":%q}`, claudeID1))
	writeTranscript(filepath.Join(logRoot, "claude", "sessions", fmt.Sprintf("%d.json", r2.PID)), fmt.Sprintf(`{"sessionId":%q}`, claudeID2))

	// Second zero: no agent_session reported on any pane, all three share sharedDir.
	for _, tc := range []struct {
		pane  string
		kind  string
		roots workerlog.ProcessRoots
		want  string
		id    string
	}{
		{p1.PaneID, "claude", r1, "Pane one before clear.", claudeID1},
		{p2.PaneID, "claude", r2, "Pane two in shared folder.", claudeID2},
		{p3.PaneID, "codex", r3, "Pane three codex summary.", codexID1},
	} {
		sess := workerlog.Session{Pane: tc.pane, Roots: tc.roots, Agent: tc.kind, Cwd: sharedDir}
		res, err := reader.Resolve(sess)
		if err != nil || res.ID != tc.id {
			t.Fatalf("%s Resolve = %+v, %v; want %s", tc.pane, res, err, tc.id)
		}
		dig, err := reader.Read(tc.pane, sess)
		if err != nil || dig.LastAssistant != tc.want {
			t.Fatalf("%s Read = %+v, %v; want %q", tc.pane, dig, err, tc.want)
		}
	}

	// /clear inside pane 1 keeps the same foreground PID and updates <pid>.json.
	writeTranscript(filepath.Join(slugDir, claudeID1AfterClear+".jsonl"), `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Pane one after clear."}]}}`+"\n")
	writeTranscript(filepath.Join(logRoot, "claude", "sessions", fmt.Sprintf("%d.json", claudeChildPID)), fmt.Sprintf(`{"sessionId":%q}`, claudeID1AfterClear))
	digAfterClear, err := reader.Read(p1.PaneID, workerlog.Session{
		Pane: p1.PaneID, Roots: r1, Agent: "claude", Source: "herdr:claude", Kind: "id", Value: claudeID1, Cwd: sharedDir,
	})
	if err != nil || digAfterClear.LastAssistant != "Pane one after clear." {
		t.Fatalf("after /clear Read = %+v, %v", digAfterClear, err)
	}

	// Daemon restart preserves live ProcessInfo discovery on running panes.
	restarted := d.RestartDaemon(t)
	r3AfterRestart := rootsOf(restarted.Backend(), p3.PaneID)
	if r3AfterRestart.PID != r3.PID {
		t.Fatalf("after RestartDaemon roots = %+v, want PID %d", r3AfterRestart, r3.PID)
	}

	// Exiting pane 1 returns it to its plain shell, and launching Codex in that same shell exposes a new PID.
	if err := restarted.Exit(ctx, p1.PaneID); err != nil {
		t.Fatal(err)
	}
	reader.Forget(p1.PaneID)
	if afterExit := rootsOf(restarted.Backend(), p1.PaneID); afterExit.PID != 0 {
		t.Fatalf("plain shell after Exit should have no foreground agent PID, got %+v", afterExit)
	}
	if err := restarted.Run(ctx, p1.PaneID, Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	r1New := rootsOf(restarted.Backend(), p1.PaneID)
	if r1New.PID <= 0 || r1New.PID == r1.PID {
		t.Fatalf("new agent in same shell got roots %+v, want fresh PID != %d", r1New, r1.PID)
	}
	rollout2 := filepath.Join(logRoot, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-10-00-"+codexIDAfterExit+".jsonl")
	writeTranscript(rollout2, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Codex took over pane one."}}`,
	}, "\n")+"\n")
	mu.Lock()
	openByPID[r1New.PID] = []string{rollout2}
	mu.Unlock()
	digNew, err := reader.Read(p1.PaneID, workerlog.Session{Pane: p1.PaneID, Roots: r1New, Agent: "codex", Cwd: sharedDir})
	if err != nil || digNew.LastAssistant != "Codex took over pane one." {
		t.Fatalf("new agent in same shell Read = %+v, %v", digNew, err)
	}
}
