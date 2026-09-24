import XCTest
@testable import SheepdogKit

/// The time-stretch rate policy: the table, the slew limit, and the
/// latency-debt gate on catching up.
final class PlaybackRatePolicyTests: XCTestCase {
    private func rate(_ b: Double, _ t: Double = 800, catchUp: Bool = true) -> Double {
        PlaybackRatePolicy.desiredRate(bufferedMilliseconds: b, targetMilliseconds: t, catchUp: catchUp)
    }

    func testTheRateTable() {
        // T = 800 ms (GPT-Live's floor).
        XCTAssertEqual(rate(2_000), 1.05, accuracy: 1e-9, "b > 2T catches up")
        XCTAssertEqual(rate(1_600), 1.0, accuracy: 1e-9, "b = 2T is not above it")
        XCTAssertEqual(rate(1_000), 1.0, accuracy: 1e-9)
        XCTAssertEqual(rate(800), 1.0, accuracy: 1e-9, "b ≥ T plays at 1.0")
        XCTAssertEqual(rate(600), 0.95, accuracy: 1e-9, "halfway between T/2 and T")
        XCTAssertEqual(rate(400), 0.90, accuracy: 1e-9, "b = T/2 is the knee")
        XCTAssertEqual(rate(300), 0.875, accuracy: 1e-9, "halfway between T/4 and T/2")
        XCTAssertEqual(rate(200), 0.85, accuracy: 1e-9, "b = T/4 is the floor")
        XCTAssertEqual(rate(50), 0.85, accuracy: 1e-9)
        XCTAssertEqual(rate(0), 0.85, accuracy: 1e-9)
        // T = 150 ms (Gemini's floor): the same shape, scaled.
        XCTAssertEqual(rate(75, 150), 0.90, accuracy: 1e-9)
        XCTAssertEqual(rate(301, 150), 1.05, accuracy: 1e-9)
    }

    func testCatchUpNeedsLatencyDebt() {
        XCTAssertEqual(rate(5_000, catchUp: false), 1.0, "a provider that is simply ahead owes nothing")
        var policy = PlaybackRatePolicy()
        for _ in 0..<50 { policy.step(bufferedMilliseconds: 5_000, targetMilliseconds: 150, dt: 0.02) }
        XCTAssertEqual(policy.rate, 1.0, "no stretch so far, so no catching up")
        XCTAssertEqual(policy.debtSeconds, 0)
    }

    func testSlewIsAtMostTwoHundredthsPerTwentyMilliseconds() {
        var policy = PlaybackRatePolicy()
        var rates: [Double] = []
        for _ in 0..<10 { rates.append(policy.step(bufferedMilliseconds: 0, targetMilliseconds: 800, dt: 0.02)) }
        XCTAssertEqual(rates[0], 0.98, accuracy: 1e-9)
        XCTAssertEqual(rates[6], 0.86, accuracy: 1e-9)
        XCTAssertEqual(rates[7], 0.85, accuracy: 1e-9, "reaches the floor after 150 ms")
        XCTAssertEqual(rates[9], 0.85, accuracy: 1e-9, "and stays there")
        // A late control-loop tick is capped, not a jump.
        var late = PlaybackRatePolicy()
        XCTAssertEqual(late.step(bufferedMilliseconds: 0, targetMilliseconds: 800, dt: 1.0), 0.90, accuracy: 1e-9)
        // Every consecutive pair within the limit, both ways.
        for _ in 0..<40 { rates.append(policy.step(bufferedMilliseconds: 5_000, targetMilliseconds: 800, dt: 0.02)) }
        for (a, b) in zip(rates, rates.dropFirst()) {
            XCTAssertLessThanOrEqual(abs(b - a), 0.02 + 1e-9)
        }
    }

    func testStretchingAccruesDebtThatCatchUpPaysBack() {
        var policy = PlaybackRatePolicy()
        // One second slowed down near the floor.
        for _ in 0..<50 { policy.step(bufferedMilliseconds: 0, targetMilliseconds: 800, dt: 0.02) }
        XCTAssertGreaterThan(policy.debtSeconds, 0.1)
        // A burst lands: far above 2T, so it speeds up until the debt is gone.
        var sawCatchUp = false
        for _ in 0..<2_000 {
            let r = policy.step(bufferedMilliseconds: 5_000, targetMilliseconds: 800, dt: 0.02)
            if r > 1.0 { sawCatchUp = true }
        }
        XCTAssertTrue(sawCatchUp, "the burst is played a little fast to win the time back")
        XCTAssertEqual(policy.rate, 1.0, accuracy: 1e-9, "debt paid, back to 1.0")
        XCTAssertLessThan(policy.debtSeconds, 0.01)
        policy.reset()
        XCTAssertEqual(policy.rate, 1.0)
        XCTAssertEqual(policy.debtSeconds, 0)
    }

    func testProviderProfiles() {
        XCTAssertEqual(PlaybackProfile.forProvider("gemini"), PlaybackProfile(floorMilliseconds: 150, ceilingMilliseconds: 500))
        XCTAssertEqual(PlaybackProfile.forProvider("openai"), PlaybackProfile(floorMilliseconds: 1_200, ceilingMilliseconds: 2_000))
        XCTAssertEqual(PlaybackProfile.forProvider(nil), .gemini, "older daemons without a provider field are Gemini")
        XCTAssertEqual(PlaybackProfile.forProvider("something-new"), .gemini)
    }
}

/// The playhead runs in wall-clock time at the current rate.
final class PlaybackQueueStretchTests: XCTestCase {
    private func chunk(_ milliseconds: Int) -> [Int16] {
        [Int16](repeating: 1, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
    }

    func testThePlayheadStretchesWithTheRate() {
        var queue = PlaybackQueue(prebufferMilliseconds: 100)
        XCTAssertEqual(queue.push(chunk(1_000), now: 0).count, 1)
        XCTAssertEqual(queue.bufferedMilliseconds(at: 0.5), 500, accuracy: 0.5)
        // Half speed from t = 0.5: the 500 ms left now takes a full second.
        queue.setRate(0.5, now: 0.5)
        XCTAssertEqual(queue.bufferedMilliseconds(at: 1.0), 250, accuracy: 0.5, "half a second at 0.5× plays 250 ms")
        XCTAssertTrue(queue.tick(now: 1.45).isEmpty)
        XCTAssertEqual(queue.phase, .playing, "at 1.0 it would have drained at t = 1.0")
        XCTAssertEqual(queue.drains, 0)
        // Appending while stretched: 100 ms of audio lasts 200 ms.
        _ = queue.push(chunk(100), now: 1.0 + 0.4)
        XCTAssertEqual(queue.bufferedMilliseconds(at: 1.4), 150, accuracy: 0.5)
        _ = queue.tick(now: 1.69)
        XCTAssertEqual(queue.drains, 0)
        _ = queue.tick(now: 1.71)
        XCTAssertEqual(queue.drains, 1, "drains at 1.5 + 0.2 s, not earlier")
    }

    func testSpeedingUpShortensThePlayhead() {
        var queue = PlaybackQueue(prebufferMilliseconds: 100)
        _ = queue.push(chunk(1_050), now: 0)
        queue.setRate(1.05, now: 0)
        _ = queue.tick(now: 0.99)
        XCTAssertEqual(queue.phase, .playing)
        _ = queue.tick(now: 1.01)
        XCTAssertEqual(queue.phase, .idle, "1 050 ms at 1.05× lasts one second")
    }

    func testSteerStretchesWhenTheBufferIsLowAndCountsStretchedTime() {
        var queue = PlaybackQueue(profile: .openAI)
        // 1 200 ms banked at once: plays at 1.0, then the buffer runs down
        // and the rate falls.
        XCTAssertFalse(queue.push(chunk(1_200), now: 0).isEmpty)
        var t = 0.0
        var lowest = 1.0
        while t < 1.1 {
            t += 0.02
            lowest = min(lowest, queue.steer(now: t))
        }
        XCTAssertLessThan(lowest, 0.95)
        XCTAssertGreaterThan(queue.stretchedSeconds, 0.3)
        XCTAssertEqual(queue.rate, lowest, "never speeds back up without more audio")
    }

    func testFlushResetsTheRate() {
        var queue = PlaybackQueue(profile: .openAI)
        _ = queue.push(chunk(1_200), now: 0)
        for step in 1...50 { queue.steer(now: Double(step) * 0.02) }
        XCTAssertLessThan(queue.rate, 1.0)
        queue.flush()
        XCTAssertEqual(queue.rate, 1.0)
        XCTAssertEqual(queue.ratePolicy.debtSeconds, 0)
    }

    func testProfileSwitchResetsTheTargetAndScalesTheWait() {
        var queue = PlaybackQueue()
        XCTAssertEqual(queue.targetMilliseconds, 150)
        XCTAssertEqual(queue.maximumWait, 0.4, accuracy: 1e-9, "Gemini: 150 + 250 ms, as before")
        queue.setProfile(.openAI)
        XCTAssertEqual(queue.targetMilliseconds, 1_200)
        XCTAssertEqual(queue.targetCeilingMilliseconds, 2_000)
        XCTAssertEqual(queue.maximumWait, 1.45, accuracy: 1e-9, "the wait scales with the target")
    }

    func testCompletionThatBeatsThePredictionStillCountsADrain() {
        var queue = PlaybackQueue(prebufferMilliseconds: 50)
        let out = queue.push(chunk(60), now: 0)
        queue.completed(out[0].id)
        XCTAssertEqual(queue.drains, 1)
        XCTAssertEqual(queue.phase, .idle)
    }
}

// MARK: - Replays

/// One chunk landing at `at` (seconds), belonging to `utterance`.
struct Arrival {
    let at: TimeInterval
    let milliseconds: Int
    let utterance: Int
}

/// A tiny deterministic generator so the timelines are the same on every run.
struct SeededGenerator: RandomNumberGenerator {
    private var state: UInt64
    init(seed: UInt64) { state = seed &* 0x9E37_79B9_7F4A_7C15 | 1 }
    mutating func next() -> UInt64 {
        state ^= state << 13
        state ^= state >> 7
        state ^= state << 17
        return state
    }
}

enum Timelines {
    /// The shape of a synthetic GPT-Live reply.
    struct Shape {
        /// Seconds of audio per reply.
        var replySeconds: ClosedRange<Double> = 2...6
        /// Audio between stalls, seconds.
        var stallEvery: ClosedRange<Double> = 2...4
        var stallSeconds: ClosedRange<Double> = 0.3...1.5
        /// The first 600 ms of a reply lands at once.
        var frontLoad = false

        /// Like the recorded turns: 2–6 s replies, a stall every 2–4 s of audio.
        static let recordedLike = Shape()
        /// Stress: 5–12 s monologues with the same stall rate.
        static let long = Shape(replySeconds: 5...12)
    }

    /// GPT-Live-like replies: 100 ms chunks (what the daemon forwards from
    /// GPT-Live), delivered at `speed` × real time *on average, stalls
    /// included*. Between stalls chunks arrive evenly, a little faster
    /// than `speed` so the average holds. Replies are 6 s of silence apart
    /// after the last chunk.
    static func gptLive(speed: Double, seed: UInt64, shape: Shape = .recordedLike, utterances: Int = 12) -> [Arrival] {
        var rng = SeededGenerator(seed: seed)
        var out: [Arrival] = []
        var t = 0.5
        for u in 0..<utterances {
            let chunks = Int((Double.random(in: shape.replySeconds, using: &rng) * 10).rounded())
            let audio = Double(chunks) / 10
            var stalls: [(afterChunk: Int, seconds: Double)] = []
            var next = Double.random(in: shape.stallEvery, using: &rng)
            while next < audio {
                stalls.append((Int(next * 10), Double.random(in: shape.stallSeconds, using: &rng)))
                next += Double.random(in: shape.stallEvery, using: &rng)
            }
            let wall = audio / speed
            var stallTotal = stalls.reduce(0) { $0 + $1.seconds }
            // Stalls may not eat more than half the wall time.
            if stallTotal > wall / 2 {
                let scale = (wall / 2) / stallTotal
                stalls = stalls.map { ($0.afterChunk, $0.seconds * scale) }
                stallTotal = wall / 2
            }
            let front = shape.frontLoad ? min(6, chunks) : 0
            let dripChunks = chunks - front
            let perChunk = dripChunks > 0 ? (wall - stallTotal) / Double(dripChunks) : 0
            var stallIndex = 0
            for c in 0..<chunks {
                if c >= front, c > 0 { t += perChunk }
                while stallIndex < stalls.count, stalls[stallIndex].afterChunk == c {
                    t += stalls[stallIndex].seconds
                    stallIndex += 1
                }
                out.append(Arrival(at: t, milliseconds: 100, utterance: u))
            }
            t += 6
        }
        return out
    }

    /// The two real GPT-Live turns recorded by `sheepdog audio-e2e
    /// --audio-timeline` (the daemon's `web/voice/testdata/`): /ws binary
    /// frame arrival times in ms, every frame 4 800 bytes = 100 ms.
    static let recordedInstructionPromise: [Int] = [0, 98, 197, 384, 446, 546, 756, 909, 1061, 1161, 1764, 1975, 2077, 2351, 2482, 2551, 2652, 2859, 3005, 3137, 3238, 3365, 3530, 3635, 3738, 3884, 4054, 4086, 4185, 4359]
    static let recordedStatusQuestion: [Int] = [0, 186, 274, 392, 493, 713, 813, 943, 1042, 1234, 1293, 1430, 1531, 1758, 1824, 2008, 2107, 2327, 2426, 2633, 2807, 2865, 2964, 3136, 3239, 3367, 3464]

    /// The recorded turns, alternating, `utterances` of them, 6 s apart.
    /// (`seed` only picks which one goes first.)
    static func recorded(seed: UInt64, utterances: Int = 12) -> [Arrival] {
        var out: [Arrival] = []
        var t = 0.5
        for u in 0..<utterances {
            let turn = (u + Int(seed)) % 2 == 0 ? recordedInstructionPromise : recordedStatusQuestion
            for at in turn { out.append(Arrival(at: t + Double(at) / 1000, milliseconds: 100, utterance: u)) }
            t += Double(turn.last ?? 0) / 1000 + 6
        }
        return out
    }

    /// Gemini-like replies: 4× real time, 40–200 ms chunks, with the
    /// ordinary sub-100 ms jitter between chunks.
    static func gemini(seed: UInt64, utterances: Int = 12) -> [Arrival] {
        var rng = SeededGenerator(seed: seed)
        var out: [Arrival] = []
        var t = 0.5
        for u in 0..<utterances {
            let total = Int.random(in: 2_000...8_000, using: &rng)
            var sum = 0
            while sum < total {
                let ms = Int.random(in: 2...10, using: &rng) * 20
                out.append(Arrival(at: t, milliseconds: ms, utterance: u))
                sum += ms
                t += Double(ms) / 1000 / 4 + Double.random(in: 0...0.07, using: &rng)
            }
            t += 6
        }
        return out
    }
}

/// Replays an arrival timeline through `PlaybackQueue` with a simulated
/// clock and a simulated player node. The node is independent of the
/// queue's own prediction: it consumes audio at the rate the queue last
/// asked for, one 20 ms control step at a time, and reports completions
/// 30 ms late, as iOS does.
struct PlaybackReplay {
    struct Result {
        var underruns = 0
        var drains = 0
        var stretchedSeconds = 0.0
        /// Control steps while playing, and how many of them ran at exactly 1.0.
        var playingSteps = 0
        var unitySteps = 0
        /// Wall time the node sat empty in the middle of a reply (after
        /// its first audio, before its last): what a listener hears as gaps.
        var audibleGapSeconds = 0.0
        var finalTarget = 0
        var minimumRate = 1.0
        var maximumRate = 1.0
        /// Control steps stretched after the reply's last chunk had
        /// already arrived: the tail running below the target.
        var tailStretchSteps = 0
        var unityFraction: Double { playingSteps == 0 ? 1 : Double(unitySteps) / Double(playingSteps) }
    }

    static let step: TimeInterval = 0.02
    static let completionLatency: TimeInterval = 0.03

    /// `stretch: false` reproduces the fixed-rate adaptive buffer this
    /// replaced: `steer` is never called, so the node plays at 1.0 always
    /// and nothing accrues to `stretchedSeconds` — a policy-only comparison
    /// against today's real (stretching) queue, with no separate
    /// production code path to keep alive just for this table.
    static func run(_ arrivals: [Arrival], queue initial: PlaybackQueue, stretch: Bool = true) -> Result {
        var queue = initial
        var result = Result()
        var node: [(id: Int, remaining: Double, utterance: Int)] = []
        var late: [(id: Int, at: TimeInterval)] = []
        var chunkUtterance: [Int: Int] = [:]
        var audioLeft: [Int: Double] = [:]
        for a in arrivals { audioLeft[a.utterance, default: 0] += Double(a.milliseconds) / 1000 }
        var pushes = 0
        var started: Set<Int> = []
        var nodeRate = 1.0
        var index = 0
        var now = 0.0
        let end = (arrivals.last?.at ?? 0) + 30

        func schedule(_ chunks: [PlaybackQueue.Chunk], utterance: Int?) {
            for chunk in chunks {
                let u = chunkUtterance[chunk.id] ?? utterance ?? -1
                node.append((chunk.id, Double(chunk.samples.count) / AudioContract.playbackRate, u))
                started.insert(u)
            }
        }

        while now < end {
            let stepEnd = now + step
            // Arrivals inside this step, at their exact times.
            while index < arrivals.count, arrivals[index].at < stepEnd {
                let a = arrivals[index]
                index += 1
                let samples = [Int16](repeating: 100, count: Int(AudioContract.playbackRate) * a.milliseconds / 1000)
                // The queue assigns ids sequentially from 1, one per
                // non-empty push; remember whose each one is.
                pushes += 1
                chunkUtterance[pushes] = a.utterance
                let released = queue.push(samples, now: max(a.at, now))
                schedule(released, utterance: a.utterance)
            }
            // The node plays this step at the rate last applied.
            var budget = step * nodeRate
            var dryTime = 0.0
            while budget > 1e-12 {
                guard !node.isEmpty else {
                    dryTime = budget / nodeRate
                    break
                }
                let take = min(budget, node[0].remaining)
                node[0].remaining -= take
                budget -= take
                audioLeft[node[0].utterance, default: 0] -= take
                if node[0].remaining <= 1e-12 {
                    late.append((node[0].id, stepEnd + completionLatency))
                    node.removeFirst()
                }
            }
            if dryTime > 0 {
                // Gap inside a reply that has started and still has audio to come.
                let inReply = started.contains { (audioLeft[$0] ?? 0) > 0.001 }
                if inReply { result.audibleGapSeconds += dryTime }
            }
            now = stepEnd
            late.removeAll { item in
                guard item.at <= now else { return false }
                queue.completed(item.id)
                return true
            }
            schedule(queue.tick(now: now), utterance: nil)
            let playing = queue.phase == .playing
            nodeRate = stretch ? queue.steer(now: now) : 1.0
            if playing {
                result.playingSteps += 1
                if nodeRate == 1.0 { result.unitySteps += 1 }
                if nodeRate != 1.0, index >= arrivals.count || arrivals[index].utterance != arrivals[max(index - 1, 0)].utterance {
                    result.tailStretchSteps += 1
                }
                result.minimumRate = min(result.minimumRate, nodeRate)
                result.maximumRate = max(result.maximumRate, nodeRate)
            }
        }
        result.underruns = queue.underruns
        result.drains = queue.drains
        result.stretchedSeconds = queue.stretchedSeconds
        result.finalTarget = queue.targetMilliseconds
        return result
    }
}

/// Before (today's fixed-rate adaptive buffer, Gemini's 150–500 ms target
/// for every provider) against after (time-stretch + per-provider targets)
/// on synthetic GPT-Live-like and Gemini-like timelines.
final class TimeStretchReplayTests: XCTestCase {
    private static let seeds: [UInt64] = [1, 2, 3, 4, 5]

    /// Sums one configuration over every seed.
    private func total(_ timeline: (UInt64) -> [Arrival], _ queue: PlaybackQueue, stretch: Bool = true) -> PlaybackReplay.Result {
        var sum = PlaybackReplay.Result()
        for seed in Self.seeds {
            let r = PlaybackReplay.run(timeline(seed), queue: queue, stretch: stretch)
            sum.underruns += r.underruns
            sum.drains += r.drains
            sum.audibleGapSeconds += r.audibleGapSeconds
            sum.stretchedSeconds += r.stretchedSeconds
            sum.playingSteps += r.playingSteps
            sum.unitySteps += r.unitySteps
            sum.tailStretchSteps += r.tailStretchSteps
            sum.minimumRate = min(sum.minimumRate, r.minimumRate)
            sum.maximumRate = max(sum.maximumRate, r.maximumRate)
        }
        return sum
    }

    /// before: today's fixed-rate adaptive buffer (Gemini's 150–500 ms for
    /// every provider). targets only: provider targets, no stretch.
    /// after: time-stretch + provider targets.
    private func compare(_ name: String, provider: PlaybackProfile, _ timeline: (UInt64) -> [Arrival]) -> (before: PlaybackReplay.Result, targetsOnly: PlaybackReplay.Result, after: PlaybackReplay.Result) {
        let before = total(timeline, PlaybackQueue(profile: .gemini), stretch: false)
        let targetsOnly = total(timeline, PlaybackQueue(profile: provider), stretch: false)
        let after = total(timeline, PlaybackQueue(profile: provider))
        let replies = Self.seeds.count * 12
        print(String(
            format: "STRETCH-TABLE | %@ | %d replies | before %d underruns (%.1f s gaps) | targets only %d (%.1f s) | after %d (%.1f s) | stretched %.1f s, r %.2f-%.2f, at 1.0 %.1f%%, tail %.1f%%",
            name, replies,
            before.underruns, before.audibleGapSeconds,
            targetsOnly.underruns, targetsOnly.audibleGapSeconds,
            after.underruns, after.audibleGapSeconds,
            after.stretchedSeconds, after.minimumRate, after.maximumRate,
            after.unityFraction * 100,
            after.playingSteps == 0 ? 0 : Double(after.tailStretchSteps) / Double(after.playingSteps) * 100
        ))
        return (before, targetsOnly, after)
    }

    func testRecordedGPTLiveTurns() {
        let r = compare("GPT-Live recorded turns (0.69x/0.78x)", provider: .openAI) { Timelines.recorded(seed: $0) }
        XCTAssertGreaterThan(r.before.underruns, 20, "the recording reproduces the phone's gaps")
        // Not zero: both turns arrive below the 0.85 rate floor (0.69×
        // and 0.78×), so an 800 ms start runs out near the end of a turn;
        // the target then settles where +100 ms per underrun balances
        // −20 ms per clean reply (about one reply in five).
        XCTAssertLessThanOrEqual(r.after.underruns, r.before.underruns / 2)
        XCTAssertLessThan(r.after.audibleGapSeconds, r.before.audibleGapSeconds / 2)
    }

    func testGPTLiveAtThreeQuartersRealTime() {
        let r = compare("GPT-Live 0.75x, 2-6 s replies, stalls 0.3-1.5 s", provider: .openAI) { Timelines.gptLive(speed: 0.75, seed: $0) }
        XCTAssertGreaterThan(r.before.underruns, 20, "the timeline reproduces the phone's problem")
        XCTAssertLessThanOrEqual(r.after.underruns, r.before.underruns / 2)
        XCTAssertGreaterThanOrEqual(r.after.minimumRate, PlaybackRatePolicy.floorRate - 1e-9)
    }

    func testGPTLiveAtEightyFivePercentRealTime() {
        let r = compare("GPT-Live 0.85x, 2-6 s replies, stalls 0.3-1.5 s", provider: .openAI) { Timelines.gptLive(speed: 0.85, seed: $0) }
        XCTAssertGreaterThan(r.before.underruns, 20)
        XCTAssertLessThanOrEqual(r.after.underruns, r.before.underruns / 2)
    }

    func testGPTLiveWithAFastStart() {
        var shape = Timelines.Shape.recordedLike
        shape.frontLoad = true
        let r = compare("GPT-Live 0.75x + fast start", provider: .openAI) { Timelines.gptLive(speed: 0.75, seed: $0, shape: shape) }
        XCTAssertLessThan(r.after.underruns, r.before.underruns)
    }

    /// Long monologues at 0.75× cannot be fixed by any client buffer that
    /// starts playing within a second: the source is 25 % short for the
    /// whole reply and even the 0.85 floor consumes faster than it
    /// arrives. The stress rows show how much the stretch still helps.
    func testLongGPTLiveMonologuesStillImprove() {
        let slow = compare("GPT-Live 0.75x, 5-12 s replies (stress)", provider: .openAI) { Timelines.gptLive(speed: 0.75, seed: $0, shape: .long) }
        XCTAssertLessThan(slow.after.underruns, slow.before.underruns)
        let fast = compare("GPT-Live 0.85x, 5-12 s replies (stress)", provider: .openAI) { Timelines.gptLive(speed: 0.85, seed: $0, shape: .long) }
        XCTAssertLessThan(fast.after.underruns, fast.before.underruns)
    }

    func testGeminiAtFourTimesRealTimePlaysAtOneAlmostAlways() {
        let r = compare("Gemini 4x", provider: .gemini) { Timelines.gemini(seed: $0) }
        XCTAssertEqual(r.before.underruns, 0)
        XCTAssertEqual(r.after.underruns, 0)
        // What is not 1.0 is the last ~150 ms of each reply (the tail runs
        // below the target once the final chunk is in) and brief dips when
        // two slow chunks meet at the start; catch-up only ever repays those.
        XCTAssertGreaterThanOrEqual(r.after.unityFraction, 0.9, "Gemini is ahead of real time: almost nothing to stretch")
    }
}
