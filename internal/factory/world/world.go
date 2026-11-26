// Package world holds the engine's executors and observers over herdr-core and
// git. It makes no policy decisions (docs/dev/engine-v2/design.md 9.4, 13).
package world

import (
	"bytes"
	"cmp"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// CodedError wraps an executor error with its section 8 failure class.
type CodedError struct {
	Class string
	Err   error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

// Fail tags err with class unless err is nil or already tagged.
func Fail(class string, err error) error {
	var ce *CodedError
	if err == nil || errors.As(err, &ce) && ce.Class != "" {
		return err
	}
	return &CodedError{Class: class, Err: err}
}

func hcFail(err error) error { return Fail(FailureClass(err), err) }

var httpSettledRe = regexp.MustCompile(`\bHTTP(?:/\d(?:\.\d)?)? (?:401|404|422)\b`)

// FailureClass returns err's section 8 failure class from structured facts:
// explicit Fail tag, context deadline, process exit code, or gh/hc status.
func FailureClass(err error) string {
	var ce *CodedError
	var he *hc.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ce) && ce.Class != "":
		return ce.Class
	case errors.Is(err, context.DeadlineExceeded):
		return flow.ClassTimeout
	case errors.As(err, &he) && (he.Code == -32600 || he.Code == -32601 || he.Code == -32602 || he.Code == 401 || he.Code == 404 || he.Code == 422):
		return flow.ClassSettled
	case gitExit(err, 126), gitExit(err, 127), httpSettledRe.MatchString(err.Error()):
		return flow.ClassSettled
	}
	return flow.ClassTransient
}

// RunSpec describes one terminal run to launch in herdr-core.
type RunSpec struct {
	Name, Cwd, Prompt, ClaudeConfig string
	Argv                            []string
	Labels, Env                     map[string]string // Env holds secrets: never argv
}

// ClaudeArgv returns the launch argv for Claude Code coordinator and worker
// runs, stating model, effort and permission mode explicitly, skipping the
// bypass confirmation dialog in bypass modes, and disabling the auto-updater
// (KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11). Reviewers use ClaudeReviewerArgv.
func ClaudeArgv(role, model, effort, permissionMode string) []string {
	model, effort, permissionMode = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(permissionMode)
	if (role != flow.RoleCoordinator && role != flow.RoleWorker) ||
		model == "" || effort == "" || permissionMode == "" {
		return nil
	}
	argv := []string{"env", "DISABLE_AUTOUPDATER=1", "claude", "--model", model, "--effort", effort, "--permission-mode", permissionMode}
	if permissionMode == "bypassPermissions" {
		argv = append(argv, "--settings", `{"skipDangerousModePermissionPrompt":true}`)
	}
	return argv
}

func claudeReadOnlyArgv(model, effort, bin, subcmd, findingsPath string) []string {
	model, effort, bin, findingsPath = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(bin), strings.TrimSpace(findingsPath)
	if model == "" || effort == "" || findingsPath == "" || !filepath.IsAbs(findingsPath) {
		return nil
	}
	trimmed := strings.TrimPrefix(filepath.Clean(findingsPath), "/")
	if trimmed == "" {
		return nil
	}
	bin = cmp.Or(bin, "sheepdog")
	tools := reviewerTools + " Bash(" + bin + " factory report " + subcmd + ":*) Edit(//" + trimmed + ")"
	return []string{"env", "DISABLE_AUTOUPDATER=1", "claude", "--model", model, "--effort", effort, "--permission-mode", "dontAsk", "--allowedTools=" + tools}
}

// ClaudeReviewerArgv returns the launch argv for a Claude Code reviewer with
// the read-only base tools plus Bash(<bin> factory report review:*) and
// Edit(//<findingsPath>) for that round's scratch findings file (design.md 9.2,
// spike S-REV-2, KB-REV-4).
func ClaudeReviewerArgv(model, effort, bin, findingsPath string) []string {
	return claudeReadOnlyArgv(model, effort, bin, "review", findingsPath)
}

// ClaudeRetroArgv returns the launch argv for a Claude Code retro run with the
// read-only base tools plus Bash(<bin> factory report retro:*) and
// Edit(//<findingsPath>) for that round's scratch findings file (design.md 9.2,
// KB-RET-12, KB-RET-15).
func ClaudeRetroArgv(model, effort, bin, findingsPath string) []string {
	return claudeReadOnlyArgv(model, effort, bin, "retro", findingsPath)
}

// ClaudePlanReviewerArgv returns the launch argv for a Claude Code plan reviewer (design.md 9.2).
func ClaudePlanReviewerArgv(model, effort, bin, findingsPath string) []string {
	return claudeReadOnlyArgv(model, effort, bin, "plan-review", findingsPath)
}

// NewUUID returns a random version-4 UUID for Claude session ids (spike S0.8).
func NewUUID() string {
	var b [16]byte
	_, _ = crand.Read(b[:])
	b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ClaudeSessionArgv returns ClaudeArgv with --session-id (fresh) or --resume,
// passing --model and --effort again on resume (KB-LCH-4, spike S0.8).
func ClaudeSessionArgv(role, model, effort, permissionMode, sessionID string, resume bool) []string {
	argv, sid := ClaudeArgv(role, model, effort, permissionMode), strings.TrimSpace(sessionID)
	if len(argv) == 0 || sid == "" {
		return argv
	} else if resume {
		return append(argv, "--resume", sid)
	}
	return append(argv, "--session-id", sid)
}

// reviewerTools is what a reviewer may use without asking: read the code and
// its history, and run the project's tests. No edit tools, but test runners
// run worker code and some git and go flags write files: read-only is a rule
// on the model, backed by the engine's clean-worktree check on the verdict.
const reviewerTools = "Read Grep Glob LS " +
	"Bash(git diff:*) Bash(git log:*) Bash(git show:*) Bash(git status:*) " +
	"Bash(go build:*) Bash(go test:*) Bash(go vet:*) Bash(gofmt -l:*) Bash(make test:*) " +
	"Bash(npm test:*) Bash(pytest:*) Bash(python3 -m pytest:*) Bash(python3 -m unittest:*)"

// StartTerminal launches spec in herdr-core after the caller resolves terminal ownership.
// The boolean reports whether beforeLaunch aborted the launch.
func StartTerminal(ctx context.Context, client *hc.Client, spec RunSpec, beforeLaunch func() error) (hc.Terminal, bool, error) {
	if client == nil || strings.TrimSpace(spec.Cwd) == "" || len(spec.Argv) == 0 {
		return hc.Terminal{}, false, Fail(flow.ClassSettled, errors.New("world: StartTerminal requires hc client, Cwd and Argv"))
	}
	if beforeLaunch != nil {
		if err := beforeLaunch(); err != nil {
			return hc.Terminal{}, true, err
		}
	}
	cli := ArgvCLI(spec.Argv)
	if cli == "claude" {
		if err := preAcceptClaudeTrust(spec.ClaudeConfig, spec.Cwd); err != nil {
			return hc.Terminal{}, false, Fail(flow.ClassSettled, err)
		}
	}
	argv := append([]string(nil), spec.Argv...)
	if spec.Prompt != "" {
		if cli == AgyCLI {
			argv = append(argv, "-i="+spec.Prompt)
		} else {
			argv = append(argv, spec.Prompt)
		}
	}
	env := spec.Env
	if cli == AgyCLI && len(env) > 0 {
		env = maps.Clone(env)
		maps.DeleteFunc(env, func(k, _ string) bool { return strings.Contains(k, "GEMINI_API_KEY") })
	}
	labels := map[string]string{"herdr.space": randID(), "herdr.tab": randID(), "herdr.seen": "0"}
	maps.Copy(labels, spec.Labels)
	t, err := client.Run(ctx, hc.RunParams{Name: spec.Name, Cwd: spec.Cwd, Argv: argv, Labels: labels, Env: env, NoParent: true, NoNotify: true})
	return t, false, hcFail(err)
}

func isClaudeArgv(argv []string) bool { return ArgvCLI(argv) == "claude" }

func defaultClaudeConfig() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	if home, _ := os.UserHomeDir(); home != "" {
		return filepath.Join(home, ".claude.json")
	}
	return ""
}

func preAcceptClaudeTrust(configPath, cwd string) error {
	path := cmp.Or(strings.TrimSpace(configPath), defaultClaudeConfig())
	if path == "" {
		return nil
	}
	if testing.Testing() {
		absCfg, err := filepath.Abs(path)
		if rel, rerr := filepath.Rel(os.TempDir(), absCfg); err != nil || rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("world: %s is outside the temp dir under go test", path)
		}
	}
	absCwd, err := absolutePath(cwd)
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // write through a symlink, never replace it
	}
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if !json.Valid(data) || dec.Decode(&root) != nil || root == nil {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return nil
	}
	projects, ok1 := childMap(root, "projects")
	entry, ok2 := childMap(projects, absCwd)
	if !ok1 || !ok2 {
		return nil
	}
	if entry["hasTrustDialogAccepted"] == true && root["hasCompletedOnboarding"] == true {
		return nil // already trusted: do not race live sessions that rewrite this file
	}
	entry["hasTrustDialogAccepted"] = true
	projects[absCwd] = entry
	root["projects"], root["hasCompletedOnboarding"] = projects, true
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude.json.tmp*")
	if err != nil {
		return nil
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(append(data, '\n')); err == nil && f.Chmod(0o600) == nil && f.Sync() == nil && f.Close() == nil {
		_ = os.Rename(f.Name(), path)
	} else {
		_ = f.Close()
	}
	return nil
}

func childMap(parent map[string]any, key string) (map[string]any, bool) {
	raw, ok := parent[key]
	if !ok || raw == nil {
		return map[string]any{}, true
	}
	m, ok := raw.(map[string]any)
	return m, ok
}

// SendRun pastes text into terminal id and submits it.
func SendRun(ctx context.Context, client *hc.Client, id, text string) error {
	if client == nil || id == "" {
		return Fail(flow.ClassSettled, errors.New("world: SendRun requires hc client and terminal id"))
	}
	_, err := client.Send(ctx, id, text, false)
	return hcFail(err)
}

// StopRun kills terminal id, removes it, and verifies both the terminal and its
// OS process group are gone (design.md 9.4).
func StopRun(ctx context.Context, client *hc.Client, id string) error {
	return StopLabeledRun(ctx, client, id, "", 0)
}

func matchesRunTerminal(t hc.Terminal, label string, createdMS uint64) bool {
	return (label == "" || t.Labels["sheepdog.run"] == label) && (createdMS == 0 || t.CreatedMS == createdMS)
}

// StopLabeledRun checks the run label and creation time before stopping a terminal.
func StopLabeledRun(ctx context.Context, client *hc.Client, id, label string, createdMS uint64) error {
	if client == nil || id == "" {
		return nil
	}
	t, err := client.Get(ctx, id)
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	} else if err != nil {
		return hcFail(err)
	}
	if !matchesRunTerminal(t, label, createdMS) {
		return nil
	}
	pid := 0
	if !hc.IsRemote(t.Address) && t.PID != nil && *t.PID > 1 && *t.PID != os.Getpid() && *t.PID != syscall.Getpgrp() {
		pid = *t.PID
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	if _, err := client.Kill(ctx, id); err != nil && !errors.Is(err, hc.ErrNotFound) && !errors.Is(err, hc.ErrFinished) {
		return hcFail(err)
	}
	for i := 0; i <= 80; i++ {
		if _, rerr := client.Remove(ctx, id); rerr != nil && !errors.Is(rerr, hc.ErrNotFound) && !errors.Is(rerr, hc.ErrLive) {
			return hcFail(rerr)
		}
		_, gerr := client.Get(ctx, id)
		procGone := pid == 0 || (syscall.Kill(pid, 0) != nil && syscall.Kill(-pid, 0) != nil)
		if errors.Is(gerr, hc.ErrNotFound) && procGone {
			return nil
		}
		if i == 4 && pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return Fail(flow.ClassTransient, fmt.Errorf("world: %s or pid %d still alive after stop", id, pid))
}

func deref[T any](p *T) (z T) {
	if p != nil {
		return *p
	}
	return z
}

const screenTailLines = 12

func matchUI(lines []string, spec, trim string) int {
	found := -1
	for _, pat := range strings.Split(spec, "|") {
		p, wild := strings.CutSuffix(pat, "*")
		for i, ln := range lines {
			s := strings.ToLower(strings.TrimLeft(ln, trim))
			if (wild && strings.HasPrefix(s, p)) || (!wild && s == pat) {
				found = max(found, i)
			}
		}
	}
	return found
}

// ScreenClass classifies the bottom UI region of text against cli's known
// dialog, sign-out and usage-limit screens (design.md 5, 9.2; spikes S0.2, S0.8).
func ScreenClass(cli, text string) string {
	cls, _ := screenClassify(cli, text)
	return cls
}

func uiAnchor(cli string, lines []string) int {
	if cli == AgyCLI {
		return agyInputAnchor(lines)
	}
	for i := len(lines) - 1; i >= max(0, len(lines)-screenTailLines); i-- {
		switch ln := lines[i]; {
		case cli == "claude" && (strings.HasPrefix(ln, "⏺") || strings.HasPrefix(ln, "●") || strings.HasPrefix(ln, "  ⎿")),
			cli == CodexCLI && (codexReplyRe.MatchString(ln) || strings.HasPrefix(ln, "  └")):
			return -1
		case cli == "claude" && i > 0 && strings.HasPrefix(ln, "❯") && isRuleLine(lines[i-1]):
			return i - 1
		case cli == CodexCLI && codexPromptRe.MatchString(ln):
			return i
		}
	}
	return -1
}

// limitUILines extracts only cli's own bottom UI area for usage-limit matching (O23).
func limitUILines(cli string, lines []string) (raw []string) {
	add := func(s, trim string) {
		if s = strings.TrimLeft(strings.TrimSpace(s), trim); s != "" {
			raw = append(raw, s)
		}
	}
	prefix, trim := "⚠", "⚠✕✗ "
	if cli == "claude" {
		prefix, trim = "⎿", "⎿⚠✕✗ "
	}
	if anc := uiAnchor(cli, lines); anc >= 0 {
		for i := anc - 1; i >= 0; i-- {
			ln := lines[i]
			if strings.TrimSpace(ln) == "" {
				continue
			}
			if cli == AgyCLI && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") && !isAgyBlockHeader(ln) && !isAgyPromptLine(ln) && !isRuleLine(ln) {
				add(ln, trim)
			} else if cli != AgyCLI && strings.HasPrefix(ln, prefix) {
				add(ln, prefix+" ")
			} else {
				break
			}
		}
		slices.Reverse(raw)
		for _, ln := range lines[anc+1:] {
			if s := strings.TrimSpace(ln); (cli != "claude" || !strings.HasPrefix(s, "❯") && strings.Trim(s, "─") != "") && (cli != AgyCLI || !isAgyPromptLine(s) && !isRuleLine(s)) {
				add(ln, trim)
			}
		}
		return raw
	}
	for _, ln := range lines {
		if cli == "claude" && (strings.HasPrefix(ln, "⏺") || strings.HasPrefix(ln, "●") || strings.HasPrefix(ln, "❯") || strings.HasPrefix(ln, "  ⎿")) ||
			cli == CodexCLI && (codexReplyRe.MatchString(ln) || codexPromptRe.MatchString(ln) || strings.HasPrefix(ln, "  └")) ||
			cli == AgyCLI && (isAgyBlockHeader(ln) || isAgyPromptLine(ln)) {
			return nil
		}
	}
	for _, ln := range lines[max(0, len(lines)-screenTailLines):] {
		if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			add(ln, trim)
		}
	}
	return raw
}

func screenChatTail(cli, clean string) string {
	lines := strings.Split(strings.TrimRight(clean, "\n \t"), "\n")
	if anc := uiAnchor(cli, lines); anc >= 0 {
		lines = lines[:anc]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return flow.CapRunes(strings.TrimSpace(strings.Join(lines[max(0, len(lines)-20):], "\n")), 2000)
}

func screenClassify(cli, text string) (string, string) {
	lines := strings.Split(strings.TrimRight(ansiRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), ""), "\n \t"), "\n")
	if cli == "agy" && agyTrustSelected(lines) {
		return flow.ScreenTrust, ""
	}
	var ui []string
	for _, ln := range lines[max(0, len(lines)-screenTailLines):] {
		if raw := strings.TrimLeft(strings.TrimSpace(ln), "⏺●⎿⚠✕✗•·│✨\u200a "); raw != "" {
			ui = append(ui, raw)
		}
	}
	limitUI := ui
	if cli == "claude" || cli == CodexCLI || cli == AgyCLI {
		limitUI = limitUILines(cli, lines)
	}
	for _, r := range [][4]string{
		{"claude", flow.ScreenSignedOut, "not logged in · please run /login*|invalid api key · please run /login*|not logged in|please run /login", ""},
		{"claude", flow.ScreenLimit, "you've hit your limit*|claude ai usage limit reached*", ""},
		{"claude", flow.ScreenTrust, "quick safety check: is this a project*|is this a project you created or one you trust?*", "yes, i trust this folder|1. yes, i trust this folder"},
		{"claude", flow.ScreenConfirm, "warning: claude code running in bypass permissions mode*|bypass permissions mode*", "1. no, exit|no, exit"},
		{CodexCLI, flow.ScreenSignedOut, "sign in with chatgpt*|not logged in", ""},
		{CodexCLI, flow.ScreenLimit, "you've hit your usage limit*|you've hit your limit*|you exceeded your current quota*", ""},
		{CodexCLI, flow.ScreenTrust, "do you trust the contents of this directory?*|trust this folder?*", "1. yes, continue|1. trust and continue"},
		{CodexCLI, flow.ScreenConfirm, "hooks need review*", "3. continue without trusting"},
		{CodexCLI, flow.ScreenConfirm, "update available!*", "2. skip"},
		{"agy", flow.ScreenSignedOut, "not signed in*|please sign in to*", ""},
		{"agy", flow.ScreenLimit, "individual quota reached*|you've hit your limit*|you've hit your usage limit*", ""},
	} {
		if r[0] == cli && r[1] == flow.ScreenLimit {
			if idx := matchUI(limitUI, r[2], ""); idx >= 0 {
				return r[1], limitUI[idx]
			}
		} else if r[0] == cli && matchUI(ui, r[2], "") >= 0 && (r[3] == "" || matchUI(ui, r[3], "❯› ") >= 0) {
			return r[1], ""
		}
	}
	return flow.ScreenNone, ""
}

var limitResetRe = regexp.MustCompile(`(?i)\b(?:resets?|try again)\s+(?:at\s+)?(?:([A-Z][a-z]{2})\s+(\d{1,2})(?:,|\s+at)?\s+)?(\d{1,2}(?::\d{2})?)\s*(am|pm)\b(?:\s*\(([A-Za-z0-9_/+.-]+)\))?`)

// ParseLimitReset parses the reset time printed inside a matched CLI usage-limit
// banner line relative to now; returns zero when the line prints no valid future time.
func ParseLimitReset(line string, now time.Time) time.Time {
	m := limitResetRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}
	}
	loc, err := now.Location(), error(nil)
	if m[5] != "" {
		loc, err = time.LoadLocation(m[5])
	}
	if err != nil {
		return time.Time{}
	}
	ref, clock := now.In(loc), m[3]
	if !strings.Contains(clock, ":") {
		clock += ":00"
	}
	monthDay := cmp.Or(m[1], ref.Format("Jan")) + " " + cmp.Or(m[2], ref.Format("2"))
	if cand, err := time.ParseInLocation("2006 Jan 2 3:04pm", fmt.Sprintf("%d %s %s%s", ref.Year(), monthDay, clock, strings.ToLower(m[4])), loc); err == nil && cand.After(now) {
		return cand.UTC()
	}
	return time.Time{}
}

var agyTrustDialog = [...]string{
	"Do you trust the contents of this project?",
	"",
	"Antigravity CLI requires permission to read, edit, and execute files here.",
	"",
	"> Yes, I trust this folder",
	"  No, exit",
	"",
	"  ↑/↓ Navigate · enter Confirm",
}

func equalDialogLine(line, want string) bool {
	return strings.TrimRight(line, " \t") == want
}

// Match the captured menu and one status line at the bottom of the screen.
func agyTrustSelected(lines []string) bool {
	n := len(lines) - len(agyTrustDialog) - 1
	if n < 0 {
		return false
	}
	return strings.TrimSpace(lines[len(lines)-1]) != "" &&
		slices.EqualFunc(lines[n:len(lines)-1], agyTrustDialog[:], equalDialogLine)
}

func agyTrustDrawing(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n \t"), "\n")
	if head := strings.Split(strings.TrimLeft(strings.TrimRight(text, "\n \t"), "\n"), "\n"); len(head) <= 4 && equalDialogLine(head[0], "Accessing workspace:") && (len(head) == 1 || equalDialogLine(head[1], "")) {
		return true
	}
	for n := 1; n <= min(len(lines), len(agyTrustDialog)); n++ {
		if slices.EqualFunc(lines[len(lines)-n:], agyTrustDialog[:n], equalDialogLine) {
			return true
		}
	}
	return false
}

// retryCoordWake reports an interruption or a newer submitted non-wake prompt.
func retryCoordWake(cli, clean string) bool {
	prompt := map[string]string{"claude": "❯", CodexCLI: "›", AgyCLI: ">"}[cli]
	if prompt == "" {
		return false
	}
	lines := strings.Split(strings.TrimRight(clean, "\n \t"), "\n")
	if anchor := uiAnchor(cli, lines); anchor >= 0 {
		lines = lines[:anchor]
	} else if strings.HasPrefix(lines[len(lines)-1], prompt) {
		lines = lines[:len(lines)-1]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		switch cli {
		case CodexCLI:
			if strings.HasPrefix(strings.TrimSpace(line), "■ Conversation interrupted") {
				return true
			}
			if !codexPromptRe.MatchString(line) {
				continue
			}
		case AgyCLI, "claude":
			marker := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "⎿"))
			if marker == "Interrupted · What should Antigravity CLI do instead?" && cli == AgyCLI ||
				marker == "Interrupted · What should Claude do instead?" && cli == "claude" {
				return true
			}
			if !strings.HasPrefix(line, prompt+" ") || cli == AgyCLI && i > 0 && !isRuleLine(lines[i-1]) {
				continue
			}
		}
		text := strings.TrimSpace(strings.TrimPrefix(line, prompt))
		count, ok := strings.CutPrefix(text, flow.CoordWakePromptPrefix)
		return !ok || count == "" || count[0] < '0' || count[0] > '9'
	}
	return false
}

// ObserveRun reads rec's terminal and screen from herdr-core and emits the events for one pass (design.md 5, 7.3).
func ObserveRun(ctx context.Context, client *hc.Client, rec flow.RunRecord, codexHome ...string) ([]flow.RunEvent, error) {
	if rec.State == flow.PhaseEnded {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("world: no hc client")
	}
	if rec.Address == "" {
		return []flow.RunEvent{{Kind: flow.EvLost}}, nil
	}
	t, err := client.Get(ctx, rec.Address)
	if errors.Is(err, hc.ErrNotFound) || (err == nil && ((rec.ID != "" && t.Labels["sheepdog.run"] != rec.ID) || (rec.CreatedMS != 0 && t.CreatedMS != 0 && t.CreatedMS != rec.CreatedMS))) {
		return []flow.RunEvent{{Kind: flow.EvLost}}, nil
	} else if err != nil {
		return nil, err
	}
	if t.State != "live" {
		return []flow.RunEvent{{Kind: flow.EvExit, Reason: deref(t.Reason)}}, nil
	}
	cls, limitLine, tail, hash, clean, nonEmpty := "", "", "", "", "", false
	infer := rec.CLI == CodexCLI && (rec.Role == flow.RoleWorker || rec.Role == flow.RoleCoordinator) && t.Status == nil
	codexStatus, lines := "unknown", 200
	if infer {
		lines = 0 // Read the current screen without older scrollback.
	}
	// Complete describes the older history. The current screen can still supply evidence.
	if rd, rerr := client.Read(ctx, rec.Address, lines); rerr == nil {
		clean = ansiRe.ReplaceAllString(strings.ReplaceAll(rd.Text, "\r\n", "\n"), "")
		nonEmpty = strings.TrimSpace(clean) != ""
		cls, limitLine = screenClassify(rec.CLI, clean)
		if infer && (cls == flow.ScreenNone || cls == flow.ScreenLimit && rec.State != flow.PhaseStarting) {
			codexStatus = codexScreenStatus(clean, cls == flow.ScreenLimit)
		}
		if nonEmpty {
			if rec.Role == flow.RoleWorker || rec.Role == flow.RoleDesigner {
				tail = screenChatTail(rec.CLI, clean)
			}
			sum := sha256.Sum256([]byte(strings.Join(strings.Fields(noiseRe.ReplaceAllString(durationRe.ReplaceAllString(clean, " "), " ")), " ")))
			hash = hex.EncodeToString(sum[:8])
		}
	}
	var evs []flow.RunEvent
	s := t.Status
	starting := rec.State == flow.PhaseStarting
	agyStarting := starting && rec.CLI == "agy"
	permission := s != nil && s.State == "blocked" && deref(s.Kind) == "permission"
	verifiedTrust := agyStarting && cls == flow.ScreenTrust &&
		rec.ID != "" && rec.CreatedMS != 0 && t.CreatedMS == rec.CreatedMS
	trustBlocked := permission && verifiedTrust
	waitForScreen := permission && agyStarting && (!nonEmpty || agyTrustDrawing(clean))
	if trustBlocked || (starting && !rec.StatusReported && s == nil && cls != "" &&
		(rec.CLI != "agy" || cls != flow.ScreenTrust || verifiedTrust)) {
		evs = append(evs, flow.RunEvent{Kind: flow.EvScreen, Screen: cls, ScreenHash: hash, LimitLine: limitLine})
		if rec.CLI == CodexCLI && !infer && nonEmpty && cls == flow.ScreenNone {
			evs = append(evs, flow.RunEvent{Kind: flow.EvStatus, Status: "working"})
		}
	}
	if hash != "" && rec.ScreenHash != "" && hash != rec.ScreenHash ||
		rec.State == flow.PhaseWorking && hasLiveDescendant(ctx, client, agentPID(t)) {
		evs = append(evs, flow.RunEvent{Kind: flow.EvActivity, ScreenHash: hash})
	}
	// Leave starting runs eligible for R3 while the menu is drawing or unreadable,
	// and treat agy's brief startup blocked/auth check as starting.
	if s != nil && !trustBlocked && !waitForScreen && !agyEarlyAuth(rec, t) && (!rec.StatusReported || s.State != rec.Status || deref(s.Kind) != rec.BlockedKind || s.Msg != rec.Question || t.Turn != rec.Turn) {
		var at time.Time
		if ms := deref(t.StatusMS); ms > 0 {
			at = time.UnixMilli(int64(ms))
		}
		evs = append(evs, flow.RunEvent{Kind: flow.EvStatus, Status: s.State, StatusKind: deref(s.Kind), Msg: s.Msg, Turn: t.Turn, StatusAt: at})
	}
	if infer && nonEmpty {
		if codexStatus == "idle" && hash == rec.SentScreenHash {
			codexStatus = "unknown"
		}
		if codexStatus != rec.Status && codexStatus != "unknown" {
			evs = append(evs, flow.RunEvent{Kind: flow.EvStatus, Status: codexStatus, ScreenHash: hash, Inferred: true})
		}
	}
	if rec.Role == flow.RoleWorker && rec.SessionID == "" {
		var sid string
		if cwd := hc.LocalCwd(t.Address, t.Cwd); cwd != "" {
			if rec.CLI == CodexCLI && len(codexHome) > 0 {
				sid = codexSession(codexHome[0], cwd, rec.StartedAt)
			} else if rec.CLI == AgyCLI {
				sid = agySession(ctx, client, cwd, rec.StartedAt)
			}
		}
		if sid != "" {
			evs = append(evs, flow.RunEvent{Kind: flow.EvSession, SessionID: sid})
		}
	}
	// An empty hash marks an unavailable screen; this tick cannot prove the run stayed quiet.
	return append(evs, flow.RunEvent{Kind: flow.EvTick, Screen: cls, ScreenHash: hash, LimitLine: limitLine, Tail: tail, Model: t.Labels["sheepdog.model"],
		RetryCoordWake: rec.Role == flow.RoleCoordinator && nonEmpty && retryCoordWake(rec.CLI, clean)}), nil
}

// ActRun executes one run-table act against herdr-core.
func ActRun(ctx context.Context, client *hc.Client, rec flow.RunRecord, act, msg string) (err error) {
	if client == nil || rec.Address == "" {
		return nil
	}
	switch act {
	case flow.RunActKeys:
		if keys := flow.ScreenKeys(rec.CLI, rec.Screen); len(keys) > 0 {
			_, err = client.Keys(ctx, rec.Address, keys...)
		}
	case flow.RunActSend, flow.RunActSendHeld:
		if msg != "" {
			err = SendRun(ctx, client, rec.Address, msg)
		}
	case flow.RunActInterrupt:
		_, err = client.Interrupt(ctx, rec.Address, 0)
	case flow.RunActStop:
		err = StopLabeledRun(ctx, client, rec.Address, rec.ID, rec.CreatedMS)
	case flow.RunActRemove:
		if _, rerr := client.Remove(ctx, rec.Address); !errors.Is(rerr, hc.ErrNotFound) && !errors.Is(rerr, hc.ErrLive) {
			err = rerr
		}
	}
	return err
}

const reportReadLines = 4000

// ReportStatus is the parsing outcome of ReadCodexReport (design.md 9.3).
type ReportStatus string

// The three outcomes of ReadCodexReport.
const (
	ReportNotYet  ReportStatus = "not_yet"
	ReportFound   ReportStatus = "found"
	ReportInvalid ReportStatus = "invalid"
)

// Report is slice 1's reviewer <report> JSON payload (prompts.md 5.4). It
// carries no free text: the findings are the reply text above the tag.
type Report struct {
	Code, Head, Verdict string
	Round, Majors       int
}

// ReportRead holds the parsed report tag (Raw) and the reply text above it
// (Body), which holds the findings.
type ReportRead struct {
	Status    ReportStatus
	Report    Report
	Raw, Body string
}

var (
	ansiRe       = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
	durationRe   = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|[dhms])\b`)
	noiseRe      = regexp.MustCompile(`[0-9✢✳✶✻✽✦✧·•\x{2800}-\x{28ff}◐◓◑◒◴◷◶◵]+`)
	tuiPromptRe  = regexp.MustCompile(`(?m)^❯[ \t\x{00a0}]+\S`)
	tuiReplyRe   = regexp.MustCompile(`(?m)^⏺`)
	tuiInputRe   = regexp.MustCompile(`(?m)^\x{2500}{8,}\n❯`)
	wrapIndentRe = regexp.MustCompile("\r?\n[ \t>]*")
	nonSlugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

func parseReportText(text, code string) ReportRead {
	clean := ansiRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	// The input box at the bottom may hold a draft the owner typed (seen
	// live); it is not a prompt echo. A box with a reply or tag after it is
	// not at the bottom.
	if m := tuiInputRe.FindAllStringIndex(clean, -1); len(m) > 0 {
		if rest := clean[m[len(m)-1][0]:]; !tuiReplyRe.MatchString(rest) && !strings.Contains(rest, "<report>") {
			clean = clean[:m[len(m)-1][0]]
		}
	}
	if m := tuiPromptRe.FindAllStringIndex(clean, -1); len(m) > 0 {
		r := tuiReplyRe.FindStringIndex(clean[m[len(m)-1][0]:])
		if r == nil {
			return ReportRead{Status: ReportNotYet}
		}
		clean = clean[m[len(m)-1][0]+r[0]:]
	}
	return parseReportTag(clean, code)
}

func parseReportTag(clean, code string) ReportRead {
	openIdx := strings.LastIndex(clean, "<report>")
	if openIdx < 0 {
		return ReportRead{Status: ReportNotYet}
	}
	// The TUI wraps long lines with an indent; joining them restores the tag.
	raw, _, ok := strings.Cut(wrapIndentRe.ReplaceAllString(clean[openIdx+len("<report>"):], ""), "</report>")
	if !ok {
		return ReportRead{Status: ReportNotYet}
	}
	raw = strings.TrimSpace(raw)
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(clean[:openIdx]), "⏺"))
	rep, valid := decodeReportJSON(raw)
	if !valid || code == "" || rep.Code != code {
		return ReportRead{Status: ReportInvalid, Report: rep, Raw: raw, Body: body}
	}
	return ReportRead{Status: ReportFound, Report: rep, Raw: raw, Body: body}
}

func decodeReportJSON(s string) (Report, bool) {
	// An old-style findings field is dropped unread: a model cannot be trusted
	// to escape free text in JSON (seen live: unescaped quotes).
	if i := strings.Index(s, `,"findings":`); i > 0 {
		s = s[:i] + "}"
	}
	var r Report
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if !strings.HasPrefix(s, "{") || dec.Decode(&r) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		return Report{}, false
	}
	return r, true
}

// RepoProject resolves folder's git top-level directory and its project id in
// local git config, minting and writing one when init is true (design.md 3).
func (g *Git) RepoProject(ctx context.Context, folder string, init bool) (string, string, error) {
	root, err := g.WorktreeRoot(ctx, folder)
	if err != nil {
		return "", "", fmt.Errorf("read git project folder %s: %w", folder, err)
	}
	id, err := g.gitCmd(ctx, root, "config", "--local", "--get", "sheepdog.projectId")
	if err != nil && !gitExit(err, 1) {
		return "", "", err
	}
	err = nil
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		id = ""
	}
	if id == "" && init {
		id = "proj-" + randID()
		_, err = g.gitCmd(ctx, root, "config", "--local", "sheepdog.projectId", id)
	}
	return root, id, err
}

// Worktree is a task attempt's git worktree and branch from the remote base.
type Worktree struct{ Path, Branch, BaseCommit string }

// NewWorktree fetches the remote base and creates branch task/<id>-<slug> and a
// worktree at worktreeDir from the fresh remote base (design.md 9.4, KB-WKT-1,
// KB-WKT-7). An existing worktree of repo at worktreeDir is adopted unchanged.
func (g *Git) NewWorktree(ctx context.Context, repo, worktreeDir, taskID, title string) (Worktree, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || taskID == "." || taskID == ".." || strings.ContainsAny(taskID, "/\\ \t\n") {
		return Worktree{}, Fail(flow.ClassSettled, fmt.Errorf("world: invalid task id %q", taskID))
	}
	if testing.Testing() {
		abs, err := filepath.Abs(worktreeDir)
		tmp, terr := filepath.EvalSymlinks(os.TempDir())
		// A new worktree may have missing parents. Resolve the nearest existing ancestor.
		var resolved string
		for candidate := abs; err == nil; candidate = filepath.Dir(candidate) {
			resolved, err = filepath.EvalSymlinks(candidate)
			if _, statErr := os.Lstat(candidate); err == nil || !os.IsNotExist(statErr) {
				break
			}
			err = nil
		}
		if rel, rerr := filepath.Rel(tmp, resolved); err != nil || terr != nil || rerr != nil || strings.HasPrefix(rel, "..") {
			return Worktree{}, Fail(flow.ClassSettled, fmt.Errorf("world: %s is outside the temp dir under go test", worktreeDir))
		}
	}
	slug := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	branch, base := "task/"+taskID, ""
	if slug != "" {
		branch += "-" + slug
	}
	ctx = GitRepository(ctx, repo)
	worktreeDir, err := absolutePath(worktreeDir)
	if err != nil {
		return Worktree{}, err
	}
	exists, err := g.PathExists(ctx, repo, filepath.Join(worktreeDir, ".git"))
	if err != nil {
		return Worktree{}, err
	}
	if exists {
		c1, err := g.gitCmd(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return Worktree{}, err
		}
		c2, err := g.gitCmd(ctx, worktreeDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return Worktree{}, err
		}
		if c1 == c2 {
			if branch, err = g.gitCmd(ctx, worktreeDir, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil {
				return Worktree{}, err
			}
			ref, _, err := g.remoteBaseCommit(ctx, repo, false)
			if err != nil {
				return Worktree{}, err
			}
			if base, err = g.gitCmd(ctx, worktreeDir, "merge-base", "HEAD", ref); err != nil {
				return Worktree{}, err
			}
		}
	}
	if base == "" {
		var err error
		if _, base, err = g.remoteBaseCommit(ctx, repo, true); err != nil {
			return Worktree{}, err
		}
		_, _ = g.gitCmd(ctx, repo, "worktree", "prune")
		if _, err := g.pathCommand(ctx, repo, `umask 077; exec mkdir -p "$1"`, filepath.Dir(worktreeDir)); err != nil {
			return Worktree{}, err
		}
		if _, err := g.gitCmd(ctx, repo, "worktree", "add", "--no-track", "-b", branch, worktreeDir, base); err != nil {
			return Worktree{}, err
		}
	}
	root, err := g.WorktreeRoot(ctx, worktreeDir)
	if err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: root, Branch: branch, BaseCommit: base}, nil
}

// WorktreeRoot returns Git's canonical top-level path for a checkout.
func (g *Git) WorktreeRoot(ctx context.Context, dir string) (string, error) {
	root, err := g.gitCmd(ctx, dir, "rev-parse", "--show-toplevel")
	if err == nil && !filepath.IsAbs(root) {
		err = errors.New("git did not return a worktree root")
	}
	return root, err
}

// FetchRemote fetches the push remote so the remote base and remote PR heads
// are in the local object store.
func (g *Git) FetchRemote(ctx context.Context, dir string) error {
	_, _, err := g.remoteBaseCommit(ctx, dir, true)
	return err
}

func (g *Git) remoteBaseCommit(ctx context.Context, repo string, fetch bool) (string, string, error) {
	if _, err := g.gitCmd(ctx, repo, "remote", "get-url", pushRemote); err != nil {
		if gitExit(err, 2) {
			return "", "", Fail(flow.ClassSettled, ErrNoOrigin)
		}
		return "", "", err
	}
	if fetch {
		if _, err := g.gitCmd(ctx, repo, "fetch", "--prune", pushRemote); err != nil {
			return "", "", err
		}
	}
	ref, err := g.gitCmd(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/"+pushRemote+"/HEAD")
	if err != nil && !gitExit(err, 1) {
		return "", "", err
	}
	if strings.HasPrefix(ref, "refs/remotes/"+pushRemote+"/") {
		sha, err := g.gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if !gitExit(err, 1) {
			return ref, sha, err
		}
	}
	ls, err := g.gitCmd(ctx, repo, "ls-remote", "--symref", pushRemote, "HEAD")
	if err != nil {
		return "", "", err
	}
	for _, ln := range strings.Split(ls, "\n") {
		if b, ok := strings.CutPrefix(ln, "ref: refs/heads/"); ok {
			if b, ok = strings.CutSuffix(b, "\tHEAD"); ok && b != "" {
				ref := "refs/remotes/" + pushRemote + "/" + b
				if sha, err := g.gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); !gitExit(err, 1) {
					return ref, sha, err
				}
			}
		}
	}
	return "", "", Fail(flow.ClassSettled, fmt.Errorf("world: no remote base branch on %s", pushRemote))
}

// InspectWorktree reads worktree's HEAD, its commit relation to ref, and
// whether the working tree is clean (design.md 3.1, 7.2, 7.3).
func (g *Git) InspectWorktree(ctx context.Context, worktree, ref string) (string, flow.Rel, bool, error) {
	tip, err := g.gitCmd(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", false, err
	}
	status, err := g.gitCmd(ctx, worktree, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", "", false, err
	}
	rel, err := g.Relation(ctx, worktree, ref, tip)
	if err != nil {
		return "", "", false, err
	}
	return tip, rel, status == "", nil
}

// RemoveWorktree removes a task worktree only if it is clean: uncommitted
// or untracked work is never destroyed (design.md 9.4, KB-MRG-78), and git
// itself refuses a dirty one (no --force). A worktree already gone counts
// as removed. It reports whether the worktree is gone.
func (g *Git) RemoveWorktree(ctx context.Context, repo, worktree string) (bool, error) {
	var exists bool
	var err error
	if worktree != "" {
		worktree, err = absolutePath(worktree)
		if err != nil {
			return false, err
		}
		exists, err = g.PathExists(ctx, repo, worktree)
	}
	if err != nil {
		return false, err
	}
	if !exists {
		_, err := g.gitCmd(ctx, repo, "worktree", "prune")
		return err == nil, err
	}
	status, err := g.gitCmd(ctx, worktree, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || status != "" {
		return false, err
	}
	_, err = g.gitCmd(ctx, repo, "worktree", "remove", worktree)
	return err == nil, err
}

// Relation is how commit x stands to ref in dir's repository: same, ahead
// (a descendant), behind (an ancestor) or diverged (design.md 3.1).
func (g *Git) Relation(ctx context.Context, dir, ref, x string) (flow.Rel, error) {
	counts, err := g.gitCmd(ctx, dir, "rev-list", "--left-right", "--count", ref+"..."+x)
	var behind, ahead int
	if err == nil {
		_, err = fmt.Sscanf(counts, "%d %d", &behind, &ahead)
	}
	switch {
	case err != nil:
		return "", err
	case behind == 0 && ahead == 0:
		return flow.RelSame, nil
	case behind == 0:
		return flow.RelAhead, nil
	case ahead == 0:
		return flow.RelBehind, nil
	}
	return flow.RelDiverged, nil
}

// MergeBase fetches the push remote's default branch and returns the merge
// base of head with it, computed now (design.md 9.4, KB-REV-12, KB-REV-13).
func (g *Git) MergeBase(ctx context.Context, worktree, head string) (string, error) {
	ref, _, err := g.remoteBaseCommit(ctx, worktree, true)
	if err != nil {
		return "", err
	}
	return g.gitCmd(ctx, worktree, "merge-base", head, ref)
}

// DeltaRanges returns the p..c git diff range(s) of non-merge-only commits on
// the first-parent line from since to head, leaving out clean merges of the
// remote base branch; if since is not an ancestor of head, it returns the full
// range mb..head (design.md 3.1, 9.4, KB-REV-12, KB-REV-13).
func (g *Git) DeltaRanges(ctx context.Context, worktree, since, head string) ([]string, error) {
	mb, err := g.MergeBase(ctx, worktree, head)
	if err != nil {
		return nil, err
	}
	if ancestor, err := g.isAncestor(ctx, worktree, since, head); err != nil {
		return nil, err
	} else if !ancestor {
		return []string{mb + ".." + head}, nil
	}
	out, err := g.gitCmd(ctx, worktree, "rev-list", "--reverse", "--first-parent", "--parents", since+".."+head)
	if err != nil {
		return nil, err
	}
	var ranges []string
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		mc, err := g.inspectMergeCommit(ctx, worktree, f, mb)
		if err != nil {
			return nil, err
		}
		if flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelAhead, Commits: []flow.MergeCommit{mc}}) {
			continue
		}
		if n := len(ranges); n > 0 {
			if a, b, _ := strings.Cut(ranges[n-1], ".."); b == f[1] {
				ranges[n-1] = a + ".." + f[0]
				continue
			}
		}
		ranges = append(ranges, f[1]+".."+f[0])
	}
	if len(ranges) == 0 {
		return []string{head + ".." + head}, nil
	}
	return ranges, nil
}

var ErrChangedLinesOverCap = errors.New("changed lines over cap or unreadable in full")

func countPatchLines(out string) int {
	n, hunk := 0, false
	for ln := range strings.Lines(out) {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			hunk = false
		case strings.HasPrefix(ln, "@@"):
			hunk = true
		case hunk && (ln[0] == '+' || ln[0] == '-'):
			n++
		}
	}
	return n
}

func diffCapErr(err error) error {
	var ge *GitError
	if GitTimedOut(err) || errors.As(err, &ge) && ge.RPC == nil && ge.Result.Truncated {
		return fmt.Errorf("%w: %w", ErrChangedLinesOverCap, err)
	}
	return err
}

func (g *Git) attrSourceHonoured(ctx context.Context, dir string) (bool, error) {
	g.mu.Lock()
	cached := g.attrSrc
	g.mu.Unlock()
	if cached != 0 {
		return cached > 0, nil
	}
	_, err := g.run(ctx, dir, "git", []string{"check-attr", "linguist-generated", "--", "."}, map[string]string{
		"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "", "GIT_ATTR_SOURCE": "refs/sheepdog/none",
	})
	if code, ok := gitExitCode(err); err != nil && (!ok || code != 128) {
		if ok {
			return false, nil
		}
		return false, err
	}
	honoured := err != nil
	g.mu.Lock()
	logNo := g.attrSrc == 0 && !honoured && g.log != nil
	if honoured {
		g.attrSrc = 1
	} else {
		g.attrSrc = -1
	}
	g.mu.Unlock()
	if logNo {
		g.log("factory.git_attr_source_unsupported", dir)
	}
	return honoured, nil
}

// ChangedLines is the lines added plus removed at tip against its merge base
// with the base branch as git last fetched it (N12, design.md 7.3, 11.5),
// counting -diff and NUL files as text while leaving out paths the base's
// committed .gitattributes marks linguist-generated (returned separately).
func (g *Git) ChangedLines(ctx context.Context, dir, tip string) (lines, generated int, err error) {
	ref, baseSHA, err := g.remoteBaseCommit(ctx, dir, false)
	if err != nil {
		return 0, 0, err
	}
	diffArgs := []string{"diff", "--text", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=0", ref + "..." + tip}
	// The patch, not --numstat: numstat still calls a -diff or NUL file binary under --text.
	out, err := g.gitCmd(ctx, dir, diffArgs...)
	if err != nil {
		return 0, 0, diffCapErr(err)
	}
	total := countPatchLines(out)
	if honoured, err := g.attrSourceHonoured(ctx, dir); err != nil {
		return 0, 0, err
	} else if !honoured {
		return total, 0, nil
	}
	r, err := g.run(ctx, dir, "git", append(diffArgs, "--", ":(top)", ":(top,exclude,attr:linguist-generated)", ":(top,exclude,attr:linguist-generated=true)"), map[string]string{
		"GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": "", "GIT_ATTR_SOURCE": baseSHA, "GIT_ATTR_NOSYSTEM": "1",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.attributesFile", "GIT_CONFIG_VALUE_0": "/dev/null",
	})
	if err != nil {
		if _, ok := gitExitCode(err); ok {
			return total, 0, nil
		}
		return 0, 0, diffCapErr(err)
	}
	lines = countPatchLines(string(r.Stdout))
	return lines, max(0, total-lines), nil
}

// MergeOnly reports whether h is ahead of x by clean merges of the base
// branch only, on the first-parent line (design.md 3.1, 9.4). It does not
// fetch; the decision is made by flow.MergeOnlyRows.
func (g *Git) MergeOnly(ctx context.Context, dir, x, h string) (bool, error) {
	ref, _, err := g.remoteBaseCommit(ctx, dir, false)
	if err != nil {
		return false, err
	}
	if x == h {
		return false, nil
	}
	mb, err := g.gitCmd(ctx, dir, "merge-base", h, ref)
	if err != nil {
		if gitExit(err, 1) { // no common ancestor
			return false, nil
		}
		return false, err
	}
	if ancestor, err := g.isAncestor(ctx, dir, x, h); err != nil || !ancestor {
		return false, err
	}
	out, err := g.gitCmd(ctx, dir, "rev-list", "--first-parent", "--parents", x+".."+h)
	if err != nil || out == "" {
		return false, err
	}
	var commits []flow.MergeCommit
	for ln := range strings.Lines(out) {
		mc, err := g.inspectMergeCommit(ctx, dir, strings.Fields(ln), mb)
		if err != nil {
			return false, err
		}
		commits = append(commits, mc)
	}
	return flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelAhead, Commits: commits}), nil
}

func (g *Git) inspectMergeCommit(ctx context.Context, dir string, f []string, mb string) (flow.MergeCommit, error) {
	c := flow.MergeCommit{Parents: max(0, len(f)-1)}
	if c.Parents != 2 {
		return c, nil
	}
	ancestor, err := g.isAncestor(ctx, dir, f[2], mb)
	c.SecondInBase = ancestor
	if err != nil || !ancestor {
		return c, err
	}
	want, err := g.gitCmd(ctx, dir, "merge-tree", "--write-tree", f[1], f[2])
	if err != nil {
		if gitExit(err, 1) {
			return c, nil // merge conflict: not a clean merge
		}
		return c, err
	}
	got, err := g.gitCmd(ctx, dir, "rev-parse", f[0]+"^{tree}")
	if err != nil {
		return c, err
	}
	c.TreeMatches = got == want
	return c, nil
}

func (g *Git) isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	_, err := g.gitCmd(ctx, dir, "merge-base", "--is-ancestor", a, b)
	if gitExit(err, 1) {
		return false, nil
	}
	return err == nil, err
}

func randID() string { return fmt.Sprintf("%08x", rand.Uint32()) }
