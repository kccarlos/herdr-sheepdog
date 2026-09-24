import Foundation

/// Every number the voice-activity detector uses, in one place.
///
/// The defaults were tuned against macOS text-to-speech mixed with white,
/// pink, brown, babble and keyboard-click noise at 20, 10 and 5 dB SNR
/// (`VoiceActivityDetectorTests`; the table is in docs/architecture.md).
public struct VADTuning: Equatable, Sendable {
    /// Capture sample rate the frames arrive at.
    public var sampleRate: Double = AudioContract.captureRate
    /// Duration of one frame. 40 ms on iOS.
    public var frameMilliseconds: Int = AudioContract.frameMilliseconds

    /// Each frame is cut into this many sub-blocks (10 ms at 40 ms frames)
    /// and its level is the *second-quietest* sub-block's energy. Speech is
    /// sustained across a frame, so that barely moves it; a keyboard click
    /// fills one or two sub-blocks and is ignored.
    public var subBlocks: Int = 4

    /// Measure the level in the speech band only (a 2nd-order high-pass and
    /// low-pass at these corners). Fans, HVAC, traffic and handling rumble
    /// live mostly below 300 Hz; hiss above 3.4 kHz carries no intelligibility.
    public var speechBandOnly = true
    public var bandLowHz: Double = 300
    public var bandHighHz: Double = 3_400

    /// A frame counts as voiced when it is at least the open margin above
    /// the noise floor. The margin adapts to how steady the room is: a fan
    /// or hiss barely moves from frame to frame, so a small margin is safe
    /// and catches quiet speech; a room of other voices (babble) swings by
    /// several dB, so it needs the full margin. The swing is measured as the
    /// spread of the quiet end of the level history (15th minus 2nd
    /// percentile — speech does not reach down there), and the margin is
    /// `marginBaseDB + marginPerSpread × spread`, clamped to
    /// `minimumOpenMarginDB … openMarginDB`.
    public var openMarginDB: Double = 10
    public var minimumOpenMarginDB: Double = 6
    public var marginBaseDB: Double = 4
    public var marginPerSpread: Double = 2
    /// Once open, a frame still counts as voiced down to the open margin
    /// less this (hysteresis, so a syllable's tail does not flicker the gate).
    public var hysteresisDB: Double = 3
    /// Voiced audio needed before the gate opens, within the last
    /// `attackWindowMilliseconds`. A window rather than a run: speech dips
    /// between syllables (a stop consonant, a breath) and a strict run
    /// restarts on every dip, while noise rarely stays up this often.
    public var attackMilliseconds: Int = 120
    public var attackWindowMilliseconds: Int = 280
    /// Unvoiced audio needed before an open gate closes again.
    public var hangoverMilliseconds: Int = 400
    /// Withheld audio released in front of the opening frame, *in addition*
    /// to the attack itself, so the first syllable is never clipped.
    public var prerollMilliseconds: Int = 100

    /// The noise floor is this percentile of the frame levels over the last
    /// `floorWindowMilliseconds`: low enough that the pauses between words
    /// set it, not the words.
    public var floorPercentile: Double = 0.15
    public var floorWindowMilliseconds: Int = 4_000
    /// The floor never goes below this (dBFS): digital silence is not a
    /// reason to open on the faintest hiss.
    public var minimumFloorDBFS: Double = -72

    /// An open that ends with less than this much voiced audio is counted as
    /// spurious in the diagnostics (a door, a cough, a burst of noise).
    public var spuriousOpenMilliseconds: Int = 300

    public init() {}

    var frames: (attack: Int, attackWindow: Int, hangover: Int, preroll: Int, window: Int, spurious: Int) {
        let f = Double(max(1, frameMilliseconds))
        func count(_ ms: Int) -> Int { max(0, Int((Double(ms) / f).rounded(.up))) }
        let attack = max(1, count(attackMilliseconds))
        return (attack, max(attack, count(attackWindowMilliseconds)), max(1, count(hangoverMilliseconds)), count(prerollMilliseconds), max(1, count(floorWindowMilliseconds)), count(spuriousOpenMilliseconds))
    }
}

/// A voice-activity detector with an adaptive noise floor — the capture gate
/// the app uses instead of the browser's peak threshold.
///
/// The browser can get away with "peak below 8 LSB for 300 ms" because
/// Chrome's own noise suppression sits in front of it. On a phone that
/// gate almost never closes, so room noise streamed to the model and came
/// back as words ("fishing", "Discord"). This one asks a better question —
/// *is this frame clearly louder than the room has been?* — per frame:
///
/// 1. the frame's level in dBFS, in the speech band (300–3 400 Hz), from
///    its second-quietest 10 ms sub-block (clicks do not count);
/// 2. the noise floor: a low percentile of those levels over the last
///    few seconds, so it follows the room up and down but not the voice;
/// 3. open once `attack` of the last `attackWindow` frames are ≥ floor +
///    margin (6–10 dB, wider in an unsteady room); close after `hangover`
///    below floor + margin − hysteresis.
///
/// The output is `CaptureGate.Output`, the browser gate's contract — the frames to send
/// (with pre-roll on reopening) and the transition to announce with the
/// `/ws` `silence` message — so the socket side does not change.
public struct VoiceActivityDetector: Sendable {
    public typealias Output = CaptureGate.Output

    public let tuning: VADTuning

    /// True while frames are being withheld. Starts false: the daemon treats
    /// a new client as ungated, so the gate starts open and closes once it
    /// has heard the room.
    public private(set) var isGated = false
    /// Frames withheld in total (reported on the `ping`).
    public private(set) var gatedFrames = 0
    /// Peak of the last frame, in LSB (reported on the `ping`).
    public private(set) var peak = 0
    /// Full-band RMS of the last frame, dBFS.
    public private(set) var levelDBFS: Double = VoiceActivityDetector.silenceDBFS
    /// The detector's measure of the last frame (speech band, click-robust), dBFS.
    public private(set) var detectionDBFS: Double = VoiceActivityDetector.silenceDBFS
    /// The current noise floor, dBFS, on the same measure.
    public private(set) var floorDBFS: Double = VoiceActivityDetector.silenceDBFS
    /// The current open margin above the floor, dB (see `VADTuning`).
    public private(set) var marginDB: Double
    /// Opens so far, and those that turned out too short to be speech.
    public private(set) var opens = 0
    public private(set) var spuriousOpens = 0
    /// Whether the last frame counted as voiced.
    public private(set) var voiced = false

    public static let silenceDBFS: Double = -120

    private let counts: (attack: Int, attackWindow: Int, hangover: Int, preroll: Int, window: Int, spurious: Int)
    private var history: [Double] = []
    private var historyIndex = 0
    /// Voiced/unvoiced for the last `attackWindow` frames while gated.
    private var recent: [Bool] = []
    private var quietFrames = 0
    private var voicedFramesThisOpen = 0
    private var countingThisOpen = false
    private var preroll: [[Int16]] = []
    private var band: SpeechBandFilter

    public init(tuning: VADTuning = VADTuning()) {
        self.tuning = tuning
        marginDB = tuning.openMarginDB
        counts = tuning.frames
        band = SpeechBandFilter(sampleRate: tuning.sampleRate, low: tuning.bandLowHz, high: tuning.bandHighHz)
        history.reserveCapacity(counts.window)
    }

    @discardableResult
    public mutating func push(_ frame: [Int16]) -> Output {
        measure(frame)
        let openThreshold = floorDBFS + marginDB
        let closeThreshold = openThreshold - tuning.hysteresisDB

        if isGated {
            // Waiting for speech: enough voiced frames in the recent window.
            voiced = detectionDBFS >= openThreshold
            recent.append(voiced)
            if recent.count > counts.attackWindow { recent.removeFirst(recent.count - counts.attackWindow) }
            if voiced, recent.lazy.filter({ $0 }).count >= counts.attack {
                isGated = false
                recent = []
                quietFrames = 0
                opens += 1
                countingThisOpen = true
                voicedFramesThisOpen = counts.attack
                var send = preroll
                preroll = []
                send.append(frame)
                return Output(send: send, transition: .opened)
            }
            withhold(frame)
            return Output(send: [], transition: nil)
        }

        voiced = detectionDBFS >= closeThreshold
        if voiced {
            quietFrames = 0
            voicedFramesThisOpen += 1
            return Output(send: [frame], transition: nil)
        }
        quietFrames += 1
        if quietFrames < counts.hangover {
            return Output(send: [frame], transition: nil)
        }
        isGated = true
        quietFrames = 0
        recent = []
        if countingThisOpen, voicedFramesThisOpen < counts.spurious { spuriousOpens += 1 }
        countingThisOpen = false
        voicedFramesThisOpen = 0
        withhold(frame)
        return Output(send: [], transition: .closed)
    }

    /// Forgets the pre-roll and reopens without a transition, as a
    /// fresh client starts. The noise floor is kept unless `keepFloor` is
    /// false: the room did not change because the user muted.
    public mutating func reset(keepFloor: Bool = true) {
        preroll = []
        isGated = false
        recent = []
        quietFrames = 0
        countingThisOpen = false
        voicedFramesThisOpen = 0
        peak = 0
        voiced = false
        levelDBFS = Self.silenceDBFS
        detectionDBFS = Self.silenceDBFS
        band.reset()
        if !keepFloor {
            history.removeAll(keepingCapacity: true)
            historyIndex = 0
            floorDBFS = Self.silenceDBFS
            marginDB = tuning.openMarginDB
        }
    }

    // MARK: - Measuring

    private mutating func measure(_ frame: [Int16]) {
        peak = PCMLevel.peak(of: frame)
        var total = 0.0
        for s in frame {
            let v = Double(s)
            total += v * v
        }
        levelDBFS = Self.dbfs(meanSquare: frame.isEmpty ? 0 : total / Double(frame.count))

        let filtered: [Double] = tuning.speechBandOnly ? band.process(frame) : frame.map(Double.init)
        let blocks = max(1, min(tuning.subBlocks, filtered.count))
        let size = max(1, filtered.count / blocks)
        var energies: [Double] = []
        energies.reserveCapacity(blocks)
        for b in 0 ..< blocks {
            let start = b * size
            let end = b == blocks - 1 ? filtered.count : start + size
            guard start < end else { continue }
            var e = 0.0
            for i in start ..< end { e += filtered[i] * filtered[i] }
            energies.append(e / Double(end - start))
        }
        energies.sort()
        // The second-quietest sub-block (or the only one).
        let pick = energies.isEmpty ? 0 : energies[min(1, energies.count - 1)]
        detectionDBFS = Self.dbfs(meanSquare: pick)
        track(detectionDBFS)
    }

    private mutating func track(_ level: Double) {
        if history.count < counts.window {
            history.append(level)
        } else {
            history[historyIndex] = level
            historyIndex = (historyIndex + 1) % counts.window
        }
        let sorted = history.sorted()
        func percentile(_ p: Double) -> Double {
            sorted[min(sorted.count - 1, max(0, Int((Double(sorted.count - 1) * p).rounded())))]
        }
        let floor = percentile(tuning.floorPercentile)
        floorDBFS = max(tuning.minimumFloorDBFS, floor)
        // Too little history to judge the room's steadiness: stay cautious.
        guard sorted.count >= counts.window / 4 else {
            marginDB = tuning.openMarginDB
            return
        }
        // Below the minimum floor the spread is the spread of silence.
        let spread = max(0, floor - max(tuning.minimumFloorDBFS, percentile(0.02)))
        marginDB = min(tuning.openMarginDB, max(tuning.minimumOpenMarginDB, tuning.marginBaseDB + tuning.marginPerSpread * spread))
    }

    private mutating func withhold(_ frame: [Int16]) {
        gatedFrames += 1
        let keep = counts.preroll + counts.attackWindow
        guard keep > 0 else { return }
        preroll.append(frame)
        if preroll.count > keep { preroll.removeFirst(preroll.count - keep) }
    }

    /// Mean square of Int16 samples → dBFS (full scale = 32 768).
    public static func dbfs(meanSquare: Double) -> Double {
        guard meanSquare > 0 else { return silenceDBFS }
        return max(silenceDBFS, 10 * log10(meanSquare / (32_768.0 * 32_768.0)))
    }

    /// RMS of a frame, dBFS.
    public static func rmsDBFS(of frame: [Int16]) -> Double {
        guard !frame.isEmpty else { return silenceDBFS }
        var total = 0.0
        for s in frame {
            let v = Double(s)
            total += v * v
        }
        return dbfs(meanSquare: total / Double(frame.count))
    }
}

/// A 2nd-order Butterworth high-pass followed by a 2nd-order low-pass
/// (RBJ biquads), carrying state across frames.
struct SpeechBandFilter: Sendable {
    private struct Biquad: Sendable {
        var b0 = 1.0, b1 = 0.0, b2 = 0.0, a1 = 0.0, a2 = 0.0
        var x1 = 0.0, x2 = 0.0, y1 = 0.0, y2 = 0.0

        init(highPass: Bool, frequency: Double, sampleRate: Double) {
            let f = min(max(1, frequency), sampleRate * 0.49)
            let w0 = 2 * Double.pi * f / sampleRate
            let alpha = sin(w0) / (2 * 0.707_106_781)
            let c = cos(w0)
            let a0 = 1 + alpha
            if highPass {
                b0 = (1 + c) / 2 / a0
                b1 = -(1 + c) / a0
                b2 = (1 + c) / 2 / a0
            } else {
                b0 = (1 - c) / 2 / a0
                b1 = (1 - c) / a0
                b2 = (1 - c) / 2 / a0
            }
            a1 = -2 * c / a0
            a2 = (1 - alpha) / a0
        }

        mutating func run(_ x: Double) -> Double {
            let y = b0 * x + b1 * x1 + b2 * x2 - a1 * y1 - a2 * y2
            x2 = x1; x1 = x
            y2 = y1; y1 = y
            return y
        }

        mutating func reset() { x1 = 0; x2 = 0; y1 = 0; y2 = 0 }
    }

    private var high: Biquad
    private var low: Biquad

    init(sampleRate: Double, low lowHz: Double, high highHz: Double) {
        high = Biquad(highPass: true, frequency: lowHz, sampleRate: sampleRate)
        low = Biquad(highPass: false, frequency: highHz, sampleRate: sampleRate)
    }

    mutating func process(_ frame: [Int16]) -> [Double] {
        var out = [Double](repeating: 0, count: frame.count)
        for (i, s) in frame.enumerated() {
            out[i] = low.run(high.run(Double(s)))
        }
        return out
    }

    mutating func reset() {
        high.reset()
        low.reset()
    }
}
