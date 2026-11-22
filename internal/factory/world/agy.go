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
const AgyCLI = "agy"

const agyHistoryScript = `f="$HOME/.gemini/antigravity-cli/history.jsonl"; [ -f "$f" ] && [ ! -L "$f" ] || exit 0; if [ -n "$1" ] && d=$(cd "$1" 2>/dev/null && pwd -P); then printf '%s\n' "$d"; else printf '%s\n' "$1"; fi; tail -n 200 "$f"`

// AgyArgv returns the launch argv for agy across worker, coordinator and
// reviewer, unsetting GEMINI_API_KEY so billing never switches to pay-as-you-go.
// A non-empty session adds --conversation <id>; a non-empty prompt is attached
// as -i=<prompt>.
func AgyArgv(model, session, prompt string) []string {
	model, session = strings.TrimSpace(model), strings.TrimSpace(session)
	if model == "" {
		return nil
	}
	argv := []string{"env", "-u", "GEMINI_API_KEY", AgyCLI, "--model", model, "--mode", "auto"}
	if session != "" {
		argv = append(argv, "--conversation", session)
	}
	if prompt != "" {
		argv = append(argv, "-i="+prompt)
	}
	return argv
}

// ArgvCLI returns the executable basename in argv after skipping an `env`
// wrapper, `-u <name>` flags and `K=V` assignments.
func ArgvCLI(argv []string) string {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if i == 0 && filepath.Base(a) == "env" {
			continue
		}
		if a == "-u" && i+1 < len(argv) {
			i++
			continue
		}
		if strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
			continue
		}
		return filepath.Base(a)
	}
	return ""
}

// agyEarlyAuth reports whether t is showing agy's brief startup sign-in check
// (blocked/auth inside start_minutes while still starting).
func agyEarlyAuth(rec flow.RunRecord, t hc.Terminal) bool {
	s := t.Status
	if rec.State != flow.PhaseStarting || s == nil || s.State != "blocked" || deref(s.Kind) != "auth" ||
		(rec.CLI != AgyCLI && s.App != "antigravity") {
		return false
	}
	start := rec.StartedAt
	if start.IsZero() && rec.CreatedMS > 0 {
		start = time.UnixMilli(int64(rec.CreatedMS))
	}
	if start.IsZero() {
		return false
	}
	at := time.Now()
	if ms := deref(t.StatusMS); ms > 0 {
		if st := time.UnixMilli(int64(ms)); st.After(at) {
			at = st
		}
	}
	return at.Sub(start) < time.Duration(flow.DefaultSettings().StartMinutes)*time.Minute
}

// agySession reads the worker's conversation id from agy's history.jsonl
// through herdr-core only (never the daemon's own $HOME).
func agySession(ctx context.Context, client *hc.Client, cwd string, started time.Time) string {
	cwd = strings.TrimSpace(cwd)
	if client == nil || cwd == "" || started.IsZero() {
		return ""
	}
	sec := 5.0
	res, err := client.Exec(ctx, hc.ExecParams{
		Argv:    []string{"sh", "-c", agyHistoryScript, "sh", cwd},
		Timeout: &sec,
	})
	if err != nil || res.TimedOut || (res.Code != nil && *res.Code != 0) || len(res.Stdout) == 0 {
		return ""
	}
	canon, rest, ok := strings.Cut(string(res.Stdout), "\n")
	if !ok {
		return ""
	}
	wantA, wantB, since := filepath.Clean(cwd), filepath.Clean(strings.TrimSpace(canon)), started.Truncate(time.Millisecond)
	id := ""
	for ln := range strings.Lines(rest) {
		var e struct {
			Timestamp      int64  `json:"timestamp"`
			Workspace      string `json:"workspace"`
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal([]byte(ln), &e) != nil || !codexIDRe.MatchString(e.ConversationID) ||
			e.Timestamp <= 0 || time.UnixMilli(e.Timestamp).Before(since) || e.Workspace == "" {
			continue
		}
		ws := filepath.Clean(e.Workspace)
		if ws != wantA && ws != wantB {
			continue
		}
		if id != "" && id != e.ConversationID {
			return ""
		}
		id = e.ConversationID
	}
	return id
}
