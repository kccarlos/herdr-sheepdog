package main

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

func TestFactoryReportPlanReviewCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SHEEPDOG_RUN_TOKEN", "tok-plan-cli")
	var called int
	var last struct {
		Token, Verb string
		Args        map[string]any
	}
	status, reply := http.StatusOK, `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		last.Token, last.Verb, last.Args = "", "", nil
		_ = json.NewDecoder(r.Body).Decode(&last)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("SHEEPDOG_WEB_PORT", port)
	execCLI := func(args ...string) (int, string, string) {
		called = 0
		var out, errBuf bytes.Buffer
		return run(context.Background(), args, &out, &errBuf), out.String(), errBuf.String()
	}
	t.Run("valid pass and changes with --findings", func(t *testing.T) {
		if c, out, e := execCLI("factory", "report", "plan-review", "pass", "--code", "abc1234567"); c != 0 || strings.TrimSpace(out) != "plan-review pass" || called != 1 || last.Token != "tok-plan-cli" || last.Verb != "plan-review" || last.Args["verdict"] != "pass" || last.Args["code"] != "abc1234567" || last.Args["findings"] != "" {
			t.Fatalf("pass: code=%d out=%q err=%q called=%d body=%+v", c, out, e, called, last)
		}
		fPath, wantF := filepath.Join(t.TempDir(), "plan-r1.draft.md"), "delete: T2 optional backlog item not in goal\n"
		_ = os.WriteFile(fPath, []byte(wantF), 0o600)
		if c, out, e := execCLI("factory", "report", "plan-review", "--code", "abc1234567", "--findings", fPath, "changes"); c != 0 || strings.TrimSpace(out) != "plan-review changes" || called != 1 || last.Verb != "plan-review" || last.Args["verdict"] != "changes" || last.Args["findings"] != wantF {
			t.Fatalf("changes: code=%d out=%q err=%q called=%d body=%+v", c, out, e, called, last)
		}
	})
	t.Run("usage errors exit 2; file errors and daemon refusal exit 1", func(t *testing.T) {
		for _, args := range [][]string{
			{"factory", "report", "plan-review"}, {"factory", "report", "plan-review", "ship", "--code", "c"},
			{"factory", "report", "plan-review", "pass", "extra", "--code", "c"}, {"factory", "report", "plan-review", "pass"},
			{"factory", "report", "plan-review", "pass", "--code", " "}, {"factory", "report", "plan-review", "changes", "--code", "c", "--findings", "-"},
			{"factory", "report", "plan-review", "changes", "--code", "c", "--findings", ""}, {"factory", "report", "plan-review", "pass", "--code", "c", "--head", "abc"},
		} {
			if c, _, e := execCLI(args...); c != 2 || called != 0 {
				t.Errorf("args=%q: code=%d called=%d err=%q, want exit 2 and 0 calls", args, c, called, e)
			}
		}
		big := filepath.Join(t.TempDir(), "big.md")
		_ = os.WriteFile(big, bytes.Repeat([]byte("a"), 64*1024+1), 0o600)
		for _, path := range []string{filepath.Join(t.TempDir(), "nope.md"), big} {
			if c, _, e := execCLI("factory", "report", "plan-review", "changes", "--code", "c", "--findings", path); c != 1 || called != 0 {
				t.Errorf("path=%s: code=%d called=%d err=%q, want exit 1 and 0 calls", path, c, called, e)
			}
		}
		status, reply = http.StatusBadRequest, `{"error":"invalid_report: wrong code"}`
		if c, _, e := execCLI("factory", "report", "plan-review", "pass", "--code", "bad"); c != 1 || !strings.Contains(e, "invalid_report") {
			t.Fatalf("refusal: code=%d err=%q, want exit 1 with invalid_report", c, e)
		}
	})
}

func TestFactoryUsageIncludesReportPlanReview(t *testing.T) {
	var buf bytes.Buffer
	printFactoryUsage(&buf)
	for _, want := range []string{"report plan-review", "--code", "--findings"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("printFactoryUsage missing %q", want)
		}
	}
}
