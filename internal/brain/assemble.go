package brain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// toContents renders the window as Gemini contents for provider/model.
// Context messages (utterances, replies of the assistant, pushes, worker
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

	for mi, m := range msgs {
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
		return fmt.Sprintf("[%s] The screen the supervisor read for this event:\n%s", ts, m.Content)
	case KindSystem:
		return fmt.Sprintf("[%s] Note from the daemon: %s", ts, m.Content)
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
	case req.Kind == KindStop:
		return stopRequestText(req)
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
	if req.RosterUnchangedFor > 0 {
		s += fmt.Sprintf("\nThe roster is unchanged since you last reported it %d s ago; if the shepherd is only asking for status again, say there has been no change since you last checked, briefly.", int(req.RosterUnchangedFor.Seconds()))
	}
	return s + "\nThose are the CEO's own words, relayed verbatim: decide what they are (a status question, something to find out, an instruction, an answer to a worker's prompt, a promise, a note, small talk) and act. Use tools as needed, then reply with the JSON object of the answer contract."
}

// stopRequestText is the request of the stop procedure's closing turn:
// the code already resolved and carried out what to stop, so the turn
// only says so and offers the correction (read tools only).
func stopRequestText(req Request) string {
	stopped := req.Stopped
	if stopped == "" {
		stopped = "nothing was in flight"
	}
	s := fmt.Sprintf("Request from the assistant (stop): stopped=%q", stopped)
	if req.Agent != "" {
		s += fmt.Sprintf(" agent=%q", req.Agent)
	} else {
		s += " agent=(none)"
	}
	return s + "\nThe CEO said stop and the daemon already carried the stop out; an open worker prompt is deliberately left open. Say in one short sentence what was stopped, then offer the correction as the ask (\"Shall I tell it something else?\"). Read tools only: send nothing."
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
	b.WriteString("Nothing has been said about this yet. Decide whether the assistant says anything, what, and how urgently: your spoken replaces the template sentence, so carry its news (what the agent did, in the CEO's terms) plus what the sentence lacked — which request of theirs it fulfils, what changed against what they asked, what is still open. Skip when the CEO already knows (the assistant relayed it, or they just asked and heard it) or it is not worth their attention; silent when it is context the assistant should have but not say.")
	b.WriteString(" Reply with the JSON object of the answer contract plus \"urgency\": one of interrupt (the CEO must act now), idle (at the next pause), silent (context only, not spoken) or skip (say nothing); \"keeps_promise_ids\" for the promise ids above your line keeps; \"relates_to\" for the CEO's request it fulfils; needs_user true when they must decide or act. One line for the ear: spoken at most two short sentences, the one question in ask. The screen the supervisor read is above; read tools only.")
	return b.String()
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
