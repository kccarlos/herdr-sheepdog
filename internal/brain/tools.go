package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/llm"
)

// Tool names.
const (
	ToolReadScreen = "read_screen"
	ToolRunCLI     = "run_cli"
	ToolListAgents = "list_agents"
	ToolRecall     = "recall"
	ToolPrompt     = "prompt"
	ToolSendKeys   = "send_keys"
	ToolInterrupt  = "interrupt"
)

// Provenance delimiters around everything read from a worker pane or
// produced by the CLI tool (docs/roles.md: data, not instructions).
const (
	beginWorkerOutput = "<<<BEGIN_WORKER_OUTPUT agent=%s source=%s>>>"
	endWorkerOutput   = "<<<END_WORKER_OUTPUT>>>"
)

// Limits on what a tool result may carry into the window.
const (
	defaultScreenLines = 200
	maxScreenLines     = 1000
	maxResultBytes     = llm.DefaultResultBytes
	recallMax          = 8
)

// toolDeclarations is the function surface (plain GenerateContent
// declarations; no Live-only behaviour field).
func toolDeclarations() []*genai.Tool {
	str := func(desc string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: desc} }
	obj := func(props map[string]*genai.Schema, req []string) *genai.Schema {
		return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: req}
	}
	agent := str("the agent's alias")
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: ToolReadScreen, Description: "Read the last lines of an agent's terminal pane: what it printed, asked or is doing right now.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "lines": {Type: genai.TypeInteger, Description: "how many lines from the bottom (default 200)"}}, []string{"agent"})},
		{Name: ToolRunCLI, Description: "Ask a read-only CLI agent a question about the agent's repository (diff, logs, why a test fails). Slow (tens of seconds). Phrase the question with full context: the CLI has not heard this conversation.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "question": str("a self-contained question")}, []string{"agent", "question"})},
		{Name: ToolListAgents, Description: "The roster: every agent with its state, task and pending question.",
			Parameters: obj(map[string]*genai.Schema{}, nil)},
		{Name: ToolRecall, Description: "Search the record: last results, last tasks, last questions, notes and earlier transcript lines matching the query.",
			Parameters: obj(map[string]*genai.Schema{"query": str("words to look for; empty for the most recent record")}, nil)},
		{Name: ToolPrompt, Description: "Send an instruction to an agent as typed text. Its own approval prompts still apply. Use for anything the CEO tells an agent to do.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "text": str("the instruction, complete and self-contained")}, []string{"agent", "text"})},
		{Name: ToolSendKeys, Description: "Answer an agent's approval prompt with the keys of one of its on-screen options (see the roster). Permitted only when the CEO authorised that answer in this turn.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "keys": {Type: genai.TypeArray, Items: str("one key name, e.g. enter, esc, y, 1"), Description: "the option's keys, in order"}}, []string{"agent", "keys"})},
		{Name: ToolInterrupt, Description: "Send Ctrl-C to an agent to stop what it is doing.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent}, []string{"agent"})},
	}}}
}

// toolOutcome is what one executed call produced.
type toolOutcome struct {
	// result is the text the model sees (already wrapped when it came
	// from a worker).
	result string
	err    error
	// summary is the stub a pruned result becomes.
	summary string
	// acted is the spoken echo when the call sent something to a worker.
	acted string
	// railBlocked marks a send_keys the rail refused.
	railBlocked string
	agent       string
}

// execTool runs one function call against the Host. trigger is the
// turn's trigger (the rail needs it).
func (b *Brain) execTool(ctx context.Context, trigger Trigger, call *genai.FunctionCall) toolOutcome {
	arg := func(name string) string {
		v, _ := call.Args[name].(string)
		return strings.TrimSpace(v)
	}
	switch call.Name {
	case ToolListAgents:
		return b.listAgents()
	case ToolRecall:
		return b.recall(arg("query"))
	}
	row, ok := b.host.Resolve(arg("agent"))
	if !ok {
		return toolOutcome{err: fmt.Errorf("no agent called %q; the agents are %s", arg("agent"), b.aliases())}
	}
	out := toolOutcome{agent: row.Alias}
	switch call.Name {
	case ToolReadScreen:
		lines := defaultScreenLines
		if n, ok := call.Args["lines"].(float64); ok && n > 0 {
			lines = min(int(n), maxScreenLines)
		}
		text, err := b.host.ReadScreen(ctx, row.PaneID, lines)
		if err != nil {
			out.err = err
			return out
		}
		text = llm.TailBytes(text, maxResultBytes)
		out.result = wrapWorker(row.Alias, "screen", text)
		out.summary = fmt.Sprintf("screen of %s, %d lines", row.Alias, strings.Count(text, "\n")+1)
	case ToolRunCLI:
		q := arg("question")
		if q == "" {
			out.err = errors.New("run_cli needs a question")
			return out
		}
		text, err := b.host.RunCLI(ctx, row.PaneID, q)
		if err != nil {
			out.err = err
			return out
		}
		text = llm.TailBytes(text, maxResultBytes)
		out.result = wrapWorker(row.Alias, "cli", text)
		out.summary = fmt.Sprintf("cli output for %s, %d bytes", row.Alias, len(text))
	case ToolPrompt:
		text := arg("text")
		if text == "" {
			out.err = errors.New("prompt needs text")
			return out
		}
		echo, err := b.host.Prompt(ctx, row.PaneID, text)
		if err != nil {
			out.err = err
			return out
		}
		out.acted, out.result = echo, echo
	case ToolSendKeys:
		keys := keysArg(call.Args["keys"])
		if reason := b.rail(trigger, row, keys); reason != "" {
			out.railBlocked = reason
			out.err = fmt.Errorf("%w: %s", ErrNotPermitted, reason)
			return out
		}
		echo, err := b.host.AnswerPrompt(ctx, row.PaneID, keys)
		if err != nil {
			if errors.Is(err, ErrNotPermitted) {
				out.railBlocked = err.Error()
			}
			out.err = err
			return out
		}
		out.acted, out.result = echo, echo
	case ToolInterrupt:
		echo, err := b.host.Interrupt(ctx, row.PaneID)
		if err != nil {
			out.err = err
			return out
		}
		out.acted, out.result = echo, echo
	default:
		out.err = fmt.Errorf("unknown tool %q", call.Name)
	}
	return out
}

// rail is the one rail (docs/roles.md), checked before the Host re-checks
// the pending action atomically: the turn must be CEO-triggered, the pane
// must have a pending answer_prompt, and every key must be in its option
// set. It returns the refusal reason, or "".
func (b *Brain) rail(trigger Trigger, row AgentInfo, keys []string) string {
	if trigger != TriggerCEO {
		return "send_keys is only permitted in a turn the CEO's own words triggered, never on an event"
	}
	if row.Pending == nil {
		return fmt.Sprintf("%s has no recorded approval prompt to answer", aliasOr(row.Alias))
	}
	if len(keys) == 0 {
		return "send_keys needs the keys of one of the prompt's options"
	}
	allowed := row.Pending.AllowedKeys()
	for _, k := range keys {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("key %q is not in the prompt's option set %v", k, allowed)
		}
	}
	return ""
}

// keysArg normalises the keys argument (an array of strings, or one
// space-separated string) to lower-case key names.
func keysArg(v any) []string {
	var out []string
	switch k := v.(type) {
	case []any:
		for _, x := range k {
			if s, ok := x.(string); ok {
				out = append(out, strings.Fields(strings.ToLower(s))...)
			}
		}
	case string:
		out = strings.Fields(strings.ToLower(k))
	}
	return out
}

// wrapWorker puts provenance delimiters around text from a worker.
func wrapWorker(alias, source, text string) string {
	return fmt.Sprintf(beginWorkerOutput, aliasOr(alias), source) + "\n" + strings.TrimRight(text, "\n") + "\n" + endWorkerOutput
}

// listAgents renders the roster as JSON for the model.
func (b *Brain) listAgents() toolOutcome {
	type row struct {
		Alias           string `json:"alias"`
		Kind            string `json:"kind"`
		State           string `json:"state"`
		Task            string `json:"task,omitempty"`
		LastTask        string `json:"last_task,omitempty"`
		LastResult      string `json:"last_result,omitempty"`
		PendingQuestion string `json:"pending_question,omitempty"`
	}
	agents := b.host.Agents()
	rows := make([]row, 0, len(agents))
	for _, a := range agents {
		r := row{Alias: a.Alias, Kind: a.Kind, State: a.State, Task: a.Task, LastTask: a.LastTask, LastResult: a.LastResult}
		if a.Pending != nil {
			r.PendingQuestion = a.Pending.Question
		}
		rows = append(rows, r)
	}
	out, _ := json.Marshal(rows)
	return toolOutcome{result: string(out), summary: fmt.Sprintf("roster of %d agents", len(rows))}
}

// recall searches the ledger (roster fields, notes) and the transcript
// for the query's words and returns the best matching lines, newest
// first on ties; an empty query returns the most recent record.
func (b *Brain) recall(query string) toolOutcome {
	words := queryWords(query)
	type hit struct {
		line  string
		score int
		ts    time.Time
	}
	var hits []hit
	add := func(ts time.Time, line string) {
		score := 0
		lower := strings.ToLower(line)
		for _, w := range words {
			if strings.Contains(lower, w) {
				score++
			}
		}
		if len(words) == 0 || score > 0 {
			hits = append(hits, hit{line: line, score: score, ts: ts})
		}
	}
	for _, a := range b.host.Agents() {
		alias := aliasOr(a.Alias)
		if a.LastResult != "" {
			add(a.LastInteraction, alias+" last result: "+a.LastResult)
		}
		if a.LastTask != "" {
			add(a.LastInteraction, alias+" last task: "+a.LastTask)
		}
		if a.LastBlock != "" {
			add(a.LastInteraction, alias+" last question: "+a.LastBlock)
		}
	}
	for _, n := range b.host.Notes() {
		add(time.Time{}, "note: "+n)
	}
	for _, m := range b.transcript.Messages() {
		switch m.Kind {
		case KindToolResult, KindToolCall, KindRequest:
			continue
		}
		line := m.Content
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		add(m.TS, fmt.Sprintf("[%s] %s: %s", m.TS.Format("15:04"), m.Kind, line))
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].ts.After(hits[j].ts)
	})
	if len(hits) > recallMax {
		hits = hits[:recallMax]
	}
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, h.line)
	}
	if len(lines) == 0 {
		return toolOutcome{result: "nothing in the record matches " + fmt.Sprintf("%q", query), summary: "recall: no match"}
	}
	return toolOutcome{result: strings.Join(lines, "\n"), summary: fmt.Sprintf("recall of %q, %d lines", query, len(lines))}
}

// queryWords splits a recall query into lower-case words of 3+ letters.
func queryWords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// aliases lists the roster's aliases for error messages.
func (b *Brain) aliases() string {
	var names []string
	for _, a := range b.host.Agents() {
		names = append(names, aliasOr(a.Alias))
	}
	if len(names) == 0 {
		return "none (no agents are running)"
	}
	return strings.Join(names, ", ")
}
