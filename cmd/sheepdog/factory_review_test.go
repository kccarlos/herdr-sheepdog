package main

// TDD step 1: failing CLI tests for `sheepdog factory report review`
// (design.md 7.2 RV1–RV8, spike S-REV-2: --findings <path>, never stdin).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

const cliHostileFindings = "" +
	"Line 01: backticks `date` and $(uname -a)\n" +
	"Line 02: env $HOME ${USER} 'single' \"double\" \\backslash\n" +
	"major: internal/factory/report.go:50 — missing validation; `go test` failed\n" +
	"Line 04: echo pwned > /tmp/srev2-cli-should-not-exist\n" +
	"Line 05: trailing newline preserved\n"

// TestFactoryReportReviewCLI checks CLI flags, --findings <path> file reading,
// 64 KiB cap, rejection of --findings - (stdin retired by S-REV-2), usage
// errors (exit 2), daemon refusals (exit 1), and unreachable daemon errors.
func TestFactoryReportReviewCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-rev-cli")

	var (
		called   int
		lastBody struct {
			Token string         `json:"token"`
			Verb  string         `json:"verb"`
			Args  map[string]any `json:"args"`
		}
		replyStatus = http.StatusOK
		replyJSON   = `{"ok":true}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		lastBody.Token, lastBody.Verb, lastBody.Args = "", "", nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		w.WriteHeader(replyStatus)
		_, _ = w.Write([]byte(replyJSON))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)

	t.Run("valid pass without --findings", func(t *testing.T) {
		called = 0
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{
			"factory", "report", "review", "pass",
			"--code", "abc1234567", "--head", "deadbeef01",
			"--self-check-misses", "0", "--deletable", "none",
		}, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errBuf.String())
		}
		if strings.TrimSpace(out.String()) != "review pass" {
			t.Errorf("stdout = %q, want %q", out.String(), "review pass")
		}
		if called != 1 || lastBody.Token != "tok-rev-cli" || lastBody.Verb != "review" {
			t.Fatalf("posted called=%d body=%+v", called, lastBody)
		}
		if lastBody.Args["verdict"] != "pass" || lastBody.Args["code"] != "abc1234567" ||
			lastBody.Args["head"] != "deadbeef01" || lastBody.Args["deletable"] != "none" ||
			lastBody.Args["self_check_misses"] != float64(0) {
			t.Fatalf("args = %+v", lastBody.Args)
		}
		if f, _ := lastBody.Args["findings"].(string); f != "" {
			t.Errorf("findings = %q, want empty", f)
		}
	})

	t.Run("valid changes with hostile --findings file byte-exact", func(t *testing.T) {
		called = 0
		findingsFile := filepath.Join(t.TempDir(), "T1-r1.draft.md")
		if err := os.WriteFile(findingsFile, []byte(cliHostileFindings), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{
			"factory", "report", "review", "changes",
			"--code", "abc1234567", "--head", "deadbeef01",
			"--self-check-misses", "2", "--deletable", "helper.go:10",
			"--class", "missing check", "--not-run", "e2e",
			"--findings", findingsFile,
		}, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errBuf.String())
		}
		if strings.TrimSpace(out.String()) != "review changes" {
			t.Errorf("stdout = %q, want %q", out.String(), "review changes")
		}
		if called != 1 || lastBody.Verb != "review" {
			t.Fatalf("posted called=%d body=%+v", called, lastBody)
		}
		if got, _ := lastBody.Args["findings"].(string); got != cliHostileFindings {
			t.Fatalf("findings mismatch:\ngot:  %q\nwant: %q", got, cliHostileFindings)
		}
		if lastBody.Args["class"] != "missing check" || lastBody.Args["not_run"] != "e2e" {
			t.Errorf("optional fields = %+v", lastBody.Args)
		}
	})

	t.Run("usage errors exit 2 without calling daemon", func(t *testing.T) {
		cases := []struct {
			name string
			args []string
		}{
			{"missing verdict", []string{"factory", "report", "review"}},
			{"invalid verdict", []string{"factory", "report", "review", "approve", "--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none"}},
			{"extra positional arg", []string{"factory", "report", "review", "pass", "extra", "--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none"}},
			{"missing --code", []string{"factory", "report", "review", "pass", "--head", "h", "--self-check-misses", "0", "--deletable", "none"}},
			{"missing --head", []string{"factory", "report", "review", "pass", "--code", "c", "--self-check-misses", "0", "--deletable", "none"}},
			{"missing --self-check-misses", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--deletable", "none"}},
			{"missing --deletable", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--self-check-misses", "0"}},
			{"non-integer --self-check-misses", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--self-check-misses", "abc", "--deletable", "none"}},
			{"negative --self-check-misses", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--self-check-misses", "-1", "--deletable", "none"}},
			{"stdin --findings - rejected (S-REV-2)", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none", "--findings", "-"}},
			{"removed --majors flag", []string{"factory", "report", "review", "pass", "--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none", "--majors", "0"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				called = 0
				var out, errBuf bytes.Buffer
				code := run(context.Background(), tc.args, &out, &errBuf)
				if code != 2 || called != 0 {
					t.Fatalf("code=%d called=%d stderr=%s, want exit 2 and 0 HTTP calls", code, called, errBuf.String())
				}
			})
		}
	})

	t.Run("findings file missing or over 64 KiB exits 1 without calling daemon", func(t *testing.T) {
		bigFile := filepath.Join(t.TempDir(), "big.md")
		if err := os.WriteFile(bigFile, bytes.Repeat([]byte("a"), 64*1024+1), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, path string
		}{
			{"missing file", filepath.Join(t.TempDir(), "nope.md")},
			{"over 64 KiB", bigFile},
		} {
			t.Run(tc.name, func(t *testing.T) {
				called = 0
				var out, errBuf bytes.Buffer
				code := run(context.Background(), []string{
					"factory", "report", "review", "changes",
					"--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none",
					"--findings", tc.path,
				}, &out, &errBuf)
				if code != 1 || called != 0 {
					t.Fatalf("code=%d called=%d stderr=%s, want exit 1 and 0 HTTP calls", code, called, errBuf.String())
				}
			})
		}
	})

	t.Run("daemon refusal prints code and sentence and exits 1", func(t *testing.T) {
		called = 0
		replyStatus = http.StatusBadRequest
		replyJSON = `{"error":"invalid_report: pass requires 0 major: lines"}`
		t.Cleanup(func() {
			replyStatus = http.StatusOK
			replyJSON = `{"ok":true}`
		})
		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{
			"factory", "report", "review", "pass",
			"--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none",
		}, &out, &errBuf)
		if code != 1 || !strings.Contains(errBuf.String(), "invalid_report") {
			t.Fatalf("code=%d stderr=%q, want exit 1 with invalid_report", code, errBuf.String())
		}
	})

	t.Run("daemon unreachable exits 1 naming 127.0.0.1:<port>", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, closedPort, _ := net.SplitHostPort(ln.Addr().String())
		_ = ln.Close()
		t.Setenv("SHEEPDOG_WEB_PORT", closedPort)

		var out, errBuf bytes.Buffer
		code := run(context.Background(), []string{
			"factory", "report", "review", "pass",
			"--code", "c", "--head", "h", "--self-check-misses", "0", "--deletable", "none",
		}, &out, &errBuf)
		if code != 1 || !strings.Contains(errBuf.String(), "127.0.0.1:"+closedPort) {
			t.Fatalf("code=%d stderr=%q, want exit 1 naming 127.0.0.1:%s", code, errBuf.String(), closedPort)
		}
	})
}

// TestFactoryReportReviewEndToEndByteExactStore wires `sheepdog factory report review`
// through the real web server and factory.Engine and checks that the findings
// file is read by the CLI and stored byte-exact under projects/<id>/reports/.
func TestFactoryReportReviewEndToEndByteExactStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	repo := filepath.Join(home, "repo")
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun("init")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "a.go")
	gitRun("commit", "-m", "feat: init")
	c1 := gitRun("rev-parse", "HEAD")

	hcSrv := hctest.New(t)
	hcSrv.OnExec = hctest.GitExec

	dataDir := filepath.Join(home, "data")
	paths := factory.Paths{DataDir: dataDir}
	eng := factory.New(paths, hc.NewClient(hcSrv.Path))

	const (
		projID = "proj-e2e"
		code   = "e2ecode123"
		revTok = "tok-rev-e2e"
	)
	// Seed state with a live reviewer run whose token hash matches revTok.
	// Use a quick sha256 via factory state (matching internal tokenHash).
	if err := factory.SaveState(paths.State(projID), flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: projID, Folder: repo, Started: true},
		Tasks: []flow.Task{{
			ID:            "T1",
			Title:         "feat: e2e review",
			EstimateLines: 50,
			Attempts: []flow.Attempt{{
				BaseCommit: c1,
				Branch:     "task/t1",
				Worktree:   repo,
				SizeCap:    100,
				Ready:      []flow.Ready{{Head: c1}},
				Review: &flow.ReviewRun{
					RunID:        "term-rev-e2e",
					RunTokenHash: sha256HexForTest(revTok),
					Run:          &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", Model: "opus", State: flow.PhaseWorking},
					Round:        1,
					Kind:         flow.RoundFull,
					Head:         c1,
					Code:         code,
					Reviewer:     flow.Reviewer{CLI: "claude", Model: "opus", Effort: "high"},
				},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	webDir := t.TempDir()
	elog, err := eventlog.Open(webDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = elog.Close() })
	st, err := state.Open(filepath.Join(webDir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tlsID, err := web.LoadTLSIdentity("", nil)
	if err != nil {
		t.Fatal(err)
	}
	webSrv, err := web.New(web.Deps{Log: elog, Store: st, DataDir: webDir, TLS: tlsID, Factory: eng})
	if err != nil {
		t.Fatal(err)
	}
	if err := webSrv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvDone := make(chan error, 1)
	go func() { srvDone <- webSrv.Serve(srvCtx, 100*time.Millisecond) }()
	t.Cleanup(func() {
		srvCancel()
		select {
		case <-srvDone:
		case <-time.After(5 * time.Second):
		}
	})

	_, port, _ := net.SplitHostPort(webSrv.Addr())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	t.Setenv("SHEEPDOG_RUN_TOKEN", revTok)

	scratchFile := filepath.Join(t.TempDir(), "T1-r1.draft.md")
	if err := os.WriteFile(scratchFile, []byte(cliHostileFindings), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	exitCode := run(context.Background(), []string{
		"factory", "report", "review", "changes",
		"--code", code, "--head", c1,
		"--self-check-misses", "1", "--deletable", "none",
		"--findings", scratchFile,
	}, &out, &errBuf)
	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, out.String(), errBuf.String())
	}
	got, err := os.ReadFile(paths.Report(projID, "T1", 1))
	if err != nil {
		t.Fatalf("read stored report: %v", err)
	}
	if string(got) != cliHostileFindings {
		t.Fatalf("stored report mismatch:\ngot:  %q\nwant: %q", string(got), cliHostileFindings)
	}
}

func sha256HexForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
