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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
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
	probedDirStart       = "<SD_DIR>"
	probedDirEnd         = "</SD_DIR>"
)

// probeScript walks up to the nearest existing directory for $1 (without
// blocking the Go caller first), verifies it is a directory, reports the
// resolved directory back to Doctor without local os.Stat calls, and exercises
// chdir + lock-free git status / ls from inside an hc terminal.
const probeScript = `d="$1"; while [ ! -e "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do d=$(dirname "$d"); done; [ -d "$d" ] || exit 2; cd "$d" && case "$d" in /*) p="$d" ;; *) p="$PWD" ;; esac && printf '<SD_DIR>%s</SD_DIR>\n' "$p" && (git --no-optional-locks status --porcelain --untracked-files=no >/dev/null 2>&1 || ls -a >/dev/null)`

// cleanExecScript strips daemon keys and parent Claude Code session markers
// inside herdr-core's machine.exec while keeping CLAUDE_CODE_* auth/provider
// settings (matching launch.py's CLEAN_ENV), and exits 127 cleanly when $1 is
// missing from herdr-core's own PATH.
const cleanExecScript = `for v in $(env | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p'); do case "$v" in CLAUDE_CODE_USE_*|CLAUDE_CODE_SKIP_*|CLAUDE_CODE_DISABLE_*|CLAUDE_CODE_ENABLE_*|CLAUDE_CODE_MAX_*|CLAUDE_CODE_OAUTH_*|CLAUDE_CODE_CLIENT_*|CLAUDE_CODE_API_*) ;; HERDR_*|SHEEPDOG_*|GEMINI_API_KEY*|VERTEX_*|DEEPSEEK_API_KEY|OPENAI_ORCH_API_KEY|GH_REPO|CLAUDECODE|CLAUDE_PID|CLAUDE_CODE_*) unset "$v" ;; esac; done; command -v "$1" >/dev/null 2>&1 || exit 127; exec "$@"`

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
	DataDir       string
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
// starts, gh login + origin validation, and codexbar. When herdr-core is up,
// child probes execute inside herdr-core's own environment without copying the
// caller shell's PATH or credentials.
func Doctor(ctx context.Context, opts DoctorOptions) []onboard.Result {
	customRunner := opts.RunCommand != nil
	customLookPath := opts.LookPath != nil
	o := opts.withDefaults()
	var out []onboard.Result

	hcRes := CheckHerdrCore(ctx, o)
	out = append(out, hcRes)
	hcOK := hcRes.Status == onboard.OK

	volRes := resSkip("project volume", "skipped until herdr-core is running")
	var probedDir string
	if hcOK {
		volRes, probedDir = checkProjectVolume(ctx, o)
	}
	out = append(out, volRes)

	volumeOK := volRes.Status == onboard.OK
	inHC := hcOK && !customRunner
	if inHC {
		o.RunCommand = hcExecRunner(o.HCSocket)
	}
	out = append(out, checkAgentCLIs(ctx, o, inHC, customLookPath, volumeOK, probedDir)...)
	out = append(out, checkGH(ctx, o, inHC, customLookPath, volumeOK, probedDir))
	out = append(out, checkCodexbar(ctx, o, inHC, customLookPath))
	if volumeOK {
		out = append(out, checkSweepLeftovers(ctx, o, hcOK, firstNonEmpty(probedDir, o.Folder))...)
	}
	if hcOK && o.DataDir != "" {
		gctx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
		defer cancel()
		out = append(out, sweepProbeResults(world.NewGit(hc.NewClient(o.HCSocket), nil, nil).SweepCodexHome(gctx, Paths{DataDir: o.DataDir}.CodexHome()), "")...)
	}
	return out
}

func sweepProbeResults(pr flow.SweepProbe, stateWhy string) (out []onboard.Result) {
	if pr.Unknown != "" {
		why := map[bool]string{true: stateWhy, false: pr.Unknown}[stateWhy != "" && pr.Probe != "local_base"]
		out = append(out, resWarn("sweep "+pr.Probe, "unknown: "+cleanReason(why), "check "+pr.Probe+" manually"))
	}
	for _, it := range pr.Items {
		out = append(out, resWarn("leftover "+it.Kind, it.Summary, "inspect and clean up manually"))
	}
	return out
}

func checkSweepLeftovers(ctx context.Context, o DoctorOptions, hcOK bool, dir string) (out []onboard.Result) {
	gctx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	defer cancel()
	git := world.NewGit(hc.NewClient(o.HCSocket), nil, nil)
	repo, id, err := git.RepoProject(gctx, dir, false)
	if err != nil {
		if _, lookupErr := findStoredProject(Paths{DataDir: o.DataDir}, dir); lookupErr != nil {
			return nil
		}
		return []onboard.Result{resWarn("sweep terminals", "unknown: "+cleanReason(map[bool]string{false: "no hc client", true: err.Error()}[hcOK]), "check terminals manually")}
	}
	s, wtRoot, p, stateWhy, client := flow.State{}, "", Paths{DataDir: o.DataDir}, "", (*hc.Client)(nil)
	var findErr error
	if o.DataDir != "" && id == "" {
		id, findErr = findStoredProject(p, repo)
	}
	if id != "" && o.DataDir != "" {
		if loaded, lerr := LoadState(p.State(id)); lerr != nil {
			stateWhy = fmt.Sprintf("cannot load factory state for %s: %v", id, lerr)
		} else {
			s, s.Project.ID, wtRoot, stateWhy = loaded, id, filepath.Join(p.project(id), "worktrees"), ""
			if hcOK {
				client = hc.NewClient(o.HCSocket)
			}
		}
	} else if id != "" {
		stateWhy = "no data dir to load factory state for " + id
	} else if findErr != nil && !strings.HasPrefix(findErr.Error(), "no factory project for ") {
		out = append(out, resWarn("sweep", "unknown: "+cleanReason(findErr.Error()), "check leftovers manually"))
	} else {
		out = append(out, resSkip("sweep", "leftover checks skipped: this folder has no factory project yet"))
	}
	for _, pr := range git.Sweep(gctx, client, repo, wtRoot, s) {
		if id == "" && pr.Probe != "local_base" {
			continue
		}
		out = append(out, sweepProbeResults(pr, stateWhy)...)
	}
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
	exitCode  int
	probedDir string
	timedOut  bool
	duration  time.Duration
	err       error
}

// CheckProjectVolume runs a timed directory/git probe from inside a real
// herdr-core terminal so macOS TCC prompts and external-SSD stalls (field
// notes 39–40) are caught before unattended runs start.
func CheckProjectVolume(ctx context.Context, o DoctorOptions) onboard.Result {
	res, _ := checkProjectVolume(ctx, o)
	return res
}

func checkProjectVolume(ctx context.Context, o DoctorOptions) (onboard.Result, string) {
	o = o.withDefaults()
	const name = "project volume"
	client := hc.NewClient(o.HCSocket)
	client.Timeout = min(o.VolumeTimeout, defaultVolumeTimeout)

	res := probeFolderInHC(ctx, client, o.TempDir, o.Folder, o.VolumeTimeout)
	if res.err != nil {
		return resFail(name, fmt.Sprintf("could not probe inside herdr-core: %s", cleanReason(res.err.Error())),
			"run: hc server restart, then run sheepdog factory doctor again"), ""
	}
	if !res.timedOut {
		switch {
		case res.exitCode == 0:
			ms := res.duration.Milliseconds()
			probed := res.probedDir
			if probed == "" {
				probed = o.Folder
			}
			targetDesc := o.Folder
			if probed != o.Folder {
				targetDesc = fmt.Sprintf("%s (parent of %s)", probed, o.Folder)
			}
			if o.GOOS == "darwin" {
				if vol := externalVolume(o.Folder); vol != "" {
					return resOK(name, fmt.Sprintf("%s answered inside herdr-core (%dms; external volume %s — internal disk is safer overnight)", targetDesc, ms, vol)), probed
				}
			}
			return resOK(name, fmt.Sprintf("%s answered inside herdr-core (%dms)", targetDesc, ms)), probed
		case res.exitCode == 2:
			return resFail(name, fmt.Sprintf("%s is not a directory", o.Folder), "pass a project folder path"), ""
		case res.exitCode < 0:
			return resFail(name, fmt.Sprintf("probe for %s inside herdr-core was killed by a signal", o.Folder),
				"run: hc server restart, then run sheepdog factory doctor again"), ""
		default:
			if o.GOOS == "darwin" {
				prompt, fix := consentPrompt(o.Home, o.Folder)
				return resFail(name, fmt.Sprintf("herdr-core was denied access to %s (%s)", o.Folder, prompt), fix), ""
			}
			return resFail(name, fmt.Sprintf("herdr-core could not read %s (exit %d)", o.Folder, res.exitCode),
				fmt.Sprintf("check directory permissions and mount status for %s", o.Folder)), ""
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
				fmt.Sprintf("click Allow on the waiting %s prompt on this Mac, or %s", prompt, fix)), ""
		}
		return resFail(name,
			fmt.Sprintf("%s did not answer inside herdr-core within %s while %s did: the disk or mount is stalling", o.Folder, o.VolumeTimeout, o.TempDir),
			fmt.Sprintf("check the disk or network mount backing %s", o.Folder)), ""
	}
	return resFail(name,
		fmt.Sprintf("both %s and %s timed out inside herdr-core (%s)", o.Folder, o.TempDir, o.VolumeTimeout),
		"restart herdr-core (hc server restart) and check system load"), ""
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
		code := derefExit(t.ExitCode)
		return probeOutcome{
			exitCode:  code,
			probedDir: readProbedDir(probeCtx, client, t.Address, code),
			duration:  time.Since(start),
		}
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
				code := derefExit(cur.ExitCode)
				return probeOutcome{
					exitCode:  code,
					probedDir: readProbedDir(probeCtx, client, t.Address, code),
					duration:  time.Since(start),
				}
			}
		}
	}
}

func readProbedDir(ctx context.Context, client *hc.Client, address string, exitCode int) string {
	if exitCode != 0 {
		return ""
	}
	rr, err := client.Read(ctx, address, 20)
	if err != nil {
		return ""
	}
	i := strings.Index(rr.Text, probedDirStart)
	if i < 0 {
		return ""
	}
	rest := rr.Text[i+len(probedDirStart):]
	j := strings.Index(rest, probedDirEnd)
	if j < 0 {
		return ""
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(rest[:j], "\r", ""), "\n", "")
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
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
	cliNotInHCPath
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

type probeContext struct {
	inHC           bool
	customLookPath bool
	canaryDir      string
}

// CheckAgentCLIs checks claude, codex and agy in parallel and appends the
// `agent clis` gate: at least one CLI must be logged in and pass its canary
// start (unless o.Canary is false), and no logged-in CLI may hang on a prompt.
func CheckAgentCLIs(ctx context.Context, o DoctorOptions, volumeOK bool) []onboard.Result {
	customLookPath := o.LookPath != nil
	o = o.withDefaults()
	return checkAgentCLIs(ctx, o, false, customLookPath, volumeOK, "")
}

func checkAgentCLIs(ctx context.Context, o DoctorOptions, inHC, customLookPath, volumeOK bool, probedDir string) []onboard.Result {
	canaryDir := o.TempDir
	if volumeOK {
		if probedDir != "" {
			canaryDir = probedDir
		} else {
			canaryDir = existingDir(o.Folder, o.TempDir)
		}
	}
	pctx := probeContext{inHC: inHC, customLookPath: customLookPath, canaryDir: canaryDir}

	checks := []func(context.Context, DoctorOptions, probeContext) cliOutcome{
		checkClaude,
		checkCodex,
		checkAgy,
	}
	outcomes := make([]cliOutcome, len(checks))
	var wg sync.WaitGroup
	for i, fn := range checks {
		wg.Add(1)
		go func(idx int, check func(context.Context, DoctorOptions, probeContext) cliOutcome) {
			defer wg.Done()
			outcomes[idx] = check(ctx, o, pctx)
		}(i, fn)
	}
	wg.Wait()

	var (
		out         = make([]onboard.Result, 0, len(outcomes)+1)
		ready       []string
		timedOut    []string
		notInHCPath []string
		installed   int
		loggedIn    int
	)
	for _, oc := range outcomes {
		out = append(out, oc.result)
		if oc.state != cliMissing {
			installed++
		}
		if oc.state == cliNotInHCPath {
			notInHCPath = append(notInHCPath, oc.name)
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
	case loggedIn == 0 && len(notInHCPath) > 0:
		out = append(out, resFail("agent clis",
			fmt.Sprintf("agent CLI on PATH locally, but not on herdr-core's PATH (%s)", strings.Join(notInHCPath, ", ")),
			"run: hc server restart, then run sheepdog factory doctor again"))
	case loggedIn == 0:
		out = append(out, resFail("agent clis", "no agent CLI is logged in",
			"sign in to at least one CLI: claude auth login, codex login, or agy"))
	default:
		out = append(out, resFail("agent clis", "no logged-in agent CLI passed its canary start",
			"fix at least one agent CLI above and run sheepdog factory doctor again"))
	}
	return out
}

func handleHCPathStatus(name string, authOut CommandOutput, o DoctorOptions, pctx probeContext) (cliOutcome, bool) {
	if !pctx.inHC {
		return cliOutcome{}, false
	}
	if authOut.Exit == 127 {
		if _, err := o.LookPath(name); err == nil {
			return cliOutcome{
				name:   name,
				state:  cliNotInHCPath,
				result: resWarn(name, "on PATH locally, but not on herdr-core's PATH", "run: hc server restart"),
			}, true
		}
		return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}, true
	}
	if !pctx.customLookPath && (authOut.Err != nil || authOut.TimedOut) {
		if _, err := o.LookPath(name); err != nil {
			return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}, true
		}
	}
	return cliOutcome{}, false
}

func checkClaude(ctx context.Context, o DoctorOptions, pctx probeContext) cliOutcome {
	const name = "claude"
	if pctx.customLookPath || !pctx.inHC {
		if _, err := o.LookPath(name); err != nil {
			return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
		}
	}
	env := []string{"DISABLE_AUTOUPDATER=1"}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, env, "claude", "auth", "status", "--json")
	cancel()
	if oc, done := handleHCPathStatus(name, authOut, o, pctx); done {
		return oc
	}
	if !claudeLoggedIn(authOut) {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run: claude auth login")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, pctx.canaryDir, env,
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
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, pctx.canaryDir, pctx.inHC)}
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

func checkCodex(ctx context.Context, o DoctorOptions, pctx probeContext) cliOutcome {
	const name = "codex"
	if pctx.customLookPath || !pctx.inHC {
		if _, err := o.LookPath(name); err != nil {
			return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
		}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "codex", "login", "status")
	cancel()
	if oc, done := handleHCPathStatus(name, authOut, o, pctx); done {
		return oc
	}
	if !codexLoggedIn(authOut) {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run: codex login")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, pctx.canaryDir, nil,
		"codex", "exec",
		"--ephemeral",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--disable", "hooks",
		"--color", "never",
		"-c", "model_reasoning_effort=low",
		"-C", pctx.canaryDir,
		"--json",
		canaryPrompt,
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, pctx.canaryDir, pctx.inHC)}
	}
	if cOut.Err != nil || cOut.Exit != 0 {
		why := cleanReason(firstNonEmpty(codexErrorText(cOut.Stdout), cOut.Stderr, cOut.Stdout, fmt.Sprintf("exit %d", cOut.Exit)))
		return cliOutcome{name: name, state: cliCanaryFailed, result: resWarn(name, "logged in, but canary start failed: "+why, "run codex once in a terminal to check login and quota")}
	}
	return cliOutcome{name: name, state: cliReady, result: resOK(name, fmt.Sprintf("logged in; canary start ok (%dms)", cOut.Duration.Milliseconds()))}
}

func codexLoggedIn(c CommandOutput) bool {
	return cmdOK(c) && !strings.Contains(strings.ToLower(c.Stdout+"\n"+c.Stderr), "not logged in")
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

func checkAgy(ctx context.Context, o DoctorOptions, pctx probeContext) cliOutcome {
	const name = "agy"
	if pctx.customLookPath || !pctx.inHC {
		if _, err := o.LookPath(name); err != nil {
			return cliOutcome{name: name, state: cliMissing, result: resSkip(name, "not on PATH")}
		}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "agy", "models")
	cancel()
	if oc, done := handleHCPathStatus(name, authOut, o, pctx); done {
		return oc
	}
	models := parseAgyModels(authOut.Stdout)
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 || len(models) == 0 {
		return cliOutcome{name: name, state: cliSignedOut, result: resWarn(name, "installed, not logged in", "run agy without arguments and sign in")}
	}
	if !o.Canary {
		return cliOutcome{name: name, state: cliReady, result: resOK(name, "logged in (canary skipped)")}
	}
	model := pickAgyCanaryModel(models)
	settingsBytes, _ := json.Marshal(map[string]any{
		"trustedWorkspaces": []string{pctx.canaryDir},
		"permissions":       map[string]any{"allow": []string{}},
	})
	cCtx, cCancel := context.WithTimeout(ctx, o.CanaryTimeout)
	defer cCancel()
	cOut := o.RunCommand(cCtx, pctx.canaryDir, nil,
		"agy",
		"--mode", "plan",
		"--disable-slash-commands",
		"--model", model,
		"--settings", string(settingsBytes),
		"--print-timeout", "25s",
		"-p="+canaryPrompt,
	)
	if cOut.TimedOut && ctx.Err() == nil {
		return cliOutcome{name: name, state: cliCanaryTimedOut, result: canaryTimeoutResult(name, o, pctx.canaryDir, pctx.inHC)}
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

func canaryTimeoutResult(cliName string, o DoctorOptions, canaryDir string, inHC bool) onboard.Result {
	where := ""
	if inHC {
		where = " inside herdr-core"
	}
	if o.GOOS == "darwin" {
		prompt, fix := consentPrompt(o.Home, canaryDir)
		if !inHC {
			fix = fmt.Sprintf("run %s once in %s", cliName, canaryDir)
		}
		return resFail(cliName,
			fmt.Sprintf("logged in, but canary start%s timed out after %s (a %s prompt or first-run dialog may be waiting)", where, o.CanaryTimeout, prompt),
			fmt.Sprintf("click Allow on any waiting %s prompt on this Mac, or %s", prompt, fix))
	}
	return resFail(cliName,
		fmt.Sprintf("logged in, but canary start%s timed out after %s", where, o.CanaryTimeout),
		fmt.Sprintf("run %s once in %s and check network access", cliName, canaryDir))
}

// hcExecRunner runs a child command inside herdr-core (machine.exec) using
// herdr-core's own PATH and credentials without opening a visible terminal
// pane, so macOS attributes file-access consent prompts to herdr-core and
// doctor catches missing PATH/auth entries that factory workers would hit.
func hcExecRunner(socket string) CommandFunc {
	client := hc.NewClient(socket)
	git := world.NewGit(client, nil, nil)
	return func(ctx context.Context, dir string, extraEnv []string, argv ...string) CommandOutput {
		if argv[0] == "git" || argv[0] == "gh" {
			args := argv[1:]
			if len(args) >= 2 && args[0] == "-C" {
				dir, args = args[1], args[2:]
			}
			run := git.Run
			if argv[0] == "gh" {
				run = git.RunGH
			}
			r, err := run(ctx, dir, args...)
			out := CommandOutput{Exit: -1, Stdout: string(r.Stdout), Stderr: string(r.Stderr), TimedOut: r.TimedOut, Err: err}
			var child *world.GitError
			if r.Code != nil && !r.Truncated && r.Signal == nil && !r.TimedOut && (!errors.As(err, &child) || child.RPC == nil) {
				out.Exit, out.Err = *r.Code, nil
			}
			return out
		}
		start := time.Now()
		sec := defaultCanaryTimeout.Seconds()
		if dl, ok := ctx.Deadline(); ok {
			if rem := time.Until(dl).Seconds(); rem > 0 {
				sec = rem
			}
		}
		res, err := client.Exec(ctx, hc.ExecParams{
			Argv:    append([]string{"sh", "-c", cleanExecScript, "sh"}, argv...),
			Cwd:     dir,
			Env:     extraEnvMap(extraEnv),
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

func extraEnvMap(extraEnv []string) map[string]string {
	if len(extraEnv) == 0 {
		return nil
	}
	m := make(map[string]string, len(extraEnv))
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// CheckGH checks gh authentication and the project origin.
func CheckGH(ctx context.Context, o DoctorOptions, volumeOK bool) onboard.Result {
	customLookPath := o.LookPath != nil
	o = o.withDefaults()
	return checkGH(ctx, o, false, customLookPath, volumeOK, "")
}

func checkGH(ctx context.Context, o DoctorOptions, inHC, customLookPath, volumeOK bool, probedDir string) onboard.Result {
	const name = "gh"
	missingRes := resWarn(name, "gh is not on PATH (local-merge mode only)", "install GitHub CLI (https://cli.github.com) and run gh auth login")
	if customLookPath || !inHC {
		if _, err := o.LookPath("gh"); err != nil {
			return missingRes
		}
	}
	authCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	authOut := o.RunCommand(authCtx, o.TempDir, nil, "gh", "auth", "status")
	cancel()
	if inHC && authOut.Exit == 127 {
		if _, err := o.LookPath("gh"); err == nil {
			return resWarn(name, "gh is on PATH locally, but not on herdr-core's PATH (local-merge mode only)", "run: hc server restart")
		}
		return missingRes
	}
	if authOut.Err != nil || authOut.TimedOut || authOut.Exit != 0 {
		if inHC && !customLookPath && (authOut.Err != nil || authOut.TimedOut) {
			if _, err := o.LookPath("gh"); err != nil {
				return missingRes
			}
		}
		return resWarn(name, "gh is not logged in (local-merge mode only)", "run: gh auth login")
	}
	if !volumeOK {
		return resOK(name, "logged in (checking origin requires a reachable project volume)")
	}

	gitDir := o.Folder
	if probedDir != "" {
		gitDir = probedDir
	}
	repoRoot := gitLine(ctx, o, gitDir, "rev-parse", "--show-toplevel")
	if repoRoot == "" {
		return resWarn(name, fmt.Sprintf("logged in; %s is not a git repo yet (local-merge mode only)", o.Folder),
			"initialize git and add a GitHub origin remote")
	}

	slug, err := readGitHubOrigin(ctx, o, repoRoot)
	if err != nil {
		return resWarn(name, "logged in; "+cleanReason(err.Error()), originFix(err))
	}
	return resOK(name, "logged in; factory GitHub calls target "+slug)
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

func readGitHubOrigin(ctx context.Context, o DoctorOptions, dir string) (string, error) {
	return world.GitHubOrigin(func(args ...string) (string, error) {
		c := o.run(ctx, o.TempDir, append([]string{"git", "-C", dir}, args...)...)
		switch {
		case cmdOK(c):
			return strings.TrimSpace(c.Stdout), nil
		case c.TimedOut:
			return "", fmt.Errorf("reading origin: %w", context.DeadlineExceeded)
		case c.Err != nil:
			return "", c.Err
		case c.Exit == 2:
			return "", nil // git remote get-url's missing-remote exit
		default:
			return "", fmt.Errorf("reading origin: %s", firstNonEmpty(c.Stderr, "git failed"))
		}
	})
}

func originFix(err error) string {
	switch {
	case errors.Is(err, world.ErrNoOrigin):
		return "add one: git remote add origin https://github.com/<owner>/<repo>.git"
	case errors.Is(err, world.ErrOriginMismatch):
		return "set origin's fetch and push URLs to the same GitHub repository and remove any extra URLs"
	default:
		return "run git remote get-url --all origin and git remote get-url --push --all origin in the project folder and resolve the error"
	}
}

// CheckCodexbar reports whether `codexbar` is on PATH for reading quota pools.
func CheckCodexbar(ctx context.Context, o DoctorOptions) onboard.Result {
	customLookPath := o.LookPath != nil
	o = o.withDefaults()
	return checkCodexbar(ctx, o, false, customLookPath)
}

func checkCodexbar(ctx context.Context, o DoctorOptions, inHC, customLookPath bool) onboard.Result {
	const name = "codexbar"
	missingFix := "install codexbar and put it on PATH"
	if o.GOOS == "darwin" {
		missingFix = "install CodexBar (brew install --cask steipete/tap/codexbar) so codexbar is on PATH"
	}
	missingRes := resWarn(name, "codexbar is not on PATH (runs without quota facts; switches only on a provider error)", missingFix)
	if customLookPath || !inHC {
		if _, err := o.LookPath("codexbar"); err != nil {
			return missingRes
		}
	}
	vCtx, cancel := context.WithTimeout(ctx, o.AuthTimeout)
	vOut := o.RunCommand(vCtx, o.TempDir, nil, "codexbar", "--version")
	cancel()
	if inHC && vOut.Exit == 127 {
		if _, err := o.LookPath("codexbar"); err == nil {
			return resWarn(name, "codexbar is on PATH locally, but not on herdr-core's PATH (runs without quota facts)", "run: hc server restart")
		}
		return missingRes
	}
	if inHC && !customLookPath && (vOut.Err != nil || vOut.TimedOut) {
		if _, err := o.LookPath("codexbar"); err != nil {
			return missingRes
		}
		return resWarn(name, "could not check codexbar inside herdr-core", "run: hc server restart")
	}
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
