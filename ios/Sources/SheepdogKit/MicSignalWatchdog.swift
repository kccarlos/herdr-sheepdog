import Foundation

/// Notices a microphone that is "on" but delivers nothing: every sample
/// digital zero (or no buffers at all) for `threshold` seconds while the
/// user is unmuted.
///
/// That is not a quiet room — a real room is never exactly zero — it is a
/// capture path that is not working (voice processing starting silent, the
/// wrong input, another app holding the microphone, an input muted at the
/// system level). The phone showed exactly this for ~25 s after connecting
/// on 2026-09-22, with the old gate quietly withholding it all. Now the
/// screen says so and the engine restarts capture once.
///
/// Pure: the caller feeds it buffer peaks and clock ticks.
public struct MicSignalWatchdog: Equatable, Sendable {
    /// How long digital silence may last before it counts as no signal.
    public var threshold: TimeInterval

    /// True while the microphone is judged to be delivering nothing.
    public private(set) var noSignal = false
    /// Set while capture should be delivering audio (running and unmuted).
    public private(set) var armedAt: TimeInterval?
    /// The last time a non-zero sample arrived.
    public private(set) var lastSignalAt: TimeInterval?
    /// How many times it has fired since it was last armed.
    public private(set) var firings = 0

    public init(threshold: TimeInterval = 3) {
        self.threshold = threshold
    }

    /// Capture started, or the user unmuted: the clock starts now.
    public mutating func arm(at now: TimeInterval) {
        armedAt = now
        lastSignalAt = nil
        noSignal = false
        firings = 0
    }

    /// Muted or stopped: nothing is expected, so nothing is wrong.
    public mutating func disarm() {
        armedAt = nil
        lastSignalAt = nil
        noSignal = false
    }

    public var isArmed: Bool { armedAt != nil }

    /// One captured buffer's peak (LSB). Returns the new `noSignal` when it
    /// changed.
    @discardableResult
    public mutating func observe(peak: Int, at now: TimeInterval) -> Bool? {
        guard armedAt != nil else { return nil }
        if peak > 0 {
            lastSignalAt = now
            if noSignal {
                noSignal = false
                return false
            }
            return nil
        }
        return tick(at: now)
    }

    /// The clock moved (also when no buffers arrive at all). Returns the
    /// new `noSignal` when it changed.
    @discardableResult
    public mutating func tick(at now: TimeInterval) -> Bool? {
        guard let armedAt else { return nil }
        let since = lastSignalAt ?? armedAt
        let silent = now - since >= threshold
        guard silent != noSignal else { return nil }
        noSignal = silent
        if silent { firings += 1 }
        return silent
    }
}
