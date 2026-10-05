package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// PromptsArm checks an alternate prompts directory (--prompts-dir, the
// prompt arm of an A/B) before a campaign: every workspace file present
// there (assistant.md, USER.md, svp/SOUL.md, svp/RULES.md, …) must be
// taken as an override, since a rejected one (a RULES.md without its
// protected sentences) would silently run the embedded text and make the
// arm a copy of the baseline. It returns the arm's line for the report:
// each file's source and SHA-256 prefix.
func PromptsArm(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("prompts dir %s: not a directory", dir)
	}
	var parts, rejected []string
	overrides := 0
	for _, f := range prompts.NewLoader(dir, nil).Files() {
		_, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Name)))
		present := statErr == nil
		if present && f.Source != prompts.SourceOverride {
			rejected = append(rejected, f.Name)
		}
		if f.Source == prompts.SourceOverride {
			overrides++
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", f.Name, f.Source, f.SHA8))
	}
	if len(rejected) > 0 {
		return "", fmt.Errorf("prompts dir %s: %s rejected (the embedded text would run instead; RULES.md must keep its protected sentences)", dir, strings.Join(rejected, ", "))
	}
	if overrides == 0 {
		return "", fmt.Errorf("prompts dir %s: no prompt file to override (want assistant.md, USER.md, svp/SOUL.md or svp/RULES.md)", dir)
	}
	return dir + ": " + strings.Join(parts, ", "), nil
}
