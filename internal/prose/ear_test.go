package prose

import (
	"strings"
	"testing"
)

// baseline101 is the line the shepherd heard on 2026-09-24
// (docs/scorecard-baseline-2026-09-24.md): 101 words, three em dashes,
// two PR numbers.
const baseline101 = "Both landed: the Context tab is merged as daemon #49 — a new /debug tab beside Supervisor showing the brain's exact request and the voice model's context side by side, linked, with a scrubber and diffs against the previous step — and the first app refactor went in as #12, dead code out and duplicated state merged, behaviour unchanged with 245 tests green. It's asking whether to carry on with the next two in parallel — the daemon's dead-code and docs cleanup then removing the legacy tool surface, and the app's testable SessionCore — and I'd say yes."

func words(s string) int { return len(strings.Fields(s)) }

// noDashes checks what every Ear / EarLine result guarantees besides
// the word cap: no dash or semicolon joins clauses, no PR number.
func noDashes(t *testing.T, name, spoken string) {
	t.Helper()
	for _, bad := range []string{"—", " – ", " -- ", " - ", "; ", "#"} {
		if strings.Contains(spoken, bad) {
			t.Errorf("%s: contains %q: %q", name, bad, spoken)
		}
	}
}

func TestEarBaselineLine(t *testing.T) {
	spoken, over := Ear(baseline101)
	if w := words(spoken); w > EarWords || w == 0 {
		t.Errorf("%d words: %q", w, spoken)
	}
	noDashes(t, "baseline", spoken)
	if strings.Contains(spoken, "49") || strings.Contains(spoken, "12") {
		t.Errorf("PR number read aloud: %q", spoken)
	}
	if !strings.HasPrefix(spoken, "Both landed") || !strings.HasSuffix(spoken, ".") {
		t.Errorf("spoken = %q", spoken)
	}
	// Nothing is lost: the overflow carries the rest, scrubbed.
	for _, want := range []string{"scrubber", "SessionCore", "I'd say yes"} {
		if !strings.Contains(over, want) {
			t.Errorf("overflow lost %q: %q", want, over)
		}
	}
	if strings.Contains(over, "#12") {
		t.Errorf("overflow not scrubbed: %q", over)
	}
}

func TestEar(t *testing.T) {
	thirty := strings.TrimSpace(strings.Repeat("word ", 30))
	forty := strings.TrimSpace(strings.Repeat("word ", 40))
	also := strings.TrimSpace(strings.Repeat("also ", 30))
	cases := []struct{ name, in, want, over string }{
		{"short", "Auth finished.", "Auth finished.", ""},
		{"no stop", "auth finished", "auth finished.", ""},
		{"empty", "", "", ""},
		{"two kept", "Auth finished. The tests pass.", "Auth finished. The tests pass.", ""},
		{"third to overflow", "One. Two. Three.", "One. Two.", "Three."},
		{"dash splits", "Auth finished — the tests pass.", "Auth finished. The tests pass.", ""},
		{"dash splits unspaced", "Auth finished—the tests pass.", "Auth finished. The tests pass.", ""},
		{"dash third", "Auth is done — tests pass — and docs is still going.", "Auth is done. Tests pass.", "And docs is still going."},
		{"en dash spaced", "Auth is done – tests pass.", "Auth is done. Tests pass.", ""},
		{"en dash range kept", "It takes 3–5 minutes.", "It takes 3–5 minutes.", ""},
		{"double hyphen", "Auth is done -- tests pass.", "Auth is done. Tests pass.", ""},
		{"hyphen word kept", "The dead-code cleanup ran with --force.", "The dead-code cleanup ran with --force.", ""},
		{"semicolon", "Auth is done; tests pass.", "Auth is done. Tests pass.", ""},
		{"pr numbers", "Auth merged #49, then PR 12 landed.", "Auth merged, then landed.", ""},
		{"hash", "Commit 1398579abc is on main.", "Commit is on main.", ""},
		{"path", "Auth wrote /Users/kc/repo/auth/test.go.", "Auth wrote a file.", ""},
		{"second sentence over cap", "Auth is done. " + forty + ".", "Auth is done.", forty + "."},
		{"one long sentence cut at comma", "The lag fix you asked for is in, and it " + also + " works.", "The lag fix you asked for is in.", "And it " + also + " works."},
		{"one long sentence no comma", forty, thirty + ".", "Word word word word word word word word word word."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, over := Ear(c.in)
			if got != c.want || over != c.over {
				t.Errorf("Ear(%q)\n got  %q / %q\n want %q / %q", c.in, got, over, c.want, c.over)
			}
			if words(got) > EarWords {
				t.Errorf("%d words", words(got))
			}
			noDashes(t, c.name, got)
		})
	}
}

func TestEarLine(t *testing.T) {
	cases := []struct{ name, headline, ask, want, over string }{
		{"no ask", "Auth finished.", "", "Auth finished.", ""},
		{"ask last", "Auth finished and the tests pass.", "Shall I have docs start next?", "Auth finished and the tests pass. Shall I have docs start next?", ""},
		{"headline without stop", "auth finished", "Run the tests?", "auth finished. Run the tests?", ""},
		{"ask made a question", "Auth wants to push to main.", "approve.", "Auth wants to push to main. Approve?", ""},
		{"ask dash becomes comma", "Auth is blocked.", "Push to main — the branch you said not to touch?", "Auth is blocked. Push to main, the branch you said not to touch?", ""},
		{"one question only", "Auth is blocked.", "It wants to push. Approve? Or shall I tell it to wait?", "Auth is blocked. Approve?", "It wants to push. Or shall I tell it to wait?"},
		{"headline question moves", "Auth finished. Want the details?", "Shall I run the tests?", "Auth finished. Shall I run the tests?", "Want the details?"},
		{"old shape keeps its question", "Auth finished. Want the details?", "", "Auth finished. Want the details?", ""},
		{"ask only", "", "Which agent do you mean?", "Which agent do you mean?", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, over := EarLine(c.headline, c.ask)
			if got != c.want || over != c.over {
				t.Errorf("EarLine(%q, %q)\n got  %q / %q\n want %q / %q", c.headline, c.ask, got, over, c.want, c.over)
			}
			noDashes(t, c.name, got)
		})
	}
	// The ask survives the cap: the headline is cut, the question is
	// still heard, last.
	const ask = "Shall I carry on?"
	got, over := EarLine(baseline101, ask)
	t.Logf("heard: %q", got)
	head := strings.TrimSuffix(got, " "+ask)
	if head == got || words(head) > EarWords || words(head) == 0 {
		t.Errorf("cap then ask: %q", got)
	}
	noDashes(t, "baseline+ask", got)
	if !strings.Contains(over, "SessionCore") {
		t.Errorf("overflow = %q", over)
	}
}
