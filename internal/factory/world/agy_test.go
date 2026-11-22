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
	agyRule      = "────────────────────────────────────────────────────────────"
	agyInputBox  = "────────────────────────────────────────────────────────────────────────────────\n> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n────────────────────────────────────────────────────────────────────────────────\nesc to cancel                                  auto · Gemini 4 Hydrogen · high\n"
	agyReviewHdr = agyBanner + agyRule + "\n" +
		"> Do not modify, create or delete any files; read and run tests only.\n" +
		"  \n" +
		"  You review task T1: feat: agy review, before it can merge.\n" +
		"  `major:` is only for what is wrong, unsafe, loses data, breaks a caller or leaves an outcome unmet; style is at most `minor:`.\n" +
		"  Write your findings one per line, starting with `major:` or `minor:`.\n" +
		"  Then end with exactly one tag line:\n" +
		"  \n" +
		"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass|changes\",\"majors\":<n>}</report>\n\n"
	agyBlocks = "▸ Thought for 4s, 210 tokens\n" +
		"  Checking the diff; major: thought text must not count.\n" +
		"  deletable: thought text must not count.\n\n" +
		"● Bash(git diff main..HEAD) (ctrl+o to expand)\n" +
		"  +major: tool output must not count\n" +
		"  +self-check misses: 9\n" +
		"  +deletable: tool output must not count\n\n" +
		"● Read(~/wt/item.go) (ctrl+o to expand)\n\n"
)

// TestReadReportAgyScreenStripsEchoToolsThoughtsAndInputBox verifies that
// ReadReport reads an agy reviewer's reply only: the `─... / > ` prompt echo,
// `▸` thought blocks, `●` tool blocks, and agy's bottom input box are stripped,
// and an echoed template <report> without a reply tag reads as ReportNotYet.
func TestReadReportAgyScreenStripsEchoToolsThoughtsAndInputBox(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	srv.Add(hc.Terminal{
		Address: "t-agy",
		Name:    "rev-agy",
		State:   "live",
		Argv:    AgyArgv("gemini-4-hydrogen-high", "", ""),
		Labels:  map[string]string{"sheepdog.cli": AgyCLI},
		Status:  &hc.Status{App: "antigravity", State: "done"},
	})

	read := func(screen string) ReportRead {
		t.Helper()
		srv.SetScreen("t-agy", screen)
		rd, err := ReadReport(ctx, client, "t-agy", "c0de12")
		if err != nil {
			t.Fatalf("ReadReport: %v", err)
		}
		return rd
	}

	t.Run("clean pass strips echo thought tool blocks and input box", func(t *testing.T) {
		screen := agyReviewHdr + agyBlocks +
			"  Checked item.go and ran go test.\n" +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n" +
			agyInputBox
		rd := read(screen)
		if rd.Status != ReportFound || rd.Report.Verdict != "pass" || rd.Report.Majors != 0 {
			t.Fatalf("ReadReport(clean pass) = %+v, want found/pass/0", rd)
		}
		for _, leaked := range []string{"You review task", "is only for what is wrong", "Thought for 4s", "Bash(git diff", "Read(~/wt/item.go)", "self-check misses: 9", "Auto mode:", "esc to cancel"} {
			if strings.Contains(rd.Body, leaked) {
				t.Errorf("Body leaked %q:\n%s", leaked, rd.Body)
			}
		}
		if !strings.Contains(rd.Body, "Checked item.go") || !strings.Contains(rd.Body, "self-check misses: 0") {
			t.Errorf("Body missing agy reply lines:\n%s", rd.Body)
		}
	})

	t.Run("one real major line keeps only real reply", func(t *testing.T) {
		screen := agyReviewHdr + agyBlocks +
			"  major: item.go:12 misses nil guard\n" +
			"  self-check misses: 1\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"changes\",\"majors\":1}</report>\n\n" +
			agyInputBox
		rd := read(screen)
		if rd.Status != ReportFound || rd.Report.Verdict != "changes" || rd.Report.Majors != 1 {
			t.Fatalf("ReadReport(1 major) = %+v, want found/changes/1", rd)
		}
		if got := strings.Count(rd.Body, "major:"); got != 1 || !strings.Contains(rd.Body, "item.go:12 misses nil guard") {
			t.Fatalf("Body should have exactly 1 real major: line, got %d:\n%s", got, rd.Body)
		}
	})

	t.Run("turn ending without report tag reads as not_yet never invalid from echo", func(t *testing.T) {
		screen := agyReviewHdr + agyBlocks +
			"  Reviewed item.go; all checks pass.\n\n" +
			agyInputBox
		if rd := read(screen); rd.Status != ReportNotYet {
			t.Fatalf("ReadReport(no tag in reply) = %+v, want not_yet", rd)
		}
	})

	t.Run("draft tag in agy bottom input box is ignored", func(t *testing.T) {
		screen := agyReviewHdr + agyBlocks +
			"  Reading files.\n\n" +
			"────────────────────────────────────────────────────────────────────────────────\n" +
			"> <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\",\"majors\":0}</report>\n" +
			"────────────────────────────────────────────────────────────────────────────────\n" +
			"esc to cancel                                  auto · Gemini 4 Hydrogen · high\n"
		if rd := read(screen); rd.Status != ReportNotYet {
			t.Fatalf("ReadReport(draft tag in input box) = %+v, want not_yet", rd)
		}
	})
}

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

// TestReadReportAgyRound3ThoughtNoBlankAndNudgeNoBlock verifies Decision E on
// the two round-2 review failure scenarios for parseAgyReport:
// (1) a reply immediately following a `▸ Thought` + 1-line preview with no
// blank line (both with and without a blank line before `<report>`); and
// (2) a direct follow-up reply (N29 nudge or invalid_report refusal) with no
// `▸` or `●` block after the last `─ / > ` prompt echo.
func TestReadReportAgyRound3ThoughtNoBlankAndNudgeNoBlock(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	srv.Add(hc.Terminal{
		Address: "t-agy",
		Name:    "rev-agy",
		State:   "live",
		Argv:    []string{"env", "-u", "GEMINI_API_KEY", AgyCLI, "--model", "gemini-4-hydrogen-high"},
		Labels:  map[string]string{"sheepdog.cli": AgyCLI},
		Status:  &hc.Status{App: "antigravity", State: "done"},
	})

	read := func(t *testing.T, screen string) ReportRead {
		t.Helper()
		srv.SetScreen("t-agy", screen)
		rd, err := ReadReport(ctx, client, "t-agy", "c0de12")
		if err != nil {
			t.Fatalf("ReadReport: %v", err)
		}
		return rd
	}

	thoughtHeader := "● Bash(git diff main..HEAD) (ctrl+o to expand)\n" +
		"  +major: tool output must not count\n\n" +
		"▸ Thought for 3s, 913 tokens\n" +
		"  Checking item.go; major: thought preview must not count.\n"

	t.Run("thought then reply with no blank line and no blank before tag", func(t *testing.T) {
		screen := agyReviewHdr + thoughtHeader +
			"  Checked item.go and ran go test.\n" +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n" +
			agyInputBox
		rd := read(t, screen)
		if rd.Status != ReportFound || rd.Report.Verdict != "pass" || rd.Report.Majors != 0 {
			t.Fatalf("ReadReport = %+v, want found/pass/0", rd)
		}
		if !strings.Contains(rd.Body, "Checked item.go") || !strings.Contains(rd.Body, "self-check misses: 0") || !strings.Contains(rd.Body, "deletable: none") {
			t.Errorf("Body missing reply lines:\n%s", rd.Body)
		}
		if strings.Contains(rd.Body, "Thought for 3s") || strings.Contains(rd.Body, "thought preview must not count") || strings.Contains(rd.Body, "tool output must not count") {
			t.Errorf("Body leaked thought or tool block:\n%s", rd.Body)
		}
	})

	t.Run("thought then findings with no blank line and blank line before tag", func(t *testing.T) {
		screen := agyReviewHdr + thoughtHeader +
			"  major: item.go:12 misses nil guard\n" +
			"  self-check misses: 1\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"changes\",\"majors\":1}</report>\n\n" +
			agyInputBox
		rd := read(t, screen)
		if rd.Status != ReportFound || rd.Report.Verdict != "changes" || rd.Report.Majors != 1 {
			t.Fatalf("ReadReport = %+v, want found/changes/1", rd)
		}
		if got := strings.Count(rd.Body, "major:"); got != 1 || !strings.Contains(rd.Body, "item.go:12 misses nil guard") {
			t.Errorf("Body should have 1 real major line, got %d:\n%s", got, rd.Body)
		}
		if !strings.Contains(rd.Body, "self-check misses: 1") || !strings.Contains(rd.Body, "deletable: none") {
			t.Errorf("Body swallowed findings/self-check lines above blank line:\n%s", rd.Body)
		}
	})

	t.Run("real captured agy-edit-prompt thought-to-reply fixture", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "herdr", "agy-edit-prompt.screen.txt"))
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		if len(all) < 15 || !strings.HasPrefix(all[11], "▸ Thought") {
			t.Fatalf("unexpected agy-edit-prompt.screen.txt layout: %q", all[:min(15, len(all))])
		}
		prefix := strings.Join(all[:15], "\n") + "\n"
		screen := prefix +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n" +
			agyInputBox
		rd := read(t, screen)
		if rd.Status != ReportFound || rd.Report.Verdict != "pass" || rd.Report.Majors != 0 {
			t.Fatalf("ReadReport(agy-edit-prompt) = %+v, want found/pass/0", rd)
		}
		if !strings.Contains(rd.Body, "mathlib_test.go:11-15") || !strings.Contains(rd.Body, "self-check misses: 0") {
			t.Errorf("Body missing reply lines after thought:\n%s", rd.Body)
		}
		if strings.Contains(rd.Body, "TestClampUpper") || strings.Contains(rd.Body, "Thought for 3s") {
			t.Errorf("Body leaked collapsed thought preview:\n%s", rd.Body)
		}
	})

	t.Run("N29 nudge answer with no block reads reply and tag", func(t *testing.T) {
		turn1 := agyReviewHdr + agyBlocks + "  Checked item.go.\n\n"
		nudgeTurn := agyRule + "\n" +
			"> You went idle with no report tag. End your reply with `<report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\"|\"changes\",\"majors\":N}</report>`.\n\n" +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n" +
			agyInputBox
		rd := read(t, turn1+nudgeTurn)
		if rd.Status != ReportFound || rd.Report.Verdict != "pass" || rd.Report.Majors != 0 {
			t.Fatalf("ReadReport(N29 nudge no block) = %+v, want found/pass/0", rd)
		}
		if !strings.Contains(rd.Body, "self-check misses: 0") || !strings.Contains(rd.Body, "deletable: none") {
			t.Errorf("Body missing nudge reply lines:\n%s", rd.Body)
		}
		if strings.Contains(rd.Body, "You went idle") || strings.Contains(rd.Body, "Checked item.go.") || strings.Contains(rd.Body, "Bash(git diff") {
			t.Errorf("Body leaked prompt echo or turn 1:\n%s", rd.Body)
		}
	})

	t.Run("refusal retry answer with no block reads reply and tag", func(t *testing.T) {
		turn1 := agyReviewHdr + agyBlocks + "  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"changes\",\"majors\":1}</report>\n\n"
		retryTurn := agyRule + "\n" +
			"> Your verdict was refused (The reply must include a self-check misses: <n> line.). Fix it and end with the report tag again.\n\n" +
			"  major: item.go:12 misses nil guard\n" +
			"  self-check misses: 1\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			"  <report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"changes\",\"majors\":1}</report>\n\n" +
			agyInputBox
		rd := read(t, turn1+retryTurn)
		if rd.Status != ReportFound || rd.Report.Verdict != "changes" || rd.Report.Majors != 1 {
			t.Fatalf("ReadReport(refusal retry no block) = %+v, want found/changes/1", rd)
		}
		if got := strings.Count(rd.Body, "major:"); got != 1 || !strings.Contains(rd.Body, "item.go:12 misses nil guard") || !strings.Contains(rd.Body, "self-check misses: 1") {
			t.Errorf("Body missing refusal retry lines:\n%s", rd.Body)
		}
		if strings.Contains(rd.Body, "Your verdict was refused") || strings.Contains(rd.Body, "Bash(git diff") {
			t.Errorf("Body leaked refusal prompt or turn 1:\n%s", rd.Body)
		}
	})

	t.Run("N29 nudge reply without tag still reads as not_yet", func(t *testing.T) {
		turn1 := agyReviewHdr + agyBlocks + "  Checked item.go.\n\n"
		nudgeNoTag := agyRule + "\n" +
			"> You went idle with no report tag. End your reply with `<report>{\"code\":\"c0de12\",\"round\":1,\"head\":\"abc123\",\"verdict\":\"pass\"|\"changes\",\"majors\":N}</report>`.\n\n" +
			"  Re-checking the proof.\n\n" +
			agyInputBox
		if rd := read(t, turn1+nudgeNoTag); rd.Status != ReportNotYet {
			t.Fatalf("ReadReport(N29 nudge without reply tag) = %+v, want not_yet", rd)
		}
	})
}
