import AVFoundation
import XCTest
import SheepdogKit
@testable import SheepdogMac

/// The microphone's multi-channel format, from generated buffers: the
/// voice-processing input saw six channels at 48 kHz with channel 0 silent.
final class MicMixdownTests: XCTestCase {
    private let rate = 48_000.0

    /// `channels` channels of `frames` samples; `fill(channel, frame)` says each.
    private func buffer(channels: Int, frames: Int = 4_800, interleaved: Bool = false, _ fill: (Int, Int) -> Float) throws -> AVAudioPCMBuffer {
        // More than two channels need a layout, as the hardware's has.
        let layout = try XCTUnwrap(AVAudioChannelLayout(layoutTag: kAudioChannelLayoutTag_DiscreteInOrder | UInt32(channels)))
        let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: rate, interleaved: interleaved, channelLayout: layout)
        let buffer = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(max(frames, 1))))
        buffer.frameLength = AVAudioFrameCount(frames)
        let data = try XCTUnwrap(buffer.floatChannelData)
        for frame in 0 ..< frames {
            for channel in 0 ..< channels {
                if interleaved {
                    data[0][frame * channels + channel] = fill(channel, frame)
                } else {
                    data[channel][frame] = fill(channel, frame)
                }
            }
        }
        return buffer
    }

    private func sine(_ frame: Int, amplitude: Float = 0.5) -> Float {
        amplitude * Float(sin(2 * Double.pi * 440 * Double(frame) / rate))
    }

    private func samples(_ buffer: AVAudioPCMBuffer) throws -> [Float] {
        let data = try XCTUnwrap(buffer.floatChannelData)
        return Array(UnsafeBufferPointer(start: data[0], count: Int(buffer.frameLength)))
    }

    func testSixChannelsWithChannelZeroSilentKeepTheSignalAtFullLevel() throws {
        let six = try buffer(channels: 6) { channel, frame in channel == 3 ? sine(frame) : 0 }
        let mono = try XCTUnwrap(MicMixdown.mono(six))
        XCTAssertEqual(mono.format.channelCount, 1)
        XCTAssertEqual(mono.format.sampleRate, rate)
        XCTAssertEqual(mono.frameLength, six.frameLength)
        let out = try samples(mono)
        XCTAssertEqual(out.map(abs).max() ?? 0, 0.5, accuracy: 0.001)
        for frame in [0, 17, 1_000, 4_799] { XCTAssertEqual(out[frame], sine(frame), accuracy: 1e-6) }
    }

    func testChannelsThatAllCarrySoundAreAveraged() throws {
        let six = try buffer(channels: 6) { channel, frame in channel == 0 ? 0 : sine(frame) }
        let out = try samples(XCTUnwrap(MicMixdown.mono(six)))
        for frame in [3, 100, 2_500] { XCTAssertEqual(out[frame], sine(frame), accuracy: 1e-6) }
    }

    func testTwoDifferentChannelsAreMixedNotPicked() throws {
        let two = try buffer(channels: 2) { channel, frame in channel == 0 ? sine(frame, amplitude: 0.4) : -sine(frame, amplitude: 0.2) }
        let out = try samples(XCTUnwrap(MicMixdown.mono(two)))
        for frame in [5, 123, 4_000] { XCTAssertEqual(out[frame], sine(frame, amplitude: 0.1), accuracy: 1e-6) }
    }

    func testAnInterleavedBufferMixesTheSameWay() throws {
        let interleaved = try buffer(channels: 3, interleaved: true) { channel, frame in channel == 1 ? sine(frame) : 0 }
        let out = try samples(XCTUnwrap(MicMixdown.mono(interleaved)))
        for frame in [1, 77, 4_321] { XCTAssertEqual(out[frame], sine(frame), accuracy: 1e-6) }
    }

    func testAnAllZeroBufferStaysZeroSoTheWatchdogSeesIt() throws {
        let silent = try buffer(channels: 6) { _, _ in 0 }
        let out = try samples(XCTUnwrap(MicMixdown.mono(silent)))
        XCTAssertTrue(out.allSatisfy { $0 == 0 })
        XCTAssertEqual(PCMLevel.peak(of: [Int16](repeating: 0, count: out.count)), 0)
    }

    func testAnEmptyOrNonFloatBufferIsRefused() throws {
        let empty = try buffer(channels: 2, frames: 0) { _, _ in 0 }
        XCTAssertNil(MicMixdown.mono(empty))
        let format = try XCTUnwrap(AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: rate, channels: 1, interleaved: true))
        let ints = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 16))
        ints.frameLength = 16
        XCTAssertNil(MicMixdown.mono(ints))
    }

    func testTheResultOwnsItsMemory() throws {
        let six = try buffer(channels: 6) { channel, frame in channel == 3 ? sine(frame) : 0 }
        let mono = try XCTUnwrap(MicMixdown.mono(six))
        let data = try XCTUnwrap(six.floatChannelData)
        for channel in 0 ..< 6 { data[channel][10] = 9 }
        XCTAssertEqual(try samples(mono)[10], sine(10), accuracy: 1e-6)
    }

    /// The whole capture format step: six channels at 48 kHz in, the 16 kHz
    /// mono PCM16 `/ws` expects out, with the tone still there.
    func testSixChannelsBecomeTheContractsCaptureFormat() throws {
        let seconds = 0.5
        let frames = Int(rate * seconds)
        let six = try buffer(channels: 6, frames: frames) { channel, frame in channel == 2 ? sine(frame, amplitude: 0.5) : 0 }
        let mono = try XCTUnwrap(MicMixdown.mono(six))
        let converter = try PCMConverter(inputFormat: mono.format)
        let pcm = try converter.convert(mono)
        XCTAssertEqual(Double(pcm.count), AudioContract.captureRate * seconds, accuracy: 200)
        let peak = PCMLevel.peak(of: pcm)
        XCTAssertEqual(Double(peak), 0.5 * 32_768, accuracy: 0.05 * 32_768)
    }
}
