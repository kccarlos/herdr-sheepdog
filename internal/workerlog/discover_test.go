package workerlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeJSONFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverClaudeFromPIDTree(t *testing.T) {
	dir := t.TempDir()
	cwd := "/work/shared/app"
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	transcript := filepath.Join(dir, "claude", "projects", ClaudeSlug(cwd), sid+".jsonl")
	writeJSONFile(t, transcript, strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Auth session"}`,
		`{"type":"user","message":{"role":"user","content":"Check the token expiry."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Token expiry defaults to one hour."}]}}`,
	}, "\n")+"\n")
	// Shell 100 -> wrapper 400 -> claude 402.
	writeJSONFile(t, filepath.Join(dir, "claude", "sessions", "402.json"), fmt.Sprintf(`{"pid":402,"sessionId":%q,"cwd":%q}`, sid, cwd))

	var treeCalls atomic.Int32
	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs: func(roots ProcessRoots) []int {
			treeCalls.Add(1)
			if roots.PID == 400 {
				return []int{400, 401, 402, roots.ShellPID}
			}
			return roots.Direct()
		},
		OpenFiles: func([]int) []string {
			t.Error("OpenFiles should not run when Claude <pid>.json resolves the session")
			return nil
		},
	})

	sess := Session{Pane: "w1:p1", Roots: ProcessRoots{PID: 400, Pgrp: 400, ShellPID: 100}, Agent: "claude", Cwd: cwd}
	res, err := r.Resolve(sess)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Format != FormatClaude || res.ID != sid || res.Path != transcript || !res.FromPID {
		t.Fatalf("Resolve = %+v, want claude %s at %s", res, sid, transcript)
	}
	if disc, ok := r.Discovered("w1:p1"); !ok || disc.ID != sid {
		t.Fatalf("Discovered = %+v, %v; want %s", disc, ok, sid)
	}
	if title, err := r.Title(sess); err != nil || title != "Auth session" {
		t.Fatalf("Title = %q, %v", title, err)
	}
	if tail, err := r.Tail(sess); err != nil || tail.LastAssistant != "Token expiry defaults to one hour." {
		t.Fatalf("Tail = %+v, %v", tail, err)
	}
	if d, err := r.Read("w1:p1", sess); err != nil || d.LastAssistant != "Token expiry defaults to one hour." || d.Instruction() != "Check the token expiry." {
		t.Fatalf("Read = %+v, %v", d, err)
	}
	// Subsequent calls reuse the cached agent PID and read <pid>.json directly without re-walking the process tree.
	if got := treeCalls.Load(); got != 1 {
		t.Fatalf("TreePIDs calls = %d, want 1", got)
	}
}

func TestDiscoverClaudeAcrossClearSamePID(t *testing.T) {
	dir := t.TempDir()
	cwd := "/work/calc"
	oldID := "11111111-1111-4111-8111-111111111111"
	newID := "22222222-2222-4222-8222-222222222222"
	projDir := filepath.Join(dir, "claude", "projects", ClaudeSlug(cwd))
	pidFile := filepath.Join(dir, "claude", "sessions", "402.json")

	writeJSONFile(t, filepath.Join(projDir, oldID+".jsonl"), strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Old topic"}`,
		`{"type":"user","message":{"role":"user","content":"Old instruction."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Old answer before clear."}]}}`,
	}, "\n")+"\n")
	writeJSONFile(t, pidFile, fmt.Sprintf(`{"sessionId":%q}`, oldID))

	var treeCalls atomic.Int32
	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs: func(roots ProcessRoots) []int {
			treeCalls.Add(1)
			return []int{roots.PID, 402}
		},
		OpenFiles: func([]int) []string { return nil },
	})

	// Suppose herdr's earlier snapshot reported oldID; it remains on the row across /clear.
	sess := Session{
		Pane:   "w1:p1",
		Roots:  ProcessRoots{PID: 400, Pgrp: 400, ShellPID: 100},
		Agent:  "claude",
		Source: "herdr:claude",
		Kind:   "id",
		Value:  oldID,
		Cwd:    cwd,
	}
	first, err := r.Read("w1:p1", sess)
	if err != nil || first.LastAssistant != "Old answer before clear." {
		t.Fatalf("first Read = %+v, %v", first, err)
	}

	// /clear inside the same claude process updates <pid>.json to newID before newID.jsonl is flushed.
	writeJSONFile(t, pidFile, fmt.Sprintf(`{"sessionId":%q}`, newID))
	if _, err := r.Locate(sess); Reason(err) != ReasonNotFound {
		t.Fatalf("Locate right after /clear = %v, want %s (never the stale pre-/clear transcript)", err, ReasonNotFound)
	}
	if disc, ok := r.Discovered("w1:p1"); !ok || disc.ID != newID {
		t.Fatalf("Discovered after /clear = %+v, %v; want %s", disc, ok, newID)
	}

	// Now the post-/clear session writes its first turn.
	writeJSONFile(t, filepath.Join(projDir, newID+".jsonl"), strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Fresh topic"}`,
		`{"type":"user","message":{"role":"user","content":"Fresh instruction."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fresh answer after clear."}]}}`,
	}, "\n")+"\n")

	after, err := r.Read("w1:p1", sess)
	if err != nil || !after.Fresh || after.LastAssistant != "Fresh answer after clear." || after.Instruction() != "Fresh instruction." {
		t.Fatalf("Read after /clear = %+v, %v", after, err)
	}
	if title, err := r.Title(sess); err != nil || title != "Fresh topic" {
		t.Fatalf("Title after /clear = %q, %v", title, err)
	}
	if got := treeCalls.Load(); got != 1 {
		t.Fatalf("TreePIDs calls across /clear = %d, want 1 (agentPID cached)", got)
	}
}

func TestDiscoverCodexOpenFDAndCaching(t *testing.T) {
	dir := t.TempDir()
	codexRoot := filepath.Join(dir, "codex", "sessions")
	indexPath := filepath.Join(dir, "codex", "session_index.jsonl")
	id1 := "01a0c000-0000-7000-8000-000000000101"
	id2 := "01a0c000-0000-7000-8000-000000000202"
	rollout1 := filepath.Join(codexRoot, "2026", "11", "04", "rollout-2026-11-04T10-00-00-"+id1+".jsonl")
	rollout2 := filepath.Join(codexRoot, "2026", "11", "04", "rollout-2026-11-04T10-05-00-"+id2+".jsonl")

	writeJSONFile(t, rollout1, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"user_message","message":"Run lint."}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Lint is clean."}}`,
	}, "\n")+"\n")
	writeJSONFile(t, indexPath, strings.Join([]string{
		fmt.Sprintf(`{"id":%q,"thread_name":"Lint check"}`, id1),
		fmt.Sprintf(`{"id":%q,"thread_name":"Second thread"}`, id2),
	}, "\n")+"\n")

	var mu sync.Mutex
	openList := []string{"/dev/ttys004", indexPath, rollout1}
	var treeCalls, openCalls atomic.Int32

	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  codexRoot,
		CodexIndex:     indexPath,
		TreePIDs: func(roots ProcessRoots) []int {
			treeCalls.Add(1)
			return []int{roots.PID, 501}
		},
		OpenFiles: func(pids []int) []string {
			openCalls.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), openList...)
		},
	})

	sess := Session{Pane: "w2:p1", Roots: ProcessRoots{PID: 500, Pgrp: 500, ShellPID: 100}, Agent: "codex"}
	res, err := r.Resolve(sess)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Format != FormatCodex || res.ID != id1 || res.Path != rollout1 || !res.FromPID {
		t.Fatalf("Resolve = %+v", res)
	}
	if title, err := r.Title(sess); err != nil || title != "Lint check" {
		t.Fatalf("Title = %q, %v", title, err)
	}
	if tail, err := r.Tail(sess); err != nil || tail.LastAssistant != "Lint is clean." {
		t.Fatalf("Tail = %+v, %v", tail, err)
	}
	if d, err := r.Read("w2:p1", sess); err != nil || d.LastAssistant != "Lint is clean." {
		t.Fatalf("Read = %+v, %v", d, err)
	}
	if openCalls.Load() != 1 || treeCalls.Load() != 1 {
		t.Fatalf("probes = tree %d open %d, want 1 and 1 (cached by pane+pid)", treeCalls.Load(), openCalls.Load())
	}

	// When the 30s snapshot later reports the same session id, neither the probe nor the read cursor resets.
	withReported := sess
	withReported.Source, withReported.Kind, withReported.Value = "herdr:codex", "id", id1
	if again, err := r.Read("w2:p1", withReported); err != nil || again.Bytes != 0 {
		t.Fatalf("Read after snapshot reported same id = %+v, %v; want 0 new bytes", again, err)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("openCalls after same reported id = %d, want 1", openCalls.Load())
	}

	// Same-PID rotation (/new): even if the old FD were still open alongside the new one,
	// Invalidate re-probes and picks the newer rollout file.
	writeJSONFile(t, rollout2, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"user_message","message":"Start fresh."}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Started the second thread."}}`,
	}, "\n")+"\n")
	now := time.Now()
	_ = os.Chtimes(rollout1, now.Add(-time.Minute), now.Add(-time.Minute))
	_ = os.Chtimes(rollout2, now, now)
	mu.Lock()
	openList = []string{rollout1, rollout2}
	mu.Unlock()

	r.Invalidate("w2:p1")
	rotated, err := r.Read("w2:p1", sess)
	if err != nil || rotated.LastAssistant != "Started the second thread." || rotated.Path != rollout2 {
		t.Fatalf("Read after Invalidate = %+v, %v", rotated, err)
	}
	if title, err := r.Title(sess); err != nil || title != "Second thread" {
		t.Fatalf("Title after rotation = %q, %v", title, err)
	}
	if openCalls.Load() != 2 {
		t.Fatalf("openCalls after Invalidate = %d, want 2", openCalls.Load())
	}

	// Missing file triggers an automatic re-probe without explicit Invalidate.
	if err := os.Remove(rollout2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	openList = []string{rollout1}
	mu.Unlock()
	if p, err := r.Locate(sess); err != nil || p != rollout1 {
		t.Fatalf("Locate after missing file = %q, %v; want %q", p, err, rollout1)
	}
	if openCalls.Load() != 3 {
		t.Fatalf("openCalls after missing file = %d, want 3", openCalls.Load())
	}
}

func TestDiscoverSharedFolderDisambiguatesByPanePID(t *testing.T) {
	dir := t.TempDir()
	sharedCwd := "/work/monorepo"
	c1 := "10000000-0000-4000-8000-000000000001"
	c2 := "10000000-0000-4000-8000-000000000002"
	x1 := "20000000-0000-7000-8000-000000000001"
	x2 := "20000000-0000-7000-8000-000000000002"

	projDir := filepath.Join(dir, "claude", "projects", ClaudeSlug(sharedCwd))
	writeJSONFile(t, filepath.Join(projDir, c1+".jsonl"), `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Claude one in monorepo."}]}}`+"\n")
	writeJSONFile(t, filepath.Join(projDir, c2+".jsonl"), `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Claude two in monorepo."}]}}`+"\n")
	writeJSONFile(t, filepath.Join(dir, "claude", "sessions", "710.json"), fmt.Sprintf(`{"sessionId":%q}`, c1))
	writeJSONFile(t, filepath.Join(dir, "claude", "sessions", "720.json"), fmt.Sprintf(`{"sessionId":%q}`, c2))

	rx1 := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T11-00-00-"+x1+".jsonl")
	rx2 := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T11-00-01-"+x2+".jsonl")
	writeJSONFile(t, rx1, `{"type":"event_msg","payload":{"type":"agent_message","message":"Codex one in monorepo."}}`+"\n")
	writeJSONFile(t, rx2, `{"type":"event_msg","payload":{"type":"agent_message","message":"Codex two in monorepo."}}`+"\n")

	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs:       func(roots ProcessRoots) []int { return roots.Direct() },
		OpenFiles: func(pids []int) []string {
			switch pids[0] {
			case 810:
				return []string{rx1}
			case 820:
				return []string{rx2}
			}
			return nil
		},
	})

	cases := []struct {
		pane string
		pid  int
		kind string
		id   string
		want string
	}{
		{"w1:p1", 710, "claude", c1, "Claude one in monorepo."},
		{"w1:p2", 720, "claude", c2, "Claude two in monorepo."},
		{"w1:p3", 810, "codex", x1, "Codex one in monorepo."},
		{"w1:p4", 820, "codex", x2, "Codex two in monorepo."},
	}
	for _, tc := range cases {
		sess := Session{Pane: tc.pane, Roots: ProcessRoots{PID: tc.pid}, Agent: tc.kind, Cwd: sharedCwd}
		res, err := r.Resolve(sess)
		if err != nil || res.ID != tc.id {
			t.Fatalf("%s Resolve = %+v, %v; want %s", tc.pane, res, err, tc.id)
		}
		d, err := r.Read(tc.pane, sess)
		if err != nil || d.LastAssistant != tc.want {
			t.Fatalf("%s Read = %+v, %v; want %q", tc.pane, d, err, tc.want)
		}
	}
}

func TestDiscoverLiveOSProcess(t *testing.T) {
	dir := t.TempDir()
	self := os.Getpid()
	codexUUID := "01a0c000-0000-7000-8000-000000000999"
	rollout := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T19-00-00-"+codexUUID+".jsonl")
	writeJSONFile(t, rollout, `{"type":"event_msg","payload":{"type":"agent_message","message":"From a live open FD."}}`+"\n")

	fh, err := os.Open(rollout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()

	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
	})
	codexSess := Session{Pane: "live:codex", Roots: ProcessRoots{PID: self}, Agent: "codex"}
	res, err := r.Resolve(codexSess)
	if err != nil {
		t.Fatalf("live Codex Resolve: %v", err)
	}
	// EvalSymlinks handles macOS /var -> /private/var on temp dirs.
	wantReal, _ := filepath.EvalSymlinks(rollout)
	gotReal, _ := filepath.EvalSymlinks(res.Path)
	if res.ID != codexUUID || gotReal != wantReal {
		t.Fatalf("live Codex Resolve = %+v, want id %s path %s", res, codexUUID, wantReal)
	}

	claudeUUID := "33333333-4444-4555-8666-999999999999"
	cwd := "/work/live"
	claudeTranscript := filepath.Join(dir, "claude", "projects", ClaudeSlug(cwd), claudeUUID+".jsonl")
	writeJSONFile(t, claudeTranscript, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"From a live pid json."}]}}`+"\n")
	writeJSONFile(t, filepath.Join(dir, "claude", "sessions", fmt.Sprintf("%d.json", self)), fmt.Sprintf(`{"sessionId":%q}`, claudeUUID))

	claudeSess := Session{Pane: "live:claude", Roots: ProcessRoots{PID: self}, Agent: "claude", Cwd: cwd}
	cres, err := r.Resolve(claudeSess)
	if err != nil || cres.ID != claudeUUID || cres.Path != claudeTranscript {
		t.Fatalf("live Claude Resolve = %+v, %v", cres, err)
	}
}

func TestUnsupportedKindWithPIDDoesNotProbeOS(t *testing.T) {
	r := New(Options{
		ClaudeProjects: filepath.Join(t.TempDir(), "claude", "projects"),
		CodexSessions:  filepath.Join(t.TempDir(), "codex", "sessions"),
		TreePIDs: func(ProcessRoots) []int {
			t.Fatal("TreePIDs called for unsupported agent kind")
			return nil
		},
		OpenFiles: func([]int) []string {
			t.Fatal("OpenFiles called for unsupported agent kind")
			return nil
		},
	})
	sess := Session{Pane: "w1:p1", Roots: ProcessRoots{PID: 500}, Agent: "agy"}
	if _, err := r.Locate(sess); Reason(err) != ReasonUnsupported {
		t.Fatalf("Locate(agy with pid) = %v, want %s", err, ReasonUnsupported)
	}
	if _, err := r.Title(sess); Reason(err) != ReasonUnsupported {
		t.Fatalf("Title(agy with pid) = %v, want %s", err, ReasonUnsupported)
	}
}

func TestDiscoverConcurrentInvalidateAndForgetRace(t *testing.T) {
	dir := t.TempDir()
	id1 := "01a0c000-0000-7000-8000-000000000301"
	id2 := "01a0c000-0000-7000-8000-000000000302"
	r1 := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T12-00-00-"+id1+".jsonl")
	r2 := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T12-01-00-"+id2+".jsonl")
	writeJSONFile(t, r1, `{"type":"event_msg","payload":{"type":"agent_message","message":"First."}}`+"\n")
	writeJSONFile(t, r2, `{"type":"event_msg","payload":{"type":"agent_message","message":"Second."}}`+"\n")

	// Deterministic stale-probe guard: an OpenFiles call that started before Invalidate
	// must not populate the cache after Invalidate swaps to r2.
	inProbe := make(chan struct{})
	release := make(chan struct{})
	var phase atomic.Int32
	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs:       func(roots ProcessRoots) []int { return roots.Direct() },
		OpenFiles: func([]int) []string {
			if phase.CompareAndSwap(0, 1) {
				close(inProbe)
				<-release
				return []string{r1}
			}
			return []string{r2}
		},
	})

	sess := Session{Pane: "w9:p1", Roots: ProcessRoots{PID: 900}, Agent: "codex"}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = r.Locate(sess)
	}()
	select {
	case <-inProbe:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for probe")
	}
	r.Invalidate("w9:p1")
	close(release)
	wg.Wait()

	got, err := r.Locate(sess)
	if err != nil || got != r2 {
		t.Fatalf("Locate after concurrent Invalidate = %q, %v; want %q (stale probe must not win)", got, err, r2)
	}

	// Stress concurrent Read/Tail/Title/Invalidate/Forget under -race.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch (i + j) % 5 {
				case 0:
					_, _ = r.Read("w9:p1", sess)
				case 1:
					_, _ = r.Tail(sess)
				case 2:
					_, _ = r.Title(sess)
				case 3:
					r.Invalidate("w9:p1")
				case 4:
					r.Forget("w9:p1")
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestDiscoverInvalidateOtherPaneDoesNotDropInFlightProbe(t *testing.T) {
	dir := t.TempDir()
	idA := "01a0c000-0000-7000-8000-000000000401"
	rolloutA := filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T13-00-00-"+idA+".jsonl")
	writeJSONFile(t, rolloutA, `{"type":"event_msg","payload":{"type":"agent_message","message":"Pane A."}}`+"\n")

	inProbe := make(chan struct{})
	release := make(chan struct{})
	var openCalls atomic.Int32
	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs:       func(roots ProcessRoots) []int { return roots.Direct() },
		OpenFiles: func([]int) []string {
			if openCalls.Add(1) == 1 {
				close(inProbe)
				<-release
			}
			return []string{rolloutA}
		},
	})

	sessA := Session{Pane: "w1:p1", Roots: ProcessRoots{PID: 901}, Agent: "codex"}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := r.Resolve(sessA); err != nil {
			t.Errorf("Resolve(pane A): %v", err)
		}
	}()
	select {
	case <-inProbe:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pane A probe")
	}

	// Invalidating or forgetting unrelated panes must not drop pane A's in-flight cache write.
	r.Invalidate("w2:p1")
	r.Forget("w3:p1")
	close(release)
	wg.Wait()

	if disc, ok := r.Discovered("w1:p1"); !ok || disc.ID != idA {
		t.Fatalf("Discovered(w1:p1) = %+v, %v; want %s cached despite other-pane invalidations", disc, ok, idA)
	}
	if d, err := r.Read("w1:p1", sessA); err != nil || d.LastAssistant != "Pane A." {
		t.Fatalf("Read(w1:p1) = %+v, %v", d, err)
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("openCalls = %d, want 1 (pane A probe stayed cached)", got)
	}
}

func TestProcessTableAndLsofParsers(t *testing.T) {
	psOut := []byte("  100     1   100\n  400   100   400\n  401   400   400\n  402   401   400\n  888   100   888\n")
	rows := parsePsTable(psOut)
	got := walkProcessTree(ProcessRoots{PID: 400, Pgrp: 400, ShellPID: 100}, rows)
	// Foreground tree (400, 401, 402) precedes shell 100 and unrelated sibling 888.
	if len(got) < 4 || got[0] != 400 || got[1] != 401 || got[2] != 402 {
		t.Fatalf("walkProcessTree = %v, want [400 401 402 ...] first", got)
	}
	// Linux /proc/<pid>/stat with spaces and parens inside comm name.
	if row, ok := parseProcStat(402, []byte("402 (claude (node)) S 401 400 100 0 -1\n")); !ok || row.ppid != 401 || row.pgid != 400 {
		t.Fatalf("parseProcStat = %+v, %v", row, ok)
	}
	lsofOut := []byte("p501\nf0\nn/dev/ttys001\nf12\nn/Users/example/.codex/sessions/2026/11/04/rollout-2026-11-04T10-00-00-01a0c000-0000-7000-8000-000000000001.jsonl\nf13\nn[pipe]\n")
	files := parseLsofFiles(lsofOut)
	if len(files) != 2 || !strings.HasSuffix(files[1], ".jsonl") {
		t.Fatalf("parseLsofFiles = %v", files)
	}
	pids := parseLsofPIDs([]byte("p501\np502\np501\n"))
	if len(pids) != 2 || pids[0] != 501 || pids[1] != 502 {
		t.Fatalf("parseLsofPIDs = %v", pids)
	}
}

func TestDifferentAgentInSamePaneOverridesPreviousSessionSource(t *testing.T) {
	dir := t.TempDir()
	cwd := "/work/app"
	oldCodexID := "01a0c000-0000-7000-8000-000000000888"
	newClaudeID := "99999999-0000-4000-8000-000000000999"

	writeJSONFile(t, filepath.Join(dir, "codex", "sessions", "2026", "11", "04", "rollout-2026-11-04T09-00-00-"+oldCodexID+".jsonl"),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"Old codex answer."}}`+"\n")
	writeJSONFile(t, filepath.Join(dir, "claude", "projects", ClaudeSlug(cwd), newClaudeID+".jsonl"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"New claude answer in same pane."}]}}`+"\n")
	writeJSONFile(t, filepath.Join(dir, "claude", "sessions", "910.json"), fmt.Sprintf(`{"sessionId":%q}`, newClaudeID))

	r := New(Options{
		ClaudeProjects: filepath.Join(dir, "claude", "projects"),
		CodexSessions:  filepath.Join(dir, "codex", "sessions"),
		TreePIDs:       func(roots ProcessRoots) []int { return roots.Direct() },
		OpenFiles:      func([]int) []string { return nil },
	})

	// Leftover Source/Value from the previous Codex run, while the pane's current Agent is claude.
	sess := Session{
		Pane:   "w1:p1",
		Roots:  ProcessRoots{PID: 910},
		Agent:  "claude",
		Source: "pid:codex",
		Kind:   "id",
		Value:  oldCodexID,
		Cwd:    cwd,
	}
	d, err := r.Read("w1:p1", sess)
	if err != nil || d.LastAssistant != "New claude answer in same pane." {
		t.Fatalf("Read = %+v, %v; want new claude answer", d, err)
	}
	// And if the new Claude process has not written <pid>.json yet, the leftover Codex session id is not reported as Claude's.
	noPIDYet := sess
	noPIDYet.Roots = ProcessRoots{PID: 911}
	if _, err := r.Resolve(noPIDYet); Reason(err) != ReasonNotReported {
		t.Fatalf("Resolve with mismatched leftover Source = %v, want %s", err, ReasonNotReported)
	}
}
