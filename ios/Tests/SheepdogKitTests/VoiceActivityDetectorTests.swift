import XCTest
@testable import SheepdogKit

/// The voice-activity detector against macOS text-to-speech
/// (`scripts/make-fixtures.sh`) mixed with generated noise — white, pink,
/// brown, babble (the other TTS voices at once) and keyboard clicks — at
/// 20, 10 and 5 dB SNR. The numbers these print are the table in
/// docs/architecture.md.
///
/// Pure computation on committed fixtures: no host app, microphone or
/// network needed.
final class VoiceActivityDetectorTests: XCTestCase {
    typealias Audio = VADTestAudio

    private let pairs = [["samantha", "daniel"], ["karen", "moira"], ["fred", "rishi"]]
    private let snrs: [Double] = [20, 10, 5]

    // MARK: - Fixtures

    func testTheSpeechFixturesAreTTSAtTheCaptureFormat() throws {
        for voice in Audio.voices {
            let samples = try Audio.speech(voice)
            let seconds = Double(samples.count) / Double(Audio.rate)
            XCTAssertGreaterThan(seconds, 1, voice)
            XCTAssertLessThan(seconds, 6, voice)
            XCTAssertGreaterThan(PCMLevel.peak(of: samples), 3_000, "\(voice) should be actual speech")
        }
    }

    // MARK: - Speech opens the gate

    func testSpeechOpensWithin250msAndIsCarriedWhole() throws {
        var table: [String] = ["noise   SNR  median  worst  coverage  noise-opens"]
        for noise in Audio.Noise.allCases {
            for snr in snrs {
                var latencies: [Int] = []
                var coverage: [Double] = []
                var noiseOpens = 0
                for (i, pair) in pairs.enumerated() {
                    let result = try Audio.speechScenario(noise: noise, snr: snr, voicesUsed: pair, seed: UInt64(11 + i))
                    XCTAssertEqual(result.utterances.count, 2)
                    for u in result.utterances {
                        latencies.append(u.latencyMs ?? 10_000)
                        coverage.append(u.coverage)
                    }
                    noiseOpens += result.noiseOpens
                }
                latencies.sort()
                table.append(String(format: "%-7@ %3.0f  %4d ms %4d ms  %5.1f%%   %d", noise.rawValue as NSString, snr, latencies[latencies.count / 2], latencies.last ?? 0, 100 * (coverage.min() ?? 0), noiseOpens))

                // Babble at 5 dB — one voice barely above a room of others —
                // is the one condition where a short opening word can be
                // lost; it is asserted on its own below.
                if noise == .babble, snr == 5 { continue }
                for latency in latencies {
                    XCTAssertLessThanOrEqual(latency, 250, "\(noise) at \(snr) dB: opened after \(latency) ms")
                }
                for c in coverage {
                    XCTAssertGreaterThanOrEqual(c, 0.95, "\(noise) at \(snr) dB: only \(Int(c * 100))% of the voice was sent")
                }
                XCTAssertEqual(noiseOpens, 0, "\(noise) at \(snr) dB: the gate opened on the noise between utterances")
            }
        }
        print("VAD speech results\n" + table.joined(separator: "\n"))
    }

    func testBabbleAt5dBStillCatchesMostSpeech() throws {
        var fast = 0, total = 0
        var coverage: [Double] = []
        for (i, pair) in pairs.enumerated() {
            let result = try Audio.speechScenario(noise: .babble, snr: 5, voicesUsed: pair, seed: UInt64(11 + i))
            for u in result.utterances {
                total += 1
                if let l = u.latencyMs, l <= 250 { fast += 1 }
                coverage.append(u.coverage)
            }
            XCTAssertEqual(result.noiseOpens, 0)
        }
        XCTAssertGreaterThanOrEqual(fast, total - 2, "at most two of six utterances may open late in 5 dB babble")
        XCTAssertGreaterThanOrEqual(coverage.reduce(0, +) / Double(coverage.count), 0.8)
    }

    // MARK: - Noise keeps it closed

    func testNoiseAloneKeepsTheGateClosed() throws {
        var table: [String] = ["noise   level     opens in 60 s"]
        for noise in Audio.Noise.allCases {
            for level in [-31.0, -45, -60] {
                let result = try Audio.noiseScenario(noise: noise, levelDBFS: level, seconds: 62)
                table.append(String(format: "%-7@ %4.0f dBFS  %d", noise.rawValue as NSString, level, result.opens))
                // The brief allows one spurious open per 10 s; the detector
                // should do far better than that.
                XCTAssertLessThanOrEqual(Double(result.opens), result.seconds / 10, "\(noise) at \(level) dBFS")
                XCTAssertEqual(result.opens, 0, "\(noise) at \(level) dBFS opened \(result.opens) times")
            }
        }
        print("VAD noise-only results\n" + table.joined(separator: "\n"))
    }

    func testKeyboardClicksDoNotOpenIt() throws {
        // Loud clicks over a quiet room, from the generator, and one crafted
        // click per frame for a second: each fills one 10 ms sub-block.
        let result = try Audio.noiseScenario(noise: .clicks, levelDBFS: -20, seconds: 30)
        XCTAssertEqual(result.opens, 0)

        var vad = VoiceActivityDetector()
        var rng = Audio.RNG(seed: 3)
        let room = { (rng: inout Audio.RNG) -> [Int16] in (0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 20) } }
        for _ in 0 ..< 75 { vad.push(room(&rng)) }
        XCTAssertTrue(vad.isGated)
        var opened = false
        for _ in 0 ..< 25 {
            var frame = room(&rng)
            for i in 40 ..< 120 { frame[i] = Int16(clamping: Int(rng.uniform() * 20_000)) }
            if vad.push(frame).transition == .opened { opened = true }
        }
        XCTAssertFalse(opened, "a click in every frame still leaves three quiet sub-blocks")
    }

    // MARK: - The floor follows the room

    func testTheFloorAdaptsWhenTheRoomGetsLouderAndQuieter() throws {
        let quiet = Audio.scaled(try Audio.noise(.white, samples: 6 * Audio.rate, seed: 5), toDBFS: -60)
        let loud = Audio.scaled(try Audio.noise(.white, samples: 12 * Audio.rate, seed: 6), toDBFS: -35)
        let quietAgain = Audio.scaled(try Audio.noise(.white, samples: 6 * Audio.rate, seed: 8), toDBFS: -60)
        var vad = VoiceActivityDetector()
        let f = Audio.frames(Audio.pcm(quiet + loud + quietAgain))
        let quietFrames = 6_000 / Audio.frameMs, loudFrames = 12_000 / Audio.frameMs

        var floorQuiet = 0.0, floorLoud = 0.0
        var loudOpens = 0
        var closedForGood: Int?
        for (i, frame) in f.enumerated() {
            let out = vad.push(frame)
            if i == quietFrames - 1 { floorQuiet = vad.floorDBFS }
            if i >= quietFrames, i < quietFrames + loudFrames {
                if out.transition == .opened { loudOpens += 1 }
                if vad.isGated { closedForGood = closedForGood ?? i } else { closedForGood = nil }
            }
            if i == quietFrames + loudFrames - 1 { floorLoud = vad.floorDBFS }
        }
        // 25 dB louder: the floor follows (on its speech-band measure, a few
        // dB under the full-band level) and the gate settles closed within
        // the floor window.
        XCTAssertEqual(floorLoud - floorQuiet, 25, accuracy: 2, "the floor should move with the room")
        XCTAssertLessThanOrEqual(loudOpens, 1, "a jump in the room may open the gate once, while the floor catches up")
        let settled = try XCTUnwrap(closedForGood, "the gate should close again in the louder room")
        XCTAssertLessThanOrEqual((settled - quietFrames) * Audio.frameMs, 5_000)

        // Back to quiet: the floor drops within a second or two (the low
        // percentile falls as soon as a few quiet frames are in the window).
        XCTAssertEqual(vad.floorDBFS, floorQuiet, accuracy: 2)
        XCTAssertTrue(vad.isGated)
    }

    func testAfterALoudRoomQuietSpeechStillOpens() throws {
        // A fan turned off: the floor has to come down before the next word.
        let loud = Audio.scaled(try Audio.noise(.pink, samples: 5 * Audio.rate, seed: 9), toDBFS: -35)
        let quiet = Audio.scaled(try Audio.noise(.pink, samples: 2 * Audio.rate, seed: 10), toDBFS: -60)
        let speech = try Audio.speech("karen").map(Double.init)
        let voice = Audio.scaled(speech, toDBFS: -45)
        let tail = Audio.scaled(try Audio.noise(.pink, samples: voice.count, seed: 12), toDBFS: -60)
        var vad = VoiceActivityDetector()
        let trace = Audio.run(Audio.pcm(loud + quiet + zip(voice, tail).map { $0 + $1 }), vad: &vad)
        let speechStart = (loud.count + quiet.count) / Audio.frame
        XCTAssertTrue(trace.opens.contains { $0 >= speechStart && $0 < speechStart + 20 }, "quiet speech after the fan stopped should open the gate")
    }

    // MARK: - The gate's contract

    func testItStartsOpenLikeANewClientAndClosesOnceItHasHeardTheRoom() {
        var vad = VoiceActivityDetector()
        XCTAssertFalse(vad.isGated)
        var rng = Audio.RNG(seed: 1)
        var closedAt: Int?
        for i in 0 ..< 50 {
            let frame = (0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 100) }
            let out = vad.push(frame)
            if out.transition == .closed { closedAt = i }
            if closedAt == nil { XCTAssertEqual(out.send.count, 1, "frames flow until it closes") }
        }
        let at = try? XCTUnwrap(closedAt)
        XCTAssertEqual(at, 9, "closes after the 400 ms hangover")
    }

    func testReopeningReleasesTheAttackAndThePreroll() {
        var tuning = VADTuning()
        tuning.hangoverMilliseconds = 80
        var vad = VoiceActivityDetector(tuning: tuning)
        var rng = Audio.RNG(seed: 2)
        for _ in 0 ..< 100 { vad.push((0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 50) }) }
        XCTAssertTrue(vad.isGated)
        let tone = (0 ..< Audio.frame).map { Int16(8_000 * sin(2 * Double.pi * 800 * Double($0) / 16_000)) }
        var out = CaptureGate.Output(send: [], transition: nil)
        for _ in 0 ..< 3 { out = vad.push(tone) }
        XCTAssertEqual(out.transition, .opened, "three voiced frames (120 ms) open it")
        // The attack window (7 frames) plus 100 ms of pre-roll (3), then the
        // opening frame itself: nothing said is lost.
        XCTAssertEqual(out.send.count, 11)
        XCTAssertEqual(out.send.last, tone)
    }

    func testAShortBurstCountsAsASpuriousOpenAndSpeechDoesNot() throws {
        var vad = VoiceActivityDetector()
        var rng = Audio.RNG(seed: 4)
        let room = { (rng: inout Audio.RNG) -> [Int16] in (0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 30) } }
        for _ in 0 ..< 100 { vad.push(room(&rng)) }
        let tone = (0 ..< Audio.frame).map { Int16(6_000 * sin(2 * Double.pi * 600 * Double($0) / 16_000)) }
        for _ in 0 ..< 4 { vad.push(tone) } // 160 ms: a door, a cough
        for _ in 0 ..< 20 { vad.push(room(&rng)) }
        XCTAssertEqual(vad.opens, 1)
        XCTAssertEqual(vad.spuriousOpens, 1)

        let speech = Audio.frames(Audio.pcm(Audio.scaled(try Audio.speech("moira").map(Double.init), toDBFS: -30)))
        for frame in speech { vad.push(frame) }
        for _ in 0 ..< 20 { vad.push(room(&rng)) }
        XCTAssertGreaterThanOrEqual(vad.opens, 2)
        XCTAssertEqual(vad.spuriousOpens, 1, "a sentence is not spurious")
    }

    func testResetKeepsTheFloorUnlessAsked() {
        var vad = VoiceActivityDetector()
        var rng = Audio.RNG(seed: 6)
        for _ in 0 ..< 100 { vad.push((0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 300) }) }
        let floor = vad.floorDBFS
        vad.reset()
        XCTAssertFalse(vad.isGated)
        XCTAssertEqual(vad.floorDBFS, floor)
        vad.reset(keepFloor: false)
        XCTAssertEqual(vad.floorDBFS, VoiceActivityDetector.silenceDBFS)
        XCTAssertEqual(vad.marginDB, vad.tuning.openMarginDB)
    }

    func testTheMarginWidensForAnUnsteadyRoom() throws {
        func margin(after noise: Audio.Noise) throws -> Double {
            let raw = Audio.scaled(try Audio.noise(noise, samples: 8 * Audio.rate, seed: 21), toDBFS: -40)
            var vad = VoiceActivityDetector()
            for frame in Audio.frames(Audio.pcm(raw)) { vad.push(frame) }
            return vad.marginDB
        }
        XCTAssertEqual(try margin(after: .white), 6, accuracy: 0.01, "a steady hiss gets the minimum margin")
        XCTAssertGreaterThan(try margin(after: .babble), 7, "other voices get a wider one")
    }

    func testDigitalSilenceDoesNotMakeEveryHissSpeech() {
        var vad = VoiceActivityDetector()
        for _ in 0 ..< 100 { vad.push([Int16](repeating: 0, count: Audio.frame)) }
        XCTAssertEqual(vad.floorDBFS, vad.tuning.minimumFloorDBFS)
        var rng = Audio.RNG(seed: 8)
        var opened = false
        // −75 dBFS of hiss: audible to nobody.
        for _ in 0 ..< 50 where vad.push((0 ..< Audio.frame).map { _ in Int16(rng.gaussian() * 6) }).transition == .opened { opened = true }
        XCTAssertFalse(opened)
    }
}
