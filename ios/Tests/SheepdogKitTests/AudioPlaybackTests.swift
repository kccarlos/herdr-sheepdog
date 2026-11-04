import AVFoundation
import XCTest
@testable import SheepdogKit

@MainActor
final class AudioPlaybackTests: XCTestCase {
    private static func toneChunk(milliseconds: Int = 40, amplitude: Double = 0.5) -> Data {
        let count = Int(AudioContract.playbackRate) * milliseconds / 1_000
        let samples = (0 ..< count).map { index -> Int16 in
            let sample = amplitude * sin(2 * .pi * 440 * Double(index) / AudioContract.playbackRate)
            return Int16(sample * 32_767)
        }
        return samples.pcm16LEData
    }

    private static func makeOfflineRig(clock: @escaping () -> TimeInterval) throws -> (AVAudioEngine, AudioPlayback) {
        let engine = AVAudioEngine()
        let playback = AudioPlayback(clock: clock)
        playback.attach(to: engine)
        playback.connect(in: engine)
        try engine.enableManualRenderingMode(.offline, format: AudioPlayback.format, maximumFrameCount: 4_096)
        return (engine, playback)
    }

    func testPCMBufferScalesInt16SamplesAndRejectsEmpty() throws {
        XCTAssertNil(AudioPlayback.pcmBuffer(for: []))

        let buffer = try XCTUnwrap(AudioPlayback.pcmBuffer(for: [Int16.min, -16_384, 0, 16_384]))
        XCTAssertEqual(buffer.frameLength, 4)
        XCTAssertEqual(buffer.format.sampleRate, AudioContract.playbackRate)
        let channel = try XCTUnwrap(buffer.floatChannelData?[0])
        XCTAssertEqual(channel[0], -1.0, accuracy: 0.0001)
        XCTAssertEqual(channel[1], -0.5, accuracy: 0.0001)
        XCTAssertEqual(channel[2], 0.0, accuracy: 0.0001)
        XCTAssertEqual(channel[3], 0.5, accuracy: 0.0001)
    }

    func testSetProviderSwitchesProfileOnceAndUpdatesMetrics() {
        let playback = AudioPlayback()
        let openAI = playback.setProvider("openai")
        XCTAssertEqual(openAI, .openAI)
        XCTAssertEqual(playback.metrics.bufferTargetMilliseconds, PlaybackProfile.openAI.floorMilliseconds)

        // Calling again with the same provider is a no-op.
        XCTAssertNil(playback.setProvider("openai"))

        let gemini = playback.setProvider("gemini")
        XCTAssertEqual(gemini, .gemini)
        XCTAssertEqual(playback.metrics.bufferTargetMilliseconds, PlaybackProfile.gemini.floorMilliseconds)
    }

    func testPreStartTickReleasesShortUtteranceBeforePlaybackStarts() throws {
        var now = 0.0
        let (engine, playback) = try Self.makeOfflineRig(clock: { now })
        defer { engine.stop() }

        var reports: [PlaybackQueue.Report] = []
        var speaking: [Bool] = []
        playback.onPlaybackReport = { reports.append($0) }
        playback.onSpeakingChanged = { speaking.append($0) }

        // 40 ms is below the 150 ms jitter-buffer target, so `enqueue` holds
        // it until `tick()` sees `maximumWait` elapse while `!isRunning`.
        playback.enqueue(Self.toneChunk(milliseconds: 40))
        XCTAssertTrue(reports.isEmpty)
        XCTAssertEqual(playback.metrics.scheduledChunks, 0)

        now = 0.6
        playback.tick()
        XCTAssertEqual(reports.map(\.state), [.started])
        XCTAssertEqual(speaking, [true])
        XCTAssertEqual(playback.metrics.scheduledChunks, 1)
    }

    func testOfflinePlaybackRendersStretchesDrainsAndAppliesMetrics() throws {
        var now = 0.0
        let (engine, playback) = try Self.makeOfflineRig(clock: { now })
        try engine.start()
        playback.start()
        defer {
            playback.stop(in: engine)
        }
        XCTAssertTrue(playback.isRunning)

        var reports: [PlaybackQueue.Report] = []
        var drains: [PlaybackQueue.DrainRecord] = []
        var speaking: [Bool] = []
        var outputLevels: [Float] = []
        playback.onPlaybackReport = { reports.append($0) }
        playback.onDrain = { drains.append($0) }
        playback.onSpeakingChanged = { speaking.append($0) }
        playback.onOutputLevel = { outputLevels.append($0) }

        // Switch to the OpenAI profile (400 ms target) and enqueue 400 ms so
        // playback starts immediately, then advance `now` across two 20 ms
        // control-loop steps so buffered audio is below target and `steer()`
        // slews the rate below 1.0.
        playback.setProvider("openai")
        for _ in 0 ..< 10 {
            playback.enqueue(Self.toneChunk(milliseconds: 40))
        }
        XCTAssertEqual(reports.map(\.state), [.started])
        XCTAssertEqual(speaking, [true])
        XCTAssertEqual(playback.metrics.scheduledChunks, 10)

        now = 0.30
        playback.steer()
        now = 0.32
        playback.steer()
        XCTAssertLessThan(playback.currentRate, 1.0)
        now = 0.34
        playback.steer()

        let out = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: engine.manualRenderingFormat, frameCapacity: 4_096))
        var peak: Float = 0
        for _ in 0 ..< 12 {
            let status = try engine.renderOffline(4_096, to: out)
            XCTAssertEqual(status, .success)
            let channel = try XCTUnwrap(out.floatChannelData?[0])
            for index in 0 ..< Int(out.frameLength) {
                peak = max(peak, abs(channel[index]))
            }
        }
        XCTAssertGreaterThan(peak, 0.1)

        // Offline rendering has no hardware DAC to fire `.dataPlayedBack`;
        // drive the completion path directly for the 10 scheduled chunks.
        for id in 0 ..< 10 {
            playback.chunkCompleted(id, level: 0.5)
        }
        XCTAssertEqual(playback.metrics.playedChunks, 10)
        XCTAssertTrue(outputLevels.contains { $0 > 0.1 })

        // Past the underrun grace window without new audio, the utterance
        // finishes cleanly as `drained`.
        now += 1.0
        playback.steer()
        playback.tick()
        XCTAssertEqual(reports.map(\.state), [.started, .drained])
        XCTAssertEqual(drains.count, 1)
        XCTAssertEqual(speaking, [true, false])
        XCTAssertEqual(outputLevels.last, 0)
        XCTAssertGreaterThan(playback.metrics.stretchedMilliseconds, 0)

        // `apply(to:)` copies playback metrics without touching capture fields.
        var combined = AudioMetrics()
        combined.hardwareSampleRate = 48_000
        combined.outputRoute = "Speaker"
        combined.captureFrames = 7
        combined.captureLatencyMilliseconds = 1.5
        combined.peakCaptureLatencyMilliseconds = 2.0
        combined.gatedFrames = 3
        playback.apply(to: &combined)
        XCTAssertEqual(combined.hardwareSampleRate, 48_000)
        XCTAssertEqual(combined.outputRoute, "Speaker")
        XCTAssertEqual(combined.captureFrames, 7)
        XCTAssertEqual(combined.gatedFrames, 3)
        XCTAssertEqual(combined.scheduledChunks, 10)
        XCTAssertEqual(combined.playedChunks, 10)
        XCTAssertEqual(combined.drains, 1)
        XCTAssertEqual(combined.bufferTargetMilliseconds, PlaybackProfile.openAI.floorMilliseconds)
    }

    func testFlushStopAndStopForFailureResetStateAndReportFlushed() throws {
        var now = 0.0
        let (engine, playback) = try Self.makeOfflineRig(clock: { now })
        try engine.start()
        playback.start()
        defer { engine.stop() }

        var reports: [PlaybackQueue.Report] = []
        var speaking: [Bool] = []
        var outputLevels: [Float] = []
        playback.onPlaybackReport = { reports.append($0) }
        playback.onSpeakingChanged = { speaking.append($0) }
        playback.onOutputLevel = { outputLevels.append($0) }

        for _ in 0 ..< 5 {
            playback.enqueue(Self.toneChunk(milliseconds: 40))
        }
        XCTAssertEqual(reports.map(\.state), [.started])
        XCTAssertEqual(speaking, [true])

        now = 0.12
        playback.steer()

        // Flush drops queued audio, resets rate to 1.0, clears speaking, and
        // leaves playback running.
        playback.flush()
        XCTAssertTrue(playback.isRunning)
        XCTAssertEqual(playback.currentRate, 1.0)
        XCTAssertEqual(reports.map(\.state), [.started, .flushed])
        XCTAssertEqual(speaking, [true, false])
        XCTAssertEqual(outputLevels.last, 0)

        // Flushing an empty player still reports `flushed`.
        playback.flush()
        XCTAssertEqual(reports.map(\.state), [.started, .flushed, .flushed])

        // `stop(in:)` halts playback and `engine`, flushes, and resets state.
        for _ in 0 ..< 5 {
            playback.enqueue(Self.toneChunk(milliseconds: 40))
        }
        playback.stop(in: engine)
        XCTAssertFalse(engine.isRunning)
        XCTAssertFalse(playback.isRunning)
        XCTAssertEqual(playback.currentRate, 1.0)
        XCTAssertEqual(reports.last?.state, .flushed)
        XCTAssertEqual(speaking.last, false)

        // `stopForFailure()` halts steering without emitting a flush report.
        try engine.start()
        playback.start()
        let reportCount = reports.count
        playback.stopForFailure()
        XCTAssertFalse(playback.isRunning)
        XCTAssertEqual(reports.count, reportCount)
    }

    func testStopTeardownOrderStopsEngineAndFlushesWhileRunningBeforeHostTeardownAndSpeakingReset() throws {
        var now = 0.0
        let (engine, playback) = try Self.makeOfflineRig(clock: { now })
        let capture = AudioCapture(watchdogThreshold: 3, clock: { now })
        try engine.start()
        playback.start()
        capture.didStart()
        defer { engine.stop() }

        playback.setProvider("openai")
        for _ in 0 ..< 10 {
            playback.enqueue(Self.toneChunk(milliseconds: 40))
        }
        now = 0.30
        playback.steer()
        now = 0.32
        playback.steer()
        XCTAssertLessThan(playback.currentRate, 1.0)

        now = 3.5
        XCTAssertEqual(capture.tickWatchdog(running: true), .lost(restart: true))
        XCTAssertTrue(capture.noSignal)

        var hostRunning = true
        var hostInterrupted = true
        var events: [String] = []
        capture.onGate = { events.append("gate:\($0)") }
        capture.onMicSignal = { events.append("micSignal:\($0)") }
        playback.onPlaybackReport = { report in
            // `engine.stop()` has already run, while `isRunning` and host
            // state are still intact when `flushed` is reported.
            XCTAssertFalse(engine.isRunning)
            XCTAssertTrue(playback.isRunning)
            XCTAssertTrue(hostRunning)
            XCTAssertTrue(hostInterrupted)
            events.append("report:\(report.state.rawValue)")
        }
        playback.onSpeakingChanged = { speaking in
            events.append("speaking:\(speaking)")
        }
        playback.onOutputLevel = { level in
            events.append("outputLevel:\(level)")
        }

        playback.stop(in: engine) {
            XCTAssertFalse(playback.isRunning)
            XCTAssertEqual(playback.currentRate, 1.0)
            capture.resetOnStop()
            hostRunning = false
            hostInterrupted = false
            events.append("paused:false")
            events.append("route:nil")
            capture.disarmWatchdog()
        }

        XCTAssertFalse(engine.isRunning)
        XCTAssertFalse(playback.isRunning)
        XCTAssertFalse(hostRunning)
        XCTAssertFalse(capture.noSignal)
        XCTAssertEqual(playback.currentRate, 1.0)
        XCTAssertEqual(
            events,
            [
                "report:flushed",
                "paused:false",
                "route:nil",
                "micSignal:false",
                "speaking:false",
                "outputLevel:0.0",
            ]
        )
    }
}
