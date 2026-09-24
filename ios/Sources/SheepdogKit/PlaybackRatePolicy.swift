import Foundation

/// The jitter-buffer target range for one voice provider.
///
/// Gemini Live delivers faster than real time, so a small buffer is
/// enough. GPT-Live delivers at 0.72–0.86× real time (measured on the
/// phone), so a buffer that starts small spends the whole reply catching
/// up: it starts at 800 ms and may grow to 2 s. The browser page and the
/// daemon use the same numbers.
public struct PlaybackProfile: Equatable, Sendable {
    /// Where the adaptive target starts, and the floor it decays back to.
    public let floorMilliseconds: Int
    /// The most the target may grow to after underruns.
    public let ceilingMilliseconds: Int

    public init(floorMilliseconds: Int, ceilingMilliseconds: Int) {
        self.floorMilliseconds = floorMilliseconds
        self.ceilingMilliseconds = max(floorMilliseconds, ceilingMilliseconds)
    }

    public static let gemini = PlaybackProfile(floorMilliseconds: 150, ceilingMilliseconds: 500)
    public static let openAI = PlaybackProfile(floorMilliseconds: 800, ceilingMilliseconds: 2_000)

    /// The profile for a `/ws` `status.provider` value. Unknown or absent
    /// providers (older daemons) get Gemini's, which was the only one.
    public static func forProvider(_ provider: String?) -> PlaybackProfile {
        switch provider?.lowercased() {
        case "openai": return .openAI
        default: return .gemini
        }
    }
}

/// Pitch-preserving adaptive time-stretch: how fast to play, from how much
/// audio is buffered. Pure arithmetic; `PlaybackQueue` owns one and
/// `AudioEngine` copies its rate onto an `AVAudioUnitTimePitch`.
///
/// With `b` the buffered audio and `T` the jitter-buffer target:
///
/// | buffered            | rate                              |
/// |---------------------|-----------------------------------|
/// | b > 2T (with debt)  | 1.05 — catch up                   |
/// | T ≤ b               | 1.00                              |
/// | T/2 ≤ b < T         | linear 0.90 → 1.00                |
/// | T/4 < b < T/2       | linear 0.85 → 0.90                |
/// | b ≤ T/4             | 0.85 (the floor)                  |
///
/// The rate moves by at most 0.02 per 20 ms (1.0 per second), so a change
/// is a glide, never a jump.
///
/// **Catch-up needs latency debt.** Speeding up to 1.05 only makes sense
/// to win back time the stretch itself added (playing at r < 1 adds
/// `(1 − r)·dt` of latency). A provider that is simply ahead of real time
/// — Gemini delivers a 10 s reply in ~2.5 s, so b is far above 2T for
/// most of every reply — owes nothing, and plays at exactly 1.0. The debt
/// resets when the queue goes idle: the silence between replies absorbs
/// whatever is left.
public struct PlaybackRatePolicy: Equatable, Sendable {
    public static let floorRate = 0.85
    public static let kneeRate = 0.90
    public static let catchUpRate = 1.05
    /// 0.02 per 20 ms.
    public static let slewPerSecond = 1.0
    /// A late control-loop tick cannot turn into a jump.
    public static let maximumStep: TimeInterval = 0.1

    /// The rate right now.
    public private(set) var rate = 1.0
    /// Wall-clock seconds the stretch has added and not yet won back.
    public private(set) var debtSeconds = 0.0

    public init() {}

    /// The rate the policy steers towards for `buffered` ms against target
    /// `target` ms, before slewing. `catchUp` is whether there is latency
    /// debt to win back.
    public static func desiredRate(bufferedMilliseconds b: Double, targetMilliseconds t: Double, catchUp: Bool = true) -> Double {
        guard t > 0 else { return 1.0 }
        if b > 2 * t { return catchUp ? catchUpRate : 1.0 }
        if b >= t { return 1.0 }
        let half = t / 2
        let quarter = t / 4
        if b >= half {
            return kneeRate + (1.0 - kneeRate) * (b - half) / half
        }
        if b > quarter {
            return floorRate + (kneeRate - floorRate) * (b - quarter) / quarter
        }
        return floorRate
    }

    /// Advances the policy by `dt` seconds of playback at the current rate
    /// (accruing or paying off debt), then slews towards the desired rate.
    /// Returns the new rate.
    @discardableResult
    public mutating func step(bufferedMilliseconds b: Double, targetMilliseconds t: Double, dt: TimeInterval) -> Double {
        let dt = min(max(dt, 0), Self.maximumStep)
        debtSeconds = max(0, debtSeconds + (1 - rate) * dt)
        let desired = Self.desiredRate(bufferedMilliseconds: b, targetMilliseconds: t, catchUp: debtSeconds > 0)
        let limit = Self.slewPerSecond * dt
        rate += min(max(desired - rate, -limit), limit)
        // Snap away float dust so "exactly 1.0" is observable.
        if abs(rate - 1.0) < 1e-9 { rate = 1.0 }
        return rate
    }

    /// Back to 1.0 with no debt: the queue went idle or was flushed.
    public mutating func reset() {
        rate = 1.0
        debtSeconds = 0
    }
}
