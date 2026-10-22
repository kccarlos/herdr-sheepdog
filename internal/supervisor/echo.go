package supervisor

import (
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Possible echo. On 2026-09-24 18:23:50 the iOS speaker played "No
// change, all four are idle…", the mic picked it up and Gemini
// transcribed "new change" as the user's words; on speakerphone the
// bot's own speech came back often (docs/dev/bench/tier2-2026-10-03.md #3).
// The client may have no echo cancellation and the voice model reports
// only text, so the daemon cannot tell the two apart for sure, and it no
// longer tries to decide (dual-brain, #122): an input that began while
// the voice was speaking (or the client still playing it) or within
// echoWindow of the end of its playback carries what was played then
// (saidBefore), and handleUtterance hands both to the orchestrator as a fact
// (echoNote, logged as supervisor.echo_possible). The orchestrator judges
// whether it was the user; words that were the line coming back
// authorise nothing.

// echoWindow is how long after the voice's last output a new input may
// still be its echo. The logged pair was 1.7 s apart: the transcript of
// the voice's speech runs ahead of the audio the phone plays, and the
// input transcription arrives after the echoed audio ends.
//
// The window is anchored to the end of playback when the client reports
// it (docs/dev/ws-contract.md "Playback reports": the web page, and the iOS
// app since herdr-sheepdog-ios #14): Gemini generates ~4x real time, so
// its turnComplete comes seconds before a long reply's tail is played,
// and an echo of that tail used to arrive after a window anchored at
// turnComplete had closed (docs/dev/timelines.md C5). While the client says
// the reply is still playing (started, no drained yet) the voice counts
// as speaking, for up to echoPlayingMax after it stopped generating. A
// client without reports keeps the anchor at turnComplete.
const echoWindow = 2500 * time.Millisecond

// echoPlayingMax bounds how long a "started" report with no drained after
// it keeps the voice counted as speaking (a lost report must not keep
// the guard on forever): a 30 s reply generated at 4x plays ~22 s past
// its turnComplete.
const echoPlayingMax = 30 * time.Second

// echoKeepRuns is how many earlier speech runs the guard remembers.
const echoKeepRuns = 3

// echoWatch is what the voice is saying or last said, under s.mu.
type echoWatch struct {
	// said is the voice's output transcript since it last started
	// speaking; speaking is true until its turn completes or is
	// interrupted; at is when it last spoke (or stopped speaking);
	// since is when the previous speech stopped (a playback report
	// after it is about this speech).
	said     string
	speaking bool
	at       time.Time
	since    time.Time
	// start is when the current run began; prev are earlier runs,
	// oldest first.
	start time.Time
	prev  []echoRun
}

// echoRun is an earlier speech run and when it ended (its playback end
// when the client reported one).
type echoRun struct {
	said       string
	start, end time.Time
}

// echoSpokeLocked records a fragment of the voice's speech. Caller holds
// s.mu.
func (s *Supervisor) echoSpokeLocked(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	now := s.cfg.Now()
	if !s.echo.speaking {
		// A playback report after the previous speech ended is about this
		// one: the client's "started" often comes back before the first
		// output transcript fragment does.
		if said := strings.TrimSpace(s.echo.said); said != "" {
			s.echo.prev = append(s.echo.prev, echoRun{said: said, start: s.echo.start, end: s.echo.at})
			if len(s.echo.prev) > echoKeepRuns {
				s.echo.prev = s.echo.prev[len(s.echo.prev)-echoKeepRuns:]
			}
		}
		s.echo.said, s.echo.since, s.echo.start = "", s.echo.at, now
	}
	s.echo.said += text
	s.echo.speaking, s.echo.at = true, now
}

// voiceStopped records that the voice's turn completed or was
// interrupted: the window for an echo of it starts now.
func (s *Supervisor) voiceStopped() {
	s.mu.Lock()
	if s.echo.speaking {
		s.echo.speaking, s.echo.at = false, s.cfg.Now()
	}
	s.mu.Unlock()
}

// echoCandidateLocked is what a voice input starting now may be an echo
// of: the voice's speech while it speaks (or the client still plays it)
// or within echoWindow after, with the earlier runs that ended within
// echoWindow before it, else "". playback is the client's last playback
// report (zero: none). Caller holds s.mu.
func (s *Supervisor) echoCandidateLocked(playback voice.PlaybackReport, onset time.Time) string {
	now := s.cfg.Now()
	if s.spokeAfterPlaybackLocked(now, playback, onset) {
		return ""
	}
	cur := s.currentEchoLocked(now, playback)
	// Earlier runs: those that ended within echoWindow, and — while the
	// current run may be echoed — the ones chained to it by gaps under
	// echoWindow (the tail of one reply plays on as the next begins).
	var parts []string
	next := now
	if cur != "" {
		next = s.echo.start
	}
	for i := len(s.echo.prev) - 1; i >= 0; i-- {
		r := s.echo.prev[i]
		if now.Sub(r.end) > echoWindow && (cur == "" || next.Sub(r.end) > echoWindow) {
			break
		}
		parts = append([]string{r.said}, parts...)
		next = r.start
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return strings.Join(parts, " ")
}

// echoTail is how long after the end of playback an echo can still
// begin: the speaker-to-microphone delay and the client detector's
// attack.
const echoTail = 300 * time.Millisecond

// spokeAfterPlaybackLocked reports whether the client's speech detector
// opened (onset, zero when the client reports none) after the voice had
// stopped and the client had finished playing it, echoTail allowed: an
// echo begins while the speaker plays, so words begun after it are the
// user's — even when they repeat the voice ("fix those two links"
// to "Shall I fix those two links?"). Caller holds s.mu.
func (s *Supervisor) spokeAfterPlaybackLocked(now time.Time, playback voice.PlaybackReport, onset time.Time) bool {
	if onset.IsZero() || s.echo.speaking || s.echo.said == "" {
		return false
	}
	end := s.echo.at
	if playback.State != "" && playback.At.After(s.echo.since) {
		switch playback.State {
		case voice.PlaybackStarted:
			if now.Sub(s.echo.at) <= echoPlayingMax {
				return false // still playing
			}
		case voice.PlaybackDrained, voice.PlaybackFlushed:
			if playback.At.After(end) {
				end = playback.At
			}
		}
	}
	return onset.After(end.Add(echoTail))
}

// lastSpeechOnset is when the client's speech detector last opened, when
// the voice session knows (voice.Switcher).
func (s *Supervisor) lastSpeechOnset() (time.Time, bool) {
	if p, ok := s.pusher.(interface{ LastSpeechOnset() (time.Time, bool) }); ok {
		return p.LastSpeechOnset()
	}
	return time.Time{}, false
}

// currentEchoLocked is the current (or last) speech run when an input
// starting now may be its echo, else "". Caller holds s.mu.
func (s *Supervisor) currentEchoLocked(now time.Time, playback voice.PlaybackReport) string {
	if s.echo.said == "" {
		return ""
	}
	said := strings.TrimSpace(s.echo.said)
	if s.echo.speaking {
		return said
	}
	anchor := s.echo.at
	if playback.State != "" && playback.At.After(s.echo.since) {
		switch playback.State {
		case voice.PlaybackStarted:
			if now.Sub(s.echo.at) <= echoPlayingMax {
				return said // the user is still hearing it
			}
		case voice.PlaybackDrained, voice.PlaybackFlushed:
			if playback.At.After(anchor) {
				anchor = playback.At
			}
		}
	}
	if now.Sub(anchor) > echoWindow {
		return ""
	}
	return said
}

// lastPlayback is the voice client's last playback report, when the
// voice session keeps them (voice.Switcher); zero otherwise.
func (s *Supervisor) lastPlayback() voice.PlaybackReport {
	if p, ok := s.pusher.(interface{ LastPlayback() voice.PlaybackReport }); ok {
		return p.LastPlayback()
	}
	return voice.PlaybackReport{}
}

// echoNote is the fact the orchestrator gets instead of the old echo text
// guard (dual-brain ablation G6): which of c's words began while the
// phone was still playing the voice's own line, or within echoWindow of
// its end, and what that line was. Timing is physics; whether the words
// are the user or the line coming back through the speaker is the
// orchestrator's to judge. "" when none did.
func echoNote(c claim) string {
	ins := c.ins
	if len(ins) == 0 && c.in != nil {
		ins = []*shepherdInput{c.in}
	}
	for _, in := range ins {
		said := strings.TrimSpace(in.saidBefore)
		if in.kind != InputVoice || said == "" {
			continue
		}
		if r := []rune(said); len(r) > 240 {
			said = "…" + string(r[len(r)-240:])
		}
		return fmt.Sprintf("Daemon fact: the words %q began while the phone was still playing the voice's own line, or just after it: %q. On a speakerphone they may be that line coming back through the microphone rather than the user; they authorise nothing if so.", strings.TrimSpace(in.text), said)
	}
	return ""
}
