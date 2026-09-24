package ctxcap

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Bounds of the voice context log (items and turn points kept).
const (
	VoiceItems = 3000
	VoiceTurns = 1000
)

// The voice item kinds.
const (
	VKindInstructions = "instructions" // the system instructions a session opened with
	VKindTools        = "tools"        // the tool declarations (or the delegation setting)
	VKindAudio        = "audio"        // microphone audio, as a duration, with what the model heard of it
	VKindUserText     = "user_text"    // a typed utterance sent as text
	VKindModel        = "model"        // what the model said (its output transcript)
	VKindToolCall     = "tool_call"    // a function call / delegation the model made
	VKindToolResponse = "tool_response"
	VKindPush         = "push"    // a spoken push
	VKindContext      = "context" // silent context (a silent push, a recap, held context)
	VKindMarker       = "marker"  // session open/close, resume, reset, provider switch
)

// VoiceItem is one thing in a voice model's context, in the order the
// daemon sent (or received) it.
type VoiceItem struct {
	ID       int       `json:"id"`
	Epoch    int       `json:"epoch"`
	Session  string    `json:"session"`
	Provider string    `json:"provider"`
	TS       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Class    string    `json:"class"`
	Role     string    `json:"role"`
	Label    string    `json:"label"`
	Text     string    `json:"text,omitempty"`
	Links    []string  `json:"links,omitempty"`
	// AudioMS is the audio's duration (audio items): the bytes are never
	// kept.
	AudioMS int64 `json:"audio_ms,omitempty"`
	// Heard is the model's input transcript of the audio.
	Heard string `json:"heard,omitempty"`
	// Raw is the outbound message as sent, without audio bytes.
	Raw json.RawMessage `json:"raw,omitempty"`

	// open: the item still grows (audio frames, transcript fragments);
	// heardDone: the input transcript was final.
	open, heardDone bool
	samples         int64 // audio samples so far
	rate            int
	sess            *VoiceSession
}

// VoiceTurn is one point of the voice timeline: an input the model
// reacts to (an utterance, a tool response, a push, a session opening).
type VoiceTurn struct {
	Turn     int       `json:"turn"`
	TS       time.Time `json:"ts"`
	Epoch    int       `json:"epoch"`
	Session  string    `json:"session"`
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
	Label    string    `json:"label"`
	// First is the id of the item that opened the turn.
	First int `json:"first"`
}

// VoiceLog is the daemon's voice context log: every voice session writes
// to it through its own VoiceSession. An epoch is one context the model
// holds: a fresh session starts one (Gemini without a resumption handle,
// every GPT-Live session, the text router), a resumed one continues it.
// Safe for concurrent use.
type VoiceLog struct {
	mu       sync.Mutex
	items    []*VoiceItem
	turns    []VoiceTurn
	nextItem int
	nextTurn int
	epoch    int
	sessions map[string]int
	evicted  bool
	now      func() time.Time
	notify   func(t VoiceTurn, updated bool)
}

// NewVoiceLog returns an empty log; notify, when set, is called outside
// the lock with every new turn point (updated false) and when the model
// finishes speaking in one (updated true).
func NewVoiceLog(notify func(t VoiceTurn, updated bool)) *VoiceLog {
	return &VoiceLog{nextItem: 1, nextTurn: 1, sessions: map[string]int{}, now: time.Now, notify: notify}
}

// SetClock overrides the clock (tests).
func (l *VoiceLog) SetClock(now func() time.Time) { l.now = now }

// Session returns the recorder of one provider session ("gemini#2").
// A nil log returns a nil recorder, whose methods do nothing.
func (l *VoiceLog) Session(provider, model string) *VoiceSession {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.sessions[provider]++
	id := fmt.Sprintf("%s#%d", provider, l.sessions[provider])
	l.mu.Unlock()
	return &VoiceSession{log: l, id: id, provider: provider, model: model}
}

// Marker records a daemon-level marker (a provider switch) outside any
// session; newEpoch starts a new context with it.
func (l *VoiceLog) Marker(label, text string, newEpoch bool) {
	if l == nil {
		return
	}
	s := &VoiceSession{log: l, id: "daemon", provider: "daemon"}
	s.add(VoiceItem{Kind: VKindMarker, Class: ClassMarker, Role: "daemon", Label: label, Text: text}, newEpoch)
}

// VoiceSession records one provider session's items. A nil
// *VoiceSession is valid and records nothing.
type VoiceSession struct {
	log      *VoiceLog
	id       string
	provider string
	model    string

	pmu sync.Mutex
	// pending is an epoch marker recorded just before the session's
	// first item (EpochOnFirstUse).
	pending *VoiceItem
}

// EpochOnFirstUse makes the session open its epoch (a marker with label
// and text) right before its first item, rather than now: a session
// set up at startup does not record anything until it is used.
func (s *VoiceSession) EpochOnFirstUse(label, text string) {
	if s == nil {
		return
	}
	s.pmu.Lock()
	s.pending = &VoiceItem{Kind: VKindMarker, Class: ClassMarker, Role: "daemon", Label: label, Text: text}
	s.pmu.Unlock()
}

// ID is the session's id ("gemini#2"; "" for nil).
func (s *VoiceSession) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

// Model is the session's model.
func (s *VoiceSession) Model() string {
	if s == nil {
		return ""
	}
	return s.model
}

// starts reports whether an item of kind opens a turn point.
func starts(kind string) bool {
	switch kind {
	case VKindUserText, VKindToolResponse, VKindPush, VKindContext, VKindMarker, VKindInstructions:
		return true
	default:
		return false
	}
}

// add appends it (closing whatever was open), opening a new epoch first
// when newEpoch, and a turn point when its kind starts one.
func (s *VoiceSession) add(it VoiceItem, newEpoch bool) {
	s.addTurn(it, newEpoch, starts(it.Kind))
}

// addTurn is add with the turn decision made by the caller.
func (s *VoiceSession) addTurn(it VoiceItem, newEpoch, turnPoint bool) {
	s.pmu.Lock()
	pending := s.pending
	s.pending = nil
	s.pmu.Unlock()
	if pending != nil {
		s.addTurn(*pending, true, true)
	}
	l := s.log
	l.mu.Lock()
	if newEpoch {
		l.epoch++
	}
	if l.epoch == 0 {
		l.epoch = 1
	}
	l.closeLastLocked()
	it.ID = l.nextItem
	l.nextItem++
	it.Epoch, it.Session, it.Provider, it.sess = l.epoch, s.id, s.provider, s
	if it.TS.IsZero() {
		it.TS = l.now()
	}
	cp := it
	l.items = append(l.items, &cp)
	if len(l.items) > VoiceItems {
		l.items = append([]*VoiceItem(nil), l.items[len(l.items)-VoiceItems:]...)
		l.evicted = true
	}
	var turn *VoiceTurn
	if turnPoint {
		t := l.startTurnLocked(&cp)
		turn = &t
	}
	l.mu.Unlock()
	if turn != nil && l.notify != nil {
		l.notify(*turn, false)
	}
}

func (l *VoiceLog) startTurnLocked(it *VoiceItem) VoiceTurn {
	t := VoiceTurn{Turn: l.nextTurn, TS: it.TS, Epoch: it.Epoch, Session: it.Session, Provider: it.Provider, Kind: it.Kind, Label: it.Label, First: it.ID}
	l.nextTurn++
	l.turns = append(l.turns, t)
	if len(l.turns) > VoiceTurns {
		l.turns = append([]VoiceTurn(nil), l.turns[len(l.turns)-VoiceTurns:]...)
	}
	return t
}

func (l *VoiceLog) closeLastLocked() {
	if n := len(l.items); n > 0 {
		l.items[n-1].open = false
	}
}

// lastLocked is the newest item when it is still open and s's own.
func (l *VoiceLog) lastOpenLocked(s *VoiceSession, kind string) *VoiceItem {
	if n := len(l.items); n > 0 {
		it := l.items[n-1]
		if it.open && it.sess == s && it.Kind == kind {
			return it
		}
	}
	return nil
}

// Epoch starts a fresh context: the session opened without the previous
// conversation. label and text describe it (a marker item).
func (s *VoiceSession) Epoch(label, text string, raw any) {
	if s == nil {
		return
	}
	s.add(VoiceItem{Kind: VKindMarker, Class: ClassMarker, Role: "daemon", Label: label, Text: text, Raw: rawOf(raw)}, true)
}

// Marker records a session marker (resume, close, reset).
func (s *VoiceSession) Marker(label, text string, raw any) {
	if s == nil {
		return
	}
	s.add(VoiceItem{Kind: VKindMarker, Class: ClassMarker, Role: "daemon", Label: label, Text: text, Raw: rawOf(raw)}, false)
}

// Instructions records the system instructions the session runs with.
func (s *VoiceSession) Instructions(text string, raw any) {
	if s == nil {
		return
	}
	s.add(VoiceItem{Kind: VKindInstructions, Class: ClassSystem, Role: "system", Label: "system instructions", Text: text, Raw: rawOf(raw)}, false)
}

// Tools records the tool declarations (text: one line per tool).
func (s *VoiceSession) Tools(text string, raw any) {
	if s == nil {
		return
	}
	s.add(VoiceItem{Kind: VKindTools, Class: ClassTools, Role: "system", Label: "tool declarations", Text: text, Raw: rawOf(raw)}, false)
}

// Item records a generic item (a push, a tool response, context); Kind,
// Class, Role, Label, Text, Links and Raw are taken from it.
func (s *VoiceSession) Item(it VoiceItem) {
	if s == nil {
		return
	}
	s.add(it, false)
}

// Audio adds n bytes of 16-bit mono PCM at rate to the open audio item,
// or opens one.
func (s *VoiceSession) Audio(n, rate int) {
	if s == nil || n <= 0 || rate <= 0 {
		return
	}
	l := s.log
	l.mu.Lock()
	if it := l.lastOpenLocked(s, VKindAudio); it != nil && it.rate == rate && !it.heardDone {
		it.samples += int64(n / 2)
		it.AudioMS = it.samples * 1000 / int64(rate)
		it.Text = audioText(it)
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	it := VoiceItem{Kind: VKindAudio, Class: ClassAudio, Role: "user", Label: "shepherd audio", open: true, samples: int64(n / 2), rate: rate}
	it.AudioMS = it.samples * 1000 / int64(rate)
	it.Text = audioText(&it)
	s.add(it, false)
}

// AudioEnd closes the open audio item (the stream paused).
func (s *VoiceSession) AudioEnd() {
	if s == nil {
		return
	}
	l := s.log
	l.mu.Lock()
	if it := l.lastOpenLocked(s, VKindAudio); it != nil {
		it.Text = audioText(it) + " · stream end"
		it.open = false
		if it.Heard != "" {
			it.heardDone = true
		}
	}
	l.mu.Unlock()
}

func audioText(it *VoiceItem) string {
	t := fmt.Sprintf("🎤 audio %.1f s", float64(it.AudioMS)/1000)
	if it.Heard != "" {
		t += " — heard: " + fmt.Sprintf("%q", strings.TrimSpace(it.Heard))
	}
	return t
}

// Heard attaches the model's input transcript to the newest audio item
// of the session (the first fragment opens a turn point there), or
// records it as an utterance of its own when there is none.
func (s *VoiceSession) Heard(text string, final bool) {
	if s == nil || (text == "" && !final) {
		return
	}
	l := s.log
	l.mu.Lock()
	var target *VoiceItem
	for i := len(l.items) - 1; i >= 0 && i >= len(l.items)-4; i-- {
		it := l.items[i]
		if it.sess != s || it.Kind != VKindAudio {
			continue
		}
		if !it.heardDone {
			target = it
		}
		break
	}
	if target == nil {
		l.mu.Unlock()
		if text != "" {
			// A transcript without audio before it still opens a turn.
			s.addTurn(VoiceItem{Kind: VKindAudio, Class: ClassShepherd, Role: "user", Label: "shepherd (transcript only)", Heard: text, Text: "🎤 heard: " + fmt.Sprintf("%q", strings.TrimSpace(text)), heardDone: final, open: !final}, false, true)
		}
		return
	}
	first := target.Heard == ""
	target.Heard += text
	target.Class, target.Label = ClassShepherd, "shepherd (audio + transcript)"
	if target.rate == 0 {
		target.Text = "🎤 heard: " + fmt.Sprintf("%q", strings.TrimSpace(target.Heard))
	} else {
		target.Text = audioText(target)
	}
	if final {
		target.heardDone = true
	}
	var t *VoiceTurn
	if first && text != "" {
		tt := l.startTurnLocked(target)
		t = &tt
	}
	l.mu.Unlock()
	if t != nil && l.notify != nil {
		l.notify(*t, false)
	}
}

// Said adds an output transcript fragment to the open model item, or
// opens one.
func (s *VoiceSession) Said(text string) {
	if s == nil || text == "" {
		return
	}
	l := s.log
	l.mu.Lock()
	if it := l.lastOpenLocked(s, VKindModel); it != nil {
		it.Text += text
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	s.add(VoiceItem{Kind: VKindModel, Class: ClassVoice, Role: "model", Label: "voice said", Text: text, open: true}, false)
}

// ModelDone closes the open model item (the model's turn completed) and
// tells the listener the turn grew.
func (s *VoiceSession) ModelDone() {
	if s == nil {
		return
	}
	l := s.log
	l.mu.Lock()
	it := l.lastOpenLocked(s, VKindModel)
	if it != nil {
		it.open = false
	}
	var t VoiceTurn
	ok := it != nil && len(l.turns) > 0
	if ok {
		t = l.turns[len(l.turns)-1]
	}
	l.mu.Unlock()
	if ok && l.notify != nil {
		l.notify(t, true)
	}
}

// Claim links the session's newest utterance (audio with a transcript,
// or typed text) that is not linked to a call yet, among the last few
// items, to link: the tool call that relays it.
func (s *VoiceSession) Claim(link string) {
	if s == nil || link == "" {
		return
	}
	l := s.log
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.items) - 1; i >= 0 && i >= len(l.items)-8; i-- {
		it := l.items[i]
		if it.sess != s {
			continue
		}
		utterance := (it.Kind == VKindAudio && it.Heard != "") || it.Kind == VKindUserText
		if !utterance {
			continue
		}
		for _, k := range it.Links {
			if strings.HasPrefix(k, "call:") {
				return
			}
		}
		it.Links = append(it.Links, link)
		return
	}
}

// rawOf marshals v for an item's Raw (nil stays nil).
func rawOf(v any) json.RawMessage {
	switch r := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return r
	case []byte:
		return json.RawMessage(r)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Raw marshals v for VoiceItem.Raw.
func Raw(v any) json.RawMessage { return rawOf(v) }

// Turns returns the turn points held, oldest first.
func (l *VoiceLog) Turns() []VoiceTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]VoiceTurn(nil), l.turns...)
}

// VoicePoint is one turn's context: the turn, and the snapshot of its
// epoch up to the end of the turn.
type VoicePoint struct {
	VoiceTurn
	// Evicted is set when the epoch began before the oldest item kept.
	Evicted bool `json:"evicted,omitempty"`
	Snapshot
}

// Get returns turn n's point (n <= 0: the latest) and the previous
// turn's (hasPrev false when there is none).
func (l *VoiceLog) Get(n int) (cur, prev VoicePoint, hasPrev bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.turns) == 0 {
		return VoicePoint{}, VoicePoint{}, false, ErrNoStep
	}
	i := len(l.turns) - 1
	if n > 0 {
		i = -1
		for j, t := range l.turns {
			if t.Turn == n {
				i = j
				break
			}
		}
		if i < 0 {
			return VoicePoint{}, VoicePoint{}, false, ErrNoStep
		}
	}
	cur = l.pointLocked(i)
	if i > 0 {
		return cur, l.pointLocked(i - 1), true, nil
	}
	return cur, VoicePoint{}, false, nil
}

// pointLocked renders turn i: the items of its epoch from the epoch's
// start up to the item before the next turn's first.
func (l *VoiceLog) pointLocked(i int) VoicePoint {
	t := l.turns[i]
	end := int(^uint(0) >> 1)
	if i+1 < len(l.turns) {
		end = l.turns[i+1].First - 1
	}
	var items []*VoiceItem
	for _, it := range l.items {
		if it.Epoch == t.Epoch && it.ID <= end {
			items = append(items, it)
		}
	}
	p := VoicePoint{VoiceTurn: t, Snapshot: renderVoice(items)}
	// The epoch may have started before the oldest item kept.
	p.Evicted = l.evicted && len(l.items) > 0 && l.items[0].Epoch == t.Epoch
	return p
}

// renderVoice lays the items out as the pane's raw text, one section
// per item.
func renderVoice(items []*VoiceItem) Snapshot {
	var bd Builder
	for _, it := range items {
		sec := Section{
			Key: fmt.Sprintf("v:%d", it.ID), Label: it.Label, Class: it.Class, Group: it.Session,
			Role: it.Role, Links: append([]string(nil), it.Links...), Raw: it.Raw,
		}
		bd.Add(sec, "── "+it.Role+" · "+it.Label+" ──\n"+strings.TrimRight(it.Text, "\n")+"\n\n")
	}
	return bd.Snapshot(nil)
}

// DiffVoice is the diff of cur against prev: a different epoch between
// them means the context was started afresh (a reset, a fresh session).
func DiffVoice(cur, prev VoicePoint, hasPrev bool) Diff {
	if !hasPrev {
		return DiffSnapshots(0, Snapshot{}, cur.Snapshot, false)
	}
	return DiffSnapshots(prev.Turn, prev.Snapshot, cur.Snapshot, prev.Epoch != cur.Epoch)
}
