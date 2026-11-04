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

        var metrics = AudioMetrics()
        let firstSignal = try capture.process(speech, metrics: &metrics)
        XCTAssertNil(firstSignal)
        XCTAssertFalse(frames.isEmpty)
        for frame in frames {
            XCTAssertEqual(frame.count, AudioContract.frameSamples * 2)
            let samples = [Int16](pcm16LE: frame)
            XCTAssertGreaterThan(PCMConverter.toneMagnitude(samples, frequency: 440, sampleRate: AudioContract.captureRate), 0.1)
        }
        XCTAssertEqual(metrics.captureFrames, 1)
        XCTAssertGreaterThan(metrics.captureLatencyMilliseconds, 0)
        XCTAssertGreaterThan(capture.inputPeak, 10_000)
        XCTAssertGreaterThan(levels.last ?? 0, 0.3)

        // Quiet but non-zero samples close the gate without tripping the
        // digital-zero watchdog.
        let quiet = try Self.constantBuffer(value: 0.0001, seconds: 0.4)
        let quietSignal = try capture.process(quiet, metrics: &metrics)
        XCTAssertNil(quietSignal)
        XCTAssertEqual(gates, [true])
        XCTAssertTrue(capture.gateIsClosed)
        XCTAssertGreaterThan(capture.gatedFrames, 0)
        XCTAssertEqual(metrics.gatedFrames, capture.gatedFrames)

        // Speech reopens the gate and flushes the pre-roll.
        let countBeforeReopen = frames.count
        let reopenSignal = try capture.process(speech, metrics: &metrics)
        XCTAssertNil(reopenSignal)
        XCTAssertEqual(gates, [true, false])
        XCTAssertFalse(capture.gateIsClosed)
        XCTAssertGreaterThan(frames.count, countBeforeReopen)
    }

    func testFollowsSampleRateChangesAndValidatesPrepareFormat() throws {
        let capture = AudioCapture()
        var frames: [Data] = []
        capture.onFrame = { frames.append($0) }
        capture.reset()
        capture.didStart()

        var metrics = AudioMetrics()
        let at48k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.12, amplitude: 0.5))
        try capture.process(at48k, metrics: &metrics)
        let firstBatch = frames.count
        XCTAssertGreaterThan(firstBatch, 0)

        let at44k = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 44_100, seconds: 0.12, amplitude: 0.5))
        try capture.process(at44k, metrics: &metrics)
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

        capture.didStart()
        var metrics = AudioMetrics()

        // Close the gate and trip the zero-signal watchdog.
        let zeros = try Self.constantBuffer(value: 0, seconds: 0.2)
        let initial = try capture.process(zeros, metrics: &metrics)
        XCTAssertNil(initial)
        XCTAssertEqual(gates, [true])
        now = 3.1
        let tripped = try capture.process(zeros, metrics: &metrics)
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
        let whileMuted = try capture.process(speech, metrics: &metrics)
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
        capture.setManualGate(false, running: false)
        capture.reset()
        capture.didStart()
        XCTAssertTrue(capture.gateIsClosed)
        now = 5.0
        XCTAssertNil(capture.tickWatchdog(running: true))

        // Speech arriving just before the press stays in the manual pre-roll.
        var metrics = AudioMetrics()
        let oneFrame = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.05))
        XCTAssertNil(try capture.process(oneFrame, metrics: &metrics))
        XCTAssertTrue(frames.isEmpty)
        XCTAssertTrue(gates.isEmpty)

        // Pressing hold-to-talk arms the watchdog, and the next frame opens
        // the gate and flushes the pre-roll ahead of it.
        capture.setManualGate(true, running: true)
        now = 8.1
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
        XCTAssertEqual(signals, [true])

        now = 8.2
        XCTAssertEqual(try capture.process(oneFrame, metrics: &metrics), .recovered)
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
        XCTAssertNil(try capture.process(oneFrame, metrics: &metrics))
        XCTAssertTrue(capture.gateIsClosed)
        XCTAssertEqual(gates, [false, true])
    }

    func testWatchdogAllowsOneAutomaticRestartPerArmAndRecoversOnSignal() throws {
        var now = 0.0
        let capture = AudioCapture(watchdogThreshold: 3, clock: { now })
        var signals: [Bool] = []
        capture.onMicSignal = { signals.append($0) }

        capture.didStart()
        var metrics = AudioMetrics()
        let zeros = try Self.constantBuffer(value: 0, seconds: 0.08)
        let speech = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 0.08))

        now = 2.9
        XCTAssertNil(try capture.process(zeros, metrics: &metrics))
        now = 3.0
        let firstLoss = try capture.process(zeros, metrics: &metrics)
        XCTAssertEqual(firstLoss, .lost(restart: true))
        XCTAssertEqual(signals, [true])

        // Rebuilding the capture chain resets the converter/chunker/gate
        // without clearing `noSignal` or re-allowing a second restart.
        try capture.prepare(inputFormat: zeros.format)
        XCTAssertTrue(capture.noSignal)

        // Real audio recovers.
        now = 4.0
        let recovered = try capture.process(speech, metrics: &metrics)
        XCTAssertEqual(recovered, .recovered)
        XCTAssertFalse(capture.noSignal)
        XCTAssertEqual(signals, [true, false])

        // A second loss in the same arm reports no-signal without restarting.
        now = 4.1
        XCTAssertNil(try capture.process(zeros, metrics: &metrics))
        now = 7.2
        let secondLoss = try capture.process(zeros, metrics: &metrics)
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
}
