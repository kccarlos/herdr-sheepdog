import AVFoundation
import XCTest
@testable import SheepdogKit

@MainActor
final class AudioCaptureTests: XCTestCase {
    private static func constantBuffer(value: Float, sampleRate: Double = 48_000, seconds: Double = 0.04) throws -> AVAudioPCMBuffer {
        let format = try XCTUnwrap(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: sampleRate, channels: 1, interleaved: false))
        let frames = AVAudioFrameCount(sampleRate * seconds)
        let buffer = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames))
        buffer.frameLength = frames
        let channel = try XCTUnwrap(buffer.floatChannelData?[0])
        for index in 0 ..< Int(frames) {
            channel[index] = value
        }
        return buffer
    }

    func testConvertsSlicesAndGates48kHzBuffersInto16kHzFrames() throws {
        var now = 10.0
        var tuning = VADTuning()
        tuning.hangoverMilliseconds = 80
        tuning.prerollMilliseconds = 80
        let capture = AudioCapture(
            tuning: tuning,
            clock: {
                now += 0.001
                return now
            }
        )
        var frames: [Data] = []
        var gates: [Bool] = []
        var levels: [Float] = []
        capture.onFrame = { frames.append($0) }
        capture.onGate = { gates.append($0) }
        capture.onLevel = { levels.append($0) }

        let speech = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.2, amplitude: 0.5))
        try capture.prepare(inputFormat: speech.format)
        capture.didStart()

        let firstSignal = try capture.process(speech)
        XCTAssertNil(firstSignal)
        XCTAssertFalse(frames.isEmpty)
        for frame in frames {
            XCTAssertEqual(frame.count, AudioContract.frameSamples * 2)
            let samples = [Int16](pcm16LE: frame)
            XCTAssertGreaterThan(PCMConverter.toneMagnitude(samples, frequency: 440, sampleRate: AudioContract.captureRate), 0.1)
        }
        XCTAssertEqual(capture.metrics.captureFrames, 1)
        XCTAssertGreaterThan(capture.metrics.captureLatencyMilliseconds, 0)
        XCTAssertGreaterThan(capture.inputPeak, 10_000)
        XCTAssertGreaterThan(levels.last ?? 0, 0.3)

        // Quiet but non-zero samples close the gate without tripping the
        // digital-zero watchdog.
        let quiet = try Self.constantBuffer(value: 0.0001, seconds: 0.4)
        let quietSignal = try capture.process(quiet)
        XCTAssertNil(quietSignal)
        XCTAssertEqual(gates, [true])
        XCTAssertTrue(capture.gateIsClosed)
        XCTAssertGreaterThan(capture.gatedFrames, 0)
        XCTAssertEqual(capture.metrics.gatedFrames, capture.gatedFrames)

        // Speech reopens the gate and flushes the pre-roll.
        let countBeforeReopen = frames.count
        let reopenSignal = try capture.process(speech)
        XCTAssertNil(reopenSignal)
        XCTAssertEqual(gates, [true, false])
        XCTAssertFalse(capture.gateIsClosed)
        XCTAssertGreaterThan(frames.count, countBeforeReopen)

        // `apply(to:)` copies capture counters onto combined metrics without
        // touching engine or playback fields.
        var combined = AudioMetrics()
        combined.hardwareSampleRate = 48_000
        combined.outputRoute = "Speaker"
        combined.scheduledChunks = 4
        combined.peakCaptureLatencyMilliseconds = 999
        capture.apply(to: &combined)
        XCTAssertEqual(combined.hardwareSampleRate, 48_000)
        XCTAssertEqual(combined.outputRoute, "Speaker")
        XCTAssertEqual(combined.scheduledChunks, 4)
        XCTAssertEqual(combined.captureFrames, 3)
        XCTAssertEqual(combined.captureLatencyMilliseconds, capture.metrics.captureLatencyMilliseconds)
        XCTAssertEqual(combined.peakCaptureLatencyMilliseconds, 999)
        XCTAssertEqual(combined.gatedFrames, capture.gatedFrames)
    }

    func testFollowsSampleRateChangesAndValidatesPrepareFormat() throws {
        let capture = AudioCapture(followsInputFormat: true)
        var frames: [Data] = []
        capture.onFrame = { frames.append($0) }
        capture.reset(isStart: true)
        capture.didStart()

        let at48k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.12, amplitude: 0.5))
        try capture.process(at48k)
        let firstBatch = frames.count
        XCTAssertGreaterThan(firstBatch, 0)

        let at44k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 44_100, seconds: 0.12, amplitude: 0.5))
        try capture.process(at44k)
        XCTAssertGreaterThan(frames.count, firstBatch)
        let lastSamples = [Int16](pcm16LE: try XCTUnwrap(frames.last))
        XCTAssertGreaterThan(PCMConverter.toneMagnitude(lastSamples, frequency: 440, sampleRate: AudioContract.captureRate), 0.1)

        // An unconvertible stream format is rejected upfront by `prepare`.
        var streamDesc = AudioStreamBasicDescription(
            mSampleRate: 0,
            mFormatID: kAudioFormatLinearPCM,
            mFormatFlags: kAudioFormatFlagIsFloat,
            mBytesPerPacket: 4,
            mFramesPerPacket: 1,
            mBytesPerFrame: 4,
            mChannelsPerFrame: 0,
            mBitsPerChannel: 32,
            mReserved: 0
        )
        if let invalid = AVAudioFormat(streamDescription: &streamDesc) {
            XCTAssertThrowsError(try capture.prepare(inputFormat: invalid))
        }
    }

    func testMuteDropsAudioResetsClosedGateAndDisarmsWatchdog() throws {
        var now = 0.0
        var tuning = VADTuning()
        tuning.hangoverMilliseconds = 40
        let capture = AudioCapture(
            tuning: tuning,
            watchdogThreshold: 3,
            clock: { now }
        )
        var frames: [Data] = []
        var gates: [Bool] = []
        var levels: [Float] = []
        var signals: [Bool] = []
        capture.onFrame = { frames.append($0) }
        capture.onGate = { gates.append($0) }
        capture.onLevel = { levels.append($0) }
        capture.onMicSignal = { signals.append($0) }

        let zeros = try Self.constantBuffer(value: 0, seconds: 0.2)
        try capture.prepare(inputFormat: zeros.format, isStart: true)
        capture.didStart()

        // Close the gate and trip the zero-signal watchdog.
        let initial = try capture.process(zeros)
        XCTAssertNil(initial)
        XCTAssertEqual(gates, [true])
        now = 3.1
        let tripped = try capture.process(zeros)
        XCTAssertEqual(tripped, .lost(restart: true))
        XCTAssertEqual(signals, [true])
        XCTAssertTrue(capture.noSignal)

        // Muting resets the closed gate back to open, zeroes the meter, and
        // clears the no-signal warning.
        capture.setMuted(true, running: true)
        XCTAssertTrue(capture.isMuted)
        XCTAssertEqual(gates, [true, false])
        XCTAssertEqual(levels.last, 0)
        XCTAssertEqual(signals, [true, false])
        XCTAssertFalse(capture.noSignal)

        // Repeating mute does not emit duplicate transitions, and buffers are
        // ignored while muted.
        capture.setMuted(true, running: true)
        XCTAssertEqual(gates, [true, false])
        XCTAssertEqual(signals, [true, false])
        let speech = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.12))
        let sentBefore = frames.count
        let whileMuted = try capture.process(speech)
        XCTAssertNil(whileMuted)
        XCTAssertEqual(frames.count, sentBefore)

        // Unmuting while interrupted leaves the watchdog disarmed.
        capture.setMuted(false, running: true, interrupted: true)
        now = 10.0
        XCTAssertNil(capture.tickWatchdog(running: true, interrupted: false))

        // Unmuting while running and active re-arms a fresh grace period.
        capture.setMuted(true, running: true)
        now = 20.0
        capture.setMuted(false, running: true, interrupted: false)
        now = 23.1
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
    }

    func testHoldToTalkPreservesClosedGateAndControlsWatchdog() throws {
        var now = 0.0
        var tuning = VADTuning()
        tuning.prerollMilliseconds = 80
        let capture = AudioCapture(tuning: tuning, watchdogThreshold: 3, clock: { now })
        var frames: [Data] = []
        var gates: [Bool] = []
        var signals: [Bool] = []
        capture.onFrame = { frames.append($0) }
        capture.onGate = { gates.append($0) }
        capture.onMicSignal = { signals.append($0) }

        // Configured before start (hold-to-talk default): gate starts closed
        // and the watchdog stays disarmed while the button is not held.
        let oneFrame = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.05))
        capture.setManualGate(false, running: false)
        try capture.prepare(inputFormat: oneFrame.format, isStart: true)
        capture.didStart()
        XCTAssertTrue(capture.gateIsClosed)
        now = 5.0
        XCTAssertNil(capture.tickWatchdog(running: true))

        // Speech arriving just before the press stays in the manual pre-roll.
        XCTAssertNil(try capture.process(oneFrame))
        XCTAssertTrue(frames.isEmpty)
        XCTAssertTrue(gates.isEmpty)

        // Pressing hold-to-talk arms the watchdog, and the next frame opens
        // the gate and flushes the pre-roll ahead of it.
        capture.setManualGate(true, running: true)
        now = 8.1
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
        XCTAssertEqual(signals, [true])

        now = 8.2
        XCTAssertEqual(try capture.process(oneFrame), .recovered)
        XCTAssertFalse(capture.gateIsClosed)
        XCTAssertEqual(gates, [false])
        XCTAssertGreaterThanOrEqual(frames.count, 2)
        XCTAssertEqual(signals, [true, false])

        // Releasing disarms the watchdog, clears any no-signal state, and
        // closes the gate on the next frame.
        now = 12.0
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: false))
        capture.setManualGate(false, running: true)
        XCTAssertFalse(capture.noSignal)
        XCTAssertEqual(signals, [true, false, true, false])
        XCTAssertNil(try capture.process(oneFrame))
        XCTAssertTrue(capture.gateIsClosed)
        XCTAssertEqual(gates, [false, true])
    }

    func testWatchdogAllowsOneAutomaticRestartPerArmAndRecoversOnSignal() throws {
        var now = 0.0
        let capture = AudioCapture(watchdogThreshold: 3, clock: { now })
        var signals: [Bool] = []
        capture.onMicSignal = { signals.append($0) }

        let zeros = try Self.constantBuffer(value: 0, seconds: 0.08)
        let speech = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.08))
        try capture.prepare(inputFormat: zeros.format, isStart: true)
        capture.didStart()

        now = 2.9
        XCTAssertNil(try capture.process(zeros))
        now = 3.0
        let firstLoss = try capture.process(zeros)
        XCTAssertEqual(firstLoss, .lost(restart: true))
        XCTAssertEqual(signals, [true])

        // Rebuilding the capture chain resets the converter/chunker/gate
        // without clearing `noSignal` or re-allowing a second restart.
        try capture.prepare(inputFormat: zeros.format)
        XCTAssertTrue(capture.noSignal)

        // Real audio recovers.
        now = 4.0
        let recovered = try capture.process(speech)
        XCTAssertEqual(recovered, .recovered)
        XCTAssertFalse(capture.noSignal)
        XCTAssertEqual(signals, [true, false])

        // A second loss in the same arm reports no-signal without restarting.
        now = 4.1
        XCTAssertNil(try capture.process(zeros))
        now = 7.2
        let secondLoss = try capture.process(zeros)
        XCTAssertEqual(secondLoss, .lost(restart: false))
        XCTAssertEqual(signals, [true, false, true])

        // An interruption disarms and clears no-signal; reactivation re-arms
        // with a fresh automatic-restart budget.
        capture.disarmWatchdog()
        XCTAssertFalse(capture.noSignal)
        XCTAssertEqual(signals, [true, false, true, false])
        now = 12.0
        XCTAssertNil(capture.tickWatchdog(running: true, interrupted: true))
        capture.armWatchdogIfNeeded(running: true, interrupted: false)
        now = 15.1
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
    }

    func testWatchdogSignalChangeRunsBeforeFramingMetricsAndLevel() throws {
        var now = 0.0
        var tuning = VADTuning()
        tuning.hangoverMilliseconds = 120
        tuning.prerollMilliseconds = 40
        let capture = AudioCapture(tuning: tuning, watchdogThreshold: 3, clock: { now })

        let initialZeros = try Self.constantBuffer(value: 0, seconds: 0.2)
        // 4 096 samples at 48 kHz (~85.3 ms): two 40 ms frames plus a remainder.
        let triggerZeros = try Self.constantBuffer(value: 0, seconds: 4_096.0 / 48_000.0)
        try capture.prepare(inputFormat: initialZeros.format, isStart: true)
        capture.didStart()

        XCTAssertNil(try capture.process(initialZeros))
        XCTAssertTrue(capture.gateIsClosed)
        XCTAssertEqual(capture.metrics.captureFrames, 1)

        var events: [String] = []
        capture.onMicSignal = { events.append("micSignal:\($0)") }
        capture.onGate = { events.append("gate:\($0)") }
        capture.onFrame = { _ in events.append("frame") }
        capture.onLevel = { events.append("level:\($0)") }
        capture.onSignalChange = { change in
            events.append("signalChange:\(change)")
            // `onSignalChange` runs before framing and before `recordCapture`.
            XCTAssertEqual(capture.metrics.captureFrames, 1)
            if change == .lost(restart: true) {
                now += 0.025
                try? capture.prepare(inputFormat: triggerZeros.format)
            }
        }

        now = 3.0
        let signal = try capture.process(triggerZeros)
        XCTAssertEqual(signal, .lost(restart: true))
        XCTAssertEqual(
            events,
            [
                "micSignal:true",
                "signalChange:lost(restart: true)",
                "frame",
                "frame",
                "level:0.0",
            ]
        )
        XCTAssertFalse(capture.gateIsClosed)
        XCTAssertEqual(capture.metrics.captureFrames, 2)
        XCTAssertEqual(capture.metrics.captureLatencyMilliseconds, 25.0, accuracy: 0.01)
        XCTAssertEqual(capture.metrics.peakCaptureLatencyMilliseconds, 25.0, accuracy: 0.01)

        // `tickWatchdog` also dispatches through `onSignalChange` after `onMicSignal`.
        capture.onSignalChange = { change in
            events.append("signalChange:\(change)")
        }
        capture.disarmWatchdog()
        now = 10.0
        capture.armWatchdogIfNeeded(running: true)
        events.removeAll()
        now = 13.1
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
        XCTAssertEqual(events, ["micSignal:true", "signalChange:lost(restart: true)"])
    }

    func testFixedFormatCaptureDoesNotRebuildConverterInProcessAndKeepsItAcrossMute() throws {
        let capture = AudioCapture()
        var frames: [Data] = []
        capture.onFrame = { frames.append($0) }

        // Without `prepare(inputFormat:)`, `followsInputFormat == false` does
        // not lazily build a converter in `process`.
        let speech48k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.12, amplitude: 0.5))
        XCTAssertNil(try capture.process(speech48k))
        XCTAssertTrue(frames.isEmpty)
        XCTAssertEqual(capture.metrics.captureFrames, 0)

        // Prepare for a 24 kHz route (e.g. Bluetooth HFP). If an in-flight
        // 48 kHz mono or stereo tap buffer arrives after `prepare`, `process`
        // must not overwrite the 24 kHz converter with a 48 kHz one or leave
        // stale samples in `chunker` that corrupt the first 24 kHz buffers.
        let speech24k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 24_000, seconds: 0.2, amplitude: 0.5))
        try capture.prepare(inputFormat: speech24k.format, isStart: true)
        capture.didStart()

        let staleMono48k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 1_000, sampleRate: 48_000, seconds: 0.05, amplitude: 0.9))
        XCTAssertThrowsError(try capture.process(staleMono48k))
        XCTAssertTrue(frames.isEmpty)
        XCTAssertEqual(capture.metrics.captureFrames, 0)

        let staleFormat = try XCTUnwrap(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 2, interleaved: false))
        let staleFrames = AVAudioFrameCount(2_400) // 50 ms at 48 kHz
        let staleBuffer = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: staleFormat, frameCapacity: staleFrames))
        staleBuffer.frameLength = staleFrames
        for ch in 0 ..< 2 {
            let data = try XCTUnwrap(staleBuffer.floatChannelData?[ch])
            for i in 0 ..< Int(staleFrames) { data[i] = 0.9 }
        }
        XCTAssertThrowsError(try capture.process(staleBuffer))
        XCTAssertTrue(frames.isEmpty)

        // Subsequent 24 kHz audio converts cleanly with the preserved 24 kHz converter.
        try capture.process(speech24k)
        XCTAssertGreaterThanOrEqual(frames.count, 4)
        for frame in frames {
            let samples = [Int16](pcm16LE: frame)
            XCTAssertGreaterThan(PCMConverter.toneMagnitude(samples, frequency: 440, sampleRate: AudioContract.captureRate), 0.1)
        }

        // Muting resets the converter in place rather than clearing it, so
        // unmuting continues converting without another `prepare`.
        capture.setMuted(true, running: true)
        capture.setMuted(false, running: true)
        let beforeUnmuteSpeech = frames.count
        try capture.process(speech24k)
        XCTAssertGreaterThan(frames.count, beforeUnmuteSpeech)
    }

    func testStartResetClearsReportedClosedGateWhileRouteRebuildPreservesIt() throws {
        var tuning = VADTuning()
        tuning.hangoverMilliseconds = 40
        let capture = AudioCapture(tuning: tuning)
        var gates: [Bool] = []
        capture.onGate = { gates.append($0) }

        let zeros = try Self.constantBuffer(value: 0, seconds: 0.2)
        try capture.prepare(inputFormat: zeros.format, isStart: true)
        capture.didStart()

        try capture.process(zeros)
        XCTAssertEqual(gates, [true])

        // Route-change `prepare` (`isStart == false`) resets `gate` to open
        // without clearing `isGateClosed`, so muting afterward still reports
        // `onGate(false)` to the caller.
        try capture.prepare(inputFormat: zeros.format)
        capture.setMuted(true, running: true)
        XCTAssertEqual(gates, [true, false])

        // Close the gate again, then simulate `stopByItself` followed by a new
        // `start()` that calls `prepare(..., isStart: true)` before
        // `AVAudioEngine.start()` fails (never reaching `didStart()`). Muting
        // afterward must not emit a duplicate `onGate(false)`.
        capture.setMuted(false, running: true)
        try capture.process(zeros)
        XCTAssertEqual(gates, [true, false, true])

        try capture.prepare(inputFormat: zeros.format, isStart: true)
        capture.setMuted(true, running: false)
        XCTAssertEqual(gates, [true, false, true])

        // `stop()` (`resetOnStop()`) also clears `isGateClosed` silently so a
        // subsequent mute while stopped does not emit `onGate(false)`.
        capture.setMuted(false, running: true)
        try capture.process(zeros)
        XCTAssertEqual(gates, [true, false, true, true])
        capture.stop()
        capture.setMuted(true, running: false)
        XCTAssertEqual(gates, [true, false, true, true])

        // Same distinction for format-following `reset(isStart:)` on macOS.
        let macCapture = AudioCapture(followsInputFormat: true, tuning: tuning)
        var macGates: [Bool] = []
        macCapture.onGate = { macGates.append($0) }
        macCapture.reset(isStart: true)
        macCapture.didStart()
        try macCapture.process(zeros)
        XCTAssertEqual(macGates, [true])
        macCapture.reset(isStart: true)
        macCapture.setMuted(true, running: false)
        XCTAssertEqual(macGates, [true])
    }
}
