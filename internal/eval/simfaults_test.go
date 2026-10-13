package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scenario's faults hit the turns they name: a failed turn falls back,
// a cut answer reaches the user as its first words only, an unvoiced one
// not at all, and the daemon records each question as unanswered.
func TestSimFaultsHitTheirTurns(t *testing.T) {
	sc := fixScenario()
	sc.Opening = ""
	sc.Script = []string{"why did it fail?", "what is bench-claude doing?", "and the tests?", "anything else?"}
	sc.Faults = &SimFaults{FailTurns: []int{1}, CutTurns: []int{2}, CutWords: 1, UnvoicedTurns: []int{3}}
	sc.Caps.MaxTurns = 6
	dir := t.TempDir()
	run := RunSim(context.Background(), sc, SimOptions{Offline: true, FixtureDir: "../../testdata/herdr", EventsDir: dir}, 1)
	if len(run.Faults) != 3 {
		t.Fatalf("faults = %+v", run.Faults)
	}
	for i, want := range []string{"fail", "cut", "unvoiced"} {
		if f := run.Faults[i]; f.Turn != i+1 || f.Kind != want {
			t.Errorf("fault %d = %+v, want %s on turn %d", i, f, want, i+1)
		}
	}
	if h := run.Faults[1]; h.Heard != "Offline" {
		t.Errorf("cut heard = %q", h.Heard)
	}
	if got := cutPrefix("bench-codex 的测试确实有一个失败了，叫 TestAdd。", 3); got != "bench-" {
		t.Errorf("cut of Chinese = %q", got)
	}
	var heard []string
	for _, l := range run.Transcript {
		if l.Who == "sheepdog" {
			heard = append(heard, l.Text)
		}
	}
	if len(heard) < 4 || !strings.Contains(heard[0], "couldn't get an answer") || heard[1] != "Offline" || heard[2] != "" || heard[3] != "Offline scripted answer." {
		t.Errorf("the user heard %q", heard)
	}
	b, err := os.ReadFile(filepath.Join(dir, sc.ID+".r1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{`"case":"fallback"`, `"case":"cut"`, `"case":"unvoiced"`} {
		if !strings.Contains(string(b), c) {
			t.Errorf("no supervisor.unanswered_recorded with %s", c)
		}
	}
}
