package factory

// TDD step 1 (round 3): failing tests for round-2 parseAgyReport findings
// and live findings A & B (Decisions C, D, E).

import (
	"encoding/json"
	"fmt"
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
	assertAgySkipPermArgv(t, "reviewer", agyReviewModel, revParams, "Review round 1", "<report>")
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

// TestAgyRound3ReviewerThoughtNoBlankAndNudgeNoBlock verifies Decision E end-to-end:
// (1) a reviewer reply immediately after `▸ Thought` + 1-line preview with no
// blank line (with a blank line before `<report>` per reviewer.md) is accepted;
// (2) an N29 nudge reply on Turn 2 with no `▸` or `●` block is accepted.
func TestAgyRound3ReviewerThoughtNoBlankAndNudgeNoBlock(t *testing.T) {
	t.Run("thought then reply with no blank line", func(t *testing.T) {
		r, revID, c1, prompt := startAgyReviewerRig(t)
		code := r.state().Tasks[0].Attempts[0].Review.Code

		var b strings.Builder
		b.WriteString("\n      ▄▀▀▄        Antigravity CLI 1.5.14\n")
		b.WriteString("     ▀▀▀▀▀▀       account@example.invalid (Google AI Ultra)\n")
		b.WriteString("    ▀▀▀▀▀▀▀▀      Gemini 4 Hydrogen (High)\n")
		b.WriteString("   ▄▀▀    ▀▀▄     ~/wt\n")
		b.WriteString("  ▄▀▀      ▀▀▄\n")
		b.WriteString("────────────────────────────────────────────────────────────\n")
		plines := strings.Split(strings.TrimRight(prompt, "\n"), "\n")
		b.WriteString("> " + plines[0] + "\n")
		for _, ln := range plines[1:] {
			b.WriteString("  " + ln + "\n")
		}
		b.WriteString("\n● Bash(git diff main..HEAD) (ctrl+o to expand)\n")
		b.WriteString("  +major: tool output must not count\n\n")
		b.WriteString("▸ Thought for 3s, 913 tokens\n")
		b.WriteString("  Checking item.go; major: thought preview must not count.\n")
		// Real agy shape: NO blank line between collapsed thought preview and reply;
		// blank line before <report> per reviewer.md.
		b.WriteString("  major: item.go:1 lacks a package doc comment.\n")
		b.WriteString("  self-check misses: 1\n")
		b.WriteString("  class: none\n")
		b.WriteString("  deletable: none\n")
		b.WriteString("  not run: none\n\n")
		fmt.Fprintf(&b, "  <report>{\"code\":%q,\"round\":1,\"head\":%q,\"verdict\":\"changes\",\"majors\":1}</report>\n\n", code, c1)
		b.WriteString("────────────────────────────────────────────────────────────────────────────────\n")
		b.WriteString("> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n")
		b.WriteString("────────────────────────────────────────────────────────────────────────────────\n")
		b.WriteString("esc to cancel                                  auto · Gemini 4 Hydrogen · high\n")

		r.srv.SetScreen(revID, b.String())
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Found one major issue."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		a := r.state().Tasks[0].Attempts[0]
		if a.Review != nil || len(a.Rounds) != 1 {
			t.Fatalf("after thought-then-reply without blank line: review=%+v rounds=%+v (logs:\n%s)", a.Review, a.Rounds, r.logs.String())
		}
		rnd := a.Rounds[0]
		if rnd.Verdict != flow.VerdictChanges || rnd.Majors != 1 || rnd.SelfCheckMisses == nil || *rnd.SelfCheckMisses != 1 {
			t.Fatalf("round 1 = %+v, want changes with 1 major and 1 miss", rnd)
		}
		body, err := os.ReadFile(rnd.ReportPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "item.go:1 lacks a package doc comment") {
			t.Errorf("saved report missing finding:\n%s", string(body))
		}
		if strings.Contains(string(body), "thought preview must not count") || strings.Contains(string(body), "tool output must not count") {
			t.Errorf("saved report leaked thought or tool output:\n%s", string(body))
		}
	})

	t.Run("N29 nudge answer with no block appended on turn 2", func(t *testing.T) {
		r, revID, c1, prompt := startAgyReviewerRig(t)
		code := r.state().Tasks[0].Attempts[0].Review.Code

		// Turn 1 ends idle without <report>.
		turn1 := formatAgyScreen(prompt, "Checked item.go; all tests pass.")
		r.srv.SetScreen(revID, turn1)
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Checked item.go; all tests pass."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass() // N29 sends nudge

		sends := sendsTo(t, r.srv.Requests(), revID)
		if len(sends) != 1 || !strings.Contains(sends[0], "You went idle with no report tag") {
			t.Fatalf("sends to agy reviewer = %q, want 1 N29 nudge", sends)
		}

		// Turn 2 appends the echoed nudge prompt and direct reply with NO ▸ or ● block.
		idxBox := strings.Index(turn1, "────────────────────────────────────────────────────────────────────────────────\n> Auto mode:")
		if idxBox < 0 {
			t.Fatalf("missing input box in turn1")
		}
		turn2 := turn1[:idxBox] +
			"────────────────────────────────────────────────────────────\n" +
			"> " + sends[0] + "\n\n" +
			"  Checked item.go and proofs; everything matches.\n" +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			fmt.Sprintf("  <report>{\"code\":%q,\"round\":1,\"head\":%q,\"verdict\":\"pass\",\"majors\":0}</report>\n\n", code, c1) +
			turn1[idxBox:]

		r.srv.SetScreen(revID, turn2)
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Added report tag."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		a := r.state().Tasks[0].Attempts[0]
		if a.Review != nil || len(a.Rounds) != 1 || a.Rounds[0].Verdict != flow.VerdictPass {
			t.Fatalf("after Turn 2 nudge reply without block: review=%+v rounds=%+v (logs:\n%s)", a.Review, a.Rounds, r.logs.String())
		}
	})
}
