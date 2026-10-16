import Foundation

/// The gate between the microphone and the socket, as the audio engine uses
/// it: the voice-activity detector decides, with a manual override for
/// hold-to-talk.
///
/// Whatever decides, the output is `Output` — frames to send and the
/// transition to announce with `{"type":"silence","on":…}` — and a
/// transition is emitted exactly when the gate's *visible* state changes,
/// so switching talk mode mid-session never leaves the daemon thinking the
/// gate is in the other state.
public struct CaptureGate: Sendable {
    public enum Transition: Equatable, Sendable {
        /// The gate closed: silence started, no frames follow.
        case closed
        /// The gate reopened: speech resumed, pre-roll leads the way.
        case opened
    }

    public struct Output: Equatable, Sendable {
        /// Frames to send now, oldest first (possibly including pre-roll).
        public var send: [[Int16]]
        public var transition: Transition?

        public init(send: [[Int16]], transition: Transition?) {
            self.send = send
            self.transition = transition
        }
    }

    /// What the About screen shows.
    public struct Diagnostics: Equatable, Sendable {
        /// Full-band RMS of the last frame, dBFS.
        public var levelDBFS: Double
        /// The VAD's noise floor and current margin.
        public var floorDBFS: Double?
        public var marginDB: Double?
        public var isOpen: Bool
        public var opens: Int
        public var spuriousOpens: Int
        public var gatedFrames: Int
        public var peak: Int
        /// The gate is being held by hold-to-talk rather than deciding.
        public var manual: Bool?

        public init(levelDBFS: Double = VoiceActivityDetector.silenceDBFS, floorDBFS: Double? = nil, marginDB: Double? = nil, isOpen: Bool = true, opens: Int = 0, spuriousOpens: Int = 0, gatedFrames: Int = 0, peak: Int = 0, manual: Bool? = nil) {
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
    private var manualPreroll: [[Int16]] = []
    private let manualPrerollFrames: Int

    public init(tuning: VADTuning = VADTuning()) {
        vad = VoiceActivityDetector(tuning: tuning)
        manualPrerollFrames = max(0, Int((Double(tuning.prerollMilliseconds) / Double(max(1, tuning.frameMilliseconds))).rounded()))
    }

    @discardableResult
    public mutating func push(_ frame: [Int16]) -> Output {
        let wasGated = isGated
        // The detector always hears the frame, so the floor and the
        // diagnostics stay live while hold-to-talk decides.
        let vadOut = vad.push(frame)

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
            send = vadOut.send
            isGated = vad.isGated
        }
        if isGated, send.isEmpty { gatedFrames += 1 }
        let transition: Transition? = wasGated == isGated ? nil : (isGated ? .closed : .opened)
        return Output(send: send, transition: transition)
    }

    /// Mute or a fresh connection: open without a transition (the daemon
    /// treats a new or unmuted client as ungated). The VAD keeps its noise
    /// floor — the room did not change because the user muted.
    public mutating func reset() {
        vad.reset(keepFloor: true)
        manualPreroll = []
        isGated = manual == false
    }

    public var peak: Int { vad.peak }

    public var diagnostics: Diagnostics {
        Diagnostics(
            levelDBFS: vad.levelDBFS,
            floorDBFS: vad.floorDBFS,
            marginDB: vad.marginDB,
            isOpen: !isGated,
            opens: vad.opens,
            spuriousOpens: vad.spuriousOpens,
            gatedFrames: gatedFrames,
            peak: vad.peak,
            manual: manual
        )
    }

    /// After a mode switch the newly deciding gate starts from the visible
    /// state, so the next push only transitions on a real change.
    private mutating func resync() {
        manualPreroll = []
        if manual == nil {
            // Start the detector open; if it was visibly gated, the next
            // push reports `.opened` and it closes again on its own.
            vad.reset(keepFloor: true)
        }
    }
}
