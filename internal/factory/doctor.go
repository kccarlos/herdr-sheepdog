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
	canaryPrompt         = "Reply with OK and nothing else."
	defaultClaudeCanary  = "claude-haiku-4-5-20251001"
	maxCapturedBytes     = 8 << 10
)

// probeScript walks up to the nearest existing directory for $1 (without
// blocking the Go caller first), verifies it is a directory, and exercises
// chdir + git status / ls from inside an hc terminal.
const probeScript = `d="$1"; while [ ! -e "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do d=$(dirname "$d"); done; [ -d "$d" ] || exit 2; cd "$d" && (git status --porcelain >/dev/null 2>&1 || ls -a >/dev/null)`

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
	// Folder is the target project folder (default ".").
	Folder string
	// HCSocket is herdr-core's Unix socket path.
	HCSocket string
	// Canary runs a tiny read-only turn on each logged-in agent CLI.
	Canary bool
	// GOOS is the operating system ("darwin", "linux"; default runtime.GOOS).
	GOOS string
	// Home is the user's home directory (default os.UserHomeDir).
	Home string
	// TempDir is a scratch folder on the system disk used as initial cwd for
	// probes and fallback checks when Folder does not answer (default os.TempDir).
	TempDir string
	// GHHost is GH_HOST when set, for recognising enterprise GitHub remotes.
	GHHost string
	// LookPath resolves executables on PATH (default exec.LookPath).
	LookPath func(string) (string, error)
	// RunCommand executes external CLI commands (default SystemRunCommand).
	RunCommand CommandFunc
	// VolumeTimeout bounds each volume probe inside herdr-core (default 5s).
	VolumeTimeout time.Duration
	// CanaryTimeout bounds each agent CLI canary start (default 30s).
	CanaryTimeout time.Duration
	// AuthTimeout bounds fast CLI status checks (default 10s).
	AuthTimeout time.Duration
}

func (o DoctorOptions) withDefaults() DoctorOptions {
	if o.Folder == "" {
		o.Folder = "."
	}
	o.Folder = filepath.Clean(o.Folder)
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

// Doctor runs the factory prerequisite checks in fixed order: herdr-core, the
// timed project-volume probe inside herdr-core, agent CLI logins + canary
// starts, gh login + pinned default repo, and codexbar.
func Doctor(ctx context.Context, opts DoctorOptions) []onboard.Result {
	o := opts.withDefaults()
	var out []onboard.Result

	hcRes := CheckHerdrCore(ctx, o)
	out = append(out, hcRes)

	volRes := onboard.Result{
		Name:   "project volume",
		Status: onboard.Skip,
		Detail: "skipped until herdr-core is running",
	}
	if hcRes.Status == onboard.OK {
		volRes = CheckProjectVolume(ctx, o)
	}
	out = append(out, volRes)

	volumeOK := volRes.Status == onboard.OK
	out = append(out, CheckAgentCLIs(ctx, o, volumeOK)...)
	out = append(out, CheckGH(ctx, o, volumeOK))
	out = append(out, CheckCodexbar(ctx, o))
	return out
}

// CheckHerdrCore verifies that `hc` is on PATH and herdr-core's socket answers.
func CheckHerdrCore(ctx context.Context, o DoctorOptions) onboard.Result {
	o = o.withDefaults()
	const name = "herdr-core"
	if _, err := o.LookPath("hc"); err != nil {
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: "hc is not on PATH",
			Fix:    "install herdr-core from https://herdr.dev so hc is on PATH",
		}
	}
	client := hc.NewClient(o.HCSocket)
	client.Timeout = o.AuthTimeout
	hello, err := client.Hello(ctx)
	if err != nil {
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("hc is on PATH, but socket %s does not answer", o.HCSocket),
			Fix:    "run: hc server start (or set herdr.hc_socket / SHEEPDOG_HC_SOCKET)",
		}
	}
	ver := strings.TrimSpace(hello.Version)
	if ver != "" {
		return onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: fmt.Sprintf("hc on PATH; socket %s answers (v%s)", o.HCSocket, ver),
		}
	}
	return onboard.Result{
		Name:   name,
		Status: onboard.OK,
		Detail: fmt.Sprintf("hc on PATH; socket %s answers", o.HCSocket),
	}
}

// probeOutcome is what one timed terminal probe inside herdr-core observed.
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
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("could not start probe inside herdr-core: %s", cleanReason(res.err.Error())),
			Fix:    "run: hc server restart, then run sheepdog factory doctor again",
		}
	}
	if !res.timedOut {
		switch res.exitCode {
		case 0:
			ms := res.duration.Milliseconds()
			if o.GOOS == "darwin" {
				if vol := externalVolume(o.Folder); vol != "" {
					return onboard.Result{
						Name:   name,
						Status: onboard.OK,
						Detail: fmt.Sprintf("%s answered inside herdr-core (%dms; external volume %s — internal disk is safer overnight)", o.Folder, ms, vol),
					}
				}
			}
			return onboard.Result{
				Name:   name,
				Status: onboard.OK,
				Detail: fmt.Sprintf("%s answered inside herdr-core (%dms)", o.Folder, ms),
			}
		case 2:
			return onboard.Result{
				Name:   name,
				Status: onboard.Fail,
				Detail: fmt.Sprintf("%s is not a directory", o.Folder),
				Fix:    "pass a project folder path",
			}
		default:
			if o.GOOS == "darwin" {
				prompt, fix := consentPrompt(o.Home, o.Folder)
				return onboard.Result{
					Name:   name,
					Status: onboard.Fail,
					Detail: fmt.Sprintf("herdr-core was denied access to %s (%s)", o.Folder, prompt),
					Fix:    fix,
				}
			}
			return onboard.Result{
				Name:   name,
				Status: onboard.Fail,
				Detail: fmt.Sprintf("herdr-core could not read %s (exit %d)", o.Folder, res.exitCode),
				Fix:    fmt.Sprintf("check directory permissions and mount status for %s", o.Folder),
			}
		}
	}

	// Folder timed out: probe TempDir to tell a volume stall / consent prompt
	// apart from herdr-core or the whole machine being wedged.
	tmpRes := probeFolderInHC(ctx, client, o.TempDir, o.TempDir, o.VolumeTimeout)
	if tmpRes.err == nil && !tmpRes.timedOut && tmpRes.exitCode == 0 {
		if o.GOOS == "darwin" {
			prompt, fix := consentPrompt(o.Home, o.Folder)
			return onboard.Result{
				Name:   name,
				Status: onboard.Fail,
				Detail: fmt.Sprintf("%s did not answer inside herdr-core within %s while %s did: disk stall or a %s permission prompt may be waiting", o.Folder, o.VolumeTimeout, o.TempDir, prompt),
				Fix:    fmt.Sprintf("click Allow on the waiting %s prompt on this Mac, or %s", prompt, fix),
			}
		}
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("%s did not answer inside herdr-core within %s while %s did: the disk or mount is stalling", o.Folder, o.VolumeTimeout, o.TempDir),
			Fix:    fmt.Sprintf("check the disk or network mount backing %s", o.Folder),
		}
	}
	return onboard.Result{
		Name:   name,
		Status: onboard.Fail,
		Detail: fmt.Sprintf("both %s and %s timed out inside herdr-core (%s)", o.Folder, o.TempDir, o.VolumeTimeout),
		Fix:    "restart herdr-core (hc server restart) and check system load",
	}
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
					return probeOutcome{exitCode: 0, duration: time.Since(start)}
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

// cleanupHCTerminal kills (if still running) and removes a probe terminal so
// doctor leaves nothing in `hc ls --all`.
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
		return 0
	}
	return *code
}

// externalVolume returns "/Volumes/<Name>" when path is on a mounted volume
// under /Volumes, else "".
func externalVolume(path string) string {
	clean := filepath.Clean(path)
	const prefix = "/Volumes/"
	if !strings.HasPrefix(clean, prefix) {
		return ""
	}
	rest := clean[len(prefix):]
	vol, _, _ := strings.Cut(rest, "/")
	if vol == "" {
		return ""
	}
	return prefix + vol
}

// consentPrompt names the specific macOS Privacy & Security consent prompt
// that guards target and returns its one-line fix.
func consentPrompt(home, target string) (prompt, fix string) {
	clean := filepath.Clean(target)
	if vol := externalVolume(clean); vol != "" {
		return fmt.Sprintf("macOS Removable Volumes (%s)", vol),
			"allow herdr-core in System Settings ▸ Privacy & Security ▸ Files and Folders ▸ Removable Volumes (or move the repo to the internal disk)"
	}
	if home != "" {
		cleanHome := filepath.Clean(home)
		protected := []struct {
			rel   string
			label string
			pane  string
		}{
			{"Documents", "macOS Documents folder", "Documents"},
			{"Desktop", "macOS Desktop folder", "Desktop"},
			{"Downloads", "macOS Downloads folder", "Downloads"},
			{filepath.Join("Library", "Mobile Documents"), "macOS iCloud Drive", "iCloud Drive"},
		}
		for _, p := range protected {
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
	cliReady
)

type cliOutcome struct {
	name   string
	state  cliProbeState
	result onboard.Result
}

// CheckAgentCLIs checks claude, codex and agy in parallel and appends the
// `agent clis` gate: at least one CLI must be logged in and pass its canary
// start (unless o.Canary is false).
func CheckAgentCLIs(ctx context.Context, o DoctorOptions, volumeOK bool) []onboard.Result {
	o = o.withDefaults()
	canaryDir := o.TempDir
	if volumeOK {
		canaryDir = existingDir(o.Folder, o.TempDir)
	}

	checks := []func(context.Context, DoctorOptions, string) cliOutcome{
		checkClaude,
		checkCodex,
		checkAgy,
	}
	outcomes := make([]cliOutcome, len(checks))
	var wg sync.WaitGroup
	for i, fn := range checks {
		wg.Add(1)
		go func(idx int, check func(context.Context, DoctorOptions, string) cliOutcome) {
			defer wg.Done()
			outcomes[idx] = check(ctx, o, canaryDir)
		}(i, fn)
	}
	wg.Wait()

	var (
		out       = make([]onboard.Result, 0, len(outcomes)+1)
		ready     []string
		installed int
		loggedIn  int
	)
	for _, oc := range outcomes {
		out = append(out, oc.result)
		if oc.state != cliMissing {
			installed++
		}
		if oc.state == cliCanaryFailed || oc.state == cliReady {
			loggedIn++
		}
		if oc.state == cliReady {
			ready = append(ready, oc.name)
		}
	}

	if len(ready) > 0 {
		out = append(out, onboard.Result{
			Name:   "agent clis",
			Status: onboard.OK,
			Detail: fmt.Sprintf("%d ready (%s)", len(ready), strings.Join(ready, ", ")),
		})
		return out
	}

	switch {
	case installed == 0:
		out = append(out, onboard.Result{
			Name:   "agent clis",
			Status: onboard.Fail,
			Detail: "no agent CLI (claude, codex, agy) found on PATH",
			Fix:    "install claude, codex or agy and sign in (one CLI is enough)",
		})
	case loggedIn == 0:
		out = append(out, onboard.Result{
			Name:   "agent clis",
			Status: onboard.Fail,
			Detail: "no agent CLI is logged in",
			Fix:    "sign in to at least one CLI: claude auth login, codex login, or agy",
		})
	default:
		out = append(out, onboard.Result{
			Name:   "agent clis",
			Status: onboard.Fail,
			Detail: "no logged-in agent CLI passed its canary start",
			Fix:    "fix at least one agent CLI above and run sheepdog factory doctor again",
		})
	}
	return out
}

func checkClaude(ctx context.Context, o DoctorOptions, canaryDir string) cliOutcome {
	const name = "claude"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: onboard.Result{Name: name, Status: onboard.Skip, Detail: "not on PATH"}}
	}
	env := []string{"DISABLE_AUTOUPDATER=1"}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, env, "claude", "auth", "status", "--json")
	cancel()
	if !claudeLoggedIn(authOut) {
		return cliOutcome{
			name:  name,
			state: cliSignedOut,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "installed, not logged in",
				Fix:    "run: claude auth login",
			},
		}
	}
	if !o.Canary {
		return cliOutcome{
			name:   name,
			state:  cliReady,
			result: onboard.Result{Name: name, Status: onboard.OK, Detail: "logged in (canary skipped)"},
		}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, canaryDir, env,
		"claude", "-p", canaryPrompt,
		"--no-session-persistence",
		"--model", defaultClaudeCanary,
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
		return cliOutcome{name: name, state: cliCanaryFailed, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if errMsg := claudeCanaryError(cOut); errMsg != "" {
		return cliOutcome{
			name:  name,
			state: cliCanaryFailed,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "logged in, but canary start failed: " + errMsg,
				Fix:    "run claude once in a terminal to check login and quota",
			},
		}
	}
	return cliOutcome{
		name:  name,
		state: cliReady,
		result: onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: fmt.Sprintf("logged in; canary start ok (%dms)", cOut.Duration.Milliseconds()),
		},
	}
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

func checkCodex(ctx context.Context, o DoctorOptions, canaryDir string) cliOutcome {
	const name = "codex"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: onboard.Result{Name: name, Status: onboard.Skip, Detail: "not on PATH"}}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "codex", "login", "status")
	cancel()
	combined := strings.ToLower(authOut.Stdout + "\n" + authOut.Stderr)
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 || strings.Contains(combined, "not logged in") {
		return cliOutcome{
			name:  name,
			state: cliSignedOut,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "installed, not logged in",
				Fix:    "run: codex login",
			},
		}
	}
	if !o.Canary {
		return cliOutcome{
			name:   name,
			state:  cliReady,
			result: onboard.Result{Name: name, Status: onboard.OK, Detail: "logged in (canary skipped)"},
		}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, canaryDir, nil,
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
		return cliOutcome{name: name, state: cliCanaryFailed, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if cOut.Err != nil || cOut.Exit != 0 {
		why := cleanReason(firstNonEmpty(codexErrorText(cOut.Stdout), cOut.Stderr, cOut.Stdout, fmt.Sprintf("exit %d", cOut.Exit)))
		return cliOutcome{
			name:  name,
			state: cliCanaryFailed,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "logged in, but canary start failed: " + why,
				Fix:    "run codex once in a terminal to check login and quota",
			},
		}
	}
	return cliOutcome{
		name:  name,
		state: cliReady,
		result: onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: fmt.Sprintf("logged in; canary start ok (%dms)", cOut.Duration.Milliseconds()),
		},
	}
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

func checkAgy(ctx context.Context, o DoctorOptions, canaryDir string) cliOutcome {
	const name = "agy"
	if _, err := o.LookPath(name); err != nil {
		return cliOutcome{name: name, state: cliMissing, result: onboard.Result{Name: name, Status: onboard.Skip, Detail: "not on PATH"}}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "agy", "models")
	cancel()
	models := parseAgyModels(authOut.Stdout)
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 || len(models) == 0 {
		return cliOutcome{
			name:  name,
			state: cliSignedOut,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "installed, not logged in",
				Fix:    "run agy without arguments and sign in",
			},
		}
	}
	if !o.Canary {
		return cliOutcome{
			name:   name,
			state:  cliReady,
			result: onboard.Result{Name: name, Status: onboard.OK, Detail: "logged in (canary skipped)"},
		}
	}
	model := pickAgyCanaryModel(models)
	settingsBytes, _ := json.Marshal(map[string]any{
		"trustedWorkspaces": []string{canaryDir},
		"permissions":       map[string]any{"allow": []string{}},
	})
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, canaryDir, nil,
		"agy",
		"--mode", "plan",
		"--disable-slash-commands",
		"--model", model,
		"--settings", string(settingsBytes),
		"--print-timeout", "25s",
		"-p", canaryPrompt,
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryFailed, result: canaryTimeoutResult(name, o, canaryDir)}
	}
	if cOut.Err != nil || cOut.Exit != 0 {
		why := cleanReason(firstNonEmpty(cOut.Stderr, cOut.Stdout, fmt.Sprintf("exit %d", cOut.Exit)))
		return cliOutcome{
			name:  name,
			state: cliCanaryFailed,
			result: onboard.Result{
				Name:   name,
				Status: onboard.Warn,
				Detail: "logged in, but canary start failed: " + why,
				Fix:    "run agy once in a terminal to check sign-in and quota",
			},
		}
	}
	return cliOutcome{
		name:  name,
		state: cliReady,
		result: onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: fmt.Sprintf("logged in; canary start ok (%s, %dms)", model, cOut.Duration.Milliseconds()),
		},
	}
}

// parseAgyModels extracts model ids from `agy models` stdout, stripping any
// leading carriage-return progress spinner frames on the first line.
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

// pickAgyCanaryModel chooses the lightest native Gemini model listed by `agy
// models` so the canary avoids 3P Claude/GPT quota pools on Antigravity and
// adapts automatically when model versions advance.
func pickAgyCanaryModel(models []string) string {
	matchers := []func(string) bool{
		func(m string) bool {
			return strings.HasPrefix(m, "gemini") && strings.Contains(m, "flash") && strings.HasSuffix(m, "-low")
		},
		func(m string) bool { return strings.HasPrefix(m, "gemini") && strings.Contains(m, "flash") },
		func(m string) bool { return strings.HasPrefix(m, "gemini") && strings.HasSuffix(m, "-low") },
		func(m string) bool { return strings.HasPrefix(m, "gemini") },
	}
	for _, match := range matchers {
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
		prompt, _ := consentPrompt(o.Home, canaryDir)
		return onboard.Result{
			Name:   cliName,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("logged in, but canary start timed out after %s (a %s prompt or first-run dialog may be waiting)", o.CanaryTimeout, prompt),
			Fix:    fmt.Sprintf("click Allow on any macOS prompt for %s, or run %s once in %s", cliName, cliName, canaryDir),
		}
	}
	return onboard.Result{
		Name:   cliName,
		Status: onboard.Fail,
		Detail: fmt.Sprintf("logged in, but canary start timed out after %s", o.CanaryTimeout),
		Fix:    fmt.Sprintf("run %s once in %s and check network access", cliName, canaryDir),
	}
}

// CheckGH checks that `gh` is on PATH, authenticated, and — when the target
// folder has a GitHub push remote — that `gh repo set-default` is pinned to
// that push remote so an `upstream` remote cannot redirect PRs (field note 33).
func CheckGH(ctx context.Context, o DoctorOptions, volumeOK bool) onboard.Result {
	o = o.withDefaults()
	const name = "gh"
	if _, err := o.LookPath("gh"); err != nil {
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: "gh is not on PATH (local-merge mode only)",
			Fix:    "install GitHub CLI (https://cli.github.com) and run gh auth login",
		}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "gh", "auth", "status")
	cancel()
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 {
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: "gh is not logged in (local-merge mode only)",
			Fix:    "run: gh auth login",
		}
	}
	if !volumeOK {
		return onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: "logged in (repo pin check skipped until project volume answers)",
		}
	}

	repoRoot := gitLine(ctx, o, o.Folder, "rev-parse", "--show-toplevel")
	if repoRoot == "" {
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: fmt.Sprintf("logged in; %s is not a git repo yet (local-merge mode only)", o.Folder),
			Fix:    "initialize git, add a GitHub origin remote and run gh repo set-default <owner/repo>",
		}
	}

	remotesRaw := gitOut(ctx, o, o.Folder, "remote")
	remotes := strings.Fields(remotesRaw)
	pushRemote := resolvePushRemote(ctx, o, o.Folder, remotes)
	slug := resolveRemoteSlug(ctx, o, o.Folder, pushRemote)
	if slug == "" {
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: fmt.Sprintf("logged in; no GitHub push remote in %s (local-merge mode only)", repoRoot),
			Fix:    fmt.Sprintf("add a GitHub origin remote and run in %s: gh repo set-default <owner/repo>", repoRoot),
		}
	}

	vCtx, vCancel := context.WithTimeout(ctx, o.AuthTimeout)
	viewOut := o.RunCommand(vCtx, o.Folder, nil, "gh", "repo", "set-default", "--view")
	vCancel()
	pinned := strings.TrimSpace(firstLine(viewOut.Stdout))
	hasUpstream := slices.Contains(remotes, "upstream") && pushRemote != "upstream"

	switch {
	case pinned == "" && hasUpstream:
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("logged in, but default repo is not pinned while remote upstream exists (gh would target upstream instead of %s)", slug),
			Fix:    fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug),
		}
	case pinned == "":
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: fmt.Sprintf("logged in, but default repo is not pinned for %s", slug),
			Fix:    fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug),
		}
	case !strings.EqualFold(pinned, slug):
		return onboard.Result{
			Name:   name,
			Status: onboard.Fail,
			Detail: fmt.Sprintf("logged in, but default repo is pinned to %s instead of push remote %s", pinned, slug),
			Fix:    fmt.Sprintf("run in %s: gh repo set-default %s", repoRoot, slug),
		}
	default:
		return onboard.Result{
			Name:   name,
			Status: onboard.OK,
			Detail: fmt.Sprintf("logged in; default repo pinned to %s", pinned),
		}
	}
}

func gitOut(ctx context.Context, o DoctorOptions, dir string, args ...string) string {
	gCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	defer cancel()
	argv := append([]string{"git", "-C", dir}, args...)
	res := o.RunCommand(gCtx, o.TempDir, nil, argv...)
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
// SCP-style remote URL (including SSH host aliases such as git@github-work:o/r).
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
		return onboard.Result{
			Name:   name,
			Status: onboard.Warn,
			Detail: "codexbar is not on PATH (runs without quota facts; switches only on a provider error)",
			Fix:    fix,
		}
	}
	vCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	vOut := o.RunCommand(vCtx, o.TempDir, nil, "codexbar", "--version")
	cancel()
	if ver := strings.TrimSpace(firstLine(vOut.Stdout)); ver != "" && vOut.Exit == 0 {
		return onboard.Result{Name: name, Status: onboard.OK, Detail: fmt.Sprintf("on PATH (%s)", ver)}
	}
	return onboard.Result{Name: name, Status: onboard.OK, Detail: "on PATH"}
}

// existingDir returns path when it is an existing directory, else walks up to
// its nearest existing ancestor directory, falling back to fallback. Call only
// after CheckProjectVolume has confirmed the volume answers.
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
		ln = strings.TrimSpace(ln)
		if ln != "" {
			picked = ln
			break
		}
	}
	picked = redactor.String(picked)
	if len(picked) > 160 {
		picked = picked[:160] + "…"
	}
	return picked
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
// with daemon/session environment variables scrubbed, killing the whole
// process group if ctx expires.
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
	if !timedOut && waitErr != nil && out.Exit < 0 {
		out.Err = waitErr
	}
	return out
}

func scrubChildEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.HasPrefix(key, "HERDR_") ||
			key == "CLAUDECODE" || strings.HasPrefix(key, "CLAUDE_CODE_") ||
			strings.HasPrefix(key, "SHEEPDOG_") ||
			strings.HasPrefix(key, "GEMINI_API_KEY") ||
			strings.HasPrefix(key, "VERTEX_") ||
			key == "DEEPSEEK_API_KEY" ||
			key == "OPENAI_ORCH_API_KEY" ||
			key == "GH_REPO" {
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
