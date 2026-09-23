import Foundation
@testable import SheepdogKit

/// Test audio for the voice-activity detector: the TTS speech fixtures from
/// `scripts/make-fixtures.sh`, noise generated here (deterministic, seeded),
/// and the mixing and scoring that turn them into the numbers in the tests
/// and in docs/architecture.md.
enum VADTestAudio {
    static let rate = 16_000
    static let frame = AudioContract.frameSamples
    static let frameMs = AudioContract.frameMilliseconds

    // MARK: - Speech fixtures

    static let voices = ["samantha", "daniel", "karen", "moira", "fred", "rishi"]

    /// Where the WAVs are: the test bundle when Xcode copied them in, else
    /// next to this source file (a plain `swiftc` harness).
    static func speech(_ voice: String) throws -> [Int16] {
        let name = "speech-\(voice)"
        let bundle = Bundle(for: BundleToken.self)
        if let url = bundle.url(forResource: name, withExtension: "wav") {
            return try wavSamples(Data(contentsOf: url))
        }
        let local = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Audio/\(name).wav")
        return try wavSamples(Data(contentsOf: local))
    }

    enum WAVError: Error { case notPCM16Mono16k(String) }

    /// Minimal RIFF reader: PCM16 LE mono at 16 kHz, which is what
    /// `afconvert -f WAVE -d LEI16@16000 -c 1` writes.
    static func wavSamples(_ data: Data) throws -> [Int16] {
        let bytes = [UInt8](data)
        func u32(_ at: Int) -> Int { Int(bytes[at]) | Int(bytes[at + 1]) << 8 | Int(bytes[at + 2]) << 16 | Int(bytes[at + 3]) << 24 }
        func u16(_ at: Int) -> Int { Int(bytes[at]) | Int(bytes[at + 1]) << 8 }
        guard bytes.count > 12, String(bytes: bytes[0 ..< 4], encoding: .ascii) == "RIFF" else { throw WAVError.notPCM16Mono16k("not RIFF") }
        var at = 12
        var channels = 0, sampleRate = 0, bits = 0
        while at + 8 <= bytes.count {
            let id = String(bytes: bytes[at ..< at + 4], encoding: .ascii) ?? ""
            let size = u32(at + 4)
            let body = at + 8
            if id == "fmt " {
                channels = u16(body + 2)
                sampleRate = u32(body + 4)
                bits = u16(body + 14)
            } else if id == "data" {
                guard channels == 1, sampleRate == rate, bits == 16 else {
                    throw WAVError.notPCM16Mono16k("\(channels) ch, \(sampleRate) Hz, \(bits) bit")
                }
                let end = min(bytes.count, body + size)
                var out = [Int16]()
                out.reserveCapacity((end - body) / 2)
                var i = body
                while i + 1 < end {
                    out.append(Int16(bitPattern: UInt16(bytes[i]) | UInt16(bytes[i + 1]) << 8))
                    i += 2
                }
                return out
            }
            at = body + size + (size & 1)
        }
        throw WAVError.notPCM16Mono16k("no data chunk")
    }

    // MARK: - Noise

    enum Noise: String, CaseIterable {
        case white, pink, brown, babble, clicks
    }

    /// A seeded generator so every run hears the same noise.
    struct RNG {
        var state: UInt64
        init(seed: UInt64) { state = seed }
        mutating func next() -> UInt64 {
            state &+= 0x9E37_79B9_7F4A_7C15
            var z = state
            z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
            z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
            return z ^ (z >> 31)
        }
        /// Uniform in [-1, 1).
        mutating func uniform() -> Double { Double(next() >> 11) / Double(1 << 52) - 1 }
        mutating func gaussian() -> Double {
            var u = 0.0
            for _ in 0 ..< 6 { u += uniform() }
            return u / sqrt(2.0) // variance 6/3 = 2 → 1
        }
        mutating func int(_ range: ClosedRange<Int>) -> Int {
            range.lowerBound + Int(next() % UInt64(range.count))
        }
    }

    /// Noise of a kind, as doubles with unit-ish scale (normalise with `scaled`).
    static func noise(_ kind: Noise, samples n: Int, seed: UInt64 = 7, excluding voice: String? = nil) throws -> [Double] {
        var rng = RNG(seed: seed)
        switch kind {
        case .white:
            return (0 ..< n).map { _ in rng.gaussian() }
        case .pink:
            // Paul Kellet's refined pink filter.
            var b0 = 0.0, b1 = 0.0, b2 = 0.0, b3 = 0.0, b4 = 0.0, b5 = 0.0, b6 = 0.0
            return (0 ..< n).map { _ in
                let w = rng.gaussian()
                b0 = 0.99886 * b0 + w * 0.0555179
                b1 = 0.99332 * b1 + w * 0.0750759
                b2 = 0.96900 * b2 + w * 0.1538520
                b3 = 0.86650 * b3 + w * 0.3104856
                b4 = 0.55000 * b4 + w * 0.5329522
                b5 = -0.7616 * b5 - w * 0.0168980
                let out = b0 + b1 + b2 + b3 + b4 + b5 + b6 + w * 0.5362
                b6 = w * 0.115926
                return out
            }
        case .brown:
            var y = 0.0
            return (0 ..< n).map { _ in
                y = 0.995 * y + 0.1 * rng.gaussian()
                return y
            }
        case .babble:
            return try noiseExcluding(.babble, samples: n, seed: seed, excluding: voice.map { [$0] } ?? [])
        case .clicks:
            // A keyboard: 3–8 ms decaying bursts, 80–350 ms apart, over a
            // quiet room (−40 dB below the clicks' own peak level).
            var out = (0 ..< n).map { _ in rng.gaussian() * 0.02 }
            var i = rng.int(0 ... 1_600)
            while i < n {
                let length = rng.int(48 ... 128)
                let amplitude = 0.6 + 0.4 * abs(rng.uniform())
                for k in 0 ..< length where i + k < n {
                    let decay = exp(-Double(k) / Double(length) * 5)
                    out[i + k] += amplitude * decay * rng.uniform()
                }
                i += rng.int(1_280 ... 5_600)
            }
            return out
        }
    }

    // MARK: - Levels and mixing

    static func rmsDB(_ x: [Double]) -> Double {
        guard !x.isEmpty else { return -120 }
        let ms = x.reduce(0.0) { $0 + $1 * $1 } / Double(x.count)
        return ms > 0 ? 10 * log10(ms / (32_768.0 * 32_768.0)) : -120
    }

    /// `x` scaled so its RMS is `db` dBFS.
    static func scaled(_ x: [Double], toDBFS db: Double) -> [Double] {
        let now = rmsDB(x)
        guard now > -119 else { return x }
        let gain = pow(10, (db - now) / 20)
        return x.map { $0 * gain }
    }

    /// Frames of clean speech that carry the voice: within 30 dB of its
    /// loudest frame. Their mean power is the speech level SNR is quoted
    /// against (active speech level, not diluted by the pauses).
    static func activeFrames(_ clean: [Double]) -> [Bool] {
        let levels = frameLevels(clean)
        let top = levels.max() ?? -120
        return levels.map { $0 > top - 30 }
    }

    /// Frames where the voice is actually above the noise (local SNR ≥ 0 dB
    /// on the clean signal). Coverage is measured over these: a consonant
    /// 20 dB under the room is not there to detect.
    static func audibleFrames(_ clean: [Double], noiseDBFS: Double) -> [Bool] {
        frameLevels(clean).map { $0 >= noiseDBFS }
    }

    /// Frames where the voice is clearly above the noise (by `margin` dB) in
    /// the detector's speech band (300–3 400 Hz). Onset latency is measured
    /// from the first of these: the first vowel, not the "s" of "Stop" —
    /// that lives above 4 kHz or a couple of dB over the room, where
    /// nothing can tell it from noise, and is recovered by the pre-roll
    /// instead (which the coverage figure checks).
    static func bandAudibleFrames(_ clean: [Double], noise: [Double], margin: Double = 6) -> [Bool] {
        var filter = SpeechBandFilter(sampleRate: Double(rate), low: 300, high: 3_400)
        let cleanBand = filter.process(pcm(clean))
        filter = SpeechBandFilter(sampleRate: Double(rate), low: 300, high: 3_400)
        let noiseBand = rmsDB(filter.process(pcm(noise)))
        return frameLevels(cleanBand).map { $0 >= noiseBand + margin }
    }

    static func frameLevels(_ x: [Double]) -> [Double] {
        stride(from: 0, to: x.count - frame + 1, by: frame).map { start in
            rmsDB(Array(x[start ..< start + frame]))
        }
    }

    static func activeLevelDB(_ clean: [Double]) -> Double {
        let active = activeFrames(clean)
        var power = 0.0, count = 0
        for (i, on) in active.enumerated() where on {
            for s in clean[i * frame ..< (i + 1) * frame] { power += s * s }
            count += frame
        }
        guard count > 0, power > 0 else { return -120 }
        return 10 * log10(power / Double(count) / (32_768.0 * 32_768.0))
    }

    static func pcm(_ x: [Double]) -> [Int16] {
        x.map { Int16(clamping: Int(($0).rounded())) }
    }

    static func frames(_ x: [Int16]) -> [[Int16]] {
        stride(from: 0, to: x.count - frame + 1, by: frame).map { Array(x[$0 ..< $0 + frame]) }
    }

    // MARK: - Running a gate

    /// What happened, frame by frame, when a gate heard a signal.
    struct Trace {
        /// Per input frame: was it sent (live, or later as pre-roll)?
        var sent: [Bool]
        /// Frame indices at which the gate opened / closed.
        var opens: [Int]
        var closes: [Int]
        /// Per input frame: the gate's floor (VAD only), dBFS.
        var floor: [Double]
        var gatedAfter: [Bool]
    }

    static func run(_ signal: [Int16], vad: inout VoiceActivityDetector) -> Trace {
        let input = frames(signal)
        var trace = Trace(sent: Array(repeating: false, count: input.count), opens: [], closes: [], floor: [], gatedAfter: [])
        for (i, f) in input.enumerated() {
            let out = vad.push(f)
            for k in 0 ..< out.send.count {
                let index = i - (out.send.count - 1) + k
                if index >= 0 { trace.sent[index] = true }
            }
            if out.transition == .opened { trace.opens.append(i) }
            if out.transition == .closed { trace.closes.append(i) }
            trace.floor.append(vad.floorDBFS)
            trace.gatedAfter.append(vad.isGated)
        }
        return trace
    }

    // MARK: - Scenarios

    /// One utterance scored: when the voice started, how long the gate
    /// took, and how much of the voice got through.
    struct Utterance {
        var voice: String
        var onsetFrame: Int
        var latencyMs: Int?
        var coverage: Double
        var closesInside: Int
    }

    struct SpeechResult {
        var noise: Noise
        var snr: Double
        var utterances: [Utterance]
        /// Opens that happened in the noise-only stretches.
        var noiseOpens: Int
        var noiseSeconds: Double
    }

    static let speechLevelDBFS = -26.0

    /// Noise throughout; two utterances from different voices at
    /// `speechLevelDBFS` active level with the noise `snr` dB below them.
    /// 2 s of noise first (the gate starts open and has to learn the room),
    /// 1.5 s between and after.
    static func speechScenario(noise kind: Noise, snr: Double, voicesUsed: [String] = ["samantha", "daniel"], seed: UInt64 = 11, tuning: VADTuning = VADTuning()) throws -> SpeechResult {
        let lead = 2 * rate, gap = 3 * rate / 2
        var clean: [Double] = Array(repeating: 0, count: lead)
        var onsets: [(String, Int, Range<Int>)] = []
        for voice in voicesUsed {
            let s = try speech(voice).map(Double.init)
            let scaled = scaled(s, toDBFS: speechLevelDBFS + (rmsDB(s) - activeLevelDB(s)))
            let start = clean.count
            clean += scaled
            onsets.append((voice, start, start ..< clean.count))
            clean += Array(repeating: 0, count: gap)
        }
        let n = clean.count
        let exclude = kind == .babble ? voicesUsed : []
        var raw = try noiseExcluding(kind, samples: n, seed: seed, excluding: exclude)
        raw = scaled(raw, toDBFS: speechLevelDBFS - snr)
        let mixed = pcm(zip(clean, raw).map { $0 + $1 })

        var vad = VoiceActivityDetector(tuning: tuning)
        let trace = run(mixed, vad: &vad)
        let active = activeFrames(clean)
        let audible = audibleFrames(clean, noiseDBFS: speechLevelDBFS - snr)
        let bandAudible = bandAudibleFrames(clean, noise: raw)

        var utterances: [Utterance] = []
        var speechFrames = Set<Int>()
        for (voice, _, range) in onsets {
            let first = range.lowerBound / frame
            let last = min(active.count, (range.upperBound + frame - 1) / frame)
            let activeHere = (first ..< last).filter { active[$0] }
            guard let end = activeHere.last, let onset = (first ..< last).first(where: { bandAudible[$0] }) else { continue }
            // Hangover and attack belong to the utterance, not to the noise.
            for i in (activeHere.first ?? onset) ... min(active.count - 1, end + 15) { speechFrames.insert(i) }
            let latency: Int?
            if trace.gatedAfter.indices.contains(onset - 1), !trace.gatedAfter[onset - 1] {
                latency = 0 // already open (a spurious open is counted separately)
            } else if let open = trace.opens.first(where: { $0 >= onset && $0 <= end }) {
                latency = (open - onset + 1) * frameMs
            } else {
                latency = nil
            }
            // Coverage over the audible frames: how much of what could be
            // heard reached the socket (live, or as pre-roll).
            let audibleHere = (first ..< last).filter { audible[$0] }
            let covered = audibleHere.filter { trace.sent[$0] }.count
            let closes = trace.closes.filter { $0 > onset && $0 < end }.count
            utterances.append(Utterance(voice: voice, onsetFrame: onset, latencyMs: latency, coverage: Double(covered) / Double(max(1, audibleHere.count)), closesInside: closes))
        }
        // Noise-only opens: after the 2 s warm-up, outside the utterances.
        let warmup = lead / frame / 2
        let noiseOpens = trace.opens.filter { $0 >= warmup && !speechFrames.contains($0) }.count
        let noiseFrames = (warmup ..< active.count).filter { !speechFrames.contains($0) }.count
        return SpeechResult(noise: kind, snr: snr, utterances: utterances, noiseOpens: noiseOpens, noiseSeconds: Double(noiseFrames * frameMs) / 1000)
    }

    static func noiseExcluding(_ kind: Noise, samples n: Int, seed: UInt64, excluding: [String]) throws -> [Double] {
        guard kind == .babble else { return try noise(kind, samples: n, seed: seed) }
        var rng = RNG(seed: seed)
        var out = [Double](repeating: 0, count: n)
        for voice in voices where !excluding.contains(voice) {
            let s = try speech(voice)
            guard !s.isEmpty else { continue }
            let rms = sqrt(s.reduce(0.0) { $0 + Double($1) * Double($1) } / Double(s.count))
            let offset = rng.int(0 ... s.count - 1)
            for i in 0 ..< n { out[i] += Double(s[(i + offset) % s.count]) / max(1, rms) }
        }
        return out
    }

    struct NoiseResult {
        var noise: Noise
        var levelDBFS: Double
        var opens: Int
        var seconds: Double
    }

    /// Noise alone for `seconds`, at `levelDBFS`; opens after a 2 s warm-up.
    static func noiseScenario(noise kind: Noise, levelDBFS: Double, seconds: Int = 20, seed: UInt64 = 23, tuning: VADTuning = VADTuning()) throws -> NoiseResult {
        let n = seconds * rate
        let raw = scaled(try noiseExcluding(kind, samples: n, seed: seed, excluding: []), toDBFS: levelDBFS)
        var vad = VoiceActivityDetector(tuning: tuning)
        let trace = run(pcm(raw), vad: &vad)
        let warmup = 2_000 / frameMs
        return NoiseResult(noise: kind, levelDBFS: levelDBFS, opens: trace.opens.filter { $0 >= warmup }.count, seconds: Double(seconds - 2))
    }
}

private final class BundleToken {}
