package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskTestFlagsAndValidation(t *testing.T) {
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{}, 2, "needs --workspace"},
		{[]string{"--workspace", "d"}, 2, "needs --question"},
		{[]string{"--workspace", "d", "--question", "q", "--backend", "bogus"}, 2, "--backend must be"},
		{[]string{"--help"}, 0, "usage: sheepdog ask-test"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		_, code, run := parseAskTestFlags(c.args, &stderr)
		if run || code != c.code || !strings.Contains(stderr.String(), c.msg) {
			t.Errorf("%v: run=%v code=%d stderr=%q", c.args, run, code, stderr.String())
		}
	}
	o, code, run := parseAskTestFlags([]string{"--workspace", "d", "--question", "why", "--backend", "codex", "--raw"}, &bytes.Buffer{})
	if !run || code != 0 || !o.raw || o.backend != "codex" {
		t.Errorf("valid flags: run=%v code=%d opts=%+v", run, code, o)
	}
}

// writeFakeCLI writes an executable shell script that prints body verbatim
// (ignoring argv), standing in for codex/claude.
func writeFakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeSpokenAnswerLLM answers spoken_answer with a fixed two-sentence
// reply naming the failing test, mirroring what a real cheap model would
// return for the acceptance scenario ("why did the tests fail").
func fakeSpokenAnswerLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"spoken":"TestTotal failed because it expected 5 but got 4.","can_answer":true}`,
		}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeAskTestConfig writes a [cli] config pointing codex_bin/claude_bin at
// fake scripts, and returns its path.
func writeAskTestConfig(t *testing.T, dir, codexBin, claudeBin string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`
[cli]
prefer = "codex"
codex_bin = %q
codex_cmd = [%q, "{question}"]
claude_bin = %q
claude_cmd = [%q, "{question}"]
timeout_s = 5
max_output_kb = 16
`, codexBin, codexBin, claudeBin, claudeBin)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunAskTestCodexPlainText(t *testing.T) {
	dir := t.TempDir()
	codexBin := writeFakeCLI(t, dir, "fake-codex", "printf 'TestTotal: expected 5, got 4\\n'\n")
	claudeBin := writeFakeCLI(t, dir, "fake-claude", "exit 1\n")
	cfgPath := writeAskTestConfig(t, dir, codexBin, claudeBin)
	llmSrv := fakeSpokenAnswerLLM(t)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"ask-test", "--config", cfgPath, "--workspace", ws, "--question", "why did the tests fail",
		"--backend", "codex", "--base-url", llmSrv.URL + "/v1", "--model", "m", "--api-key", "k",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v (%s)", err, stdout.String())
	}
	if out["backend"] != "codex" {
		t.Errorf("backend = %v", out["backend"])
	}
	if raw, _ := out["raw"].(string); !strings.Contains(raw, "TestTotal") {
		t.Errorf("raw = %v", out["raw"])
	}
	if spoken, _ := out["spoken"].(string); !strings.Contains(spoken, "TestTotal") {
		t.Errorf("spoken = %v", out["spoken"])
	}
	if out["error"] != nil {
		t.Errorf("unexpected error: %v", out["error"])
	}
}

func TestRunAskTestRawSkipsSpokenAnswer(t *testing.T) {
	dir := t.TempDir()
	codexBin := writeFakeCLI(t, dir, "fake-codex", "printf 'raw only\\n'\n")
	claudeBin := writeFakeCLI(t, dir, "fake-claude", "exit 1\n")
	cfgPath := writeAskTestConfig(t, dir, codexBin, claudeBin)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"ask-test", "--config", cfgPath, "--workspace", ws, "--question", "why", "--backend", "codex", "--raw",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if _, ok := out["spoken"]; ok {
		t.Errorf("--raw should skip spoken_answer, got %v", out["spoken"])
	}
	if raw, _ := out["raw"].(string); !strings.Contains(raw, "raw only") {
		t.Errorf("raw = %v", out["raw"])
	}
}

func TestRunAskTestClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	codexBin := writeFakeCLI(t, dir, "fake-codex", "exit 1\n")
	claudeBin := writeFakeCLI(t, dir, "fake-claude", `printf '{"result":"TestTotal expected 5 got 4","is_error":false}\n'`+"\n")
	cfgPath := writeAskTestConfig(t, dir, codexBin, claudeBin)
	llmSrv := fakeSpokenAnswerLLM(t)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"ask-test", "--config", cfgPath, "--workspace", ws, "--question", "why did the tests fail",
		"--backend", "claude", "--base-url", llmSrv.URL + "/v1", "--model", "m", "--api-key", "k",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if out["raw"] != "TestTotal expected 5 got 4" {
		t.Errorf("raw = %v, want the extracted result field", out["raw"])
	}
}

func TestRunAskTestBackendFailureExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	codexBin := writeFakeCLI(t, dir, "fake-codex", "echo boom >&2\nexit 3\n")
	claudeBin := writeFakeCLI(t, dir, "fake-claude", "exit 1\n")
	cfgPath := writeAskTestConfig(t, dir, codexBin, claudeBin)
	ws := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"ask-test", "--config", cfgPath, "--workspace", ws, "--question", "why", "--backend", "codex", "--raw",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit for a failing backend")
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v", err)
	}
	if out["error"] == nil {
		t.Error("expected an error field in the JSON output")
	}
}
