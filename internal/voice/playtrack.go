package voice

import (
	"strings"
	"time"
)

// Playback truth (docs/dev/ws-contract.md "Playback reports"): the /ws client
// reports when its player starts, drains and flushes, and how much of the
// spoken run it actually played. The sessions use that to keep "the
// model is speaking" true until the user has heard the end of the
// reply (Gemini generates ~4× real time, so its turnComplete arrives
// seconds before the speech ends), to measure the push gap from the real
// end of speech, and to tell the orchestrator which part of a cut-off reply was
// heard. A client that never reports (an old page, voice-test) gets a
// timing guess instead: audio delivered minus wall time since it began.

// Client playback states (wsproto "playback" message).
const (
	PlaybackStarted = "started"
	PlaybackDrained = "drained"
	PlaybackFlushed = "flushed"
)

const (
	// PlaybackGapHold is how long a reply still counts as playing after
	// the client reported drained: an underrun gap or the next chunk of
	// the same reply resumes within it.
	PlaybackGapHold = 300 * time.Millisecond
	// PlaybackReportSlack bounds a run the client said started but never
	// said ended (a lost message, a client gone quiet): it stops counting
	// as playing this long after the estimated end of the audio sent.
	PlaybackReportSlack = 5 * time.Second
	// ClientSpeechRelease is how long the user still counts as
	// speaking after the client's speech detector closed.
	ClientSpeechRelease = 300 * time.Millisecond
	// ClientSpeechTrust bounds how long an open client speech detector
	// holds pushes on its own. The web page's detector is its silence
	// gate, which any sound above the noise floor keeps open, so an open
	// gate past this counts only while input transcripts corroborate it.
	ClientSpeechTrust = 8 * time.Second
	// cutTolerance: a flushed run played to within this of its end was
	// heard whole (no cut).
	cutTolerance = 150 * time.Millisecond
	// maxAwaiting bounds the flushed runs waiting for a client report,
	// and awaitingTTL how long one waits: a client that never answers a
	// flush (no audio context yet) must not have a later flushed report
	// matched to a stale run.
	maxAwaiting = 4
	awaitingTTL = 10 * time.Second
)

// outBytesPerMs is the /ws output format's byte rate: 24 kHz PCM16 mono.
const outBytesPerMs = 48

// voicedBlockBytes (10 ms at 24 kHz PCM16) and voicedPeakMin classify
// output PCM into voiced speech vs pauses so silence does not skew
// word attribution when a reply is cut.
const (
	voicedBlockBytes = 10 * outBytesPerMs
	voicedPeakMin    = 200
)

// TranscriptCut marks an EventTranscript that corrects the model's
// preceding output speech: the client flushed a spoken run before
// playing all of it (a barge-in, the stop button, an interrupting push).
// The event's Text is the part the user heard (possibly empty).
type TranscriptCut struct {
	// HeardMs is how much of the run the client played.
	HeardMs int
	// RunMs is how much audio the daemon sent in the run.
	RunMs int
	// Full is everything the model's output transcript said in the run.
	Full string
	// UtteranceID is the utterance whose reply the run played (the run's
	// start was tagged with it, PlaybackUtterance), "" for any other line.
	UtteranceID string
	// ByPush: the daemon flushed the run to make way for an urgent push,
	// rather than the user talking over it.
	ByPush bool
}

// PlaybackReport is one client playback report as the session saw it
// (voice.playback): RunMs is the audio the daemon sent in that run.
type PlaybackReport struct {
	State    string
	PlayedMs int
	RunMs    int
	RunID    uint32
	At       time.Time
	// Cut is set when a flushed run was cut short.
	Cut *TranscriptCut
	// Heard is the heard part of a cut run.
	Heard string
}

// PlaybackUtterance tags the next playback start with the utterance the
// session last handed to the model to speak (a reply push or a spoken
// tool result), so turn.latency matches that start to the reply by id.
// Not safe for concurrent use: the session calls it under its own lock.
type PlaybackUtterance struct{ id, run string }

// Handed records what the model was just given to speak: an utterance's
// reply (id), or anything else ("": the next start is not a reply).
func (p *PlaybackUtterance) Handed(id string) { p.id = id }

// Started returns the utterance to tag a playback report in state with,
// once: only a start is tagged, and only the first after a hand-over.
func (p *PlaybackUtterance) Started(state string) string {
	if state != PlaybackStarted {
		return ""
	}
	id := p.id
	p.id = ""
	p.run = id
	return id
}

// TagPayload adds utterance_id to a voice.playback payload when id is set.
func TagPayload(payload map[string]any, id string) map[string]any {
	if id != "" {
		payload["utterance_id"] = id
	}
	return payload
}

// Run is the utterance whose reply the latest started run plays ("" when
// that run is not a reply).
func (p *PlaybackUtterance) Run() string { return p.run }

// Payload is the voice.playback event payload.
func (r PlaybackReport) Payload() map[string]any {
	p := map[string]any{"state": r.State, "played_ms": r.PlayedMs, "run_ms": r.RunMs}
	if r.RunID != 0 {
		p["run_id"] = r.RunID
	}
	if r.Cut != nil {
		p["cut"] = true
		p["heard"] = r.Heard
		p["said"] = r.Cut.Full
	}
	return p
}

// spokenFrag is one output transcript fragment and the run's byte and
// voiced-byte counts when it arrived.
type spokenFrag struct {
	text     string
	at       int
	voicedAt int
}

// playSeg maps cumulative sent bytes to cumulative voiced bytes across
// contiguous voiced (slope 1) or silent (slope 0) runs.
type playSeg struct {
	sent   int
	voiced int
}

// playRun is the audio and output transcript sent since the last
// boundary (the client's drained, a flush).
type playRun struct {
	id     uint32
	sent   int
	voiced int
	segs   []playSeg
	frags  []spokenFrag
	// flushedAt is when the daemon flushed the run (awaiting runs).
	flushedAt time.Time
}

func (r playRun) empty() bool { return r.sent == 0 && len(r.frags) == 0 }

func (r *playRun) addBytes(n int, voiced bool) {
	if n <= 0 {
		return
	}
	r.sent += n
	if voiced {
		r.voiced += n
	}
	if k := len(r.segs); k > 0 {
		v0 := 0
		if k > 1 {
			v0 = r.segs[k-2].voiced
		}
		prevVoiced := r.segs[k-1].voiced > v0
		if prevVoiced == voiced {
			r.segs[k-1] = playSeg{sent: r.sent, voiced: r.voiced}
			return
		}
	}
	r.segs = append(r.segs, playSeg{sent: r.sent, voiced: r.voiced})
}

func (r playRun) voicedAt(n int) int {
	if n <= 0 {
		return 0
	}
	if n >= r.sent {
		return r.voiced
	}
	s0, v0 := 0, 0
	for _, seg := range r.segs {
		if n <= seg.sent {
			if seg.voiced == v0 {
				return v0
			}
			return v0 + (n - s0)
		}
		s0, v0 = seg.sent, seg.voiced
	}
	return r.voiced
}

func pcmBlockPeak(pcm []byte) int {
	max := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		if v < 0 {
			v = -v
		}
		if v > max {
			max = v
		}
	}
	return max
}

// PlaybackTracker follows what the /ws client plays for one session. It
// is not safe for concurrent use: the session calls it under its own
// lock.
type PlaybackTracker struct {
	run playRun
	// awaiting are runs the daemon flushed (an interrupt), waiting for
	// the client's flushed report to say how much of each was heard.
	awaiting []playRun
	// reports: the attached client sends playback messages.
	reports      bool
	playing      bool
	playingSince time.Time
	holdUntil    time.Time
	endedAt      time.Time
	// est is the estimated wall-clock end of playing the audio sent so
	// far (the fallback, and the bound on a report-less started run).
	est  time.Time
	last PlaybackReport
}

// Sent records n bytes of output audio queued for the client (treated as
// voiced when raw PCM samples are not provided).
func (p *PlaybackTracker) Sent(now time.Time, n int) {
	if n <= 0 {
		return
	}
	p.run.addBytes(n, true)
	p.advanceEst(now, n)
}

// SentPCM records output PCM16 (24 kHz mono) queued for the client,
// tracking voiced 10 ms blocks separately from silent pauses so word
// checkpoints follow voiced speech.
func (p *PlaybackTracker) SentPCM(now time.Time, pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	for off := 0; off < len(pcm); off += voicedBlockBytes {
		end := off + voicedBlockBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		p.run.addBytes(end-off, pcmBlockPeak(pcm[off:end]) >= voicedPeakMin)
	}
	p.advanceEst(now, len(pcm))
}

func (p *PlaybackTracker) advanceEst(now time.Time, n int) {
	if p.est.Before(now) {
		p.est = now
	}
	p.est = p.est.Add(time.Duration(n) * time.Millisecond / outBytesPerMs)
}

// Reports reports whether the attached client has sent playback messages.
func (p *PlaybackTracker) Reports() bool { return p.reports }

// SetRunID tags the current run with a monotonic run_id so flushed and
// drained reports match their exact run instead of relying on arrival order.
func (p *PlaybackTracker) SetRunID(id uint32) {
	if id != 0 {
		p.run.id = id
	}
}

// RunID returns the current run's id (0 when untagged).
func (p *PlaybackTracker) RunID() uint32 { return p.run.id }

// AssignAwaitingRunID stamps the latest untagged awaiting (or active) run
// with next() when a flush raced ahead of outbound audio delivery.
func (p *PlaybackTracker) AssignAwaitingRunID(next func() uint32) uint32 {
	if n := len(p.awaiting); n > 0 && p.awaiting[n-1].id == 0 {
		id := next()
		p.awaiting[n-1].id = id
		return id
	}
	if !p.run.empty() && p.run.id == 0 {
		id := next()
		p.run.id = id
		return id
	}
	return 0
}

// Said records an output transcript fragment at the run's current byte
// and voiced-byte counts: the fragment describes the speech up to here.
func (p *PlaybackTracker) Said(text string) {
	if text == "" {
		return
	}
	p.run.frags = append(p.run.frags, spokenFrag{text: text, at: p.run.sent, voicedAt: p.run.voiced})
}

// Flush records that the daemon dropped the output and told the client
// to flush (EventInterrupted): the current run waits for the client's
// flushed report, and nothing is playing any more. It reports whether
// a run now waits for that report (false: nothing had been sent).
func (p *PlaybackTracker) Flush(now time.Time) bool {
	_, awaited := p.FlushRun(now)
	return awaited
}

// FlushRun is Flush, also returning the flushed run's id (0 when untagged).
func (p *PlaybackTracker) FlushRun(now time.Time) (uint32, bool) {
	runID := p.run.id
	awaited := !p.run.empty()
	if awaited {
		p.run.flushedAt = now
		p.awaiting = append(p.awaiting, p.run)
		if len(p.awaiting) > maxAwaiting {
			p.awaiting = p.awaiting[len(p.awaiting)-maxAwaiting:]
		}
	}
	p.run = playRun{}
	p.playing = false
	p.holdUntil = time.Time{}
	p.est = time.Time{}
	p.endedAt = now
	return runID, awaited
}

// Detach forgets the client (it went away, or a new one replaced it):
// its runs, its reports and whether it reports at all.
func (p *PlaybackTracker) Detach() {
	*p = PlaybackTracker{last: p.last}
}

// Report applies an untagged client playback report and returns it as the
// session should log it; a flushed run that was cut short carries Cut.
func (p *PlaybackTracker) Report(now time.Time, state string, playedMs int) PlaybackReport {
	return p.ReportRun(now, state, playedMs, 0)
}

// ReportRun applies a client playback report optionally tagged with runID
// (0 falls back to FIFO matching for legacy clients). When runID != 0, a
// late or duplicate report for an already-settled run is ignored without
// touching a newer active run.
func (p *PlaybackTracker) ReportRun(now time.Time, state string, playedMs int, runID uint32) PlaybackReport {
	if playedMs < 0 {
		playedMs = 0
	}
	p.reports = true
	rep := PlaybackReport{State: state, PlayedMs: playedMs, RunID: runID, At: now}
	switch state {
	case PlaybackStarted:
		if runID != 0 && runID != p.run.id {
			p.last = rep
			return rep
		}
		if rep.RunID == 0 {
			rep.RunID = p.run.id
		}
		rep.RunMs = p.run.sent / outBytesPerMs
		if !p.playing {
			p.playingSince = now
		}
		p.playing = true
		p.holdUntil = time.Time{}
	case PlaybackDrained:
		if runID != 0 && runID != p.run.id {
			p.last = rep
			return rep
		}
		if rep.RunID == 0 {
			rep.RunID = p.run.id
		}
		rep.RunMs = p.run.sent / outBytesPerMs
		p.playing = false
		p.holdUntil = now.Add(PlaybackGapHold)
		p.endedAt = now
		p.run = p.run.advance(playedMs * outBytesPerMs)
	case PlaybackFlushed:
		for len(p.awaiting) > 0 && now.Sub(p.awaiting[0].flushedAt) > awaitingTTL {
			p.awaiting = p.awaiting[1:]
		}
		var r playRun
		matched := false
		if runID != 0 {
			for i := range p.awaiting {
				if p.awaiting[i].id == runID {
					r = p.awaiting[i]
					p.awaiting = p.awaiting[i+1:]
					matched = true
					break
				}
			}
			if !matched && p.run.id == runID {
				r, p.run = p.run, playRun{}
				p.est = time.Time{}
				matched = true
			}
			if !matched {
				p.last = rep
				return rep
			}
		} else {
			if len(p.awaiting) > 0 {
				r, p.awaiting = p.awaiting[0], p.awaiting[1:]
			} else {
				// The client flushed on its own (the stop button) or ahead
				// of the daemon's interrupt: the current run is what it cut.
				r, p.run = p.run, playRun{}
				p.est = time.Time{}
			}
			if rep.RunID == 0 {
				rep.RunID = r.id
			}
		}
		rep.RunMs = r.sent / outBytesPerMs
		if p.run.empty() {
			p.playing = false
			p.holdUntil = time.Time{}
			p.endedAt = now
		}
		if heard, full, cut := r.heard(playedMs * outBytesPerMs); cut {
			rep.Cut = &TranscriptCut{HeardMs: playedMs, RunMs: rep.RunMs, Full: full}
			rep.Heard = heard
		}
	}
	p.last = rep
	return rep
}

// Playing reports whether the client is still playing the model's audio
// and, when it is, how long until that should be checked again (0: wait
// for the next report, which pokes the caller).
func (p *PlaybackTracker) Playing(now time.Time) (bool, time.Duration) {
	if !p.reports {
		if now.Before(p.est) {
			return true, p.est.Sub(now)
		}
		return false, 0
	}
	if p.playing {
		limit := p.playingSince
		if p.est.After(limit) {
			limit = p.est
		}
		limit = limit.Add(PlaybackReportSlack)
		if now.Before(limit) {
			return true, limit.Sub(now)
		}
		return false, 0
	}
	if now.Before(p.holdUntil) {
		return true, p.holdUntil.Sub(now)
	}
	// Audio sent since the last boundary the client has not started
	// playing yet (its jitter buffer is filling): the timing guess.
	if p.run.sent > 0 && now.Before(p.est) {
		return true, p.est.Sub(now)
	}
	return false, 0
}

// EndedAt is when the client's playback last ended (drained or flushed),
// or, without reports, when the audio sent so far should have finished
// playing; zero when nothing has played.
func (p *PlaybackTracker) EndedAt(now time.Time) time.Time {
	if !p.reports && !p.est.IsZero() && !p.est.After(now) && p.est.After(p.endedAt) {
		return p.est
	}
	return p.endedAt
}

// Last is the most recent client report (zero State: none yet).
func (p *PlaybackTracker) Last() PlaybackReport { return p.last }

// advance drops the first n bytes (played and drained) from the run: the
// fragments they covered and the byte count; later fragments shift down.
func (r playRun) advance(n int) playRun {
	if n >= r.sent {
		return playRun{}
	}
	vDrop := r.voicedAt(n)
	out := playRun{id: r.id, sent: r.sent - n, voiced: r.voiced - vDrop}
	for _, seg := range r.segs {
		if seg.sent > n {
			out.segs = append(out.segs, playSeg{sent: seg.sent - n, voiced: seg.voiced - vDrop})
		}
	}
	for _, f := range r.frags {
		if f.at > n {
			va := f.voicedAt - vDrop
			if va < 0 {
				va = 0
			}
			out.frags = append(out.frags, spokenFrag{text: f.text, at: f.at - n, voicedAt: va})
		}
	}
	return out
}

// heard maps n played bytes onto the run's transcript: whole fragments
// that arrived by then, and a proportional share of the words of the one
// straddling it, using voiced-audio checkpoints when voiced PCM was seen
// (falling back to byte offsets for synthetic silent test buffers). cut
// is false when the run was heard (nearly) whole or said nothing.
func (r playRun) heard(n int) (heard, full string, cut bool) {
	var all strings.Builder
	for _, f := range r.frags {
		all.WriteString(f.text)
	}
	full = strings.TrimSpace(all.String())
	tol := int(cutTolerance/time.Millisecond) * outBytesPerMs
	if full == "" || (n > 0 && n >= r.sent-tol) {
		return "", full, false
	}
	useVoiced := r.voiced > 0
	pos := n
	if useVoiced {
		pos = r.voicedAt(n)
		if pos > 0 && pos >= r.voiced-tol {
			return "", full, false
		}
	}
	var b strings.Builder
	prev := 0
	for _, f := range r.frags {
		fat := f.at
		if useVoiced {
			fat = f.voicedAt
		}
		if fat <= pos && pos > 0 {
			b.WriteString(f.text)
			prev = fat
			continue
		}
		if pos > prev && fat > prev {
			b.WriteString(wordPrefix(f.text, float64(pos-prev)/float64(fat-prev)))
		}
		break
	}
	return strings.TrimSpace(b.String()), full, true
}

// wordPrefix returns the first share (0..1) of text's words, keeping the
// original spacing.
func wordPrefix(text string, share float64) string {
	words := strings.Fields(text)
	k := int(share * float64(len(words)))
	if k <= 0 {
		return ""
	}
	if k >= len(words) {
		return text
	}
	// Cut after the k-th word in the original string.
	seen, inWord := 0, false
	for i, c := range text {
		space := c == ' ' || c == '\t' || c == '\n'
		if !space && !inWord {
			seen++
		}
		if space && inWord && seen == k {
			return text[:i]
		}
		inWord = !space
	}
	return text
}

// ClientSpeech is the client's own speech detector (wsproto "speaking",
// or its silence gate read the same way). Not safe for concurrent use.
type ClientSpeech struct {
	known    bool
	on       bool
	since    time.Time
	closedAt time.Time
}

// Set records a transition of the client's detector.
func (c *ClientSpeech) Set(now time.Time, on bool) {
	if c.known && c.on == on {
		return
	}
	c.known, c.on = true, on
	if on {
		c.since = now
	} else {
		c.closedAt = now
	}
}

// Onset is when the client's detector last opened (ok false when it
// never reported): when the user — or the speaker's echo — last
// began to sound.
func (c *ClientSpeech) Onset() (time.Time, bool) {
	if !c.known || c.since.IsZero() {
		return time.Time{}, false
	}
	return c.since, true
}

// Reset forgets the client's detector (the client went away).
func (c *ClientSpeech) Reset() { *c = ClientSpeech{} }

// Hold is how long pushes should still wait for the user according
// to the client's detector: while it is open (up to ClientSpeechTrust
// since it opened) and ClientSpeechRelease after it closed; 0 when it
// says nothing. Callers combine it with their transcript-based rule,
// which stays the fallback.
func (c *ClientSpeech) Hold(now time.Time) time.Duration {
	if !c.known {
		return 0
	}
	if c.on {
		if left := ClientSpeechTrust - now.Sub(c.since); left > 0 {
			return left
		}
		return 0
	}
	if left := ClientSpeechRelease - now.Sub(c.closedAt); left > 0 {
		return left
	}
	return 0
}
