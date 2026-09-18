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

// eventRequestText is the request message of an event turn (parsed by
// braintest's reflex model and web/debug/supervisor.js; keep the shape).
func eventRequestText(req Request) string {
	agent := aliasOr(req.Agent)
	event := req.Event
	if event == "" {
		event = "changed state"
	}
	switch req.Kind {
	case KindKeepPromise:
		text, cond := "", ""
		if req.Promise != nil {
			text, cond = req.Promise.Text, promiseCondition(*req.Promise)
		}
		return fmt.Sprintf("Event from the supervisor (keep_promise): agent=%q event=%q template=%q promise=%q condition=%q\n"+
			"The CEO asked to be told this, and now it has happened; the sentence in template is what the announcement said (it may not have been spoken at the current chattiness level). "+
			"Reply with the JSON object of the answer contract: spoken is one line that keeps the promise, saying what the CEO asked to be told together with the result, in the CEO's terms; needs_user true when they must act on it. No tools are needed unless the result is unclear.", agent, event, req.Template, text, cond)
	default:
		return fmt.Sprintf("Event from the supervisor (summarize): agent=%q event=%q template=%q\n"+
			"The assistant already said the sentence in template. Add only what it lacks and the CEO would want to hear: which request of theirs this fulfils (relates_to), what changed against what they asked, what is still open, whether they must act now. "+
			"Reply with the JSON object of the answer contract, spoken at most two short sentences that do not repeat the template; set spoken to \"\" when the template was sufficient, and relates_to to the CEO's request it fulfils when there is one.", agent, event, req.Template)
	}
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
