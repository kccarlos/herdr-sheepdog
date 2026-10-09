package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// The action list the segment judge's said_vs_done item checks claims
// against (docs/eval/analysis-done-2026-10-08.md, F1): what sheepdog
// sent to workers (prompts, keys with the pending answer they gave,
// interrupts), what it started, closed and renamed, what a rail refused,
// which tools failed or timed out, and the checks it ran (run_cli,
// read_screen, investigate), each with its outcome. Measurement only:
// the list is rendered for the judge, never read by the product.

// CallAction is one entry of the list.
type CallAction struct {
	AtMS int64
	// Text says what happened, with the worker and the outcome.
	Text string
	// Read marks a screen read (listed only inside the segment: there
	// are hundreds per call).
	Read bool
}

// CallActions builds a call's action list from the record's actions
// (what was sent) and, when given, its event log (outcomes, pending
// answers, checks, spawns, closes, renames, rail refusals, tool
// timeouts and failures).
func CallActions(acts []SimLine, evs []northstar.Event) []CallAction {
	var nas []northstar.Action
	nas = append(nas, RecordActions(acts)...)
	var out []CallAction
	if len(evs) > 0 {
		f := northstar.ReadFacts(evs)
		for _, a := range f.Sends {
			// The record lists what went; the log adds what failed (or
			// everything, for a record without actions).
			if len(acts) == 0 || a.Outcome != northstar.OutcomeOK {
				nas = append(nas, a)
			}
		}
		nas = append(nas, f.Actions...)
		northstar.AttachAnswers(nas, f.Answers)
		alias := f.Aliases
		calls := map[string]string{}
		for _, e := range evs {
			p := e.Payload
			switch e.Type {
			case "brain.tool_call":
				calls[evStr(p, "id")] = evStr(p, "name")
			case "brain.rail_blocked":
				what := "an action"
				if k := evStrs(p, "keys"); len(k) > 0 {
					what = "keys " + strings.Join(k, " ")
				} else if n := calls[evStr(p, "id")]; n != "" {
					what = n
				}
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("tried %s on %s; REFUSED by a rail (%s), nothing sent", what, orDefault(evStr(p, "agent"), "a worker"), evStr(p, "reason"))})
			case "brain.tool_timeout":
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("tool %s on %s TIMED OUT", evStr(p, "name"), orDefault(evStr(p, "agent"), "-"))})
			case "brain.tool_result":
				name := orDefault(evStr(p, "name"), calls[evStr(p, "id")])
				switch name {
				case "prompt", "send_keys", "interrupt", "spawn", "close", "rename":
					if res := evStr(p, "result"); failedText(res) {
						out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("tool %s FAILED: %s", name, clipText(res, 120))})
					}
				}
			case "note.created":
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("wrote itself a note (kept in its own record, nothing sent): %q", clipText(evStr(p, "text"), 140))})
			case "promise.created":
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("recorded a promise to the CEO (nothing sent): %q", clipText(orDefault(evStr(p, "text"), evStr(p, "what")), 140))})
			case "supervisor.rename":
				who := alias[e.Pane]
				if who == "" {
					who = e.Pane
				}
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("renamed %s to %q", who, evStr(p, "name"))})
			case "supervisor.rename_error":
				out = append(out, CallAction{AtMS: e.AtMS, Text: fmt.Sprintf("rename to %q: herdr refused (%s)", evStr(p, "name"), evStr(p, "error"))})
			}
		}
	}
	for _, a := range nas {
		out = append(out, CallAction{AtMS: a.AtMS, Text: describeAction(a), Read: a.Tool == "read_screen"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtMS < out[j].AtMS })
	return out
}

// describeAction renders one action for the judge.
func describeAction(a northstar.Action) string {
	var s string
	switch a.Kind {
	case northstar.ActPrompt:
		s = fmt.Sprintf("sent %s the prompt %q", a.Agent, clipText(a.Detail, 160))
	case northstar.ActKeys:
		s = fmt.Sprintf("answered %s's prompt with keys %s", a.Agent, a.Detail)
		if a.Answer != "" {
			s += " (answer: " + a.Answer + ")"
		}
	case northstar.ActInterrupt:
		s = fmt.Sprintf("interrupted %s (%s)", a.Agent, a.Detail)
	case northstar.ActSpawn:
		s = fmt.Sprintf("started a new worker %s (%s)", a.Agent, a.Detail)
	case northstar.ActClose:
		s = fmt.Sprintf("closed %s", a.Agent)
	case northstar.ActCheck, northstar.ActCheckStarted:
		switch a.Tool {
		case "read_screen":
			s = fmt.Sprintf("read %s's screen", orDefault(a.Agent, "a worker"))
		case "investigate":
			if a.Kind == northstar.ActCheckStarted {
				s = fmt.Sprintf("started an investigation of %s", orDefault(a.Agent, "a worker"))
			} else {
				s = fmt.Sprintf("investigation of %s returned", orDefault(a.Agent, "a worker"))
			}
		default:
			s = fmt.Sprintf("ran a check (%s) on %s", a.Tool, orDefault(a.Agent, "-"))
		}
	default:
		s = string(a.Kind) + " " + a.Agent
	}
	switch a.Outcome {
	case northstar.OutcomeOK, "":
		s += ": ok"
	case northstar.OutcomeTimeout:
		s += ": TIMED OUT"
	default:
		s += ": " + strings.ToUpper(a.Outcome)
		if a.Detail != "" && a.Kind != northstar.ActPrompt && a.Kind != northstar.ActKeys {
			s += " (" + clipText(a.Detail, 80) + ")"
		}
	}
	return s
}

// RenderSegmentActions renders the list for one segment: the actions in
// its span (from start up to end, plus the claim slack after it; end < 0
// is the end of the call), then the earlier ones without screen reads.
func RenderSegmentActions(acts []CallAction, start, end int64) string {
	var in, before []string
	for _, a := range acts {
		line := fmt.Sprintf("- [t+%.1fs] %s", float64(a.AtMS)/1000, a.Text)
		switch {
		case a.AtMS >= start && (end < 0 || a.AtMS < end+northstar.ClaimSlackMS):
			in = append(in, line)
		case a.AtMS < start && !a.Read:
			before = append(before, line)
		}
	}
	var b strings.Builder
	b.WriteString("In this segment's span:\n")
	if len(in) == 0 {
		b.WriteString("- none: sheepdog sent, tried and checked nothing in this span\n")
	}
	for _, l := range in {
		b.WriteString(l + "\n")
	}
	if len(before) > 0 {
		b.WriteString("Earlier in the call (screen reads left out):\n")
		for _, l := range before {
			b.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func evStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func evStrs(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// failedText reports whether a tool result says the tool failed.
func failedText(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"error", "failed", "timed out", "timeout", "could not", "couldn't"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return strings.Contains(low, "error:")
}

func clipText(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
