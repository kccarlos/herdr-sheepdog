package prose

import (
	"strings"
	"testing"
)

// baseline101 is the line the shepherd heard on 2026-09-24
// (docs/scorecard-baseline-2026-09-24.md): 101 words, three em dashes,
// two PR numbers.
const baseline101Once = "Both landed: the Context tab is merged as daemon #49 — a new /debug tab beside Supervisor showing the brain's exact request and the voice model's context side by side, linked, with a scrubber and diffs against the previous step — and the first app refactor went in as #12, dead code out and duplicated state merged, behaviour unchanged with 245 tests green. It's asking whether to carry on with the next two in parallel — the daemon's dead-code and docs cleanup then removing the legacy tool surface, and the app's testable SessionCore — and I'd say yes."

// baseline101 is the 2026-09-24 line twice over, so it is a runaway under
// the single high cap (the line alone, 101 words, is no longer cut).
const baseline101 = baseline101Once + " " + baseline101Once

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
	hundred := strings.TrimSpace(strings.Repeat("word ", 115))
	cases := []struct{ name, in, want, over string }{
		{"short", "Auth finished.", "Auth finished.", ""},
		{"no stop", "auth finished", "auth finished.", ""},
		{"empty", "", "", ""},
		{"two kept", "Auth finished. The tests pass.", "Auth finished. The tests pass.", ""},
		{"three kept", "One. Two. Three.", "One. Two. Three.", ""},
		{"dash splits", "Auth finished — the tests pass.", "Auth finished. The tests pass.", ""},
		{"dash splits unspaced", "Auth finished—the tests pass.", "Auth finished. The tests pass.", ""},
		{"dash third", "Auth is done — tests pass — and docs is still going.", "Auth is done. Tests pass. And docs is still going.", ""},
		{"en dash spaced", "Auth is done – tests pass.", "Auth is done. Tests pass.", ""},
		{"en dash range kept", "It takes 3–5 minutes.", "It takes 3–5 minutes.", ""},
		{"double hyphen", "Auth is done -- tests pass.", "Auth is done. Tests pass.", ""},
		{"hyphen word kept", "The dead-code cleanup ran with --force.", "The dead-code cleanup ran with --force.", ""},
		{"semicolon", "Auth is done; tests pass.", "Auth is done. Tests pass.", ""},
		{"pr numbers", "Auth merged #49, then PR 12 landed.", "Auth merged, then landed.", ""},
		{"hash", "Commit 1398579abc is on main.", "Commit is on main.", ""},
		{"path", "Auth wrote /Users/kc/repo/auth/test.go.", "Auth wrote a file.", ""},
		{"long second sentence kept", "Auth is done. " + forty + ".", "Auth is done. " + forty + ".", ""},
		{"one long sentence kept", "The lag fix you asked for is in, and it " + also + " works.", "The lag fix you asked for is in, and it " + also + " works.", ""},
		{"thirty words kept", thirty, thirty + ".", ""},
		{"second sentence over runaway", "Auth is done. " + hundred + ".", "Auth is done.", hundred + "."},
		{"one runaway sentence cut at comma", "The lag fix you asked for is in, and it " + strings.Repeat("also ", 120) + "works.", "The lag fix you asked for is in.", "And it " + strings.Repeat("also ", 120) + "works."},
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

// One runaway cap for every spoken line (docs/bench-2026-10-03.md #13):
// about 45 s of speech, ~110 English words or ~200 hanzi. The old per-kind
// caps (2 sentences / 30 words, 4 / 60) are gone by decision; the prompt,
// not the code, decides how much to say.
func TestEarRunawayCap(t *testing.T) {
	if EarRunaway.Seconds != 44 || EarWords != 110 {
		t.Fatalf("cap = %+v", EarRunaway)
	}
	detail := "The mic stays red after a call. The listening banner flickers on reconnect. The haptic buzz fires twice. Hold-to-talk drops the first word. Dark mode loses the status colour."
	got, over := EarLine(detail, "Shall it fix them?")
	if want := detail + " Shall it fix them?"; got != want || over != "" {
		t.Errorf("a long-ish line is not cut:\n got  %q / %q", got, over)
	}
	// A runaway is stopped: 120 words in short sentences keep the first
	// 110, whole sentences only.
	runaway := strings.TrimSpace(strings.Repeat(strings.Repeat("word ", 9)+"end. ", 12))
	got, over = EarLine(runaway, "")
	if w := words(got); w > EarWords || w < 90 || over == "" {
		t.Errorf("runaway of %d words: %q / %q", w, got, over)
	}
	if !strings.HasSuffix(got, "end.") {
		t.Errorf("cut mid-sentence: %q", got)
	}
	// One endless sentence is cut at a word, not mid-word.
	got, over = Ear(strings.TrimSpace(strings.Repeat("word ", 150)))
	if words(got) != EarWords || !strings.HasSuffix(got, "word.") || words(over) != 40 {
		t.Errorf("endless sentence: %d words, over %d", words(got), words(over))
	}
}

func TestSpeechSeconds(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"one two three four five", 2},
		{strings.Repeat("字", 9), 2},
		{"好的，已经让 codex 再跑一遍。", 0},
	} {
		got := SpeechSeconds(c.in)
		if c.want != 0 && (got < c.want-0.01 || got > c.want+0.01) {
			t.Errorf("SpeechSeconds(%q) = %.2f, want %.2f", c.in, got, c.want)
		}
	}
	// Mixed text adds both: 3 words + 9 hanzi = 1.2 + 2.
	if got := SpeechSeconds("alpha beta 一二三四五六七八九 gamma"); got < 3.19 || got > 3.21 {
		t.Errorf("mixed = %.2f", got)
	}
	// ~200 hanzi and ~110 words are the same 44 s.
	if got := SpeechSeconds(strings.Repeat("字", 198)); got < 43.9 || got > 44.1 {
		t.Errorf("198 hanzi = %.2f", got)
	}
}

// Real Chinese pushes from the bench logs (2026-10-02 23:51-23:59,
// 2026-10-03 00:46-00:53): the brain's spoken and ask, which the join used
// to turn into "...。. 要不要...？?".
func TestEarChineseBenchPushes(t *testing.T) {
	cases := []struct{ name, spoken, ask, want string }{
		{"00:46:54", "两个都闲着，手头没在跑任何东西。 codex 已经把「同一件商品加两次数量被盖掉」那个老毛病修好、测试全过，claude 的删除功能也早通过了。", "要不要我让 claude 把整套检查再跑一遍，确认这两处合在一起都没问题？",
			"两个都闲着，手头没在跑任何东西。 codex 已经把「同一件商品加两次数量被盖掉」那个老毛病修好、测试全过，claude 的删除功能也早通过了。 要不要我让 claude 把整套检查再跑一遍，确认这两处合在一起都没问题？"},
		{"00:47:51 no ask", "好的，已经让 codex 再跑一遍，这回要它把测试结果原样贴出来。 跑完我就告诉你", "",
			"好的，已经让 codex 再跑一遍，这回要它把测试结果原样贴出来。 跑完我就告诉你。"},
		{"00:48:12 interrupt", "codex 停在一步上：它要你允许它运行测试命令。 就是你刚让它做的那件事，点一下就能开跑。", "允许它运行吗",
			"codex 停在一步上：它要你允许它运行测试命令。 就是你刚让它做的那件事，点一下就能开跑。 允许它运行吗？"},
		{"00:48:32 kept promise", "测试跑完了，结果不好：那个相加数量的问题还是失败的，数量出来是三，应该是五。这跟你之前听到的「全过」不一样。", "要我把这条失败原样念给你听，还是直接让 codex 把它修掉？",
			"测试跑完了，结果不好：那个相加数量的问题还是失败的，数量出来是三，应该是五。 这跟你之前听到的「全过」不一样。 要我把这条失败原样念给你听，还是直接让 codex 把它修掉？"},
		{"mixed ask in English", "README 写好了。", "Ship it?", "README 写好了。 Ship it?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, over := EarLine(c.spoken, c.ask)
			if got != c.want || over != "" {
				t.Errorf("got  %q / %q\nwant %q", got, over, c.want)
			}
			for _, seam := range []string{"。.", "？?", "！!", "，,", "。。", "？？"} {
				if strings.Contains(got, seam) {
					t.Errorf("seam %q in %q", seam, got)
				}
			}
		})
	}
}

func TestEarChineseSentencesAndCap(t *testing.T) {
	// Sentences split at full-width marks, no space needed.
	if got := splitSentences("好的。已经让它跑了！要不要看？"); len(got) != 3 || got[2] != "要不要看？" {
		t.Errorf("split = %q", got)
	}
	if got := Spoken("好的。已经让它跑了！要不要看？", 2); got != "好的。已经让它跑了！" {
		t.Errorf("Spoken = %q", got)
	}
	// An ask ending in ！ becomes a full-width question, not "！?".
	if q, _ := earAsk("跑吧！"); q != "跑吧？" {
		t.Errorf("ask = %q", q)
	}
	// A headline of 300 hanzi in sentences of 20 is stopped at ~200.
	sen := strings.Repeat("字", 19) + "。"
	got, over := Ear(strings.Repeat(sen, 15))
	n := len([]rune(strings.ReplaceAll(got, "。", "")))
	if n > 200 || n < 170 || over == "" || !strings.HasSuffix(got, "。") {
		t.Errorf("kept %d hanzi, over %q", n, over)
	}
	// One endless Chinese sentence with commas is cut at a comma, closed
	// with a full-width stop.
	clause := strings.Repeat("字", 29) + "，"
	got, over = Ear(strings.Repeat(clause, 10))
	if !strings.HasSuffix(got, "字。") || strings.Contains(got, "，。") || over == "" {
		t.Errorf("cut = %q / %q", got, over)
	}
	if n := len([]rune(got)); n > 205 {
		t.Errorf("kept %d runes", n)
	}
	// Short Chinese lines are never cut.
	if got, over := Ear("好。"); got != "好。" || over != "" {
		t.Errorf("got %q / %q", got, over)
	}
}
