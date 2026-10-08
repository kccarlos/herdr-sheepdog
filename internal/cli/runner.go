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
// for its JSON report.
type Result struct {
	Backend Backend
	Argv    []string
	DurMS   int64
	Exit    int
	Killed  bool
	// Capped reports whether stdout was longer than MaxOutputBytes (the
	// tail was kept).
	Capped bool
	// Raw is the extracted answer: codex's last agent_message (its stdout
	// when it was not JSONL), or claude's `result` field (raw stdout when
	// it was not JSON).
	Raw string
	// Stderr is the truncated stderr, for diagnostics.
	Stderr string
	// SessionID is the backend's own session: codex's thread id, claude's
	// session_id. A follow-up resumes it (Query.Session); "" means the
	// backend did not report one and a follow-up must start cold.
	SessionID string
	// Resumed reports that this run continued Query.Session rather than
	// starting a new one.
	Resumed bool
	// Forked reports that this run forked Query.Session (Query.Fork):
	// SessionID is then the fork's own id, never Query.Session.
	Forked bool

	// NumTurns, DurationMS and TotalCostUSD come from claude's own JSON
	// result object (docs/cli-query-tools.md §2) when present; nil for
	// codex or when claude's stdout was not JSON. DurationMS is claude's
	// self-reported wall time and is distinct from DurMS above, which
	// Runner measures itself around the whole process.
	NumTurns     *int
	DurationMS   *int64
	TotalCostUSD *float64
}

// Query is one invocation: which backend, where, what to ask, and
// whether to resume a session the backend kept. The zero value runs the
// configured preference in Workspace with cfg.Timeout.
type Query struct {
	// Backend pins the backend; "" applies cfg.Prefer.
	Backend Backend
	// Workspace is the agent's working directory (the child's cwd, and
	// {cwd} in the codex template).
	Workspace string
	// Question is what to ask, self-contained.
	Question string
	// Session, when set, continues that backend session through the
	// resume templates instead of starting a new one.
	Session string
	// Fork asks Session through the fork templates instead: a copy of
	// the session answers and the original is not written to
	// (docs/cli-query-tools.md §2c). It is how a worker's own session is
	// asked; Backend must name the worker's backend and Session is
	// required.
	Fork bool
	// Timeout overrides cfg.Timeout for this run: an investigation has
	// its own, much larger ceiling (brain.investigate_max_s).
	Timeout time.Duration
	// Slot names the one-at-a-time slot; "" uses Workspace. A background
	// investigation passes its own, so a minutes-long job does not lock
	// the orchestrator out of a short run_cli lookup in the same repository.
	Slot string
}

// Runner implements supervisor.Querier by shelling out to codex or claude
// in read-only mode (plan §3.5, WP4). It is safe for concurrent use; at
// most one query runs per slot (by default the workspace) at a time
// (ErrBusy otherwise).
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
// and returns its raw output (the orchestrator's run_cli tool).
func (r *Runner) Query(ctx context.Context, workspace, question string) (string, error) {
	res, err := r.Run(ctx, Query{Workspace: workspace, Question: question})
	if err != nil {
		return "", err
	}
	return res.Raw, nil
}

// ResolveBackend applies cli.prefer and returns the backend Query would
// use.
func (r *Runner) ResolveBackend() (Backend, error) {
	return r.resolveBackend(r.cfg.Prefer)
}

// Has reports whether backend was detected at startup (a fork must run
// on the worker's own backend, whatever cli.prefer says).
func (r *Runner) Has(backend Backend) bool {
	switch backend {
	case Codex:
		return r.det.Codex
	case Claude:
		return r.det.Claude
	}
	return false
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

// Run carries out one query: it renders the backend's template (the
// resume one when q.Session is set), runs the child in the workspace and
// returns what it answered with the session it left behind. At most one
// call per slot runs at a time; a concurrent call for the same slot
// returns ErrBusy immediately rather than queueing.
func (r *Runner) Run(ctx context.Context, q Query) (Result, error) {
	backend := q.Backend
	if backend == "" {
		var err error
		if backend, err = r.resolveBackend(r.cfg.Prefer); err != nil {
			return Result{}, err
		}
	}
	slot := q.Slot
	if slot == "" {
		slot = q.Workspace
	}
	if !r.lock(slot) {
		return Result{}, ErrBusy
	}
	defer r.unlock(slot)

	if q.Fork && q.Session == "" {
		return Result{}, fmt.Errorf("cli: %s: a fork needs the session to fork", backend)
	}
	tmpl, bin, err := r.template(backend, q.Session != "", q.Fork)
	if err != nil {
		return Result{}, err
	}
	question := strings.TrimSpace(q.Question)
	vars := map[string]string{
		"question":  question,
		"cwd":       q.Workspace,
		"model":     r.cfg.ClaudeModel,
		"reasoning": r.cfg.CodexReasoning,
		"session":   q.Session,
	}
	argv := renderArgv(tmpl, vars)
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("cli: empty command template for %s", backend)
	}
	argv[0] = bin

	timeout := q.Timeout
	if timeout <= 0 {
		timeout = r.cfg.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	exec := runCommand(runCtx, argv, q.Workspace, r.cfg.MaxOutputBytes)
	dur := time.Since(start)

	result := Result{
		Backend: backend,
		Argv:    argv,
		DurMS:   dur.Milliseconds(),
		Exit:    exec.Exit,
		Killed:  exec.Killed,
		Capped:  exec.StdoutCapped,
		Stderr:  exec.Stderr,
		Resumed: q.Session != "" && !q.Fork,
		Forked:  q.Fork,
	}
	if !q.Fork {
		// A resumed run keeps its session even when the output named
		// none; a fork's session is only ever the one it reported, so
		// the forked session can never be mistaken for the fork's.
		result.SessionID = q.Session
	}

	resErr := r.finish(&result, exec, backend, timeout)
	if resErr == nil && q.Fork && result.SessionID == q.Session {
		resErr = fmt.Errorf("%w: %s reported the original session back", ErrNotForked, backend)
	}
	r.log(result, argv, question, resErr)
	return result, resErr
}

// template returns the argv template and binary for backend; resume
// picks the follow-up template and fork the fork template (fixed argv,
// docs/cli-query-tools.md §2c).
func (r *Runner) template(backend Backend, resume, fork bool) ([]string, string, error) {
	switch {
	case backend == Codex && fork:
		return defaultCodexForkCmd, r.cfg.CodexBin, nil
	case backend == Claude && fork:
		return defaultClaudeForkCmd, r.cfg.ClaudeBin, nil
	case backend == Codex && resume:
		return r.cfg.CodexResumeCmd, r.cfg.CodexBin, nil
	case backend == Codex:
		return r.cfg.CodexCmd, r.cfg.CodexBin, nil
	case backend == Claude && resume:
		return r.cfg.ClaudeResumeCmd, r.cfg.ClaudeBin, nil
	case backend == Claude:
		return r.cfg.ClaudeCmd, r.cfg.ClaudeBin, nil
	default:
		return nil, "", fmt.Errorf("cli: unknown backend %q", backend)
	}
}

// finish turns the raw process outcome into result.Raw, or an error. The
// process is never re-run; every branch fills in result.Raw when there is
// anything useful to say.
func (r *Runner) finish(result *Result, exec execResult, backend Backend, timeout time.Duration) error {
	if exec.StartErr != nil && !exec.Killed {
		return fmt.Errorf("cli: %s: %w", backend, exec.StartErr)
	}
	if exec.Killed {
		return fmt.Errorf("cli: %s: %w after %s", backend, ErrTimedOut, timeout)
	}
	if exec.Exit != 0 {
		xe := &ExitError{Backend: backend, Code: exec.Exit, Stderr: strings.TrimSpace(exec.Stderr)}
		if backend == Claude {
			if parsed, ok := parseClaudeResult(exec.Stdout); ok {
				xe.Subtype, xe.Reported = parsed.Subtype, parsed.reported()
				result.NumTurns, result.DurationMS, result.TotalCostUSD = parsed.NumTurns, parsed.DurationMS, parsed.TotalCostUSD
			}
		}
		if xe.Stderr == "" && xe.Reported == "" {
			// Neither said why: the end of stdout is all there is.
			xe.Reported = elideRunes(strings.TrimSpace(exec.Stdout), 300)
		}
		return xe
	}
	switch backend {
	case Claude:
		if parsed, ok := parseClaudeResult(exec.Stdout); ok {
			if parsed.IsError {
				return &ExitError{Backend: backend, Subtype: parsed.Subtype, Reported: parsed.reported()}
			}
			result.Raw = strings.TrimSpace(parsed.Result)
			result.NumTurns = parsed.NumTurns
			result.DurationMS = parsed.DurationMS
			result.TotalCostUSD = parsed.TotalCostUSD
			if parsed.SessionID != "" {
				result.SessionID = parsed.SessionID
			}
		} else {
			result.Raw = strings.TrimSpace(exec.Stdout) // not JSON: fall back to raw text
		}
	default:
		if answer, thread, ok := parseCodexJSONL(exec.Stdout); ok {
			result.Raw = answer
			if thread == "" && exec.StdoutCapped {
				// A talkative run (codex streams every command's output)
				// pushed thread.started out of the tail; it is on the
				// first line, which the head kept.
				_, thread, _ = parseCodexJSONL(exec.StdoutHead)
			}
			if thread != "" {
				result.SessionID = thread
			}
		} else {
			result.Raw = strings.TrimSpace(exec.Stdout) // not JSONL: a template without --json
		}
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
		// The session itself is never logged (it names a transcript on
		// disk); whether there is one decides if a follow-up can resume.
		"session": result.SessionID != "",
		"resumed": result.Resumed,
		"forked":  result.Forked,
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
