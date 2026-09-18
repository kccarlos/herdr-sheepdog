package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLLMTestFlagsAndValidation(t *testing.T) {
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{}, 2, "unknown or missing --template"},
		{[]string{"--template", "bogus"}, 2, "unknown or missing --template"},
		{[]string{"--template", "summarize_block"}, 2, "needs --screen"},
		{[]string{"--template", "spoken_answer", "--screen", "x"}, 2, "needs --text"},
		{[]string{"--template", "summarize_done"}, 2, "needs --screen"},
		{[]string{"--template", "resolve_instruction", "--text", "hi"}, 2, "needs --text and --agents"},
		{[]string{"--help"}, 0, "usage: sheepdog llm-test"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		_, code, run := parseLLMTestFlags(c.args, &stderr)
		if run || code != c.code || !strings.Contains(stderr.String(), c.msg) {
			t.Errorf("%v: run=%v code=%d stderr=%q", c.args, run, code, stderr.String())
		}
	}
	o, code, run := parseLLMTestFlags([]string{"--template", "resolve_instruction", "--text", "yes", "--agents", "auth:blocked:whether to write", "--agent", "auth"}, &bytes.Buffer{})
	if !run || code != 0 || o.agent != "auth" {
		t.Errorf("valid flags: run=%v code=%d opts=%+v", run, code, o)
	}
	if o, code, run := parseLLMTestFlags([]string{"--template", "summarize_done", "--screen", "x"}, &bytes.Buffer{}); !run || code != 0 || o.template != "summarize_done" {
		t.Errorf("summarize_done flags: run=%v code=%d opts=%+v", run, code, o)
	}
}

func TestParseAgentList(t *testing.T) {
	got := parseAgentList("auth:working:run the tests, docs:blocked:whether to write claude.txt,vps,,bare:idle")
	if len(got) != 4 {
		t.Fatalf("got %d agents: %+v", len(got), got)
	}
	if got[0].Alias != "auth" || got[0].State != "working" || got[0].Task != "run the tests" || got[0].PaneID != "w1:p1" {
		t.Errorf("auth = %+v", got[0])
	}
	if got[1].Alias != "docs" || got[1].State != "blocked" || got[1].Question != "whether to write claude.txt" || got[1].Task != "" {
		t.Errorf("docs = %+v", got[1])
	}
	if got[2].Alias != "vps" || got[2].State != "idle" || got[3].Alias != "bare" || got[3].State != "idle" {
		t.Errorf("defaults = %+v %+v", got[2], got[3])
	}
}

func TestRunLLMTestAgainstFakeEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"spoken":"auth is asking whether it can create hello.txt.","question":"whether it can create hello.txt","is_question":true,"needs_decision":true,"options":[{"label":"Yes","key":"enter"}],"answer_keys":{"yes":["enter"],"no":["esc"]}}`,
		}}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	screen := filepath.Join(dir, "screen.txt")
	if err := os.WriteFile(screen, []byte("Would you like to run the following command?\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-test", "--config", filepath.Join(dir, "none.toml"), "--base-url", srv.URL + "/v1", "--model", "m", "--api-key", "k", "--template", "summarize_block", "--screen", screen, "--alias", "auth"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	var out struct {
		Spoken     string `json:"spoken"`
		IsQuestion bool   `json:"is_question"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || !out.IsQuestion || !strings.HasPrefix(out.Spoken, "auth is asking") {
		t.Errorf("stdout = %s (%v)", stdout.String(), err)
	}
	if !strings.Contains(stderr.String(), `"type":"llm.call"`) || strings.Contains(stderr.String(), `"k"`) {
		t.Errorf("stderr = %s", stderr.String())
	}
	// Missing key with a non-Gemini endpoint.
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "SHEEPDOG_LLM_API_KEY"} {
		t.Setenv(k, "")
	}
	stderr.Reset()
	if code := run(context.Background(), []string{"llm-test", "--config", filepath.Join(dir, "none.toml"), "--base-url", srv.URL + "/v1", "--template", "summarize_block", "--screen", screen}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "no API key") {
		t.Errorf("no key: code=%d stderr=%s", code, stderr.String())
	}
}
