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
	if got := ClaudeSlug("/Users/you/Documents/herdr-sheepdog/.claude/worktrees/x"); got != "-Users-you-Documents-herdr-sheepdog--claude-worktrees-x" {
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

// Tail reads the end of the transcript for the orchestrator (read_screen
// source "session", docs/dev/history/dive-deep.md WP8a) and leaves every cursor
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

func writeSessionFile(t *testing.T, agent string, lines ...string) (string, Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, Session{Agent: agent, Kind: "path", Value: path}
}

func TestClaudeAPIErrorsAndAborts(t *testing.T) {
	t.Run("529 overloaded does not pollute LastAssistant", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Audit the auth handler."}}`,
			`{"type":"system","subtype":"api_error","error":{"status":529,"error":{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}},"retryInMs":1000,"retryAttempt":1,"maxRetries":10}`,
			`{"type":"assistant","isApiErrorMessage":true,"error":"server_error","apiErrorStatus":529,"message":{"role":"assistant","content":[{"type":"text","text":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopOverloaded || d.Error.Status != 529 {
			t.Fatalf("Error = %+v, want StopOverloaded 529", d.Error)
		}
		if d.LastAssistant != "" || d.Fresh {
			t.Errorf("LastAssistant = %q, Fresh = %v; API error text must not look like an assistant answer", d.LastAssistant, d.Fresh)
		}
		if got, want := d.Error.Sentence("auth"), "auth stopped because the model provider returned 529 overloaded."; got != want {
			t.Errorf("Sentence = %q, want %q", got, want)
		}
	})

	t.Run("retried system api_error cleared when assistant answers", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Run tests."}}`,
			`{"type":"system","subtype":"api_error","error":{"status":529,"error":{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}},"retryAttempt":1,"maxRetries":10}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"All 14 tests passed."}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error != nil {
			t.Errorf("Error = %+v, want nil after successful retry", d.Error)
		}
		if d.LastAssistant != "All 14 tests passed." || !d.Fresh {
			t.Errorf("LastAssistant = %q, Fresh = %v", d.LastAssistant, d.Fresh)
		}
	})

	t.Run("quota wall with reset time strips IANA timezone", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Finish refactoring."}}`,
			`{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 3pm (America/Los_Angeles)"}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopQuota {
			t.Fatalf("Error = %+v, want StopQuota", d.Error)
		}
		if d.Error.ResetsAt != "3pm" {
			t.Errorf("ResetsAt = %q, want 3pm", d.Error.ResetsAt)
		}
		if got, want := d.Error.Summary(), "stopped because it hit its usage limit, resets 3pm"; got != want {
			t.Errorf("Summary = %q, want %q", got, want)
		}
		if strings.Contains(d.Error.Summary(), "America") {
			t.Errorf("Summary leaked IANA timezone: %q", d.Error.Summary())
		}
	})

	t.Run("pipe epoch quota, billing, 429 rate limit, auth, context full", func(t *testing.T) {
		cases := []struct {
			name    string
			line    string
			kind    StopKind
			summary string
		}{
			{
				name:    "pipe epoch usage limit",
				line:    `{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"role":"assistant","content":[{"type":"text","text":"Claude AI usage limit reached|1760000000"}]}}`,
				kind:    StopQuota,
				summary: "stopped because it hit its usage limit",
			},
			{
				name:    "billing credit balance",
				line:    `{"type":"assistant","isApiErrorMessage":true,"error":"billing_error","apiErrorStatus":402,"message":{"role":"assistant","content":[{"type":"text","text":"Credit balance is too low"}]}}`,
				kind:    StopQuota,
				summary: "stopped because its credit balance is too low",
			},
			{
				name:    "429 organization rate limit",
				line:    `{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"role":"assistant","content":[{"type":"text","text":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your organization's rate limit.\"}}"}]}}`,
				kind:    StopRateLimit,
				summary: "stopped because its provider returned a 429 rate limit",
			},
			{
				name:    "401 expired oauth",
				line:    `{"type":"assistant","isApiErrorMessage":true,"error":"authentication_failed","apiErrorStatus":401,"message":{"role":"assistant","content":[{"type":"text","text":"OAuth token has expired. Please obtain a new token or run /login."}]}}`,
				kind:    StopAuth,
				summary: "stopped because its provider login or API token failed",
			},
			{
				name:    "context window exceeded",
				line:    `{"type":"assistant","isApiErrorMessage":true,"error":"invalid_request","apiErrorStatus":400,"message":{"role":"assistant","content":[{"type":"text","text":"Prompt is too long"}]}}`,
				kind:    StopContextFull,
				summary: "stopped because its conversation reached the context limit",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, sess := writeSessionFile(t, "claude",
					`{"type":"user","message":{"role":"user","content":"Check build."}}`,
					tc.line,
				)
				d, err := New(Options{}).Tail(sess)
				if err != nil {
					t.Fatal(err)
				}
				if d.Error == nil || d.Error.Kind != tc.kind {
					t.Fatalf("Error = %+v, want %s", d.Error, tc.kind)
				}
				if got := d.Error.Summary(); got != tc.summary {
					t.Errorf("Summary = %q, want %q", got, tc.summary)
				}
				if strings.Contains(d.Error.Message, "|1760000000") || strings.Contains(d.Error.Message, `{"type":`) {
					t.Errorf("Message kept raw envelope: %q", d.Error.Message)
				}
			})
		}
	})

	t.Run("aborted turn and incremental recovery", func(t *testing.T) {
		path, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Search the repo."}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`,
		)
		r := New(Options{})
		d, err := r.Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopAborted {
			t.Fatalf("Error = %+v, want StopAborted", d.Error)
		}
		if d.Instruction() != "Search the repo." {
			t.Errorf("Instruction = %q; interrupt marker must not become an instruction", d.Instruction())
		}
		appendLines(t, path,
			`{"type":"user","message":{"role":"user","content":"Now just check README."}}`+"\n",
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"README looks up to date."}]}}`+"\n",
		)
		recovered, err := r.Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Error != nil {
			t.Errorf("recovered Error = %+v, want nil", recovered.Error)
		}
		if recovered.LastAssistant != "README looks up to date." || !recovered.Fresh {
			t.Errorf("recovered = %+v", recovered)
		}
	})

	t.Run("merge propagates and clears error", func(t *testing.T) {
		var d Digest
		d.Merge(Digest{Instructions: []string{"Build"}, Error: &TurnError{Kind: StopOverloaded, Status: 529}})
		if d.Error == nil || d.Error.Kind != StopOverloaded {
			t.Fatalf("after error merge: %+v", d.Error)
		}
		d.Merge(Digest{LastAssistant: "Built cleanly.", Fresh: true})
		if d.Error != nil {
			t.Errorf("after recovery merge: %+v, want nil", d.Error)
		}
	})
}

func TestClaudeNarrationBlocks(t *testing.T) {
	// "narration" at byte offsets 0, 1 and 2 before base64 encoding.
	sigs := []struct {
		name string
		sig  string
	}{
		{"align0", "EpgBbmFycmF0aW9uLXRleHQ="},
		{"align1", "EAB5hcnJhdGlvbi10ZXh0"},
		{"align2", "EAABuYXJyYXRpb24tdGV4dA=="},
	}
	for _, sc := range sigs {
		t.Run("signature "+sc.name, func(t *testing.T) {
			_, sess := writeSessionFile(t, "claude",
				`{"type":"user","message":{"role":"user","content":"Fix flaky test."}}`,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Fixed the race in pool_test.go and verified 20 runs.","signature":"`+sc.sig+`"}]}}`,
			)
			d, err := New(Options{}).Read("w1:p1", sess)
			if err != nil {
				t.Fatal(err)
			}
			if d.LastAssistant != "Fixed the race in pool_test.go and verified 20 runs." || !d.Fresh {
				t.Errorf("LastAssistant = %q, Fresh = %v", d.LastAssistant, d.Fresh)
			}
		})
	}

	t.Run("thinkingDisplay updates", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Deploy staging."}}`,
			`{"type":"assistant","thinkingDisplay":"updates","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Staging deploy finished cleanly.","signature":"opaque"}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.LastAssistant != "Staging deploy finished cleanly." || !d.Fresh {
			t.Errorf("LastAssistant = %q, Fresh = %v", d.LastAssistant, d.Fresh)
		}
	})

	t.Run("real text block in turn always wins over narration", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Explain the failure."}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The migration skipped step 3."}]}}`,
			`{"type":"assistant","thinkingDisplay":"updates","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Internal progress note.","signature":"EpgBbmFycmF0aW9u"}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.LastAssistant != "The migration skipped step 3." {
			t.Errorf("LastAssistant = %q; narration must not overwrite a real text block in the same turn", d.LastAssistant)
		}
	})

	t.Run("private non-narration thinking is never read", func(t *testing.T) {
		_, sess := writeSessionFile(t, "claude",
			`{"type":"user","message":{"role":"user","content":"Think quietly."}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Private chain of thought that must stay unread.","signature":"ErUBCkYICxgCIkDprivateSignatureWithoutMarker=="}]}}`,
		)
		d, err := New(Options{}).Read("w1:p1", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.LastAssistant != "" || d.Fresh {
			t.Errorf("private thinking leaked: LastAssistant = %q, Fresh = %v", d.LastAssistant, d.Fresh)
		}
	})
}

func TestCodexErrorsAndAborts(t *testing.T) {
	t.Run("ordinary text mentioning 429 is not an error", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Investigate HTTP 429"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","message":"The test covers a 429 response."}}`,
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"The test covers a 429 response."}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error != nil {
			t.Fatalf("ordinary text classified as error: %+v", d.Error)
		}
	})
	t.Run("terminal error survives completion carrying earlier message", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Do the work"}}`,
			`{"type":"event_msg","payload":{"type":"error","message":"Rate limit","codex_error_info":"rate_limit_exceeded"}}`,
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"I checked the files."}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopRateLimit {
			t.Fatalf("Error = %+v, want rate limit", d.Error)
		}
		if d.Fresh || d.LastAssistant != "" {
			t.Errorf("stale completion: %+v", d)
		}
	})
	t.Run("usage limit error is not overwritten by trailing turn_aborted", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"task_started"}}`,
			`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"input_text","text":"Refactor billing."}]}}}`,
			`{"type":"event_msg","payload":{"type":"error","message":"You've hit your usage limit. Try again in 2 hours.","codex_error_info":"usage_limit_exceeded"}}`,
			`{"type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopQuota {
			t.Fatalf("Error = %+v, want StopQuota", d.Error)
		}
		if d.Error.ResetsAt != "in 2 hours" {
			t.Errorf("ResetsAt = %q, want 'in 2 hours'", d.Error.ResetsAt)
		}
		if got, want := d.Error.Summary(), "stopped because it hit its usage limit, resets in 2 hours"; got != want {
			t.Errorf("Summary = %q, want %q", got, want)
		}
	})

	t.Run("map variant codex_error_info with 529 http_status_code", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"input_text","text":"Run linter."}]}}}`,
			`{"type":"event_msg","payload":{"type":"error","message":"Upstream overloaded","codex_error_info":{"http_connection_failed":{"http_status_code":529}}}}`,
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":null}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopOverloaded || d.Error.Status != 529 {
			t.Fatalf("Error = %+v, want StopOverloaded 529", d.Error)
		}
	})

	t.Run("model_cap with reset_after_seconds", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Check types."}}`,
			`{"type":"event_msg","payload":{"type":"error","message":"Model capacity reached","codex_error_info":{"model_cap":{"model":"gpt-5","reset_after_seconds":120}}}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopOverloaded || d.Error.ResetsAt != "in 2 minutes" {
			t.Fatalf("Error = %+v, want StopOverloaded with ResetsAt 'in 2 minutes'", d.Error)
		}
	})

	t.Run("plain turn_aborted", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Long search."}}`,
			`{"type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopAborted {
			t.Fatalf("Error = %+v, want StopAborted", d.Error)
		}
	})

	t.Run("transient stream_error cleared when turn completes", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Update docs."}}`,
			`{"type":"event_msg","payload":{"type":"stream_error","message":"Reconnecting... 1/5","codex_error_info":"response_stream_disconnected"}}`,
			`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","phase":"final_answer","content":[{"type":"output_text","text":"Updated README."}]}}}`,
			`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Updated README."}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error != nil {
			t.Errorf("Error = %+v, want nil after stream reconnect", d.Error)
		}
		if d.LastAssistant != "Updated README." || !d.Fresh {
			t.Errorf("LastAssistant = %q, Fresh = %v", d.LastAssistant, d.Fresh)
		}
	})

	t.Run("unrecovered stream_error reports connection drop", func(t *testing.T) {
		_, sess := writeSessionFile(t, "codex",
			`{"type":"event_msg","payload":{"type":"user_message","message":"Update docs."}}`,
			`{"type":"event_msg","payload":{"type":"stream_error","message":"stream disconnected before completion","codex_error_info":"response_stream_disconnected"}}`,
		)
		d, err := New(Options{}).Read("w1:p2", sess)
		if err != nil {
			t.Fatal(err)
		}
		if d.Error == nil || d.Error.Kind != StopConnection {
			t.Fatalf("Error = %+v, want StopConnection", d.Error)
		}
	})
}

func TestStopErrorRedactionAndClassification(t *testing.T) {
	e := ClassifyError(401, "", "Incorrect API key provided: sk-test-12345678901234567890 Bearer abcdefghijklmnop", 0)
	if strings.Contains(e.Message, "12345678901234567890") || strings.Contains(e.Message, "abcdefghijklmnop") {
		t.Error("credential was not redacted")
	}
	if got := ClassifyError(429, "rate_limit_error", "rate limit; check billing settings", 0).Kind; got != StopRateLimit {
		t.Errorf("kind = %s", got)
	}
	if got := ClassifyError(0, "http_connection_failed", "connection failed to billing service", 0).Kind; got != StopConnection {
		t.Errorf("kind = %s", got)
	}
}

func TestLongDuplicateInstruction(t *testing.T) {
	var a accumulator
	text := strings.Repeat("a", maxText+1)
	a.instruction(text)
	a.instruction(text)
	if len(a.instructions) != 1 {
		t.Errorf("instructions = %d", len(a.instructions))
	}
}
