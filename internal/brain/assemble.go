package brain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

// toContents renders the window as Gemini contents for provider/model,
// in modelOrder (a step's calls and results adjacent). Context messages (utterances, replies of the assistant, pushes, worker
// events, notes, requests, summaries) become user text lines;
// consecutive ones merge into one content. A step's tool calls become
// the model content the provider returned (Raw, with its thought
// signatures) when the same provider and model made them, else function
// call parts rebuilt from the neutral fields (another model's signatures
// would not be accepted); their results become function response parts.
// A step that made several calls is one tool-call message with Raw
// followed by Raw-less ones: Raw already holds every call of the step,
// so those are skipped when it is replayed and merged into one model
// content when it is not. Replies are model text.
func toContents(msgs []Message, provider, model string) []*genai.Content {
	return toContentsTraced(msgs, provider, model, nil)
}

// origin maps one window message onto the request built from it (the
// Context tab's section map): item is the content (Gemini) or message
// (OpenAI-compatible) index, part the part or tool call index within
// it (-1: the whole item), and start/end the message's byte range in a
// text merged from several messages (-1: the whole part).
type origin struct {
	msg, item, part, start, end int
}

// toContentsTraced is toContents, recording where each message went into
// tr when it is not nil.
func toContentsTraced(msgs []Message, provider, model string, tr *[]origin) []*genai.Content {
	var out []*genai.Content
	note := func(o origin) {
		if tr != nil {
			*tr = append(*tr, o)
		}
	}
	var text []string // pending user lines
	var textMsgs []int
	flushText := func() {
		if len(text) == 0 {
			return
		}
		pos := 0
		for i, line := range text {
			note(origin{msg: textMsgs[i], item: len(out), part: 0, start: pos, end: pos + len(line)})
			pos += len(line) + 1
		}
		out = append(out, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: strings.Join(text, "\n")}}})
		text, textMsgs = nil, nil
	}
	var calls []*genai.Part // pending model function-call parts (no Raw)
	var callMsgs []int
	flushCalls := func() {
		if len(calls) == 0 {
			return
		}
		for i, m := range callMsgs {
			note(origin{msg: m, item: len(out), part: i, start: -1, end: -1})
		}
		out = append(out, &genai.Content{Role: genai.RoleModel, Parts: calls})
		calls, callMsgs = nil, nil
	}
	var results []*genai.Part // pending function response parts
	var resultMsgs []int
	flushResults := func() {
		if len(results) == 0 {
			return
		}
		for i, m := range resultMsgs {
			note(origin{msg: m, item: len(out), part: i, start: -1, end: -1})
		}
		out = append(out, &genai.Content{Role: genai.RoleUser, Parts: results})
		results, resultMsgs = nil, nil
	}
	flushAll := func() { flushText(); flushCalls(); flushResults() }
	rawCovers := false // the step's Raw was replayed: its other calls are in it

	for _, mi := range modelOrder(msgs) {
		m := msgs[mi]
		if m.Kind != KindToolCall {
			rawCovers = false
		}
		switch m.Kind {
		case KindToolCall:
			flushText()
			flushResults()
			if len(m.Raw) > 0 && sameOrigin(m, provider, model) {
				flushCalls()
				var c genai.Content
				if err := json.Unmarshal(m.Raw, &c); err == nil && len(c.Parts) > 0 {
					c.Role = genai.RoleModel
					note(origin{msg: mi, item: len(out), part: -1, start: -1, end: -1})
					out = append(out, &c)
					rawCovers = true
					continue
				}
			}
			if len(m.Raw) > 0 {
				// Another origin's Raw: this call starts a step of its own.
				rawCovers = false
			}
			if rawCovers {
				continue
			}
			if m.ToolCall != nil {
				calls = append(calls, &genai.Part{FunctionCall: &genai.FunctionCall{Name: m.ToolCall.Name, Args: m.ToolCall.Args}})
				callMsgs = append(callMsgs, mi)
			}
		case KindToolResult:
			flushText()
			flushCalls()
			name := "tool"
			if m.ToolResult != nil {
				name = m.ToolResult.Name
			}
			resp := map[string]any{"output": m.Content}
			if m.ToolResult != nil && m.ToolResult.Error != "" {
				resp = map[string]any{"error": m.ToolResult.Error}
			}
			results = append(results, &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: name, Response: resp}})
			resultMsgs = append(resultMsgs, mi)
		case KindReply:
			flushAll()
			note(origin{msg: mi, item: len(out), part: 0, start: -1, end: -1})
			out = append(out, &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: m.Content}}})
		default:
			flushCalls()
			flushResults()
			text = append(text, contextLine(m))
			textMsgs = append(textMsgs, mi)
		}
	}
	flushAll()
	return out
}

// modelOrder is the order both renderers visit the window in, as
// indices into msgs (so the origins they record still name the
// transcript's messages): the transcript order, except that a step's
// tool calls and their results stay adjacent. Every provider requires
// it — an assistant message with tool_calls must be followed by a tool
// message for each call (DeepSeek's HTTP 400 otherwise), a Gemini
// function call by its function responses — but the transcript is
// written by several goroutines: the assistant's speech (Brain.Said)
// and pushes are ingested while a step's tools execute, so "On it."
// lands between a read_screen call and its result (2026-09-24 18:16:43,
// turn 6), and because that order stayed in the window, every later
// call of the run failed until "New conversation". So a context message
// (an utterance, the assistant's speech, a push, a worker event, a note,
// a request…) that falls between a step's calls and the last of their
// results is moved to just after those results, keeping the relative
// order of everything moved. This also heals a transcript recorded
// before the fix: the order is decided at assembly, not at ingestion.
//
// A step whose results never all arrived — the loop records one for
// every call it executes (execCalls), so only a transcript cut short (a
// daemon killed mid-step) or trimmed by an older compaction can hold one
// — is dropped whole, calls and the results it has: a dangling call is
// refused by the provider, and a Raw replay would re-list every call of
// the step anyway. It counts as over at a reply, at a message of another
// turn, at a call after its results began, or at the end of the window.
// A result whose call is not in the window is dropped too.
func modelOrder(msgs []Message) []int {
	order := make([]int, 0, len(msgs))
	var step, deferred []int // the open step's calls and results; context moved past it
	pending := map[string]bool{}
	stepTurn, results := 0, false
	closeStep := func() {
		if len(pending) == 0 {
			order = append(order, step...)
		}
		order = append(order, deferred...)
		step, deferred, results = nil, nil, false
		clear(pending)
	}
	for i, m := range msgs {
		switch m.Kind {
		case KindToolCall:
			if results {
				// A new step while the last one still waits for a result.
				closeStep()
			}
			if len(step) == 0 {
				stepTurn = m.Turn
			}
			step = append(step, i)
			pending[callKey(m)] = true
		case KindToolResult:
			k := resultKey(m)
			if !pending[k] {
				continue // its call is not in the window (or already answered)
			}
			delete(pending, k)
			step, results = append(step, i), true
			if len(pending) == 0 {
				closeStep()
			}
		default:
			if len(step) > 0 && (m.Kind == KindReply || (m.Turn != 0 && m.Turn != stepTurn)) {
				closeStep()
			}
			if len(step) > 0 {
				deferred = append(deferred, i)
				continue
			}
			order = append(order, i)
		}
	}
	if len(step) > 0 {
		closeStep()
	}
	return order
}

// callKey and resultKey pair a tool call with its result: by id, or by
// the tool's name when a message has none.
func callKey(m Message) string {
	if m.ToolCall == nil {
		return "?"
	}
	if m.ToolCall.ID != "" {
		return m.ToolCall.ID
	}
	return "name:" + m.ToolCall.Name
}

func resultKey(m Message) string {
	if m.ToolResult == nil {
		return "?"
	}
	if m.ToolResult.ID != "" {
		return m.ToolResult.ID
	}
	return "name:" + m.ToolResult.Name
}

// sameOrigin reports whether m's Raw was produced by provider/model, so
// the adapter may replay it. A message without a recorded provider or
// model (a transcript from before they were recorded) is taken to be
// the caller's.
func sameOrigin(m Message, provider, model string) bool {
	return (m.Provider == "" || m.Provider == provider) && (m.Model == "" || m.Model == model)
}

// contextLine renders one context message for the model.
func contextLine(m Message) string {
	ts := m.TS.Format("15:04:05")
	switch m.Kind {
	case KindCEOUtterance:
		return fmt.Sprintf("[%s] CEO said: %q", ts, m.Content)
	case KindAssistantSaid:
		if m.Cut {
			if m.Content == "" {
				return fmt.Sprintf("[%s] Assistant started to speak but was cut off before the CEO heard any of it.", ts)
			}
			return fmt.Sprintf("[%s] Assistant said (cut off after %.1f s): %q", ts, float64(m.CutAfterMs)/1000, m.Content)
		}
		return fmt.Sprintf("[%s] Assistant said: %q", ts, m.Content)
	case KindPush:
		return fmt.Sprintf("[%s] Pushed to the assistant: %q", ts, m.Content)
	case KindWorkerEvent:
		return fmt.Sprintf("[%s] Worker event: %s", ts, m.Content)
	case KindNote:
		return fmt.Sprintf("[%s] Note from the CEO: %q", ts, m.Content)
	case KindCompaction:
		return fmt.Sprintf("[summary of the conversation up to %s] %s", ts, m.Content)
	case KindRequest:
		return fmt.Sprintf("[%s] %s", ts, m.Content)
	case KindPromise:
		return fmt.Sprintf("[%s] %s", ts, m.Content)
	case KindEventScreen:
		if m.ToolResult != nil && m.ToolResult.Name == toolWorkerLog {
			return fmt.Sprintf("[%s] What the worker wrote (its own last message, from its session transcript):\n%s", ts, m.Content)
		}
		return fmt.Sprintf("[%s] The screen the supervisor read for this event:\n%s", ts, m.Content)
	case KindSystem:
		return fmt.Sprintf("[%s] Note from the daemon: %s", ts, m.Content)
	case KindState:
		return m.Content
	default:
		return fmt.Sprintf("[%s] %s: %s", ts, m.Kind, m.Content)
	}
}

// requestText is the request message content for kind and req. The
// first line's shape is parsed by braintest's reflex model and by
// web/debug/supervisor.js; keep it.
func requestText(req Request) string {
	switch {
	case req.Kind == KindInvestigation:
		return investigationRequestText(req)
	case req.Kind.IsEvent():
		return eventRequestText(req)
	}
	s := fmt.Sprintf("Request from the assistant (%s): utterance=%q", KindUtterance, req.Question)
	if req.Agent != "" {
		s += fmt.Sprintf(" agent=%q", req.Agent)
	} else {
		s += " agent=(none: the CEO did not say a name)"
	}
	if req.Trigger == TriggerEvent {
		s += " [triggered by an event, not by the CEO]"
	}
	if req.Briefing != "" {
		s += "\nStatus as the daemon has it: " + req.Briefing
	}
	if line := heardLine(req.facts.Heard); line != "" {
		s += "\n" + line
	}
	if line := languageLine(req.facts.Language); line != "" {
		s += "\n" + line
	}
	if req.records != "" {
		s += "\n" + req.records
	}
	if req.RosterUnchangedFor > 0 {
		s += fmt.Sprintf("\nThe roster is unchanged since you last reported it %d s ago; if the shepherd is only asking for status again, say there has been no change since you last checked, briefly.", int(req.RosterUnchangedFor.Seconds()))
	}
	return s + "\nThose are the CEO's own words, relayed verbatim: decide what they are (a status question, something to find out, an instruction, an answer to a worker's prompt, a promise, a note, small talk) and act. Use tools as needed, then reply with the JSON object of the answer contract."
}

// investigationRequestText is the request of a delivery turn: the job
// the CEO is owed an answer to, what they asked in their own words, and
// the sub-agent's answer wrapped as worker output (data, not
// instructions). The first line keeps the event shape braintest's reflex
// model and web/debug/supervisor.js parse.
func investigationRequestText(req Request) string {
	inv := req.Investigation
	if inv == nil {
		inv = &InvestigationInfo{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Event from the supervisor (investigation): agent=%q event=%q template=%q\n", aliasOr(req.Agent), "investigation.done", req.Template)
	fmt.Fprintf(&b, "The look you started (%s, through %s) came back. You asked it: %q\n", inv.ID, sourceOr(inv.Source, inv.Backend), inv.Question)
	if inv.Asked != "" {
		fmt.Fprintf(&b, "What the CEO actually said: %q\n", inv.Asked)
	}
	if inv.FollowUpOf != "" {
		fmt.Fprintf(&b, "It digs deeper into investigation %s, so they already heard that answer: do not repeat it.\n", inv.FollowUpOf)
	}
	if line := languageLine(req.facts.Language); line != "" {
		b.WriteString(line + "\n")
	}
	if req.Result != "" {
		b.WriteString("What it found:\n")
		b.WriteString(wrapWorker(req.Agent, "investigation", req.Result))
		b.WriteString("\n")
	}
	b.WriteString("Answer the CEO now, from this and the conversation: spoken is the outcome in at most two short sentences for the ear, what happened and what it means for them; the next move, if any, is the ask; detail explains each term in everyday words. No paths, hashes or ids, jargon only if they used it first. If the agent has worked since, say the answer is as of when the look ran. Reply with the JSON object of the answer contract; read tools only, and say nothing about how the look was run.")
	return b.String()
}

// sourceOr names how an investigation ran, for the request line.
func sourceOr(source, backend string) string {
	switch {
	case source == InvestigationSourceWorker:
		return "the agent itself"
	case backend != "":
		return backend
	default:
		return "a read-only CLI agent"
	}
}

// eventRequestText is the request message of an announce turn (parsed by
// braintest's reflex model and web/debug/supervisor.js; keep the shape of
// the first line and of the promise lines).
func eventRequestText(req Request) string {
	agent := aliasOr(req.Agent)
	event := req.Event
	if event == "" {
		event = "changed state"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Event from the supervisor (announce): agent=%q event=%q template=%q\n", agent, event, req.Template)
	if len(req.Promises) > 0 {
		b.WriteString("Promises this event keeps (the CEO asked to be told; your line says so, with the result):\n")
		for _, p := range req.Promises {
			fmt.Fprintf(&b, "- %s: %q (%s)\n", p.ID, p.Text, promiseCondition(p))
		}
	}
	if req.Owed {
		b.WriteString("The CEO is waiting on this: the agent was working on their request. The finish is not spoken until your line lands, so your line is what the CEO hears: say what it found or did, the gist from the screen, not only that it finished.\n")
	}
	if req.AlsoAsking != "" {
		fmt.Fprintf(&b, "After finishing, the agent is also showing a prompt of its own: %q. Your one line covers both: what it found, then that it is asking this, with the question in ask, needs_user true and the prompt's choices in options.\n", req.AlsoAsking)
	}
	if line := promptStateLine(req.facts.Prompt); line != "" {
		b.WriteString(line + "\n")
	}
	if line := languageLine(req.facts.Language); line != "" {
		b.WriteString(line + "\n")
	}
	if len(req.facts.Spoken) > 0 {
		fmt.Fprintf(&b, "Already said to the CEO about this event, word for word: %s. Your spoken would be said after it, not instead of it. Decide whether the assistant says anything more: skip when that already carries the news, or add only what it lacked (which request of theirs it fulfils, what changed against what they asked, what is still open), or correct it if it was wrong; silent when it is context the assistant should have but not say.", quotedLines(req.facts.Spoken))
	} else {
		b.WriteString("Nothing has been said about this yet. Decide whether the assistant says anything, what, and how urgently: your spoken replaces the template sentence, so carry its news (what the agent did, in the CEO's terms) plus what the sentence lacked — which request of theirs it fulfils, what changed against what they asked, what is still open. Skip when the CEO already knows (the assistant relayed it, or they just asked and heard it) or it is not worth their attention; silent when it is context the assistant should have but not say.")
	}
	b.WriteString(" Reply with the JSON object of the answer contract plus \"urgency\": one of interrupt (the CEO must act now), idle (at the next pause), silent (context only, not spoken) or skip (say nothing); \"keeps_promise_ids\" for the promise ids above your line keeps; \"relates_to\" for the CEO's request it fulfils; needs_user true when they must decide or act. One line for the ear: spoken at most two short sentences, the one question in ask. The screen the supervisor read is above; read tools only.")
	return b.String()
}

// heardLine states what the CEO had heard when they began speaking,
// when the voice was given a line after that: their words were said
// before they heard it, so they cannot answer it (H6). "" otherwise.
func heardLine(h *HeardFacts) string {
	if h == nil || h.SpokeAt.IsZero() || len(h.After) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The CEO began saying these words at %s.", h.SpokeAt.Format("15:04:05"))
	if h.Before != nil {
		fmt.Fprintf(&b, " The last line the voice was given before that (%s): %q%s.", h.Before.At.Format("15:04:05"), h.Before.Text, unheardMark(h.Before.Unheard))
	}
	b.WriteString(" Given to the voice after they began, so not heard when they spoke: ")
	b.WriteString(pushedLines(h.After, h.SpokeAt))
	b.WriteString(". Their words answer what they had heard, not these; if they could mean either, ask.")
	return b.String()
}

// pushedLines renders lines as [15:04:06, 2 s after] "text"; ….
func pushedLines(lines []PushedLine, since time.Time) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = fmt.Sprintf("[%s, %d s after] %q%s", l.At.Format("15:04:05"), int(l.At.Sub(since).Seconds()), l.Text, unheardMark(l.Unheard))
	}
	return strings.Join(parts, "; ")
}

func unheardMark(unheard bool) string {
	if unheard {
		return " (judged not heard)"
	}
	return ""
}

// languageLine states the language of the CEO's last words
// (EventFacts.Language), "" when unknown. Only the fact: what to do with
// it is the model's.
func languageLine(lang string) string {
	if lang == "" {
		return ""
	}
	return "The CEO's last words were in " + lang + "."
}

// promptStateLine states an announced prompt's real state (EventFacts).
func promptStateLine(state string) string {
	switch state {
	case PromptWaiting:
		return "The agent's prompt is still open, waiting for the CEO's answer."
	case PromptAnswered:
		return "The agent's prompt has already been answered."
	case PromptGone:
		return "The agent's prompt is no longer open: the agent left it without an answer from the CEO."
	}
	return ""
}

// quotedLines renders lines as "a"; "b".
func quotedLines(lines []string) string {
	q := make([]string, len(lines))
	for i, l := range lines {
		q[i] = fmt.Sprintf("%q", l)
	}
	return strings.Join(q, "; ")
}

// factsUpdate is the window note for what changed in an announced
// event's facts since the request was rendered (before a later step of
// the same turn), "" when nothing did.
func factsUpdate(before, now EventFacts) string {
	var parts []string
	if len(now.Spoken) > len(before.Spoken) {
		parts = append(parts, "while you were working the assistant said, about this event: "+quotedLines(now.Spoken[len(before.Spoken):])+"; your spoken would follow it, not replace it")
	}
	if nb, na := heardAfter(before.Heard), heardAfter(now.Heard); len(na) > len(nb) && now.Heard != nil {
		parts = append(parts, "while you were working the voice was given, after the CEO began speaking: "+pushedLines(na[len(nb):], now.Heard.SpokeAt)+"; the CEO's words do not answer it")
	}
	if now.Language != before.Language && now.Language != "" {
		parts = append(parts, "the CEO's last words are now in "+now.Language)
	}
	if now.Prompt != before.Prompt {
		if line := promptStateLine(now.Prompt); line != "" {
			parts = append(parts, strings.TrimSuffix(lowerFirstRune(line), "."))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Update from the supervisor: " + strings.Join(parts, "; ") + "."
}

// heardAfter is h.After, nil-safe.
func heardAfter(h *HeardFacts) []PushedLine {
	if h == nil {
		return nil
	}
	return h.After
}

// lowerFirstRune lower-cases the first letter of s.
func lowerFirstRune(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// promiseLine renders a promise record for the window and recall.
func promiseLine(p PromiseInfo) string {
	return fmt.Sprintf("Promise (%s): %q, %s", statusOr(p.Status), p.Text, promiseCondition(p))
}

// investigationLine renders one job of the ledger for the prompt's
// Investigations section and for recall.
func investigationLine(inv InvestigationInfo, now time.Time) string {
	when := "just now"
	if !inv.Started.IsZero() {
		when = ago(now.Sub(inv.Started)) + " ago"
	}
	line := fmt.Sprintf("investigation %s (%s, %s): %q", inv.ID, aliasOr(inv.Agent), when, inv.Question)
	switch inv.Status {
	case InvestigationRunning:
		return line + " → still running"
	case InvestigationCancelled:
		return line + " → cancelled"
	case InvestigationError:
		return line + " → failed: " + inv.Err
	default:
		summary := inv.Summary
		if summary == "" {
			summary = "no answer"
		}
		return line + " → " + summary
	}
}

// promiseCondition is "when auth finishes" / "when auth asks something".
func promiseCondition(p PromiseInfo) string {
	alias := aliasOr(p.Agent)
	switch p.Event {
	case PromiseEventBlocked:
		return "when " + alias + " asks something"
	case PromiseEventIdle:
		return "when " + alias + " goes idle"
	case PromiseEventAnyChange:
		return "when " + alias + " changes state"
	default:
		return "when " + alias + " finishes"
	}
}

func statusOr(s string) string {
	if s == "" {
		return PromiseOpen
	}
	return s
}

// toolWorkerLog is the ToolResult name of the announce turn's "what the
// worker wrote" message: a KindEventScreen like the screen, pruned the
// same way, rendered with its own label (contextLine).
const toolWorkerLog = "worker_log"

// workerWrote returns the last message the worker wrote for a human, from
// the journal entry the supervisor appended for this announce's event
// just before it started the turn (docs/iteration-2.md WP4), or "" when
// there is none: no transcript, nothing new, or an entry older than the
// agent's current state (it belongs to an earlier event, or the agent
// has moved on). Both times come from the supervisor's clock.
func workerWrote(agent AgentInfo) string {
	if len(agent.Journal) == 0 || agent.StateSince.IsZero() {
		return ""
	}
	e := agent.Journal[len(agent.Journal)-1]
	if e.LastAssistant == "" || e.At.Before(agent.StateSince) {
		return ""
	}
	return e.LastAssistant
}

// Ledger budget of the journal: the last journalLedgerEntries entries per
// agent, none more than journalLedgerAge older than the agent's newest,
// each at most journalLedgerRunes of text, so four agents add under 800
// characters to every prompt (docs/iteration-2.md WP4, the age limit
// WP5); the full entries are one recall away. The age is measured
// against the newest entry, not the brain's clock: the entries are
// stamped by the supervisor's.
const (
	journalLedgerEntries = 3
	journalLedgerRunes   = 36
	journalLedgerAge     = 24 * time.Hour
)

// journalLedgerLines renders an agent's last journal entries for the
// ledger, one short indented line each, newest last ("" when none).
func journalLedgerLines(journal []JournalEntry, now time.Time) string {
	return journalLedgerLinesN(journal, now, journalLedgerEntries)
}

// journalLedgerLinesN is journalLedgerLines with at most n entries (the
// state block's budget, dynamicSections).
func journalLedgerLinesN(journal []JournalEntry, now time.Time, n int) string {
	var newest time.Time
	for _, e := range journal {
		if e.At.After(newest) {
			newest = e.At
		}
	}
	var recent []JournalEntry
	for _, e := range journal {
		if e.At.IsZero() || newest.Sub(e.At) <= journalLedgerAge {
			recent = append(recent, e)
		}
	}
	start := max(0, len(recent)-n)
	var b strings.Builder
	for _, e := range recent[start:] {
		text := e.LastAssistant
		if text == "" && e.Instruction != "" {
			text = "asked: " + e.Instruction
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "  - %s %s ago: %q\n", e.Event, ago(now.Sub(e.At)), clipText(oneLine(text), journalLedgerRunes))
	}
	return b.String()
}

// journalRecallLine renders one journal entry in full for recall.
func journalRecallLine(alias string, e JournalEntry) string {
	line := fmt.Sprintf("%s journal (%s, %s)", aliasOr(alias), e.At.Format("15:04"), e.Event)
	if e.LastAssistant != "" {
		line += fmt.Sprintf(": wrote %q", oneLine(e.LastAssistant))
	}
	if e.Instruction != "" {
		line += fmt.Sprintf("; instruction %q", e.Instruction)
	}
	if len(e.Files) > 0 {
		line += "; files " + strings.Join(e.Files, ", ")
	}
	if len(e.Tools) > 0 {
		names := make([]string, 0, len(e.Tools))
		for name := range e.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for i, name := range names {
			names[i] = fmt.Sprintf("%s×%d", name, e.Tools[name])
		}
		line += "; tools " + strings.Join(names, ", ")
	}
	return line
}

// oneLine joins a multi-line journal message into one line for the
// ledger and recall (the announce turn keeps the lines).
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipText cuts s to n runes at a word boundary when it can, with an
// ellipsis.
func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.") + "…"
}

// Budget of an agent record (agentRecords): the last recordEntries
// journal entries, each text at most recordRunes.
const (
	recordEntries = 3
	recordRunes   = 120
)

// agentRecords states, for each agent the CEO's words name, everything
// the daemon has on record about it — its state and since when, its last
// result, and its journal entries with their times — and says outright
// when there is no journal entry (run C, H7: asked whether bench-claude
// had pushed, Vertex said it had not, from a screen that says nothing
// about pushing). The daemon sees a worker only through its screen and
// its own session transcript, which the record says too: what is not
// here is unknown to it, as a fact. "" when no agent is named.
func agentRecords(agents []AgentInfo, words string, clock func() time.Time) string {
	var b strings.Builder
	var now time.Time
	for _, a := range agents {
		if !namesAgent(words, a.Alias) {
			continue
		}
		if b.Len() == 0 {
			now = clock() // only when a record is written: the clock ticks in fixtures
			b.WriteString("On record (from the worker's screen and its own session transcript, the daemon's only sources; what is not here is not on record, and a run_cli or investigate look can check it):")
		}
		fmt.Fprintf(&b, "\n- %s: %s", aliasOr(a.Alias), stateOr(a.State))
		if !a.StateSince.IsZero() {
			fmt.Fprintf(&b, " since %s (%s ago)", a.StateSince.Format("15:04:05"), ago(now.Sub(a.StateSince)))
		}
		if a.LastResult != "" {
			fmt.Fprintf(&b, "; last result: %q", clipText(oneLine(a.LastResult), recordRunes))
		}
		if len(a.Journal) == 0 {
			b.WriteString("; no journal entry: beyond this and its screen, nothing it did is on record.")
			continue
		}
		b.WriteString("; journal:")
		for _, e := range a.Journal[max(0, len(a.Journal)-recordEntries):] {
			fmt.Fprintf(&b, " [%s %s]", e.At.Format("15:04:05"), e.Event)
			if e.LastAssistant != "" {
				fmt.Fprintf(&b, " wrote %q", clipText(oneLine(e.LastAssistant), recordRunes))
			}
			if e.Instruction != "" {
				fmt.Fprintf(&b, " instruction %q", clipText(oneLine(e.Instruction), recordRunes))
			}
			if len(e.Files) > 0 {
				b.WriteString(" files " + strings.Join(e.Files, ", "))
			}
			if len(e.Tools) > 0 {
				names := make([]string, 0, len(e.Tools))
				for name := range e.Tools {
					names = append(names, name)
				}
				sort.Strings(names)
				for i, name := range names {
					names[i] = fmt.Sprintf("%s×%d", name, e.Tools[name])
				}
				b.WriteString(" tools " + strings.Join(names, ", "))
			}
			b.WriteString(";")
		}
	}
	return b.String()
}

// namesAgent reports whether words name alias: the alias itself, or
// with its dashes and underscores heard as spaces ("bench claude"),
// case-insensitively, as a whole word.
func namesAgent(words, alias string) bool {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return false
	}
	w := strings.ToLower(words)
	spaced := strings.NewReplacer("-", " ", "_", " ").Replace(alias)
	for _, form := range []string{alias, spaced} {
		for i := 0; ; {
			j := strings.Index(w[i:], form)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(form)
			if wordEdge(w, start-1) && wordEdge(w, end) {
				return true
			}
			i = start + 1
		}
	}
	return false
}

// wordEdge reports whether the byte at i of s is outside s or not a
// letter, digit, dash or underscore.
func wordEdge(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	return (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_'
}
