package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBrainTestFlagsAndValidation(t *testing.T) {
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{}, 2, "needs --question"},
		{[]string{"--question", "q", "--kind", "shout"}, 2, "--kind must be ask or propose"},
		{[]string{"--question", "q", "--state", "asleep"}, 2, "--state must be"},
		{[]string{"--help"}, 0, "usage: sheepdog brain-test"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		_, code, run := parseBrainTestFlags(c.args, &stderr)
		if run || code != c.code || !strings.Contains(stderr.String(), c.msg) {
			t.Errorf("%v: run=%v code=%d stderr=%q", c.args, run, code, stderr.String())
		}
	}
	o, code, run := parseBrainTestFlags([]string{"--question", "why?", "--kind", "propose", "--say", "a", "--say", "b", "--fake"}, &bytes.Buffer{})
	if !run || code != 0 || o.kind != "propose" || len(o.say) != 2 || !o.fake {
		t.Errorf("valid flags: run=%v code=%d opts=%+v", run, code, o)
	}
}

// The fake model runs the whole chain without a network: mock herdr,
// screen fixture, brain turn, events on stderr, reply on stdout.
func TestBrainTestFakeEndToEnd(t *testing.T) {
	t.Setenv("SHEEPDOG_LLM_BASE_URL", "http://127.0.0.1:1/v1")
	t.Setenv("SHEEPDOG_LLM_API_KEY", "x")
	t.Setenv("SHEEPDOG_DATA_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"brain-test", "--fake", "--question", "what did auth say?", "--agent", "auth", "--state", "working", "--fixture", "../../testdata/herdr/codex-working.screen.txt", "--say", "what did auth say?", "--config", "/nonexistent/config.toml"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	var out struct {
		Result struct {
			Spoken, Detail, Scheduling string
		}
		Transcript []struct{ Kind string }
		Error      string
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout: %v\n%s", err, stdout.String())
	}
	if out.Error != "" || out.Result.Spoken != "The fake model read the screen of auth." || out.Result.Scheduling != "idle" || !strings.Contains(out.Result.Detail, "scripted") {
		t.Errorf("out = %+v", out)
	}
	var kinds []string
	for _, m := range out.Transcript {
		kinds = append(kinds, m.Kind)
	}
	if got := strings.Join(kinds, " "); got != "worker_event ceo_utterance request brain_tool_call brain_tool_result reply" {
		t.Errorf("transcript kinds = %s", got)
	}
	for _, ev := range []string{`"type":"brain.turn"`, `"type":"brain.step"`, `"type":"brain.tool_call"`, `"type":"brain.tool_result"`, `"type":"brain.system_prompt"`, `"type":"herdr.read"`} {
		if !strings.Contains(stderr.String(), ev) {
			t.Errorf("stderr lacks %s", ev)
		}
	}
	if strings.Contains(stderr.String(), `"type":"llm.call"`) {
		t.Error("the template path ran")
	}
	// Without a key and without --fake it refuses.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"brain-test", "--question", "q", "--fixture", "../../testdata/herdr/codex-working.screen.txt", "--config", "/nonexistent/config.toml"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no Gemini key") {
		t.Errorf("no key: exit %d stderr %s", code, stderr.String())
	}
}
