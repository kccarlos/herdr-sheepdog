package docscheck

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The user-facing docs: the README, the community files, everything in
// docs/ except docs/dev, and the iPhone app's README (ios/README.md). Developer docs describe commands that change with
// the code and are not held to this check.
func userDocs(t testing.TB, root string) []string {
	t.Helper()
	files := []string{"README.md", "CONTRIBUTING.md", "SECURITY.md", "ios/README.md"}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, filepath.Join("docs", e.Name()))
		}
	}
	return files
}

var (
	fenceLineRe = regexp.MustCompile("^\\s*(```|~~~)\\s*([A-Za-z]*)")
	codeSpanRe  = regexp.MustCompile("`([^`\n]+)`")
	envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
	wordRe      = regexp.MustCompile(`^[a-z][a-z-]*$`)
	cmdLineRe   = regexp.MustCompile(`^\s*(?:[$>] )?(.*)$`)
)

type docCommand struct {
	file   string
	text   string
	inline bool // from an inline code span, not a fenced block
}

// commandsIn returns every `sheepdog …` command a doc shows: lines of its
// shell code blocks, and inline code spans that start with "sheepdog ".
func commandsIn(file, md string) []docCommand {
	var out []docCommand
	var inline []string
	add := func(line string, isInline bool) {
		// cut a trailing comment and split pipelines and lists
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		for _, seg := range regexp.MustCompile(`\s*(?:\|\||&&|\||;)\s*`).Split(line, -1) {
			f := strings.Fields(seg)
			for len(f) > 0 && envAssignRe.MatchString(f[0]) {
				f = f[1:]
			}
			if len(f) > 0 && f[0] == "sheepdog" {
				out = append(out, docCommand{file, strings.Join(f, " "), isInline})
			}
		}
	}
	var prose strings.Builder
	fence, shell := "", false
	for _, line := range strings.Split(md, "\n") {
		if m := fenceLineRe.FindStringSubmatch(line); m != nil {
			if fence == "" {
				fence = m[1]
				switch m[2] {
				case "sh", "bash", "shell", "zsh", "console", "":
					shell = true
				default:
					shell = false
				}
			} else if m[1] == fence {
				fence = ""
			}
			continue
		}
		switch {
		case fence == "":
			prose.WriteString(line + "\n")
		case shell:
			add(cmdLineRe.FindStringSubmatch(line)[1], false)
		}
	}
	for _, m := range codeSpanRe.FindAllStringSubmatch(prose.String(), -1) {
		if strings.HasPrefix(m[1], "sheepdog ") {
			inline = append(inline, strings.ReplaceAll(m[1], `\|`, "|"))
		}
	}
	for _, l := range inline {
		add(l, true)
	}
	return out
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// sheepdogBinary builds the CLI once per test run.
func sheepdogBinary(t testing.TB, root string) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "docscheck")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "sheepdog")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/sheepdog")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = &buildError{string(out), err}
		}
	})
	if binErr != nil {
		t.Fatalf("building sheepdog: %v", binErr)
	}
	return binPath
}

type buildError struct {
	out string
	err error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// help runs `sheepdog args... --help` in a throwaway HOME and returns its output.
func help(t testing.TB, bin string, args ...string) string {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(bin, append(args, "--help")...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_DATA_HOME=" + home + "/.local/share", "PATH=/usr/bin:/bin"}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	_ = cmd.Run() // some commands exit non-zero on --help; only the text matters
	return buf.String()
}

var topLevelRe = regexp.MustCompile(`(?m)^  ([a-z][a-z-]*) {2,}\S`)

// TestDocCommandsExist checks that every `sheepdog …` command in the user
// docs names a real subcommand and only flags (and action words such as
// `clear` or `install`) that the subcommand's own --help prints.
func TestDocCommandsExist(t *testing.T) {
	root := repoRoot(t)
	bin := sheepdogBinary(t, root)
	top := help(t, bin)
	commands := map[string]bool{"help": true}
	for _, m := range topLevelRe.FindAllStringSubmatch(top, -1) {
		commands[m[1]] = true
	}
	if len(commands) < 15 {
		t.Fatalf("parsed only %d commands from `sheepdog --help`:\n%s", len(commands), top)
	}
	if strings.Contains(top, "sheepdog dev") {
		commands["dev"] = true
	}
	helps := map[string]string{}
	checked := 0
	for _, file := range userDocs(t, root) {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range commandsIn(file, string(raw)) {
			f := strings.Fields(c.text)
			if len(f) < 2 {
				continue
			}
			checked++
			sub := f[1]
			// Prose quotes program output in code spans ("sheepdog is not
			// running"), so a span is judged only when it reads as a command:
			// its second word is a command or a flag. docs/commands.md, the
			// reference, is judged in full, as is every fenced block.
			if c.inline && c.file != "docs/commands.md" && !commands[sub] && !strings.HasPrefix(sub, "-") {
				continue
			}
			text := top
			if !strings.HasPrefix(sub, "-") {
				if !commands[sub] {
					t.Errorf("%s: `%s`: %q is not a sheepdog command (see `sheepdog --help`)", c.file, c.text, sub)
					continue
				}
				if _, ok := helps[sub]; !ok {
					helps[sub] = help(t, bin, sub)
				}
				text = helps[sub] + "\n" + top
			}
			for i, tok := range f[2:] {
				if strings.HasPrefix(tok, "-") {
					flag, _, _ := strings.Cut(tok, "=")
					if flag == "--help" || flag == "-h" || flag == "--" {
						continue
					}
					if !regexp.MustCompile(`(^|[\s\[|(,])` + regexp.QuoteMeta(flag) + `($|[\s\]|),=<])`).MatchString(text) {
						t.Errorf("%s: `%s`: flag %s is not in `sheepdog %s --help`", c.file, c.text, flag, sub)
					}
				} else if i == 0 && wordRe.MatchString(tok) && helps[sub] != "" {
					if !regexp.MustCompile(`\b` + tok + `\b`).MatchString(helps[sub]) {
						t.Errorf("%s: `%s`: %q is not mentioned in `sheepdog %s --help`", c.file, c.text, tok, sub)
					}
				}
			}
		}
	}
	if checked < 20 {
		t.Errorf("only %d sheepdog commands found in the user docs; is the extraction broken?", checked)
	}
	t.Logf("checked %d commands in the user docs against %d subcommands' --help", checked, len(helps))
}

// TestCommandExtraction pins how commands are pulled out of Markdown.
func TestCommandExtraction(t *testing.T) {
	md := "Run `sheepdog status --json` or `sheepdog logs -f` here.\n\n```sh\n$ GEMINI_API_KEY=k sheepdog demo --no-open  # note\nherdr plugin install x | tee y\nsheepdog doctor --report | pbcopy\n```\n\n```toml\nsheepdog nope\n```\n\nOutput: `sheepdog is not running`.\n"
	var got []string
	for _, c := range commandsIn("x.md", md) {
		got = append(got, c.text)
	}
	want := []string{"sheepdog demo --no-open", "sheepdog doctor --report", "sheepdog status --json", "sheepdog logs -f", "sheepdog is not running"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}
