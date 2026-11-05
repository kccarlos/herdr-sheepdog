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
// client flushed a reply part-way) corrects the orchestrator's "Assistant said"
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
		if m.Text == "" && m.Final != nil && *m.Final {
			continue // the turn's end closing the open reply
		}
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

func TestWSInFlightAudioDoesNotCrossFlush(t *testing.T) {
	t.Run("in_flight_pre_interrupt_chunk_after_event_interrupted", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)

		ep1 := h.session.currentEpoch()
		flushed := h.session.FlushAudio()
		h.session.emit(voice.EventInterrupted{Epoch: flushed})
		c.expect(wsproto.TypeFlush)

		// Simulate Server.Run / Switcher delivering an already-popped ep1 chunk
		// after EventInterrupted flushed the client, followed by a fresh ep2 chunk.
		h.session.emitChunk(voice.AudioChunk{Data: []byte("stale-ep1"), Epoch: ep1})
		h.session.pushAudio([]byte("fresh-ep2"))

		var got []byte
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read audio: %v", err)
			}
			if bin != nil {
				got = bin
				break
			}
		}
		if string(got) != "fresh-ep2" {
			t.Fatalf("first binary frame after flush = %q, want %q (pre-flush chunk crossed flush)", got, "fresh-ep2")
		}
	})

	t.Run("cancel_purges_session_audio_before_flush", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)

		// Hold the session's outbound pump so pre-cancel audio is guaranteed to
		// be both in-flight in the pump and queued in rawChunks when Cancel() runs.
		h.session.holdPump(true)
		h.session.pushAudio([]byte("pre-cancel"))
		h.session.pushAudio([]byte("pre-cancel"))
		c.send(wsproto.Cancel())
		c.expect(wsproto.TypeFlush)
		h.session.holdPump(false)

		h.session.pushAudio([]byte("post-cancel"))

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read after cancel flush: %v", err)
			}
			if bin == nil {
				continue
			}
			if string(bin) == "pre-cancel" {
				t.Fatalf("received pre-cancel audio frame %q after flush", bin)
			}
			if string(bin) == "post-cancel" {
				break
			}
		}
	})

	t.Run("replaced_client_playback_report_ignored", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c1 := dialWS(t, h)
		c1.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.srv.hub.current() != nil }, "c1 attached")
		oldClient := h.srv.hub.current()

		c2 := dialWS(t, h)
		c2.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool {
			cur := h.srv.hub.current()
			return cur != nil && cur != oldClient
		}, "c2 replaced c1")

		// Simulate oldClient's readLoop delivering an already-read playback frame
		// right after c2 replaced oldClient.
		h.srv.hub.setPlayback(oldClient, wsproto.PlaybackFlushed, 0)
		if got := h.session.playbackReports(); len(got) != 0 {
			t.Fatalf("replaced client's playback report reached session: %v", got)
		}

		c2.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		waitFor(t, time.Second, func() bool { return len(h.session.playbackReports()) == 1 }, "c2 playback report")
		if got := h.session.playbackReports(); got[0] != "started:0" {
			t.Fatalf("c2 reports = %v, want [started:0]", got)
		}
	})
}

func TestWSNewEpochAudioOrdersAfterFlushAndSurvivesLateInterrupted(t *testing.T) {
	t.Run("new_epoch_audio_arriving_before_event_interrupted_waits_behind_flush", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)

		// Epoch 1 audio reaches the client.
		h.session.pushAudio([]byte("ep1-audio"))
		deadline := time.Now().Add(2 * time.Second)
		for {
			_, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read ep1 audio: %v", err)
			}
			if bin != nil {
				if string(bin) != "ep1-audio" {
					t.Fatalf("got %q, want ep1-audio", bin)
				}
				break
			}
		}

		// Session flushes Epoch 1 and immediately starts streaming Epoch 2 audio,
		// which reaches Server.Run -> hub.sendAudioChunk BEFORE EventInterrupted
		// travels through the event loop to srv.OnVoiceEvent.
		flushedEp := h.session.FlushAudio()
		h.session.pushAudio([]byte("ep2-first"))
		time.Sleep(30 * time.Millisecond)
		h.session.emit(voice.EventInterrupted{Epoch: flushedEp})
		h.session.pushAudio([]byte("ep2-second"))

		var wire []string
		deadline = time.Now().Add(2 * time.Second)
		for len(wire) < 3 && time.Now().Before(deadline) {
			msg, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read after flush (got %v so far): %v", wire, err)
			}
			if bin != nil {
				wire = append(wire, "bin:"+string(bin))
				continue
			}
			if msg.Type == wsproto.TypeFlush {
				wire = append(wire, "flush")
			}
		}
		want := []string{"flush", "bin:ep2-first", "bin:ep2-second"}
		if strings.Join(wire, ",") != strings.Join(want, ",") {
			t.Fatalf("wire sequence = %v, want %v (new audio overtook flush or was dropped)", wire, want)
		}
	})

	t.Run("client_flush_preserves_newer_epoch_queued_frames", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.srv.hub.current() != nil }, "client attached")
		cl := h.srv.hub.current()

		ep1 := voice.NextAudioEpoch()
		ep2 := voice.NextAudioEpoch()

		// Lock writeLoop from draining while we stage both epochs in cl.queue,
		// then run cl.flush(ep1, flush).
		cl.mu.Lock()
		_, _ = cl.enqueueLocked(frame{binary: true, data: []byte("stale-ep1"), epoch: ep1})
		_, _ = cl.enqueueLocked(frame{binary: true, data: []byte("fresh-ep2"), epoch: ep2})
		cl.mu.Unlock()

		dropped, sent := cl.flush(ep1, controlFrame(wsproto.Flush()))
		if !sent || dropped != 1 {
			t.Fatalf("cl.flush(ep1) = (dropped=%d, sent=%v), want (1, true)", dropped, sent)
		}

		var wire []string
		deadline := time.Now().Add(2 * time.Second)
		for len(wire) < 2 && time.Now().Before(deadline) {
			msg, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read after cl.flush (got %v): %v", wire, err)
			}
			if bin != nil {
				wire = append(wire, "bin:"+string(bin))
			} else if msg.Type == wsproto.TypeFlush {
				wire = append(wire, "flush")
			}
		}
		want := []string{"flush", "bin:fresh-ep2"}
		if strings.Join(wire, ",") != strings.Join(want, ",") {
			t.Fatalf("wire sequence = %v, want %v", wire, want)
		}
	})
}

func TestWSStopSwitchAndUnderrunPlaybackEndToEnd(t *testing.T) {
	const bytesPerSec24k = 48000

	t.Run("stop_sequence_two_daemon_flushes_preserve_stop_reply", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true, brain: true, trackPlayback: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.session.isAttached() }, "client attached")

		firstFull := "one two three four five six seven eight"
		h.session.pushTrackedSpeech(make([]byte, 4*bytesPerSec24k), firstFull)
		h.session.endTrackedTurn()

		deadline := time.Now().Add(2 * time.Second)
		for {
			_, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read first reply audio: %v", err)
			}
			if bin != nil {
				break
			}
		}

		// User taps Stop after hearing 2 s of the 4 s reply: the client flushes
		// its local player (played_ms: 2000) and sends cancel.
		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackFlushed, 2000))
		c.send(wsproto.Cancel())

		waitFor(t, 2*time.Second, func() bool { return len(h.session.taggedPushes()) == 1 }, "stop push delivered")

		stopReply := "Stopped everything for you."
		h.session.pushTrackedSpeech(make([]byte, 2*bytesPerSec24k), stopReply)
		h.session.endTrackedTurn()

		// Read until we have both daemon flushes and the stop reply's binary audio.
		var wire []string
		flushes := 0
		gotStopAudio := false
		deadline = time.Now().Add(2 * time.Second)
		for (!gotStopAudio || flushes < 2) && time.Now().Before(deadline) {
			msg, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read stop sequence (got %v): %v", wire, err)
			}
			if bin != nil {
				wire = append(wire, "audio")
				gotStopAudio = true
				continue
			}
			if msg.Type == wsproto.TypeFlush {
				wire = append(wire, "flush")
				flushes++
				// Every real browser/iOS client replies to each daemon flush
				// over an empty buffer with {state: "flushed", played_ms: 0}.
				c.send(wsproto.Playback(wsproto.PlaybackFlushed, 0))
			}
		}
		wantWire := []string{"flush", "flush", "audio"}
		if strings.Join(wire, ",") != strings.Join(wantWire, ",") {
			t.Fatalf("wire sequence = %v, want %v", wire, wantWire)
		}

		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackDrained, 2000))
		waitFor(t, 2*time.Second, func() bool { return len(h.session.playbackReports()) == 6 }, "all 6 playback reports processed")

		waitFor(t, 2*time.Second, func() bool {
			for _, m := range h.brain.Transcript().Messages() {
				if m.Kind == brain.KindAssistantSaid && !m.Cut && m.Content == stopReply {
					return true
				}
			}
			return false
		}, "uncut stop reply in brain transcript")

		inWindow := map[int]bool{}
		for _, w := range h.brain.Window() {
			inWindow[w.Seq] = true
		}
		var sawCutFirst, sawUncutStop bool
		for _, m := range h.brain.Transcript().Messages() {
			if m.Kind != brain.KindAssistantSaid {
				continue
			}
			if !m.Cut && m.Content == firstFull && inWindow[m.Seq] {
				t.Fatalf("full uncut first reply still in brain window: %+v", m)
			}
			if m.Cut && m.Content == "one two three four" && m.CutAfterMs == 2000 && inWindow[m.Seq] {
				sawCutFirst = true
			}
			if !m.Cut && m.Content == stopReply && inWindow[m.Seq] {
				sawUncutStop = true
			}
			if m.Cut && strings.Contains(m.Content, "Stopped") {
				t.Fatalf("stop reply was falsely cut in brain transcript: %+v", m)
			}
		}
		if !sawCutFirst || !sawUncutStop {
			t.Fatalf("brain messages = %+v (window = %+v), want cut first reply + uncut stop reply in window", h.brain.Transcript().Messages(), h.brain.Window())
		}
		entries := h.session.recapEntries()
		if len(entries) != 2 || entries[0].Text != "one two three four" || entries[1].Text != stopReply {
			t.Fatalf("session recap = %+v, want [one two three four, %s]", entries, stopReply)
		}
	})

	t.Run("stop_while_streaming_does_not_emit_extra_daemon_flush", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true, brain: true, trackPlayback: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.session.isAttached() }, "client attached")

		firstFull := "one two three four five six seven eight"
		h.session.pushTrackedSpeech(make([]byte, 4*bytesPerSec24k), firstFull)

		deadline := time.Now().Add(2 * time.Second)
		for {
			_, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read first reply audio: %v", err)
			}
			if bin != nil {
				break
			}
		}

		// User taps Stop while the model is still streaming: client flushes
		// locally first, then another model audio chunk arrives before cancel
		// finishes stopping the agents and runs afterCancel.
		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackFlushed, 2000))
		waitFor(t, time.Second, func() bool { return len(h.session.playbackReports()) == 2 }, "client flush processed")

		h.session.pushTrackedSpeech(make([]byte, bytesPerSec24k), " nine ten")
		time.Sleep(30 * time.Millisecond)

		c.send(wsproto.Cancel())
		waitFor(t, 2*time.Second, func() bool { return len(h.session.taggedPushes()) == 1 }, "stop push delivered")

		stopReply := "Stopped everything for you."
		h.session.pushTrackedSpeech(make([]byte, 2*bytesPerSec24k), stopReply)
		h.session.endTrackedTurn()

		flushes := 0
		gotPostStopAudio := false
		deadline = time.Now().Add(2 * time.Second)
		for (!gotPostStopAudio || flushes < 2) && time.Now().Before(deadline) {
			msg, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read stop sequence (flushes=%d): %v", flushes, err)
			}
			if bin != nil {
				if flushes >= 2 {
					gotPostStopAudio = true
				}
				continue
			}
			if msg.Type == wsproto.TypeFlush {
				flushes++
				c.send(wsproto.Playback(wsproto.PlaybackFlushed, 0))
			}
		}
		if flushes != 2 {
			t.Fatalf("daemon flushes = %d, want 2 (client flush created an untracked daemon flush)", flushes)
		}

		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackDrained, 2000))
		waitFor(t, 2*time.Second, func() bool { return len(h.session.playbackReports()) == 6 }, "all playback reports processed")

		entries := h.session.recapEntries()
		if len(entries) == 0 || entries[len(entries)-1].Text != stopReply || entries[len(entries)-1].Cut {
			t.Fatalf("session recap = %+v, want intact trailing %q", entries, stopReply)
		}
	})

	t.Run("provider_switch_flush_ack_preserves_new_provider_reply", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true, brain: true, switcher: true, trackPlayback: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.session.isAttached() }, "client attached to gemini")

		code, body := h.sendJSON("PUT", "/api/voice/provider", map[string]string{"provider": "openai"}, nil)
		if code != 200 {
			t.Fatalf("PUT /api/voice/provider = %d: %s", code, body)
		}

		reply := "Hello from GPT-Live."
		h.session2.pushTrackedSpeech(make([]byte, 2*bytesPerSec24k), reply)
		h.session2.endTrackedTurn()

		var wire []string
		deadline := time.Now().Add(2 * time.Second)
		for len(wire) < 2 && time.Now().Before(deadline) {
			msg, bin, err := c.read(time.Until(deadline))
			if err != nil {
				t.Fatalf("read after provider switch (got %v): %v", wire, err)
			}
			if bin != nil {
				wire = append(wire, "audio")
				continue
			}
			if msg.Type == wsproto.TypeFlush {
				wire = append(wire, "flush")
				c.send(wsproto.Playback(wsproto.PlaybackFlushed, 0))
			}
		}
		if strings.Join(wire, ",") != "flush,audio" {
			t.Fatalf("wire sequence = %v, want [flush audio]", wire)
		}
		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackDrained, 2000))
		waitFor(t, 2*time.Second, func() bool { return len(h.session2.playbackReports()) == 3 }, "session2 playback reports")

		waitFor(t, 2*time.Second, func() bool {
			inWindow := map[int]bool{}
			for _, w := range h.brain.Window() {
				inWindow[w.Seq] = true
			}
			for _, m := range h.brain.Transcript().Messages() {
				if m.Kind == brain.KindAssistantSaid && !m.Cut && m.Content == reply && inWindow[m.Seq] {
					return true
				}
			}
			return false
		}, "uncut GPT-Live reply in brain window")

		for _, m := range h.brain.Transcript().Messages() {
			if m.Kind == brain.KindAssistantSaid && m.Cut {
				t.Fatalf("new provider reply was falsely cut by switch flush ack: %+v", m)
			}
		}
	})

	t.Run("underrun_drain_then_cut_retains_heard_first_sentence_in_brain", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true, brain: true, trackPlayback: true})
		c := dialWS(t, h)
		c.expect(wsproto.TypeStatus)
		waitFor(t, time.Second, func() bool { return h.session.isAttached() }, "client attached")

		part1 := "First sentence heard. "
		part2 := "Second sentence cut."
		full := "First sentence heard. Second sentence cut."

		// Part 1 streams and the client underruns (drains) at 2000 ms mid-turn.
		h.session.pushTrackedSpeech(make([]byte, 2*bytesPerSec24k), part1)
		c.send(wsproto.Playback(wsproto.PlaybackStarted, 0))
		c.send(wsproto.Playback(wsproto.PlaybackDrained, 2000))
		waitFor(t, time.Second, func() bool { return len(h.session.playbackReports()) == 2 }, "mid-turn drain processed")

		// Part 2 of the same turn streams and the turn completes before the
		// client's barge-in flush arrives.
		h.session.pushTrackedSpeech(make([]byte, 2*bytesPerSec24k), part2)
		h.session.endTrackedTurn()
		waitFor(t, 2*time.Second, func() bool {
			for _, m := range h.brain.Transcript().Messages() {
				if m.Kind == brain.KindAssistantSaid && m.Content == full {
					return true
				}
			}
			return false
		}, "full turn recorded in brain before cut")

		// Client resumes at played_ms: 2000 and immediately cuts with played_ms: 0
		// into the resumed sub-run.
		c.send(wsproto.Playback(wsproto.PlaybackStarted, 2000))
		c.send(wsproto.Playback(wsproto.PlaybackFlushed, 0))

		var cutMsg *brain.Message
		waitFor(t, 2*time.Second, func() bool {
			for _, m := range h.brain.Transcript().Messages() {
				if m.Kind == brain.KindAssistantSaid && m.Cut {
					mm := m
					cutMsg = &mm
					return true
				}
			}
			return false
		}, "cut message in brain transcript")

		if cutMsg.Content != "First sentence heard." || cutMsg.CutAfterMs != 2000 {
			t.Fatalf("cut message = %+v, want Content=%q CutAfterMs=2000", cutMsg, "First sentence heard.")
		}
		inWindow := map[int]bool{}
		for _, w := range h.brain.Window() {
			inWindow[w.Seq] = true
		}
		if !inWindow[cutMsg.Seq] {
			t.Fatalf("cut message (seq %d) missing from brain window", cutMsg.Seq)
		}
		for _, m := range h.brain.Transcript().Messages() {
			if m.Kind == brain.KindAssistantSaid && !m.Cut && m.Content == full && inWindow[m.Seq] {
				t.Fatalf("full uncut turn still in brain window after cut: %+v", m)
			}
		}
	})
}
