package world

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// CodexCLI names Codex in a reviewer record (flow.Reviewer.CLI).
const CodexCLI = "codex"

// CodexArgv returns the launch argv for a Codex reviewer in cwd: model and
// effort stated, a read-only sandbox, never an approval prompt, and the run
// kept inside herdr-core (--no-daemon). Per run, through -c overrides that
// write nothing to Codex's config: cwd is trusted (no folder-trust prompt),
// hooks are off (no hooks-review prompt, and no project hook runs), and no
// update check. Spike S0.3 and PR D3's step 0. StartRun appends the prompt.
func CodexArgv(model, cwd string) []string {
	model, cwd = strings.TrimSpace(model), strings.TrimSpace(cwd)
	if model == "" || cwd == "" {
		return nil
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real // Codex keys trust by the resolved folder (step 0: /tmp → /private/tmp)
	}
	key, _ := json.Marshal(cwd) // a JSON string is a TOML basic string
	return []string{"codex", "--no-daemon", "--no-alt-screen", "-m", model, "-s", "read-only", "-a", "never",
		"-c", "projects={" + string(key) + "={trust_level=\"trusted\"}}",
		"-c", "features.hooks=false", "-c", "check_for_update_on_startup=false",
		"-c", `model_reasoning_effort="medium"`}
}

var (
	codexPromptRe = regexp.MustCompile(`(?m)^›[ \t\x{00a0}]`)
	codexReplyRe  = regexp.MustCompile(`(?m)^•[ \t]`)
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
