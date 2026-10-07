package scorecard

import (
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// LatencyReport is the "Latency" section of docs/iteration-2.md's
// scorecard: how long the user waits between speaking and hearing an
// answer (J1: "first word ≤ 2 s, answer ≤ 5 s"), and how the orchestrator itself
// spends its time.
type LatencyReport struct {
	// AnswerLatencyMS is, per voice.tool_call(to_supervisor), the time to
	// its matching voice.tool_result.
	AnswerLatencyMS Stats
	// ReplyLatencyMS is, per voice.tool_call(to_supervisor), the time
	// until its reply reached the voice: the tool result when it carried
	// the answer, else the late push (computeReplyLatency).
	ReplyLatencyMS Stats
	// TimeToFirstWordMS is, per voice.tool_call, the time from the last
	// voice.transcript.in before it to the first voice.transcript.out
	// after it.
	TimeToFirstWordMS Stats
	// BrainTurnDurByKind is brain.turn's dur_ms distribution, keyed by
	// payload.kind (utterance|stop|announce|investigation).
	BrainTurnDurByKind map[string]Stats
	// BrainStepPromptTokens is brain.step's prompt_tokens distribution
	// across every model call.
	BrainStepPromptTokens Stats
}

type toolCallPayload struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

func (p toolCallPayload) text() string {
	if p.Args == nil {
		return ""
	}
	s, _ := p.Args["text"].(string)
	return s
}

type toolResultPayload struct {
	ID string `json:"id"`
}

type transcriptPayload struct {
	Final bool   `json:"final"`
	Text  string `json:"text"`
}

type brainTurnPayload struct {
	Kind string `json:"kind"`
}

type brainStepPayload struct {
	PromptTokens int `json:"prompt_tokens"`
}

// computeLatency implements the Latency section over one ordered event
// slice (a session, or the whole loaded range). It matches a
// voice.tool_call to its voice.tool_result by payload.id when both carry
// one; when an id is missing (an older log), it falls back to pairing
// calls and results in the order they occur, which is exact as long as
// calls and results interleave one-for-one, the normal case.
func computeLatency(events []eventlog.Event) LatencyReport {
	resultByID := map[string]eventlog.Event{}
	var unmatchedResults []eventlog.Event
	var outs []eventlog.Event // voice.transcript.out, in order
	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.tool_result":
			if p := decode[toolResultPayload](ev.Payload); p.ID != "" {
				resultByID[p.ID] = ev
			} else {
				unmatchedResults = append(unmatchedResults, ev)
			}
		case ev.Source == "voice" && ev.Type == "voice.transcript.out":
			outs = append(outs, ev)
		}
	}

	var answerMS, firstWordMS, promptTokens []float64
	turnDurByKind := map[string][]float64{}
	var lastIn *eventlog.Event
	unmatchedIdx, outIdx := 0, 0

	for i := range events {
		ev := events[i]
		switch {
		case ev.Source == "voice" && ev.Type == "voice.transcript.in":
			lastIn = &events[i]

		case ev.Source == "voice" && ev.Type == "voice.tool_call":
			p := decode[toolCallPayload](ev.Payload)
			if p.Name != "to_supervisor" {
				continue
			}
			if res, ok := resultByID[p.ID]; ok && p.ID != "" {
				answerMS = append(answerMS, msBetween(ev, res))
			} else if unmatchedIdx < len(unmatchedResults) {
				res := unmatchedResults[unmatchedIdx]
				unmatchedIdx++
				if res.TS.After(ev.TS) {
					answerMS = append(answerMS, msBetween(ev, res))
				}
			}
			if lastIn != nil {
				for outIdx < len(outs) && outs[outIdx].TS.Before(ev.TS) {
					outIdx++
				}
				if outIdx < len(outs) {
					firstWordMS = append(firstWordMS, msBetween(*lastIn, outs[outIdx]))
				}
			}

		case ev.Source == "brain" && ev.Type == "brain.turn":
			if ev.DurMS != nil {
				kind := decode[brainTurnPayload](ev.Payload).Kind
				turnDurByKind[kind] = append(turnDurByKind[kind], float64(*ev.DurMS))
			}

		case ev.Source == "brain" && ev.Type == "brain.step":
			if p := decode[brainStepPayload](ev.Payload); p.PromptTokens > 0 {
				promptTokens = append(promptTokens, float64(p.PromptTokens))
			}
		}
	}

	byKind := make(map[string]Stats, len(turnDurByKind))
	for k, v := range turnDurByKind {
		byKind[k] = computeStats(v)
	}

	return LatencyReport{
		AnswerLatencyMS:       computeStats(answerMS),
		ReplyLatencyMS:        computeReplyLatency(events),
		TimeToFirstWordMS:     computeStats(firstWordMS),
		BrainTurnDurByKind:    byKind,
		BrainStepPromptTokens: computeStats(promptTokens),
	}
}

// msBetween returns b.TS - a.TS in milliseconds.
func msBetween(a, b eventlog.Event) float64 {
	return b.TS.Sub(a.TS).Seconds() * 1000
}
