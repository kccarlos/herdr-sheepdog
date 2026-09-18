package brain

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// toContents renders the window as model contents. Context messages
// (utterances, replies of the assistant, pushes, worker events, notes,
// requests, summaries) become user text lines; consecutive ones merge into
// one content. A step's tool calls become the model content the provider
// returned (Raw, with its thought signatures) or, without Raw, function
// call parts; their results become function response parts. Replies are
// model text.
func toContents(msgs []Message) []*genai.Content {
	var out []*genai.Content
	var text []string // pending user lines
	flushText := func() {
		if len(text) == 0 {
			return
		}
		out = append(out, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: strings.Join(text, "\n")}}})
		text = nil
	}
	var calls []*genai.Part // pending model function-call parts (no Raw)
	flushCalls := func() {
		if len(calls) == 0 {
			return
		}
		out = append(out, &genai.Content{Role: genai.RoleModel, Parts: calls})
		calls = nil
	}
	var results []*genai.Part // pending function response parts
	flushResults := func() {
		if len(results) == 0 {
			return
		}
		out = append(out, &genai.Content{Role: genai.RoleUser, Parts: results})
		results = nil
	}
	flushAll := func() { flushText(); flushCalls(); flushResults() }

	for _, m := range msgs {
		switch m.Kind {
		case KindToolCall:
			flushText()
			flushResults()
			if len(m.Raw) > 0 {
				flushCalls()
				var c genai.Content
				if err := json.Unmarshal(m.Raw, &c); err == nil && len(c.Parts) > 0 {
					c.Role = genai.RoleModel
					out = append(out, &c)
					continue
				}
			}
			if m.ToolCall != nil {
				calls = append(calls, &genai.Part{FunctionCall: &genai.FunctionCall{Name: m.ToolCall.Name, Args: m.ToolCall.Args}})
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
		case KindReply:
			flushAll()
			out = append(out, &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: m.Content}}})
		default:
			flushCalls()
			flushResults()
			text = append(text, contextLine(m))
		}
	}
	flushAll()
	return out
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
	default:
		return fmt.Sprintf("[%s] %s: %s", ts, m.Kind, m.Content)
	}
}

// requestText is the request message content for kind and req.
func requestText(req Request) string {
	if req.Kind.IsEvent() {
		return eventRequestText(req)
	}
	what := "question"
	if req.Kind == KindPropose {
		what = "instruction"
	}
	s := fmt.Sprintf("Request from the assistant (%s): %s=%q", req.Kind, what, req.Question)
	if req.Agent != "" {
		s += fmt.Sprintf(" agent=%q", req.Agent)
	} else {
		s += " agent=(none: the CEO did not say a name)"
	}
	if req.Trigger == TriggerEvent {
		s += " [triggered by an event, not by the CEO]"
	}
	if req.Question == "" && req.Kind == KindPropose {
		s += "\nThe assistant passed no instruction text: the CEO's own words above are the instruction (a bare \"let me know when …\" is a promise to record)."
	}
	return s + "\nUse tools as needed, then reply with the JSON object of the answer contract."
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
	if req.TemplateSpoken {
		b.WriteString("The assistant already said the sentence in template (a question is never delayed). Decide whether to add one line and how urgently: add only what it lacks and the CEO would want now — the decision named in context, which request of theirs this is about, what they said about it — and skip when the sentence was enough or they already know.")
	} else {
		b.WriteString("Nothing has been said about this yet. Decide whether the assistant says anything, what, and how urgently: your spoken replaces the template sentence, so carry its news (what the agent did, in the CEO's terms) plus what the sentence lacked — which request of theirs it fulfils, what changed against what they asked, what is still open. Skip when the CEO already knows (the assistant relayed it, or they just asked and heard it) or it is not worth their attention; silent when it is context the assistant should have but not say.")
	}
	b.WriteString(" Reply with the JSON object of the answer contract plus \"urgency\": one of interrupt (the CEO must act now), idle (at the next pause), silent (context only, not spoken) or skip (say nothing); \"keeps_promise_ids\" for the promise ids above your line keeps; \"relates_to\" for the CEO's request it fulfils; needs_user true when they must decide or act. One line, at most two short sentences. The screen the supervisor read is above; read tools only.")
	return b.String()
}

// promiseLine renders a promise record for the window and recall.
func promiseLine(p PromiseInfo) string {
	return fmt.Sprintf("Promise (%s): %q, %s", statusOr(p.Status), p.Text, promiseCondition(p))
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
