package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

type fakeProcFS struct {
	dir       string
	mu        sync.Mutex
	trees     map[int][]int
	openFiles map[int][]string
}

func newFakeProcFS(t *testing.T) *fakeProcFS {
	t.Helper()
	return &fakeProcFS{
		dir:       t.TempDir(),
		trees:     map[int][]int{},
		openFiles: map[int][]string{},
	}
}

func (f *fakeProcFS) option() func(*Config) {
	return func(c *Config) {
		c.WorkerLog = workerlog.New(workerlog.Options{
			ClaudeProjects: filepath.Join(f.dir, "claude", "projects"),
			CodexSessions:  filepath.Join(f.dir, "codex", "sessions"),
			TreePIDs: func(roots workerlog.ProcessRoots) []int {
				f.mu.Lock()
				defer f.mu.Unlock()
				var out []int
				for _, p := range roots.Direct() {
					out = append(out, p)
					out = append(out, f.trees[p]...)
				}
				return out
			},
			OpenFiles: func(pids []int) []string {
				f.mu.Lock()
				defer f.mu.Unlock()
				var out []string
				for _, p := range pids {
					out = append(out, f.openFiles[p]...)
				}
				return out
			},
		})
	}
}

func (f *fakeProcFS) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fakeProcFS) setClaudePID(t *testing.T, pid int, sessionID string) {
	t.Helper()
	f.write(t, filepath.Join("claude", "sessions", fmt.Sprintf("%d.json", pid)), fmt.Sprintf(`{"sessionId":%q}`, sessionID))
}

func (f *fakeProcFS) setTree(root int, children ...int) {
	f.mu.Lock()
	f.trees[root] = children
	f.mu.Unlock()
}

func (f *fakeProcFS) setOpen(pid int, files ...string) {
	f.mu.Lock()
	f.openFiles[pid] = files
	f.mu.Unlock()
}

func setPaneFG(srv interface{ SetProcessInfo(herdr.ProcessInfo) }, pane string, shellPID, fgPID uint32, name string) {
	srv.SetProcessInfo(herdr.ProcessInfo{
		PaneID:                 pane,
		ShellPID:               shellPID,
		ForegroundProcessGroup: fgPID,
		ForegroundProcesses:    []herdr.ProcessItem{{PID: fgPID, Name: name}},
	})
}

func TestSecondZeroSessionFromForegroundPID(t *testing.T) {
	proc := newFakeProcFS(t)
	claudeID := "aaaaaaaa-1111-4222-8333-444444444444"
	codexID := "01a0c000-1111-7222-8333-444444444444"

	inv := &fakeInvestigator{answer: "Forked from the live worker.", session: forkSession, backend: "claude"}
	h := newInvestigationHarness(t, inv, proc.option())

	// Register w5:p1 ("auth", claude) and w6:p1 ("docs", codex) with NO agent_session.
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)

	// Wrapper PID 410 spawns claude child PID 412; codex PID 510 holds rollout open.
	setPaneFG(h.srv, "w5:p1", 100, 410, "claude-wrapper")
	proc.setTree(410, 412)
	proc.setClaudePID(t, 412, claudeID)
	proc.write(t, filepath.Join("claude", "projects", workerlog.ClaudeSlug("/work/w5:p1"), claudeID+".jsonl"), strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Token refresh"}`,
		`{"type":"user","message":{"role":"user","content":` + jsonString(withSignOff("Fix token refresh")) + `}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/work/w5:p1/refresh.go"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Token refresh now retries once."}]}}`,
	}, "\n")+"\n")

	setPaneFG(h.srv, "w6:p1", 101, 510, "codex")
	rollout := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-00-00-"+codexID+".jsonl"), strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"user_message","message":` + jsonString(withSignOff("Update docs")) + `}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Updated the API docs."}}`,
	}, "\n")+"\n")
	proc.write(t, filepath.Join("codex", "session_index.jsonl"), fmt.Sprintf(`{"id":%q,"thread_name":"API docs"}`+"\n", codexID))
	proc.setOpen(510, rollout)

	// 1. ReadSession works at second zero (no 30s snapshot) and does not move the journal cursor.
	host := brainHost{h.sup}
	sr, err := host.ReadSession(context.Background(), "w5:p1")
	if err != nil || sr.LastAssistant != "Token refresh now retries once." || sr.Instruction != "Fix token refresh." {
		t.Fatalf("ReadSession(w5:p1) = %+v, %v", sr, err)
	}

	// 2. Worker investigation forks the PID-discovered UUID even with no herdr agent_session.
	if _, err := startWorker(h, "why only one retry?", ""); err != nil {
		t.Fatalf("startInvestigation(source=worker): %v", err)
	}
	waitFor(t, func() bool { return len(inv.all()) == 1 }, "worker fork query")
	if q := inv.all()[0]; !q.Fork || q.Session != claudeID || q.Backend != "claude" {
		t.Fatalf("fork query = %+v, want claude fork of %s", q, claudeID)
	}

	// 3. Finishing both turns within <30s journals both summaries, reads both titles, and syncs SessionValue.
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)

	r1, _ := h.store.Get("w5:p1")
	if len(r1.Journal) != 1 || r1.Journal[0].LastAssistant != "Token refresh now retries once." || r1.Title != "Token refresh" || r1.SessionValue != claudeID {
		t.Fatalf("claude row after second-zero finish = title %q session %q journal %+v", r1.Title, r1.SessionValue, r1.Journal)
	}
	r2, _ := h.store.Get("w6:p1")
	if len(r2.Journal) != 1 || r2.Journal[0].LastAssistant != "Updated the API docs." || r2.Title != "API docs" || r2.SessionValue != codexID {
		t.Fatalf("codex row after second-zero finish = title %q session %q journal %+v", r2.Title, r2.SessionValue, r2.Journal)
	}
}

func TestSessionRotationAcrossClearAndNewSamePID(t *testing.T) {
	oldRetry := journalRetry
	journalRetry = 5 * time.Millisecond
	t.Cleanup(func() { journalRetry = oldRetry })

	proc := newFakeProcFS(t)
	oldClaude := "11111111-2222-4333-8444-000000000001"
	newClaude := "11111111-2222-4333-8444-000000000002"
	oldCodex := "01a0c000-2222-7333-8444-000000000001"
	newCodexWindow := "01a0c000-2222-7333-8444-000000000002"
	newCodexBlocked := "01a0c000-2222-7333-8444-000000000003"
	newCodexTerm := "01a0c000-2222-7333-8444-000000000004"

	inv := &fakeInvestigator{answer: "Post-clear context.", session: forkSession, backend: "claude"}
	h := newInvestigationHarness(t, inv, proc.option())
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)
	if _, err := h.store.Update("w5:p1", func(a *state.Agent) {
		// Stale snapshot value on the row before /clear.
		a.SessionSource, a.SessionKind, a.SessionValue = "herdr:claude", herdr.SessionKindID, oldClaude
	}); err != nil {
		t.Fatal(err)
	}

	setPaneFG(h.srv, "w5:p1", 100, 420, "claude")
	proc.setClaudePID(t, 420, oldClaude)
	proc.write(t, filepath.Join("claude", "projects", workerlog.ClaudeSlug("/work/w5:p1"), oldClaude+".jsonl"),
		`{"type":"ai-title","aiTitle":"Before clear"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Summary before clear."}]}}`+"\n")

	setPaneFG(h.srv, "w6:p1", 101, 520, "codex")
	rOld := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-00-00-"+oldCodex+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Codex turn one."}}`+"\n")
	proc.setOpen(520, rOld)

	h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)

	// Claude /clear in the same PID updates 420.json while row.SessionValue still holds oldClaude.
	proc.setClaudePID(t, 420, newClaude)
	proc.write(t, filepath.Join("claude", "projects", workerlog.ClaudeSlug("/work/w5:p1"), newClaude+".jsonl"),
		`{"type":"ai-title","aiTitle":"After clear"}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Summary after clear."}]}}`+"\n")
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)

	cRow, _ := h.store.Get("w5:p1")
	if len(cRow.Journal) != 2 || cRow.Journal[1].LastAssistant != "Summary after clear." || cRow.Title != "After clear" || cRow.SessionValue != newClaude {
		t.Fatalf("claude after /clear = title %q session %q journal %+v", cRow.Title, cRow.SessionValue, cRow.Journal)
	}
	if _, err := startWorker(h, "what happened after clear?", ""); err != nil {
		t.Fatalf("startWorker after /clear: %v", err)
	}
	waitFor(t, func() bool { return len(inv.all()) == 1 }, "post-clear fork")
	if got := inv.all()[0].Session; got != newClaude {
		t.Fatalf("forked session = %q, want %q (not stale pre-/clear %q)", got, newClaude, oldClaude)
	}

	// Subsequent transitions carrying Watcher's cached pre-/clear session (even after
	// cache invalidation or restart when pane.process_info returns no live PIDs)
	// must not revert row.SessionValue to oldClaude.
	h.sup.cfg.WorkerLog.Invalidate("w5:p1")
	h.srv.SetProcessInfo(herdr.ProcessInfo{PaneID: "w5:p1"})
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID:  "w5:p1",
		From:    herdr.StateDone,
		To:      herdr.StateIdle,
		Agent:   "claude",
		Cwd:     "/work/w5:p1",
		Session: &herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: herdr.SessionKindID, Value: oldClaude},
	})
	setPaneFG(h.srv, "w5:p1", 100, 420, "claude")
	if afterStale, _ := h.store.Get("w5:p1"); afterStale.SessionValue != newClaude || afterStale.SessionSource != "pid:claude" || afterStale.Title != "After clear" {
		t.Fatalf("claude after stale transition + missing roots = title %q source %q session %q, want After clear pid:claude %q", afterStale.Title, afterStale.SessionSource, afterStale.SessionValue, newClaude)
	}

	// Codex /new sent through WindowPrompt invalidates the cached FD for PID 520.
	proc.write(t, filepath.Join("codex", "session_index.jsonl"), strings.Join([]string{
		fmt.Sprintf(`{"id":%q,"thread_name":"Window thread"}`, newCodexWindow),
		fmt.Sprintf(`{"id":%q,"thread_name":"Blocked thread"}`, newCodexBlocked),
		fmt.Sprintf(`{"id":%q,"thread_name":"Terminal thread"}`, newCodexTerm),
	}, "\n")+"\n")
	if _, err := h.sup.WindowPrompt(context.Background(), "w6:p1", "/new"); err != nil {
		t.Fatalf("WindowPrompt(/new): %v", err)
	}
	rNewWin := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-05-00-"+newCodexWindow+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Codex after window /new."}}`+"\n")
	now := time.Now()
	_ = os.Chtimes(rOld, now.Add(-3*time.Minute), now.Add(-3*time.Minute))
	_ = os.Chtimes(rNewWin, now.Add(-2*time.Minute), now.Add(-2*time.Minute))
	proc.setOpen(520, rNewWin)

	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	xRow, _ := h.store.Get("w6:p1")
	if len(xRow.Journal) != 2 || xRow.Journal[1].LastAssistant != "Codex after window /new." || xRow.SessionValue != newCodexWindow || xRow.Title != "Window thread" {
		t.Fatalf("codex after WindowPrompt(/new) = title %q session %q journal %+v", xRow.Title, xRow.SessionValue, xRow.Journal)
	}

	// Codex /new typed directly in the terminal (outside sheepdog) followed by a
	// blocked transition (working -> blocked): rNewWin has 0 new bytes, so
	// recordJournal invalidates immediately, journals rNewBlocked, updates Title
	// and SessionValue, and ReadSession sees the rotated rollout while blocked.
	rNewBlocked := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-08-00-"+newCodexBlocked+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Codex needs approval in new thread."}}`+"\n")
	_ = os.Chtimes(rNewBlocked, now.Add(-time.Minute), now.Add(-time.Minute))
	proc.setOpen(520, rNewBlocked)
	h.srv.SetScreen("w6:p1", "Allow command?\n1. Yes\n2. No\n")

	h.transition("w6:p1", herdr.StateDone, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateBlocked)
	xBlocked, _ := h.store.Get("w6:p1")
	if len(xBlocked.Journal) != 3 || xBlocked.Journal[2].Event != "blocked" || xBlocked.Journal[2].LastAssistant != "Codex needs approval in new thread." || xBlocked.SessionValue != newCodexBlocked || xBlocked.Title != "Blocked thread" {
		t.Fatalf("codex on blocked after terminal /new = title %q session %q journal %+v", xBlocked.Title, xBlocked.SessionValue, xBlocked.Journal)
	}
	if sr, err := (brainHost{h.sup}).ReadSession(context.Background(), "w6:p1"); err != nil || sr.LastAssistant != "Codex needs approval in new thread." {
		t.Fatalf("ReadSession while blocked after terminal /new = %+v, %v", sr, err)
	}

	// Another terminal /new finishing on working -> done also updates both journal and Title.
	rNewTerm := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T10-10-00-"+newCodexTerm+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Codex after terminal /new."}}`+"\n")
	_ = os.Chtimes(rNewTerm, now, now)
	proc.setOpen(520, rNewTerm)
	h.srv.SetScreen("w6:p1", "Done.\n")

	h.transition("w6:p1", herdr.StateBlocked, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	xRow2, _ := h.store.Get("w6:p1")
	if len(xRow2.Journal) != 4 || xRow2.Journal[3].LastAssistant != "Codex after terminal /new." || xRow2.SessionValue != newCodexTerm || xRow2.Title != "Terminal thread" {
		t.Fatalf("codex after terminal /new = title %q session %q journal %+v", xRow2.Title, xRow2.SessionValue, xRow2.Journal)
	}
}

func TestCodexTerminalNewRotationDiscoveredOnBlockedTurn(t *testing.T) {
	proc := newFakeProcFS(t)
	firstID := "01a0c000-3333-7444-8555-000000000001"
	rotatedID := "01a0c000-3333-7444-8555-000000000002"

	inv := &fakeInvestigator{answer: "ok", session: forkSession, backend: "codex"}
	h := newInvestigationHarness(t, inv, proc.option())
	h.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)
	setPaneFG(h.srv, "w6:p1", 101, 530, "codex")

	now := time.Now()
	rFirst := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T11-00-00-"+firstID+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"First thread turn."}}`+"\n")
	_ = os.Chtimes(rFirst, now.Add(-time.Minute), now.Add(-time.Minute))
	proc.write(t, filepath.Join("codex", "session_index.jsonl"), strings.Join([]string{
		fmt.Sprintf(`{"id":%q,"thread_name":"First thread"}`, firstID),
		fmt.Sprintf(`{"id":%q,"thread_name":"Rotated thread"}`, rotatedID),
	}, "\n")+"\n")
	proc.setOpen(530, rFirst)

	h.transition("w6:p1", herdr.StateWorking, herdr.StateIdle)

	// User types /new directly in Codex's terminal; the new thread hits an approval prompt (working -> blocked).
	rRotated := proc.write(t, filepath.Join("codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T11-05-00-"+rotatedID+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Need approval in rotated thread."}}`+"\n")
	_ = os.Chtimes(rRotated, now, now)
	proc.setOpen(530, rRotated)
	h.srv.SetScreen("w6:p1", "Allow command?\n1. Yes\n2. No\n")

	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateBlocked)

	row, _ := h.store.Get("w6:p1")
	if row.SessionValue != rotatedID || row.Title != "Rotated thread" {
		t.Fatalf("blocked row after terminal /new = title %q session %q, want Rotated thread %s", row.Title, row.SessionValue, rotatedID)
	}
	if len(row.Journal) != 2 || row.Journal[1].Event != "blocked" || row.Journal[1].LastAssistant != "Need approval in rotated thread." {
		t.Fatalf("blocked journal after terminal /new = %+v", row.Journal)
	}
	if sr, err := (brainHost{h.sup}).ReadSession(context.Background(), "w6:p1"); err != nil || sr.LastAssistant != "Need approval in rotated thread." {
		t.Fatalf("ReadSession while blocked = %+v, %v", sr, err)
	}
}

func TestSharedFolderWorkersKeepSeparatePIDTranscripts(t *testing.T) {
	proc := newFakeProcFS(t)
	sharedCwd := "/work/shared-repo"
	idA := "aaaaaaaa-0000-4000-8000-000000000010"
	idB := "bbbbbbbb-0000-4000-8000-000000000020"

	inv := &fakeInvestigator{answer: "ok", session: forkSession, backend: "claude"}
	h := newInvestigationHarness(t, inv, proc.option())
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	for _, pane := range []string{"w5:p1", "w6:p1"} {
		if _, err := h.store.Update(pane, func(a *state.Agent) {
			a.Cwd = sharedCwd
		}); err != nil {
			t.Fatal(err)
		}
	}

	setPaneFG(h.srv, "w5:p1", 100, 610, "claude")
	setPaneFG(h.srv, "w6:p1", 101, 620, "claude")
	proc.setClaudePID(t, 610, idA)
	proc.setClaudePID(t, 620, idB)
	slug := workerlog.ClaudeSlug(sharedCwd)
	proc.write(t, filepath.Join("claude", "projects", slug, idA+".jsonl"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Agent A finished auth."}]}}`+"\n")
	proc.write(t, filepath.Join("claude", "projects", slug, idB+".jsonl"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Agent B finished billing."}]}}`+"\n")

	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)

	rA, _ := h.store.Get("w5:p1")
	rB, _ := h.store.Get("w6:p1")
	if len(rA.Journal) != 1 || rA.Journal[0].LastAssistant != "Agent A finished auth." || rA.SessionValue != idA {
		t.Fatalf("pane A = session %q journal %+v", rA.SessionValue, rA.Journal)
	}
	if len(rB.Journal) != 1 || rB.Journal[0].LastAssistant != "Agent B finished billing." || rB.SessionValue != idB {
		t.Fatalf("pane B = session %q journal %+v", rB.SessionValue, rB.Journal)
	}
}

func TestOlderHerdrWithoutProcessInfoFallsBackCleanly(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, withWorkerLog(dir))
	h.srv.Handle("pane.process_info", func(json.RawMessage) (any, error) {
		return nil, &herdr.Error{Code: "invalid_request", Message: "invalid request: unknown variant `pane.process_info`"}
	})
	h.addAgent("w6:p1", "docs", "claude", herdr.StateWorking)
	claudeTurn(t, h, "w6:p1", "Fallback transcript works on older herdr.")

	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	h.transition("w6:p1", herdr.StateDone, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)

	row, _ := h.store.Get("w6:p1")
	if len(row.Journal) == 0 || !strings.Contains(row.Journal[0].LastAssistant, "Fallback transcript works") {
		t.Fatalf("journal = %+v", row.Journal)
	}
	if got := len(h.srv.RequestsFor("pane.process_info")); got != 1 {
		t.Fatalf("pane.process_info calls = %d, want 1 (cached unsupported after first invalid_request)", got)
	}
}
