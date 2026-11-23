package factory

// TDD step 1 (round 3): failing tests for round-2 parseAgyReport findings
// and live findings A & B (Decisions C, D, E).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func assertAgySkipPermArgv(t *testing.T, role, model string, p hc.RunParams, wantPromptParts ...string) {
	t.Helper()
	wantPrefix := []string{"env", "-u", "GEMINI_API_KEY", "agy"}
	if len(p.Argv) < len(wantPrefix)+3 || !slices.Equal(p.Argv[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("%s argv = %q, want prefix %q", role, p.Argv, wantPrefix)
	}
	if !slices.Contains(p.Argv, "--dangerously-skip-permissions") {
		t.Fatalf("%s argv = %q, want --dangerously-skip-permissions", role, p.Argv)
	}
	if got := argValue(p.Argv, "--model"); got != model {
		t.Fatalf("%s --model = %q, want %q (argv=%q)", role, got, model, p.Argv)
	}
	prompt, ok := strings.CutPrefix(p.Argv[len(p.Argv)-1], "-i=")
	if !ok || strings.TrimSpace(prompt) == "" {
		t.Fatalf("%s last argv element = %q, want non-empty -i=<prompt>", role, p.Argv[len(p.Argv)-1])
	}
	for _, part := range wantPromptParts {
		if !strings.Contains(prompt, part) {
			t.Errorf("%s -i= prompt lacks %q:\n%s", role, part, prompt)
		}
	}
	assertNoGeminiAPIKey(t, role, p)
}

// TestAgyRound3AllRolesArgvSkipPermissionsAndNoGeminiKey verifies Decision C:
// agy coordinator, worker, and reviewer all start with --dangerously-skip-permissions,
// never pass GEMINI_API_KEY, and the reviewer keeps its read-only instruction.
func TestAgyRound3AllRolesArgvSkipPermissionsAndNoGeminiKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "paid-key-must-not-leak")
	const wantReadOnly = "do not modify, create or delete any files; read and run tests only"

	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: "agy", Model: agyCoordModel, Effort: "medium"}},
		flow.RoleWorker:      {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
		flow.RoleReviewer:    {{CLI: "agy", Model: agyReviewModel, Effort: "high"}},
	})

	// 1. Coordinator launch (J7).
	r.pass()
	if len(*r.runs) != 1 {
		t.Fatalf("coordinator did not launch on agy: %d runs", len(*r.runs))
	}
	coordParams := r.last()
	assertAgySkipPermArgv(t, "coordinator", agyCoordModel, coordParams, "Ship agy support", "plan apply")

	// 2. Worker launch (N11 -> N23).
	coordTok := coordParams.Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy skip perms","brief":"Add hello.go.",` + testScopeJSON + `,"outcomes":[{"text":"hello exists","proved_by":"manual: hello.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 worker
	if len(*r.runs) != 2 {
		t.Fatalf("worker did not launch on agy: %d runs", len(*r.runs))
	}
	workerParams := r.last()
	wt := r.state().Tasks[0].Attempts[0].Worktree
	assertAgySkipPermArgv(t, "worker", agyWorkerModel, workerParams, "T1", "Add hello.go.", "factory report ready", wt)

	// 3. Reviewer launch (ready -> N26 -> N29).
	if err := os.WriteFile(filepath.Join(wt, "hello.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "hello.go")
	gitIn(t, wt, "commit", "-m", "feat: add hello")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	workerTok := workerParams.Env["SHEEPDOG_RUN_TOKEN"]
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"hello exists","artifact":"hello.go"}],"touched":"hello.go","skipped":""}`)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	r.pass() // N26
	writePRJSON(t, r.gh, "view.json", c1, greenRollup)
	r.pass() // N29
	if len(*r.runs) != 3 {
		t.Fatalf("reviewer did not launch on agy: %d runs", len(*r.runs))
	}
	revParams := r.last()
	assertAgySkipPermArgv(t, "reviewer", agyReviewModel, revParams, "Review round 1", "factory report review")
	prompt, _ := strings.CutPrefix(revParams.Argv[len(revParams.Argv)-1], "-i=")
	if !strings.Contains(strings.ToLower(prompt), wantReadOnly) {
		t.Fatalf("agy reviewer -i= first prompt lacks %q:\n%s", wantReadOnly, prompt)
	}
}

func terminalKeyCalls(reqs []hctest.Request, addr string) [][]string {
	var out [][]string
	for _, q := range reqs {
		var p struct {
			Terminal string   `json:"terminal"`
			Keys     []string `json:"keys"`
		}
		if q.Method == "terminal.keys" && json.Unmarshal(q.Params, &p) == nil && p.Terminal == addr {
			out = append(out, p.Keys)
		}
	}
	return out
}

// TestAgyRound3CoordinatorTrustDialogGetsOneKey verifies Decision D (Live
// Finding B): an agy coordinator started in the project folder gets the 5b.4
// Enter key once on its verified bottom trust dialog, including when the first
// pass sees only the initial "Accessing workspace:" header under
// blocked(permission) before the rest of the dialog finishes drawing.
func TestAgyRound3CoordinatorTrustDialogGetsOneKey(t *testing.T) {
	trust := readScreen(t, "agy-trust-dialog.screen.txt")

	t.Run("full verified trust dialog on coordinator start", func(t *testing.T) {
		r := newAgyRig(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "agy", Model: agyCoordModel, Effort: "medium"}},
		})
		r.pass() // J7 starts agy coordinator
		coordID := r.state().Project.RunID
		if coordID == "" {
			t.Fatalf("agy coordinator did not start: %+v", r.state().Project)
		}
		r.srv.SetScreen(coordID, trust)
		r.srv.SetStatus(coordID, permission())

		*r.now = r.now.Add(15 * time.Second)
		r.pass()
		keys := terminalKeyCalls(r.srv.Requests(), coordID)
		if len(keys) != 1 || !slices.Equal(keys[0], []string{"enter"}) {
			t.Fatalf("keys sent to coordinator = %v, want exactly [[enter]]", keys)
		}
		if run := r.state().Project.Run; run == nil || run.State != flow.PhaseStarting || !slices.Equal(run.ScreensAnswered, []string{flow.ScreenTrust}) {
			t.Fatalf("coordinator run after trust key = %+v, want starting with [trust] answered", run)
		}
		// Subsequent passes on the same dialog send no second key.
		*r.now = r.now.Add(15 * time.Second)
		r.pass()
		if keys := terminalKeyCalls(r.srv.Requests(), coordID); len(keys) != 1 {
			t.Fatalf("keys sent after second pass = %v, want still 1", keys)
		}
	})

	t.Run("accessing workspace header before full trust dialog under blocked permission", func(t *testing.T) {
		r := newAgyRig(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "agy", Model: agyCoordModel, Effort: "medium"}},
		})
		r.pass() // J7 starts agy coordinator
		coordID := r.state().Project.RunID
		if coordID == "" {
			t.Fatalf("agy coordinator did not start: %+v", r.state().Project)
		}

		// Pass 1: agy has only printed lines 1-3 of agy-trust-dialog.screen.txt while blocked/permission is reported.
		r.srv.SetScreen(coordID, "Accessing workspace:\n\n"+r.repo+"\n")
		r.srv.SetStatus(coordID, permission())
		*r.now = r.now.Add(15 * time.Second)
		r.pass()
		if keys := terminalKeyCalls(r.srv.Requests(), coordID); len(keys) != 0 {
			t.Fatalf("keys sent before full dialog drew = %v, want 0", keys)
		}
		if run := r.state().Project.Run; run == nil || run.State == flow.PhaseBlocked || run.State == flow.PhaseEnded {
			t.Fatalf("coordinator trapped before full trust dialog drew: %+v", run)
		}

		// Pass 2: the rest of the trust dialog renders; coordinator must get exactly one Enter.
		r.srv.SetScreen(coordID, trust)
		*r.now = r.now.Add(15 * time.Second)
		r.pass()
		keys := terminalKeyCalls(r.srv.Requests(), coordID)
		if len(keys) != 1 || !slices.Equal(keys[0], []string{"enter"}) {
			t.Fatalf("keys sent after full trust dialog rendered = %v, want exactly [[enter]]", keys)
		}
		if run := r.state().Project.Run; run == nil || run.State != flow.PhaseStarting || !slices.Equal(run.ScreensAnswered, []string{flow.ScreenTrust}) {
			t.Fatalf("coordinator run after answering trust dialog = %+v, want starting with [trust] answered", run)
		}

		// Pass 3: no second key even if the dialog stays visible.
		*r.now = r.now.Add(15 * time.Second)
		r.pass()
		if keys := terminalKeyCalls(r.srv.Requests(), coordID); len(keys) != 1 {
			t.Fatalf("keys sent on later pass = %v, want still 1", keys)
		}
	})
}
