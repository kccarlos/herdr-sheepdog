package world

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// AgyCLI names Antigravity CLI in a run record or candidate.
const (
	AgyCLI           = "agy"
	agyHistoryScript = `f="$HOME/.gemini/antigravity-cli/history.jsonl"; [ -f "$f" ] && [ ! -L "$f" ] || exit 0; if [ -n "$1" ] && d=$(cd "$1" 2>/dev/null && pwd -P); then printf '%s\n' "$d"; else printf '%s\n' "$1"; fi; tail -n 200 "$f"`
)

// AgyArgv returns the launch argv for agy without GEMINI_API_KEY.
func AgyArgv(model, session, prompt string) []string {
	if model, session = strings.TrimSpace(model), strings.TrimSpace(session); model == "" {
		return nil
	}
	argv := []string{"env", "-u", "GEMINI_API_KEY", AgyCLI, "--model", model, "--dangerously-skip-permissions"}
	if session != "" {
		argv = append(argv, "--conversation", session)
	}
	if prompt != "" {
		argv = append(argv, "-i="+prompt)
	}
	return argv
}

// ArgvCLI returns the executable basename in argv after env/-u/K=V wrappers.
func ArgvCLI(argv []string) string {
	for i := 0; i < len(argv); i++ {
		if a := argv[i]; a == "-u" && i+1 < len(argv) {
			i++
		} else if (i != 0 || filepath.Base(a) != "env") && (!strings.Contains(a, "=") || strings.HasPrefix(a, "-")) {
			return filepath.Base(a)
		}
	}
	return ""
}

// agyEarlyAuth reports agy's brief startup blocked/auth check inside start_minutes.
func agyEarlyAuth(rec flow.RunRecord, t hc.Terminal) bool {
	s := t.Status
	if rec.State != flow.PhaseStarting || s == nil || s.State != "blocked" || deref(s.Kind) != "auth" || (rec.CLI != AgyCLI && s.App != "antigravity") {
		return false
	}
	start, at := rec.StartedAt, time.Now()
	if start.IsZero() && rec.CreatedMS > 0 {
		start = time.UnixMilli(int64(rec.CreatedMS))
	}
	if ms := deref(t.StatusMS); ms > 0 && time.UnixMilli(int64(ms)).After(at) {
		at = time.UnixMilli(int64(ms))
	}
	return !start.IsZero() && at.Sub(start) < time.Duration(flow.DefaultSettings().StartMinutes)*time.Minute
}

// agySession reads the worker's conversation id from agy's history.jsonl via herdr-core.
func agySession(ctx context.Context, client *hc.Client, cwd string, started time.Time) string {
	if cwd = strings.TrimSpace(cwd); client == nil || cwd == "" || started.IsZero() {
		return ""
	}
	sec := 5.0
	res, err := client.Exec(ctx, hc.ExecParams{Argv: []string{"sh", "-c", agyHistoryScript, "sh", cwd}, Timeout: &sec})
	canon, rest, ok := strings.Cut(string(res.Stdout), "\n")
	if err != nil || res.TimedOut || (res.Code != nil && *res.Code != 0) || !ok {
		return ""
	}
	wantA, wantB, since, id := filepath.Clean(cwd), filepath.Clean(strings.TrimSpace(canon)), started.Truncate(time.Millisecond), ""
	for ln := range strings.Lines(rest) {
		var e struct {
			Timestamp                 int64
			Workspace, ConversationID string
		}
		if json.Unmarshal([]byte(ln), &e) != nil || !codexIDRe.MatchString(e.ConversationID) ||
			e.Timestamp <= 0 || time.UnixMilli(e.Timestamp).Before(since) || e.Workspace == "" ||
			(filepath.Clean(e.Workspace) != wantA && filepath.Clean(e.Workspace) != wantB) {
			continue
		}
		if id != "" && id != e.ConversationID {
			return ""
		}
		id = e.ConversationID
	}
	return id
}

func isAgyTerminal(t hc.Terminal) bool {
	return t.Labels["sheepdog.cli"] == AgyCLI || ArgvCLI(t.Argv) == AgyCLI || (t.Status != nil && t.Status.App == "antigravity")
}
func isRuleLine(ln string) bool {
	return strings.HasPrefix(strings.TrimSpace(ln), "────────") && strings.Trim(strings.TrimSpace(ln), "─") == ""
}
func isAgyPromptLine(ln string) bool {
	return ln == ">" || strings.HasPrefix(ln, "> ") || strings.HasPrefix(ln, ">\t") || strings.HasPrefix(ln, ">\u00a0")
}
func isAgyBlockHeader(ln string) bool {
	return strings.HasPrefix(ln, "▸") || strings.HasPrefix(ln, "●")
}

func agyInputAnchor(lines []string) int {
	seenClose := false
	for i := len(lines) - 1; i >= max(0, len(lines)-screenTailLines); i-- {
		if isAgyBlockHeader(lines[i]) {
			return -1
		}
		if isRuleLine(lines[i]) {
			seenClose = true
		} else if seenClose && i > 0 && isAgyPromptLine(lines[i]) && isRuleLine(lines[i-1]) {
			return i - 1
		}
	}
	return -1
}

// parseAgyReport strips agy's input box, echoed `> ` prompt and `▸`/`●` blocks.
func parseAgyReport(text, code string) ReportRead {
	clean := ansiRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	lines := strings.Split(strings.TrimRight(clean, "\n \t"), "\n")
	if anc := agyInputAnchor(lines); anc >= 0 {
		lines = lines[:anc]
	}
	promptIdx, lastBlock := -1, -1
	for i, ln := range lines {
		if isAgyPromptLine(ln) && (i == 0 || isRuleLine(lines[i-1])) {
			promptIdx, lastBlock = i, -1
		} else if isAgyBlockHeader(ln) {
			lastBlock = i
		}
	}
	var out []string
	if lastBlock < 0 && promptIdx >= 0 {
		start := len(lines)
		for i := promptIdx + 1; i < len(lines); i++ {
			if strings.Contains(lines[i], `"majors":<n>}</report>`) {
				start = i + 1
				break
			}
			if start == len(lines) && strings.TrimSpace(lines[i]) == "" {
				start = i + 1
			}
		}
		out = lines[start:]
	} else {
		inBlock, afterPrompt := false, promptIdx < 0
		for i := max(promptIdx, 0); i < len(lines); i++ {
			if ln := lines[i]; isAgyBlockHeader(ln) {
				inBlock, afterPrompt = i != lastBlock || !strings.HasPrefix(ln, "▸"), true
				if !inBlock && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
					i++
				}
			} else if inBlock {
				inBlock = strings.TrimSpace(ln) != ""
			} else if afterPrompt {
				out = append(out, ln)
			}
		}
	}
	return parseReportTag(strings.TrimSpace(strings.Join(out, "\n")), code)
}
