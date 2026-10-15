package scorecard

import (
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// stopWindow is how close a voice.tool_call(stop) has to precede a
// Ctrl-C send to count as "the stop word caused this", per J6 in
// docs/dev/iteration-2.md ("never Ctrl-C on 'never mind'").
const stopWindow = 5 * time.Second

// CtrlCSend is one Ctrl-C keystroke the daemon sent to a pane.
type CtrlCSend struct {
	TS             time.Time
	PaneID         string
	PrecededByStop bool
	// TriggerText is the nearest preceding to_supervisor relay or
	// transcript fragment: what the user was saying when this
	// happened.
	TriggerText string
}

// CtrlCReport is docs/dev/iteration-2.md's "Ctrl-C sends without a stop word".
type CtrlCReport struct {
	Sends               []CtrlCSend
	Total               int
	PrecededByStopCount int
}

type herdrSendPayload struct {
	Keys []string `json:"keys"`
	Kind string   `json:"kind"`
}

// computeCtrlC implements the Ctrl-C section.
func computeCtrlC(events []eventlog.Event) CtrlCReport {
	var r CtrlCReport
	var lastUtteranceText string
	var lastUtteranceTS time.Time
	var lastStopTS time.Time
	haveStop := false

	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.tool_call":
			p := decode[toolCallPayload](ev.Payload)
			switch p.Name {
			case "to_supervisor":
				if text := p.text(); text != "" && ev.TS.After(lastUtteranceTS) {
					lastUtteranceText, lastUtteranceTS = text, ev.TS
				}
			case "stop":
				lastStopTS, haveStop = ev.TS, true
			}
		case ev.Source == "voice" && ev.Type == "voice.transcript.in":
			p := decode[transcriptPayload](ev.Payload)
			if p.Text != "" && ev.TS.After(lastUtteranceTS) {
				lastUtteranceText, lastUtteranceTS = p.Text, ev.TS
			}
		case ev.Source == "herdr" && ev.Type == "herdr.send":
			p := decode[herdrSendPayload](ev.Payload)
			if !hasCtrlC(p.Keys) {
				continue
			}
			preceded := haveStop && !lastStopTS.After(ev.TS) && ev.TS.Sub(lastStopTS) <= stopWindow
			r.Sends = append(r.Sends, CtrlCSend{
				TS:             ev.TS,
				PaneID:         ev.PaneID,
				PrecededByStop: preceded,
				TriggerText:    lastUtteranceText,
			})
		}
	}
	r.Total = len(r.Sends)
	for _, s := range r.Sends {
		if s.PrecededByStop {
			r.PrecededByStopCount++
		}
	}
	return r
}

// hasCtrlC reports whether keys contains a ctrl+c chord, matched loosely
// (case-insensitive substring) since key-name spelling is not part of any
// documented contract.
func hasCtrlC(keys []string) bool {
	for _, k := range keys {
		if strings.Contains(strings.ToLower(k), "ctrl+c") {
			return true
		}
	}
	return false
}
