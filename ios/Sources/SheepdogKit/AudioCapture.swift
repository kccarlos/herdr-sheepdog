import AVFoundation
import Foundation

/// The main-actor half of microphone capture shared by the iOS and macOS audio
/// engines: sample-rate conversion to 16 kHz mono PCM16 (`PCMConverter`),
/// 40 ms framing (`FrameChunker`), speech detection and hold-to-talk
/// (`CaptureGate`), and the zero-signal watchdog (`MicSignalWatchdog`).
///
/// Platform-only work stays in each app's engine — `AVAudioSession` and
/// interruptions on iOS, the render-thread multi-channel mix-down and
/// `AVAudioEngineConfigurationChange` on macOS — which copies each tap buffer
/// off the audio render thread and hands it to `process(_:)`.
@MainActor
public final class AudioCapture {
    /// A change in the microphone watchdog's verdict that the engine logs and,
    /// on the first loss since arming, rebuilds the capture chain for once.
    public enum SignalChange: Equatable, Sendable {
        /// Every sample was digital zero (or no buffers arrived) for
        /// `watchdogThreshold` seconds. `restart` is true once per arm.
        case lost(restart: Bool)
        /// A non-zero sample arrived after a loss.
        case recovered
    }

    public static let noMicrophoneMessage = "No microphone is available right now — check that nothing else is using it."

    /// A 40 ms PCM16 LE frame ready for `/ws`.
    public var onFrame: ((Data) -> Void)?
    /// The silence gate flipped: `true` when it closed.
    public var onGate: ((Bool) -> Void)?
    /// Peak input level across the frames of the last tap buffer, 0…1.
    public var onLevel: ((Float) -> Void)?
    /// The microphone stopped (`true`) or resumed (`false`) delivering signal.
    public var onMicSignal: ((Bool) -> Void)?
    /// The watchdog changed state: runs before framing in `process` so a
    /// capture-chain restart resets `chunker` and `gate` ahead of the
    /// triggering buffer and counts toward capture latency.
    public var onSignalChange: ((SignalChange) -> Void)?

    /// True when `process` builds and replaces `PCMConverter` to follow each
    /// buffer's format (macOS after `MicMixdown`); false when `prepare` owns
    /// the converter for the current hardware route (iOS).
    public let followsInputFormat: Bool
    /// The capture-side switch `setMuted` throws: gates `process` and
    /// arms/disarms the watchdog. Not a second source of truth for "muted",
    /// which is `SessionCore`'s.
    public private(set) var isMuted = false
    public private(set) var metrics = AudioMetrics()
    /// Debounces `onGate`: tracks whether the caller was told the gate closed
    /// so muting can report the reset back to open.
    private var isGateClosed = false
    private var converter: PCMConverter?
    private var chunker = FrameChunker()
    private var gate: CaptureGate
    private var watchdog: MicSignalWatchdog
    private var reportedNoSignal = false
    private var restartedForSignal = false
    private let clock: () -> TimeInterval

    public init(
        followsInputFormat: Bool = false,
        tuning: VADTuning = VADTuning(),
        watchdogThreshold: TimeInterval = 3,
        clock: @escaping () -> TimeInterval = { CFAbsoluteTimeGetCurrent() }
    ) {
        self.followsInputFormat = followsInputFormat
        gate = CaptureGate(tuning: tuning)
        watchdog = MicSignalWatchdog(threshold: watchdogThreshold)
        self.clock = clock
    }

    public var gateDiagnostics: CaptureGate.Diagnostics { gate.diagnostics }
    public var gateIsClosed: Bool { gate.isGated }
    public var noSignal: Bool { watchdog.noSignal }
    public var watchdogThreshold: TimeInterval { watchdog.threshold }
    public var inputPeak: Int { gate.peak }
    public var gatedFrames: Int { gate.gatedFrames }

    // MARK: - Lifecycle

    /// Validates and builds the sample-rate converter for `inputFormat` upfront
    /// (start and route-change rebuilds on iOS) and resets framing and the gate.
    public func prepare(inputFormat: AVAudioFormat, isStart: Bool = false) throws {
        converter = try PCMConverter(inputFormat: inputFormat)
        chunker.reset()
        gate.reset()
        if isStart { isGateClosed = false }
    }

    /// Resets framing, the gate and converter state (clearing a format-following
    /// converter on macOS; resetting the prepared converter in place on iOS).
    public func reset(isStart: Bool = false) {
        chunker.reset()
        gate.reset()
        if followsInputFormat {
            converter = nil
        } else {
            converter?.reset()
        }
        if isStart { isGateClosed = false }
    }

    /// Marks the capture chain running after `AVAudioEngine.start()` succeeds,
    /// arming the watchdog when capture is expected to deliver samples.
    public func didStart() {
        isGateClosed = false
        restartedForSignal = false
        if !isMuted, gate.manual != false {
            watchdog.arm(at: clock())
        }
    }

    /// Resets framing, the gate, converter state and the reported gate flag
    /// during `stop()`, before the host clears its state and disarms the watchdog.
    public func resetOnStop() {
        reset(isStart: true)
    }

    /// Tears down capture state and disarms the watchdog when the engine stops.
    public func stop() {
        resetOnStop()
        disarmWatchdog()
    }

    /// Arms the watchdog if the engine is running, unmuted, not interrupted and
    /// not held closed by hold-to-talk.
    public func armWatchdogIfNeeded(running: Bool, interrupted: Bool = false) {
        if AudioLifecycle.watchdogShouldRun(
            running: running,
            muted: isMuted,
            interrupted: interrupted,
            gateClosed: gate.manual == false
        ) {
            watchdog.arm(at: clock())
            restartedForSignal = false
        }
    }

    /// Disarms the watchdog and clears any reported no-signal state (mute,
    /// stop, hold-to-talk release, or an iOS audio interruption).
    public func disarmWatchdog() {
        watchdog.disarm()
        reportSignal()
    }

    // MARK: - Mute and gate

    /// Muted means silence, not zeros: nothing leaves the microphone and the
    /// gate restarts its bookkeeping on unmute.
    public func setMuted(_ muted: Bool, running: Bool, interrupted: Bool = false) {
        isMuted = muted
        if muted {
            disarmWatchdog()
            reset()
            if isGateClosed {
                isGateClosed = false
                onGate?(false)
            }
            onLevel?(0)
        } else {
            armWatchdogIfNeeded(running: running, interrupted: interrupted)
        }
    }

    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic.
    /// Closing disarms the watchdog; opening starts a fresh grace period.
    public func setManualGate(_ manual: Bool?, running: Bool, interrupted: Bool = false) {
        let wasClosed = gate.manual == false
        gate.manual = manual
        if manual == false {
            disarmWatchdog()
        } else if wasClosed {
            armWatchdogIfNeeded(running: running, interrupted: interrupted)
        }
    }

    // MARK: - Processing and watchdog

    /// Converts one tap buffer to 16 kHz PCM16, checks the signal watchdog,
    /// slices 40 ms frames, runs `CaptureGate` and updates `metrics`.
    @discardableResult
    public func process(_ buffer: AVAudioPCMBuffer) throws -> SignalChange? {
        guard !isMuted else { return nil }
        if !followsInputFormat, converter == nil { return nil }
        let started = clock()
        if followsInputFormat, converter?.inputFormat != buffer.format {
            converter = try PCMConverter(inputFormat: buffer.format)
        }
        guard let samples = try converter?.convert(buffer), !samples.isEmpty else { return nil }
        let signal = watchdog.observe(peak: PCMLevel.peak(of: samples), at: clock()).map(recordSignalChange)
        if let signal {
            onSignalChange?(signal)
        }

        var level: Float = 0
        let trackLevel = onLevel != nil
        for frame in chunker.push(samples) {
            if trackLevel {
                let peak = PCMLevel.peak(of: frame)
                level = max(level, Float(peak) / 32_768)
            }
            let out = gate.push(frame)
            if let transition = out.transition {
                let closed = transition == .closed
                isGateClosed = closed
                onGate?(closed)
            }
            for send in out.send {
                onFrame?(send.pcm16LEData)
            }
        }
        metrics.gatedFrames = gate.gatedFrames
        metrics.recordCapture(latency: clock() - started, frames: 1)
        onLevel?(level)
        return signal
    }

    /// Copies the capture counters onto an engine's `AudioMetrics`.
    public func apply(to target: inout AudioMetrics) {
        target.captureFrames = metrics.captureFrames
        target.captureLatencyMilliseconds = metrics.captureLatencyMilliseconds
        target.peakCaptureLatencyMilliseconds = max(target.peakCaptureLatencyMilliseconds, metrics.peakCaptureLatencyMilliseconds)
        target.gatedFrames = metrics.gatedFrames
    }

    /// Advances the zero-signal watchdog on the host timer's cadence, catching
    /// a tap that delivers no buffers at all.
    @discardableResult
    public func tickWatchdog(running: Bool, interrupted: Bool = false) -> SignalChange? {
        guard AudioLifecycle.watchdogShouldRun(
            running: running,
            muted: isMuted,
            interrupted: interrupted,
            gateClosed: gate.manual == false
        ), let change = watchdog.tick(at: clock()) else { return nil }
        let signal = recordSignalChange(change)
        onSignalChange?(signal)
        return signal
    }

    private func recordSignalChange(_ noSignal: Bool) -> SignalChange {
        reportSignal()
        guard noSignal else { return .recovered }
        let restart = !restartedForSignal
        restartedForSignal = true
        return .lost(restart: restart)
    }

    private func reportSignal() {
        guard watchdog.noSignal != reportedNoSignal else { return }
        reportedNoSignal = watchdog.noSignal
        onMicSignal?(watchdog.noSignal)
    }
}
