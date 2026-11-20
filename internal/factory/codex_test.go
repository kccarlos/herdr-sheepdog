package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// TestReviewerCandidatesBySetting: round 1 is opus either way; the second
// passing round is Codex only when second_reviewer = "codex".
func TestReviewerCandidatesBySetting(t *testing.T) {
	def, cdx := flow.DefaultSettings(), flow.DefaultSettings()
	cdx.SecondReviewer = "codex"
	for _, tc := range []struct {
		st     flow.Settings
		passed []string
		want   string
	}{
		{def, nil, "opus"},
		{def, []string{"opus"}, "opus"},
		{cdx, nil, "opus"},
		{cdx, []string{"opus"}, codexModel},
		{cdx, []string{"opus", codexModel}, "opus"},
	} {
		if got := nextReviewerModel(workerModel, tc.passed, reviewerCandidates(tc.st)); got != tc.want {
			t.Errorf("%s after %v = %q, want %q", tc.st.SecondReviewer, tc.passed, got, tc.want)
		}
	}
	if r := reviewerOf(codexModel); r.CLI != world.CodexCLI || reviewerOf("opus").CLI != "claude" {
		t.Errorf("reviewerOf CLI wrong: %+v", r)
	}
	if cdx.Validate() != nil || def.Validate() != nil {
		t.Error("opus and codex must validate")
	}
	bad := flow.DefaultSettings()
	bad.SecondReviewer = "haiku"
	if bad.Validate() == nil {
		t.Error("second_reviewer haiku validated")
	}
	if got := cdx.Changed(); !slices.Equal(got, []string{"second_reviewer codex"}) {
		t.Errorf("Changed = %q", got)
	}
}

// codexSetup brings a task to just before round 2 with second_reviewer =
// "codex", round 1 passed by opus at c1, and a fake Codex sign-in.
func codexSetup(t *testing.T) (*hctest.Server, *Engine, Paths, string, string, string, *[]hc.RunParams, *bytes.Buffer) {
	ctx := context.Background()
	codexHome := t.TempDir() // a fake sign-in: tests never touch the real ~/.codex
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	srv := hctest.New(t)
	runs := &[]hc.RunParams{}
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { *runs = append(*runs, p) }
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	eng := New(paths, hc.NewClient(srv.Path))
	logs := &bytes.Buffer{}
	eng.Log = logging.NewJSONLLogger(logs)
	if err := eng.Start(repo, "Goal"); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // J7
	st, _ := LoadState(paths.State(id))
	st.Settings.StuckMinutes, st.Settings.SecondReviewer = flow.StuckMin, "codex"
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
	a := &st.Tasks[0].Attempts[0]
	a.Ready = []flow.Ready{{Head: c1}}
	a.Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Reviewer: flow.Reviewer{CLI: "claude", Model: "opus", Effort: "medium"}, Verdict: flow.VerdictPass}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	return srv, eng, paths, id, c1, wt, runs, logs
}

// codexRound2 brings a task to round 2 with second_reviewer = "codex" and
// round 1 passed by opus at c1. The Codex terminal never reports a status.
func codexRound2(t *testing.T) (*hctest.Server, *Engine, Paths, string, string) {
	ctx := context.Background()
	srv, eng, paths, id, c1, wt, runs, _ := codexSetup(t)
	eng.passProject(ctx, id, false) // N26
	eng.passProject(ctx, id, false) // N29 starts round 2 on Codex
	st, _ := LoadState(paths.State(id))
	rev := st.Tasks[0].Attempts[0].Review
	last := (*runs)[len(*runs)-1]
	if rev == nil || rev.Round != 2 || rev.Reviewer.CLI != world.CodexCLI || rev.Reviewer.Model != codexModel ||
		!slices.Equal(last.Argv[:len(last.Argv)-1], world.CodexArgv(codexModel, wt)) || !strings.Contains(last.Argv[len(last.Argv)-1], "Review round 2") {
		t.Fatalf("round 2 review = %+v argv %q", rev, last.Argv)
	}
	if home := filepath.Join(paths.DataDir, "factory", "codex-home"); last.Env["CODEX_HOME"] != home {
		t.Fatalf("CODEX_HOME = %q, want %q", last.Env["CODEX_HOME"], home)
	}
	if rev.IdleSince == nil { // S2's clock runs from the start: Codex reports no idle
		t.Fatal("no S2 clock at the Codex run's start")
	}
	return srv, eng, paths, id, c1
}

func codexScreen(code, head, reply string) string {
	return "› You are the factory code reviewer for task T1: feat: x\n" +
		`  <report>{"code":"` + code + `","round":2,"head":"` + head + `","verdict":"pass|changes","majors":<n>}</report>` + "\n\n" + reply
}

// TestEngineCodexReviewerTagIsTheVerdict: a Codex run with no hc status is
// read every pass; the echo alone is not a verdict, the reply's tag is.
func TestEngineCodexReviewerTagIsTheVerdict(t *testing.T) {
	ctx := context.Background()
	srv, eng, paths, id, c1 := codexRound2(t)
	st, _ := LoadState(paths.State(id))
	rev := st.Tasks[0].Attempts[0].Review
	srv.SetScreen(rev.RunID, codexScreen(rev.Code, c1, "• Working (3s • esc to interrupt)\n"))
	eng.passProject(ctx, id, false)
	if st, _ = LoadState(paths.State(id)); len(st.Tasks[0].Attempts[0].Rounds) != 1 || st.Tasks[0].Attempts[0].Review == nil {
		t.Fatalf("echo read as a verdict: %+v", st.Tasks[0].Attempts[0])
	}
	srv.SetScreen(rev.RunID, codexScreen(rev.Code, c1, "• Clean change.\n\n  <report>{\"code\":\""+rev.Code+"\",\"round\":2,\"head\":\""+c1+
		"\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n  Worked for 9s • 8:44 PM\n\n› Ask Codex to do anything\n"))
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	a := st.Tasks[0].Attempts[0]
	if a.Review != nil || len(a.Rounds) != 2 || a.Rounds[1].Verdict != flow.VerdictPass || a.Rounds[1].Reviewer.CLI != world.CodexCLI {
		t.Fatalf("after the Codex tag: review=%+v rounds=%+v", a.Review, a.Rounds)
	}
	if b, err := os.ReadFile(a.Rounds[1].ReportPath); err != nil || !strings.Contains(string(b), "• Clean change.") {
		t.Fatalf("round 2 report = %q, %v", b, err)
	}
	if flow.Ladder(flow.In{Task: st.Tasks[0], Settings: st.Settings}) != flow.Passed {
		t.Fatal("opus pass + Codex pass is not passed")
	}
}

// TestEngineCodexReviewerChangesCounted: Codex prints findings as "• "
// bullets; a "changes" tag with two "• major:" lines is round 2's changes.
func TestEngineCodexReviewerChangesCounted(t *testing.T) {
	ctx := context.Background()
	srv, eng, paths, id, c1 := codexRound2(t)
	st, _ := LoadState(paths.State(id))
	rev := st.Tasks[0].Attempts[0].Review
	srv.SetScreen(rev.RunID, codexScreen(rev.Code, c1, "• I’ll read the diff and check the change.\n\n• Explored\n  └ Read x.go\n\n"+
		"• major: add() subtracts its second argument.\n\n• major: no test covers add().\n\n• minor: name the helper.\n\n"+
		"  <report>{\"code\":\""+rev.Code+"\",\"round\":2,\"head\":\""+c1+"\",\"verdict\":\"changes\",\"majors\":2}</report>\n\n"+
		"  Worked for 11s • 7:46 PM\n\n› Ask Codex to do anything\n"))
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	a := st.Tasks[0].Attempts[0]
	if a.Review != nil || len(a.Rounds) != 2 || a.Rounds[1].Verdict != flow.VerdictChanges || a.Rounds[1].Majors != 2 {
		t.Fatalf("after the Codex changes tag: review=%+v rounds=%+v", a.Review, a.Rounds)
	}
}

// TestEngineCodexAuthDivergedFallsBackToOpus: a regular file at the scoped
// home's auth.json (Codex replaced our link on a token refresh) is never
// touched; round 2 starts on opus and one event tells the owner.
func TestEngineCodexAuthDivergedFallsBackToOpus(t *testing.T) {
	ctx := context.Background()
	_, eng, paths, id, _, _, runs, logs := codexSetup(t)
	home := filepath.Join(paths.DataDir, "factory", "codex-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N26
	eng.passProject(ctx, id, false) // N29 starts round 2, on opus
	st, _ := LoadState(paths.State(id))
	rev, last := st.Tasks[0].Attempts[0].Review, (*runs)[len(*runs)-1]
	if rev == nil || rev.Round != 2 || rev.Reviewer.CLI != "claude" || rev.Reviewer.Model != "opus" ||
		!slices.Contains(last.Argv, "opus") || last.Env["CODEX_HOME"] != "" || rev.IdleSince != nil {
		t.Fatalf("round 2 review = %+v argv %q env %v", rev, last.Argv, last.Env["CODEX_HOME"])
	}
	if b, _ := os.ReadFile(filepath.Join(home, "auth.json")); string(b) != "rotated" {
		t.Fatalf("scoped auth.json changed: %q", b)
	}
	if n := strings.Count(logs.String(), "factory.codex_auth_diverged"); n != 1 || strings.Contains(logs.String(), "rotated") {
		t.Fatalf("diverged events = %d in %s", n, logs.String())
	}
}

// TestEngineCodexReviewerNoTagIsStuck: a Codex run that never prints its
// tag starts S2's clock on the first read and wakes after stuck_minutes.
func TestEngineCodexReviewerNoTagIsStuck(t *testing.T) {
	ctx := context.Background()
	srv, eng, paths, id, c1 := codexRound2(t)
	st, _ := LoadState(paths.State(id))
	rev := st.Tasks[0].Attempts[0].Review
	srv.SetScreen(rev.RunID, codexScreen(rev.Code, c1, "• Still thinking.\n"))
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if st.Tasks[0].Attempts[0].Review.IdleSince == nil || len(st.Project.Wakes) != 0 {
		t.Fatalf("no S2 clock on the first no-tag read: %+v", st.Tasks[0].Attempts[0].Review)
	}
	past := time.Now().Add(-time.Duration(flow.StuckMin)*time.Minute - time.Second)
	st.Tasks[0].Attempts[0].Review.IdleSince = &past
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	want := "stuck@reviewer_idle@2@" + c1
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != want {
		t.Fatalf("wakes = %+v, want %s", st.Project.Wakes, want)
	}
}

// TestEngineCodexReviewerAdoptedHasClock: a live Codex reviewer adopted after
// a restart (no review record) gets S2's clock like a fresh start.
func TestEngineCodexReviewerAdoptedHasClock(t *testing.T) {
	ctx := context.Background()
	_, eng, paths, id, _ := codexRound2(t)
	st, _ := LoadState(paths.State(id))
	run := st.Tasks[0].Attempts[0].Review.RunID
	a := &st.Tasks[0].Attempts[0]
	a.Review = nil // a crash before the record and its start act were committed; the terminal lives on
	forgetStarts(a, false)
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	rev := st.Tasks[0].Attempts[0].Review
	if rev == nil || rev.RunID != run || rev.Reviewer.CLI != world.CodexCLI || rev.IdleSince == nil {
		t.Fatalf("adopted review = %+v, want run %s with an S2 clock", rev, run)
	}
}
