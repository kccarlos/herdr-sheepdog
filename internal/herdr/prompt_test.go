package herdr_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// screen reads a captured screen fixture.
func screen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wantOption is one expected parsed option.
type wantOption struct {
	label string
	keys  string // space-separated
}

// TestParsePrompt runs the parser over every captured prompt: the six
// layouts the workers draws (Codex's command approval and trust dialog,
// Claude Code's permission prompt and AskUserQuestion widget, agy's
// command and edit prompts) plus the git prompt seen live on 2026-09-18.
func TestParsePrompt(t *testing.T) {
	cases := []struct {
		fixture  string
		alias    string
		question string
		command  string
		reason   string
		options  []wantOption
		def      int
		yes, no  string
		summary  string
	}{
		{
			fixture:  "codex-approval-prompt.screen.txt",
			alias:    "auth",
			question: "Would you like to run the following command?",
			command:  `printf '%s\n' 'hello from codex.' > hello.txt`,
			reason:   "May I create the requested hello.txt file in the current workspace?",
			options: []wantOption{
				{"Yes, proceed", "y"},
				{"Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt`", "p"},
				{"No, and tell Codex what to do differently", "esc"},
			},
			def: 0, yes: "y", no: "esc",
			summary: "auth asks to run printf.",
		},
		{
			fixture:  "codex-git-prompt.screen.txt",
			alias:    "auth",
			question: "Would you like to run the following command?",
			command:  "git diff --stat; git status --short",
			reason:   "Do you want to allow Git to verify which unrelated changes remain unstaged?",
			options: []wantOption{
				{"Yes, proceed", "y"},
				{"Yes, and don't ask again for commands that start with `git diff --stat`", "p"},
				{"No, and tell Codex what to do differently", "esc"},
			},
			def: 0, yes: "y", no: "esc",
			summary: "auth asks to run git diff.",
		},
		{
			fixture:  "codex-trust-dialog.screen.txt",
			alias:    "auth",
			question: "Do you trust the contents of this directory?",
			options:  []wantOption{{"Yes, continue", "1"}, {"No, quit", "2"}},
			def:      0, yes: "1", no: "2",
			summary: "auth is asking do you trust the contents of this directory.",
		},
		{
			fixture:  "claude-permission-prompt.screen.txt",
			alias:    "docs",
			question: "Do you want to create claude.txt?",
			options: []wantOption{
				{"Yes", "1"},
				{"Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "2"},
				{"No", "3"},
			},
			def: 0, yes: "1", no: "3",
			summary: "docs is asking do you want to create claude.txt.",
		},
		{
			// Claude Code's AskUserQuestion widget, seen live on
			// 2026-10-02: a description line under each option, and a
			// fourth "Chat about this" below a rule that is not an answer.
			fixture:  "claude-askuserquestion.screen.txt",
			alias:    "docs",
			question: "This is a test approval prompt: do you want to proceed?",
			options: []wantOption{
				{"Yes, proceed", "1"},
				{"No, decline", "2"},
				{"Type something.", "3"},
			},
			def: 0, yes: "1", no: "2",
			summary: "docs is asking this is a test approval prompt: do you want to proceed.",
		},
		{
			fixture:  "agy-approval-prompt.screen.txt",
			alias:    "one",
			question: "Run this command?",
			options: []wantOption{
				{"Yes, run command", "1"},
				{"Yes, and always allow in this conversation for commands that start with 'go test ./...'", "2"},
				{"Yes, and always allow for commands that start with 'go test ./...' (Persist to settings.json)", "3"},
				{"No, cancel", "4"},
			},
			def: 0, yes: "1", no: "4",
			summary: "one is asking run this command.",
		},
		{
			fixture:  "agy-edit-prompt.screen.txt",
			alias:    "one",
			question: "Accept this file edit?",
			options:  []wantOption{{"Yes, accept this change", "1"}, {"No, reject this change", "2"}},
			def:      0, yes: "1", no: "2",
			summary: "one is asking accept this file edit.",
		},
		{
			// Claude Code 2.1.288's command approval (tier-3 bench
			// 2026-10-03, finding 3): the command sits between dashed
			// rules under a "Bash command" header, not on a "$" line.
			fixture:  "claude-bash-prompt.screen.txt",
			alias:    "Claude",
			question: "Do you want to proceed?",
			command:  "go test ./shop -v",
			reason:   "Run the shop package tests",
			options:  []wantOption{{"Yes", "1"}, {"Yes, and don’t ask again for: go test *", "2"}, {"No", "3"}},
			def:      0, yes: "1", no: "3",
			summary: "Claude asks to run go test.",
		},
		{
			// Claude Code 2.1.288's folder trust dialog draws no numbers
			// (tier-3 bench 2026-10-03, finding 1): the cursor starts on
			// "No, exit" and "yes" is one row down.
			fixture:  "claude-trust-dialog.screen.txt",
			alias:    "bench",
			question: "Quick safety check: Is this a project you created or one you trust?",
			options:  []wantOption{{"No, exit", "enter"}, {"Yes, I trust this folder", "down enter"}},
			def:      0, yes: "down enter", no: "enter",
			summary: "bench is asking quick safety check: Is this a project you created or one you trust.",
		},
		{
			// The same approval at the phone's width (phone terminal
			// phase 1 check, Claude Code 2.1.304 in a 58-column pane
			// held by a control bridge, 2026-10-20): options wrap onto
			// continuation lines, the parser joins them.
			fixture:  "claude-bash-prompt-58col.screen.txt",
			alias:    "Claude",
			question: "Do you want to proceed?",
			command:  "date > stamp-58.txt",
			reason:   "Write current date to stamp file",
			options: []wantOption{
				{"Yes", "1"},
				{"Yes, and always allow access to /private/tmp/sdp1-proj from this project", "2"},
				{"Yes, and switch to auto mode · auto mode handles these prompts for you", "3"},
				{"No", "4"},
			},
			def: 0, yes: "1", no: "4",
			summary: "Claude asks to run date.",
		},
		{
			// The same approval at the phone's width (phone terminal
			// phase 1 check, Claude Code 2.1.304 in a 44-column pane
			// held by a control bridge, 2026-10-20): options wrap onto
			// continuation lines, the parser joins them.
			fixture:  "claude-bash-prompt-44col.screen.txt",
			alias:    "Claude",
			question: "Do you want to proceed?",
			command:  "date > stamp-44.txt",
			reason:   "Write current date to stamp file",
			options: []wantOption{
				{"Yes", "1"},
				{"Yes, and always allow access to /private/tmp/sdp1-proj from this project", "2"},
				{"Yes, and switch to auto mode · auto mode handles these prompts for you", "3"},
				{"No", "4"},
			},
			def: 0, yes: "1", no: "4",
			summary: "Claude asks to run date.",
		},
		{
			// agy's trust dialog: the same unnumbered shape, ">" marker,
			// "Yes" first.
			fixture:  "agy-trust-dialog.screen.txt",
			alias:    "one",
			question: "Do you trust the contents of this project?",
			options:  []wantOption{{"Yes, I trust this folder", "enter"}, {"No, exit", "down enter"}},
			def:      0, yes: "enter", no: "down enter",
			summary: "one is asking do you trust the contents of this project.",
		},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			p, ok := herdr.ParsePrompt(screen(t, c.fixture))
			if !ok {
				t.Fatal("not parsed as a prompt")
			}
			if p.Question != c.question {
				t.Errorf("question = %q, want %q", p.Question, c.question)
			}
			if p.Command != c.command {
				t.Errorf("command = %q, want %q", p.Command, c.command)
			}
			if p.Reason != c.reason {
				t.Errorf("reason = %q, want %q", p.Reason, c.reason)
			}
			if len(p.Options) != len(c.options) {
				t.Fatalf("options = %d, want %d: %+v", len(p.Options), len(c.options), p.Options)
			}
			for i, w := range c.options {
				got := p.Options[i]
				if got.Number != i+1 || got.Label != w.label || strings.Join(got.Keys, " ") != w.keys {
					t.Errorf("option %d = %+v, want %q keys %q", i+1, got, w.label, w.keys)
				}
			}
			if p.Default != c.def {
				t.Errorf("default = %d, want %d", p.Default, c.def)
			}
			if got := strings.Join(p.YesKeys, " "); got != c.yes {
				t.Errorf("yes keys = %q, want %q", got, c.yes)
			}
			if got := strings.Join(p.NoKeys, " "); got != c.no {
				t.Errorf("no keys = %q, want %q", got, c.no)
			}
			if got := p.Summary(c.alias); got != c.summary {
				t.Errorf("summary = %q, want %q", got, c.summary)
			}
			if !herdr.EndsInQuestion(screen(t, c.fixture)) {
				t.Error("EndsInQuestion = false on a prompt")
			}
		})
	}
}

// TestParsePromptNotAPrompt keeps the parser off screens that are not
// prompts: a working agent, a finished one, and the injection fixture,
// whose numbered-looking text must never become an option (its text is
// data, never an instruction).
func TestParsePromptNotAPrompt(t *testing.T) {
	for _, name := range []string{"codex-working.screen.txt", "codex-after-approval.screen.txt", "claude-after-approval.screen.txt", "codex-after-ctrl-c.screen.txt", "injection.screen.txt", "claude-idle.screen.txt"} {
		t.Run(name, func(t *testing.T) {
			s := screen(t, name)
			if p, ok := herdr.ParsePrompt(s); ok {
				t.Errorf("parsed as a prompt: %+v", p)
			}
			if herdr.EndsInQuestion(s) {
				t.Error("EndsInQuestion = true")
			}
		})
	}
}

// TestEndsInQuestionLastLine covers the agy case the parser cannot see:
// the agent reports done with a plain question on its last line.
func TestEndsInQuestionLastLine(t *testing.T) {
	if !herdr.EndsInQuestion("● Ran the tests\n\nShall I commit this?\n") {
		t.Error("a trailing question is not seen")
	}
	if herdr.EndsInQuestion("● Ran the tests\n\n123 tests, 0 failures.\n") {
		t.Error("a plain finish counts as a question")
	}
}

// TestParsePromptCursorRequired keeps a numbered list in an agent's own
// message from becoming a prompt: without the cursor marker there is
// nothing to answer.
func TestParsePromptCursorRequired(t *testing.T) {
	list := "● Here is what I would do:\n\n  1. Rename the field\n  2. Update the tests\n\n› Ask Codex to do anything\n"
	if _, ok := herdr.ParsePrompt(list); ok {
		t.Error("a numbered list parsed as a prompt")
	}
}

// TestParsePromptDescribedListNotAPrompt: a numbered list whose items
// carry indented descriptions, the AskUserQuestion shape, is still not a
// prompt without the cursor marker.
func TestParsePromptDescribedListNotAPrompt(t *testing.T) {
	list := "● Two ways to do it:\n\n  1. Rename the field\n     Touches every caller.\n  2. Add an alias\n     Keeps the old name working.\n\n› Ask Codex to do anything\n"
	if p, ok := herdr.ParsePrompt(list); ok {
		t.Errorf("a described numbered list parsed as a prompt: %+v", p)
	}
	if herdr.EndsInQuestion(list) {
		t.Error("EndsInQuestion = true")
	}
}

// AskUserQuestion's own rows are not answers (docs/dev/history/bench-2026-10-03.md
// #2): "Type something." is kept for the rail but marked Meta and left out
// of Answers (the phone card, the spoken choices, the fast yes/no);
// "Chat about this" is dropped altogether, with or without the rule above
// it.
func TestParsePromptMetaOptions(t *testing.T) {
	p, ok := herdr.ParsePrompt(screen(t, "claude-askuserquestion.screen.txt"))
	if !ok {
		t.Fatal("not a prompt")
	}
	if len(p.Options) != 3 || !p.Options[2].Meta || p.Options[0].Meta || p.Options[1].Meta {
		t.Fatalf("options = %+v", p.Options)
	}
	if strings.Join(p.Options[2].Keys, " ") != "3" {
		t.Errorf("the rail must still accept Type something: %+v", p.Options[2])
	}
	ans := p.Answers()
	if len(ans) != 2 || ans[0].Label != "Yes, proceed" || ans[1].Label != "No, decline" {
		t.Errorf("answers = %+v", ans)
	}

	// No rule between the options: "Chat about this" is a fourth numbered
	// row. It is dropped; "Type something." is still marked.
	const noRule = "Pick one?\n\n❯ 1. Red\n  2. Blue\n  3. Type something.\n  4. Chat about this\n\nEnter to select"
	p, ok = herdr.ParsePrompt(noRule)
	if !ok || len(p.Options) != 3 || p.Options[2].Label != "Type something." || !p.Options[2].Meta {
		t.Fatalf("no-rule prompt = %+v ok=%v", p, ok)
	}
	for _, o := range p.Options {
		if strings.Contains(strings.ToLower(o.Label), "chat") {
			t.Errorf("Chat about this offered: %+v", o)
		}
	}
	// No yes/no label: the fast path has nothing to map to when the cursor
	// sits on a meta row.
	const cursorOnMeta = "Pick one?\n\n  1. Red\n  2. Blue\n❯ 3. Type something.\n"
	p, ok = herdr.ParsePrompt(cursorOnMeta)
	if !ok || p.Default != 2 || len(p.YesKeys) != 0 || len(p.Answers()) != 2 {
		t.Errorf("cursor on meta: %+v ok=%v", p, ok)
	}
	// A real "Yes" still maps, whatever the meta rows are.
	if p, _ := herdr.ParsePrompt(screen(t, "claude-askuserquestion.screen.txt")); strings.Join(p.YesKeys, " ") != "1" || strings.Join(p.NoKeys, " ") != "2" {
		t.Errorf("yes/no = %v / %v", p.YesKeys, p.NoKeys)
	}
}

// docs/dev/eval/eval-campaign-2026-10-03.md H3: the block template read the raw
// command aloud ("bench-codex asks to run go test -race./...."). Only the
// command's plain leading words are spoken; the full command stays in
// Prompt.Command.
func TestSummarySpeaksOnlyPlainCommandWords(t *testing.T) {
	cases := map[string]string{
		"go test -race ./...":                  "bench-codex asks to run go test.",
		"git push --force origin main":         "bench-codex asks to run git push.",
		"./scripts/release.sh":                 "bench-codex asks to run a command.",
		"npm run build":                        "bench-codex asks to run npm run build.",
		"go run ./cmd/sheepdog eval --set all": "bench-codex asks to run go run.",
	}
	for cmd, want := range cases {
		if got := (herdr.Prompt{Command: cmd}).Summary("bench-codex"); got != want {
			t.Errorf("Summary for %q = %q, want %q", cmd, got, want)
		}
	}
}

// TestParseCursorMenuMovesFromTheCursor: on an unnumbered menu each
// option's keys are the arrow moves from the cursor row, so a cursor on
// the middle row selects the first option with "up".
func TestParseCursorMenuMovesFromTheCursor(t *testing.T) {
	s := "Pick a model\n\n  fast\n❯ balanced\n  careful\n\n↑/↓ to move · enter to select\n"
	p, ok := herdr.ParsePrompt(s)
	if !ok {
		t.Fatal("not parsed")
	}
	want := []string{"up enter", "enter", "down enter"}
	if len(p.Options) != 3 || p.Default != 1 {
		t.Fatalf("options = %+v, default %d", p.Options, p.Default)
	}
	for i, w := range want {
		if got := strings.Join(p.Options[i].Keys, " "); got != w || p.Options[i].Number != i+1 {
			t.Errorf("option %d = %+v, want keys %q", i+1, p.Options[i], w)
		}
	}
}

// TestParseCursorMenuNotAMenu keeps the unnumbered reading off what only
// looks like one: an input line between rules, a shell prompt, a single
// cursor row, misaligned rows, a block with prose below it, and a block
// with two cursors.
func TestParseCursorMenuNotAMenu(t *testing.T) {
	for name, s := range map[string]string{
		"input line":   "──────\n❯ Try \"fix lint\"\n──────\n  ⏸ manual mode on\n",
		"shell prompt": "bench on main\n❯ claude --model haiku\n\n",
		"single row":   "Question?\n\n❯ Only one\n\nEnter to confirm\n",
		"misaligned":   "❯ No, exit\n      Yes, I trust this folder\n",
		"prose below":  "❯ one\n  two\n\na\nb\nc\nd\n",
		"two cursors":  "❯ one\n❯ two\n",
		"wrapped long": "❯ " + strings.Repeat("word ", 20) + "\n  continued here\n",
	} {
		t.Run(name, func(t *testing.T) {
			if p, ok := herdr.ParsePrompt(s); ok {
				t.Errorf("parsed as a prompt: %+v", p)
			}
		})
	}
}

// TestSummaryInChinese: a Chinese approval names the command when there
// is one, quotes a question only when it is Chinese itself, and
// otherwise says that the agent needs a decision rather than quoting a
// foreign-language question (tier-3 bench 2026-10-03, finding 6: every
// Chinese block line was "Claude 在问：Do you want to proceed?").
func TestSummaryInChinese(t *testing.T) {
	for _, c := range []struct{ fixture, want string }{
		{"claude-bash-prompt.screen.txt", "Claude 请求运行 go test。"},
		{"claude-permission-prompt.screen.txt", "Claude 需要你做个决定。"},
		{"claude-trust-dialog.screen.txt", "Claude 需要你做个决定。"},
	} {
		p, ok := herdr.ParsePrompt(screen(t, c.fixture))
		if !ok {
			t.Fatalf("%s: not parsed", c.fixture)
		}
		if got := p.SummaryIn("Claude", true); got != c.want {
			t.Errorf("%s: %q, want %q", c.fixture, got, c.want)
		}
	}
	p := herdr.Prompt{Question: "要创建 claude.txt 吗"}
	if got := p.SummaryIn("Claude", true); got != "Claude 在问：要创建 claude.txt 吗？" {
		t.Errorf("chinese question: %q", got)
	}
}
