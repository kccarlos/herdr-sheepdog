package observe

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// The latency breakdown of one user utterance (Pipecat's
// LatencyBreakdown; docs/architecture.md "turn.latency"). The wait from
// the user's last word to the first audio of the answer is cut into
// consecutive segments, each measured from the events that bound it:
//
//	voice_ms        last words → the voice's to_supervisor call (voice.tool_call)
//	relay_ms        tool call → supervisor.relay (the daemon's own relay: the
//	                unanswered wait, relay_by_daemon waited_ms)
//	queue_ms        relay → brain.turn_started (fast paths, the turn lock)
//	brain_ms        turn started → brain.turn; within it steps_ms (each
//	                brain.step), model_ms (their sum) and tool_ms (brain.tool_result)
//	delivery_ms     brain.turn → the reply handed over (voice.tool_result
//	                carrying it, or brain.reply_delivered queueing the push)
//	push_queue_ms   push queued → sent to the voice model (voice.push)
//	first_audio_ms  handed to the voice → the client started playing
//	                (voice.playback {state: started})
//
// total_ms is last words → first audio. Segments that do not apply (no
// orchestrator turn for a fast path, no push for an inline reply) are left out.
// Events are matched by utterance_id and turn_id; a log from before the
// ids falls back to the call id (voice.tool_call ↔ supervisor.relay ↔
// voice.tool_result / brain.reply_pending), the orchestrator turn number, and
// time (the first utterance step after the relay). The voice session
// tags the playback start that follows a reply with its utterance_id; an
// untagged start (an older log) goes to the reply waiting longest.

// Latency timeouts.
const (
	// audioWait is how long a reply handed to the voice may go without a
	// playback start before the record ends as no_audio.
	audioWait = 30 * time.Second
	// recordTTL bounds a record (a push may be held five minutes for a
	// listener, voice.push_held): past it, it ends as timeout.
	recordTTL = 10 * time.Minute
	// callTTL bounds a voice tool call waiting for its relay.
	callTTL = time.Minute
	// legacyEndWindow: with no waited_ms on the relay (older logs), the
	// last voice.transcript.in counts as the end of the words only when
	// it is this close before the tool call.
	legacyEndWindow = 30 * time.Second
)

// Outcomes of a breakdown.
const (
	OutcomePlayed    = "played"    // the reply started playing
	OutcomeNoAudio   = "no_audio"  // handed over, never heard playing (audioWait)
	OutcomeNoVoice   = "no_voice"  // pushed with --voice=off: nothing plays
	OutcomeSilent    = "silent"    // answered with silence (a silent result or push)
	OutcomeDropped   = "dropped"   // the push was dropped (voice.push_dropped)
	OutcomeCoalesced = "coalesced" // merged into the next utterance
	OutcomeTimeout   = "timeout"   // no reply within recordTTL (or the log ended)
	OutcomeRestart   = "restart"   // the daemon restarted first
)

// Breakdown is one turn.latency.
type Breakdown struct {
	UtteranceID string `json:"utterance_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	CallID      string `json:"call_id,omitempty"`
	// Relay is "voice" (the voice model's to_supervisor) or "daemon"
	// (the daemon relayed words the voice left unanswered).
	Relay string `json:"relay"`
	// Reply is how the answer reached the voice: "tool_result" or
	// "push" ("" when it never did).
	Reply   string `json:"reply,omitempty"`
	Outcome string `json:"outcome"`
	// Stale: the user had moved on when the reply landed.
	Stale bool `json:"stale,omitempty"`

	TotalMS      *int64  `json:"total_ms,omitempty"`
	VoiceMS      *int64  `json:"voice_ms,omitempty"`
	RelayMS      *int64  `json:"relay_ms,omitempty"`
	QueueMS      *int64  `json:"queue_ms,omitempty"`
	BrainMS      *int64  `json:"brain_ms,omitempty"`
	StepsMS      []int64 `json:"steps_ms,omitempty"`
	ModelMS      *int64  `json:"model_ms,omitempty"`
	ToolMS       *int64  `json:"tool_ms,omitempty"`
	DeliveryMS   *int64  `json:"delivery_ms,omitempty"`
	PushQueueMS  *int64  `json:"push_queue_ms,omitempty"`
	FirstAudioMS *int64  `json:"first_audio_ms,omitempty"`

	// At is when the record ended (the playback start, or the event or
	// tick that ended it).
	At time.Time `json:"-"`
}

// Payload is the turn.latency payload; its utterance_id and turn_id are
// lifted to the line's top level by the event log.
func (b Breakdown) Payload() map[string]any {
	raw, _ := json.Marshal(b)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// record is one utterance being followed.
type record struct {
	uttID, callID string
	relay         string
	turnKey       string
	turnID        string

	end        time.Time // the user's last words (zero: unknown)
	toolCallAt time.Time
	relayAt    time.Time
	relayWait  time.Duration // the daemon's own relay: its unanswered wait

	turnStart time.Time
	steps     []int64
	toolMS    int64
	brainEnd  time.Time

	reply     string
	handover  time.Time // reply handed over (tool result, push queued)
	pushAt    time.Time // push sent to the voice model
	awaitFrom time.Time // waiting for audio since (zero: not yet)
	stale     bool
}

// pendingCall is a voice to_supervisor call not yet relayed.
type pendingCall struct {
	at     time.Time
	lastIn time.Time
}

// Tracker builds Breakdowns from events in log order. It is not safe for
// concurrent use. The live daemon feeds it from the observer
// (LatencyObserver); the scorecard feeds it a recorded log (Replay).
type Tracker struct {
	open   map[string]*record // by utterance id (and legacy aliases)
	byCall map[string]*record
	byTurn map[string]*record
	calls  map[string]pendingCall
	lastIn time.Time
	// TaggedPlayback: the voice tags each reply's playback start with
	// its utterance_id (this daemon's sessions do), so an untagged start
	// is never a reply. False for an older log: an untagged start goes
	// to the reply waiting longest.
	TaggedPlayback bool
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{open: map[string]*record{}, byCall: map[string]*record{}, byTurn: map[string]*record{}, calls: map[string]pendingCall{}}
}

// payload fields the tracker reads; every decode is best-effort.
type latencyFields struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	CallID           string      `json:"call_id"`
	WaitedMS         *int64      `json:"waited_ms"`
	Turn             json.Number `json:"turn"`
	Step             int         `json:"step"`
	Template         string      `json:"template"`
	Pending          bool        `json:"pending"`
	Cancelled        bool        `json:"cancelled"`
	Spoken           string      `json:"spoken"`
	AnswersUtterance string      `json:"answers_utterance"`
	Skipped          bool        `json:"skipped"`
	SchedulingSent   string      `json:"scheduling_sent"`
	Replaces         string      `json:"replaces"`
	State            string      `json:"state"`
}

// Add feeds one event and returns the breakdowns it completed.
func (t *Tracker) Add(ev eventlog.Event) []Breakdown {
	var p latencyFields
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &p)
	}
	switch ev.Type {
	case "daemon.start":
		out := t.endAll(ev.TS, OutcomeRestart)
		t.calls = map[string]pendingCall{}
		t.lastIn = time.Time{}
		return out
	case "voice.transcript.in":
		t.lastIn = ev.TS
	case "voice.tool_call":
		if p.Name == "to_supervisor" && p.ID != "" {
			t.calls[p.ID] = pendingCall{at: ev.TS, lastIn: t.lastIn}
		}
	case "supervisor.relay", "supervisor.relay_fallback", "supervisor.relay_by_daemon":
		t.relayed(ev, p)
	case "brain.turn_started":
		if r := t.open[ev.UtteranceID]; r != nil && ev.UtteranceID != "" && r.turnKey == "" {
			t.attachTurn(r, turnKey(ev, p), ev.TurnID, ev.TS)
		}
	case "brain.step":
		key := turnKey(ev, p)
		r := t.byTurn[key]
		if r == nil && ev.TurnID == "" && p.Step == 1 && (p.Template == "utterance" || p.Template == "stop") {
			// No ids: the first utterance step after a relay is its turn.
			start := ev.TS
			if ev.DurMS != nil {
				start = start.Add(-time.Duration(*ev.DurMS) * time.Millisecond)
			}
			if r = t.oldestWithoutTurn(start); r != nil {
				t.attachTurn(r, key, "", start)
			}
		}
		if r != nil && ev.DurMS != nil {
			r.steps = append(r.steps, *ev.DurMS)
		}
	case "brain.tool_result":
		if r := t.byTurn[turnKey(ev, p)]; r != nil && ev.DurMS != nil {
			r.toolMS += *ev.DurMS
		}
	case "brain.turn":
		if r := t.byTurn[turnKey(ev, p)]; r != nil {
			r.brainEnd = ev.TS
		}
	case "voice.tool_result":
		r := t.byCall[p.ID]
		switch {
		case r == nil || p.Cancelled:
			// A cancelled call's reply may still come as a push.
		case p.Pending:
			r.reply = "push"
		case p.Spoken != "":
			r.reply, r.handover, r.awaitFrom = "tool_result", ev.TS, ev.TS
		default:
			return t.end(r, ev.TS, OutcomeSilent)
		}
	case "brain.reply_pending":
		// Older logs: the first event naming both the utterance and
		// the call.
		if r := t.byCall[p.CallID]; r != nil && p.ID != "" && t.open[p.ID] == nil {
			r.uttID = p.ID
			t.open[p.ID] = r
		}
	case "brain.reply_delivered":
		if r := t.lookup(ev, p.ID); r != nil {
			r.reply = "push"
			if r.handover.IsZero() {
				r.handover = ev.TS
			}
		}
	case "brain.reply_stale":
		if r := t.lookup(ev, p.ID); r != nil {
			r.stale = true
		}
	case "supervisor.utterance_coalesced":
		if r := t.open[p.Replaces]; r != nil {
			return t.end(r, ev.TS, OutcomeCoalesced)
		}
	case "voice.push":
		r := t.lookup(ev, p.AnswersUtterance)
		if r == nil {
			break
		}
		r.reply, r.pushAt = "push", ev.TS
		if r.handover.IsZero() {
			r.handover = ev.TS
		}
		switch {
		case p.Skipped:
			return t.end(r, ev.TS, OutcomeNoVoice)
		case p.SchedulingSent == "silent":
			return t.end(r, ev.TS, OutcomeSilent)
		}
		r.awaitFrom = ev.TS
	case "voice.push_dropped":
		if r := t.lookup(ev, p.AnswersUtterance); r != nil {
			return t.end(r, ev.TS, OutcomeDropped)
		}
	case "voice.playback":
		if p.State != "started" {
			break
		}
		if ev.UtteranceID != "" {
			// The session tagged this start with the reply it plays.
			if r := t.open[ev.UtteranceID]; r != nil {
				if r.awaitFrom.IsZero() {
					r.awaitFrom = r.handover
				}
				return t.end(r, ev.TS, OutcomePlayed)
			}
			break
		}
		// Untagged: no reply when the voice tags them; in an older log,
		// the longest-waiting reply handed over by now.
		if t.TaggedPlayback {
			break
		}
		if r := t.firstAwaiting(ev.TS); r != nil {
			return t.end(r, ev.TS, OutcomePlayed)
		}
	}
	return nil
}

// relayed opens the record for a relay.
func (t *Tracker) relayed(ev eventlog.Event, p latencyFields) {
	key := ev.UtteranceID
	if key == "" && p.CallID != "" {
		key = "call:" + p.CallID
	}
	if key == "" || t.open[key] != nil {
		return
	}
	r := &record{uttID: ev.UtteranceID, callID: p.CallID, relay: "voice", relayAt: ev.TS}
	if ev.Type == "supervisor.relay_by_daemon" {
		r.relay = "daemon"
		if p.WaitedMS != nil {
			r.relayWait = time.Duration(*p.WaitedMS) * time.Millisecond
			r.end = ev.TS.Add(-r.relayWait)
		}
	} else if c, ok := t.calls[p.CallID]; ok {
		delete(t.calls, p.CallID)
		r.toolCallAt = c.at
		switch {
		case p.WaitedMS != nil:
			r.end = ev.TS.Add(-time.Duration(*p.WaitedMS) * time.Millisecond)
		case !c.lastIn.IsZero() && c.at.Sub(c.lastIn) <= legacyEndWindow:
			r.end = c.lastIn
		}
	}
	t.open[key] = r
	if p.CallID != "" {
		t.byCall[p.CallID] = r
	}
}

// turnKey is the key of the turn an event belongs to: its turn_id, else
// (older logs) its turn number.
func turnKey(ev eventlog.Event, p latencyFields) string {
	if ev.TurnID != "" {
		return ev.TurnID
	}
	if p.Turn != "" {
		return "n:" + p.Turn.String()
	}
	return ""
}

func (t *Tracker) attachTurn(r *record, key, id string, start time.Time) {
	if key == "" {
		return
	}
	r.turnKey, r.turnID, r.turnStart = key, id, start
	t.byTurn[key] = r
}

// lookup finds the record of an event: its utterance id, else the id
// its payload names.
func (t *Tracker) lookup(ev eventlog.Event, alt string) *record {
	if r := t.open[ev.UtteranceID]; r != nil && ev.UtteranceID != "" {
		return r
	}
	if alt != "" {
		return t.open[alt]
	}
	return nil
}

// oldestWithoutTurn is the oldest record relayed before at with no turn
// and no reply yet.
func (t *Tracker) oldestWithoutTurn(at time.Time) *record {
	var best *record
	for _, r := range t.uniq() {
		if r.turnKey != "" || !r.handover.IsZero() || r.relayAt.After(at) {
			continue
		}
		if best == nil || r.relayAt.Before(best.relayAt) {
			best = r
		}
	}
	return best
}

// firstAwaiting is the record waiting longest for audio, handed over by
// at.
func (t *Tracker) firstAwaiting(at time.Time) *record {
	var best *record
	for _, r := range t.uniq() {
		if r.awaitFrom.IsZero() || r.awaitFrom.After(at) {
			continue
		}
		if best == nil || r.awaitFrom.Before(best.awaitFrom) {
			best = r
		}
	}
	return best
}

// uniq lists the open records once each (a record may be under two keys).
func (t *Tracker) uniq() []*record {
	seen := make(map[*record]bool, len(t.open))
	out := make([]*record, 0, len(t.open))
	for _, r := range t.open {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].relayAt.Before(out[j].relayAt) })
	return out
}

// Expire ends the records that waited too long by now.
func (t *Tracker) Expire(now time.Time) []Breakdown {
	var out []Breakdown
	for _, r := range t.uniq() {
		switch {
		case !r.awaitFrom.IsZero() && now.Sub(r.awaitFrom) > audioWait:
			out = append(out, t.end(r, now, OutcomeNoAudio)...)
		case now.Sub(r.relayAt) > recordTTL:
			out = append(out, t.end(r, now, OutcomeTimeout)...)
		}
	}
	for id, c := range t.calls {
		if now.Sub(c.at) > callTTL {
			delete(t.calls, id)
		}
	}
	return out
}

// Flush ends every open record as timeout (the end of a replayed log).
func (t *Tracker) Flush(at time.Time) []Breakdown { return t.endAll(at, OutcomeTimeout) }

func (t *Tracker) endAll(at time.Time, outcome string) []Breakdown {
	var out []Breakdown
	for _, r := range t.uniq() {
		out = append(out, t.end(r, at, outcome)...)
	}
	return out
}

// end removes r and returns its breakdown.
func (t *Tracker) end(r *record, at time.Time, outcome string) []Breakdown {
	for k, v := range t.open {
		if v == r {
			delete(t.open, k)
		}
	}
	if r.callID != "" && t.byCall[r.callID] == r {
		delete(t.byCall, r.callID)
	}
	if r.turnKey != "" && t.byTurn[r.turnKey] == r {
		delete(t.byTurn, r.turnKey)
	}
	return []Breakdown{r.breakdown(at, outcome)}
}

func (r *record) breakdown(at time.Time, outcome string) Breakdown {
	b := Breakdown{UtteranceID: r.uttID, TurnID: r.turnID, CallID: r.callID, Relay: r.relay, Reply: r.reply, Outcome: outcome, Stale: r.stale, At: at}
	if r.relay == "daemon" {
		if r.relayWait > 0 {
			b.RelayMS = msPtr(r.relayWait)
		}
	} else {
		b.VoiceMS = span(r.end, r.toolCallAt)
		b.RelayMS = span(r.toolCallAt, r.relayAt)
	}
	b.QueueMS = span(r.relayAt, r.turnStart)
	b.BrainMS = span(r.turnStart, r.brainEnd)
	if len(r.steps) > 0 {
		b.StepsMS = append([]int64(nil), r.steps...)
		var sum int64
		for _, s := range r.steps {
			sum += s
		}
		b.ModelMS = &sum
	}
	if r.turnKey != "" {
		tool := r.toolMS
		b.ToolMS = &tool
	}
	from := r.brainEnd
	if from.IsZero() {
		from = r.relayAt
	}
	b.DeliveryMS = span(from, r.handover)
	if r.reply == "push" {
		b.PushQueueMS = span(r.handover, r.pushAt)
	}
	if outcome == OutcomePlayed {
		b.FirstAudioMS = span(r.awaitFrom, at)
		start := r.end
		if start.IsZero() {
			start = r.toolCallAt
		}
		if start.IsZero() {
			start = r.relayAt
		}
		b.TotalMS = span(start, at)
	}
	return b
}

// span is b − a in milliseconds, at least 0; nil when either is unknown.
func span(a, b time.Time) *int64 {
	if a.IsZero() || b.IsZero() {
		return nil
	}
	return msPtr(max(b.Sub(a), 0))
}

func msPtr(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

// Replay runs a recorded, ordered event slice through a tracker and
// returns every breakdown, the ones still open at the end as timeout.
// turn.latency events already in the log are ignored (the scorecard
// prefers them when present: TurnLatencies).
func Replay(events []eventlog.Event) []Breakdown {
	t := NewTracker()
	for _, ev := range events {
		if ev.Type == "voice.playback" && ev.UtteranceID != "" {
			t.TaggedPlayback = true
			break
		}
	}
	var out []Breakdown
	var lastExpire time.Time
	for _, ev := range events {
		out = append(out, t.Add(ev)...)
		if ev.TS.Sub(lastExpire) >= time.Second {
			out = append(out, t.Expire(ev.TS)...)
			lastExpire = ev.TS
		}
	}
	if len(events) > 0 {
		out = append(out, t.Flush(events[len(events)-1].TS)...)
	}
	return out
}

// LatencyObserver is the live Handler: it logs each completed breakdown
// as turn.latency.
type LatencyObserver struct {
	log logging.Logger
	t   *Tracker
}

// NewLatencyObserver returns the turn.latency handler.
func NewLatencyObserver(log logging.Logger) *LatencyObserver {
	t := NewTracker()
	t.TaggedPlayback = true // this daemon's voice sessions tag reply playback
	return &LatencyObserver{log: log, t: t}
}

// Event implements Handler.
func (o *LatencyObserver) Event(ev eventlog.Event) { o.emit(o.t.Add(ev)) }

// Tick implements Handler.
func (o *LatencyObserver) Tick(now time.Time) { o.emit(o.t.Expire(now)) }

func (o *LatencyObserver) emit(bs []Breakdown) {
	for _, b := range bs {
		o.log.Log(logSource, "turn.latency", "", b.Payload())
	}
}
