import Foundation

/// The gate between the microphone and the socket, as the audio engine uses
/// it: the voice-activity detector by default, the browser's old peak gate
/// behind a diagnostics toggle, and a manual override for hold-to-talk.
///
/// Whatever decides, the output is `SilenceGate.Output` — frames to send
/// and the transition to announce with `{"type":"silence","on":…}` — and a
/// transition is emitted exactly when the gate's *visible* state changes,
/// so switching detector or talk mode mid-session never leaves the daemon
/// thinking the gate is in the other state.
public struct CaptureGate: Sendable {
    public enum Detector: String, Equatable, Sendable, CaseIterable {
        /// `VoiceActivityDetector`: adaptive noise floor, speech band.
        case voiceActivity = "vad"
        /// `SilenceGate`: the browser's peak < 8 LSB for 300 ms.
        case peak
    }

    /// What the About screen shows.
    public struct Diagnostics: Equatable, Sendable {
        public var detector: Detector
        /// Full-band RMS of the last frame, dBFS.
        public var levelDBFS: Double
        /// The VAD's noise floor and current margin (nil for the peak gate).
        public var floorDBFS: Double?
        public var marginDB: Double?
        public var isOpen: Bool
        public var opens: Int
        public var spuriousOpens: Int
        public var gatedFrames: Int
        public var peak: Int
        /// The gate is being held by hold-to-talk rather than deciding.
        public var manual: Bool?

        public init(detector: Detector = .voiceActivity, levelDBFS: Double = VoiceActivityDetector.silenceDBFS, floorDBFS: Double? = nil, marginDB: Double? = nil, isOpen: Bool = true, opens: Int = 0, spuriousOpens: Int = 0, gatedFrames: Int = 0, peak: Int = 0, manual: Bool? = nil) {
            self.detector = detector
            self.levelDBFS = levelDBFS
            self.floorDBFS = floorDBFS
            self.marginDB = marginDB
            self.isOpen = isOpen
            self.opens = opens
            self.spuriousOpens = spuriousOpens
            self.gatedFrames = gatedFrames
            self.peak = peak
            self.manual = manual
        }

        /// "−41 dBFS", or "—" for silence.
        public static func format(dBFS: Double?) -> String {
            guard let dBFS, dBFS > VoiceActivityDetector.silenceDBFS + 1 else { return "—" }
            return String(format: "%.0f dBFS", dBFS).replacingOccurrences(of: "-", with: "−")
        }
    }

    public var detector: Detector {
        didSet { if detector != oldValue { resync() } }
    }

    /// Hold-to-talk: `true` holds the gate open (the orb is pressed),
    /// `false` holds it closed, `nil` lets the detector decide.
    public var manual: Bool? {
        // Press and release keep the pre-roll: it is what was said just
        // before the thumb landed.
        didSet { if manual != oldValue, manual == nil || oldValue == nil { resync() } }
    }

    /// The gate's visible state: true while frames are withheld.
    public private(set) var isGated = false
    public private(set) var gatedFrames = 0

    private var vad: VoiceActivityDetector
    private var peakGate: SilenceGate
    private var manualPreroll: [[Int16]] = []
    private let manualPrerollFrames: Int

    public init(detector: Detector = .voiceActivity, tuning: VADTuning = VADTuning(), peakGate: SilenceGate = SilenceGate()) {
        self.detector = detector
        vad = VoiceActivityDetector(tuning: tuning)
        self.peakGate = peakGate
        manualPrerollFrames = max(0, Int((Double(tuning.prerollMilliseconds) / Double(max(1, tuning.frameMilliseconds))).rounded()))
    }

    @discardableResult
    public mutating func push(_ frame: [Int16]) -> SilenceGate.Output {
        let wasGated = isGated
        // Both detectors always hear the frame, so the floor and the
        // diagnostics stay live whichever one is deciding.
        let vadOut = vad.push(frame)
        let peakOut = peakGate.push(frame)

        var send: [[Int16]]
        if let manual {
            if manual {
                send = wasGated ? manualPreroll + [frame] : [frame]
                manualPreroll = []
                isGated = false
            } else {
                send = []
                isGated = true
                manualPreroll.append(frame)
                if manualPreroll.count > manualPrerollFrames { manualPreroll.removeFirst(manualPreroll.count - manualPrerollFrames) }
            }
        } else {
            switch detector {
            case .voiceActivity:
                send = vadOut.send
                isGated = vad.isGated
            case .peak:
                send = peakOut.send
                isGated = peakGate.isGated
            }
        }
        if isGated, send.isEmpty { gatedFrames += 1 }
        let transition: SilenceGate.Transition? = wasGated == isGated ? nil : (isGated ? .closed : .opened)
        return SilenceGate.Output(send: send, transition: transition)
    }

    /// Mute or a fresh connection: open without a transition (the daemon
    /// treats a new or unmuted client as ungated). The VAD keeps its noise
    /// floor — the room did not change because the user muted.
    public mutating func reset() {
        vad.reset(keepFloor: true)
        peakGate.reset()
        manualPreroll = []
        isGated = manual == false
    }

    public var peak: Int { vad.peak }

    public var diagnostics: Diagnostics {
        Diagnostics(
            detector: detector,
            levelDBFS: vad.levelDBFS,
            floorDBFS: detector == .voiceActivity ? vad.floorDBFS : nil,
            marginDB: detector == .voiceActivity ? vad.marginDB : nil,
            isOpen: !isGated,
            opens: vad.opens,
            spuriousOpens: vad.spuriousOpens,
            gatedFrames: gatedFrames,
            peak: vad.peak,
            manual: manual
        )
    }

    /// After a detector or mode switch the newly deciding gate starts from
    /// the visible state, so the next push only transitions on a real change.
    private mutating func resync() {
        manualPreroll = []
        if manual == nil {
            // Start the detector open; if it was visibly gated, the next
            // push reports `.opened` and it closes again on its own.
            vad.reset(keepFloor: true)
            peakGate.reset()
        }
    }
}
