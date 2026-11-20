// Package world holds the engine's executors and observers over herdr-core and
// git. It makes no policy decisions (docs/dev/engine-v2/design.md 9.4, 13).
package world

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
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

// RunSpec describes one terminal run to launch in herdr-core.
type RunSpec struct {
	Name, Cwd, Prompt, ClaudeConfig string
	Argv                            []string
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
		// otherwise swallow the prompt that StartRun appends (seen live).
		argv = append(argv, "--allowedTools="+reviewerTools)
	}
	return argv
}

// reviewerTools is what a reviewer may use without asking: read the code and
// its history, and run the project's tests. No edit tools, but test runners
// run worker code and some git and go flags write files: read-only is a rule
// on the model, backed by the engine's clean-worktree check on the verdict.
const reviewerTools = "Read Grep Glob LS " +
	"Bash(git diff:*) Bash(git log:*) Bash(git show:*) Bash(git status:*) " +
	"Bash(go build:*) Bash(go test:*) Bash(go vet:*) Bash(gofmt -l:*) Bash(make test:*) " +
	"Bash(npm test:*) Bash(pytest:*) Bash(python3 -m pytest:*) Bash(python3 -m unittest:*)"

// StartRun launches spec in herdr-core and returns its terminal address.
func StartRun(ctx context.Context, client *hc.Client, spec RunSpec) (string, error) {
	if client == nil || strings.TrimSpace(spec.Cwd) == "" || len(spec.Argv) == 0 {
		return "", errors.New("world: StartRun requires hc client, Cwd and Argv")
	}
	if spec.Name != "" {
		if t, err := client.Get(ctx, spec.Name); err == nil {
			if t.State == "live" {
				return t.Address, nil
			}
			if _, err := client.Remove(ctx, t.Address); err != nil && !errors.Is(err, hc.ErrNotFound) {
				return "", err
			}
		}
	}
	if isClaudeArgv(spec.Argv) {
		if err := preAcceptClaudeTrust(spec.ClaudeConfig, spec.Cwd); err != nil {
			return "", err
		}
	}
	argv := append([]string(nil), spec.Argv...)
	if spec.Prompt != "" {
		argv = append(argv, spec.Prompt)
	}
	labels := map[string]string{"herdr.space": randID(), "herdr.tab": randID(), "herdr.seen": "0"}
	maps.Copy(labels, spec.Labels)
	t, err := client.Run(ctx, hc.RunParams{
		Name: spec.Name, Cwd: spec.Cwd, Argv: argv, Labels: labels, Env: spec.Env,
		NoParent: true, NoNotify: true,
	})
	return t.Address, err
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
		return errors.New("world: SendRun requires hc client and terminal id")
	}
	_, err := client.Send(ctx, id, text, false)
	return err
}

// StopRun kills terminal id, removes it, and verifies both the terminal and its
// OS process group are gone (design.md 9.4).
func StopRun(ctx context.Context, client *hc.Client, id string) error {
	if client == nil || id == "" {
		return nil
	}
	t, err := getTerminal(ctx, client, id)
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	pid := 0
	if t.PID != nil && *t.PID > 1 && *t.PID != os.Getpid() && *t.PID != syscall.Getpgrp() {
		pid = *t.PID
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	if _, err := client.Kill(ctx, id); err != nil && !errors.Is(err, hc.ErrNotFound) && !errors.Is(err, hc.ErrFinished) {
		return err
	}
	for i := 0; i <= 80; i++ {
		if _, rerr := client.Remove(ctx, id); rerr != nil && !errors.Is(rerr, hc.ErrNotFound) && !errors.Is(rerr, hc.ErrLive) {
			return rerr
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
	return fmt.Errorf("world: %s or pid %d still alive after stop", id, pid)
}

// RunState maps herdr-core's terminal state and OSC 7501 status to flow's
// idle | working | gone.
func RunState(ctx context.Context, client *hc.Client, id string) (flow.RunState, error) {
	if id == "" {
		return flow.RunGone, nil
	}
	t, err := getTerminal(ctx, client, id)
	if errors.Is(err, hc.ErrNotFound) || (err == nil && t.State != "live") {
		return flow.RunGone, nil
	} else if err != nil {
		return "", err
	}
	if t.Status != nil && stateIdle(t.Status.State) {
		return flow.RunIdle, nil
	}
	return flow.RunWorking, nil
}

func getTerminal(ctx context.Context, client *hc.Client, id string) (hc.Terminal, error) {
	if client == nil {
		return hc.Terminal{}, errors.New("world: no hc client")
	}
	return client.Get(ctx, id)
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
func RepoProject(ctx context.Context, folder string, init bool) (string, string, error) {
	root, err := gitCmd(ctx, folder, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("not a git repository (%s): %w", folder, err)
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	id, _ := gitCmd(ctx, root, "config", "--local", "--get", "sheepdog.projectId")
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		id = ""
	}
	if id == "" && init {
		id = "proj-" + randID()
		_, err = gitCmd(ctx, root, "config", "--local", "sheepdog.projectId", id)
	}
	return root, id, err
}

// Worktree is a task attempt's git worktree and branch from the remote base.
type Worktree struct{ Path, Branch, BaseCommit string }

// NewWorktree fetches the remote base and creates branch task/<id>-<slug> and a
// worktree at worktreeDir from the fresh remote base (design.md 9.4, KB-WKT-1,
// KB-WKT-7). An existing worktree of repo at worktreeDir is adopted unchanged.
func NewWorktree(ctx context.Context, repo, worktreeDir, taskID, title string) (Worktree, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || taskID == "." || taskID == ".." || strings.ContainsAny(taskID, "/\\ \t\n") {
		return Worktree{}, fmt.Errorf("world: invalid task id %q", taskID)
	}
	if testing.Testing() {
		abs, err := filepath.Abs(worktreeDir)
		if rel, rerr := filepath.Rel(os.TempDir(), abs); err != nil || rerr != nil || strings.HasPrefix(rel, "..") {
			return Worktree{}, fmt.Errorf("world: %s is outside the temp dir under go test", worktreeDir)
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
	repo = realPath(repo)
	if _, err := os.Stat(filepath.Join(worktreeDir, ".git")); err == nil {
		worktreeDir = realPath(worktreeDir)
		c1, e1 := gitCmd(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
		c2, e2 := gitCmd(ctx, worktreeDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e1 == nil && e2 == nil && c1 == c2 {
			if b, _ := gitCmd(ctx, worktreeDir, "symbolic-ref", "--quiet", "--short", "HEAD"); b != "" {
				branch = b
			}
			ref, _, _ := remoteBaseCommit(ctx, repo, false)
			base, _ = gitCmd(ctx, worktreeDir, "merge-base", "HEAD", ref)
			if base == "" {
				if base, err = gitCmd(ctx, worktreeDir, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
					return Worktree{}, err
				}
			}
		}
	}
	if base == "" {
		var err error
		if _, base, err = remoteBaseCommit(ctx, repo, true); err != nil {
			return Worktree{}, err
		}
		_, _ = gitCmd(ctx, repo, "worktree", "prune")
		if err := os.MkdirAll(filepath.Dir(worktreeDir), 0o700); err != nil {
			return Worktree{}, err
		}
		if _, err := gitCmd(ctx, repo, "worktree", "add", "--no-track", "-b", branch, worktreeDir, base); err != nil {
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

// PushRemote picks the remote the engine pushes to: origin, else the only
// remote, else "" (none).
func PushRemote(names []string) string {
	switch {
	case slices.Contains(names, "origin"):
		return "origin"
	case len(names) == 1:
		return names[0]
	}
	return ""
}

// FetchRemote fetches the push remote so the remote base and remote PR heads
// are in the local object store.
func FetchRemote(ctx context.Context, dir string) error {
	_, _, err := remoteBaseCommit(ctx, dir, true)
	return err
}

func remoteBaseCommit(ctx context.Context, repo string, fetch bool) (string, string, error) {
	out, err := gitCmd(ctx, repo, "remote")
	if err != nil {
		return "", "", err
	}
	remote := PushRemote(strings.Fields(out))
	if remote == "" {
		return "", "", fmt.Errorf("world: no unique git remote in %s", repo)
	}
	if fetch {
		if _, err := gitCmd(ctx, repo, "fetch", "--prune", remote); err != nil {
			return "", "", err
		}
	}
	if ref, _ := gitCmd(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD"); strings.HasPrefix(ref, "refs/remotes/"+remote+"/") {
		if sha, err := gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref, sha, nil
		}
	}
	ls, err := gitCmd(ctx, repo, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "", "", err
	}
	for _, ln := range strings.Split(ls, "\n") {
		if b, ok := strings.CutPrefix(ln, "ref: refs/heads/"); ok {
			if b, ok = strings.CutSuffix(b, "\tHEAD"); ok && b != "" {
				ref := "refs/remotes/" + remote + "/" + b
				if sha, err := gitCmd(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
					return ref, sha, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("world: no remote base branch on %s", remote)
}

// InspectWorktree reads worktree's HEAD, its commit relation to ref, and
// whether the working tree is clean (design.md 3.1, 7.2, 7.3).
func InspectWorktree(ctx context.Context, worktree, ref string) (string, flow.Rel, bool, error) {
	tip, e1 := gitCmd(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	status, e2 := gitCmd(ctx, worktree, "status", "--porcelain", "--untracked-files=normal")
	if err := errors.Join(e1, e2); err != nil {
		return "", "", false, err
	}
	rel, err := Relation(ctx, worktree, ref, tip)
	if err != nil {
		return "", "", false, err
	}
	return tip, rel, status == "", nil
}

// RemoveWorktree removes a task worktree only if it is clean: uncommitted
// or untracked work is never destroyed (design.md 9.4, KB-MRG-78), and git
// itself refuses a dirty one (no --force). A worktree already gone counts
// as removed. It reports whether the worktree is gone.
func RemoveWorktree(ctx context.Context, repo, worktree string) (bool, error) {
	if _, err := os.Stat(worktree); os.IsNotExist(err) {
		_, err = gitCmd(ctx, repo, "worktree", "prune")
		return err == nil, err
	}
	status, err := gitCmd(ctx, worktree, "status", "--porcelain", "--untracked-files=normal")
	if err != nil || status != "" {
		return false, err
	}
	_, err = gitCmd(ctx, repo, "worktree", "remove", worktree)
	return err == nil, err
}

// Relation is how commit x stands to ref in dir's repository: same, ahead
// (a descendant), behind (an ancestor) or diverged (design.md 3.1).
func Relation(ctx context.Context, dir, ref, x string) (flow.Rel, error) {
	counts, err := gitCmd(ctx, dir, "rev-list", "--left-right", "--count", ref+"..."+x)
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
func MergeBase(ctx context.Context, worktree, head string) (string, error) {
	ref, _, err := remoteBaseCommit(ctx, worktree, true)
	if err != nil {
		return "", err
	}
	return gitCmd(ctx, worktree, "merge-base", head, ref)
}

// DeltaRanges returns the p..c git diff range(s) of non-merge-only commits on
// the first-parent line from since to head, leaving out clean merges of the
// remote base branch; if since is not an ancestor of head, it returns the full
// range mb..head (design.md 3.1, 9.4, KB-REV-12, KB-REV-13).
func DeltaRanges(ctx context.Context, worktree, since, head string) ([]string, error) {
	mb, err := MergeBase(ctx, worktree, head)
	if err != nil {
		return nil, err
	}
	if !isAncestor(ctx, worktree, since, head) {
		return []string{mb + ".." + head}, nil
	}
	out, err := gitCmd(ctx, worktree, "rev-list", "--reverse", "--first-parent", "--parents", since+".."+head)
	if err != nil {
		return nil, err
	}
	var ranges []string
	for ln := range strings.Lines(out) {
		f := strings.Fields(ln)
		if len(f) < 2 || flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelAhead, Commits: []flow.MergeCommit{inspectMergeCommit(ctx, worktree, f, mb)}}) {
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

// ChangedLines is the lines added plus removed at tip against its merge base
// with the base branch, as git last fetched it (N12, design.md 11.5). It
// does not fetch: it runs every pass. Every file counts as text, so no
// attribute or NUL byte the worker commits can hide lines.
func ChangedLines(ctx context.Context, dir, tip string) (int, error) {
	ref, _, err := remoteBaseCommit(ctx, dir, false)
	if err != nil {
		return 0, err
	}
	// The patch, not --numstat: numstat still calls a -diff or NUL file
	// binary under --text.
	out, err := gitCmd(ctx, dir, "diff", "--text", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=0", ref+"..."+tip)
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
func MergeOnly(ctx context.Context, dir, x, h string) (bool, error) {
	ref, _, err := remoteBaseCommit(ctx, dir, false)
	if err != nil {
		return false, err
	}
	if x == h {
		return flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelSame}), nil
	}
	mb, err := gitCmd(ctx, dir, "merge-base", h, ref)
	if err != nil || !isAncestor(ctx, dir, x, h) { // no common ancestor is "not merge_only"; only a cancelled read is an error
		return flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelDiverged}), ctx.Err()
	}
	out, err := gitCmd(ctx, dir, "rev-list", "--first-parent", "--parents", x+".."+h)
	if err != nil || out == "" {
		return false, err
	}
	var commits []flow.MergeCommit
	for ln := range strings.Lines(out) {
		c := inspectMergeCommit(ctx, dir, strings.Fields(ln), mb)
		commits = append(commits, c)
		if !flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelAhead, Commits: commits}) {
			return false, nil
		}
	}
	return flow.IsMergeOnly(flow.MergeOnlyIn{Rel: flow.RelAhead, Commits: commits}), nil
}

func inspectMergeCommit(ctx context.Context, dir string, f []string, mb string) flow.MergeCommit {
	c := flow.MergeCommit{Parents: max(0, len(f)-1)}
	if c.Parents != 2 {
		return c
	}
	c.SecondInBase = isAncestor(ctx, dir, f[2], mb)
	if !c.SecondInBase {
		return c
	}
	want, err := gitCmd(ctx, dir, "merge-tree", "--write-tree", f[1], f[2])
	if err != nil {
		return c
	}
	got, err := gitCmd(ctx, dir, "rev-parse", f[0]+"^{tree}")
	c.TreeMatches = err == nil && got == want
	return c
}

func isAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := gitCmd(ctx, dir, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	gctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if gctx.Err() != nil {
			return "", fmt.Errorf("%w: %v: %s", gctx.Err(), err, strings.TrimSpace(string(out)))
		}
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func randID() string { return fmt.Sprintf("%08x", rand.Uint32()) }
