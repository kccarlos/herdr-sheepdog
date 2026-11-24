package main

// PR 12.2, TDD step 1: failing CLI tests for `sheepdog factory report retro`
// (plan.md 12.2, design.md 7.2, 9.3, prompts.md 5.5, KB-RET-15).

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFactoryReportRetroCLI checks CLI flags, cause and change-kind validation,
// --findings <path> file reading (byte-exact with real hostile capture), 64 KiB
// cap, rejection of --findings - (S-REV-2), usage errors (exit 2), daemon
// refusals (exit 1), and unreachable daemon errors.
func TestFactoryReportRetroCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-retro-cli")

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

	baseFlags := func(cause string, extra ...string) []string {
		return append([]string{
			"--code", "abc1234567",
			"--head", "deadbeef01",
			"--cause", cause,
			"--worker-change", "add table test covering negative inputs",
			"--worker-change-kind", "check",
			"--coordinator-change", "name edge cases in task outcomes",
			"--coordinator-change-kind", "brief",
		}, extra...)
	}

	t.Run("valid ship without --findings", func(t *testing.T) {
		called = 0
		var out, errBuf bytes.Buffer
		args := append([]string{"factory", "report", "retro", "ship"}, baseFlags("none")...)
		code := run(context.Background(), args, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errBuf.String())
		}
		if strings.TrimSpace(out.String()) != "retro ship" {
			t.Errorf("stdout = %q, want %q", out.String(), "retro ship")
		}
		if called != 1 || lastBody.Token != "tok-retro-cli" || lastBody.Verb != "retro" {
			t.Fatalf("posted called=%d body=%+v", called, lastBody)
		}
		if lastBody.Args["verdict"] != "ship" ||
			lastBody.Args["code"] != "abc1234567" ||
			lastBody.Args["head"] != "deadbeef01" ||
			lastBody.Args["cause"] != "none" ||
			lastBody.Args["worker_change"] != "add table test covering negative inputs" ||
			lastBody.Args["worker_change_kind"] != "check" ||
			lastBody.Args["coordinator_change"] != "name edge cases in task outcomes" ||
			lastBody.Args["coordinator_change_kind"] != "brief" {
			t.Fatalf("args = %+v", lastBody.Args)
		}
		if f, _ := lastBody.Args["findings"].(string); f != "" {
			t.Errorf("findings = %q, want empty", f)
		}
	})

	t.Run("valid fix with hostile --findings file byte-exact", func(t *testing.T) {
		called = 0
		findingsFile := filepath.Join(t.TempDir(), "T1-r4.draft.md")
		if err := os.WriteFile(findingsFile, []byte(cliHostileFindings), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errBuf bytes.Buffer
		args := append([]string{"factory", "report", "retro", "fix"}, baseFlags("patch-shaped-fix", "--findings", findingsFile)...)
		code := run(context.Background(), args, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errBuf.String())
		}
		if strings.TrimSpace(out.String()) != "retro fix" {
			t.Errorf("stdout = %q, want %q", out.String(), "retro fix")
		}
		if called != 1 || lastBody.Verb != "retro" {
			t.Fatalf("posted called=%d body=%+v", called, lastBody)
		}
		if got, _ := lastBody.Args["findings"].(string); got != cliHostileFindings {
			t.Fatalf("findings mismatch:\ngot:  %q\nwant: %q", got, cliHostileFindings)
		}
		if lastBody.Args["verdict"] != "fix" || lastBody.Args["cause"] != "patch-shaped-fix" {
			t.Errorf("args = %+v", lastBody.Args)
		}
	})

	t.Run("valid redesign with flags before verdict", func(t *testing.T) {
		called = 0
		findingsFile := filepath.Join(t.TempDir(), "T1-r4-redesign.draft.md")
		const notes = "second_cause: wrong-seam\nreplan: split parser from runner\n"
		if err := os.WriteFile(findingsFile, []byte(notes), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errBuf bytes.Buffer
		args := append([]string{"factory", "report", "retro"}, baseFlags("wrong-seam", "--findings", findingsFile)...)
		args = append(args, "redesign")
		code := run(context.Background(), args, &out, &errBuf)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errBuf.String())
		}
		if strings.TrimSpace(out.String()) != "retro redesign" {
			t.Errorf("stdout = %q, want %q", out.String(), "retro redesign")
		}
		if called != 1 || lastBody.Verb != "retro" || lastBody.Args["verdict"] != "redesign" {
			t.Fatalf("posted called=%d body=%+v", called, lastBody)
		}
		if got, _ := lastBody.Args["findings"].(string); got != notes {
			t.Errorf("findings = %q, want %q", got, notes)
		}
	})

	t.Run("usage errors exit 2 without calling daemon", func(t *testing.T) {
		cases := []struct {
			name string
			args []string
		}{
			{"missing verdict", []string{"factory", "report", "retro"}},
			{"invalid verdict pass", append([]string{"factory", "report", "retro", "pass"}, baseFlags("none")...)},
			{"invalid verdict changes", append([]string{"factory", "report", "retro", "changes"}, baseFlags("patch-shaped-fix")...)},
			{"extra positional arg", append([]string{"factory", "report", "retro", "ship", "extra"}, baseFlags("none")...)},
			{"missing --code", []string{
				"factory", "report", "retro", "ship",
				"--head", "h", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"missing --head", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"missing --cause", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"unknown --cause", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "not-a-cause",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"missing --worker-change", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"missing --worker-change-kind", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change", "w",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"bad --worker-change-kind", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "prompt",
				"--coordinator-change", "c", "--coordinator-change-kind", "brief",
			}},
			{"missing --coordinator-change", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change-kind", "brief",
			}},
			{"missing --coordinator-change-kind", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c",
			}},
			{"bad --coordinator-change-kind", []string{
				"factory", "report", "retro", "ship",
				"--code", "c", "--head", "h", "--cause", "none",
				"--worker-change", "w", "--worker-change-kind", "check",
				"--coordinator-change", "c", "--coordinator-change-kind", "rule",
			}},
			{"stdin --findings - rejected (S-REV-2)", append([]string{"factory", "report", "retro", "fix"}, baseFlags("patch-shaped-fix", "--findings", "-")...)},
			{"empty --findings rejected", append([]string{"factory", "report", "retro", "fix"}, baseFlags("patch-shaped-fix", "--findings", "")...)},
			{"unsupported --confidence flag (left out of slice 12)", append([]string{"factory", "report", "retro", "ship"}, baseFlags("none", "--confidence", "high")...)},
			{"unsupported --majors flag", append([]string{"factory", "report", "retro", "fix"}, baseFlags("patch-shaped-fix", "--majors", "1")...)},
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
				args := append([]string{"factory", "report", "retro", "fix"}, baseFlags("patch-shaped-fix", "--findings", tc.path)...)
				code := run(context.Background(), args, &out, &errBuf)
				if code != 1 || called != 0 {
					t.Fatalf("code=%d called=%d stderr=%s, want exit 1 and 0 HTTP calls", code, called, errBuf.String())
				}
			})
		}
	})

	t.Run("daemon refusal prints code and sentence and exits 1", func(t *testing.T) {
		called = 0
		replyStatus = http.StatusBadRequest
		replyJSON = `{"error":"invalid_report: ship requires 0 major: lines"}`
		t.Cleanup(func() {
			replyStatus = http.StatusOK
			replyJSON = `{"ok":true}`
		})
		var out, errBuf bytes.Buffer
		args := append([]string{"factory", "report", "retro", "ship"}, baseFlags("none")...)
		code := run(context.Background(), args, &out, &errBuf)
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
		args := append([]string{"factory", "report", "retro", "ship"}, baseFlags("none")...)
		code := run(context.Background(), args, &out, &errBuf)
		if code != 1 || !strings.Contains(errBuf.String(), "127.0.0.1:"+closedPort) {
			t.Fatalf("code=%d stderr=%q, want exit 1 naming 127.0.0.1:%s", code, errBuf.String(), closedPort)
		}
	})
}

// TestFactoryUsageIncludesReportRetro checks that `sheepdog factory help` lists
// `report retro` and every retro flag so TestFactoryTemplatesNameRealCommands
// and users see the exact command shape (plan.md 12.2, design.md 7.2).
func TestFactoryUsageIncludesReportRetro(t *testing.T) {
	var buf bytes.Buffer
	printFactoryUsage(&buf)
	usage := buf.String()
	for _, want := range []string{
		"report retro",
		"--cause",
		"--worker-change",
		"--worker-change-kind",
		"--coordinator-change",
		"--coordinator-change-kind",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("printFactoryUsage missing %q:\n%s", want, usage)
		}
	}
}
