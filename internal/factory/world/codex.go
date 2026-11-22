package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// CodexCLI names Codex in a reviewer record (flow.Reviewer.CLI).
const CodexCLI = "codex"

// CodexArgv returns the launch argv for a Codex reviewer in cwd: model and
// effort stated and cwd already canonical, a read-only sandbox, never an approval prompt, and the run
// kept inside herdr-core (--no-daemon). Per run, through -c overrides that
// write nothing to Codex's config: cwd is trusted (no folder-trust prompt),
// hooks are off (no hooks-review prompt, and no project hook runs), and no
// update check. Spike S0.3 and PR D3's step 0. StartRun appends the prompt.
func CodexArgv(model, effort, cwd string) []string {
	return codexArgv(model, effort, cwd, []string{"codex"}, []string{"-s", "read-only", "-a", "never"})
}

// CodexWorkArgv launches or resumes a full-access worker or coordinator.
func CodexWorkArgv(model, effort, cwd, session string) []string {
	command := []string{"codex"}
	if session != "" {
		command = append(command, "resume", session)
	}
	return codexArgv(model, effort, cwd, command, []string{"--dangerously-bypass-approvals-and-sandbox"})
}

func codexArgv(model, effort, cwd string, command, options []string) []string {
	model, effort, cwd = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(cwd)
	if model == "" || effort == "" || cwd == "" {
		return nil
	}
	key, _ := json.Marshal(cwd)    // a JSON string is a TOML basic string
	eff, _ := json.Marshal(effort) // likewise
	argv := append(command, "--no-daemon", "--no-alt-screen", "-m", model)
	argv = append(argv, options...)
	return append(argv, "-c", "projects={"+string(key)+"={trust_level=\"trusted\"}}",
		"-c", "features.hooks=false", "-c", "check_for_update_on_startup=false",
		"-c", "model_reasoning_effort="+string(eff))
}

// CodexHome makes dir a Codex home that holds only a link to the owner's
// auth.json (from $CODEX_HOME, else ~/.codex), so a reviewer run with
// CODEX_HOME=dir reads none of the owner's AGENTS.md, config or skills.
// project_doc_max_bytes=0 does not drop the user file (PR D3 fix, live).
func CodexHome(dir string) (string, error) {
	src := os.Getenv("CODEX_HOME")
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		src = filepath.Join(home, ".codex")
	}
	auth, link := filepath.Join(src, "auth.json"), filepath.Join(dir, "auth.json")
	if _, err := os.Stat(auth); err != nil {
		return "", fmt.Errorf("world: Codex is not signed in (%w)", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink == 0: // Codex replaced our link (a token refresh): it may hold the only live token
		return "", ErrCodexAuthDiverged
	default:
		if cur, _ := os.Readlink(link); cur == auth {
			return dir, nil
		}
		if err := os.Remove(link); err != nil { // only ever our own stale link
			return "", err
		}
	}
	return dir, os.Symlink(auth, link)
}

// ErrCodexAuthDiverged: the scoped home's auth.json is a file, not our link.
// CodexHome never deletes or overwrites it.
var ErrCodexAuthDiverged = errors.New("world: the scoped Codex sign-in is a file, not a link to yours")

var (
	codexPromptRe  = regexp.MustCompile(`(?m)^›[ \t\x{00a0}]`)
	codexWorkingRe = regexp.MustCompile(`^[ \t\x{00a0}]*\S[ \t\x{00a0}]+Working \(.*esc to interrupt`)
	codexIDRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
	codexReplyRe   = regexp.MustCompile(`(?m)^•[ \t]`)
)

// ReadCodexReport is ReadReport for a Codex run. Codex sends herdr-core no
// status (spike S0.3), so it reads now instead of waiting for idle: the tag
// itself ends the run. The prompt echo (it holds the tag's template) is cut
// away first: a tag counts only after a reply line below the last echo.
func ReadCodexReport(ctx context.Context, client *hc.Client, term, code string) (ReportRead, error) {
	if client == nil || strings.TrimSpace(term) == "" {
		return ReportRead{}, errors.New("world: ReadCodexReport requires hc client and terminal")
	}
	res, err := client.Read(ctx, term, reportReadLines)
	if err != nil || !res.Complete {
		return ReportRead{Status: ReportNotYet}, err
	}
	return parseReportText(codexReply(res.Text), code), nil
}

// codexReply is the text from the reply after the last prompt echo above
// the last tag, or "" when that tag is the echo's own.
func codexReply(text string) string {
	clean := ansiRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	end := strings.LastIndex(clean, "<report>")
	m := codexPromptRe.FindAllStringIndex(clean[:max(end, 0)], -1)
	if end < 0 || len(m) == 0 {
		return clean
	}
	start := m[len(m)-1][0]
	r := codexReplyRe.FindStringIndex(clean[start:end])
	if r == nil {
		return ""
	}
	return clean[start+r[0]:]
}

// Look for the current composer after the transcript. An active turn can
// draw the same composer, with the interrupt hint just above it.
func codexScreenStatus(text string, limit bool) string {
	lines := strings.Split(strings.TrimRight(text, "\n \t"), "\n")
	for _, line := range lines[max(0, len(lines)-16):] {
		if codexWorkingRe.MatchString(line) {
			return "working"
		}
	}
	if limit {
		return "idle"
	}
	tail := lines[max(0, len(lines)-8):]
	for i := len(tail) - 1; i >= 0; i-- {
		line := strings.TrimSpace(tail[i])
		if line == "" || line == "? for shortcuts" || strings.HasPrefix(tail[i], "  ") && strings.Contains(line, " · ") {
			continue // Codex's optional footer follows the composer.
		}
		if codexPromptRe.MatchString(tail[i]) {
			return "idle"
		}
		break
	}
	return "unknown"
}

// codexSession accepts only one rollout with metadata tying it to this
// launch. A missing or ambiguous rollout leaves continuation to the handoff.
func codexSession(home, cwd string, started time.Time) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || started.IsZero() {
		return ""
	}
	cwd = filepath.Clean(cwd)
	paths, err := filepath.Glob(filepath.Join(home, "sessions", "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", "rollout-*.jsonl"))
	if err != nil {
		return ""
	}
	id := ""
	for _, path := range paths {
		meta := codexRolloutMeta(path)
		if !meta.ownSession() || meta.Timestamp.Before(started) || meta.Cwd == "" || filepath.Clean(meta.Cwd) != cwd {
			continue
		}
		if id != "" {
			return ""
		}
		id = meta.ID
	}
	return id
}

type rolloutMeta struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	Timestamp      time.Time       `json:"timestamp"`
	Source         json.RawMessage `json:"source"`
	ParentThreadID string          `json:"parent_thread_id"`
}

func (m rolloutMeta) ownSession() bool {
	var src string
	return codexIDRe.MatchString(m.ID) &&
		(m.SessionID == "" || m.SessionID == m.ID) &&
		m.ParentThreadID == "" &&
		json.Unmarshal(m.Source, &src) == nil && src == "cli"
}

func codexRolloutMeta(path string) rolloutMeta {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return rolloutMeta{}
	}
	f, err := os.Open(path)
	if err != nil {
		return rolloutMeta{}
	}
	defer func() { _ = f.Close() }()
	var entry struct {
		Type    string
		Payload rolloutMeta
	}
	if json.NewDecoder(f).Decode(&entry) == nil && entry.Type == "session_meta" {
		return entry.Payload
	}
	return rolloutMeta{}
}
