package workerlog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	claudeID = "11111111-2222-4333-8444-555555555555"
	codexID  = "01a0c000-0000-7000-8000-000000000001"
)

func fixtureReader() *Reader {
	return New(Options{ClaudeProjects: filepath.Join("testdata", "claude", "projects"), CodexSessions: filepath.Join("testdata", "codex", "sessions")})
}

func TestLocateClaudeBySlugAndByGlob(t *testing.T) {
	r := fixtureReader()
	want := filepath.Join("testdata", "claude", "projects", "-tmp-work-calc", claudeID+".jsonl")
	for _, cwd := range []string{"/tmp/work.calc", "", "/somewhere/else"} {
		got, err := r.Locate(Session{Agent: "claude", Source: "herdr:claude", Kind: "id", Value: claudeID, Cwd: cwd})
		if err != nil || got != want {
			t.Errorf("cwd %q: Locate = %q, %v; want %q", cwd, got, err, want)
		}
	}
	if got := ClaudeSlug("/Users/kc/Documents/herdr-sheepdog/.claude/worktrees/x"); got != "-Users-kc-Documents-herdr-sheepdog--claude-worktrees-x" {
		t.Errorf("ClaudeSlug = %q", got)
	}
}

func TestLocateCodexIDIsTheSuffix(t *testing.T) {
	r := fixtureReader()
	got, err := r.Locate(Session{Agent: "codex", Source: "herdr:codex", Kind: "id", Value: codexID})
	if err != nil || !strings.HasSuffix(got, "rollout-2026-09-24T10-00-00-"+codexID+".jsonl") {
		t.Fatalf("Locate = %q, %v", got, err)
	}
}

func TestLocateReasons(t *testing.T) {
	r := fixtureReader()
	cases := []struct {
		name string
		sess Session
		want string
	}{
		{"none reported", Session{Agent: "claude"}, ReasonNotReported},
		{"agy", Session{Agent: "agy", Source: "herdr:antigravity_cli", Kind: "id", Value: "70f9969a"}, ReasonUnsupported},
		{"no such claude session", Session{Agent: "claude", Kind: "id", Value: "00000000-0000-0000-0000-000000000000"}, ReasonNotFound},
		{"no such codex session", Session{Agent: "codex", Kind: "id", Value: "01a0ffff"}, ReasonNotFound},
		{"missing path", Session{Agent: "claude", Kind: "path", Value: filepath.Join(t.TempDir(), "gone.jsonl")}, ReasonNotFound},
		{"not a plain id", Session{Agent: "claude", Kind: "id", Value: "*"}, ReasonNotFound},
	}
	for _, c := range cases {
		_, err := r.Read("w1:p1", c.sess)
		if got := Reason(err); got != c.want {
			t.Errorf("%s: reason %q (%v), want %q", c.name, got, err, c.want)
		}
	}
}

func TestReadClaude(t *testing.T) {
	d, err := fixtureReader().Read("w1:p1", Session{Agent: "claude", Kind: "id", Value: claudeID, Cwd: "/tmp/work.calc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.LastAssistant, "The total now rounds half up") || !strings.HasSuffix(d.LastAssistant, "What is next: nothing.") {
		t.Errorf("LastAssistant = %q", d.LastAssistant)
	}
	if got := d.Instruction(); got != "Fix the rounding bug in the total and run the tests." {
		t.Errorf("Instruction = %q (meta and tool results are not instructions)", got)
	}
	if want := map[string]int{"Read": 1, "Edit": 1, "Write": 1, "Bash": 1}; !reflect.DeepEqual(d.Tools, want) {
		t.Errorf("Tools = %v, want %v", d.Tools, want)
	}
	if want := []string{"total.go", "total_test.go"}; !reflect.DeepEqual(d.Files, want) {
		t.Errorf("Files = %v, want %v (edited files, base names only)", d.Files, want)
	}
	if d.Format != FormatClaude || d.Bytes == 0 {
		t.Errorf("Format %q, Bytes %d", d.Format, d.Bytes)
	}
}

func TestReadCodex(t *testing.T) {
	d, err := fixtureReader().Read("w1:p2", Session{Agent: "codex", Source: "herdr:codex", Kind: "id", Value: codexID})
	if err != nil {
		t.Fatal(err)
	}
	if d.LastAssistant != "Added the changelog entry for the rounding fix." {
		t.Errorf("LastAssistant = %q (the final answer, not later commentary)", d.LastAssistant)
	}
	if got := d.Instruction(); got != "Add a CHANGELOG entry for the rounding fix." {
		t.Errorf("Instruction = %q (not the environment context)", got)
	}
	if want := map[string]int{"command": 1, "file_change": 1}; !reflect.DeepEqual(d.Tools, want) {
		t.Errorf("Tools = %v, want %v", d.Tools, want)
	}
	if want := []string{"CHANGELOG.md", "notes.md"}; !reflect.DeepEqual(d.Files, want) {
		t.Errorf("Files = %v, want %v", d.Files, want)
	}
}

// copyFixture puts the Claude fixture in a temp dir so a test can append.
func copyFixture(t *testing.T) (string, Session) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "claude", "projects", "-tmp-work-calc", claudeID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, Session{Agent: "claude", Kind: "path", Value: path}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
}

func TestReadIsIncremental(t *testing.T) {
	path, sess := copyFixture(t)
	r := New(Options{})
	first, err := r.Read("w1:p1", sess)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing new: no instruction, no tools, but the last message stays.
	again, err := r.Read("w1:p1", sess)
	if err != nil {
		t.Fatal(err)
	}
	if again.Bytes != 0 || again.Instruction() != "" || again.Tools != nil || again.LastAssistant != first.LastAssistant {
		t.Errorf("second read = %+v; want nothing new but the last message", again)
	}
	// A new turn, its last line not finished yet.
	appendLines(t, path,
		`{"type":"user","message":{"role":"user","content":"Now add a test for negative totals."}}`+"\n",
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/tmp/work.calc/calc/neg_test.go"}}]}}`+"\n",
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Added the negative`)
	third, err := r.Read("w1:p1", sess)
	if err != nil {
		t.Fatal(err)
	}
	if third.Instruction() != "Now add a test for negative totals." || !reflect.DeepEqual(third.Files, []string{"neg_test.go"}) || third.Tools["Edit"] != 1 || len(third.Tools) != 1 {
		t.Errorf("third read = %+v", third)
	}
	if third.LastAssistant != first.LastAssistant {
		t.Errorf("a partial line was parsed: %q", third.LastAssistant)
	}
	appendLines(t, path, ` total test."}]}}`+"\n")
	fourth, err := r.Read("w1:p1", sess)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.LastAssistant != "Added the negative total test." || fourth.Instruction() != "" {
		t.Errorf("fourth read = %+v", fourth)
	}
	// Another pane's cursor is its own.
	other, err := r.Read("w1:p9", sess)
	if err != nil || other.Instruction() != "Now add a test for negative totals." {
		t.Errorf("other pane = %+v, %v", other, err)
	}
	// A new session on the same pane starts over.
	r.Forget("w1:p1")
	fresh, err := r.Read("w1:p1", sess)
	if err != nil || fresh.Bytes == 0 {
		t.Errorf("after Forget = %+v, %v", fresh, err)
	}
}

func TestReadStartsAtTheTailWindowOfABigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"role":"user","content":"An old instruction at the top."}}` + "\n")
	filler := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("x", 4000) + `"}]}}` + "\n"
	for b.Len() < TailWindow+(1<<20) {
		b.WriteString(filler)
	}
	b.WriteString(`{"type":"user","message":{"role":"user","content":"The recent instruction."}}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done with the recent one."}]}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{}).Read("w1:p1", Session{Agent: "claude", Kind: "path", Value: path})
	if err != nil {
		t.Fatal(err)
	}
	if d.Skipped == 0 || d.Bytes > TailWindow {
		t.Errorf("Skipped %d, Bytes %d: a first read scans only the tail window", d.Skipped, d.Bytes)
	}
	if !reflect.DeepEqual(d.Instructions, []string{"The recent instruction."}) || d.LastAssistant != "Done with the recent one." {
		t.Errorf("digest = %q / %q", d.Instructions, d.LastAssistant)
	}
}

func TestReadSkipsOverlongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	long := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("y", MaxLine+10) + `"}]}}` + "\n"
	tail := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"After the long line."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(long+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Options{})
	sess := Session{Agent: "claude", Kind: "path", Value: path}
	d, err := r.Read("p", sess)
	if err != nil {
		t.Fatal(err)
	}
	if d.LastAssistant != "After the long line." {
		t.Errorf("LastAssistant = %.40q", d.LastAssistant)
	}
	if again, _ := r.Read("p", sess); again.Bytes != 0 {
		t.Errorf("the long line was not consumed: %d bytes re-read", again.Bytes)
	}
}

func TestCaps(t *testing.T) {
	a := &accumulator{format: FormatClaude}
	for i := 0; i < 15; i++ {
		a.file("/x/file" + strings.Repeat("a", i) + ".go")
	}
	for i := 0; i < 8; i++ {
		a.instruction("instruction " + strings.Repeat("b", i))
	}
	if len(a.files) != 0 {
		t.Errorf("an instruction resets the files of the turn before: %v", a.files)
	}
	if len(a.instructions) != MaxInstructions || a.instructions[MaxInstructions-1] != "instruction bbbbbbb" {
		t.Errorf("instructions = %v", a.instructions)
	}
	for i := 0; i < 15; i++ {
		a.file("/x/file" + strings.Repeat("a", i) + ".go")
	}
	if len(a.files) != MaxFiles {
		t.Errorf("files = %d, want %d", len(a.files), MaxFiles)
	}
	a.assistant(strings.Repeat("z", maxText+100), false)
	if n := len([]rune(a.lastAssistant)); n != maxText {
		t.Errorf("assistant kept %d runes, want %d", n, maxText)
	}

	msg := "Outcome first. " + strings.Repeat("middle words ", 100) + "What is next: ship it."
	clipped := Clip(msg, 600)
	if n := len([]rune(clipped)); n > 600 {
		t.Errorf("Clip kept %d runes", n)
	}
	if !strings.HasPrefix(clipped, "Outcome first.") || !strings.HasSuffix(clipped, "What is next: ship it.") || !strings.Contains(clipped, " … ") {
		t.Errorf("Clip = %q: keeps both ends", clipped)
	}
	if Clip("short", 600) != "short" {
		t.Error("Clip changed a short text")
	}
}

// Tail reads the end of the transcript for the brain (read_screen
// source "session", docs/dive-deep.md WP8a) and leaves every cursor
// alone: the journal's next Read still sees everything as new.
func TestTailDoesNotMoveTheJournalsCursor(t *testing.T) {
	path, sess := copyFixture(t)
	r := New(Options{})
	d, err := r.Tail(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Fresh || !strings.HasPrefix(d.LastAssistant, "The total now rounds half up") || d.Instruction() != "Fix the rounding bug in the total and run the tests." {
		t.Errorf("Tail = %+v", d)
	}
	if want := []string{"total.go", "total_test.go"}; !reflect.DeepEqual(d.Files, want) {
		t.Errorf("Files = %v, want %v", d.Files, want)
	}
	// Twice: the same answer, nothing consumed.
	if again, err := r.Tail(sess); err != nil || again.LastAssistant != d.LastAssistant || again.Bytes != d.Bytes {
		t.Errorf("second Tail = %+v, %v", again, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Read("w1:p1", sess)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Fresh || first.Bytes != st.Size() || first.Instruction() == "" {
		t.Errorf("Read after Tail = %+v (size %d): the tail read moved the cursor", first, st.Size())
	}
	// After the journal read, Tail still reports the last message.
	if after, err := r.Tail(sess); err != nil || after.LastAssistant != first.LastAssistant {
		t.Errorf("Tail after Read = %+v, %v", after, err)
	}
}

func TestTailOfABigFileAndMissingSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"role":"user","content":"An old instruction at the top."}}` + "\n")
	filler := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + strings.Repeat("x", 4000) + `"}]}}` + "\n"
	for b.Len() < TailWindow+(1<<20) {
		b.WriteString(filler)
	}
	b.WriteString(`{"type":"user","message":{"role":"user","content":"The recent instruction."}}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done with the recent one."}]}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{}).Tail(Session{Agent: "claude", Kind: "path", Value: path})
	if err != nil {
		t.Fatal(err)
	}
	if d.Skipped == 0 || d.Bytes > TailWindow || d.Instruction() != "The recent instruction." || d.LastAssistant != "Done with the recent one." {
		t.Errorf("Tail = skipped %d, bytes %d, %q / %q", d.Skipped, d.Bytes, d.Instructions, d.LastAssistant)
	}
	r := fixtureReader()
	for sess, want := range map[Session]string{
		{Agent: "claude"}: ReasonNotReported,
		{Agent: "agy", Source: "herdr:antigravity_cli", Kind: "id", Value: "70f9969a"}:   ReasonUnsupported,
		{Agent: "claude", Kind: "path", Value: filepath.Join(t.TempDir(), "gone.jsonl")}: ReasonNotFound,
	} {
		if _, err := r.Tail(sess); Reason(err) != want {
			t.Errorf("Tail(%+v): %v, want %s", sess, err, want)
		}
	}
}

func TestTitleFromSessionFiles(t *testing.T) {
	r := New(Options{ClaudeProjects: filepath.Join("testdata", "claude", "projects"), CodexSessions: filepath.Join("testdata", "codex", "sessions")})
	cases := []struct {
		name string
		sess Session
		want string
	}{
		{"claude: the last ai-title", Session{Agent: "claude", Kind: "id", Value: "22222222-3333-4444-8555-666666666666", Cwd: "/tmp/work/titles"}, "README"},
		{"claude: a custom title wins", Session{Agent: "claude", Kind: "id", Value: "33333333-4444-4555-8666-777777777777"}, "docs pass"},
		{"claude: no title yet", Session{Agent: "claude", Kind: "id", Value: claudeID}, ""},
		{"codex: the last thread name by id", Session{Agent: "codex", Source: "herdr:codex", Kind: "id", Value: codexID}, "Run suite and report failure"},
		{"codex: by rollout path", Session{Agent: "codex", Kind: "path", Value: "/x/rollout-2026-09-24T10-00-00-" + codexID + ".jsonl"}, "Run suite and report failure"},
		{"codex: unnamed thread", Session{Agent: "codex", Kind: "id", Value: "01a0c000-0000-7000-8000-000000000042"}, ""},
	}
	for _, c := range cases {
		got, err := r.Title(c.sess)
		if err != nil || got != c.want {
			t.Errorf("%s: Title = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := r.Title(Session{Agent: "agy", Kind: "id", Value: "x"}); Reason(err) != ReasonUnsupported {
		t.Errorf("agy: %v", err)
	}
	if _, err := r.Title(Session{Agent: "claude"}); Reason(err) != ReasonNotReported {
		t.Errorf("none reported: %v", err)
	}
	none := New(Options{ClaudeProjects: t.TempDir(), CodexSessions: filepath.Join(t.TempDir(), "sessions")})
	if _, err := none.Title(Session{Agent: "codex", Kind: "id", Value: codexID}); Reason(err) != ReasonNotFound {
		t.Errorf("no index: %v", err)
	}
}

// A title line cut by the tail window is skipped, and a later one is
// still found.
func TestTitleTailWindow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	body := `{"type":"ai-title","aiTitle":"Old"}` + "\n" + strings.Repeat(`{"type":"user","message":{"role":"user","content":"x"}}`+"\n", 10) + `{"type":"ai-title","aiTitle":"New"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := scanTail(p, int64(len(body)-5), func(l []byte) {
		if strings.Contains(string(l), "aiTitle") {
			got = append(got, string(l))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{`{"type":"ai-title","aiTitle":"New"}`}) {
		t.Errorf("got %v", got)
	}
}
