// Package demo is `sheepdog demo` (onboarding tier 0): the daemon over a
// mock herdr whose three scripted workers — one finishes, one asks for
// an approval and then fails, one finishes later — follow an eval sim
// scenario embedded here, so a visitor hears announcements, answers a
// prompt and asks "what's going on" without herdr or any real agent.
//
// It is a scratch daemon (internal/bench StartDaemon): loopback only,
// its own port and data dir, no phone notifications, the codex/claude
// CLIs pointed at files that do not exist, and herdr in mock mode with a
// socket path inside the demo's dir, so it never touches real panes and
// runs safely next to a live daemon. The workers are driven over HTTP
// by internal/bench/world, as the voice benchmark drives them.
package demo

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// DefaultPort is the demo's port: not the daemon's 7777, not the
// bench's 7790–7792.
const DefaultPort = 7778

//go:embed data
var data embed.FS

// Scenario writes the embedded scenario and its screens under dir
// (dir/demo.json, dir/screens/) and loads it. The screens directory is
// also the world's fixture directory.
func Scenario(dir string) (sc eval.SimScenario, screens string, err error) {
	err = fs.WalkDir(data, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("data", path)
		out := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := data.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return eval.SimScenario{}, "", err
	}
	scs, err := eval.LoadSimFile(filepath.Join(dir, "demo.json"))
	if err != nil {
		return eval.SimScenario{}, "", err
	}
	return scs[0], filepath.Join(dir, "screens"), nil
}
