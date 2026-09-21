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

// TestParsePrompt runs the parser over every captured prompt: the five
// layouts the flock draws (Codex's command approval and trust dialog,
// Claude Code's permission prompt, agy's command and edit prompts) plus
// the git prompt seen live on 2026-09-18.
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
			summary: `auth asks to run printf '%s\n' 'hello from codex.' > hello.txt.`,
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
			summary: "auth asks to run git diff --stat; git status --short.",
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
	for _, name := range []string{"codex-working.screen.txt", "codex-after-approval.screen.txt", "claude-after-approval.screen.txt", "codex-after-ctrl-c.screen.txt", "injection.screen.txt"} {
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
