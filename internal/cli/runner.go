package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// logQuestionChars is how much of the question survives in the logged
// argv (plan: "question elided to 80 chars").
const logQuestionChars = 80

// Result is one CLI invocation's outcome, returned by RunBackend and used
// by `sheepdog ask-test` for its JSON report.
type Result struct {
	Backend Backend
	Argv    []string
	DurMS   int64
	Exit    int
	Killed  bool
	// Capped reports whether stdout was longer than MaxOutputBytes (the
	// tail was kept).
	Capped bool
	// Raw is the extracted answer: codex's stdout, or claude's `result`
	// field (raw stdout when it was not JSON).
	Raw string
	// Stderr is the truncated stderr, for diagnostics.
	Stderr string

	// NumTurns, DurationMS and TotalCostUSD come from claude's own JSON
	// result object (docs/cli-query-tools.md §2) when present; nil for
	// codex or when claude's stdout was not JSON. DurationMS is claude's
	// self-reported wall time and is distinct from DurMS above, which
	// Runner measures itself around the whole process.
	NumTurns     *int
	DurationMS   *int64
	TotalCostUSD *float64
}

// Runner implements supervisor.Querier by shelling out to codex or claude
// in read-only mode (plan §3.5, WP4). It is safe for concurrent use; at
// most one query runs per workspace at a time (ErrBusy otherwise).
type Runner struct {
	cfg Config
	det Detection

	mu   sync.Mutex
	busy map[string]bool
}

// NewRunner validates cfg and returns a Runner. det is the result of a
// Detect call made once at startup; Runner does not re-detect per query.
func NewRunner(cfg Config, det Detection) (*Runner, error) {
	cfg = cfg.withDefaults()
	if len(cfg.CodexCmd) == 0 || len(cfg.ClaudeCmd) == 0 {
		return nil, fmt.Errorf("cli: codex_cmd and claude_cmd must not be empty")
	}
	return &Runner{cfg: cfg, det: det, busy: map[string]bool{}}, nil
}

// Query implements supervisor.Querier: it picks the backend per cfg.Prefer
// and returns its raw output (SpokenAnswer does the rest).
func (r *Runner) Query(ctx context.Context, workspace, question string) (string, error) {
	backend, err := r.resolveBackend(r.cfg.Prefer)
	if err != nil {
		return "", err
	}
	res, err := r.RunBackend(ctx, backend, workspace, question)
	if err != nil {
		return "", err
	}
	return res.Raw, nil
}

// ResolveBackend applies cli.prefer and returns the backend Query would
// use (`sheepdog ask-test` uses it when --backend is not given).
func (r *Runner) ResolveBackend() (Backend, error) {
	return r.resolveBackend(r.cfg.Prefer)
}

// resolveBackend applies cli.prefer: "codex" or "claude" pins the
// backend (ErrUnavailable if not detected); "auto" (or "") picks codex if
// present, else claude, else ErrUnavailable.
func (r *Runner) resolveBackend(prefer string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(prefer)) {
	case string(Codex):
		if !r.det.Codex {
			return "", ErrUnavailable
		}
		return Codex, nil
	case string(Claude):
		if !r.det.Claude {
			return "", ErrUnavailable
		}
		return Claude, nil
	default: // "auto" or unset
		if r.det.Codex {
			return Codex, nil
		}
		if r.det.Claude {
			return Claude, nil
		}
		return "", ErrUnavailable
	}
}

// RunBackend runs a specific backend regardless of cli.prefer (used by
// `sheepdog ask-test --backend`). At most one call per workspace runs at
// a time; a concurrent call for the same workspace returns ErrBusy
// immediately rather than queueing.
func (r *Runner) RunBackend(ctx context.Context, backend Backend, workspace, question string) (Result, error) {
	if !r.lock(workspace) {
		return Result{}, ErrBusy
	}
	defer r.unlock(workspace)

	tmpl, bin, err := r.template(backend)
	if err != nil {
		return Result{}, err
	}
	question = strings.TrimSpace(question)
	vars := map[string]string{
		"question":  question,
		"cwd":       workspace,
		"model":     r.cfg.ClaudeModel,
		"reasoning": r.cfg.CodexReasoning,
	}
	argv := renderArgv(tmpl, vars)
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("cli: empty command template for %s", backend)
	}
	argv[0] = bin

	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	start := time.Now()
	exec := runCommand(runCtx, argv, workspace, r.cfg.MaxOutputBytes)
	dur := time.Since(start)

	result := Result{
		Backend: backend,
		Argv:    argv,
		DurMS:   dur.Milliseconds(),
		Exit:    exec.Exit,
		Killed:  exec.Killed,
		Capped:  exec.StdoutCapped,
		Stderr:  exec.Stderr,
	}

	resErr := r.finish(&result, exec, backend)
	r.log(result, argv, question, resErr)
	return result, resErr
}

// template returns the argv template and binary for backend.
func (r *Runner) template(backend Backend) ([]string, string, error) {
	switch backend {
	case Codex:
		return r.cfg.CodexCmd, r.cfg.CodexBin, nil
	case Claude:
		return r.cfg.ClaudeCmd, r.cfg.ClaudeBin, nil
	default:
		return nil, "", fmt.Errorf("cli: unknown backend %q", backend)
	}
}

// finish turns the raw process outcome into result.Raw, or an error. The
// process is never re-run; every branch fills in result.Raw when there is
// anything useful to say.
func (r *Runner) finish(result *Result, exec execResult, backend Backend) error {
	if exec.StartErr != nil && !exec.Killed {
		return fmt.Errorf("cli: %s: %w", backend, exec.StartErr)
	}
	if exec.Killed {
		return fmt.Errorf("cli: %s: timed out after %s", backend, r.cfg.Timeout)
	}
	if exec.Exit != 0 {
		return fmt.Errorf("cli: %s: exit %d: %s", backend, exec.Exit, strings.TrimSpace(exec.Stderr))
	}
	switch backend {
	case Claude:
		if parsed, ok := parseClaudeResult(exec.Stdout); ok {
			if parsed.IsError {
				return fmt.Errorf("cli: claude reported an error: %s", strings.TrimSpace(parsed.Result))
			}
			result.Raw = strings.TrimSpace(parsed.Result)
			result.NumTurns = parsed.NumTurns
			result.DurationMS = parsed.DurationMS
			result.TotalCostUSD = parsed.TotalCostUSD
		} else {
			result.Raw = strings.TrimSpace(exec.Stdout) // not JSON: fall back to raw text
		}
	default:
		result.Raw = strings.TrimSpace(exec.Stdout)
	}
	if result.Raw == "" {
		return fmt.Errorf("cli: %s: empty output", backend)
	}
	return nil
}

// log emits cli.run: backend, cmd (argv joined, question elided), exit,
// dur_ms, output (truncated 1 KB) and killed.
func (r *Runner) log(result Result, fullArgv []string, question string, resErr error) {
	// fullArgv already has {question} substituted with the full question;
	// replace it back down to an elided form wherever it appears verbatim.
	logArgv := fullArgv
	if question != "" {
		elided := elideRunes(question, logQuestionChars)
		logArgv = make([]string, len(fullArgv))
		for i, a := range fullArgv {
			logArgv[i] = strings.ReplaceAll(a, question, elided)
		}
	}
	output := result.Raw
	if output == "" && resErr != nil {
		output = resErr.Error()
	}
	payload := map[string]any{
		"backend": string(result.Backend),
		"cmd":     strings.Join(logArgv, " "),
		"exit":    result.Exit,
		"dur_ms":  result.DurMS,
		"output":  elideRunes(output, 1024),
		"killed":  result.Killed,
		"capped":  result.Capped,
	}
	if resErr != nil {
		payload["error"] = resErr.Error()
	}
	// claude's own accounting (docs/cli-query-tools.md §2), when its JSON
	// output included it; nil (unset) for codex or non-JSON stdout.
	if result.NumTurns != nil {
		payload["num_turns"] = *result.NumTurns
	}
	if result.DurationMS != nil {
		payload["duration_ms"] = *result.DurationMS
	}
	if result.TotalCostUSD != nil {
		payload["total_cost_usd"] = *result.TotalCostUSD
	}
	r.cfg.Log.Log("cli", "cli.run", "", payload)
}

// lock claims the workspace slot, or reports false when it is already
// held (ErrBusy).
func (r *Runner) lock(workspace string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy[workspace] {
		return false
	}
	r.busy[workspace] = true
	return true
}

func (r *Runner) unlock(workspace string) {
	r.mu.Lock()
	delete(r.busy, workspace)
	r.mu.Unlock()
}
