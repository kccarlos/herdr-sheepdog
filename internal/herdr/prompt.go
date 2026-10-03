package herdr

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Approval prompts are parsed here, in code (docs/roles.md "Hot paths
// that stay code"): no model sees a screen to find out what a worker is
// asking. Codex, Claude Code and agy all draw the same shape — a
// question, sometimes the command it is about, then two or more numbered
// options, one of them carrying the cursor — so one parser covers the
// flock. Everything on the screen is data: an option's text becomes a
// label and a keystroke, never anything else (the provenance rule).

// promptTailLines is how much of the screen's end a prompt may occupy;
// promptQuestionLines how far above the options its question may stand.
const (
	promptTailLines      = 40
	promptQuestionLines  = 12
	promptCommandWords   = 12
	promptMaxOptionLabel = 200
)

var (
	// reOption is one numbered option: an optional cursor marker, the
	// number, the label, and the key hint the label may end with.
	reOption = regexp.MustCompile(`^\s*([›❯>])?\s*(\d+)\.\s+(.+?)\s*$`)
	// reHint is the parenthesised key hint at the end of a label
	// ("Yes, proceed (y)", "No, … (esc)").
	reHint = regexp.MustCompile(`^(.*?)\s+\(([^()]+)\)$`)
	// reKeyHint is what counts as a key: one plain key name. A chord
	// ("shift+tab") or prose ("Persist to settings.json") is left in
	// the label and the option is selected by its number instead.
	reKeyHint = regexp.MustCompile(`^[a-z0-9]{1,6}$`)
	// reCommand is Codex's command line, reReason its reason line;
	// neither is the prompt's question.
	reCommand = regexp.MustCompile(`^\s*\$\s+(.+?)\s*$`)
	reReason  = regexp.MustCompile(`^\s*Reason:\s*(.+?)\s*$`)
	reMeta    = regexp.MustCompile(`^\s*(Environment|Model|Sandbox|Approval):`)
	// reRule matches the separator rules the TUIs draw between sections.
	reRule = regexp.MustCompile(`^[\s─━═╌╍┄┅–—_\-·•\*]+$`)
)

// Option is one selectable choice of an approval prompt.
type Option struct {
	// Number is the digit it is listed under on screen (1-based).
	Number int
	// Label is its text, without the number and without a key hint that
	// became Keys.
	Label string
	// Keys is the keystroke sequence that selects it: the parenthesised
	// hint when the screen names a plain key ("y", "p", "esc"), else the
	// option's number.
	Keys []string
	// Meta marks an option that is not an answer to the question: Claude
	// Code's AskUserQuestion draws "Type something." (free text) next to
	// the real choices. It stays in Prompt.Options, so its keys remain
	// allowed for the brain's send_keys (it can give a free-text answer
	// the CEO dictated), but it is never shown on the phone card, read as
	// a spoken choice or mapped by the fast yes/no path: use
	// Prompt.Answers for those. "Chat about this" is not an option at all
	// (see isChatOption): it is dropped.
	Meta bool
}

// Prompt is a worker's approval prompt, as parsed from its screen.
type Prompt struct {
	// Question is the line the prompt asks, as it stands on the screen
	// ("Do you want to create claude.txt?").
	Question string
	// Command is the command the prompt is about when it shows one
	// (Codex's "$ git diff --stat" line).
	Command string
	// Reason is the prompt's own reason line, when it shows one (Codex).
	Reason string
	// Options are the numbered choices in screen order, meta options
	// ("Type something.") included, "Chat about this" excluded. These are
	// the keys the one rail accepts; to show or speak the choices use
	// Answers.
	Options []Option
	// Default is the index in Options of the option carrying the cursor
	// marker — the one "enter" also selects.
	Default int
	// YesKeys select the first option whose label starts with "Yes"
	// (else the default option: what enter would do); NoKeys the first
	// whose label starts with "No".
	YesKeys []string
	NoKeys  []string
}

// Answers are the options that answer the question: Options without the
// meta ones. The phone card, the spoken choices and the fast yes/no
// mapping use this list; Options is for the rail.
func (p Prompt) Answers() []Option {
	var out []Option
	for _, o := range p.Options {
		if !o.Meta {
			out = append(out, o)
		}
	}
	return out
}

// metaLabel and chatLabel are the AskUserQuestion widget's own rows, as
// lower-cased labels without their trailing full stop.
func labelKey(label string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(label), ". "))
}

func isMetaOption(label string) bool { return labelKey(label) == "type something" }

func isChatOption(label string) bool { return labelKey(label) == "chat about this" }

// ParsePrompt reads the approval prompt at the end of a screen: the
// numbered options, the question above them and, for Codex, the command
// and reason it is about. ok is false when the screen's tail is not a
// prompt — no block of two or more options numbered from one (each may
// carry indented description lines) with the cursor on exactly one of
// them.
func ParsePrompt(screen string) (Prompt, bool) {
	lines := tailLines(screen, promptTailLines)
	opts, first, cursor, ok := findOptions(lines)
	if !ok {
		return Prompt{}, false
	}
	// "Chat about this" is dropped; "Type something." is kept but marked.
	kept, def := make([]Option, 0, len(opts)), -1
	for i, o := range opts {
		if isChatOption(o.Label) {
			continue
		}
		o.Meta = isMetaOption(o.Label)
		if i == cursor {
			def = len(kept)
		}
		kept = append(kept, o)
	}
	opts = kept
	p := Prompt{Options: opts, Default: def}
	p.Question, p.Command, p.Reason = above(lines[:first])
	for _, o := range opts {
		if o.Meta {
			continue
		}
		label := strings.ToLower(o.Label)
		if len(p.YesKeys) == 0 && strings.HasPrefix(label, "yes") {
			p.YesKeys = o.Keys
		}
		if len(p.NoKeys) == 0 && strings.HasPrefix(label, "no") {
			p.NoKeys = o.Keys
		}
	}
	if len(p.YesKeys) == 0 && p.Default >= 0 && !opts[p.Default].Meta {
		p.YesKeys = opts[p.Default].Keys
	}
	return p, true
}

// EndsInQuestion reports whether the screen's tail is waiting for the
// operator: an approval prompt, or a last line that asks something. It is
// what makes a finish whose screen ends in a question a block instead
// (agy reports done around its prompts).
func EndsInQuestion(screen string) bool {
	if _, ok := ParsePrompt(screen); ok {
		return true
	}
	lines := tailLines(screen, promptTailLines)
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return strings.HasSuffix(line, "?")
		}
	}
	return false
}

// Phrase is Question as it fits after "<alias> is asking …": no trailing
// question mark, lower-cased first letter.
func (p Prompt) Phrase() string {
	q := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(p.Question), "?"))
	if q == "" {
		return ""
	}
	return strings.ToLower(q[:1]) + q[1:]
}

// Summary is the sentence the daemon speaks about the prompt when the
// brain does not phrase one (docs/roles.md "Pushes").
func (p Prompt) Summary(alias string) string {
	switch {
	case p.Command != "":
		return alias + " asks to run " + shortCommand(p.Command) + "."
	case p.Phrase() != "":
		return alias + " is asking " + p.Phrase() + "."
	default:
		return alias + " is asking for a decision."
	}
}

// findOptions returns the last block of options at the end of lines: two
// or more numbered lines (parseOptions), numbered from one, with the cursor
// marker on exactly one of them (so a numbered list in an agent's own
// message is not mistaken for a prompt). first is the block's index in
// lines, cursor the index in opts of the marked option.
func findOptions(lines []string) (opts []Option, first, cursor int, ok bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		m := reOption.FindStringSubmatch(lines[i])
		if m == nil || m[2] != "1" {
			continue
		}
		block, at, cursors := parseOptions(lines[i:])
		if len(block) >= 2 && cursors == 1 {
			return block, i, at, true
		}
	}
	return nil, 0, 0, false
}

// parseOptions reads the numbered options starting at lines[0]
// (numbered 1, 2, 3, …); cursor is the index of the marked option and
// cursors how many carry a marker. A non-numbered line indented at least
// as deep as the current option's label is that option's continuation
// (Claude Code's AskUserQuestion draws a description under each label)
// and is skipped; any other line — a blank, a rule (AskUserQuestion's
// "Chat about this" sits below one), prose — ends the block.
func parseOptions(lines []string) (opts []Option, cursor, cursors int) {
	cursor = -1
	labelCol := 0
	for _, line := range lines {
		m := reOption.FindStringSubmatchIndex(line)
		if m != nil && line[m[4]:m[5]] == strconv.Itoa(len(opts)+1) {
			if m[2] >= 0 {
				cursor, cursors = len(opts), cursors+1
			}
			labelCol = utf8.RuneCountInString(line[:m[6]])
			opts = append(opts, option(len(opts)+1, line[m[6]:m[7]]))
			continue
		}
		if len(opts) == 0 || strings.TrimSpace(line) == "" || reRule.MatchString(line) || reOption.MatchString(line) {
			break
		}
		if indent := utf8.RuneCountInString(line) - utf8.RuneCountInString(strings.TrimLeft(line, " \t")); indent < labelCol {
			break
		}
	}
	return opts, cursor, cursors
}

// option builds one option from its number and its raw label: a plain
// key name in trailing parentheses is the keystroke, anything else stays
// part of the label and the number selects it.
func option(number int, label string) Option {
	label = truncate(strings.TrimSpace(label), promptMaxOptionLabel)
	keys := []string{strconv.Itoa(number)}
	if m := reHint.FindStringSubmatch(label); m != nil && reKeyHint.MatchString(strings.ToLower(m[2])) {
		label, keys = strings.TrimSpace(m[1]), []string{strings.ToLower(m[2])}
	}
	return Option{Number: number, Label: label, Keys: keys}
}

// above reads what stands over the options: the question (the nearest
// line that asks one, cut after its question mark, since the TUIs wrap
// the paragraph it starts), and Codex's command and reason lines. When
// no line asks anything, the nearest line of prose is the question.
func above(lines []string) (question, command, reason string) {
	fallback := ""
	from := max(len(lines)-promptQuestionLines, 0)
	for i := len(lines) - 1; i >= from; i-- {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "" || reRule.MatchString(line) || reMeta.MatchString(line):
			continue
		case reCommand.MatchString(line):
			if command == "" {
				command = reCommand.FindStringSubmatch(line)[1]
			}
			continue
		case reReason.MatchString(line):
			if reason == "" {
				reason = reReason.FindStringSubmatch(line)[1]
			}
			continue
		}
		if q := strings.Index(line, "?"); q >= 0 && question == "" {
			question = strings.TrimSpace(line[:q+1])
		}
		if fallback == "" {
			fallback = line
		}
	}
	if question == "" {
		question = fallback
	}
	return question, command, reason
}

// tailLines returns the last n lines of text.
func tailLines(text string, n int) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// shortCommand trims a command to something sayable inside a sentence.
func shortCommand(cmd string) string {
	words := strings.Fields(cmd)
	if len(words) > promptCommandWords {
		return strings.Join(words[:promptCommandWords], " ") + "…"
	}
	return strings.Join(words, " ")
}

// truncate cuts s to n runes (an option's label is screen text: it is
// never trusted to be short).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
