package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// sendsTo is the text of every terminal.send to term.
func sendsTo(t *testing.T, reqs []hctest.Request, term string) []string {
	t.Helper()
	var out []string
	for _, r := range reqs {
		var p struct{ Terminal, Text string }
		if r.Method == "terminal.send" && json.Unmarshal(r.Params, &p) == nil && p.Terminal == term {
			out = append(out, p.Text)
		}
	}
	return out
}

// logRules counts the log.jsonl lines of rule.
func logRules(t *testing.T, path, rule string) []LogLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []LogLine
	for ln := range strings.Lines(string(data)) {
		var l LogLine
		if json.Unmarshal([]byte(ln), &l) == nil && l.Rule == rule {
			out = append(out, l)
		}
	}
	return out
}

// TestJ12HeldWakesOneBatchWhenIdle: held wakes stay held with no
// coordinator run and while it works; once it is idle they go as one
// message with the facts block, are logged once and cleared.
func TestJ12HeldWakesOneBatchWhenIdle(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	eng := New(paths, client)
	srv.Add(hc.Terminal{Address: "coord1", Name: "coord", State: "live", Status: &hc.Status{App: "claude-code", State: "working"}})
	const id = "proj-wake"
	st := flow.State{Seq: 1, Project: flow.Project{ID: id, Folder: t.TempDir(), Started: true, RunID: "gone1"}, Settings: flow.DefaultSettings()}
	st.Settings.WorkerRestarts = 1
	at := time.Now()
	st.Project.Wakes = []flow.Wake{
		{Key: "worker_failed", Task: "T1", Why: "N23: the worker is working", Facts: `title "feat: x"; H c1; PR #7, checks green; last verdict none`, At: at},
		{Key: "no_rule_matched@c2@open@red@clean@same", Task: "T2", Why: "N43: no rule matched", Facts: `title "feat: y"`, At: at},
	}
	save := func() {
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
	}
	held := func(when string) {
		t.Helper()
		eng.passProject(ctx, id, false)
		got, _ := LoadState(paths.State(id))
		if n := len(sendsTo(t, srv.Requests(), "coord1")); n != 0 || len(got.Project.Wakes) != 2 {
			t.Fatalf("%s: %d sends, %d wakes held; want 0 and 2", when, n, len(got.Project.Wakes))
		}
	}
	save()
	held("no coordinator run")
	st.Project.RunID = "coord1"
	save()
	held("coordinator working")

	setIdle(t, srv, client, "coord1", "idle")
	for range 3 {
		eng.passProject(ctx, id, false)
	}
	sends := sendsTo(t, srv.Requests(), "coord1")
	if len(sends) != 1 {
		t.Fatalf("sends = %q, want one batch", sends)
	}
	for _, want := range []string{"Wake: 2", "T1: N23:", "worker_failed", `title "feat: x"; H c1; PR #7, checks green`, "T2: N43", "differ from their default: worker_restarts 1."} {
		if !strings.Contains(sends[0], want) {
			t.Errorf("wake message missing %q:\n%s", want, sends[0])
		}
	}
	if strings.Contains(sends[0], "checks_grace_minutes") || strings.Contains(sends[0], "review_rounds") {
		t.Errorf("wake message lists default settings:\n%s", sends[0])
	}
	if got, _ := LoadState(paths.State(id)); len(got.Project.Wakes) != 0 {
		t.Errorf("wakes not cleared: %+v", got.Project.Wakes)
	}
	if l := logRules(t, paths.Log(id), "J12"); len(l) != 1 || len(l[0].Asked) != 2 {
		t.Errorf("J12 log lines = %+v, want one listing both keys", l)
	}
}

// TestReviewerRetriesFailureWakesOnce: a reviewer that keeps ending with no
// valid report is retried while tries < reviewer_retries, then counts as a
// failure and wakes the coordinator once via J12.
func TestReviewerRetriesFailureWakesOnce(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	if err := eng.Start(repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // J7: the coordinator starts, working
	st, _ := LoadState(paths.State(id))
	coord := st.Project.RunID
	st.Settings.ReviewerRetries = 1
	st.Tasks = []flow.Task{{ID: "T1", Title: "feat: x", EstimateLines: 50}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23
	st, _ = LoadState(paths.State(id))
	wt := st.Tasks[0].Attempts[0].Worktree
	if err := os.WriteFile(filepath.Join(wt, "x.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "x.go")
	gitIn(t, wt, "commit", "-m", "feat: c1")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, gh, "pr.json", c1, "[]")
	st.Tasks[0].Attempts[0].Ready = []flow.Ready{{Head: c1}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N26
	eng.passProject(ctx, id, false) // N29 starts reviewer (try 0)

	// Reviewer 0 exits with no report -> 4.8 starts retry 1 (no wake yet).
	st, _ = LoadState(paths.State(id))
	srv.Finish(st.Tasks[0].Attempts[0].Review.RunID, "exited")
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].Review.Retries != 1 || st.Tasks[0].Attempts[0].Review.RunID == "" || len(st.Project.Wakes) != 0 {
		t.Fatalf("after first reviewer exit: review=%+v wakes=%+v", st.Tasks[0].Attempts[0].Review, st.Project.Wakes)
	}

	// Retry 1 also exits with no report -> tries == reviewer_retries (1), wakes once (held while coord works).
	srv.Finish(st.Tasks[0].Attempts[0].Review.RunID, "exited")
	for range 3 {
		eng.passProject(ctx, id, false)
	}
	st, _ = LoadState(paths.State(id))
	wantKey := "reviewer_failed@1@" + c1
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != wantKey || len(sendsTo(t, srv.Requests(), coord)) != 0 {
		t.Fatalf("held wakes = %+v, want one %s and no send", st.Project.Wakes, wantKey)
	}

	setIdle(t, srv, client, coord, "idle")
	for range 3 {
		eng.passProject(ctx, id, false)
	}
	sends := sendsTo(t, srv.Requests(), coord)
	if len(sends) != 1 || !strings.Contains(sends[0], "T1: N29:") || !strings.Contains(sends[0], "reviewer_retries 1") || !strings.Contains(sends[0], "H "+c1) {
		t.Fatalf("coordinator sends = %q, want one wake with the facts", sends)
	}
}

// sendFailProxy forwards hc calls to the fake server at target, one call
// per connection, but answers terminal.send with an error while fail is set.
func sendFailProxy(t *testing.T, target string, fail *atomic.Bool) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdp") // a socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.Unmarshal(line, &req)
				if req.Method == "terminal.send" && fail.Load() {
					_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"send failed"}}`+"\n", req.ID)
					return
				}
				up, err := net.Dial("unix", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				if _, err := up.Write(line); err == nil {
					_, _ = io.Copy(c, up)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// TestJ12DeliveryAtLeastOnce: a failed send keeps the wakes held; a send
// that went through but whose commit failed keeps them too, so the next
// pass sends them again (a duplicate, never a lost wake).
func TestJ12DeliveryAtLeastOnce(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	var fail atomic.Bool
	fail.Store(true)
	paths := testPaths(t)
	eng := New(paths, hc.NewClient(sendFailProxy(t, srv.Path, &fail)))
	pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
	srv.Add(hc.Terminal{Address: "coord1", Name: "coord", State: "live", Status: &hc.Status{App: "claude-code", State: "idle"}, StatusMS: &pastMS})
	const id = "proj-once"
	st := flow.State{Seq: 1, Project: flow.Project{ID: id, Folder: t.TempDir(), Started: true, RunID: "coord1"}, Settings: flow.DefaultSettings()}
	st.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23: worker failed", Facts: `title "feat: x"`, At: time.Now()}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	check := func(when string, sends, held int) {
		t.Helper()
		got, _ := LoadState(paths.State(id))
		if n := len(sendsTo(t, srv.Requests(), "coord1")); n != sends || len(got.Project.Wakes) != held {
			t.Fatalf("%s: %d sends, %d wakes held; want %d and %d", when, n, len(got.Project.Wakes), sends, held)
		}
	}
	eng.passProject(ctx, id, false)
	check("send failed", 0, 1)

	fail.Store(false)
	if err := os.WriteFile(paths.Log(id), nil, 0o400); err != nil { // the commit's log append fails
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	check("sent, commit failed", 1, 1)

	if err := os.Chmod(paths.Log(id), 0o600); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	check("sent again, committed", 2, 0)
	if l := logRules(t, paths.Log(id), "J12"); len(l) != 1 {
		t.Fatalf("J12 lines = %+v, want 1", l)
	}
}

// TestWakeMessageBounded: titles are cut to 120 characters and at most 20
// wakes are listed, then how many more.
func TestWakeMessageBounded(t *testing.T) {
	long := strings.Repeat("é", 200)
	if f := taskFacts(flow.Task{Title: long}, flow.Facts{}, time.Time{}); !strings.Contains(f, strconv.Quote(strings.Repeat("é", 120)+"…")) {
		t.Fatalf("title not cut to 120 characters: %s", f)
	}
	var s flow.State
	s.Settings = flow.DefaultSettings()
	for i := range 25 {
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: fmt.Sprintf("k%d", i), Task: fmt.Sprintf("T%d", i)})
	}
	msg := wakeMessage(s)
	if n := strings.Count(msg, "\n- T"); n != 20 || !strings.Contains(msg, "and 5 more") || !strings.HasPrefix(msg, "Wake: 25 ") {
		t.Fatalf("listed %d wakes, want 20 then \"and 5 more\":\n%s", n, msg)
	}
}

// TestN25N6RecapOpensOnceWakesCoordinatorAndSplits runs the full non-convergence
// path in Engine: 3 changes rounds -> N25 opens recap once -> N6 wakes
// coordinator once with allowed choices in facts (no reviewer starts, worker is
// not told findings) -> coordinator splits -> N1 closes PR with note and keeps
// branch -> split child starts as a normal task.
func TestN25N6RecapOpensOnceWakesCoordinatorAndSplits(t *testing.T) {
	l := newLanding(t)
	pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
	l.srv.Add(hc.Terminal{Address: "coord1", Name: "coord", State: "live", Status: &hc.Status{App: "claude-code", State: "idle"}, StatusMS: &pastMS})
	l.edit(func(s *flow.State) {
		s.Project.RunID = "coord1"
		s.Project.RunTokenHash = tokenHash("tok-coord")
		s.Tasks[0].Attempts[0].Rounds = []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundDelta, Head: l.c1, Verdict: flow.VerdictChanges, Majors: 1},
		}
	})

	l.pass() // N25 opens recap
	l.pass() // N6 queues wake, J12 delivers it to idle coord1
	l.pass() // N6 waits: no second wake, no reviewer, no tell_worker_findings

	st := l.state()
	if len(st.Tasks[0].Recaps) != 1 || st.Tasks[0].Recaps[0].Trigger != "review" ||
		strings.Join(st.Tasks[0].Recaps[0].Allowed, ",") != "split,new,drop" {
		t.Fatalf("recaps = %+v", st.Tasks[0].Recaps)
	}
	coordSends := sendsTo(t, l.srv.Requests(), "coord1")
	if len(coordSends) != 1 || !strings.Contains(coordSends[0], "allowed [split, new, drop]") || !strings.Contains(coordSends[0], "T1: N6:") {
		t.Fatalf("coordinator wakes = %q, want one wake with allowed [split, new, drop]", coordSends)
	}
	if workerSends := sendsTo(t, l.srv.Requests(), l.worker); len(workerSends) != 0 {
		t.Fatalf("worker was told findings during recap: %q", workerSends)
	}
	if st.Tasks[0].Attempts[0].Review != nil {
		t.Fatalf("reviewer started during recap: %+v", st.Tasks[0].Attempts[0].Review)
	}

	// Coordinator splits T1 into T1a.
	if err := l.eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"split","why":"smaller scope","children":[{"id":"T1a","estimate_lines":50,"title":"feat: part a",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatalf("recap decide split: %v", err)
	}
	l.pass() // T1 -> N1 cleanup (closes PR #7, keeps branch); T1a -> N11 new attempt
	st = l.state()
	if len(st.Tasks) != 2 || len(st.Tasks[1].Attempts) != 1 || st.Tasks[1].SplitFrom != "T1" {
		t.Fatalf("split child did not start as normal task: %+v", st.Tasks)
	}
	if closes := ghCalls(t, l.gh, "pr close"); len(closes) != 1 || !strings.Contains(closes[0], "split into T1a") {
		t.Fatalf("pr close calls = %q", closes)
	}
	if l.remoteBranch() == "" {
		t.Fatal("parent remote branch deleted on split")
	}
}
