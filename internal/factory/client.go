package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/procprio"
)

const (
	defaultReadTimeout   = 15 * time.Second
	defaultActionTimeout = 30 * time.Second
	maxContractBytes     = 8 << 20
)

// ErrOwnerUnsupported indicates that the unpacked engine build predates `owner ... --json` (SDF-21).
var ErrOwnerUnsupported = errors.New("factory: engine owner subcommand is not supported by this engine build")

// ExecFunc executes an engine CLI command (`args` starting with `"owner"`).
type ExecFunc func(ctx context.Context, args ...string) (stdout []byte, stderr string, exit int, err error)

// ClientOptions configures the owner contract v1 Client.
type ClientOptions struct {
	DataDir        string
	TasksDir       string
	SettingsPath   string
	Home           string
	TempDir        string
	Getenv         func(string) string
	Engine         EngineSource
	NoPythonRepair bool
	LookPath       func(string) (string, error)
	RunCommand     CommandFunc
	DaemonURL      func() string
	Exec           ExecFunc
	ReadTimeout    time.Duration
	ActionTimeout  time.Duration
}

// AsksFilter selects options for `owner asks [--project <p>] [--open] --json`.
type AsksFilter struct {
	Project string
	Open    bool
}

// DigestFilter selects options for `owner digest [--project <p>] [--since <ms|iso>] --json`.
type DigestFilter struct {
	Project string
	Since   string
}

// AnswerRequest is the input to `owner answer <ask_id> --option <opt> [--proof <id>] --json`.
type AnswerRequest struct {
	AskID  string
	Option string
	Proof  string
}

// ProposeRuleRequest is the input to `owner propose-rule ... --json`.
type ProposeRuleRequest struct {
	Scope   string
	Project string
	Type    RuleType
	Value   any
	Text    string
	Section string
	Until   string
}

// Client invokes `engine owner ... --json` over the unpacked factory engine and
// uv-managed CPython, validating every response against owner contract v1.
type Client struct {
	opts           ClientOptions
	customStore    bool
	customSettings bool

	mu       sync.Mutex
	prepared bool
	eng      Engine
	py       ManagedPython
}

// NewClient constructs an owner contract v1 Client.
func NewClient(opts ClientOptions) *Client {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	opts.Getenv = getenv
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}
	envDataDir, envSettings := defaultFactoryLocations(opts.Home, getenv)
	if opts.DataDir == "" {
		opts.DataDir = envDataDir
	}
	tasksDir, settingsPath, customStore, customSettings := ResolveFactoryPaths(opts.DataDir, opts.Home, getenv)
	if strings.TrimSpace(opts.TasksDir) != "" {
		tasksDir = filepath.Clean(opts.TasksDir)
		customStore = !sameCleanPath(tasksDir, filepath.Join(envDataDir, "tasks"))
	}
	if strings.TrimSpace(opts.SettingsPath) != "" {
		settingsPath = filepath.Clean(opts.SettingsPath)
		customSettings = !sameCleanPath(settingsPath, envSettings)
	}
	opts.TasksDir = tasksDir
	opts.SettingsPath = settingsPath
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultReadTimeout
	}
	if opts.ActionTimeout <= 0 {
		opts.ActionTimeout = defaultActionTimeout
	}
	return &Client{
		opts:           opts,
		customStore:    customStore,
		customSettings: customSettings,
	}
}

// Active reports whether at least one factory project is started (or a test
// Exec hook is wired). Until a project is started, read calls stay off and
// unpack nothing.
func (c *Client) Active() bool {
	if c == nil {
		return false
	}
	if c.opts.Exec != nil {
		return true
	}
	return HasStartedProject(c.opts.TasksDir, c.opts.SettingsPath) || hasAskFiles(c.opts.TasksDir)
}

func hasAskFiles(tasksDir string) bool {
	if strings.TrimSpace(tasksDir) == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(tasksDir, "asks"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			return true
		}
	}
	return false
}

// Projects calls `owner projects --json` (§2.1).
func (c *Client) Projects(ctx context.Context) (ProjectsResponse, error) {
	if !c.Active() {
		return ProjectsResponse{Protocol: ProtocolVersion, Projects: []Project{}}, nil
	}
	raw, err := c.exec(ctx, c.opts.ReadTimeout, "owner", "projects", "--json")
	if err != nil {
		return ProjectsResponse{}, err
	}
	return DecodeProjectsResponse(raw)
}

// Asks calls `owner asks [--project <p>] [--open] --json` (§2.2).
func (c *Client) Asks(ctx context.Context, filter AsksFilter) (AsksResponse, error) {
	if !c.Active() {
		return AsksResponse{Protocol: ProtocolVersion, Asks: []Ask{}}, nil
	}
	if c.opts.Exec == nil && !hasAskFiles(c.opts.TasksDir) {
		return AsksResponse{Protocol: ProtocolVersion, Asks: []Ask{}}, nil
	}
	args := []string{"owner", "asks"}
	if p := strings.TrimSpace(filter.Project); p != "" {
		args = append(args, "--project", p)
	}
	if filter.Open {
		args = append(args, "--open")
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ReadTimeout, args...)
	if err != nil {
		return AsksResponse{}, err
	}
	return DecodeAsksResponse(raw)
}

// Digest calls `owner digest [--project <p>] [--since <s>] --json` (§2.3).
func (c *Client) Digest(ctx context.Context, filter DigestFilter) (DigestResponse, error) {
	args := []string{"owner", "digest"}
	if p := strings.TrimSpace(filter.Project); p != "" {
		args = append(args, "--project", p)
	}
	if s := strings.TrimSpace(filter.Since); s != "" {
		args = append(args, "--since", s)
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ReadTimeout, args...)
	if err != nil {
		return DigestResponse{}, err
	}
	return DecodeDigestResponse(raw)
}

// Rules calls `owner rules [--project <p>] --json` (§2.4).
func (c *Client) Rules(ctx context.Context, project string) (RulesResponse, error) {
	if !c.Active() {
		return RulesResponse{Protocol: ProtocolVersion, Rules: []Rule{}}, nil
	}
	args := []string{"owner", "rules"}
	if p := strings.TrimSpace(project); p != "" {
		args = append(args, "--project", p)
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ReadTimeout, args...)
	if err != nil {
		return RulesResponse{}, err
	}
	return DecodeRulesResponse(raw)
}

// Graph calls `owner graph --project <p> --json` (§2.5, provisional until SDF-32).
func (c *Client) Graph(ctx context.Context, project string) (GraphResponse, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return GraphResponse{}, contractErr(CodeNotFound, ErrContractNotFound, "project is required")
	}
	raw, err := c.exec(ctx, c.opts.ReadTimeout, "owner", "graph", "--project", project, "--json")
	if err != nil {
		return GraphResponse{}, err
	}
	return DecodeGraphResponse(raw)
}

// Alerts calls `owner alerts [--since <s|ms>] --json` (§6.3).
func (c *Client) Alerts(ctx context.Context, since string) (AlertsResponse, error) {
	if !c.Active() {
		return AlertsResponse{Protocol: ProtocolVersion, Alerts: []Alert{}}, nil
	}
	args := []string{"owner", "alerts"}
	if s := strings.TrimSpace(since); s != "" {
		args = append(args, "--since", s)
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ReadTimeout, args...)
	if err != nil {
		return AlertsResponse{}, err
	}
	return DecodeAlertsResponse(raw)
}

// Answer calls `owner answer <ask_id> --option <opt> [--proof <id>] --json` (§3.1).
func (c *Client) Answer(ctx context.Context, req AnswerRequest) (AnswerResponse, error) {
	askID := strings.TrimSpace(req.AskID)
	opt := strings.TrimSpace(req.Option)
	if askID == "" {
		return AnswerResponse{}, contractErr(CodeNotFound, ErrContractNotFound, "ask_id is empty")
	}
	if opt == "" {
		return AnswerResponse{}, contractErr(CodeInvalidOption, ErrInvalidOption, "option is empty")
	}
	args := []string{"owner", "answer", askID, "--option", opt}
	if proof := strings.TrimSpace(req.Proof); proof != "" {
		args = append(args, "--proof", proof)
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ActionTimeout, args...)
	if err != nil {
		return AnswerResponse{}, err
	}
	return DecodeAnswerResponse(raw)
}

// AnswerIndex formats a 1-based option index and calls Answer.
func (c *Client) AnswerIndex(ctx context.Context, askID string, optionIndex int, proofID string) (AnswerResponse, error) {
	if optionIndex < 1 || optionIndex > 9 {
		return AnswerResponse{}, contractErr(CodeInvalidOption, ErrInvalidOption, "option index %d out of range", optionIndex)
	}
	return c.Answer(ctx, AnswerRequest{
		AskID:  askID,
		Option: strconv.Itoa(optionIndex),
		Proof:  proofID,
	})
}

// Say calls `owner say <project> <text> --json` (§3.2).
func (c *Client) Say(ctx context.Context, project, text string) (SayResponse, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return SayResponse{}, contractErr(CodeNotFound, ErrContractNotFound, "project is empty")
	}
	if strings.TrimSpace(text) == "" {
		return SayResponse{}, contractErr(CodeInvalidAsk, ErrInvalidAsk, "message text is empty")
	}
	raw, err := c.exec(ctx, c.opts.ActionTimeout, "owner", "say", project, text, "--json")
	if err != nil {
		return SayResponse{}, err
	}
	return DecodeSayResponse(raw)
}

// ProposeRule calls `owner propose-rule ... --json` (§3.3).
func (c *Client) ProposeRule(ctx context.Context, req ProposeRuleRequest) (ProposeRuleResponse, error) {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		return ProposeRuleResponse{}, contractErr(CodeInvalidRule, ErrInvalidRule, "scope is empty")
	}
	args := []string{"owner", "propose-rule", "--scope", scope}
	if p := strings.TrimSpace(req.Project); p != "" {
		args = append(args, "--project", p)
	}
	if strings.TrimSpace(string(req.Type)) != "" {
		valJSON, err := json.Marshal(req.Value)
		if err != nil {
			return ProposeRuleResponse{}, contractErr(CodeInvalidRule, ErrInvalidRule, "encode rule value: %v", err)
		}
		args = append(args, "--type", string(req.Type), "--value", string(valJSON))
	} else {
		args = append(args, "--text", req.Text)
		if sec := strings.TrimSpace(req.Section); sec != "" {
			args = append(args, "--section", sec)
		}
	}
	if u := strings.TrimSpace(req.Until); u != "" {
		args = append(args, "--until", u)
	}
	args = append(args, "--json")
	raw, err := c.exec(ctx, c.opts.ActionTimeout, args...)
	if err != nil {
		return ProposeRuleResponse{}, err
	}
	return DecodeProposeRuleResponse(raw)
}

// RevokeRule calls `owner revoke-rule <rule_id> --json` (§3.4).
func (c *Client) RevokeRule(ctx context.Context, ruleID string) (RevokeRuleResponse, error) {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return RevokeRuleResponse{}, contractErr(CodeNotFound, ErrContractNotFound, "rule_id is empty")
	}
	raw, err := c.exec(ctx, c.opts.ActionTimeout, "owner", "revoke-rule", ruleID, "--json")
	if err != nil {
		return RevokeRuleResponse{}, err
	}
	return DecodeRevokeRuleResponse(raw)
}

func (c *Client) exec(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		stdout []byte
		stderr string
		exit   int
		err    error
	)
	if c.opts.Exec != nil {
		stdout, stderr, exit, err = c.opts.Exec(runCtx, args...)
	} else {
		stdout, stderr, exit, err = c.execManaged(runCtx, args...)
	}

	trimmed := bytes.TrimSpace(stdout)
	if exit != 0 || err != nil {
		if len(trimmed) > 0 {
			var env ErrorEnvelope
			if jerr := json.Unmarshal(trimmed, &env); jerr == nil && !env.OK && env.Code != "" {
				return nil, &ContractError{
					Code:    env.Code,
					Message: env.Error,
					Err:     sentinelForCode(env.Code),
				}
			}
		}
		if strings.Contains(stderr, "invalid choice: 'owner'") {
			return nil, ErrOwnerUnsupported
		}
		if err != nil {
			return nil, fmt.Errorf("factory owner %s: %w", strings.Join(args, " "), err)
		}
		why := cleanReason(firstNonEmpty(stderr, string(trimmed), fmt.Sprintf("exit %d", exit)))
		return nil, fmt.Errorf("factory owner %s: %s", strings.Join(args, " "), why)
	}
	return trimmed, nil
}

func (c *Client) prepare(ctx context.Context) (Engine, ManagedPython, error) {
	c.mu.Lock()
	if c.prepared {
		eng, py := c.eng, c.py
		c.mu.Unlock()
		return eng, py, nil
	}
	c.mu.Unlock()

	var (
		eng Engine
		err error
	)
	if c.opts.NoPythonRepair {
		eng, err = InspectEngine(c.opts.DataDir, c.opts.Engine)
	} else {
		eng, err = EnsureEngine(c.opts.DataDir, c.opts.Engine)
	}
	if err != nil {
		return Engine{}, ManagedPython{}, err
	}
	py, err := EnsurePython(ctx, PythonOptions{
		DataDir:       c.opts.DataDir,
		PinnedVersion: eng.PythonVersion,
		Home:          c.opts.Home,
		TempDir:       c.opts.TempDir,
		NoRepair:      c.opts.NoPythonRepair,
		LookPath:      c.opts.LookPath,
		RunCommand:    c.opts.RunCommand,
	})
	if err != nil {
		return Engine{}, ManagedPython{}, err
	}

	c.mu.Lock()
	c.eng = eng
	c.py = py
	c.prepared = true
	c.mu.Unlock()
	return eng, py, nil
}

func (c *Client) childEnv(py ManagedPython, eng Engine) []string {
	base := EngineEnv(scrubChildEnv(os.Environ()), py, eng)
	set := func(k, v string) {
		if v == "" {
			return
		}
		base = append(base, k+"="+v)
	}
	if !c.customStore || !c.customSettings {
		for _, k := range []string{
			"SHEEPDOG_FACTORY_HOME", "SHEEPDOG_HOME", "SHEPHERDR_HOME",
			"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		} {
			set(k, strings.TrimSpace(c.opts.Getenv(k)))
		}
	}
	if c.customStore {
		set("HERDR_TASK_STORE", c.opts.TasksDir)
		if t := strings.TrimSpace(c.opts.Getenv("HERDR_TASK_TEMPLATES")); t != "" {
			set("HERDR_TASK_TEMPLATES", t)
		} else {
			set("HERDR_TASK_TEMPLATES", eng.TemplatesDir)
		}
	} else if t := strings.TrimSpace(c.opts.Getenv("HERDR_TASK_TEMPLATES")); t != "" {
		set("HERDR_TASK_TEMPLATES", t)
	}
	if c.customSettings {
		set("HERDR_TASK_SETTINGS", c.opts.SettingsPath)
	}
	if c.opts.DaemonURL != nil {
		set("SHEEPDOG_DAEMON_URL", strings.TrimSpace(c.opts.DaemonURL()))
	}
	return base
}

func (c *Client) execManaged(ctx context.Context, args ...string) ([]byte, string, int, error) {
	eng, py, err := c.prepare(ctx)
	if err != nil {
		return nil, "", -1, err
	}
	cwd := existingDir(c.opts.Home, existingDir(c.opts.TempDir, "."))
	argv := make([]string, 0, 4+len(args))
	argv = append(argv, py.Executable, "-I", "-B", eng.HerdrTask)
	argv = append(argv, args...)

	execArgv := procprio.WrapArgv(argv)
	cmd := exec.Command(execArgv[0], execArgv[1:]...) //nolint:gosec // argv constructed directly without shell
	cmd.Dir = cwd
	cmd.Env = c.childEnv(py, eng)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = defaultWaitDelay

	var stdout limitedBuffer
	stdout.max = maxContractBytes
	var stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, "", -1, err
	}
	procprio.ApplyProcessGroup(cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		if cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return nil, stderr.String(), -1, ctx.Err()
	case waitErr := <-done:
		exit := -1
		if ps := cmd.ProcessState; ps != nil {
			exit = ps.ExitCode()
		}
		if stdout.overflow {
			return nil, stderr.String(), exit, fmt.Errorf("contract response exceeded %d bytes", maxContractBytes)
		}
		if waitErr != nil && exit < 0 && !errors.Is(waitErr, exec.ErrWaitDelay) {
			return stdout.buf.Bytes(), stderr.String(), exit, waitErr
		}
		return stdout.buf.Bytes(), stderr.String(), exit, nil
	}
}

type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	rem := b.max - b.buf.Len()
	if rem < len(p) {
		b.overflow = true
		if rem > 0 {
			_, _ = b.buf.Write(p[:rem])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}
