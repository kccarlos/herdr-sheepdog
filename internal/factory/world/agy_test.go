package world

import (
	"context"
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

const (
	agyBanner = "\n" +
		"      ▄▀▀▄        Antigravity CLI 1.5.14\n" +
		"     ▀▀▀▀▀▀       account@example.invalid (Google AI Ultra)\n" +
		"    ▀▀▀▀▀▀▀▀      Gemini 4 Hydrogen (High)\n" +
		"   ▄▀▀    ▀▀▄     ~/wt\n" +
		"  ▄▀▀      ▀▀▄\n"
	agyRule     = "────────────────────────────────────────────────────────────"
	agyInputBox = "────────────────────────────────────────────────────────────────────────────────\n> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n────────────────────────────────────────────────────────────────────────────────\nesc to cancel                                  auto · Gemini 4 Hydrogen · high\n"
)

// TestAgyScreenClassScopesLimitToBottomUIAndStripsFooterTail verifies O23 for
// agy: quoted limit strings in agy's transcript or tool output never classify
// as ScreenLimit, while a genuine column-0 limit banner does, and ObserveRun's
// EvTick.Tail excludes agy's bottom input/status bar.
func TestAgyScreenClassScopesLimitToBottomUIAndStripsFooterTail(t *testing.T) {
	quotedWithBox := agyBanner + agyRule + "\n" +
		"> Check world.go for \"Individual quota reached\" and \"You've hit your limit\".\n\n" +
		"▸ Thought for 3s, 120 tokens\n" +
		"  Inspecting screenClassify in world.go.\n\n" +
		"● Read(~/repo/internal/factory/world/world.go)\n" +
		"  You've hit your limit · resets 2pm (America/Los_Angeles)\n" +
		"  Individual quota reached · resets 3pm\n\n" +
		"  Done checking world.go; both limit patterns are present.\n\n" +
		agyInputBox

	quotedNoBox := "● Read(~/repo/internal/factory/world/world.go)\n" +
		"  You've hit your limit · resets 2pm (America/Los_Angeles)\n" +
		"  Individual quota reached · resets 3pm\n\n" +
		"  Done checking world.go.\n"

	for _, tc := range []struct {
		name, screen, want string
	}{
		{"quoted limit in agy output with input box", quotedWithBox, flow.ScreenNone},
		{"quoted limit in agy output without input box", quotedNoBox, flow.ScreenNone},
		{"real column-0 agy limit banner", "Individual quota reached · resets 3pm (America/Los_Angeles)\n", flow.ScreenLimit},
		{"real column-0 agy limit banner above input box", "Individual quota reached · resets 3pm (America/Los_Angeles)\n\n" + agyInputBox, flow.ScreenLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScreenClass(AgyCLI, tc.screen); got != tc.want {
				t.Fatalf("ScreenClass(agy) = %q, want %q", got, tc.want)
			}
		})
	}

	// ObserveRun Tail (LastChat) must strip agy's bottom input box and footer.
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	srv.Add(hc.Terminal{
		Address:   "t-tail",
		State:     "live",
		CreatedMS: 1000,
		Labels:    map[string]string{"sheepdog.run": "r1", "sheepdog.cli": AgyCLI},
		Status:    &hc.Status{App: "antigravity", State: "working"},
	})
	srv.SetScreen("t-tail", quotedWithBox)
	evs, err := ObserveRun(ctx, client, flow.RunRecord{
		ID: "r1", Address: "t-tail", CreatedMS: 1000, Role: flow.RoleWorker, CLI: AgyCLI,
		State: flow.PhaseWorking, StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.IndexFunc(evs, func(e flow.RunEvent) bool { return e.Kind == flow.EvTick })
	if idx < 0 {
		t.Fatalf("missing EvTick in %+v", evs)
	}
	tail := evs[idx].Tail
	if !strings.Contains(tail, "Done checking world.go") {
		t.Errorf("EvTick.Tail missing worker reply:\n%s", tail)
	}
	if strings.Contains(tail, "Auto mode:") || strings.Contains(tail, "esc to cancel") {
		t.Errorf("EvTick.Tail leaked agy footer/input box:\n%s", tail)
	}
}

// TestAgyRound3ArgvDangerouslySkipPermissionsAndNoGeminiAPIKey verifies
// Decision C on AgyArgv: fresh and resumed argvs include
// --dangerously-skip-permissions and never carry GEMINI_API_KEY.
func TestAgyRound3ArgvDangerouslySkipPermissionsAndNoGeminiAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "paid-key-must-not-leak")
	for _, tc := range []struct {
		name, session string
	}{
		{"fresh", ""},
		{"resumed", "70f9969a-d48c-40e4-8f30-5d6e6d0ffe7b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := AgyArgv("gemini-4-carbon-medium", tc.session, "")
			if len(argv) < 6 || argv[0] != "env" || argv[1] != "-u" || argv[2] != "GEMINI_API_KEY" || argv[3] != AgyCLI {
				t.Fatalf("AgyArgv(%q) = %q, want env -u GEMINI_API_KEY agy ...", tc.session, argv)
			}
			if !slices.Contains(argv, "--dangerously-skip-permissions") {
				t.Fatalf("AgyArgv(%q) = %q, want --dangerously-skip-permissions", tc.session, argv)
			}
			if tc.session != "" && !slices.Contains(argv, "--conversation") {
				t.Fatalf("resumed AgyArgv(%q) = %q, want --conversation", tc.session, argv)
			}
			for i, a := range argv {
				if i == 2 {
					continue
				}
				if strings.Contains(a, "GEMINI_API_KEY") || strings.Contains(a, "paid-key-must-not-leak") {
					t.Errorf("argv[%d] leaked GEMINI_API_KEY: %q", i, a)
				}
			}
		})
	}
}

func hardWrapRunes(line string, width int) string {
	r := []rune(line)
	if len(r) <= width || width <= 0 {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(r); i += width {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(r[i:min(i+width, len(r))]))
	}
	return b.String()
}

// TestObserveRunCoordWakeEvidenceRealCaptures pins world-level ObserveRun
// coordinator wake evidence (EvTick.WakeSeq, WakeConfirmed, WakeReason) against
// real Antigravity CLI 1.5.18 and Claude Code screen captures:
//   - testdata/agy-esc-handled-833.screen.txt (live PR #833 capture, numbered #25/#27)
//   - testdata/agy-esc-handled.screen.txt (live 829-live-r3 capture)
//   - testdata/agy-esc-active.screen.txt & agy-esc-interrupted.screen.txt (live 829-live-r2)
//   - ../../../testdata/herdr/claude-bash-prompt-44col.screen.txt (44-col Claude interrupt)
func TestObserveRunCoordWakeEvidenceRealCaptures(t *testing.T) {
	readFixture := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	activeScreen := readFixture(filepath.Join("testdata", "agy-esc-active.screen.txt"))
	interruptedScreen := readFixture(filepath.Join("testdata", "agy-esc-interrupted.screen.txt"))
	handled829 := readFixture(filepath.Join("testdata", "agy-esc-handled.screen.txt"))
	handled833 := readFixture(filepath.Join("testdata", "agy-esc-handled-833.screen.txt"))
	claude44 := readFixture(filepath.Join("..", "..", "..", "testdata", "herdr", "claude-bash-prompt-44col.screen.txt"))

	// Bind numbered wake headers onto the pre-PR2b 829 captures while preserving all captured lines.
	active25 := strings.Replace(activeScreen, "> Wake: 1 task wake(s) wait on you.", "> Wake: #25 1 task wake(s) wait on you.", 1)
	interrupted25 := strings.Replace(interruptedScreen, "> Wake: 1 task wake(s) wait on you.", "> Wake: #25 1 task wake(s) wait on you.", 1)
	handled829Bound := strings.Replace(handled829, "> Wake: 1 task wake(s) wait on you.", "> Wake: #41 1 task wake(s) wait on you.", 1)
	handled829Bound = strings.Replace(handled829Bound, "Answer in one line.Wake: 1 task wake(s) wait on you.", "Answer in one line.Wake: #42 1 task wake(s) wait on you.", 1)

	// Derive initial-wake-behind-composer-draft directly from real agy-esc-handled-833.screen.txt
	// by removing the earlier #25 block so #27 behind the composer draft is the sole wake.
	wake25Mark := "\n" + agyRule + "\n> Wake: #25 "
	wake27Mark := "\n" + agyRule + "\n> What are you doing next? Answer in one line.Wake: #27 "
	w25Idx := strings.Index(handled833, wake25Mark)
	w27Idx := strings.Index(handled833, wake27Mark)
	if w25Idx < 0 || w27Idx < 0 || w27Idx <= w25Idx {
		t.Fatalf("agy-esc-handled-833.screen.txt missing expected #25/#27 markers: w25=%d w27=%d", w25Idx, w27Idx)
	}
	initialComposerDraft := handled833[:w25Idx] + handled833[w27Idx:]

	// Guard case derived from real agy-esc-interrupted.screen.txt: a later owner prompt casually
	// mentioning "Wake: #99" without "Settings that differ" must not false-match as wake #99.
	boxStart := strings.Index(interrupted25, "\n────────────────────────────────────────────────────────────────────────────────")
	if boxStart < 0 {
		t.Fatal("agy-esc-interrupted.screen.txt missing bottom input box rule")
	}
	emptyBox := strings.Replace(interrupted25[boxStart+1:], "> What are you doing next? Answer in one line.", ">", 1)
	ownerMentionsWakeNoSettings := interrupted25[:boxStart] + "\n\n" +
		agyRule + "\n" +
		"> why did Wake: #99 fail?\n\n" +
		"  It was interrupted before finishing.\n" +
		emptyBox

	// Claude 44-col narrow-pane interrupt: embed lines 1-4 of real testdata/herdr/claude-bash-prompt-44col.screen.txt
	// verbatim after a numbered Claude wake prompt.
	claude44TurnEnd := strings.Index(claude44, "\n❯ Use the Bash tool")
	if claude44TurnEnd < 0 {
		t.Fatal("claude-bash-prompt-44col.screen.txt missing second prompt boundary")
	}
	claude44Interrupted := "❯ Wake: #42 1 task wake(s) wait on you.\n" +
		"  - T1: worker_failed.\n" +
		"  Settings that differ from their default: none.\n\n" +
		claude44[:claude44TurnEnd] + "\n" +
		"────────────────────────────────────────────\n❯ \n────────────────────────────────────────────\n"

	// Narrow-pane agy interrupt after partial tool output: derived from real agy-esc-interrupted.screen.txt
	// by taking its real `● Bash(...)` line and real `  ⎿  Interrupted · What should Antigravity CLI do instead?`
	// line and wrapping to 44 columns (PTY hard-wrap matching iPhone portrait width) and 50 columns (word-wrap).
	const realAgyInterrupt = "  ⎿  Interrupted · What should Antigravity CLI do instead?"
	var realAgyToolLine string
	for _, ln := range strings.Split(interruptedScreen, "\n") {
		if strings.HasPrefix(ln, "● Bash(") {
			realAgyToolLine = ln
			break
		}
	}
	if realAgyToolLine == "" || !strings.Contains(interrupted25, realAgyInterrupt) {
		t.Fatal("agy-esc-interrupted.screen.txt missing real tool or interrupt line")
	}
	agy44Wrapped := strings.Replace(
		interrupted25,
		realAgyInterrupt,
		hardWrapRunes(realAgyToolLine, 44)+"\n"+hardWrapRunes(realAgyInterrupt, 44),
		1,
	)
	agy50Wrapped := strings.Replace(
		interrupted25,
		realAgyInterrupt,
		realAgyToolLine+"\n  ⎿  Interrupted · What should Antigravity CLI do\n     instead?",
		1,
	)

	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)

	for _, tc := range []struct {
		name          string
		cli           string
		role          string
		screen        string
		wantSeq       int64
		wantConfirmed bool
		wantReason    string
	}{
		{
			name:          "agy_833_live_handled_populated_composer",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        handled833,
			wantSeq:       27,
			wantConfirmed: true,
			wantReason:    "",
		},
		{
			name:          "agy_829_r3_handled_populated_composer",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        handled829Bound,
			wantSeq:       42,
			wantConfirmed: true,
			wantReason:    "",
		},
		{
			name:          "agy_initial_wake_behind_composer_draft",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        initialComposerDraft,
			wantSeq:       27,
			wantConfirmed: true,
			wantReason:    "",
		},
		{
			name:          "agy_esc_active_queued_owner_prompt_not_confirmed",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        active25,
			wantSeq:       25,
			wantConfirmed: false,
			wantReason:    "missing reply after wake echo",
		},
		{
			name:          "agy_esc_interrupted_queued_composer",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        interrupted25,
			wantSeq:       25,
			wantConfirmed: false,
			wantReason:    "wake reply interrupted",
		},
		{
			name:          "agy_owner_prompt_mentioning_wake_without_settings_ignored",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        ownerMentionsWakeNoSettings,
			wantSeq:       25,
			wantConfirmed: false,
			wantReason:    "wake reply interrupted",
		},
		{
			name:          "claude_44col_real_interrupted_capture",
			cli:           "claude",
			role:          flow.RoleCoordinator,
			screen:        claude44Interrupted,
			wantSeq:       42,
			wantConfirmed: false,
			wantReason:    "wake reply interrupted",
		},
		{
			name:          "agy_44col_hard_wrapped_interrupted_after_tool_output",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        agy44Wrapped,
			wantSeq:       25,
			wantConfirmed: false,
			wantReason:    "wake reply interrupted",
		},
		{
			name:          "agy_50col_word_wrapped_interrupted_after_tool_output",
			cli:           AgyCLI,
			role:          flow.RoleCoordinator,
			screen:        agy50Wrapped,
			wantSeq:       25,
			wantConfirmed: false,
			wantReason:    "wake reply interrupted",
		},
		{
			name:          "worker_role_ignores_coordinator_wake_evidence",
			cli:           AgyCLI,
			role:          flow.RoleWorker,
			screen:        handled833,
			wantSeq:       0,
			wantConfirmed: false,
			wantReason:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := "antigravity"
			if tc.cli == "claude" {
				app = "claude-code"
			}
			addr := "t-" + tc.name
			srv.Add(hc.Terminal{
				Address:   addr,
				State:     "live",
				CreatedMS: 1000,
				Labels:    map[string]string{"sheepdog.run": "r-" + addr, "sheepdog.cli": tc.cli},
				Status:    &hc.Status{App: app, State: "idle"},
			})
			srv.SetScreen(addr, tc.screen)
			evs, err := ObserveRun(ctx, client, flow.RunRecord{
				ID: "r-" + addr, Address: addr, CreatedMS: 1000, Role: tc.role, CLI: tc.cli,
				State: flow.PhaseWorking, StartedAt: time.Now(),
			}, RunObservation{PendingWake: tc.role == flow.RoleCoordinator})
			if err != nil {
				t.Fatal(err)
			}
			idx := slices.IndexFunc(evs, func(e flow.RunEvent) bool { return e.Kind == flow.EvTick })
			if idx < 0 {
				t.Fatalf("missing EvTick in %+v", evs)
			}
			got := evs[idx]
			if got.WakeSeq != tc.wantSeq || got.WakeConfirmed != tc.wantConfirmed || got.WakeReason != tc.wantReason {
				t.Fatalf("ObserveRun EvTick wake evidence = (seq=%d, confirmed=%v, reason=%q), want (seq=%d, confirmed=%v, reason=%q)",
					got.WakeSeq, got.WakeConfirmed, got.WakeReason, tc.wantSeq, tc.wantConfirmed, tc.wantReason)
			}
		})
	}
}
