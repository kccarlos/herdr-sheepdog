import Foundation

/// Scheduling logic for the gapless player, with no hardware attached.
///
/// The daemon sends PCM16 at 24 kHz in chunks of whatever length the model
/// produced (40–730 ms observed). The contract says they must be played by
/// appending to a buffer, never by scheduling each one at "now": this queue
/// holds a small jitter buffer and then hands chunks out **in order**, one
/// `AVAudioPlayerNode.scheduleBuffer` each, so the node itself concatenates
/// them sample-accurately.
///
/// The jitter buffer's target is **adaptive**, not fixed. It starts at
/// `prebufferMilliseconds` (150 ms in production). The queue tracks its own
/// playhead — when the audio it has handed to the player should run out —
/// rather than waiting on `AVAudioPlayerNode`'s completion callbacks, which
/// land late and jittery on iOS: `completed(_:)` stays for depth/`isSpeaking`
/// bookkeeping, but drain detection happens in `push`/`tick` against
/// `playheadEndsAt`. A drain followed by more audio of the *same* utterance
/// within 300 ms is an **underrun** — a gap the user actually heard — and
/// raises the target by 100 ms (capped at 500 ms) for the rest of the
/// session; a target increase persists, but decays 20 ms for every
/// utterance that completes without one, down to the floor. After an
/// underrun the queue refills to the (now higher) target before handing
/// anything back out, rather than resuming on a single chunk. The first
/// chunk of that refill gets an 8 ms fade-in so the resumption doesn't
/// click; there is no matching fade-out at the drain side — by the time the
/// queue learns a chunk was the last one, its PCM is already with the
/// player (or already played), so nothing is left to fade.
///
/// **Time-stretch.** The queue also owns a `PlaybackRatePolicy`: `steer`
/// (called every 20 ms by `AudioEngine`) reads the buffered audio against
/// the target and picks a playback rate (0.85–1.05) that `AudioEngine`
/// applies to an `AVAudioUnitTimePitch` with pitch 0. At rate r the node
/// plays r seconds of audio per wall-clock second, so the playhead is kept
/// in wall-clock time and rescaled on every rate change: the drain
/// prediction and `bufferedMilliseconds` both use the real rate. The target
/// range comes from a `PlaybackProfile` per voice provider (Gemini
/// 150–500 ms, GPT-Live 1200–2000 ms), and the "start anyway" wait scales
/// with the target (target + 250 ms, as in the browser page).
///
/// `flush` (server) and the Stop button drop everything at once. A flush is
/// not a drain and not a finished utterance: it does not move the adaptive
/// target either way.
///
/// **Playback reports** (docs/ws-contract.md "Playback reports"). The queue
/// also says what its player did, as the daemon wants to hear it, exactly
/// as the browser's `Player` (`web/voice/audio-util.js`) does: `started`
/// when a run begins playing after being empty (not when it resumes from
/// an underrun), `drained` once it ran dry and nothing arrived within the
/// 300 ms underrun window (an underrun that resumes inside the window is
/// the same run), `flushed` on every flush, even an empty one. `played_ms`
/// is **stream time** — the run's received samples minus the ones still
/// buffered, where "buffered" is the predicted playhead's remaining audio
/// (wall-clock time left × rate), so a run stretched to 0.85× still reports
/// its 24 kHz length, not the longer wall-clock time it took. Reports pile
/// up in `reports`; the caller takes them with `takeReports()` after each
/// call and sends them.
public struct PlaybackQueue: Sendable {
    /// One `{"type":"playback"}` report for the daemon.
    public struct Report: Equatable, Sendable {
        public var state: WSMessage.PlaybackState
        /// Stream milliseconds of the run played (0 for `started`).
        public var playedMilliseconds: Int

        public init(_ state: WSMessage.PlaybackState, playedMilliseconds: Int) {
            self.state = state
            self.playedMilliseconds = playedMilliseconds
        }

        public var message: WSMessage { .playback(state, playedMs: playedMilliseconds) }
    }

    /// One chunk ready to be scheduled, in arrival order.
    public struct Chunk: Equatable, Identifiable, Sendable {
        public let id: Int
        public let samples: [Int16]
        public var frameCount: Int { samples.count }
        public init(id: Int, samples: [Int16]) {
            self.id = id
            self.samples = samples
        }
    }

    public enum Phase: Equatable, Sendable {
        /// Nothing queued and nothing playing.
        case idle
        /// Filling the jitter buffer; nothing has been handed out yet.
        case buffering
        /// Chunks are with the player node.
        case playing
    }

    /// A drain counts as an underrun only if more audio of the same
    /// utterance shows up within this long.
    private static let underrunWindow: TimeInterval = 0.3
    private static let targetStepMilliseconds = 100
    private static let targetDecayMilliseconds = 20
    private static let fadeMilliseconds = 8.0
    /// Headroom over the target before a short utterance starts anyway.
    private static let waitPaddingMilliseconds = 250

    public let sampleRate: Double
    /// A lone short utterance still starts after this long rather than never.
    /// Also the ceiling on how long a post-underrun refill will wait before
    /// giving up and playing what it has. The current target plus 250 ms.
    public var maximumWait: TimeInterval {
        Double(targetMilliseconds + Self.waitPaddingMilliseconds) / 1000
    }
    /// The initial adaptive target, and the floor it decays back down to.
    public private(set) var targetFloorMilliseconds: Int
    /// The most the target may grow to.
    public private(set) var targetCeilingMilliseconds: Int

    /// The playback rate the playhead is running at right now.
    public private(set) var rate = 1.0
    /// Wall-clock seconds played at a rate other than 1.0, in total.
    public private(set) var stretchedSeconds: TimeInterval = 0
    public private(set) var ratePolicy = PlaybackRatePolicy()
    private var lastSteerAt: TimeInterval?

    public private(set) var phase: Phase = .idle
    /// Samples held back, not yet handed to the player.
    public private(set) var bufferedSamples = 0
    /// Samples handed to the player and not yet reported complete.
    public private(set) var scheduledSamples = 0
    public private(set) var flushes = 0
    public private(set) var chunksDropped = 0

    /// The jitter-buffer target right now, in milliseconds: floor to
    /// ceiling (150–500 ms for Gemini), growing on underruns and decaying
    /// by a clean utterance at a time.
    public private(set) var targetMilliseconds: Int
    /// Drains that turned out to be audible gaps (more of the same
    /// utterance arrived within `underrunWindow`).
    public private(set) var underruns = 0
    /// Every time the player ran out of scheduled audio, underrun or not
    /// (the ordinary silence between utterances is a drain too).
    public private(set) var drains = 0
    /// The longest gap seen between chunk arrivals while a gap was worth
    /// caring about (mid-utterance, or still inside the underrun window of
    /// a drain) — a long pause between utterances does not count.
    public private(set) var largestArrivalGapMilliseconds = 0

    /// Reports not yet taken by the caller, oldest first.
    public private(set) var reports: [Report] = []
    /// Samples received in the current run (since the last `drained` or
    /// `flushed` report): what `played_ms` counts against.
    public private(set) var runSamples = 0
    /// The next release resumes an underrun: the same run, no `started`.
    private var continuingRun = false

    private var pending: [Chunk] = []
    private var nextID = 1
    private var bufferingSince: TimeInterval = 0
    private var live: Set<Int> = []
    private var scheduledLengths: [Int: Int] = [:]

    /// When the audio already handed to the player is predicted to run
    /// out, in wall-clock time at the current `rate`. This is the
    /// authoritative "is the node dry" clock — computed from chunk
    /// durations, not from completion callbacks, which are late enough on
    /// iOS to make a poor detector.
    private var playheadEndsAt: TimeInterval?
    private var lastPushAt: TimeInterval?
    /// Set when a drain is observed; cleared once the following gap (or
    /// lack of one) has been folded into the underrun/decay bookkeeping.
    private var drainedAt: TimeInterval?
    private var utteranceHadUnderrun = false
    /// True while the next chunk released from buffering should carry a
    /// fade-in because it is resuming after an underrun.
    private var fadeInNextRelease = false

    public init(
        sampleRate: Double = AudioContract.playbackRate,
        prebufferMilliseconds: Int = 150,
        ceilingMilliseconds: Int = 500
    ) {
        self.sampleRate = sampleRate
        self.targetFloorMilliseconds = prebufferMilliseconds
        self.targetCeilingMilliseconds = max(prebufferMilliseconds, ceilingMilliseconds)
        self.targetMilliseconds = prebufferMilliseconds
    }

    public init(profile: PlaybackProfile, sampleRate: Double = AudioContract.playbackRate) {
        self.init(
            sampleRate: sampleRate,
            prebufferMilliseconds: profile.floorMilliseconds,
            ceilingMilliseconds: profile.ceilingMilliseconds
        )
    }

    public var profile: PlaybackProfile {
        PlaybackProfile(floorMilliseconds: targetFloorMilliseconds, ceilingMilliseconds: targetCeilingMilliseconds)
    }

    /// Switches the target range (the voice provider changed). The target
    /// restarts at the new floor; counters are kept.
    public mutating func setProfile(_ profile: PlaybackProfile) {
        guard profile != self.profile else { return }
        targetFloorMilliseconds = profile.floorMilliseconds
        targetCeilingMilliseconds = profile.ceilingMilliseconds
        targetMilliseconds = profile.floorMilliseconds
    }

    /// True while the player has audio to play — what the orb calls "speaking".
    public var isSpeaking: Bool { scheduledSamples > 0 }

    /// Samples to collect before the next chunk is released, from the
    /// current adaptive target.
    public var prebufferSamples: Int {
        Int(sampleRate * Double(targetMilliseconds) / 1000.0)
    }

    /// How much audio has been queued or scheduled and not yet reported
    /// played by the node, in milliseconds. Follows the (late) completion
    /// callbacks; `bufferedMilliseconds(at:)` is the predicted figure.
    public var queueDepthMilliseconds: Int {
        Int(Double(bufferedSamples + scheduledSamples) / sampleRate * 1000.0)
    }

    /// How much audio is waiting to be heard at `now`, in milliseconds of
    /// audio: what is held back plus what the predicted playhead has not
    /// reached yet (wall-clock time left × rate). The rate policy's `b` and
    /// the ping's `buffer_ms`.
    public func bufferedMilliseconds(at now: TimeInterval) -> Double {
        var seconds = Double(bufferedSamples) / sampleRate
        if phase == .playing, let endsAt = playheadEndsAt {
            seconds += max(0, endsAt - now) * rate
        }
        return seconds * 1000
    }

    // MARK: - Time-stretch

    /// The 20 ms control loop: notices a drain, then moves the rate one
    /// slew step towards what the buffered audio calls for and rescales
    /// the playhead to it. Returns the rate to apply to the node.
    @discardableResult
    public mutating func steer(now: TimeInterval) -> Double {
        observeDrain(now: now)
        let dt = lastSteerAt.map { max(0, now - $0) } ?? 0
        lastSteerAt = now
        switch phase {
        case .idle:
            // Keep the rate through a possible underrun (the drain's
            // bookkeeping is still open); reset once the utterance is over.
            if drainedAt == nil {
                ratePolicy.reset()
                applyRate(1.0, now: now)
            }
        case .buffering:
            break
        case .playing:
            if rate != 1.0 { stretchedSeconds += min(dt, PlaybackRatePolicy.maximumStep) }
            let next = ratePolicy.step(
                bufferedMilliseconds: bufferedMilliseconds(at: now),
                targetMilliseconds: Double(targetMilliseconds),
                dt: dt
            )
            applyRate(next, now: now)
        }
        return rate
    }

    /// Sets the rate directly, bypassing the policy (tests).
    mutating func setRate(_ next: Double, now: TimeInterval) {
        observeDrain(now: now)
        applyRate(next, now: now)
    }

    /// Rescales the remaining playhead: the audio left is (endsAt − now)·r
    /// seconds, and at the new rate it lasts that divided by r'.
    private mutating func applyRate(_ next: Double, now: TimeInterval) {
        guard next != rate else { return }
        if phase == .playing, let endsAt = playheadEndsAt, endsAt > now {
            let remainingAudio = (endsAt - now) * rate
            playheadEndsAt = now + remainingAudio / next
        }
        rate = next
    }

    /// Appends a chunk. Returns the chunks to schedule now, in order: empty
    /// while the jitter buffer fills, then everything held plus this one.
    public mutating func push(_ samples: [Int16], now: TimeInterval) -> [Chunk] {
        guard !samples.isEmpty else { return [] }
        trackArrivalGap(now: now)
        observeDrain(now: now)

        if phase == .playing {
            runSamples += samples.count
            return appendLive(samples, now: now)
        }

        if phase == .idle {
            resolveDrainBookkeeping(now: now)
            phase = .buffering
            bufferingSince = now
        }

        runSamples += samples.count
        return bufferAndMaybeRelease(samples, now: now)
    }

    /// The reports since the last call, oldest first; empties `reports`.
    public mutating func takeReports() -> [Report] {
        defer { reports.removeAll() }
        return reports
    }

    /// Releases a short utterance that never reached the jitter buffer, and
    /// notices a drain even when nothing has been pushed since. Call it
    /// from the same timer that refreshes the UI.
    public mutating func tick(now: TimeInterval) -> [Chunk] {
        observeDrain(now: now)
        if phase == .idle, let drainedAt, now - drainedAt > Self.underrunWindow {
            finalizeCleanUtterance()
            reportDrained()
            self.drainedAt = nil
        }
        guard phase == .buffering, now - bufferingSince >= maximumWait else { return [] }
        return release(now: now)
    }

    /// Reports that the player finished a chunk. This is bookkeeping for
    /// the queue depth and `isSpeaking` only: drain *detection* for the
    /// adaptive target happens in `push`/`tick` against the playhead, which
    /// usually notices first. A completion for a chunk the playhead already
    /// wrote off (dropped by a predicted drain, or by `flush`) is a no-op.
    public mutating func completed(_ id: Int) {
        guard live.remove(id) != nil else { return }
        if let index = scheduledLengths.index(forKey: id) {
            scheduledSamples -= scheduledLengths[index].value
            scheduledLengths.remove(at: index)
        }
        if scheduledSamples < 0 { scheduledSamples = 0 }
        if live.isEmpty && pending.isEmpty {
            scheduledSamples = 0
            if phase == .playing {
                // The node reported dry before the predicted playhead got
                // there: that is still a drain, dated at the prediction.
                drains += 1
                drainedAt = playheadEndsAt
            }
            phase = .idle
            playheadEndsAt = nil
        }
    }

    /// Drop everything now: the queued chunks and the ones already with the
    /// player (the caller stops the node). Returns true when there was
    /// anything to drop, i.e. the node needs stopping. A flush is a reset,
    /// not a drain: it does not count toward underruns or move the
    /// adaptive target, and the next utterance buffers at whatever target
    /// the queue already had.
    ///
    /// Always reports `flushed`, with how much of the run was heard by
    /// `now` (all of it once it ran dry; 0 with nothing playing). Without a
    /// `now`, everything not yet reported played counts as unheard.
    @discardableResult
    public mutating func flush(now: TimeInterval? = nil) -> Bool {
        let unplayed: Int
        if drainedAt != nil {
            unplayed = 0
        } else if let now {
            unplayed = Int((bufferedMilliseconds(at: now) / 1000 * sampleRate).rounded())
        } else {
            unplayed = bufferedSamples + scheduledSamples
        }
        reports.append(Report(.flushed, playedMilliseconds: milliseconds(max(0, runSamples - unplayed))))
        runSamples = 0
        continuingRun = false
        let hadAudio = !pending.isEmpty || !live.isEmpty
        chunksDropped += pending.count + live.count
        pending.removeAll()
        live.removeAll()
        scheduledLengths.removeAll()
        bufferedSamples = 0
        scheduledSamples = 0
        phase = .idle
        flushes += 1
        playheadEndsAt = nil
        drainedAt = nil
        utteranceHadUnderrun = false
        fadeInNextRelease = false
        ratePolicy.reset()
        rate = 1.0
        return hadAudio
    }

    // MARK: - Drain / underrun detection

    /// If the node's predicted playhead has passed `now` while chunks are
    /// still marked live, the player has run dry. Move to `.idle`
    /// preemptively (a late `completed` for one of those chunks then finds
    /// it already gone from `live` and no-ops) and remember when, so the
    /// next arrival can be judged against the underrun window.
    private mutating func observeDrain(now: TimeInterval) {
        guard phase == .playing, let endsAt = playheadEndsAt, now >= endsAt else { return }
        drains += 1
        phase = .idle
        live.removeAll()
        scheduledLengths.removeAll()
        scheduledSamples = 0
        playheadEndsAt = nil
        drainedAt = endsAt
    }

    /// Called once, right before re-entering `.buffering` from `.idle`:
    /// decides whether the gap since the last drain (if any) was short
    /// enough to be the underrun that caused it, or long enough that the
    /// utterance which just finished was clean.
    private mutating func resolveDrainBookkeeping(now: TimeInterval) {
        guard let drainedAt else { return }
        if now - drainedAt <= Self.underrunWindow {
            registerUnderrun()
            continuingRun = true
        } else {
            finalizeCleanUtterance()
            reportDrained()
        }
        self.drainedAt = nil
    }

    /// The run is over: all of it played (the playhead ran out).
    private mutating func reportDrained() {
        reports.append(Report(.drained, playedMilliseconds: milliseconds(runSamples)))
        runSamples = 0
        continuingRun = false
    }

    private func milliseconds(_ samples: Int) -> Int {
        Int((Double(samples) / sampleRate * 1000).rounded())
    }

    private mutating func registerUnderrun() {
        underruns += 1
        utteranceHadUnderrun = true
        targetMilliseconds = min(targetMilliseconds + Self.targetStepMilliseconds, targetCeilingMilliseconds)
        fadeInNextRelease = true
    }

    private mutating func finalizeCleanUtterance() {
        if !utteranceHadUnderrun {
            targetMilliseconds = max(targetMilliseconds - Self.targetDecayMilliseconds, targetFloorMilliseconds)
        }
        utteranceHadUnderrun = false
    }

    private mutating func trackArrivalGap(now: TimeInterval) {
        defer { lastPushAt = now }
        guard let last = lastPushAt else { return }
        let midUtterance = phase != .idle || (drainedAt.map { now - $0 <= Self.underrunWindow } ?? false)
        guard midUtterance else { return }
        largestArrivalGapMilliseconds = max(largestArrivalGapMilliseconds, Int((now - last) * 1000))
    }

    // MARK: - Scheduling

    /// Phase is `.playing` and the playhead has not run dry: the chunk goes
    /// straight to the player and extends the predicted playhead.
    private mutating func appendLive(_ samples: [Int16], now: TimeInterval) -> [Chunk] {
        let chunk = Chunk(id: nextID, samples: samples)
        nextID += 1
        live.insert(chunk.id)
        scheduledLengths[chunk.id] = chunk.frameCount
        scheduledSamples += chunk.frameCount
        let start = max(playheadEndsAt ?? now, now)
        playheadEndsAt = start + duration(of: chunk.frameCount) / rate
        return [chunk]
    }

    private mutating func bufferAndMaybeRelease(_ samples: [Int16], now: TimeInterval) -> [Chunk] {
        var chunk = Chunk(id: nextID, samples: samples)
        nextID += 1
        if fadeInNextRelease, pending.isEmpty {
            chunk = fadedIn(chunk)
            fadeInNextRelease = false
        }
        pending.append(chunk)
        bufferedSamples += chunk.frameCount
        if bufferedSamples >= prebufferSamples { return release(now: now) }
        return []
    }

    private mutating func release(now: TimeInterval) -> [Chunk] {
        let out = pending
        pending.removeAll()
        bufferedSamples = 0
        phase = .playing
        if !continuingRun { reports.append(Report(.started, playedMilliseconds: 0)) }
        continuingRun = false
        var playhead = now
        for chunk in out {
            live.insert(chunk.id)
            scheduledLengths[chunk.id] = chunk.frameCount
            scheduledSamples += chunk.frameCount
            playhead += duration(of: chunk.frameCount) / rate
        }
        playheadEndsAt = playhead
        return out
    }

    private func duration(of frameCount: Int) -> TimeInterval {
        Double(frameCount) / sampleRate
    }

    /// An 8 ms linear ramp from zero, applied to the PCM before it is ever
    /// handed out — the queue owns the samples up to that point, so the
    /// fade is real, not a flag for the caller to interpret.
    private func fadedIn(_ chunk: Chunk) -> Chunk {
        let fadeSamples = min(chunk.samples.count, Int(sampleRate * Self.fadeMilliseconds / 1000.0))
        guard fadeSamples > 1 else { return chunk }
        var samples = chunk.samples
        for index in 0..<fadeSamples {
            let gain = Double(index) / Double(fadeSamples)
            samples[index] = Int16((Double(samples[index]) * gain).rounded())
        }
        return Chunk(id: chunk.id, samples: samples)
    }
}
