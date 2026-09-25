package web

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// TestWSPlaybackReportsReachTheSession: the client's playback messages
// go to the voice session (which logs voice.playback with the run), a
// malformed one is an error, and web.client.stats counts them.
func TestWSPlaybackReportsReachTheSession(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	c.sendRaw(`{"type":"playback","state":"started"}`)
	c.send(wsproto.Playback(wsproto.PlaybackFlushed, 4000))
	waitFor(t, time.Second, func() bool { return len(h.session.playbackReports()) == 2 }, "playback reports at the session")
	if got := h.session.playbackReports(); got[0] != "started:0" || got[1] != "flushed:4000" {
		t.Fatalf("reports = %v", got)
	}
	c.sendRaw(`{"type":"playback","state":"drained"}`)
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "played_ms") {
		t.Fatalf("error = %+v", e)
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "web.client.stats" && strings.Contains(string(ev.Payload), `"playback_reports":2`) {
				return true
			}
		}
		return false
	}, "stats counting the playback reports")
}

// TestWSSilenceGateIsTheSpeechDetector: from a client that never sends
// speaking, its silence gate is the speech detector (open = speaking);
// once it sends speaking, that is authoritative and the gate is not.
func TestWSSilenceGateIsTheSpeechDetector(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	c.send(wsproto.Silence(false))
	c.send(wsproto.Silence(true))
	waitFor(t, time.Second, func() bool { return len(h.session.speakingReports()) == 2 }, "gate as detector")
	if got := h.session.speakingReports(); !got[0] || got[1] {
		t.Fatalf("speaking from the gate = %v, want [true false]", got)
	}

	c.send(wsproto.Speaking(true))
	c.send(wsproto.Silence(false)) // no longer the detector
	c.send(wsproto.Speaking(false))
	waitFor(t, time.Second, func() bool { return len(h.session.speakingReports()) == 4 }, "speaking messages")
	if got := h.session.speakingReports(); !got[2] || got[3] {
		t.Fatalf("speaking = %v, want the speaking messages only after the first", got)
	}
	var logged []string
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "web.client.speaking" {
			logged = append(logged, string(ev.Payload))
		}
	}
	if len(logged) != 2 {
		t.Fatalf("web.client.speaking = %v", logged)
	}
}

// TestWSCutTranscriptReachesTheBrainNotThePage: a cut transcript (the
// client flushed a reply part-way) corrects the brain's "Assistant said"
// line and is not shown to the page as a caption.
func TestWSCutTranscriptReachesTheBrainNotThePage(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, brain: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	full := "one two three four five six seven eight nine ten"
	h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: full})
	h.session.emit(voice.EventTurnComplete{})
	h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: "one two three four", Final: true,
		Cut: &voice.TranscriptCut{HeardMs: 4000, RunMs: 10000, Full: full}})
	h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: "next"})

	var captions []string
	for len(captions) < 2 {
		m := c.expect(wsproto.TypeTranscript)
		captions = append(captions, m.Text)
	}
	if captions[0] != full || captions[1] != "next" {
		t.Fatalf("captions = %q, want the reply then the next line (no cut caption)", captions)
	}

	var cut *brain.Message
	waitFor(t, 2*time.Second, func() bool {
		for _, m := range h.brain.Transcript().Messages() {
			if m.Kind == brain.KindAssistantSaid && m.Cut {
				mm := m
				cut = &mm
				return true
			}
		}
		return false
	}, "cut line in the brain transcript")
	if cut.Content != "one two three four" || cut.CutAfterMs != 4000 {
		t.Fatalf("cut line = %+v", cut)
	}
	inWindow := map[int]bool{}
	for _, w := range h.brain.Window() {
		inWindow[w.Seq] = true
	}
	for _, m := range h.brain.Transcript().Messages() {
		if m.Kind == brain.KindAssistantSaid && !m.Cut && m.Content == full && inWindow[m.Seq] {
			t.Fatalf("the full line (seq %d) is still in the window", m.Seq)
		}
	}
}

// TestWSCancelPushIsTaggedStop: the stop button's reply carries the stop
// tag, the exemption that keeps it INTERRUPT while the model or the
// client's playback counts as speaking.
func TestWSCancelPushIsTaggedStop(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.Cancel())
	c.expect(wsproto.TypeFlush)
	waitFor(t, 2*time.Second, func() bool { return len(h.session.taggedPushes()) == 1 }, "stop result pushed with tags")
	if tags := h.session.taggedPushes()[0]; tags["stop"] != true {
		t.Fatalf("tags = %v", tags)
	}
}
