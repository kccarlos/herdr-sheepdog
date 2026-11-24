package world

import (
	"context"
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
