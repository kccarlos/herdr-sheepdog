package demo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The embedded scenario loads and tells the demo's story: one worker
// finishes, one asks for an approval and fails only once approved, one
// finishes later; every worker can answer an investigation.
func TestScenario(t *testing.T) {
	dir := t.TempDir()
	sc, screens, err := Scenario(dir)
	if err != nil {
		t.Fatal(err)
	}
	if screens != filepath.Join(dir, "screens") {
		t.Errorf("screens = %s", screens)
	}
	if len(sc.Agents) != 3 {
		t.Fatalf("agents = %+v", sc.Agents)
	}
	for _, a := range sc.Agents {
		if a.State != "working" || a.Answer == "" {
			t.Errorf("agent %s: state %q, answer %q", a.Alias, a.State, a.Answer)
		}
	}
	resolved, err := eval.ResolveScreens(sc, screens)
	if err != nil {
		t.Fatal(err)
	}
	var blocked, approveCues, done int
	for _, e := range sc.Events {
		switch {
		case e.State == "blocked":
			blocked++
			if _, ok := herdr.ParsePrompt(resolved[e.Screen]); !ok {
				t.Errorf("the approval screen %s is not a prompt the daemon recognises", e.Screen)
			}
		case e.OnKeysTo != "":
			if e.KeysAre != eval.KeysApprove {
				t.Errorf("cue %+v moves on a decline too", e)
			}
			approveCues++
		}
		if e.State == "done" {
			done++
		}
	}
	if blocked != 1 || approveCues != 2 || done != 3 {
		t.Errorf("blocked %d, approve cues %d, done %d", blocked, approveCues, done)
	}
	if _, err := os.Stat(filepath.Join(screens, "tests-fail.txt")); err != nil {
		t.Error(err)
	}
}
