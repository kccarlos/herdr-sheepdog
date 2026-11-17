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
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/procprio"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

const (
	defaultVolumeTimeout = 5 * time.Second
	defaultAuthTimeout   = 10 * time.Second
	defaultCanaryTimeout = 30 * time.Second
	defaultWaitDelay     = 500 * time.Millisecond
	canaryPrompt         = "Reply with OK and nothing else."
	defaultClaudeCanary  = "haiku"
	maxCapturedBytes     = 8 << 10
)

// probeScript walks up to the nearest existing directory for $1 (without
// blocking the Go caller first), verifies it is a directory, and exercises
// chdir + lock-free git status / ls from inside an hc terminal.
const probeScript = `d="$1"; while [ ! -e "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do d=$(dirname "$d"); done; [ -d "$d" ] || exit 2; cd "$d" && (git --no-optional-locks status --porcelain --untracked-files=no >/dev/null 2>&1 || ls -a >/dev/null)`

// cleanExecScript strips daemon keys and parent Claude Code session markers
// inside herdr-core's machine.exec while keeping CLAUDE_CODE_* auth/provider
// settings (matching launch.py's CLEAN_ENV).
const cleanExecScript = `for v in $(env | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p'); do case "$v" in CLAUDE_CODE_USE_*|CLAUDE_CODE_SKIP_*|CLAUDE_CODE_DISABLE_*|CLAUDE_CODE_ENABLE_*|CLAUDE_CODE_MAX_*|CLAUDE_CODE_OAUTH_*|CLAUDE_CODE_CLIENT_*|CLAUDE_CODE_API_*) ;; HERDR_*|SHEEPDOG_*|GEMINI_API_KEY*|VERTEX_*|DEEPSEEK_API_KEY|OPENAI_ORCH_API_KEY|GH_REPO|CLAUDECODE|CLAUDE_PID|CLAUDE_CODE_*) unset "$v" ;; esac; done; exec "$@"`

// CommandOutput is the captured outcome of one child command run by Doctor.
type CommandOutput struct {
	Stdout   string
	Stderr   string
	Exit     int
	TimedOut bool
	Duration time.Duration
	Err      error
}

// CommandFunc runs argv in dir with extraEnv overlaid on a scrubbed environment.
type CommandFunc func(ctx context.Context, dir string, extraEnv []string, argv ...string) CommandOutput

// DoctorOptions configures Doctor. Zero values use the current machine's defaults.
type DoctorOptions struct {
	Folder        string
	HCSocket      string
	Canary        bool
	ClaudeModel   string
	GOOS          string
	Home          string
	TempDir       string
	GHHost        string
	LookPath      func(string) (string, error)
	RunCommand    CommandFunc
	VolumeTimeout time.Duration
	CanaryTimeout time.Duration
	AuthTimeout   time.Duration
}

func (o DoctorOptions) withDefaults() DoctorOptions {
	if o.Folder == "" {
		o.Folder = "."
	}
	o.Folder = filepath.Clean(o.Folder)
	if o.ClaudeModel == "" {
		o.ClaudeModel = defaultClaudeCanary
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	if o.HCSocket == "" {
		o.HCSocket = hc.DefaultSocket(os.Getenv, o.Home, os.Getuid(), nil)
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.RunCommand == nil {
		o.RunCommand = SystemRunCommand
	}
	if o.VolumeTimeout <= 0 {
		o.VolumeTimeout = defaultVolumeTimeout
	}
	if o.CanaryTimeout <= 0 {
		o.CanaryTimeout = defaultCanaryTimeout
	}
	if o.AuthTimeout <= 0 {
		o.AuthTimeout = defaultAuthTimeout
	}
	return o
}

func resOK(name, detail string) onboard.Result {
	return onboard.Result{Name: name, Status: onboard.OK, Detail: detail}
}

func resWarn(name, detail, fix string) onboard.Result {
	return onboard.Result{Name: name, Status: onboard.Warn, Detail: detail, Fix: fix}
}

func resFail(name, detail, fix string) onboard.Result {
	return onboard.Result{Name: name, Status: onboard.Fail, Detail: detail, Fix: fix}
}

func resSkip(name, detail string) onboard.Result {
	return onboard.Result{Name: name, Status: onboard.Skip, Detail: detail}
}

// Doctor runs the factory prerequisite checks in fixed order: herdr-core, the
// timed project-volume probe inside herdr-core, agent CLI logins + canary
// starts (executed inside herdr-core when it is up), gh login + pinned default
// repo, and codexbar.
func Doctor(ctx context.Context, opts DoctorOptions) []onboard.Result {
	customRunner := opts.RunCommand != nil
	o := opts.withDefaults()
	var out []onboard.Result

	hcRes := CheckHerdrCore(ctx, o)
	out = append(out, hcRes)
	hcOK := hcRes.Status == onboard.OK

	volRes := resSkip("project volume", "skipped until herdr-core is running")
	if hcOK {
		volRes = CheckProjectVolume(ctx, o)
	}
	out = append(out, volRes)

	volumeOK := volRes.Status == onboard.OK
	canaryOpt := o
	if hcOK && !customRunner {
		canaryOpt.RunCommand = hcExecRunner(o.HCSocket)
	}
	out = append(out, checkAgentCLIsWith(ctx, o, canaryOpt.RunCommand, volumeOK)...)
	out = append(out, CheckGH(ctx, o, volumeOK))
	out = append(out, CheckCodexbar(ctx, o))
	return out
}

// CheckHerdrCore verifies that `hc` is on PATH and herdr-core's socket answers.
func CheckHerdrCore(ctx context.Context, o DoctorOptions) onboard.Result {
	o = o.withDefaults()
	const name = "herdr-core"
	if _, err := o.LookPath("hc"); err != nil {
		return resFail(name, "hc is not on PATH", "install herdr-core from https://herdr.dev so hc is on PATH")
	}
	client := hc.NewClient(o.HCSocket)
	client.Timeout = o.AuthTimeout
	hello, err := client.Hello(ctx)
	if err != nil {
		return resFail(name, fmt.Sprintf("hc is on PATH, but socket %s does not answer", o.HCSocket),
			"run: hc server start (or set herdr.hc_socket / SHEEPDOG_HC_SOCKET)")
	}
	if ver := strings.TrimSpace(hello.Version); ver != "" {
		return resOK(name, fmt.Sprintf("hc on PATH; socket %s answers (v%s)", o.HCSocket, ver))
	}
	return resOK(name, fmt.Sprintf("hc on PATH; socket %s answers", o.HCSocket))
}

type probeOutcome struct {
	exitCode int
	timedOut bool
	duration time.Duration
	err      error
}

// CheckProjectVolume runs a timed directory/git probe from inside a real
// herdr-core terminal so macOS TCC prompts and external-SSD stalls (field
// notes 39–40) are caught before unattended runs start.
func CheckProjectVolume(ctx context.Context, o DoctorOptions) onboard.Result {
	o = o.withDefaults()
	const name = "project volume"
	client := hc.NewClient(o.HCSocket)
	client.Timeout = min(o.VolumeTimeout, defaultVolumeTimeout)

	res := probeFolderInHC(ctx, client, o.TempDir, o.Folder, o.VolumeTimeout)
	if res.err != nil {
		return resFail(name, fmt.Sprintf("could not probe inside herdr-core: %s", cleanReason(res.err.Error())),
			"run: hc server restart, then run sheepdog factory doctor again")
	}
	if !res.timedOut {
		switch res.exitCode {
		case 0:
			ms := res.duration.Milliseconds()
			targetDesc := o.Folder
			if probed := existingDir(o.Folder, ""); probed != "" && probed != o.Folder {
				targetDesc = fmt.Sprintf("%s (parent of %s)", probed, o.Folder)
			}
			if o.GOOS == "darwin" {
				if vol := externalVolume(o.Folder); vol != "" {
					return resOK(name, fmt.Sprintf("%s answered inside herdr-core (%dms; external volume %s — internal disk is safer overnight)", targetDesc, ms, vol))
				}
			}
			return resOK(name, fmt.Sprintf("%s answered inside herdr-core (%dms)", targetDesc, ms))
		case 2:
			return resFail(name, fmt.Sprintf("%s is not a directory", o.Folder), "pass a project folder path")
		default:
			if o.GOOS == "darwin" {
				prompt, fix := consentPrompt(o.Home, o.Folder)
				return resFail(name, fmt.Sprintf("herdr-core was denied access to %s (%s)", o.Folder, prompt), fix)
			}
			return resFail(name, fmt.Sprintf("herdr-core could not read %s (exit %d)", o.Folder, res.exitCode),
				fmt.Sprintf("check directory permissions and mount status for %s", o.Folder))
		}
	}

	// Folder timed out: probe TempDir to tell a volume stall / consent prompt
	// apart from herdr-core or the whole machine being wedged.
	tmpRes := probeFolderInHC(ctx, client, o.TempDir, o.TempDir, o.VolumeTimeout)
	if tmpRes.err == nil && !tmpRes.timedOut && tmpRes.exitCode == 0 {
		if o.GOOS == "darwin" {
			prompt, fix := consentPrompt(o.Home, o.Folder)
			return resFail(name,
				fmt.Sprintf("%s did not answer inside herdr-core within %s while %s did: disk stall or a %s permission prompt may be waiting", o.Folder, o.VolumeTimeout, o.TempDir, prompt),
				fmt.Sprintf("click Allow on the waiting %s prompt on this Mac, or %s", prompt, fix))
		}
		return resFail(name,
			fmt.Sprintf("%s did not answer inside herdr-core within %s while %s did: the disk or mount is stalling", o.Folder, o.VolumeTimeout, o.TempDir),
			fmt.Sprintf("check the disk or network mount backing %s", o.Folder))
	}
	return resFail(name,
		fmt.Sprintf("both %s and %s timed out inside herdr-core (%s)", o.Folder, o.TempDir, o.VolumeTimeout),
		"restart herdr-core (hc server restart) and check system load")
}

func probeFolderInHC(ctx context.Context, client *hc.Client, tempDir, target string, timeout time.Duration) probeOutcome {
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	t, err := client.Run(probeCtx, hc.RunParams{
		Argv:     []string{"sh", "-c", probeScript, "sh", target},
		Cwd:      tempDir,
		NoParent: true,
		NoNotify: true,
	})
	if err != nil {
		if probeCtx.Err() != nil && ctx.Err() == nil {
			return probeOutcome{timedOut: true, duration: time.Since(start)}
		}
		return probeOutcome{err: err, duration: time.Since(start)}
	}
	defer cleanupHCTerminal(client, t.Address)

	if t.State == "finished" {
		return probeOutcome{exitCode: derefExit(t.ExitCode), duration: time.Since(start)}
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-probeCtx.Done():
			if ctx.Err() != nil {
				return probeOutcome{err: ctx.Err(), duration: time.Since(start)}
			}
			return probeOutcome{timedOut: true, duration: time.Since(start)}
		case <-ticker.C:
			cur, gerr := client.Get(probeCtx, t.Address)
			if gerr != nil {
				if errors.Is(gerr, hc.ErrNotFound) {
					return probeOutcome{err: gerr, duration: time.Since(start)}
				}
				if probeCtx.Err() != nil && ctx.Err() == nil {
					return probeOutcome{timedOut: true, duration: time.Since(start)}
				}
				continue
			}
			if cur.State == "finished" {
				return probeOutcome{exitCode: derefExit(cur.ExitCode), duration: time.Since(start)}
			}
		}
	}
}

func cleanupHCTerminal(client *hc.Client, address string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := &hc.Client{Socket: client.Socket, Timeout: 500 * time.Millisecond}
	if _, err := c.Remove(ctx, address); err == nil || errors.Is(err, hc.ErrNotFound) {
		return
	}
	_, _ = c.Kill(ctx, address)
	for range 10 {
		if _, err := c.Remove(ctx, address); err == nil || errors.Is(err, hc.ErrNotFound) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func derefExit(code *int) int {
	if code == nil {
		return -1
	}
	return *code
}

func externalVolume(path string) string {
	clean := filepath.Clean(path)
	const prefix = "/Volumes/"
	if !strings.HasPrefix(clean, prefix) {
		return ""
	}
	vol, _, _ := strings.Cut(clean[len(prefix):], "/")
	if vol == "" {
		return ""
	}
	return prefix + vol
}

func consentPrompt(home, target string) (prompt, fix string) {
	clean := filepath.Clean(target)
	if vol := externalVolume(clean); vol != "" {
		return fmt.Sprintf("macOS Removable Volumes (%s)", vol),
			"allow herdr-core in System Settings ▸ Privacy & Security ▸ Files and Folders ▸ Removable Volumes (or move the repo to the internal disk)"
	}
	if home != "" {
		cleanHome := filepath.Clean(home)
		for _, p := range []struct{ rel, label, pane string }{
			{"Documents", "macOS Documents folder", "Documents"},
			{"Desktop", "macOS Desktop folder", "Desktop"},
			{"Downloads", "macOS Downloads folder", "Downloads"},
			{filepath.Join("Library", "Mobile Documents"), "macOS iCloud Drive", "iCloud Drive"},
		} {
			root := filepath.Join(cleanHome, p.rel)
			if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
				return p.label, fmt.Sprintf("allow herdr-core in System Settings ▸ Privacy & Security ▸ Files and Folders ▸ %s (or Full Disk Access)", p.pane)
			}
		}
	}
	return "macOS Files and Folders",
		"allow herdr-core in System Settings ▸ Privacy & Security ▸ Files and Folders (or Full Disk Access)"
}

type cliProbeState int

const (
	cliMissing cliProbeState = iota
	cliSignedOut
	cliCanaryFailed
	cliCanaryTimedOut
	cliReady
)

type cliOutcome struct {
	name   string
	state  cliProbeState
	result onboard.Result
}

// CheckAgentCLIs checks claude, codex and agy in parallel and appends the
// `agent clis` gate: at least one CLI must be logged in and pass its canary
// start (unless o.Canary is false), and no logged-in CLI may hang on a prompt.
func CheckAgentCLIs(ctx context.Context, o DoctorOptions, volumeOK bool) []onboard.Result {
	o = o.withDefaults()
	return checkAgentCLIsWith(ctx, o, o.RunCommand, volumeOK)
}

func checkAgentCLIsWith(ctx context.Context, o DoctorOptions, canaryRun CommandFunc, volumeOK bool) []onboard.Result {
	canaryDir := o.TempDir
	if volumeOK {
		canaryDir = existingDir(o.Folder, o.TempDir)
	}

	checks := []func(context.Context, DoctorOptions, CommandFunc, string) cliOutcome{
		checkClaude,
		checkCodex,
		checkAgy,
	}
	outcomes := make([]cliOutcome, len(checks))
	var wg sync.WaitGroup
	for i, fn := range checks {
		wg.Add(1)
		go func(idx int, check func(context.Context, DoctorOptions, CommandFunc, string) cliOutcome) {
			defer wg.Done()
			outcomes[idx] = check(ctx, o, canaryRun, canaryDir)
		}(i, fn)
	}
	wg.Wait()

	var (
		out       = make([]onboard.Result, 0, len(outcomes)+1)
		ready     []string
		timedOut  []string
		installed int
		loggedIn  int
	)
	for _, oc := range outcomes {
		out = append(out, oc.result)
		if oc.state != cliMissing {
			installed++
		}
		if oc.state >= cliCanaryFailed {
			loggedIn++
		}
		if oc.state == cliCanaryTimedOut {
			timedOut = append(timedOut, oc.name)
		}
		if oc.state == cliReady {
			ready = append(ready, oc.name)
		}
	}

	switch {
	case len(timedOut) > 0:
		out = append(out, resFail("agent clis",
			fmt.Sprintf("canary start timed out for %s (a consent or first-run prompt may be waiting)", strings.Join(timedOut, ", ")),
			"resolve the timed-out CLI prompt above and run sheepdog factory doctor again"))
	case len(ready) > 0:
		out = append(out, resOK("agent clis", fmt.Sprintf("%d ready (%s)", len(ready), strings.Join(ready, ", "))))
	case installed == 0:
		out = append(out, resFail("agent clis", "no agent CLI (claude, codex, agy) found on PATH",
			"install claude, codex or agy and sign in (one CLI is enough)"))
	case loggedIn == 0:
		out = append(out, resFail("agent clis", "no agent CLI is logged in",
			"sign in to at least one CLI: claude auth login, codex login, or agy"))
	default:
		out = append(out, resFail("agent clis", "no logged-in agent CLI passed its canary start",
			"fix at least one agent CLI above and run sheepdog factory doctor again"))
	}
	return out
}

func checkClaude(ctx context.Context, o DoctorOptions, canaryRun CommandFunc, canaryDir string) cliOutcome {
	const name = "claude"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
	}
	env := []string{"DISABLE_AUTOUPDATER=1"}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, env, "claude", "auth", "status", "--json")
	cancel()
	if !claudeLoggedIn(authOut) {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run: claude auth login")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := canaryRun(cCtx, canaryDir, env,
		"claude", "-p", canaryPrompt,
		"--no-session-persistence",
		"--model", o.ClaudeModel,
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "user",
		"--settings", `{"hooks":{}}`,
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--output-format", "json",
		"--max-budget-usd", "0.05",
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if errMsg := claudeCanaryError(cOut); errMsg != "" {
		return cliOutcome{name: name, state: cliCanaryFailed, result: resWarn(name, "logged in, but canary start failed: "+errMsg, "run claude once in a terminal to check login and quota")}
	}
	return cliOutcome{name: name, state: cliReady, result: resOK(name, fmt.Sprintf("logged in; canary start ok (%dms)", cOut.Duration.Milliseconds()))}
}

func claudeLoggedIn(out CommandOutput) bool {
	if out.Err != nil || out.TimedOut {
		return false
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Stdout)), &status); err == nil {
		return status.LoggedIn
	}
	return out.Exit == 0
}

func claudeCanaryError(out CommandOutput) string {
	var parsed struct {
		IsError bool     `json:"is_error"`
		Subtype string   `json:"subtype"`
		Result  string   `json:"result"`
		Errors  []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Stdout)), &parsed); err == nil {
		if parsed.IsError || (parsed.Subtype != "" && parsed.Subtype != "success") {
			for _, e := range parsed.Errors {
				if e = strings.TrimSpace(e); e != "" {
					return cleanReason(e)
				}
			}
			if r := strings.TrimSpace(parsed.Result); r != "" {
				return cleanReason(r)
			}
			if parsed.Subtype != "" {
				return cleanReason(parsed.Subtype)
			}
		}
	}
	if out.Err != nil {
		return cleanReason(out.Err.Error())
	}
	if out.Exit != 0 {
		return cleanReason(firstNonEmpty(out.Stderr, out.Stdout, fmt.Sprintf("exit %d", out.Exit)))
	}
	return ""
}

func checkCodex(ctx context.Context, o DoctorOptions, canaryRun CommandFunc, canaryDir string) cliOutcome {
	const name = "codex"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "codex", "login", "status")
	cancel()
	combined := strings.ToLower(authOut.Stdout + "\n" + authOut.Stderr)
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 || strings.Contains(combined, "not logged in") {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run: codex login")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := canaryRun(cCtx, canaryDir, nil,
		"codex", "exec",
		"--ephemeral",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--disable", "hooks",
		"--color", "never",
		"-c", "model_reasoning_effort=low",
		"-C", canaryDir,
		"--json",
		canaryPrompt,
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if cOut.Err != nil || cOut.Exit != 0 {
		why := cleanReason(firstNonEmpty(codexErrorText(cOut.Stdout), cOut.Stderr, cOut.Stdout, fmt.Sprintf("exit %d", cOut.Exit)))
		return cliOutcome{name: name, state: cliCanaryFailed, result: resWarn(name, "logged in, but canary start failed: "+why, "run codex once in a terminal to check login and quota")}
	}
	return cliOutcome{name: name, state: cliReady, result: resOK(name, fmt.Sprintf("logged in; canary start ok (%dms)", cOut.Duration.Milliseconds()))}
}

func codexErrorText(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Error.Message != "" {
			return ev.Error.Message
		}
		if ev.Type == "error" && ev.Message != "" {
			return ev.Message
		}
	}
	return ""
}

var agyModelIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func checkAgy(ctx context.Context, o DoctorOptions, canaryRun CommandFunc, canaryDir string) cliOutcome {
	const name = "agy"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "agy", "models")
	cancel()
	models := parseAgyModels(authOut.Stdout)
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 || len(models) == 0 {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run agy without arguments and sign in")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	model := pickAgyCanaryModel(models)
	settingsBytes, _ := json.Marshal(map[string]any{
		"trustedWorkspaces": []string{canaryDir},
		"permissions":       map[string]any{"allow": []string{}},
	})
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := canaryRun(cCtx, canaryDir, nil,
		"agy",
		"--mode", "plan",
		"--disable-slash-commands",
		"--model", model,
		"--settings", string(settingsBytes),
		"--print-timeout", "25s",
		"-p="+canaryPrompt,
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if cOut.Err != nil || cOut.Exit != 0 {
		why := cleanReason(firstNonEmpty(cOut.Stderr, cOut.Stdout, fmt.Sprintf("exit %d", cOut.Exit)))
		return cliOutcome{name: name, state: cliCanaryFailed, result: resWarn(name, "logged in, but canary start failed: "+why, "run agy once in a terminal to check sign-in and quota")}
	}
	return cliOutcome{name: name, state: cliReady, result: resOK(name, fmt.Sprintf("logged in; canary start ok (%s, %dms)", model, cOut.Duration.Milliseconds()))}
}

func parseAgyModels(stdout string) []string {
	var models []string
	for _, line := range strings.Split(stdout, "\n") {
		if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
			line = line[idx+1:]
		}
		line = strings.TrimSpace(line)
		if line == "" || (!strings.Contains(line, "\t") && !strings.Contains(line, "  ")) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && agyModelIDRe.MatchString(fields[0]) {
			models = append(models, fields[0])
		}
	}
	return models
}

func pickAgyCanaryModel(models []string) string {
	for _, match := range []func(string) bool{
		func(m string) bool {
			return strings.HasPrefix(m, "gemini") && strings.Contains(m, "flash") && strings.HasSuffix(m, "-low")
		},
		func(m string) bool { return strings.HasPrefix(m, "gemini") && strings.Contains(m, "flash") },
		func(m string) bool { return strings.HasPrefix(m, "gemini") && strings.HasSuffix(m, "-low") },
		func(m string) bool { return strings.HasPrefix(m, "gemini") },
	} {
		for _, m := range models {
			if match(m) {
				return m
			}
		}
	}
	return models[0]
}

func canaryTimeoutResult(cliName string, o DoctorOptions, canaryDir string) onboard.Result {
	if o.GOOS == "darwin" {
		prompt, fix := consentPrompt(o.Home, canaryDir)
		return resFail(cliName,
			fmt.Sprintf("logged in, but canary start inside herdr-core timed out after %s (a %s prompt or first-run dialog may be waiting)", o.CanaryTimeout, prompt),
			fmt.Sprintf("click Allow on any waiting %s prompt on this Mac, or %s", prompt, fix))
	}
	return resFail(cliName,
		fmt.Sprintf("logged in, but canary start timed out after %s", o.CanaryTimeout),
		fmt.Sprintf("run %s once in %s and check network access", cliName, canaryDir))
}

// hcExecRunner runs a canary command inside herdr-core (machine.exec) so macOS
// attributes file-access consent prompts to herdr-core (as factory workers do)
// without opening a visible terminal pane.
func hcExecRunner(socket string) CommandFunc {
	return func(ctx context.Context, dir string, extraEnv []string, argv ...string) CommandOutput {
		start := time.Now()
		sec := defaultCanaryTimeout.Seconds()
		if dl, ok := ctx.Deadline(); ok {
			if rem := time.Until(dl).Seconds(); rem > 0 {
				sec = rem
			}
		}
		envMap := forwardedAuthEnv(os.Environ(), extraEnv)
		client := hc.NewClient(socket)
		res, err := client.Exec(ctx, hc.ExecParams{
			Argv:    append([]string{"sh", "-c", cleanExecScript, "sh"}, argv...),
			Cwd:     dir,
			Env:     envMap,
			Timeout: &sec,
		})
		dur := time.Since(start)
		if res.MS > 0 {
			dur = time.Duration(res.MS) * time.Millisecond
		}
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return CommandOutput{Exit: -1, TimedOut: true, Duration: dur}
			}
			return CommandOutput{Exit: -1, Duration: dur, Err: err}
		}
		exit := -1
		if res.Code != nil {
			exit = *res.Code
		}
		return CommandOutput{
			Stdout:   truncateBytes(res.Stdout, maxCapturedBytes),
			Stderr:   truncateBytes(res.Stderr, maxCapturedBytes),
			Exit:     exit,
			TimedOut: res.TimedOut,
			Duration: dur,
		}
	}
}

func forwardedAuthEnv(environ, extraEnv []string) map[string]string {
	m := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == "PATH" || k == "ANTHROPIC_API_KEY" || k == "OPENAI_API_KEY" || k == "CLAUDE_CONFIG_DIR" || k == "CODEX_HOME" || isClaudeAuthEnv(k) {
			m[k] = v
		}
	}
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// CheckGH checks that `gh` is on PATH, authenticated, and — when the target
// folder has a GitHub push remote — that `gh repo set-default` is pinned to
// that push remote so an `upstream` or `github` remote cannot redirect PRs.
func CheckGH(ctx context.Context, o DoctorOptions, volumeOK bool) onboard.Result {
	o = o.withDefaults()
	const name = "gh"
	if _, err := o.LookPath("gh"); err != nil {
		return resWarn(name, "gh is not on PATH (local-merge mode only)", "install GitHub CLI (https://cli.github.com) and run gh auth login")
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "gh", "auth", "status")
	cancel()
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 {
		return resWarn(name, "gh is not logged in (local-merge mode only)", "run: gh auth login")
	}
	if !volumeOK {
		return resOK(name, "logged in (repo pin check skipped until project volume answers)")
	}

	repoRoot := gitLine(ctx, o, o.Folder, "rev-parse", "--show-toplevel")
	if repoRoot == "" {
		return resWarn(name, fmt.Sprintf("logged in; %s is not a git repo yet (local-merge mode only)", o.Folder),
			"initialize git, add a GitHub origin remote and run gh repo set-default <owner/repo>")
	}

	remotes := strings.Fields(gitOut(ctx, o, o.Folder, "remote"))
	pushRemote := resolvePushRemote(ctx, o, o.Folder, remotes)
	slug := resolveRemoteSlug(ctx, o, o.Folder, pushRemote)
	if slug == "" {
		return resWarn(name, fmt.Sprintf("logged in; no GitHub push remote in %s (local-merge mode only)", repoRoot),
			fmt.Sprintf("add a GitHub origin remote and run in %s: gh repo set-default <owner/repo>", repoRoot))
	}

	vCtx, vCancel := context.WithTimeout(ctx, o.AuthTimeout)
	viewOut := o.RunCommand(vCtx, o.Folder, nil, "gh", "repo", "set-default", "--view")
	vCancel()
	combinedLower := strings.ToLower(viewOut.Stderr + "\n" + viewOut.Stdout)
	if viewOut.Err != nil || viewOut.TimedOut || (viewOut.Exit != 0 && !strings.Contains(combinedLower, "no default")) {
		why := cleanReason(firstNonEmpty(errText(viewOut.Err), viewOut.Stderr, viewOut.Stdout, "timed out"))
		return resWarn(name, fmt.Sprintf("logged in, but could not check default repo in %s: %s", repoRoot, why),
			fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug))
	}

	pinned := strings.TrimSpace(firstLine(viewOut.Stdout))
	hijackRemote := ambiguousGHRemote(remotes, pushRemote)

	switch {
	case pinned == "" && hijackRemote != "":
		return resFail(name,
			fmt.Sprintf("logged in, but default repo is not pinned while remote %s exists (gh would target %s instead of %s)", hijackRemote, hijackRemote, slug),
			fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug))
	case pinned == "":
		return resWarn(name, fmt.Sprintf("logged in, but default repo is not pinned for %s", slug),
			fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug))
	case !strings.EqualFold(pinned, slug):
		return resFail(name, fmt.Sprintf("logged in, but default repo is pinned to %s instead of push remote %s", pinned, slug),
			fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug))
	default:
		return resOK(name, fmt.Sprintf("logged in; default repo pinned to %s", pinned))
	}
}

// ambiguousGHRemote returns a higher-priority remote ("upstream" or "github")
// that gh's own remote order prefers over pushRemote when unpinned.
func ambiguousGHRemote(remotes []string, pushRemote string) string {
	for _, r := range []string{"upstream", "github"} {
		if r != pushRemote && slices.Contains(remotes, r) {
			return r
		}
	}
	return ""
}

func gitOut(ctx context.Context, o DoctorOptions, dir string, args ...string) string {
	gCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	defer cancel()
	res := o.RunCommand(gCtx, o.TempDir, nil, append([]string{"git", "-C", dir}, args...)...)
	if res.Err != nil || res.TimedOut || res.Exit != 0 {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

func gitLine(ctx context.Context, o DoctorOptions, dir string, args ...string) string {
	return firstLine(gitOut(ctx, o, dir, args...))
}

func resolvePushRemote(ctx context.Context, o DoctorOptions, dir string, remotes []string) string {
	if len(remotes) == 0 {
		return ""
	}
	br := gitLine(ctx, o, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	type cand struct {
		name          string
		allowUpstream bool
	}
	var cands []cand
	if br != "" {
		cands = append(cands, cand{gitLine(ctx, o, dir, "config", "--get", "branch."+br+".pushRemote"), true})
	}
	cands = append(cands, cand{gitLine(ctx, o, dir, "config", "--get", "remote.pushDefault"), true})
	if br != "" {
		cands = append(cands, cand{gitLine(ctx, o, dir, "config", "--get", "branch."+br+".remote"), false})
	}
	for _, c := range cands {
		if c.name != "" && slices.Contains(remotes, c.name) && (c.allowUpstream || c.name != "upstream") {
			return c.name
		}
	}
	if slices.Contains(remotes, "origin") {
		return "origin"
	}
	for _, r := range remotes {
		if r != "upstream" {
			return r
		}
	}
	return ""
}

func resolveRemoteSlug(ctx context.Context, o DoctorOptions, dir, remote string) string {
	if remote == "" {
		return ""
	}
	ghHost := strings.ToLower(strings.TrimSpace(o.GHHost))
	if pushURL := gitLine(ctx, o, dir, "config", "--get", "remote."+remote+".pushurl"); pushURL != "" {
		if slug := RepoFromURL(pushURL, ghHost); slug != "" {
			return slug
		}
	}
	return RepoFromURL(gitLine(ctx, o, dir, "config", "--get", "remote."+remote+".url"), ghHost)
}

var (
	schemeRemoteRe = regexp.MustCompile(`^(?:https?|ssh|git)://(?:[^/@]+@)?([^/:]+)(?::\d+)?/(.+)$`)
	scpRemoteRe    = regexp.MustCompile(`^(?:[^/@:]+@)?([A-Za-z0-9._-]{2,}):/?(.+)$`)
	slugPartRe     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// RepoFromURL extracts "owner/name" from a GitHub HTTPS, git://, ssh:// or
// SCP-style remote URL (including SSH host aliases).
func RepoFromURL(rawURL, ghHost string) string {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return ""
	}
	isSSH := !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "git://")
	var m []string
	if strings.Contains(u, "://") {
		m = schemeRemoteRe.FindStringSubmatch(u)
	} else {
		m = scpRemoteRe.FindStringSubmatch(u)
	}
	if m == nil {
		return ""
	}
	host := strings.ToLower(m[1])
	path := strings.TrimSuffix(strings.Trim(m[2], "/"), ".git")
	ghHost = strings.ToLower(strings.TrimSpace(ghHost))
	if isSSH {
		for _, other := range []string{"gitlab", "bitbucket", "codeberg", "sr.ht", "sourcehut"} {
			if strings.Contains(host, other) {
				return ""
			}
		}
	} else if !strings.Contains(host, "github") && (ghHost == "" || host != ghHost) {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return ""
	}
	for _, p := range parts {
		if p == "." || p == ".." || !slugPartRe.MatchString(p) {
			return ""
		}
	}
	return path
}

// CheckCodexbar reports whether `codexbar` is on PATH for reading quota pools.
func CheckCodexbar(ctx context.Context, o DoctorOptions) onboard.Result {
	o = o.withDefaults()
	const name = "codexbar"
	if _, err := o.LookPath("codexbar"); err != nil {
		fix := "install codexbar and put it on PATH"
		if o.GOOS == "darwin" {
			fix = "install CodexBar (brew install --cask steipete/tap/codexbar) so codexbar is on PATH"
		}
		return resWarn(name, "codexbar is not on PATH (runs without quota facts; switches only on a provider error)", fix)
	}
	vCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	vOut := o.RunCommand(vCtx, o.TempDir, nil, "codexbar", "--version")
	cancel()
	if ver := strings.TrimSpace(firstLine(vOut.Stdout)); ver != "" && vOut.Exit == 0 {
		return resOK(name, fmt.Sprintf("on PATH (%s)", ver))
	}
	return resOK(name, "on PATH")
}

func existingDir(path, fallback string) string {
	cur := filepath.Clean(path)
	for {
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return fallback
		}
		cur = parent
	}
}

var redactor = redact.New()

func cleanReason(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	var picked string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			picked = ln
			break
		}
	}
	picked = redactor.String(picked)
	if r := []rune(picked); len(r) > 160 {
		picked = string(r[:160]) + "…"
	}
	return picked
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func truncateBytes(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			return ln
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// SystemRunCommand runs argv in its own process group at utility OS priority
// with daemon/session environment variables scrubbed, bounded pipe WaitDelay,
// and process-group SIGKILL if ctx expires.
func SystemRunCommand(ctx context.Context, dir string, extraEnv []string, argv ...string) CommandOutput {
	start := time.Now()
	if len(argv) == 0 {
		return CommandOutput{Exit: -1, Err: errors.New("empty argv")}
	}
	execArgv := procprio.WrapArgv(argv)
	cmd := exec.Command(execArgv[0], execArgv[1:]...) //nolint:gosec // argv is constructed directly without a shell
	cmd.Dir = dir
	cmd.Env = append(scrubChildEnv(os.Environ()), extraEnv...)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = defaultWaitDelay

	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return CommandOutput{Exit: -1, Duration: time.Since(start), Err: err}
	}
	procprio.ApplyProcessGroup(cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var (
		waitErr  error
		timedOut bool
	)
	select {
	case <-ctx.Done():
		timedOut = true
		if cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
	case waitErr = <-done:
	}

	out := CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Exit:     -1,
		TimedOut: timedOut,
		Duration: time.Since(start),
	}
	if ps := cmd.ProcessState; ps != nil {
		out.Exit = ps.ExitCode()
	}
	if !timedOut && waitErr != nil && out.Exit < 0 && !errors.Is(waitErr, exec.ErrWaitDelay) {
		out.Err = waitErr
	}
	return out
}

func isClaudeAuthEnv(key string) bool {
	for _, pfx := range []string{
		"CLAUDE_CODE_USE_",
		"CLAUDE_CODE_SKIP_",
		"CLAUDE_CODE_DISABLE_",
		"CLAUDE_CODE_ENABLE_",
		"CLAUDE_CODE_MAX_",
		"CLAUDE_CODE_OAUTH_",
		"CLAUDE_CODE_CLIENT_",
		"CLAUDE_CODE_API_",
	} {
		if strings.HasPrefix(key, pfx) {
			return true
		}
	}
	return false
}

func shouldScrubEnvKey(key string) bool {
	if isClaudeAuthEnv(key) {
		return false
	}
	return strings.HasPrefix(key, "HERDR_") ||
		key == "CLAUDECODE" || key == "CLAUDE_PID" || strings.HasPrefix(key, "CLAUDE_CODE_") ||
		strings.HasPrefix(key, "SHEEPDOG_") ||
		strings.HasPrefix(key, "GEMINI_API_KEY") ||
		strings.HasPrefix(key, "VERTEX_") ||
		key == "DEEPSEEK_API_KEY" ||
		key == "OPENAI_ORCH_API_KEY" ||
		key == "GH_REPO"
}

func scrubChildEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if shouldScrubEnvKey(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	rem := maxCapturedBytes - c.buf.Len()
	if rem > 0 {
		_, _ = c.buf.Write(p[:min(rem, len(p))])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
