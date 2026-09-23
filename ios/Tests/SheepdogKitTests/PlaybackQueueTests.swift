import XCTest
@testable import SheepdogKit

final class PlaybackQueueTests: XCTestCase {
    private func chunk(_ milliseconds: Int) -> [Int16] {
        [Int16](repeating: 1, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
    }

    func testHoldsTheJitterBufferThenReleasesInOrder() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150)
        XCTAssertEqual(queue.phase, .idle)

        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty, "40 ms is not enough to start")
        XCTAssertEqual(queue.phase, .buffering)
        XCTAssertTrue(queue.push(chunk(60), now: 0.05).isEmpty)

        let released = queue.push(chunk(60), now: 0.1)
        XCTAssertEqual(released.map(\.id), [1, 2, 3], "chunks come out in arrival order")
        XCTAssertEqual(queue.phase, .playing)
        XCTAssertEqual(queue.queueDepthMilliseconds, 160)
    }

    func testOncePlayingEveryChunkGoesStraightToThePlayer() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        _ = queue.push(chunk(60), now: 0)
        XCTAssertEqual(queue.phase, .playing)
        // The 60 ms chunk's playhead ends at t = 0.06: arriving at 0.05 is
        // still real-time, not an underrun.
        let next = queue.push(chunk(700), now: 0.05)
        XCTAssertEqual(next.map(\.id), [2], "no re-buffering mid-utterance: that would be a gap")
        XCTAssertEqual(queue.queueDepthMilliseconds, 760)
    }

    func testAShortUtteranceStartsAfterTheMaximumWait() {
        var queue = PlaybackQueue(prebufferMilliseconds: 150, maximumWait: 0.4)
        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty)
        XCTAssertTrue(queue.tick(now: 0.2).isEmpty, "still inside the wait")
        let released = queue.tick(now: 0.45)
        XCTAssertEqual(released.map(\.id), [1])
        XCTAssertEqual(queue.phase, .playing)
    }

    func testCompletionDrainsTheDepthAndReturnsToIdle() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let first = queue.push(chunk(60), now: 0)
        let second = queue.push(chunk(60), now: 0.01)
        XCTAssertTrue(queue.isSpeaking)
        queue.completed(first[0].id)
        XCTAssertEqual(queue.queueDepthMilliseconds, 60)
        XCTAssertTrue(queue.isSpeaking)
        queue.completed(second[0].id)
        XCTAssertFalse(queue.isSpeaking)
        XCTAssertEqual(queue.phase, .idle, "the next utterance re-buffers")
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
    }

    func testFlushEmptiesEverythingAtOnce() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let scheduled = queue.push(chunk(200), now: 0)
        _ = queue.push(chunk(200), now: 0.05)
        XCTAssertTrue(queue.flush(), "there was audio, so the node needs stopping")
        XCTAssertEqual(queue.phase, .idle)
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
        XCTAssertFalse(queue.isSpeaking)
        XCTAssertEqual(queue.chunksDropped, 2)
        XCTAssertEqual(queue.flushes, 1)
        // A completion arriving after the flush (the node was mid-callback)
        // must not resurrect the depth.
        queue.completed(scheduled[0].id)
        XCTAssertEqual(queue.queueDepthMilliseconds, 0)
    }

    func testFlushOnAnEmptyQueueSaysThereWasNothingToStop() {
        var queue = PlaybackQueue()
        XCTAssertFalse(queue.flush())
        XCTAssertEqual(queue.flushes, 1)
    }

    func testBufferedChunksAreDroppedByAFlushToo() {
        var queue = PlaybackQueue(prebufferMilliseconds: 500)
        XCTAssertTrue(queue.push(chunk(40), now: 0).isEmpty)
        XCTAssertTrue(queue.flush())
        XCTAssertTrue(queue.tick(now: 10).isEmpty, "nothing is left to release")
    }

    func testEmptyChunksAreIgnored() {
        var queue = PlaybackQueue()
        XCTAssertTrue(queue.push([], now: 0).isEmpty)
        XCTAssertEqual(queue.phase, .idle)
    }
}

/// A synthetic playback clock: advances in fixed steps like the app's UI
/// timer (`AudioEngine.tick`), delivers arrivals with `push`, and reports a
/// chunk `completed` once its own duration has actually elapsed — mirroring
/// `AVAudioPlayerNode`'s real completion callback, which fires only once the
/// audio has played, not when it was scheduled.
private final class PlaybackSimulator {
    private(set) var queue: PlaybackQueue
    private(set) var now: TimeInterval = 0
    private(set) var released: [PlaybackQueue.Chunk] = []
    private var outstanding: [(id: Int, completesAt: TimeInterval)] = []
    /// Mirrors the real node's play cursor: chunks scheduled back to back
    /// complete in sequence, not each `duration` after the moment they
    /// happened to be handed out. Chained the same way `PlaybackQueue`
    /// itself computes `playheadEndsAt`, so the simulated "hardware"
    /// completion and the queue's own prediction agree.
    private var nodePlayheadEndsAt: TimeInterval = 0

    init(_ queue: PlaybackQueue) { self.queue = queue }

    /// One arrival, `milliseconds` of audio landing at `atMilliseconds`.
    /// Returns whatever that specific `push` released (empty while
    /// buffering or refilling).
    @discardableResult
    func arrive(_ milliseconds: Int, atMilliseconds: Double, amplitude: Int16 = 200) -> [PlaybackQueue.Chunk] {
        advance(to: atMilliseconds / 1000)
        let samples = [Int16](repeating: amplitude, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
        let out = queue.push(samples, now: now)
        record(out)
        return out
    }

    func flush(atMilliseconds: Double) {
        advance(to: atMilliseconds / 1000)
        queue.flush()
        outstanding.removeAll()
        nodePlayheadEndsAt = now
    }

    /// Advances the clock, ticking (and completing chunks whose playback
    /// time has passed) along the way, without any new arrival.
    func idle(toMilliseconds milliseconds: Double) {
        advance(to: milliseconds / 1000)
    }

    private func record(_ chunks: [PlaybackQueue.Chunk]) {
        for chunk in chunks {
            released.append(chunk)
            let duration = Double(chunk.samples.count) / AudioContract.playbackRate
            let start = max(nodePlayheadEndsAt, now)
            let completesAt = start + duration
            nodePlayheadEndsAt = completesAt
            outstanding.append((chunk.id, completesAt))
        }
    }

    private func advance(to target: TimeInterval, stepSeconds: TimeInterval = 0.02) {
        while now < target {
            now = min(now + stepSeconds, target)
            record(queue.tick(now: now))
            var stillOutstanding: [(id: Int, completesAt: TimeInterval)] = []
            for item in outstanding {
                if item.completesAt <= now {
                    queue.completed(item.id)
                } else {
                    stillOutstanding.append(item)
                }
            }
            outstanding = stillOutstanding
        }
    }
}

/// The adaptive jitter buffer: growing the target on a real underrun,
/// refilling before resuming, decaying on clean utterances, and the 8 ms
/// fade-in at a post-underrun resumption. Replays synthetic bursty arrival
/// timelines the way the daemon and GPT-Live actually produce them.
final class PlaybackQueueAdaptiveTests: XCTestCase {
    func testSteadyRealTimeArrivalNeverUnderruns() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        // 40 ms chunks every 40 ms: real time, the daemon's usual cadence.
        var t = 0.0
        for _ in 0..<40 {
            sim.arrive(40, atMilliseconds: t)
            t += 40
        }
        sim.idle(toMilliseconds: t + 500)
        XCTAssertEqual(sim.queue.underruns, 0, "arrival keeps pace with playback: nothing should ever run dry")
        XCTAssertEqual(sim.queue.targetMilliseconds, 150, "no underrun ever fired, so the target never grew")
    }

    func testBurstsWithMidUtteranceStallsAdaptAndConverge() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))

        var t = 0.0
        var underrunsBefore = 0
        var lastRepetitionAddedAnUnderrun = true
        // Four repeats of the same shape: a fast burst that fills the
        // *current* target, then a fixed 300 ms real-world stall (no
        // arrival at all for 300 ms after the burst), then a long silence
        // before the next "utterance". Whether that fixed 300 ms stall
        // actually drains the node depends on the buffered runway (the
        // target): while target < 300 ms the node runs dry before the
        // stall ends and the resumption lands inside the 300 ms underrun
        // window, so it counts; once the target has grown to cover the
        // full 300 ms, the same stall never drains the node at all, so
        // later repeats stop adding underruns.
        for _ in 0..<4 {
            underrunsBefore = sim.queue.underruns
            let target = sim.queue.targetMilliseconds
            // All of the burst lands at the same instant `t`, so the
            // release time (and thus the predicted drain time) is exact:
            // t + buffered, with no wall-clock slop to account for.
            var buffered = 0
            while buffered < target {
                sim.arrive(20, atMilliseconds: t)
                buffered += 20
            }
            let resumeAt = t + 300
            sim.arrive(20, atMilliseconds: resumeAt)
            sim.arrive(20, atMilliseconds: resumeAt + 20)
            sim.arrive(20, atMilliseconds: resumeAt + 40)
            let underrunsAfter = sim.queue.underruns
            lastRepetitionAddedAnUnderrun = underrunsAfter > underrunsBefore
            // A long real silence: the next repeat is a fresh utterance.
            t = resumeAt + 40 + 2_000
            sim.idle(toMilliseconds: t)
        }

        XCTAssertGreaterThan(sim.queue.underruns, 0, "a 300 ms stall against a 150 ms floor must underrun at least once")
        XCTAssertFalse(lastRepetitionAddedAnUnderrun, "once the target covers the stall, the same stall must stop underrunning")
        XCTAssertGreaterThan(sim.queue.targetMilliseconds, 150, "the target grew off its floor")
        XCTAssertLessThanOrEqual(sim.queue.targetMilliseconds, 500, "the target never exceeds the ceiling")
    }

    func testTargetStepsUpAndDecaysBackTowardTheFloor() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        // Establish playback (releases at t = 0.02 s, playhead ends at
        // 0.18 s), then resume 180 ms after the drain — inside the 300 ms
        // window — to force exactly one underrun.
        sim.arrive(80, atMilliseconds: 0)
        sim.arrive(80, atMilliseconds: 20)
        sim.arrive(80, atMilliseconds: 360)
        XCTAssertEqual(sim.queue.underruns, 1)
        XCTAssertEqual(sim.queue.targetMilliseconds, 250, "one underrun steps the target up by 100 ms")
        // Finish this utterance generously (well past the 250 ms target)
        // so it releases as one batch and plays out without the
        // short-utterance (`maximumWait`) fallback getting involved.
        sim.arrive(300, atMilliseconds: 380)

        // Several more utterances, each comfortably over target with a
        // long real gap after, finish cleanly: the target decays 20 ms per
        // clean utterance, clamped at the floor. (The utterance above did
        // have an underrun, so its own drain does not decay — only the
        // ones after it do.)
        var t = 380.0 + 300 + 2_000
        for _ in 0..<10 {
            sim.idle(toMilliseconds: t)
            sim.arrive(300, atMilliseconds: t)
            t += 300 + 2_000
        }
        sim.idle(toMilliseconds: t)
        XCTAssertEqual(sim.queue.targetMilliseconds, 150, "clean utterances decay the target back to the floor and no further")
    }

    func testRefillHoldsChunksUntilTheNewTargetIsBufferedAgain() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        sim.arrive(80, atMilliseconds: 0)
        sim.arrive(80, atMilliseconds: 20)
        // Drains at 0.18 s; resume within the underrun window (180 ms later).
        let firstAfterUnderrun = sim.arrive(60, atMilliseconds: 360)
        XCTAssertTrue(firstAfterUnderrun.isEmpty, "the resuming chunk alone is not scheduled: that would still click")
        XCTAssertEqual(sim.queue.phase, .buffering)

        // Keep feeding until the new (250 ms) target is met; only then does
        // a batch come out, all at once.
        let second = sim.arrive(60, atMilliseconds: 380)
        XCTAssertTrue(second.isEmpty)
        let third = sim.arrive(60, atMilliseconds: 400)
        XCTAssertTrue(third.isEmpty)
        let fourth = sim.arrive(60, atMilliseconds: 420)
        let fifth = sim.arrive(60, atMilliseconds: 440)
        XCTAssertFalse(fifth.isEmpty, "300 ms buffered clears the 250 ms target")
        XCTAssertEqual(sim.queue.phase, .playing)
        // Everything held during the refill comes out together, in order.
        XCTAssertGreaterThanOrEqual(fifth.count, 4, "the refill releases as one batch, not one chunk at a time")
        _ = fourth
    }

    func testFadeInAppliesOnlyToTheResumptionAfterAnUnderrunNotToSteadyChunks() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        XCTAssertTrue(sim.arrive(80, atMilliseconds: 0, amplitude: 200).isEmpty, "still buffering")
        let coldStart = sim.arrive(80, atMilliseconds: 20, amplitude: 200)
        XCTAssertEqual(coldStart.first?.samples.first, 200, "no fade at a cold start: there is nothing to click against")
        XCTAssertEqual(coldStart.last?.samples.first, 200, "steady playback is untouched")

        // Force an underrun, then refill to the new target.
        var refilled: [PlaybackQueue.Chunk] = []
        refilled += sim.arrive(60, atMilliseconds: 360, amplitude: 200)
        refilled += sim.arrive(60, atMilliseconds: 380, amplitude: 200)
        refilled += sim.arrive(60, atMilliseconds: 400, amplitude: 200)
        refilled += sim.arrive(60, atMilliseconds: 420, amplitude: 200)
        refilled += sim.arrive(60, atMilliseconds: 440, amplitude: 200)
        XCTAssertGreaterThanOrEqual(sim.queue.underruns, 1)
        guard let resumed = refilled.first else {
            return XCTFail("the refill should have released a batch")
        }
        XCTAssertNotEqual(resumed.samples.first, 200, "the first sample of a resumption ramps up from zero")
        XCTAssertEqual(resumed.samples.first, 0, "the fade starts at true zero, not a partial step")
        // 8 ms at 24 kHz = 192 samples; well inside an 80 ms (1 920-sample)
        // chunk, so the tail is untouched by the fade.
        XCTAssertEqual(resumed.samples[300], 200, "only the head of the resuming chunk is faded")
        if refilled.count > 1 {
            XCTAssertEqual(refilled[1].samples.first, 200, "only the first chunk of the refill batch gets the fade-in")
        }
    }

    func testGPTLiveLikeFastStartThenSlowerThanRealTimeStaysUnderrunFree() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        // Fast start: a lot of audio lands almost at once, the way GPT-Live
        // front-loads a reply.
        sim.arrive(150, atMilliseconds: 0)
        sim.arrive(150, atMilliseconds: 10)
        sim.arrive(150, atMilliseconds: 20)
        // 450 ms of runway banked in 20 ms of wall-clock time. Now arrivals
        // fall behind real time, but the cumulative deficit never comes
        // close to eating that runway.
        var t = 20.0
        for _ in 0..<8 {
            t += 90 // a chunk's worth of audio takes 90 ms to arrive...
            sim.arrive(70, atMilliseconds: t) // ...but is only 70 ms long: a 20 ms/chunk deficit.
        }
        sim.idle(toMilliseconds: t + 1_000)
        XCTAssertEqual(sim.queue.underruns, 0, "the fast start banked enough runway to absorb the slow drip")
    }

    func testFlushMidUtteranceDoesNotCountAsADrainOrMoveTheTarget() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        sim.arrive(80, atMilliseconds: 0)
        sim.arrive(80, atMilliseconds: 20)
        XCTAssertEqual(sim.queue.phase, .playing)
        sim.flush(atMilliseconds: 40)
        XCTAssertEqual(sim.queue.phase, .idle)
        XCTAssertEqual(sim.queue.underruns, 0)
        XCTAssertEqual(sim.queue.drains, 0, "a flush is a reset, not a drain")
        XCTAssertEqual(sim.queue.targetMilliseconds, 150, "flushing does not move the adaptive target")

        // The next utterance buffers fresh, at the same target, from idle.
        XCTAssertTrue(sim.arrive(40, atMilliseconds: 60).isEmpty)
        XCTAssertEqual(sim.queue.phase, .buffering)
    }

    func testLongSilenceBetweenUtterancesIsNotAnUnderrun() {
        let sim = PlaybackSimulator(PlaybackQueue(prebufferMilliseconds: 150))
        sim.arrive(80, atMilliseconds: 0)
        sim.arrive(80, atMilliseconds: 20)
        XCTAssertEqual(sim.queue.phase, .playing)
        // Playhead ends at 0.18 s. Say nothing for 5 s — an ordinary pause
        // between the dog's sentences, not a network stall.
        sim.idle(toMilliseconds: 5_160)
        XCTAssertEqual(sim.queue.phase, .idle)
        XCTAssertEqual(sim.queue.underruns, 0, "a long, ordinary silence between utterances must never count as an underrun")
        XCTAssertEqual(sim.queue.drains, 1, "the node did go dry — just not because anything was late")

        // The next utterance starts cleanly.
        let released = sim.arrive(80, atMilliseconds: 5_160)
        XCTAssertTrue(released.isEmpty)
        XCTAssertEqual(sim.arrive(80, atMilliseconds: 5_180).count, 2)
        XCTAssertEqual(sim.queue.underruns, 0)
    }
}
