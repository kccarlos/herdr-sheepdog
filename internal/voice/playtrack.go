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
	maxAwaiting = 16
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

// FlushMeta captures the session context of a daemon-flushed run at the
// moment it is interrupted, so a late PlaybackFlushed report carries the
// interrupted turn's own recap sequence, utterance id, and interrupt cause.
type FlushMeta struct {
	RecapSeq    uint64
	UtteranceID string
	ByPush      bool
}

// PlaybackReport is one client playback report as the session saw it
// (voice.playback): RunMs is the audio the daemon sent in that run.
type PlaybackReport struct {
	State    string
	PlayedMs int
	RunMs    int
	At       time.Time
	// Cut is set when a flushed run was cut short.
	Cut *TranscriptCut
	// Heard is the heard part of a cut run.
	Heard string
	// Awaited identifies a daemon-interrupted run, already saved in recap.
	Awaited bool
	// RecapSeq is the Recap sequence id of the cut assistant entry when known.
	RecapSeq uint64

	fromTracker   bool
	awaitedPopped int
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

// Flushed returns and clears the utterance id of a flushed run: the active
// started run when set, or the handed utterance when awaited is true (the
// model had already begun producing the reply before PlaybackStarted arrived).
func (p *PlaybackUtterance) Flushed(awaited bool) string {
	if p.run != "" {
		id := p.run
		p.run = ""
		return id
	}
	if awaited && p.id != "" {
		id := p.id
		p.id = ""
		return id
	}
	return ""
}

// Payload is the voice.playback event payload.
func (r PlaybackReport) Payload() map[string]any {
	p := map[string]any{"state": r.State, "played_ms": r.PlayedMs, "run_ms": r.RunMs}
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

// playRun is the audio and output transcript of an assistant reply across
// client playback segments. When a mid-reply underrun produces PlaybackDrained
// before the turn finishes, the drained segment's heard text and duration stay
// in prefixRaw/prefixHeardMs/prefixRunMs so a subsequent cut trims only the
// unheard continuation rather than erasing what already played.
type playRun struct {
	prefixRaw     string
	prefixHeardMs int
	prefixRunMs   int

	sent   int
	voiced int
	segs   []playSeg
	frags  []spokenFrag
	meta   FlushMeta
	// flushedAt is when the daemon flushed the run (awaiting runs).
	flushedAt time.Time
}

func (r playRun) empty() bool {
	return r.sent == 0 && len(r.frags) == 0 && r.prefixRaw == ""
}

func (r playRun) hasPending() bool {
	return r.sent > 0 || len(r.frags) > 0
}

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
	// done holds completed assistant turns still playing on the client when a
	// newer turn has already started streaming behind them.
	done []playRun
	run  playRun
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

// Empty reports whether nothing is pending or retained in the tracker.
func (p *PlaybackTracker) Empty() bool { return len(p.done) == 0 && p.run.empty() }

func (p *PlaybackTracker) pendingBytes() int {
	n := p.run.sent
	for _, d := range p.done {
		n += d.sent
	}
	return n
}

func (p *PlaybackTracker) takeActiveRun() playRun {
	if len(p.done) > 0 {
		r := p.done[0]
		p.done = nil
		p.run = playRun{}
		return r
	}
	r := p.run
	p.run = playRun{}
	return r
}

// SetRecapSeq associates the Recap sequence id of a settled assistant turn
// with the current run so a subsequent flush targets that exact entry.
func (p *PlaybackTracker) SetRecapSeq(seq uint64) {
	if seq == 0 {
		return
	}
	if !p.run.empty() {
		p.run.meta.RecapSeq = seq
	} else if n := len(p.done); n > 0 && p.done[n-1].meta.RecapSeq == 0 {
		p.done[n-1].meta.RecapSeq = seq
	}
}

// TurnComplete marks the end of the current assistant turn. If all audio of
// the turn has already drained, the retained prefix is retired cleanly so the
// next turn starts fresh; otherwise the turn waits in done until drained or
// flushed.
func (p *PlaybackTracker) TurnComplete() {
	if !p.run.hasPending() {
		p.run = playRun{}
		return
	}
	p.done = append(p.done, p.run)
	p.run = playRun{}
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
	return p.FlushMeta(now, FlushMeta{})
}

// FlushMeta is Flush, also recording the interrupted turn's session metadata
// on the awaiting run so a late PlaybackFlushed report uses that turn's own
// RecapSeq, UtteranceID, and ByPush rather than a newer turn's state. Every
// daemon flush records an acknowledgement slot, even when nothing was playing,
// so an empty flushed report never consumes a subsequent reply.
func (p *PlaybackTracker) FlushMeta(now time.Time, meta FlushMeta) bool {
	r := p.takeActiveRun()
	hadRun := !r.empty()
	if hadRun {
		if meta.RecapSeq != 0 && r.meta.RecapSeq == 0 {
			r.meta.RecapSeq = meta.RecapSeq
		}
		r.meta.UtteranceID = meta.UtteranceID
		r.meta.ByPush = meta.ByPush
	}
	r.flushedAt = now
	p.awaiting = append(p.awaiting, r)
	if len(p.awaiting) > maxAwaiting {
		p.awaiting = p.awaiting[len(p.awaiting)-maxAwaiting:]
	}
	p.playing = false
	p.holdUntil = time.Time{}
	p.est = time.Time{}
	p.endedAt = now
	return hadRun
}

// Detach forgets the client (it went away, or a new one replaced it):
// its runs, its reports and whether it reports at all.
func (p *PlaybackTracker) Detach() {
	*p = PlaybackTracker{last: p.last}
}

// Report applies a client playback report and returns it as the session
// should log it; a flushed run that was cut short carries Cut.
func (p *PlaybackTracker) Report(now time.Time, state string, playedMs int) PlaybackReport {
	if playedMs < 0 {
		playedMs = 0
	}
	p.reports = true
	rep := PlaybackReport{State: state, PlayedMs: playedMs, At: now, fromTracker: true}
	switch state {
	case PlaybackStarted:
		rep.RunMs = p.pendingBytes() / outBytesPerMs
		if !p.playing {
			p.playingSince = now
		}
		p.playing = true
		p.holdUntil = time.Time{}
	case PlaybackDrained:
		rep.RunMs = p.pendingBytes() / outBytesPerMs
		p.playing = false
		p.holdUntil = now.Add(PlaybackGapHold)
		p.endedAt = now
		p.drainBytes(playedMs * outBytesPerMs)
	case PlaybackFlushed:
		for len(p.awaiting) > 0 && now.Sub(p.awaiting[0].flushedAt) > awaitingTTL {
			p.awaiting = p.awaiting[1:]
		}
		if playedMs > 0 {
			for len(p.awaiting) > 0 && p.awaiting[0].empty() {
				p.awaiting = p.awaiting[1:]
				rep.awaitedPopped++
			}
		}
		var r playRun
		if len(p.awaiting) > 0 {
			r, p.awaiting = p.awaiting[0], p.awaiting[1:]
			rep.Awaited = true
			rep.awaitedPopped++
		} else {
			// The client flushed on its own (the stop button) or ahead
			// of the daemon's interrupt: the current run is what it cut.
			r = p.takeActiveRun()
			p.est = time.Time{}
		}
		heard, full, heardMs, runMs, cut := r.heardAt(playedMs)
		rep.RunMs = runMs
		rep.RecapSeq = r.meta.RecapSeq
		if p.Empty() {
			p.playing = false
			p.holdUntil = time.Time{}
			p.endedAt = now
		}
		if cut {
			rep.Cut = &TranscriptCut{
				HeardMs:     heardMs,
				RunMs:       runMs,
				Full:        full,
				UtteranceID: r.meta.UtteranceID,
				ByPush:      r.meta.ByPush,
			}
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
	if p.pendingBytes() > 0 && now.Before(p.est) {
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

func (p *PlaybackTracker) drainBytes(n int) {
	rem := n
	for len(p.done) > 0 && rem > 0 {
		if rem >= p.done[0].sent {
			rem -= p.done[0].sent
			p.done = p.done[1:]
			continue
		}
		p.done[0] = p.done[0].advanceInTurn(rem)
		return
	}
	if len(p.done) == 0 && rem > 0 && !p.run.empty() {
		p.run = p.run.advanceInTurn(rem)
	}
}

func shiftNonNeg(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// advanceInTurn records n drained bytes within the same assistant turn,
// retaining the already-heard text and duration in prefixRaw/prefixHeardMs
// and shifting any remaining in-flight audio and fragments down.
func (r playRun) advanceInTurn(n int) playRun {
	if n <= 0 {
		return r
	}
	clamped := n
	if clamped > r.sent {
		clamped = r.sent
	}
	heardRaw, remFrags := r.splitAt(n)
	out := playRun{
		prefixRaw:     r.prefixRaw + heardRaw,
		prefixHeardMs: r.prefixHeardMs + clamped/outBytesPerMs,
		prefixRunMs:   r.prefixRunMs + clamped/outBytesPerMs,
		meta:          r.meta,
		frags:         remFrags,
	}
	if n >= r.sent {
		return out
	}
	vDrop := r.voicedAt(n)
	out.sent = r.sent - n
	out.voiced = r.voiced - vDrop
	for _, seg := range r.segs {
		if seg.sent > n {
			out.segs = append(out.segs, playSeg{sent: seg.sent - n, voiced: seg.voiced - vDrop})
		}
	}
	return out
}

func (r playRun) hasUnvoicedTail() bool {
	useVoiced := r.voiced > 0
	total := r.sent
	if useVoiced {
		total = r.voiced
	}
	for i, prev := 0, 0; i < len(r.frags); {
		txt, j, fat := r.nextFragGroup(i, prev, total, useVoiced)
		if fat <= prev && strings.TrimSpace(txt) != "" {
			return true
		}
		prev, i = fat, j
	}
	return false
}

func (r playRun) splitAt(n int) (heardRaw string, rem []spokenFrag) {
	if len(r.frags) == 0 {
		return "", nil
	}
	if n >= r.sent && !r.hasUnvoicedTail() {
		var all strings.Builder
		for _, f := range r.frags {
			all.WriteString(f.text)
		}
		return all.String(), nil
	}
	vDrop := r.voicedAt(n)
	useVoiced := r.voiced > 0
	total := r.sent
	pos := n
	if useVoiced {
		total = r.voiced
		pos = vDrop
	}
	shiftTail := func(from int) {
		for _, f := range r.frags[from:] {
			rem = append(rem, spokenFrag{
				text:     f.text,
				at:       shiftNonNeg(f.at - n),
				voicedAt: shiftNonNeg(f.voicedAt - vDrop),
			})
		}
	}
	var b strings.Builder
	for i, prev := 0, 0; i < len(r.frags); {
		txt, j, fat := r.nextFragGroup(i, prev, total, useVoiced)
		if fat <= prev {
			shiftTail(i)
			break
		}
		if fat <= pos && pos > 0 {
			b.WriteString(txt)
			prev, i = fat, j
			continue
		}
		if pos > prev && fat > prev {
			pref := wordPrefix(txt, float64(pos-prev)/float64(fat-prev))
			b.WriteString(pref)
			rest := txt[len(pref):]
			if strings.TrimSpace(rest) != "" {
				last := r.frags[j-1]
				rem = append(rem, spokenFrag{
					text:     rest,
					at:       shiftNonNeg(last.at - n),
					voicedAt: shiftNonNeg(last.voicedAt - vDrop),
				})
			}
			shiftTail(j)
			break
		}
		shiftTail(i)
		break
	}
	return b.String(), rem
}

func (r playRun) fragOffset(i int, useVoiced bool) int {
	if useVoiced {
		return r.frags[i].voicedAt
	}
	return r.frags[i].at
}

// nextFragGroup coalesces consecutive fragments starting at i that share the
// same audio span above prev, returning the combined text, the next index j,
// and the span's end offset fat.
func (r playRun) nextFragGroup(i, prev, total int, useVoiced bool) (text string, j, fat int) {
	off := r.fragOffset(i, useVoiced)
	var b strings.Builder
	b.WriteString(r.frags[i].text)
	j = i + 1
	if off <= prev {
		for j < len(r.frags) && r.fragOffset(j, useVoiced) <= prev {
			b.WriteString(r.frags[j].text)
			j++
		}
		if j < len(r.frags) {
			fat = r.fragOffset(j, useVoiced)
		} else {
			fat = total
		}
		return b.String(), j, fat
	}
	at := r.frags[i].at
	for j < len(r.frags) && r.frags[j].at == at {
		b.WriteString(r.frags[j].text)
		j++
	}
	return b.String(), j, off
}

func (r playRun) heardAt(playedMs int) (heard, full string, heardMs, runMs int, cut bool) {
	n := playedMs * outBytesPerMs
	segRunMs := r.sent / outBytesPerMs
	runMs = r.prefixRunMs + segRunMs
	var segAll strings.Builder
	for _, f := range r.frags {
		segAll.WriteString(f.text)
	}
	segRaw := segAll.String()
	full = strings.TrimSpace(r.prefixRaw + segRaw)
	if strings.TrimSpace(segRaw) == "" {
		return "", full, r.prefixHeardMs, runMs, false
	}
	segHeardRaw, segCut := r.segmentHeardRaw(n)
	if !segCut {
		return "", full, r.prefixHeardMs + playedMs, runMs, false
	}
	return strings.TrimSpace(r.prefixRaw + segHeardRaw), full, r.prefixHeardMs + playedMs, runMs, true
}

// segmentHeardRaw maps n played bytes onto the current segment's fragments,
// returning the untrimmed heard prefix and whether the segment was cut short.
func (r playRun) segmentHeardRaw(n int) (heardRaw string, cut bool) {
	tol := int(cutTolerance/time.Millisecond) * outBytesPerMs
	useVoiced := r.voiced > 0
	total := r.sent
	if useVoiced {
		total = r.voiced
	}
	unvoicedTail := r.hasUnvoicedTail()
	pos := n
	if useVoiced {
		pos = r.voicedAt(n)
	}
	if !unvoicedTail {
		if n > 0 && (n >= r.sent || (r.sent > tol && n >= r.sent-tol)) {
			return "", false
		}
		if useVoiced && pos > 0 && (pos >= r.voiced || (r.voiced > tol && pos >= r.voiced-tol)) {
			return "", false
		}
	}
	var b strings.Builder
	for i, prev := 0, 0; i < len(r.frags); {
		txt, j, fat := r.nextFragGroup(i, prev, total, useVoiced)
		if fat <= prev {
			break
		}
		if fat <= pos && pos > 0 {
			b.WriteString(txt)
			prev, i = fat, j
			continue
		}
		if pos > prev && fat > prev {
			b.WriteString(wordPrefix(txt, float64(pos-prev)/float64(fat-prev)))
		}
		break
	}
	return b.String(), true
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
