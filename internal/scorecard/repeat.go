package scorecard

import (
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/earmetrics"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// repeatWindow is how close together two utterances have to be to count
// as the user re-asking the same thing (docs/iteration-2.md
// "repeat-ask rate (same intent re-asked within 60 s)").
const repeatWindow = 60 * time.Second

// repeatOverlapThreshold is the shared-word fraction (of the smaller
// utterance's distinct words) above which two consecutive utterances are
// treated as the same ask restated.
const repeatOverlapThreshold = 0.6

// RepeatAskReport is docs/iteration-2.md's repeat-ask rate.
type RepeatAskReport struct {
	Total   int // utterances considered
	Repeats int // consecutive pairs judged a re-ask
	Rate    float64
}

var repeatCueWords = []string{"again", "repeat", "say again", "i mean"}

// computeRepeatAsk implements the repeat-ask-rate section. The utterance
// stream is every voice.tool_call(to_supervisor) text and every final
// voice.transcript.in text, merged in time order (real logs carry almost
// none of the latter — "final" is essentially never true in practice, see
// docs/history/scorecard-baseline-2026-09-24.md — so this is a fallback, not the
// primary source).
func computeRepeatAsk(events []eventlog.Event) RepeatAskReport {
	utterances := utteranceTexts(events)
	if len(utterances) == 0 {
		return RepeatAskReport{}
	}
	r := RepeatAskReport{Total: len(utterances)}
	for i := 1; i < len(utterances); i++ {
		prev, cur := utterances[i-1], utterances[i]
		if cur.ts.Sub(prev.ts) > repeatWindow {
			continue
		}
		if isRepeat(prev.text, cur.text) {
			r.Repeats++
		}
	}
	if r.Total > 0 {
		r.Rate = float64(r.Repeats) / float64(r.Total)
	}
	return r
}

func isRepeat(prev, cur string) bool {
	if earmetrics.WordOverlap(prev, cur) >= repeatOverlapThreshold {
		return true
	}
	lower := strings.ToLower(cur)
	for _, cue := range repeatCueWords {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

type timedText struct {
	ts   time.Time
	text string
}

// utteranceTexts returns, in time order, every voice.tool_call
// (to_supervisor) text and every final voice.transcript.in text.
func utteranceTexts(events []eventlog.Event) []timedText {
	var out []timedText
	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.tool_call":
			p := decode[toolCallPayload](ev.Payload)
			if p.Name == "to_supervisor" {
				if text := p.text(); text != "" {
					out = append(out, timedText{ev.TS, text})
				}
			}
		case ev.Source == "voice" && ev.Type == "voice.transcript.in":
			p := decode[transcriptPayload](ev.Payload)
			if p.Final && p.Text != "" {
				out = append(out, timedText{ev.TS, p.Text})
			}
		}
	}
	return out
}
