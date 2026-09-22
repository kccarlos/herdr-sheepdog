import Foundation

/// Silence gate for capture frames — a port of the browser's `SilenceGate`
/// (`web/voice/audio-util.js`) and of docs/ws-contract.md, "Silence gate".
///
/// Near-zero PCM streamed continuously degrades the model session, and a
/// hardware-muted or very quiet microphone produces exactly that. Frames
/// whose peak stays below `floor` (Int16 LSB) for `holdMilliseconds` are not
/// sent at all; the last `prerollMilliseconds` of withheld frames are kept
/// and released in front of the first loud frame so the first syllable is
/// not clipped.
///
/// Pure: `push(_:)` returns what to send and whether the gate flipped. The
/// caller sends `{"type":"silence","on":…}` on every transition.
public struct SilenceGate: Sendable {
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
    }

    /// Peak below this (Int16 LSB) counts as silent. The contract's value.
    public let floor: Int
    /// Consecutive silence needed before the gate closes.
    public let holdMilliseconds: Int
    /// How much withheld audio is replayed when speech resumes.
    public let prerollMilliseconds: Int
    /// Duration of one frame. 40 ms on iOS (the browser uses 20 ms).
    public let frameMilliseconds: Int

    private let prerollFrames: Int
    private var preroll: [[Int16]] = []

    /// True while frames are being withheld.
    public private(set) var isGated = false
    /// Consecutive silent time seen so far.
    public private(set) var quietMilliseconds = 0
    /// Frames withheld in total (reported on the `ping`).
    public private(set) var gatedFrames = 0
    /// Peak of the last frame, in LSB (reported on the `ping`).
    public private(set) var peak = 0

    public init(floor: Int = 8, holdMilliseconds: Int = 300, prerollMilliseconds: Int = 100, frameMilliseconds: Int = AudioContract.frameMilliseconds) {
        self.floor = floor
        self.holdMilliseconds = holdMilliseconds
        self.prerollMilliseconds = prerollMilliseconds
        self.frameMilliseconds = max(1, frameMilliseconds)
        self.prerollFrames = max(0, Int((Double(prerollMilliseconds) / Double(max(1, frameMilliseconds))).rounded()))
    }

    @discardableResult
    public mutating func push(_ frame: [Int16]) -> Output {
        let framePeak = SilenceGate.peak(of: frame)
        peak = framePeak
        if framePeak >= floor {
            quietMilliseconds = 0
            if isGated {
                isGated = false
                var send = preroll
                preroll = []
                send.append(frame)
                return Output(send: send, transition: .opened)
            }
            return Output(send: [frame], transition: nil)
        }
        quietMilliseconds += frameMilliseconds
        if !isGated {
            if quietMilliseconds < holdMilliseconds {
                return Output(send: [frame], transition: nil)
            }
            isGated = true
            withhold(frame)
            return Output(send: [], transition: .closed)
        }
        withhold(frame)
        return Output(send: [], transition: nil)
    }

    /// Forgets the pre-roll and reopens, without emitting a transition. Used
    /// on mute (the gate resumes its own bookkeeping on unmute) and on a
    /// fresh connection (the daemon treats a new client as ungated).
    public mutating func reset() {
        preroll = []
        isGated = false
        quietMilliseconds = 0
        peak = 0
    }

    private mutating func withhold(_ frame: [Int16]) {
        gatedFrames += 1
        guard prerollFrames > 0 else { return }
        preroll.append(frame)
        if preroll.count > prerollFrames { preroll.removeFirst() }
    }

    /// Largest absolute sample of a frame, in LSB.
    public static func peak(of frame: [Int16]) -> Int {
        var p = 0
        for s in frame {
            let a = s == Int16.min ? 32_768 : abs(Int(s))
            if a > p { p = a }
        }
        return p
    }
}
