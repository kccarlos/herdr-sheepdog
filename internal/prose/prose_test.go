package prose

import (
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	want := `{"spoken":"Auth is asking.","options":[{"label":"Yes {ok}","key":"y"}]}`
	cases := map[string]string{
		"clean":      want,
		"fenced":     "```json\n" + want + "\n```",
		"bare fence": "```\n" + want + "\n```",
		"prose":      "Sure! Here is the object:\n" + want + "\nLet me know if you need more.",
		"escaped":    `Note: "{" is not it. ` + want,
		"two":        `{"broken": ` + want,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ExtractJSON(in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("got %s", got)
			}
		})
	}
	for name, in := range map[string]string{"empty": "", "prose": "no json here", "unbalanced": `{"a": 1`} {
		if _, err := ExtractJSON(in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestTailBytesKeepsLastLines(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, strings.Repeat("x", 90)+" "+strings.Repeat("0", 5)[:5-len(itoa(i))]+itoa(i))
	}
	text := strings.Join(lines, "\n")
	got := TailBytes(text, 1000)
	if len(got) > 1000 {
		t.Fatalf("len = %d", len(got))
	}
	if !strings.HasSuffix(got, "00099") {
		t.Errorf("last line lost: %q", got[len(got)-20:])
	}
	if strings.HasPrefix(got, " ") || strings.Count(got, "\n") != 9 {
		t.Errorf("should be 10 whole lines, got %d newlines, starts %q", strings.Count(got, "\n"), got[:5])
	}
	if TailBytes("short", 100) != "short" {
		t.Error("short text changed")
	}
	if TailBytes("", 8) != "" {
		t.Error("empty")
	}
	if got := TailBytes(strings.Repeat("y", 50), 10); len(got) != 10 {
		t.Errorf("single long line: %q", got)
	}
	if got := TailBytes("aaaa\nbbbb", 6); got != "bbbb" {
		t.Errorf("partial line must be dropped: %q", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestSpokenEnforcesD9(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Auth wrote /Users/kc/repo/auth/test.go and pushed.", "Auth wrote a file and pushed."},
		{"See https://github.com/openai/codex/releases/latest for notes.", "See a link for notes."},
		{"Commit 1398579abc is on main.", "Commit is on main."},
		{"Pane w5:p1 is idle.", "Pane is idle."},
		{"One. Two. Three. Four.", "One. Two."},
		{"Is it done? Yes it is! And more follows here.", "Is it done? Yes it is!"},
		{"line one\nline two", "line one line two"},
		{"The word defaced is fine and 1234567 too.", "The word defaced is fine and 1234567 too."},
		{"  spaced   out  ", "spaced out"},
		{"Ran C:\\Users\\kc\\thing.exe now.", "Ran a file now."},
		{"TestTotal in internal/x/calc_test.go:12 failed.", "TestTotal in a file:12 failed."},
		{"Total() is wrong per calc_test.go.", "Total() is wrong per calc_test.go."},
	}
	for _, c := range cases {
		if got := Spoken(c.in, 2); got != c.want {
			t.Errorf("Spoken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Spoken("a. b. c.", 0); got != "a. b. c." {
		t.Errorf("no limit: %q", got)
	}
	if EndSentence("Hello") != "Hello." || EndSentence("Hi!") != "Hi!" || EndSentence("") != "" || EndSentence(`say "no"`) != `say "no"` {
		t.Error("EndSentence")
	}
}
