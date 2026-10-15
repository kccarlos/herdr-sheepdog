package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestRunner(t *testing.T, cfg Config, det Detection) *Runner {
	t.Helper()
	r, err := NewRunner(cfg, det)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// TestRunnerTemplatesRenderedAndEnvScrubbed proves {cwd}/{question} are
// substituted, cwd is the workspace, and HERDR_*/CLAUDECODE/CLAUDE_CODE_*
// do not reach the child even though they are set on the test process.
func TestRunnerTemplatesRenderedAndEnvScrubbed(t *testing.T) {
	bin := fakeBinPath(t)
	ws := t.TempDir()
	wantCwd, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_FOO", "bar")

	log := &recLog{}
	cfg := Config{
		Prefer:    "codex",
		CodexBin:  bin,
		ClaudeBin: bin,
		CodexCmd:  []string{bin, "-C", "{cwd}", "{question}"},
		ClaudeCmd: []string{bin, "{question}"},
		Log:       log,
	}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	res, err := r.RunBackend(context.Background(), Codex, ws, "why did tests fail")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if res.Exit != 0 || res.Killed || res.Capped {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Raw, "why did tests fail") {
		t.Errorf("question not substituted: %q", res.Raw)
	}
	gotCwd := fieldOf(t, res.Raw, "CWD")
	resolved, err := filepath.EvalSymlinks(gotCwd)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != wantCwd {
		t.Errorf("cwd = %q, want %q", resolved, wantCwd)
	}
	for _, want := range []string{"HERDR:ABSENT", "CLAUDECODE:ABSENT", "CCFOO:ABSENT"} {
		if !strings.Contains(res.Raw, want) {
			t.Errorf("raw = %q, want to contain %q (env not scrubbed)", res.Raw, want)
		}
	}

	events := log.ofType("cli.run")
	if len(events) != 1 {
		t.Fatalf("cli.run events = %d", len(events))
	}
	payload := events[0].Payload.(map[string]any)
	if payload["backend"] != "codex" || payload["exit"] != 0 || payload["killed"] != false {
		t.Errorf("cli.run payload = %+v", payload)
	}
	if cmd, _ := payload["cmd"].(string); !strings.Contains(cmd, bin) {
		t.Errorf("cmd = %q", cmd)
	}
}

// fieldOf extracts "KEY:value" (semicolon-separated, per fakebin.sh) from
// raw output.
func fieldOf(t *testing.T, raw, key string) string {
	t.Helper()
	for _, part := range strings.Split(raw, ";") {
		if v, ok := strings.CutPrefix(part, key+":"); ok {
			return v
		}
	}
	t.Fatalf("field %q not found in %q", key, raw)
	return ""
}

// TestRunnerQuestionElidedInLog proves a long question is elided to 80
// chars in the logged cmd string, while the argv actually executed keeps
// the full question (the fake script echoes it back in full).
func TestRunnerQuestionElidedInLog(t *testing.T) {
	bin := fakeBinPath(t)
	long := strings.TrimSpace(strings.Repeat("why did it fail ", 20)) // > 80 runes; RunBackend trims the question
	log := &recLog{}
	cfg := Config{CodexBin: bin, CodexCmd: []string{bin, "{question}"}, Log: log}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	res, err := r.RunBackend(context.Background(), Codex, t.TempDir(), long)
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if !strings.Contains(res.Raw, long) {
		t.Error("the executed argv should carry the full question")
	}
	payload := log.ofType("cli.run")[0].Payload.(map[string]any)
	cmd := payload["cmd"].(string)
	if strings.Contains(cmd, long) {
		t.Errorf("logged cmd should elide the question, got %q", cmd)
	}
	if !strings.Contains(cmd, "…") {
		t.Errorf("logged cmd should show elision, got %q", cmd)
	}
}

// TestRunnerTimeoutKillsProcessGroup proves a hung backend (plus a
// grandchild it spawned) is killed at the timeout, with Killed:true, and
// leaves no orphan.
func TestRunnerTimeoutKillsProcessGroup(t *testing.T) {
	bin := fakeBinPath(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("FAKE_SLEEP", "5")
	t.Setenv("FAKE_GRANDCHILD_FILE", pidFile)

	log := &recLog{}
	cfg := Config{
		CodexBin: bin, CodexCmd: []string{bin, "{question}"},
		Timeout: 300 * time.Millisecond, Log: log,
	}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	start := time.Now()
	res, err := r.RunBackend(context.Background(), Codex, t.TempDir(), "slow")
	dur := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("error does not wrap ErrTimedOut: %v", err)
	}
	if !res.Killed {
		t.Errorf("res.Killed = false, want true (res=%+v err=%v)", res, err)
	}
	if dur > 3*time.Second {
		t.Errorf("took %s, should have been killed near the 300ms timeout", dur)
	}
	payload := log.ofType("cli.run")[0].Payload.(map[string]any)
	if payload["killed"] != true {
		t.Errorf("cli.run.killed = %v, want true", payload["killed"])
	}

	// The grandchild's pid file appears almost immediately; wait for it,
	// then confirm the process is gone (process-group kill, not just the
	// direct child).
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, rerr := os.ReadFile(pidFile)
		if rerr == nil && strings.TrimSpace(string(b)) != "" {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild pid file was never written")
	}
	deadline = time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("grandchild pid %d is still alive after the timeout kill", pid)
	}
}

// TestRunnerOutputCapKeepsTail proves stdout beyond max_output_kb is
// capped, keeping the tail, and the cap is logged.
func TestRunnerOutputCapKeepsTail(t *testing.T) {
	bin := fakeBinPath(t)
	t.Setenv("FAKE_BIG", "50000")

	const maxBytes = 1024
	log := &recLog{}
	cfg := Config{
		CodexBin: bin, CodexCmd: []string{bin, "{question}"},
		MaxOutputBytes: maxBytes, Log: log,
	}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	res, err := r.RunBackend(context.Background(), Codex, t.TempDir(), "big")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if !res.Capped {
		t.Error("res.Capped = false, want true")
	}
	if len(res.Raw) > maxBytes {
		t.Errorf("raw is %d bytes, want <= %d", len(res.Raw), maxBytes)
	}
	if !strings.HasSuffix(res.Raw, "TAIL_MARKER") {
		t.Errorf("tail not kept: raw ends with %q", res.Raw[max(0, len(res.Raw)-40):])
	}
	payload := log.ofType("cli.run")[0].Payload.(map[string]any)
	if payload["capped"] != true {
		t.Errorf("cli.run.capped = %v, want true", payload["capped"])
	}
}

// TestRunnerClaudeJSONExtraction proves the `result` field is extracted
// from claude's JSON stdout, and is_error becomes a Go error.
func TestRunnerClaudeJSONExtraction(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{ClaudeBin: bin, ClaudeCmd: []string{bin, "{question}"}}
	r := newTestRunner(t, cfg, Detection{Claude: true})

	t.Setenv("FAKE_CLAUDE_JSON", "1")
	res, err := r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if !strings.Contains(res.Raw, "ARGV:") {
		t.Errorf("raw should be the extracted result field, got %q", res.Raw)
	}

	t.Setenv("FAKE_IS_ERROR", "true")
	_, err = r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err == nil {
		t.Fatal("is_error:true should produce a Go error")
	}
}

// TestRunnerClaudeSurfacesCostAndTurns proves num_turns, duration_ms and
// total_cost_usd from claude's own JSON land on Result and in the
// cli.run log payload, and stay nil/absent for codex (whose stdout never
// carries them).
func TestRunnerClaudeSurfacesCostAndTurns(t *testing.T) {
	bin := fakeBinPath(t)
	log := &recLog{}
	cfg := Config{ClaudeBin: bin, ClaudeCmd: []string{bin, "{question}"}, Log: log}
	r := newTestRunner(t, cfg, Detection{Claude: true})

	t.Setenv("FAKE_CLAUDE_JSON", "1")
	t.Setenv("FAKE_NUM_TURNS", "7")
	t.Setenv("FAKE_DURATION_MS", "15381")
	t.Setenv("FAKE_COST_USD", "0.43")

	res, err := r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if res.NumTurns == nil || *res.NumTurns != 7 {
		t.Errorf("NumTurns = %v, want 7", res.NumTurns)
	}
	if res.DurationMS == nil || *res.DurationMS != 15381 {
		t.Errorf("DurationMS = %v, want 15381", res.DurationMS)
	}
	if res.TotalCostUSD == nil || *res.TotalCostUSD != 0.43 {
		t.Errorf("TotalCostUSD = %v, want 0.43", res.TotalCostUSD)
	}

	payload := log.ofType("cli.run")[0].Payload.(map[string]any)
	if payload["num_turns"] != 7 {
		t.Errorf("payload num_turns = %v", payload["num_turns"])
	}
	if payload["duration_ms"] != int64(15381) {
		t.Errorf("payload duration_ms = %v", payload["duration_ms"])
	}
	if payload["total_cost_usd"] != 0.43 {
		t.Errorf("payload total_cost_usd = %v", payload["total_cost_usd"])
	}
}

// TestRunnerCodexHasNoClaudeMeta proves codex results never carry claude's
// cost/turns fields.
func TestRunnerCodexHasNoClaudeMeta(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{CodexBin: bin, CodexCmd: []string{bin, "{question}"}}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	res, err := r.RunBackend(context.Background(), Codex, t.TempDir(), "why")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if res.NumTurns != nil || res.DurationMS != nil || res.TotalCostUSD != nil {
		t.Errorf("codex result should have nil claude fields, got %+v", res)
	}
}

// TestRunnerClaudeFallsBackToRawText proves non-JSON stdout from claude is
// still returned (fallback per docs/dev/cli-query-tools.md).
func TestRunnerClaudeFallsBackToRawText(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{ClaudeBin: bin, ClaudeCmd: []string{bin, "{question}"}}
	r := newTestRunner(t, cfg, Detection{Claude: true})

	res, err := r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err != nil {
		t.Fatalf("RunBackend: %v", err)
	}
	if !strings.Contains(res.Raw, "ARGV:") {
		t.Errorf("raw = %q", res.Raw)
	}
}

// TestRunnerNonZeroExit proves a failing backend produces an error with
// stderr, and the log carries the exit code and error.
func TestRunnerNonZeroExit(t *testing.T) {
	bin := fakeBinPath(t)
	t.Setenv("FAKE_EXIT", "3")
	t.Setenv("FAKE_STDERR", "boom: something broke")

	log := &recLog{}
	cfg := Config{CodexBin: bin, CodexCmd: []string{bin, "{question}"}, Log: log}
	r := newTestRunner(t, cfg, Detection{Codex: true})

	_, err := r.RunBackend(context.Background(), Codex, t.TempDir(), "why")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "boom: something broke") {
		t.Errorf("error should carry stderr: %v", err)
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("error is not an ExitError with code 3: %#v", err)
	}
	payload := log.ofType("cli.run")[0].Payload.(map[string]any)
	if payload["exit"] != 3 {
		t.Errorf("exit = %v, want 3", payload["exit"])
	}
	if payload["error"] == nil || payload["error"] == "" {
		t.Error("cli.run should log the error")
	}
}

// TestRunnerErrBusy proves a second concurrent call for the same
// workspace is refused immediately instead of queueing.
func TestRunnerErrBusy(t *testing.T) {
	bin := fakeBinPath(t)
	t.Setenv("FAKE_SLEEP", "1")
	cfg := Config{CodexBin: bin, CodexCmd: []string{bin, "{question}"}}
	r := newTestRunner(t, cfg, Detection{Codex: true})
	ws := t.TempDir()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.RunBackend(context.Background(), Codex, ws, "slow")
	}()
	time.Sleep(150 * time.Millisecond) // let the first call start the process

	_, err := r.RunBackend(context.Background(), Codex, ws, "again")
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second call error = %v, want ErrBusy", err)
	}
	<-done

	// After the first call finishes, the slot is free again.
	t.Setenv("FAKE_SLEEP", "")
	if _, err := r.RunBackend(context.Background(), Codex, ws, "now free"); err != nil {
		t.Errorf("slot should be free: %v", err)
	}
}

func TestResolveBackendPrefer(t *testing.T) {
	cases := []struct {
		prefer  string
		det     Detection
		want    Backend
		wantErr bool
	}{
		{"codex", Detection{Codex: true, Claude: true}, Codex, false},
		{"claude", Detection{Codex: true, Claude: true}, Claude, false},
		{"codex", Detection{Claude: true}, "", true},
		{"claude", Detection{Codex: true}, "", true},
		{"auto", Detection{Codex: true, Claude: true}, Codex, false},
		{"auto", Detection{Claude: true}, Claude, false},
		{"", Detection{Claude: true}, Claude, false},
		{"auto", Detection{}, "", true},
	}
	for _, c := range cases {
		r := newTestRunner(t, Config{Prefer: c.prefer}, c.det)
		got, err := r.resolveBackend(c.prefer)
		if c.wantErr {
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("prefer=%q det=%+v: err = %v, want ErrUnavailable", c.prefer, c.det, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("prefer=%q det=%+v: got %v, %v, want %v", c.prefer, c.det, got, err, c.want)
		}
	}
}

// TestQueryImplementsSupervisorQuerier is a compile-time-ish smoke test
// that Query (the supervisor.Querier method) works end to end.
func TestQueryImplementsSupervisorQuerier(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{Prefer: "codex", CodexBin: bin, CodexCmd: []string{bin, "{question}"}}
	r := newTestRunner(t, cfg, Detection{Codex: true})
	out, err := r.Query(context.Background(), t.TempDir(), "why")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !strings.Contains(out, "ARGV:") {
		t.Errorf("out = %q", out)
	}
}

func TestQueryUnavailable(t *testing.T) {
	r := newTestRunner(t, Config{Prefer: "codex"}, Detection{})
	if _, err := r.Query(context.Background(), t.TempDir(), "why"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// budgetJSON is claude 2.1.293's stdout for a fork stopped at its
// --max-budget-usd (2026-10-08, trimmed): exit 1, stderr empty, no
// result field.
const budgetJSON = `{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":2,"total_cost_usd":536.44,"session_id":"44f1f415-fae0-4764-a9f2-d5fae6a9e517","terminal_reason":"budget_exhausted","errors":["Reached maximum budget ($2.00)"]}`

// TestRunnerClaudeExitCarriesReportedReason: 2026-10-08 00:17 a fork
// failed as "cli: claude: exit 1: " because claude said why only in its
// JSON stdout. The error now carries the subtype and errors, matches
// ErrBudget, and the cost is still logged.
func TestRunnerClaudeExitCarriesReportedReason(t *testing.T) {
	bin := fakeBinPath(t)
	log := &recLog{}
	cfg := Config{ClaudeBin: bin, ClaudeCmd: []string{bin, "{question}"}, Log: log}
	r := newTestRunner(t, cfg, Detection{Claude: true})

	t.Setenv("FAKE_EXIT", "1")
	t.Setenv("FAKE_STDOUT", budgetJSON)
	_, err := r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err == nil {
		t.Fatal("expected an error")
	}
	if want := "cli: claude: exit 1: error_max_budget_usd: Reached maximum budget ($2.00)"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrBudget) {
		t.Error("errors.Is(err, ErrBudget) = false")
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("not an ExitError with code 1: %#v", err)
	}
	if p := log.ofType("cli.run")[0].Payload.(map[string]any); p["total_cost_usd"] != 536.44 || p["error"] != err.Error() {
		t.Errorf("cli.run = %v", p)
	}

	// Another reported error is not the budget; is_error with exit 0
	// says why too (it used to print an empty result).
	t.Setenv("FAKE_STDOUT", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["API Error: 529 Overloaded"]}`)
	_, err = r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err == nil || errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "error_during_execution: API Error: 529 Overloaded") {
		t.Errorf("during execution: %v", err)
	}
	t.Setenv("FAKE_EXIT", "0")
	_, err = r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err == nil || err.Error() != "cli: claude reported an error: error_during_execution: API Error: 529 Overloaded" {
		t.Errorf("exit 0, is_error: %v", err)
	}

	// Neither stderr nor JSON: the start of stdout stands in.
	t.Setenv("FAKE_EXIT", "1")
	t.Setenv("FAKE_STDOUT", "Credit balance is too low")
	_, err = r.RunBackend(context.Background(), Claude, t.TempDir(), "why")
	if err == nil || !strings.HasSuffix(err.Error(), "exit 1: Credit balance is too low") {
		t.Errorf("plain stdout: %v", err)
	}
}
