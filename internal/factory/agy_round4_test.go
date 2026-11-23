package factory

// TDD step 1 (round 4): failing tests for agy's Markdown-rendered reviewer
// reply hiding <report>...</report> tags and leaving the bare JSON line, and
// asking the agy reviewer for a fenced code block in its first prompt
// (design.md 9.2 round 4).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestAgyRound4ReviewerFirstPromptAsksForFencedCodeBlock verifies that an agy
// reviewer's first prompt (-i=<prompt>) asks agy to put the report tag inside
// a fenced code block while keeping the read-only instruction and <report>
// template.
func TestAgyRound4ReviewerFirstPromptAsksForFencedCodeBlock(t *testing.T) {
	r, _, _, prompt := startAgyReviewerRig(t)
	_ = r
	low := strings.ToLower(prompt)
	if !strings.Contains(low, "do not modify, create or delete any files; read and run tests only") {
		t.Fatalf("agy reviewer first prompt missing read-only instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<report>") {
		t.Fatalf("agy reviewer first prompt missing <report> template:\n%s", prompt)
	}
	if !strings.Contains(low, "code block") && !strings.Contains(low, "fenced") && !strings.Contains(prompt, "```") {
		t.Fatalf("agy reviewer first prompt does not ask for a fenced code block:\n%s", prompt)
	}
}

// TestAgyRound4HiddenReportTagEndToEnd verifies design.md 9.2 round 4 through
// the Engine loop:
// (1) the captured hidden-tag reply (bare JSON line with matching code, no
// visible <report> tags) is accepted as pass/0 without triggering an N29 nudge;
// (2) a bare JSON line with a different code is ignored (N29 nudges once,
// never invalid_report), and a matching bare JSON line on turn 2 is accepted;
// (3) a visible <report> tag inside a fenced code block still works.
func TestAgyRound4HiddenReportTagEndToEnd(t *testing.T) {
	hiddenBytes, err := os.ReadFile(filepath.Join("testdata", "agy-review-hidden-tag.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	beforeNudge, _, hasNudge := strings.Cut(strings.ReplaceAll(string(hiddenBytes), "\r\n", "\n"), "\n────────")
	if !hasNudge {
		t.Fatalf("unexpected testdata/agy-review-hidden-tag.screen.txt layout")
	}

	t.Run("hidden-tag capture accepted as pass without nudge", func(t *testing.T) {
		r, revID, c1, prompt := startAgyReviewerRig(t)
		code := r.state().Tasks[0].Attempts[0].Review.Code

		// Replay the real Turn 1 capture shape (prompt echo + Bash/Thought + bare JSON line).
		turn1Reply := strings.ReplaceAll(beforeNudge, "matn62unlw", code)
		turn1Reply = strings.ReplaceAll(turn1Reply, "c0d13cc143ceb3633bf4f46deb50527a40ca01e7", c1)

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
		b.WriteString("\n")
		b.WriteString(turn1Reply)
		b.WriteString("\n\n")
		b.WriteString("────────────────────────────────────────────────────────────────────────────────\n")
		b.WriteString("> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n")
		b.WriteString("────────────────────────────────────────────────────────────────────────────────\n")
		b.WriteString("esc to cancel                                  auto · Gemini 4 Hydrogen · high\n")

		r.srv.SetScreen(revID, b.String())
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "self-check misses: 0"})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		if sends := sendsTo(t, r.srv.Requests(), revID); len(sends) != 0 {
			t.Fatalf("unexpected N29 nudge on hidden-tag reply: %q", sends)
		}
		a := r.state().Tasks[0].Attempts[0]
		if a.Review != nil || len(a.Rounds) != 1 {
			t.Fatalf("hidden-tag reply not accepted: review=%+v rounds=%+v (logs:\n%s)", a.Review, a.Rounds, r.logs.String())
		}
		rnd := a.Rounds[0]
		if rnd.Verdict != "pass" || rnd.Majors != 0 || rnd.SelfCheckMisses == nil || *rnd.SelfCheckMisses != 0 {
			t.Fatalf("round 1 = %+v, want pass with 0 majors and 0 misses", rnd)
		}
		body, err := os.ReadFile(rnd.ReportPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "deletable: none") || strings.Contains(string(body), "Bash(git") {
			t.Errorf("saved report unexpected:\n%s", string(body))
		}
	})

	t.Run("bare JSON with wrong code nudged once then matching bare JSON accepted", func(t *testing.T) {
		r, revID, c1, prompt := startAgyReviewerRig(t)
		code := r.state().Tasks[0].Attempts[0].Review.Code

		wrongTurn := formatAgyScreen(prompt, strings.Join([]string{
			"self-check misses: 0",
			"class: none",
			"deletable: none",
			"not run: none",
			fmt.Sprintf(`{"code":"wrongcode0","round":1,"head":%q,"verdict":"pass","majors":0}`, c1),
		}, "\n"))
		r.srv.SetScreen(revID, wrongTurn)
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Checked item.go."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass() // N29 sends nudge (never invalid_report)

		if strings.Contains(r.logs.String(), `"invalid_report"`) {
			t.Fatalf("wrong-code bare JSON logged invalid_report instead of N29 nudge:\n%s", r.logs.String())
		}
		sends := sendsTo(t, r.srv.Requests(), revID)
		if len(sends) != 1 || !strings.Contains(sends[0], "You went idle with no report tag") {
			t.Fatalf("sends = %q, want 1 N29 nudge", sends)
		}

		idxBox := strings.Index(wrongTurn, "────────────────────────────────────────────────────────────────────────────────\n> Auto mode:")
		if idxBox < 0 {
			t.Fatalf("missing input box in wrongTurn")
		}
		turn2 := wrongTurn[:idxBox] +
			"────────────────────────────────────────────────────────────\n" +
			"> " + sends[0] + "\n\n" +
			"  self-check misses: 0\n" +
			"  class: none\n" +
			"  deletable: none\n" +
			"  not run: none\n" +
			fmt.Sprintf("  {\"code\":%q,\"round\":1,\"head\":%q,\"verdict\":\"pass\",\"majors\":0}\n\n", code, c1) +
			wrongTurn[idxBox:]

		r.srv.SetScreen(revID, turn2)
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Added report JSON."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		a := r.state().Tasks[0].Attempts[0]
		if a.Review != nil || len(a.Rounds) != 1 || a.Rounds[0].Verdict != "pass" || a.Rounds[0].Majors != 0 {
			t.Fatalf("turn 2 matching bare JSON not accepted: review=%+v rounds=%+v (logs:\n%s)", a.Review, a.Rounds, r.logs.String())
		}
	})

	t.Run("visible report tag in fenced code block accepted", func(t *testing.T) {
		r, revID, c1, prompt := startAgyReviewerRig(t)
		code := r.state().Tasks[0].Attempts[0].Review.Code

		reply := strings.Join([]string{
			"major: item.go:1 lacks a package doc comment.",
			"self-check misses: 1",
			"class: none",
			"deletable: none",
			"not run: none",
			"",
			"```",
			fmt.Sprintf(`<report>{"code":%q,"round":1,"head":%q,"verdict":"changes","majors":1}</report>`, code, c1),
			"```",
		}, "\n")
		r.srv.SetScreen(revID, formatAgyScreen(prompt, reply))
		r.srv.SetStatus(revID, &hc.Status{App: "antigravity", State: "done", Msg: "Found one major issue."})
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		*r.now = r.now.Add(25 * time.Second)
		r.pass()

		a := r.state().Tasks[0].Attempts[0]
		if a.Review != nil || len(a.Rounds) != 1 || a.Rounds[0].Verdict != "changes" || a.Rounds[0].Majors != 1 {
			t.Fatalf("fenced visible <report> tag not accepted: review=%+v rounds=%+v (logs:\n%s)", a.Review, a.Rounds, r.logs.String())
		}
	})
}
