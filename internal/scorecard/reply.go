package scorecard

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// replyMatchWindow bounds the order fallback of computeReplyLatency: a
// reply push is matched to a pending call at most this long after it.
const replyMatchWindow = 3 * time.Minute

type replyToolResultPayload struct {
	ID      string `json:"id"`
	Pending bool   `json:"pending"`
	Spoken  string `json:"spoken"`
}

type relayPayload struct {
	CallID string `json:"call_id"`
}

type replyPendingPayload struct {
	ID     string `json:"id"`
	CallID string `json:"call_id"`
}

type replyPushPayload struct {
	AnswersUtterance string `json:"answers_utterance"`
	CallID           string `json:"call_id"`
}

// computeReplyLatency is, per voice.tool_call(to_supervisor), the time
// until the reply was handed to the voice: its voice.tool_result when
// that carried the answer, else — the call returned a pending result —
// the voice.push that delivered the answer later. AnswerLatencyMS stops
// at the pending result (~reply_wait_s), so it hides every slow reply;
// this does not.
//
// The push is found by the utterance id (eventlog.Event.UtteranceID:
// supervisor.relay {call_id} names the utterance, the push carries it);
// a log from before the ids falls back to the links that were there
// (brain.reply_pending {id, call_id}, the push's answers_utterance and
// call_id tags), and a reply push whose utterance is still unknown is
// matched by order to the oldest pending call within replyMatchWindow.
func computeReplyLatency(events []eventlog.Event) Stats {
	type call struct {
		at   time.Time
		done bool
	}
	calls := map[string]*call{} // by voice tool call id
	byUtt := map[string]*call{} // by utterance id
	var pending []*call         // pending calls, in order
	var ms []float64
	answer := func(c *call, at time.Time) {
		if c == nil || c.done {
			return
		}
		c.done = true
		ms = append(ms, at.Sub(c.at).Seconds()*1000)
	}
	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.tool_call":
			p := decode[toolCallPayload](ev.Payload)
			if p.Name == "to_supervisor" && p.ID != "" {
				calls[p.ID] = &call{at: ev.TS}
			}
		case ev.Type == "supervisor.relay":
			if c := calls[decode[relayPayload](ev.Payload).CallID]; c != nil && ev.UtteranceID != "" {
				byUtt[ev.UtteranceID] = c
			}
		case ev.Type == "brain.reply_pending":
			p := decode[replyPendingPayload](ev.Payload)
			if c := calls[p.CallID]; c != nil && p.ID != "" && byUtt[p.ID] == nil {
				byUtt[p.ID] = c
			}
		case ev.Source == "voice" && ev.Type == "voice.tool_result":
			p := decode[replyToolResultPayload](ev.Payload)
			c := calls[p.ID]
			switch {
			case c == nil:
			case p.Pending:
				pending = append(pending, c)
			case p.Spoken != "":
				answer(c, ev.TS)
			default:
				c.done = true // answered with silence: nothing to time
			}
		case ev.Source == "voice" && ev.Type == "voice.push":
			p := decode[replyPushPayload](ev.Payload)
			utt := ev.UtteranceID
			if utt == "" {
				utt = p.AnswersUtterance
			}
			if utt == "" {
				continue // not a reply to an utterance
			}
			c := byUtt[utt]
			if c == nil {
				c = calls[p.CallID]
			}
			if c == nil {
				for _, q := range pending {
					if !q.done && ev.TS.Sub(q.at) <= replyMatchWindow {
						c = q
						break
					}
				}
			}
			answer(c, ev.TS)
		}
	}
	return computeStats(ms)
}
