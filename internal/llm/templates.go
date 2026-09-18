package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// Template file names under prompts/.
const (
	TemplateSummarizeBlock     = "summarize_block.txt"
	TemplateResolveInstruction = "resolve_instruction.txt"
	TemplateSpokenAnswer       = "spoken_answer.txt"
)

// systemPrompt is shared by all three templates; the instructions live in
// the user message so the templates are self-contained.
const systemPrompt = "You are a precise assistant inside a voice supervisor for coding agents. Reply with a single JSON object only: no prose, no markdown fences."

// Option is one selectable choice on a blocked agent's screen.
type Option struct {
	// Label is the short spoken label ("Yes, proceed").
	Label string `json:"label"`
	// Key is the keystroke(s) selecting it, space-separated when it is a
	// sequence ("enter", "y", "down enter").
	Key string `json:"key"`
}

// Keys returns the herdr key names of the option in order.
func (o Option) Keys() []string { return strings.Fields(strings.ToLower(o.Key)) }

// AnswerKeys maps a plain yes / no onto keystrokes.
type AnswerKeys struct {
	Yes []string `json:"yes"`
	No  []string `json:"no"`
}

// BlockSummary is the summarize_block contract (plan §3.4, extended with
// question, options and answer_keys).
type BlockSummary struct {
	Spoken        string     `json:"spoken"`
	Question      string     `json:"question"`
	IsQuestion    bool       `json:"is_question"`
	NeedsDecision bool       `json:"needs_decision"`
	Options       []Option   `json:"options"`
	AnswerKeys    AnswerKeys `json:"answer_keys"`
}

// AgentSummary is one row of the agent list given to ResolveInstruction.
type AgentSummary struct {
	Alias  string
	PaneID string
	Kind   string
	State  string
	// Task is the current task (empty when unknown).
	Task string
	// Question is what the agent is asking when it is blocked.
	Question string
}

// Resolution is the resolve_instruction contract.
type Resolution struct {
	// PaneID is the chosen pane, empty when ambiguous. The model may
	// answer with an alias instead; ResolveInstruction maps it back.
	PaneID          string `json:"pane_id"`
	InstructionText string `json:"instruction_text"`
	IsAnswer        bool   `json:"is_answer"`
	Answer          string `json:"answer"`
	Echo            string `json:"echo"`
	Ambiguous       bool   `json:"ambiguous"`
	ClarifyQuestion string `json:"clarify_question"`
}

// Answer is the spoken_answer contract.
type Answer struct {
	Spoken    string `json:"spoken"`
	CanAnswer bool   `json:"can_answer"`
}

// Templates is the model-call surface the Supervisor depends on; *Client
// implements it and tests fake it.
type Templates interface {
	SummarizeBlock(ctx context.Context, screenTail, alias string) (BlockSummary, error)
	ResolveInstruction(ctx context.Context, userText string, agents []AgentSummary, hint string) (Resolution, error)
	SpokenAnswer(ctx context.Context, raw, question, alias string) (Answer, error)
}

var _ Templates = (*Client)(nil)

// SummarizeBlock describes what a blocked agent is waiting for. The screen
// tail is truncated to its last DefaultScreenBytes; the reply is validated
// and its spoken text scrubbed per D9. Options with no key are dropped;
// when answer_keys.yes is empty but an "enter"-selected option exists it
// is used, and a missing "no" falls back to esc when an esc option exists.
func (c *Client) SummarizeBlock(ctx context.Context, screenTail, alias string) (BlockSummary, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = "the agent"
	}
	user, err := prompts.Render(TemplateSummarizeBlock, map[string]string{
		"alias":  alias,
		"screen": TailBytes(screenTail, DefaultScreenBytes),
	})
	if err != nil {
		return BlockSummary{}, err
	}
	reply, err := c.Chat(ctx, "summarize_block", systemPrompt, user)
	if err != nil {
		return BlockSummary{}, err
	}
	var out BlockSummary
	if err := decode(reply.Text, &out); err != nil {
		return BlockSummary{}, err
	}
	return normalizeBlock(out, alias)
}

// normalizeBlock validates and cleans a BlockSummary.
func normalizeBlock(out BlockSummary, alias string) (BlockSummary, error) {
	out.Spoken = endSentence(Spoken(out.Spoken, 2))
	if out.Spoken == "" {
		return out, errors.New("llm: summarize_block: empty spoken")
	}
	out.Question = questionPhrase(out.Question, alias)
	kept := make([]Option, 0, len(out.Options))
	for _, o := range out.Options {
		o.Label = strings.TrimSpace(Spoken(o.Label, 1))
		o.Key = strings.Join(o.Keys(), " ")
		if o.Key == "" || o.Label == "" {
			continue
		}
		kept = append(kept, o)
	}
	out.Options = kept
	out.AnswerKeys.Yes = normalizeKeys(out.AnswerKeys.Yes)
	out.AnswerKeys.No = normalizeKeys(out.AnswerKeys.No)
	if !out.IsQuestion {
		out.NeedsDecision = false
		out.AnswerKeys = AnswerKeys{Yes: []string{}, No: []string{}}
		out.Question = ""
		return out, nil
	}
	if len(out.AnswerKeys.Yes) == 0 {
		out.AnswerKeys.Yes = keysForOption(out.Options, "enter", "y", "1")
	}
	if len(out.AnswerKeys.No) == 0 {
		out.AnswerKeys.No = keysForOption(out.Options, "esc", "n")
	}
	if out.Question == "" {
		out.Question = questionFromSpoken(out.Spoken, alias)
	}
	return out, nil
}

// keysForOption returns the keys of the first option whose key equals one
// of the candidates, in candidate order.
func keysForOption(opts []Option, candidates ...string) []string {
	for _, c := range candidates {
		for _, o := range opts {
			if o.Key == c {
				return o.Keys()
			}
		}
	}
	return nil
}

// questionPhrase scrubs the question and strips a leading "<alias> is
// asking" the model sometimes repeats, so it completes "<alias> is asking …".
func questionPhrase(q, alias string) string {
	q = strings.TrimSpace(Spoken(q, 1))
	q = strings.TrimRight(q, ".!?")
	lower := strings.ToLower(q)
	for _, prefix := range []string{alias + " is asking ", "the agent is asking ", alias + " asks ", "asking "} {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			q = strings.TrimSpace(q[len(prefix):])
			break
		}
	}
	return q
}

// normalizeKeys lowercases, trims and drops empty key names; a single
// space-separated entry is split into a sequence.
func normalizeKeys(keys []string) []string {
	out := []string{}
	for _, k := range keys {
		out = append(out, strings.Fields(strings.ToLower(k))...)
	}
	return out
}

// questionFromSpoken derives "whether …" from "alias is asking whether …"
// when the model left question empty.
func questionFromSpoken(spoken, alias string) string {
	s := strings.TrimRight(spoken, ".!?")
	lower := strings.ToLower(s)
	for _, marker := range []string{" is asking ", " asks ", " wants to know ", " is waiting for "} {
		if i := strings.Index(lower, marker); i >= 0 {
			return strings.TrimSpace(s[i+len(marker):])
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(s, alias))
}

// ResolveInstruction maps the user's words onto one agent and the text to
// send (decision D6: ambiguity comes back as a clarify question and
// nothing is sent). hint is the alias the voice model extracted, or "".
// The returned PaneID is always one of the listed panes or "".
func (c *Client) ResolveInstruction(ctx context.Context, userText string, agents []AgentSummary, hint string) (Resolution, error) {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return Resolution{}, errors.New("llm: resolve_instruction: empty instruction")
	}
	hintLine := ""
	if hint = strings.TrimSpace(hint); hint != "" {
		hintLine = fmt.Sprintf("The operator named the agent as %q.\n", hint)
	}
	user, err := prompts.Render(TemplateResolveInstruction, map[string]string{
		"user_text": strings.ReplaceAll(userText, `"`, "'"),
		"agents":    FormatAgents(agents),
		"hint":      hintLine,
	})
	if err != nil {
		return Resolution{}, err
	}
	reply, err := c.Chat(ctx, "resolve_instruction", systemPrompt, user)
	if err != nil {
		return Resolution{}, err
	}
	var out Resolution
	if err := decode(reply.Text, &out); err != nil {
		return Resolution{}, err
	}
	return normalizeResolution(out, agents), nil
}

// normalizeResolution validates the target and scrubs the spoken fields.
func normalizeResolution(out Resolution, agents []AgentSummary) Resolution {
	out.PaneID = strings.TrimSpace(out.PaneID)
	out.InstructionText = strings.TrimSpace(out.InstructionText)
	out.Answer = strings.TrimSpace(out.Answer)
	out.Echo = endSentence(Spoken(out.Echo, 2))
	out.ClarifyQuestion = endSentence(Spoken(out.ClarifyQuestion, 2))
	if out.PaneID != "" {
		out.PaneID = matchAgent(out.PaneID, agents)
	}
	if out.Ambiguous || out.PaneID == "" || out.InstructionText == "" {
		out.Ambiguous = true
		out.PaneID = ""
		if out.ClarifyQuestion == "" {
			out.ClarifyQuestion = defaultClarify(agents)
		}
		return out
	}
	alias := out.PaneID
	for _, a := range agents {
		if a.PaneID == out.PaneID {
			alias = a.Alias
		}
	}
	switch {
	case out.IsAnswer:
		if out.Answer == "" {
			out.Answer = out.InstructionText
		}
		out.Echo = fmt.Sprintf("Telling %s %s.", alias, strings.TrimRight(out.Answer, "."))
	case out.Echo == "":
		out.Echo = fmt.Sprintf("Telling %s to %s.", alias, strings.TrimRight(out.InstructionText, "."))
	}
	return out
}

// matchAgent maps a pane id or alias (case-insensitive) onto a listed pane
// id, or "".
func matchAgent(v string, agents []AgentSummary) string {
	lv := strings.ToLower(v)
	for _, a := range agents {
		if strings.ToLower(a.PaneID) == lv || strings.ToLower(a.Alias) == lv {
			return a.PaneID
		}
	}
	return ""
}

// defaultClarify builds "Do you mean auth or docs?" from the agent list.
func defaultClarify(agents []AgentSummary) string {
	if len(agents) == 0 {
		return "No agents are running, so there is nobody to tell."
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Alias)
	}
	if len(names) == 1 {
		return fmt.Sprintf("Do you mean %s, and what should it do?", names[0])
	}
	return fmt.Sprintf("Do you mean %s or %s?", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// FormatAgents renders the agent list for the resolve_instruction prompt.
func FormatAgents(agents []AgentSummary) string {
	if len(agents) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s (pane %s, %s, %s)", a.Alias, a.PaneID, orUnknown(a.Kind), orUnknown(a.State))
		if a.Question != "" {
			fmt.Fprintf(&b, ": asking %s", a.Question)
		} else if a.Task != "" {
			fmt.Fprintf(&b, ": %s", truncateRunes(a.Task, 120))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// SpokenAnswer turns raw material (a screen tail or a CLI tool's output)
// about the agent called alias into an answer for the ear: at most two
// sentences, no paths, hashes or URLs (D9), enforced in Go after the model.
func (c *Client) SpokenAnswer(ctx context.Context, raw, question, alias string) (Answer, error) {
	if alias = strings.TrimSpace(alias); alias == "" {
		alias = "the agent"
	}
	user, err := prompts.Render(TemplateSpokenAnswer, map[string]string{
		"question": strings.ReplaceAll(strings.TrimSpace(question), `"`, "'"),
		"raw":      TailBytes(raw, DefaultScreenBytes),
		"alias":    alias,
	})
	if err != nil {
		return Answer{}, err
	}
	reply, err := c.Chat(ctx, "spoken_answer", systemPrompt, user)
	if err != nil {
		return Answer{}, err
	}
	var out Answer
	if err := decode(reply.Text, &out); err != nil {
		return Answer{}, err
	}
	out.Spoken = endSentence(Spoken(out.Spoken, 2))
	if out.Spoken == "" {
		return Answer{}, errors.New("llm: spoken_answer: empty spoken")
	}
	return out, nil
}
