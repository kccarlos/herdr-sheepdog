package voiceclient

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Frame is one /ws frame, either way.
type Frame struct {
	AtMS int64 `json:"at_ms"`
	// Dir is up (client -> daemon) or down.
	Dir string `json:"dir"`
	// Kind is text or audio; audio frames keep only their size.
	Kind  string          `json:"kind"`
	Bytes int             `json:"bytes,omitempty"`
	Msg   json.RawMessage `json:"msg,omitempty"`
}

// Event is one daemon event from /events.
type Event struct {
	AtMS    int64          `json:"at_ms"`
	ID      int64          `json:"id,omitempty"`
	Type    string         `json:"type"`
	Pane    string         `json:"pane,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Fragment is one transcript fragment; AudioMS is how much of the
// utterance's audio had arrived when it came (where it sits in the run).
type Fragment struct {
	AtMS    int64  `json:"at_ms"`
	Text    string `json:"text"`
	AudioMS int    `json:"audio_ms,omitempty"`
	Final   bool   `json:"final,omitempty"`
}

// Utterance ends.
const (
	EndDrained    = "drained"     // the player played it to the end
	EndFlushed    = "flushed"     // the daemon flushed it (barge-in, cancel, an interrupting push)
	EndLocalFlush = "local_flush" // the client cut it (the user started talking)
	EndDetached   = "detached"    // the client disconnected
	EndNoAudio    = "no_audio"    // a transcript that never came with audio
)

// BotUtterance is one thing the bot said: from its first audio or
// transcript to the player's drained or a flush.
type BotUtterance struct {
	Index   int   `json:"index"`
	StartMS int64 `json:"start_ms"`
	// PlayStartMS is when the player started it (-1: never).
	PlayStartMS int64      `json:"play_start_ms"`
	EndMS       int64      `json:"end_ms"`
	End         string     `json:"end"`
	Fragments   []Fragment `json:"fragments,omitempty"`
	// Text is the whole output transcript; Heard the part the user
	// heard (all of it when drained; cut at PlayedMS on a flush).
	Text     string `json:"text"`
	Heard    string `json:"heard"`
	PlayedMS int    `json:"played_ms"`
	AudioMS  int    `json:"audio_ms"`
	// Push is set when a voice.push event preceded it (the daemon spoke
	// on its own rather than answering).
	Push   bool   `json:"push,omitempty"`
	PushID string `json:"push_id,omitempty"`
}

// UserUtterance is one thing the simulated user said.
type UserUtterance struct {
	Label   string `json:"label"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	// BargeIn: the bot was playing when the user started.
	BargeIn bool `json:"barge_in,omitempty"`
}

// Timed is a timestamped note (status changes, errors, reports sent).
type Timed struct {
	AtMS int64  `json:"at_ms"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

// Recording is everything one client saw and did.
type Recording struct {
	Start     time.Time       `json:"start"`
	Frames    []Frame         `json:"frames"`
	Events    []Event         `json:"events"`
	Bot       []BotUtterance  `json:"bot"`
	User      []UserUtterance `json:"user"`
	In        []Fragment      `json:"in_transcripts"`
	Notes     []Timed         `json:"notes"`
	Features  []string        `json:"features,omitempty"`
	Connected bool            `json:"-"`
	// MicAudio is every frame the microphone produced (16 kHz, gated or
	// not); BotAudio every sample the daemon sent (24 kHz). Not in JSON.
	MicAudio []int16 `json:"-"`
	BotAudio []int16 `json:"-"`
}

// heardText cuts an utterance's transcript at playedMS: every fragment
// is placed at the audio received when it arrived, and the fragment
// straddling the cut contributes a proportional share of its words (the
// daemon's own method, docs/ws-contract.md "Playback reports").
func heardText(frags []Fragment, playedMS, audioMS int) string {
	if playedMS >= audioMS {
		return joinFragments(frags)
	}
	var keep []Fragment
	for i, f := range frags {
		end := audioMS
		if i+1 < len(frags) {
			end = frags[i+1].AudioMS
		}
		switch {
		case end <= playedMS:
			keep = append(keep, f)
		case f.AudioMS < playedMS:
			span := end - f.AudioMS
			share := 1.0
			if span > 0 {
				share = float64(playedMS-f.AudioMS) / float64(span)
			}
			keep = append(keep, Fragment{Text: cutWords(f.Text, share)})
		}
	}
	return joinFragments(keep)
}

// cutWords keeps the first share of s's words (characters for CJK).
func cutWords(s string, share float64) string {
	if hasCJK(s) {
		r := []rune(s)
		return string(r[:int(float64(len(r))*share)])
	}
	w := strings.Fields(s)
	n := int(float64(len(w))*share + 0.5)
	if strings.HasPrefix(s, " ") && n > 0 {
		return " " + strings.Join(w[:n], " ")
	}
	return strings.Join(w[:n], " ")
}

func joinFragments(frags []Fragment) string {
	var b strings.Builder
	for _, f := range frags {
		b.WriteString(f.Text)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
