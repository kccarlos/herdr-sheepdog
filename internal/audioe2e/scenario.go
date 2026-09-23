// Package audioe2e is the text-to-speech end-to-end harness behind
// `sheepdog audio-e2e`: it renders scenario speech with macOS `say`,
// mixes a generated noise bed at a given SNR, streams it into a running
// daemon's /ws exactly like a client (16 kHz PCM16 in 40 ms frames at
// real-time pace, with the same silence-gate messages the apps send),
// records every server message and the /events stream, and checks the
// scenario's expectations (docs/audio-e2e-results.md).
package audioe2e

import (
	"encoding/json"
	"fmt"
	"os"
)

// File is a scenarios file (testdata/audio-e2e/scenarios.json).
type File struct {
	// Voice is the default `say` voice (default Samantha).
	Voice     string     `json:"voice"`
	Scenarios []Scenario `json:"scenarios"`
}

// Scenario is one run: optional setup calls, then the audio segments,
// then a tail with the gate closed while the daemon answers.
type Scenario struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	// Providers limits the scenario to these voice providers (empty: all).
	Providers []string  `json:"providers,omitempty"`
	Setup     []Setup   `json:"setup,omitempty"`
	Segments  []Segment `json:"segments"`
	// Noise is a bed under every segment (speech and silence alike).
	Noise *Noise `json:"noise,omitempty"`
	// TailMS is how long to keep listening after the last segment
	// (default 10000); the run ends earlier once a reply has finished
	// (no audio for QuietMS after the last chunk) unless FullTail is set.
	TailMS   int  `json:"tail_ms,omitempty"`
	QuietMS  int  `json:"quiet_ms,omitempty"`
	FullTail bool `json:"full_tail,omitempty"`
	// CancelAfterMS sends {"type":"cancel"} this long after the first
	// reply audio arrived (the stop button); 0 = never.
	CancelAfterMS int `json:"cancel_after_ms,omitempty"`
	// SpeakAfterReplyMS starts the segments marked after_reply this long
	// after the first reply audio (barge-in: "stop" mid-reply).
	SpeakAfterReplyMS int    `json:"speak_after_reply_ms,omitempty"`
	Expect            Expect `json:"expect"`
}

// Setup is one HTTP call before the audio starts.
type Setup struct {
	// InjectBlocked posts /api/inject/blocked with the screen read from
	// testdata/herdr/<ScreenFixture>.
	InjectBlocked *InjectBlocked `json:"inject_blocked,omitempty"`
	// WaitMS pauses (e.g. for the block's announcement to be spoken).
	WaitMS int `json:"wait_ms,omitempty"`
}

// InjectBlocked is the mock herdr block.
type InjectBlocked struct {
	PaneID        string `json:"pane_id"`
	Alias         string `json:"alias,omitempty"`
	Kind          string `json:"kind,omitempty"`
	ScreenFixture string `json:"screen_fixture"`
}

// Segment is one piece of the input stream: speech, silence or noise.
type Segment struct {
	// Say is text rendered with `say`.
	Say   string `json:"say,omitempty"`
	Voice string `json:"voice,omitempty"`
	// SilenceMS is digital silence (the gate closes after 300 ms of it,
	// unless a noise bed keeps it open).
	SilenceMS int `json:"silence_ms,omitempty"`
	// NoiseMS is noise only, at the scenario bed's level (or NoiseDBFS).
	NoiseMS   int     `json:"noise_ms,omitempty"`
	NoiseDBFS float64 `json:"noise_dbfs,omitempty"`
	NoiseKind string  `json:"noise_kind,omitempty"`
	// AfterReply holds this segment until SpeakAfterReplyMS after the
	// first reply audio.
	AfterReply bool `json:"after_reply,omitempty"`
	// Speech marks the segment for the end-of-speech latency (default:
	// true for Say).
	Speech *bool `json:"speech,omitempty"`
}

// Noise is a generated bed: kind white|pink|brown|cafe, mixed either at
// SNRDB below the speech (RMS of the voiced speech) or at an absolute
// level DBFS.
type Noise struct {
	Kind  string   `json:"kind"`
	SNRDB *float64 `json:"snr_db,omitempty"`
	DBFS  float64  `json:"dbfs,omitempty"`
	Seed  int64    `json:"seed,omitempty"`
}

// Expect are the checks; zero values are not checked.
type Expect struct {
	// InContains: every group must fuzzily match the input transcript
	// (any alternative in a group, "|"-separated).
	InContains []string `json:"in_contains,omitempty"`
	// NoInTranscript: no input transcript at all (pure noise).
	NoInTranscript bool `json:"no_in_transcript,omitempty"`
	// Tool: a to_supervisor / stop call (voice.tool_call name).
	Tool string `json:"tool,omitempty"`
	// NoTool: no voice.tool_call at all.
	NoTool bool `json:"no_tool,omitempty"`
	// Reply: reply audio and an out transcript arrived.
	Reply bool `json:"reply,omitempty"`
	// Flush: a flush message arrived (stop / cancel).
	Flush bool `json:"flush,omitempty"`
	// KeysSent: the mock herdr got keys (herdr.send kind keys) — the
	// approval was authorised by the transcript.
	KeysSent bool `json:"keys_sent,omitempty"`
	// Events: each must appear (type, and payload fields equal).
	Events []EventMatch `json:"events,omitempty"`
}

// EventMatch matches one daemon event.
type EventMatch struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
	// Providers limits this check to these voice providers.
	Providers []string `json:"providers,omitempty"`
}

// Load reads a scenarios file.
func Load(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("audioe2e: %s: %w", path, err)
	}
	if f.Voice == "" {
		f.Voice = "Samantha"
	}
	for i, s := range f.Scenarios {
		if s.Name == "" {
			return f, fmt.Errorf("audioe2e: scenario %d has no name", i)
		}
		if len(s.Segments) == 0 {
			return f, fmt.Errorf("audioe2e: scenario %s has no segments", s.Name)
		}
	}
	return f, nil
}

// For reports whether s runs on provider.
func (s Scenario) For(provider string) bool {
	return inList(provider, s.Providers)
}

func inList(v string, list []string) bool {
	if len(list) == 0 {
		return true
	}
	for _, l := range list {
		if l == v {
			return true
		}
	}
	return false
}
