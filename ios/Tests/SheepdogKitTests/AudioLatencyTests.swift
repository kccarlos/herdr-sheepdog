import AVFoundation
import XCTest
@testable import SheepdogKit

/// What the conversion actually costs, measured rather than guessed, so the
/// number in docs/architecture.md has something behind it.
final class AudioLatencyTests: XCTestCase {
    /// One tap buffer: 4 096 frames at 48 kHz ≈ 85 ms of audio.
    func testCaptureCostPerTapBuffer() throws {
        let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48_000, channels: 1, interleaved: false)!
        let converter = try PCMConverter(inputFormat: format)
        var chunker = FrameChunker()
        var gate = CaptureGate()

        let source = try XCTUnwrap(PCMConverter.sineBuffer(frequency: 440, sampleRate: 48_000, seconds: 10))
        let samples = source.floatChannelData![0]
        let blockSize = 4_096
        var blocks: [AVAudioPCMBuffer] = []
        var offset = 0
        while offset + blockSize <= Int(source.frameLength) {
            let block = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(blockSize))!
            block.frameLength = AVAudioFrameCount(blockSize)
            block.floatChannelData![0].update(from: samples + offset, count: blockSize)
            blocks.append(block)
            offset += blockSize
        }

        var worst = 0.0
        var total = 0.0
        var frames = 0
        for block in blocks {
            let start = CFAbsoluteTimeGetCurrent()
            for frame in chunker.push(try converter.convert(block)) {
                frames += gate.push(frame).send.map { _ in 1 }.reduce(0, +)
            }
            let elapsed = (CFAbsoluteTimeGetCurrent() - start) * 1000
            total += elapsed
            worst = max(worst, elapsed)
        }
        let mean = total / Double(blocks.count)
        print(String(
            format: "LATENCY capture: %d buffers of 85.3 ms, mean %.3f ms, worst %.3f ms, %d frames of 40 ms sent",
            blocks.count, mean, worst, frames
        ))
        // Real-time budget for one 85 ms buffer is 85 ms; anything near it
        // would make the conversion the bottleneck. It is not.
        XCTAssertLessThan(mean, 10, "conversion must be far inside the real-time budget")
    }

    func testPlaybackQueueDepthStaysSmall() {
        var queue = PlaybackQueue()
        var now = 0.0
        var worst = 0
        // Chunks as the model sends them: bursts of 40–730 ms.
        for milliseconds in [120, 40, 730, 60, 200, 90, 400, 40] {
            let samples = [Int16](repeating: 0, count: Int(AudioContract.playbackRate) * milliseconds / 1000)
            for chunk in queue.push(samples, now: now) {
                worst = max(worst, queue.queueDepthMilliseconds)
                queue.completed(chunk.id)
            }
            now += 0.05
        }
        print("LATENCY playback: worst queue depth \(worst) ms")
        XCTAssertLessThan(worst, 2_000)
    }
}
