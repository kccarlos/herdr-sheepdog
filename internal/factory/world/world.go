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
	Fresh                           bool              // caller already resolved ownership
	Labels, Env                     map[string]string // Env holds secrets: never argv
}

// ClaudeArgv returns the launch argv for Claude Code across the three roles,
// stating model, effort and permission mode explicitly, skipping the bypass
// confirmation dialog in bypass modes, and disabling the auto-updater
// (KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11).
func ClaudeArgv(role, model, effort, permissionMode string) []string {
	model, effort, permissionMode = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(permissionMode)
	if (role != flow.RoleCoordinator && role != flow.RoleWorker && role != flow.RoleReviewer) ||
		model == "" || effort == "" || permissionMode == "" {
		return nil
	}
	argv := []string{"env", "DISABLE_AUTOUPDATER=1", "claude", "--model", model, "--effort", effort, "--permission-mode", permissionMode}
	if permissionMode == "bypassPermissions" {
		argv = append(argv, "--settings", `{"skipDangerousModePermissionPrompt":true}`)
	}
	if permissionMode == "dontAsk" {
		// A reviewer never waits on a prompt (seen live: plan mode waited 20
		// minutes on a shell prompt, then reviewed without running tests).
		// dontAsk denies anything not listed; the list is read-only plus
		// the usual test runners, and never Edit or Write.
		// One argument with "=": --allowedTools takes many values and would
		// otherwise swallow the prompt that StartTerminal appends (seen live).
		argv = append(argv, "--allowedTools="+reviewerTools)
	}
	return argv
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

// StartTerminal launches spec in herdr-core (or adopts a live same-name terminal) and returns its terminal record and whether an hc check failed before launch.
func StartTerminal(ctx context.Context, client *hc.Client, spec RunSpec, beforeLaunch func() error) (hc.Terminal, bool, error) {
	if client != nil && spec.Name != "" && !spec.Fresh {
		switch t, err := client.Get(ctx, spec.Name); {
		case err != nil && !errors.Is(err, hc.ErrNotFound):
			return hc.Terminal{}, true, hcFail(err)
		case err == nil && t.State == "live":
			return t, false, nil
		case err == nil:
			if _, rerr := client.Remove(ctx, t.Address); rerr != nil && !errors.Is(rerr, hc.ErrNotFound) {
				return hc.Terminal{}, false, hcFail(rerr)
			}
		}
	}
	if client == nil || strings.TrimSpace(spec.Cwd) == "" || len(spec.Argv) == 0 {
		return hc.Terminal{}, false, Fail(flow.ClassSettled, errors.New("world: StartTerminal requires hc client, Cwd and Argv"))
	}
	if beforeLaunch != nil {
		if err := beforeLaunch(); err != nil {
			return hc.Terminal{}, true, err
		}
	}
	if isClaudeArgv(spec.Argv) {
		if err := preAcceptClaudeTrust(spec.ClaudeConfig, spec.Cwd); err != nil {
			return hc.Terminal{}, false, Fail(flow.ClassSettled, err)
		}
	}
	argv := append([]string(nil), spec.Argv...)
	if spec.Prompt != "" {
		argv = append(argv, spec.Prompt)
	}
	labels := map[string]string{"herdr.space": randID(), "herdr.tab": randID(), "herdr.seen": "0"}
	maps.Copy(labels, spec.Labels)
	t, err := client.Run(ctx, hc.RunParams{Name: spec.Name, Cwd: spec.Cwd, Argv: argv, Labels: labels, Env: spec.Env, NoParent: true, NoNotify: true})
	return t, false, hcFail(err)
}

func isClaudeArgv(argv []string) bool {
	for i, a := range argv {
		if (i == 0 && filepath.Base(a) == "env") || (strings.Contains(a, "=") && !strings.HasPrefix(a, "-")) {
			continue
		}
		return filepath.Base(a) == "claude"
	}
	return false
}

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
	absCwd, err := filepath.Abs(cwd)
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
	if label != "" && t.Labels["sheepdog.run"] != label || createdMS != 0 && t.CreatedMS != createdMS {
		return nil
	}
	pid := 0
	if t.PID != nil && *t.PID > 1 && *t.PID != os.Getpid() && *t.PID != syscall.Getpgrp() {
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

func matchUI(lines []string, spec, trim string) bool {
	return slices.ContainsFunc(strings.Split(spec, "|"), func(pat string) bool {
		p, wild := strings.CutSuffix(pat, "*")
		return slices.ContainsFunc(lines, func(ln string) bool {
			s := strings.TrimLeft(ln, trim)
			return (wild && strings.HasPrefix(s, p)) || (!wild && s == pat)
		})
	})
}

// ScreenClass classifies the bottom UI region of text against cli's known
// dialog, sign-out and usage-limit screens (design.md 5, 9.2; spikes S0.2, S0.8).
func ScreenClass(cli, text string) string {
	lines := strings.Split(strings.TrimRight(ansiRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), ""), "\n \t"), "\n")
	var ui []string
	for _, ln := range lines[max(0, len(lines)-screenTailLines):] {
		if s := strings.TrimLeft(strings.ToLower(strings.TrimSpace(ln)), "⏺●⎿⚠✕✗•·│✨\u200a "); s != "" {
			ui = append(ui, s)
		}
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
		{"agy", flow.ScreenTrust, "do you trust the contents of this project?", "> yes, i trust this folder"},
	} {
		if r[0] == cli && matchUI(ui, r[2], "") && (r[3] == "" || matchUI(ui, r[3], "❯› ")) {
			return r[1]
		}
	}
	return flow.ScreenNone
}

// ObserveRun reads rec's terminal and screen from herdr-core and emits the events for one pass (design.md 5, 7.3).
func ObserveRun(ctx context.Context, client *hc.Client, rec flow.RunRecord) ([]flow.RunEvent, error) {
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
	cls, hash, nonEmpty := "", "", false
	// Complete describes the older history. The current screen can still supply evidence.
	if rd, rerr := client.Read(ctx, rec.Address, 200); rerr == nil {
		clean := ansiRe.ReplaceAllString(strings.ReplaceAll(rd.Text, "\r\n", "\n"), "")
		nonEmpty, cls = strings.TrimSpace(clean) != "", ScreenClass(rec.CLI, clean)
		if rd.Text != "" {
			sum := sha256.Sum256([]byte(strings.Join(strings.Fields(noiseRe.ReplaceAllString(durationRe.ReplaceAllString(clean, " "), " ")), " ")))
			hash = hex.EncodeToString(sum[:8])
		}
	}
	var evs []flow.RunEvent
	if cls != "" && rec.State == flow.PhaseStarting && !rec.StatusReported && t.Status == nil {
		evs = append(evs, flow.RunEvent{Kind: flow.EvScreen, Screen: cls, ScreenHash: hash})
		if rec.CLI == CodexCLI && nonEmpty && cls == flow.ScreenNone {
			evs = append(evs, flow.RunEvent{Kind: flow.EvStatus, Status: "working"})
		}
	}
	if hash != "" && rec.ScreenHash != "" && hash != rec.ScreenHash {
		evs = append(evs, flow.RunEvent{Kind: flow.EvActivity, ScreenHash: hash})
	}
	if s := t.Status; s != nil && (!rec.StatusReported || s.State != rec.Status || deref(s.Kind) != rec.BlockedKind || s.Msg != rec.Question || t.Turn != rec.Turn) {
		var at time.Time
		if ms := deref(t.StatusMS); ms > 0 {
			at = time.UnixMilli(int64(ms))
		}
		evs = append(evs, flow.RunEvent{Kind: flow.EvStatus, Status: s.State, StatusKind: deref(s.Kind), Msg: s.Msg, Turn: t.Turn, StatusAt: at})
	}
	// An empty hash marks an unavailable screen; this tick cannot prove the run stayed quiet.
	return append(evs, flow.RunEvent{Kind: flow.EvTick, Screen: cls, ScreenHash: hash, Model: t.Labels["sheepdog.model"]}), nil
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

// ReportStatus is the parsing outcome of ReadReport (design.md 9.3).
type ReportStatus string

// The three outcomes of ReadReport.
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

// ReadReport waits until term is idle/done, reads its stitched output, skips
// everything up to and including the last prompt echo, and parses the last
// <report>...</report> tag (design.md 9.3, spike S0.3); the reply text above
// that tag is the findings (Body).
func ReadReport(ctx context.Context, client *hc.Client, term, code string) (ReportRead, error) {
	if client == nil || strings.TrimSpace(term) == "" {
		return ReportRead{}, errors.New("world: ReadReport requires hc client and terminal")
	}
	if err := waitReportIdle(ctx, client, term); err != nil {
		return ReportRead{}, err
	}
	res, err := client.Read(ctx, term, reportReadLines)
	if err != nil || !res.Complete {
		return ReportRead{Status: ReportNotYet}, err
	}
	return parseReportText(res.Text, code), nil
}

func waitReportIdle(ctx context.Context, client *hc.Client, term string) error {
	if t, err := client.Get(ctx, term); err != nil || termIdle(t) {
		return err
	}
	sub, err := client.Subscribe(ctx, hc.SubscribeFilter{Terminal: term})
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	if _, snap := sub.Snapshot(); len(snap) == 0 || termIdle(snap[0]) {
		return nil
	}
	for n := range sub.Events() {
		var sd hc.StatusData
		var td hc.TurnData
		if (n.Method == "missed" && (len(n.Terminals) == 0 || termIdle(n.Terminals[0]))) ||
			(n.Method == "event" && (n.Type == "finished" || n.Type == "removed" || n.Type == "lost" ||
				(n.Type == "status" && n.DecodeData(&sd) == nil && sd.Status != nil && stateIdle(sd.Status.State)) ||
				(n.Type == "turn" && n.DecodeData(&td) == nil && stateIdle(td.State)))) {
			return nil
		}
	}
	return cmp.Or(ctx.Err(), sub.Err())
}

func termIdle(t hc.Terminal) bool {
	return t.State != "live" || (t.Status != nil && stateIdle(t.Status.State))
}
func stateIdle(s string) bool { return s == "idle" || s == "done" || s == "error" }

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
	root, err := g.gitCmd(ctx, folder, "rev-parse", "--show-toplevel")
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
	if _, err := os.Stat(filepath.Join(worktreeDir, ".git")); err == nil {
		worktreeDir = realPath(worktreeDir)
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
		if err := os.MkdirAll(filepath.Dir(worktreeDir), 0o700); err != nil {
			return Worktree{}, err
		}
		if _, err := g.gitCmd(ctx, repo, "worktree", "add", "--no-track", "-b", branch, worktreeDir, base); err != nil {
			return Worktree{}, err
		}
	}
	return Worktree{Path: realPath(worktreeDir), Branch: branch, BaseCommit: base}, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
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
	if _, err := os.Stat(worktree); os.IsNotExist(err) {
		_, err = g.gitCmd(ctx, repo, "worktree", "prune")
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

// ChangedLines is the lines added plus removed at tip against its merge base
// with the base branch, as git last fetched it (N12, design.md 11.5). It
// does not fetch: it runs every pass. Every file counts as text, so no
// attribute or NUL byte the worker commits can hide lines.
func (g *Git) ChangedLines(ctx context.Context, dir, tip string) (int, error) {
	ref, _, err := g.remoteBaseCommit(ctx, dir, false)
	if err != nil {
		return 0, err
	}
	// The patch, not --numstat: numstat still calls a -diff or NUL file
	// binary under --text.
	out, err := g.gitCmd(ctx, dir, "diff", "--text", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=0", ref+"..."+tip)
	var result *GitError
	if GitTimedOut(err) || errors.As(err, &result) && result.RPC == nil && result.Result.Truncated {
		return 0, fmt.Errorf("%w: %w", ErrChangedLinesOverCap, err)
	}
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
	return n, err
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
