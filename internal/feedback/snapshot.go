package feedback

import (
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/audiotiming"
)

// Window is how far back a user.feedback snapshot looks.
const Window = 30 * time.Second

// Tags a mark can carry; the app's one-tap default is TagPaused.
const (
	TagPaused      = "paused"
	TagWrongAnswer = "wrong_answer"
	TagTooShort    = "too_short"
	TagTooLong     = "too_long"
	TagMisheard    = "misheard"
	TagOther       = "other"
)

// Tags lists every tag, in the app's order.
var Tags = []string{TagPaused, TagWrongAnswer, TagTooShort, TagTooLong, TagMisheard, TagOther}

// ValidTag reports whether t is one of Tags.
func ValidTag(t string) bool {
	for _, x := range Tags {
		if x == t {
			return true
		}
	}
	return false
}

// MaxNote is the longest note kept, in runes.
const MaxNote = 500

// EventType is the event a mark is logged as.
const EventType = "user.feedback"

// Payload is a user.feedback event's payload.
type Payload struct {
	ID   string `json:"id"`
	Tag  string `json:"tag"`
	Note string `json:"note,omitempty"`
	// ServerTS is when the daemon got the mark; ClientTS when the user
	// tapped (the phone's clock), and SkewMs the difference (server −
	// client: request latency plus clock offset).
	ServerTS         time.Time `json:"server_ts"`
	ClientTS         string    `json:"client_ts,omitempty"`
	SkewMs           *int64    `json:"skew_ms,omitempty"`
	ConversationTurn string    `json:"conversation_turn,omitempty"`
	Snapshot         Snapshot  `json:"snapshot"`
}

// Snapshot is what the daemon knew about the last Window when the mark
// came in.
type Snapshot struct {
	WindowS  int    `json:"window_s"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Runs are the voice's runs of output audio with a chunk in the
	// window, as the web layer received and sent them.
	Runs []Run `json:"audio_runs"`
	// Events are the relevant log events in the window (transcripts,
	// playback reports, drains, client stats, pushes, tool calls, the
	// orchestrator's turns), payload strings shortened.
	Events []Event `json:"events"`
	// BrainInFlight are the orchestrator turns started and not finished.
	BrainInFlight []InFlight `json:"brain_in_flight,omitempty"`
}

// Run is one run of the voice's output audio.
type Run struct {
	Run   int       `json:"run"`
	Start time.Time `json:"start"`
	// Timing is the run's voice.audio_timing shape, measured at the web
	// layer (arrival from the voice session).
	Timing map[string]any `json:"timing"`
	// MaxSendWaitMs is the longest a chunk waited between arriving and
	// being written to the client.
	MaxSendWaitMs float64 `json:"max_send_wait_ms"`
	// Unsent counts chunks never written (no client, dropped, queued).
	Unsent int `json:"unsent,omitempty"`
	// Chunks are the per-chunk times: [arrival ms since Start, audio ms,
	// written ms since Start or -1].
	Chunks [][3]float64 `json:"chunks"`
}

// Event is one log event, compacted.
type Event struct {
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// InFlight is an orchestrator turn that started and has not finished.
type InFlight struct {
	TurnID  string    `json:"turn_id"`
	Kind    string    `json:"kind,omitempty"`
	Trigger string    `json:"trigger,omitempty"`
	Started time.Time `json:"started"`
	AgeMs   int64     `json:"age_ms"`
}

// eventTypes are the log events a snapshot keeps.
var eventTypes = map[string]bool{
	"voice.transcript.in": true, "voice.transcript.out": true,
	"voice.playback": true, "voice.audio_timing": true,
	"voice.push": true, "voice.push_held": true, "voice.push_released": true,
	"voice.push_deferred": true, "voice.push_dropped": true, "voice.push_unheard": true,
	"voice.briefing_unvoiced": true,
	"voice.tool_call":         true, "voice.tool_result": true,
	"voice.interrupted": true, "voice.stalled": true, "voice.session_error": true,
	"voice.connect": true, "voice.disconnected": true, "voice.drop": true,
	"voice.resume": true, "voice.recycle": true, "voice.muted": true,
	"voice.provider_changed": true,
	"web.client.drain":       true, "web.client.flush": true, "web.client.stats": true,
	"web.client.connect": true, "web.client.disconnect": true,
	"web.client.backpressure": true, "web.client.hello": true,
	"brain.turn_started": true, "brain.turn": true,
	"turn.latency": true, "supervisor.relay": true,
}

// maxString shortens long payload strings in a snapshot.
const maxString = 200

// maxChunksPerRun bounds a run's per-chunk list.
const maxChunksPerRun = 800

// Build assembles the snapshot at now from the audio log's chunks and the
// event log's in-memory tail (eventlog.Log.Tail).
func Build(now time.Time, provider, model string, chunks []Chunk, events []eventlog.Event) Snapshot {
	from := now.Add(-Window)
	s := Snapshot{WindowS: int(Window / time.Second), Provider: provider, Model: model, Runs: runs(chunks), Events: []Event{}}
	if s.Runs == nil {
		s.Runs = []Run{}
	}
	started := map[string]eventlog.Event{}
	var order []string
	for _, ev := range events {
		switch ev.Type {
		case "brain.turn_started":
			if id := turnID(ev); id != "" {
				if _, seen := started[id]; !seen {
					order = append(order, id)
				}
				started[id] = ev
			}
		case "brain.turn":
			delete(started, turnID(ev))
		}
		if ev.TS.Before(from) || ev.TS.After(now) || !eventTypes[ev.Type] {
			continue
		}
		s.Events = append(s.Events, Event{TS: ev.TS, Type: ev.Type, Payload: shorten(ev.Payload)})
	}
	for _, id := range order {
		ev, ok := started[id]
		if !ok {
			continue
		}
		var p struct {
			Kind    string `json:"kind"`
			Trigger string `json:"trigger"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		s.BrainInFlight = append(s.BrainInFlight, InFlight{TurnID: id, Kind: p.Kind, Trigger: p.Trigger, Started: ev.TS, AgeMs: now.Sub(ev.TS).Milliseconds()})
	}
	return s
}

func turnID(ev eventlog.Event) string {
	if ev.TurnID != "" {
		return ev.TurnID
	}
	var p struct {
		TurnID string `json:"turn_id"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	return p.TurnID
}

func runs(chunks []Chunk) []Run {
	var out []Run
	var tr *audiotiming.Tracker
	flush := func() {
		if tr != nil && len(out) > 0 {
			out[len(out)-1].Timing = tr.Finish()
		}
	}
	for _, c := range chunks {
		if len(out) == 0 || out[len(out)-1].Run != c.Run {
			flush()
			out = append(out, Run{Run: c.Run, Start: c.At, Chunks: [][3]float64{}})
			tr = audiotiming.New(c.Run, 24000)
		}
		r := &out[len(out)-1]
		tr.Chunk(c.At, c.Bytes)
		sent := -1.0
		if c.SentAt.IsZero() {
			r.Unsent++
		} else {
			sent = round1(ms(c.SentAt.Sub(r.Start)))
			if w := ms(c.SentAt.Sub(c.At)); w > r.MaxSendWaitMs {
				r.MaxSendWaitMs = round1(w)
			}
		}
		if len(r.Chunks) < maxChunksPerRun {
			r.Chunks = append(r.Chunks, [3]float64{round1(ms(c.At.Sub(r.Start))), round1(c.AudioMs()), sent})
		}
	}
	flush()
	return out
}

// shorten rewrites a payload with long strings cut to maxString runes.
func shorten(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(shortenValue(v))
	if err != nil {
		return raw
	}
	return b
}

func shortenValue(v any) any {
	switch x := v.(type) {
	case string:
		if utf8.RuneCountInString(x) > maxString {
			return string([]rune(x)[:maxString]) + "…"
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = shortenValue(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = shortenValue(e)
		}
		return x
	}
	return v
}

// TruncateNote trims a note to MaxNote runes.
func TruncateNote(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxNote {
		s = string([]rune(s)[:MaxNote])
	}
	return s
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
