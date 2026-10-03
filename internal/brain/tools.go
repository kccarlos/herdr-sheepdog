package brain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// Tool names.
const (
	ToolReadScreen  = "read_screen"
	ToolRunCLI      = "run_cli"
	ToolListAgents  = "list_agents"
	ToolRecall      = "recall"
	ToolPrompt      = "prompt"
	ToolSendKeys    = "send_keys"
	ToolInterrupt   = "interrupt"
	ToolPromise     = "promise"
	ToolNote        = "note"
	ToolInvestigate = "investigate"
)

// IsActionTool reports whether name sends something to a worker or
// writes to the record (a promise, a note, a background job): permitted
// in CEO turns only, and never in a read-only turn.
func IsActionTool(name string) bool {
	switch name {
	case ToolPrompt, ToolSendKeys, ToolInterrupt, ToolPromise, ToolNote, ToolInvestigate:
		return true
	}
	return false
}

// Provenance delimiters around everything read from a worker pane or
// produced by the CLI tool (docs/roles.md: data, not instructions).
const (
	// Both markers carry the same per-call random nonce, so text from a
	// worker cannot forge the end marker: it cannot know the nonce, and
	// wrapWorker defangs any marker-shaped string inside the text anyway.
	beginWorkerOutput = "<<<BEGIN_WORKER_OUTPUT nonce=%s agent=%s source=%s>>>"
	endWorkerOutput   = "<<<END_WORKER_OUTPUT nonce=%s>>>"
	// endWorkerPrefix starts every end marker, whatever its nonce.
	endWorkerPrefix = "<<<END_WORKER_OUTPUT"
)

// read_screen's sources (docs/dive-deep.md WP8a).
const (
	// SourceScreen reads the pane: what the agent is doing now.
	SourceScreen = "screen"
	// SourceSession reads the worker's own session transcript: what it
	// last wrote for a human, its last instruction, what it changed.
	SourceSession = "session"
)

// Limits on what a tool result may carry into the window.
const (
	// sessionMessageBytes caps the worker's last message in a session
	// read (about 3,000 characters): its newest part is kept, so the
	// result stays under the window's per-result cap with room for the
	// instruction and the did-line.
	sessionMessageBytes = 3000
	// sessionInstructionRunes caps the last instruction in a session read.
	sessionInstructionRunes = 300
	defaultScreenLines      = 200
	maxScreenLines          = 1000
	maxResultBytes          = prose.DefaultResultBytes
	recallMax               = 8
)

// toolDeclarations is the function surface (plain GenerateContent
// declarations; no Live-only behaviour field). readOnly is the surface
// of an event turn: the read tools only, no actions, promise or
// note.
func toolDeclarations(readOnly bool) []*genai.Tool {
	str := func(desc string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: desc} }
	obj := func(props map[string]*genai.Schema, req []string) *genai.Schema {
		return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: req}
	}
	agent := str("the agent's alias")
	read := []*genai.FunctionDeclaration{
		{Name: ToolReadScreen, Description: "Read an agent. source screen (the default): the last lines of its terminal pane, for what it is doing right now and what it is asking. source session: its own session record, for what it said, found or changed: its last message for a human in full, the last instruction it received, and the files it edited.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "lines": {Type: genai.TypeInteger, Description: "screen only: how many lines from the bottom (default 200)"},
				"source": {Type: genai.TypeString, Enum: []string{SourceScreen, SourceSession}, Description: "screen (default) or session"}}, []string{"agent"})},
		{Name: ToolRunCLI, Description: "Ask a read-only CLI agent a question about the agent's repository (diff, logs, why a test fails). Slow (tens of seconds). Phrase the question with full context: the CLI has not heard this conversation.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "question": str("a self-contained question")}, []string{"agent", "question"})},
		{Name: ToolListAgents, Description: "The roster: every agent with its state, task and pending question.",
			Parameters: obj(map[string]*genai.Schema{}, nil)},
		{Name: ToolRecall, Description: "Search the record: last results, last tasks, last questions, notes, promises and earlier transcript lines matching the query.",
			Parameters: obj(map[string]*genai.Schema{"query": str("words to look for; empty for the most recent record")}, nil)},
	}
	if readOnly {
		return []*genai.Tool{{FunctionDeclarations: read}}
	}
	act := []*genai.FunctionDeclaration{
		{Name: ToolPrompt, Description: "Send an instruction to an agent as typed text. Its own approval prompts still apply. Use for anything the CEO tells an agent to do. " +
			"To start a new agent instead, only when the CEO asks for one, omit agent and give new_agent_dir; text is then its first instruction (optional). " +
			"A directory that is not a known project is refused with the list of projects: ask the CEO which one. Its first question (e.g. trusting the folder) comes to the CEO like any other. " +
			"Codex runs on a free plan: start one only when the CEO asks for Codex by name.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "text": str("the instruction, complete and self-contained"),
				"new_agent_dir":   str("only to start a new agent: the project's directory name as the CEO said it, or its path"),
				"new_agent_kind":  {Type: genai.TypeString, Enum: SpawnKinds, Description: "only to start a new agent: claude (default) or codex"},
				"new_agent_model": str("only when the CEO asks for a model; for a cheap Claude, haiku"),
			}, nil)},
		{Name: ToolSendKeys, Description: "Answer an agent's approval prompt with the keys of one of its on-screen options (see the roster). Permitted only when the CEO authorised that answer in this turn.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "keys": {Type: genai.TypeArray, Items: str("one key name, e.g. enter, esc, y, 1"), Description: "the option's keys, in order"}}, []string{"agent", "keys"})},
		{Name: ToolInterrupt, Description: "Send Ctrl-C to an agent to stop what it is doing. The result says whether it stopped.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent}, []string{"agent"})},
		{Name: ToolPromise, Description: "Record promised work: the CEO asked to be told when something happens to an agent (\"tell me when it's done\", \"let me know if it asks\", \"remind me\"). The supervisor keeps it: it tells the CEO at the next matching event, whatever the chattiness level. Use it in the same turn as any instruction that goes with it.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "text": str("what the CEO asked to be told, in their words"), "event": {Type: genai.TypeString, Enum: PromiseEvents, Description: "the condition: done (it finished), blocked (it asks something), idle, or any_change"},
				"due_in_minutes": {Type: genai.TypeInteger, Description: "only when the CEO gave a deadline or asked to be interrupted at once (0); a promise kept past its due time interrupts instead of waiting for a pause"}}, []string{"agent", "text", "event"})},
		{Name: ToolInvestigate, Description: "Start a background look into something the screen cannot answer and that may take more than a few seconds. source cli (the default): a fresh read-only CLI agent in the agent's repository, for questions about the code itself. source worker: the worker's own session, asked through a read-only copy that remembers everything it saw and did, for why it did something, what it tried, or to explain its decision; it never touches the worker. It returns at once with a job id; the answer reaches the CEO on its own when the job returns, so acknowledge with pending true and say roughly how long. Phrase the question with full context: neither has heard this conversation. One live investigation per agent.",
			Parameters: obj(map[string]*genai.Schema{"agent": agent, "question": str("a self-contained question"), "follow_up_of": str("the id of the investigation this digs deeper into, to resume the same session"),
				"source": {Type: genai.TypeString, Enum: []string{InvestigationSourceCLI, InvestigationSourceWorker}, Description: "cli (default, the repository) or worker (the worker's own memory)"}}, []string{"agent", "question"})},
		{Name: ToolNote, Description: "Remember something the CEO decided or wants kept in mind (\"remember the migrations are off limits today\", \"we are on the release branch\"). Notes are in the ledger of every later turn. Not for promised work: that is promise. With agent and name it renames that agent instead (\"call that one docs\"): the CEO hears it by that name from then on.",
			Parameters: obj(map[string]*genai.Schema{"text": str("what to remember, in the CEO's words"), "agent": str("only to rename: the agent's alias"), "name": str("only to rename: the new name, as the CEO said it")}, nil)},
	}
	return []*genai.Tool{{FunctionDeclarations: append(read, act...)}}
}

// Per-tool timeouts: a tool that hangs gets a "timed out" result the
// model can still answer from, instead of running out the whole turn
// (pipecat's per-function timeout_secs). Constants, not config keys
// (docs/iteration-2.md rule 4). From the event log: read_screen p90
// 52 ms, max 1.6 s; recall and list_agents a few ms; investigate (the
// start only) 6 ms; run_cli ran into the 12 s turn budget twice.
const (
	toolTimeoutQuick       = 2 * time.Second // list_agents, recall (in memory)
	toolTimeoutReadScreen  = 4 * time.Second // read_screen, screen or session
	toolTimeoutInvestigate = 5 * time.Second // investigate: starting the job only
	toolTimeoutRunCLI      = 8 * time.Second // run_cli
	// toolTimeoutMin is the least a tool gets when the turn's budget
	// is nearly spent.
	toolTimeoutMin = time.Second
)

// toolTimeout returns name's timeout and whether the call may be
// abandoned when it expires (a read: nothing was sent, so the result
// can be handed back while the host call is still unwinding). The
// action tools (prompt, send_keys, interrupt, promise, note) have none:
// cutting a send short leaves "maybe sent" with no echo to report, and
// they took ~300 ms at most in the logs; the turn's budget bounds them.
func toolTimeout(name string) (limit time.Duration, abandon bool) {
	switch name {
	case ToolListAgents, ToolRecall:
		return toolTimeoutQuick, true
	case ToolReadScreen:
		return toolTimeoutReadScreen, true
	case ToolRunCLI:
		return toolTimeoutRunCLI, true
	case ToolInvestigate:
		// The start is waited for: a job that did start must be
		// reported as started.
		return toolTimeoutInvestigate, false
	default:
		return 0, false
	}
}

// runTool runs one call under its tool timeout (toolTimeout), cut so
// that StepReserve of the turn's deadline is left for the answer. On
// expiry (the tool's own deadline, not the turn's) the outcome is an
// error saying so, and limit is the timeout that expired (0 otherwise).
func (b *Brain) runTool(ctx context.Context, t *turn, call *genai.FunctionCall) (out toolOutcome, limit time.Duration) {
	limit, abandon := toolTimeout(call.Name)
	if limit <= 0 {
		return b.execTool(ctx, t, call), 0
	}
	if dl, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(dl)-b.cfg.StepReserve)
	}
	limit = max(limit, toolTimeoutMin)
	tctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	timedOut := func() bool { return errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil }
	expired := func() toolOutcome {
		agent, _ := call.Args["agent"].(string)
		if row, ok := b.host.Resolve(agent); ok {
			agent = row.Alias
		}
		return toolOutcome{agent: agent, err: fmt.Errorf("%s timed out after %s: it did not answer in time; answer from what you have, or say it is not responding", call.Name, seconds(limit))}
	}
	if !abandon {
		out = b.execTool(tctx, t, call)
		if out.err != nil && timedOut() {
			return expired(), limit
		}
		return out, 0
	}
	ch := make(chan toolOutcome, 1)
	go func() { ch <- b.execTool(tctx, t, call) }()
	select {
	case out = <-ch:
		if out.err != nil && timedOut() {
			return expired(), limit
		}
		return out, 0
	case <-tctx.Done():
		if !timedOut() {
			// The turn ended (budget, stop, Close): that is the turn's
			// fallback, not a tool timeout.
			return toolOutcome{err: fmt.Errorf("the turn ended while %s ran: %w", call.Name, ctx.Err())}, 0
		}
		return expired(), limit
	}
}

// seconds renders d for a sentence the model reads: "4 s", "2.5 s".
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Round(100*time.Millisecond).Seconds(), 'f', -1, 64) + " s"
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
	// railBlocked marks a send_keys the rail refused, or any action tool
	// called in an event-triggered turn.
	railBlocked string
	agent       string
	// pane is the agent's pane when the call named one.
	pane string
	// promise is the record a promise call created.
	promise *PromiseInfo
}

// execTool runs one function call against the Host. t is the turn: its
// trigger and kind decide whether an action tool may run at all, and
// its number stamps a promise.
func (b *Brain) execTool(ctx context.Context, t *turn, call *genai.FunctionCall) toolOutcome {
	trigger, turn := t.req.Trigger, t.n
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
	if IsActionTool(call.Name) {
		if trigger != TriggerCEO || t.req.Kind.ReadOnly() {
			// Event turns are read-only: they are not declared
			// these tools, and the executor refuses them regardless (the
			// model may still name one).
			reason := call.Name + " is only permitted in a turn the CEO's own words triggered, never on an event"
			return toolOutcome{railBlocked: reason, err: fmt.Errorf("%w: %s", ErrNotPermitted, reason)}
		}
		if err := ctx.Err(); err != nil {
			// The turn was cancelled (the CEO said stop) between the
			// model answering and its calls running: nothing is sent.
			return toolOutcome{err: fmt.Errorf("the turn was cancelled before %s ran: %w", call.Name, err)}
		}
	}
	if call.Name == ToolPrompt && spawning(call.Args) {
		return b.spawn(ctx, arg)
	}
	if call.Name == ToolNote && arg("name") != "" {
		row, ok := b.host.Resolve(arg("agent"))
		if !ok {
			return toolOutcome{err: fmt.Errorf("to rename, name the agent: no agent called %q; the agents are %s", arg("agent"), b.aliases())}
		}
		echo, err := b.host.Rename(ctx, row.PaneID, arg("name"))
		if err != nil {
			return toolOutcome{agent: row.Alias, err: err}
		}
		return toolOutcome{agent: row.Alias, pane: row.PaneID, acted: echo, result: echo}
	}
	if call.Name == ToolNote {
		text := arg("text")
		if text == "" {
			return toolOutcome{err: errors.New("note needs the text to remember")}
		}
		echo, err := b.host.Note(text)
		if err != nil {
			return toolOutcome{err: err}
		}
		return toolOutcome{acted: echo, result: echo}
	}
	row, ok := b.host.Resolve(arg("agent"))
	if !ok {
		return toolOutcome{err: fmt.Errorf("no agent called %q; the agents are %s", arg("agent"), b.aliases())}
	}
	out := toolOutcome{agent: row.Alias, pane: row.PaneID}
	switch call.Name {
	case ToolReadScreen:
		switch source := strings.ToLower(arg("source")); source {
		case "", SourceScreen:
		case SourceSession:
			return b.readSession(ctx, row, out)
		default:
			out.err = fmt.Errorf("read_screen source %q: want %s or %s", source, SourceScreen, SourceSession)
			return out
		}
		lines := defaultScreenLines
		if n, ok := call.Args["lines"].(float64); ok && n > 0 {
			lines = min(int(n), maxScreenLines)
		}
		text, err := b.host.ReadScreen(ctx, row.PaneID, lines)
		if err != nil {
			out.err = err
			return out
		}
		text = prose.TailBytes(text, maxResultBytes)
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
		text = prose.TailBytes(text, maxResultBytes)
		out.result = wrapWorker(row.Alias, "cli", text)
		out.summary = fmt.Sprintf("cli output for %s, %d bytes", row.Alias, len(text))
	case ToolInvestigate:
		q := arg("question")
		if q == "" {
			out.err = errors.New("investigate needs a question")
			return out
		}
		start, err := b.host.Investigate(ctx, row.PaneID, InvestigationRequest{Question: q, FollowUpOf: arg("follow_up_of"), Source: arg("source"), Asked: t.req.Question, Turn: turn})
		if err != nil {
			out.err = err
			return out
		}
		out.result = mustJSON(start)
		out.summary = fmt.Sprintf("investigation %s of %s started", start.Job, row.Alias)
		// The job is running whatever happens to the rest of this turn,
		// so the fallback path must say so rather than report the roster
		// as if nothing were under way.
		out.acted = "Looking into " + aliasOr(row.Alias) + " now; the answer comes on its own."
	case ToolPrompt:
		text := arg("text")
		if text == "" {
			out.err = errors.New("prompt needs text")
			return out
		}
		// A runaway guard (campaign D N7, bench-pronoun-followup r5): the
		// same instruction to the same agent twice in one turn is typed
		// once; the second call gets the fact.
		key := row.PaneID + "\x00" + strings.ToLower(strings.Join(strings.Fields(text), " "))
		if t.prompted[key] {
			out.result = "That exact instruction already went to " + aliasOr(row.Alias) + " in this turn; it was not sent again."
			b.log.Log(logSource, "brain.prompt_repeat", "", map[string]any{"agent": row.Alias, "turn": turn})
			return out
		}
		echo, err := b.host.Prompt(ctx, row.PaneID, text)
		if err != nil {
			out.err = err
			return out
		}
		if t.prompted == nil {
			t.prompted = map[string]bool{}
		}
		t.prompted[key] = true
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
	case ToolPromise:
		text, event := arg("text"), strings.ToLower(arg("event"))
		if text == "" {
			out.err = errors.New("promise needs the text of what the CEO asked to be told")
			return out
		}
		if !validPromiseEvent(event) {
			out.err = fmt.Errorf("promise event %q: want one of %s", event, strings.Join(PromiseEvents, ", "))
			return out
		}
		req := PromiseRequest{Text: text, Event: event, Turn: turn}
		if n, ok := call.Args["due_in_minutes"].(float64); ok && n >= 0 {
			due := b.cfg.Now().Add(time.Duration(n * float64(time.Minute)))
			req.Due = &due
		}
		p, err := b.host.Promise(ctx, row.PaneID, req)
		if err != nil {
			out.err = err
			return out
		}
		out.promise = &p
		echo := "I will tell you " + promiseCondition(p) + "."
		out.acted, out.result = echo, echo
	default:
		out.err = fmt.Errorf("unknown tool %q", call.Name)
	}
	return out
}

// readSession is read_screen with source session: the worker's own
// session transcript through the Host, rendered by sessionText and
// wrapped as worker output. A session that cannot be read is an error
// the model reads (the worker.session_missing reason), pointing it back
// at the screen.
func (b *Brain) readSession(ctx context.Context, row AgentInfo, out toolOutcome) toolOutcome {
	read, err := b.host.ReadSession(ctx, row.PaneID)
	if err != nil {
		out.err = fmt.Errorf("%w; read_screen with source screen still shows what it is doing now", err)
		return out
	}
	text := sessionText(read)
	out.result = wrapWorker(row.Alias, SourceSession, text)
	out.summary = fmt.Sprintf("session of %s, last message %d chars", aliasOr(row.Alias), utf8.RuneCountInString(read.LastAssistant))
	return out
}

// sessionText renders a session read for the model: the last
// instruction, one line of what the worker did for it, then its last
// message whole up to sessionMessageBytes. A longer message loses its
// oldest part (the newest is where a worker puts its findings and
// sign-off), cut at a line boundary, and says so. The message comes
// last, because the window keeps the tail of a bulky result.
func sessionText(r SessionRead) string {
	var b strings.Builder
	if instr := strings.TrimSpace(r.Instruction); instr != "" {
		fmt.Fprintf(&b, "Last instruction it received: %s\n", clipRunes(strings.Join(strings.Fields(instr), " "), sessionInstructionRunes))
	}
	var did []string
	if len(r.Files) > 0 {
		did = append(did, "edited "+strings.Join(r.Files, ", "))
	}
	if len(r.Tools) > 0 {
		names := make([]string, 0, len(r.Tools))
		for name := range r.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		calls := make([]string, 0, len(names))
		for _, name := range names {
			calls = append(calls, fmt.Sprintf("%s %d", name, r.Tools[name]))
		}
		did = append(did, "tool calls: "+strings.Join(calls, ", "))
	}
	if len(did) > 0 {
		fmt.Fprintf(&b, "What it did for it: %s\n", strings.Join(did, "; "))
	}
	msg := strings.TrimSpace(r.LastAssistant)
	switch {
	case msg == "":
		b.WriteString("Its last message: none in its session record yet.")
	case len(msg) > sessionMessageBytes:
		kept := tailCut(msg, sessionMessageBytes)
		fmt.Fprintf(&b, "Its last message (the first %d of %d characters are cut; this is its end):\n…%s", utf8.RuneCountInString(msg)-utf8.RuneCountInString(kept), utf8.RuneCountInString(msg), strings.TrimSpace(kept))
	default:
		fmt.Fprintf(&b, "Its last message, whole:\n%s", msg)
	}
	return b.String()
}

// tailCut keeps the last max bytes of s, starting at a line break when
// one falls in the first quarter of them, else at a word boundary, else
// at a rune boundary.
func tailCut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < max/4 {
		return strings.TrimSpace(cut[i+1:])
	}
	if i := strings.IndexAny(cut, " \t"); i >= 0 && i < max/4 {
		return strings.TrimSpace(cut[i+1:])
	}
	for len(cut) > 0 && !utf8.RuneStart(cut[0]) {
		cut = cut[1:]
	}
	return strings.TrimSpace(cut)
}

// clipRunes cuts s to n runes, marking the cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// validPromiseEvent reports whether event is one of PromiseEvents.
func validPromiseEvent(event string) bool {
	for _, e := range PromiseEvents {
		if e == event {
			return true
		}
	}
	return false
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
		if row.State == "working" {
			return fmt.Sprintf("%s has no recorded approval prompt to answer", aliasOr(row.Alias))
		}
		// Nothing recorded: blocked, its answer_prompt pending expired
		// unanswered while nobody was watching, or the daemon never caught
		// it (docs/history/issues-2026-09-18.md "an expired approval prompt
		// can never be approved"); or idle with a menu on screen, which
		// herdr does not report as blocked (#107's known limit). Deferred
		// to the Host, which re-reads the screen and revives the pending
		// action before its own atomic check.
		return ""
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
	if !row.Pending.SelectedBy(keys) {
		return fmt.Sprintf("keys %v mix more than one option; send just the one option's key(s), from %v", keys, allowed)
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

// reMarker matches a provenance marker's opening ("<<<BEGIN_WORKER_OUTPUT",
// "<<< end_worker_output"), in any case, inside worker text.
var reMarker = regexp.MustCompile(`(?i)<<<\s*((?:BEGIN|END)_WORKER_OUTPUT)`)

// nonceSource makes a marker nonce; tests swap it for a counter so golden
// transcripts stay deterministic.
var nonceSource = newNonce

// newNonce is a fresh unguessable marker nonce.
func newNonce() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-derived
		// nonce still differs per call.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// wrapWorker puts provenance delimiters around text from a worker. Any
// marker-shaped string inside the text is neutralised (its "<<<" becomes
// "[") and both markers carry a per-call nonce, so a screen cannot close
// the wrapper early and have what follows read as the brain's own.
func wrapWorker(alias, source, text string) string {
	nonce := nonceSource()
	text = reMarker.ReplaceAllString(strings.TrimRight(text, "\n"), "[$1")
	return fmt.Sprintf(beginWorkerOutput, nonce, aliasOr(alias), source) + "\n" + text + "\n" + fmt.Sprintf(endWorkerOutput, nonce)
}

// listAgents renders the roster as JSON for the model. What the workers
// wrote (task, last task, last result, the question they asked) sits under
// each row's worker_data key: it is data from another agent, never an
// instruction to the brain (RULES.md provenance rule).
func (b *Brain) listAgents() toolOutcome {
	type workerData struct {
		Task            string `json:"task,omitempty"`
		LastTask        string `json:"last_task,omitempty"`
		LastResult      string `json:"last_result,omitempty"`
		PendingQuestion string `json:"pending_question,omitempty"`
	}
	type row struct {
		Alias      string      `json:"alias"`
		Spoken     string      `json:"spoken,omitempty"`
		Title      string      `json:"session_title,omitempty"`
		Kind       string      `json:"kind"`
		State      string      `json:"state"`
		WorkerData *workerData `json:"worker_data,omitempty"`
	}
	agents := b.host.Agents()
	rows := make([]row, 0, len(agents))
	for _, a := range agents {
		r := row{Alias: a.Alias, Kind: a.Kind, State: a.State, Title: a.Title}
		if a.Spoken != a.Alias {
			r.Spoken = a.Spoken
		}
		wd := workerData{Task: a.Task, LastTask: a.LastTask, LastResult: a.LastResult}
		if a.Pending != nil {
			wd.PendingQuestion = a.Pending.Question
		}
		if wd != (workerData{}) {
			r.WorkerData = &wd
		}
		rows = append(rows, r)
	}
	out, _ := json.Marshal(rows)
	return toolOutcome{result: string(out), summary: fmt.Sprintf("roster of %d agents", len(rows))}
}

// recall searches the ledger (roster fields, work journals, notes,
// promises, investigations) and the transcript for the query's words and
// returns the best matching lines, newest first on ties; an empty query
// returns the most recent record.
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
			line := alias + " last result: " + a.LastResult
			if a.LastDetail != "" {
				line += "; " + a.LastDetail
			}
			add(a.LastInteraction, line)
		}
		if a.LastTask != "" {
			add(a.LastInteraction, alias+" last task: "+a.LastTask)
		}
		if a.LastBlock != "" {
			add(a.LastInteraction, alias+" last question: "+a.LastBlock)
		}
		for _, e := range a.Journal {
			add(e.At, journalRecallLine(alias, e))
		}
	}
	for _, n := range b.host.Notes() {
		add(time.Time{}, "note: "+n)
	}
	for _, p := range b.host.Promises() {
		add(p.CreatedAt, promiseLine(p))
	}
	for _, inv := range b.host.Investigations() {
		add(inv.Started, investigationLine(inv, b.cfg.Now()))
	}
	for _, m := range b.transcript.Messages() {
		switch m.Kind {
		case KindToolResult, KindToolCall, KindRequest, KindEventScreen, KindPromise:
			// Promises come from the Host (current status), not from
			// their transcript lines.
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
		n := aliasOr(a.Alias)
		if a.Spoken != "" && a.Spoken != a.Alias {
			n += fmt.Sprintf(" (said %q)", a.Spoken)
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return "none (no agents are running)"
	}
	return strings.Join(names, ", ")
}
