import AVFoundation
import Foundation

/// Converts the microphone's hardware format to the contract's capture
/// format: PCM16 little-endian, mono, 16 000 Hz.
///
/// `AVAudioConverter` does the anti-alias filtering and the rate change
/// (the browser does the same job with a 31-tap FIR plus a linear
/// resampler; here the system converter is both better and cheaper).
/// Sample-rate conversion needs the pull API — `convert(to:error:withInputFrom:)`
/// — because a rate change is not a one-in-one-out transform.
public final class PCMConverter {
    public enum Failure: Error, CustomStringConvertible {
        case unsupportedInputFormat(String)
        case converterUnavailable
        case conversion(String)

        public var description: String {
            switch self {
            case let .unsupportedInputFormat(d): return "unsupported microphone format: \(d)"
            case .converterUnavailable: return "the system could not build an audio converter"
            case let .conversion(m): return "audio conversion failed: \(m)"
            }
        }
    }

    public let inputFormat: AVAudioFormat
    public let outputFormat: AVAudioFormat
    private let converter: AVAudioConverter

    /// The contract's capture format.
    public static func captureFormat() -> AVAudioFormat {
        AVAudioFormat(
            commonFormat: .pcmFormatInt16,
            sampleRate: AudioContract.captureRate,
            channels: 1,
            interleaved: true
        )!
    }

    public init(inputFormat: AVAudioFormat, outputFormat: AVAudioFormat = PCMConverter.captureFormat()) throws {
        guard inputFormat.channelCount > 0, inputFormat.sampleRate > 0 else {
            // The simulator sometimes reports a 0-channel input format before
            // a route settles; say so plainly instead of trapping.
            throw Failure.unsupportedInputFormat("\(inputFormat.sampleRate) Hz, \(inputFormat.channelCount) ch")
        }
        guard let converter = AVAudioConverter(from: inputFormat, to: outputFormat) else {
            throw Failure.converterUnavailable
        }
        converter.sampleRateConverterQuality = AVAudioQuality.high.rawValue
        self.inputFormat = inputFormat
        self.outputFormat = outputFormat
        self.converter = converter
    }

    /// Converts one tap buffer. Returns the Int16 samples at 16 kHz; the
    /// converter keeps its own phase between calls, so a stream chopped into
    /// arbitrary blocks yields the same samples as one big call.
    public func convert(_ buffer: AVAudioPCMBuffer) throws -> [Int16] {
        guard buffer.frameLength > 0 else { return [] }
        let ratio = outputFormat.sampleRate / inputFormat.sampleRate
        // Slack for the resampler's internal delay so nothing is truncated.
        let capacity = AVAudioFrameCount((Double(buffer.frameLength) * ratio).rounded(.up)) + 64
        guard let out = AVAudioPCMBuffer(pcmFormat: outputFormat, frameCapacity: capacity) else {
            throw Failure.conversion("could not allocate an output buffer")
        }

        var supplied = false
        var error: NSError?
        let status = converter.convert(to: out, error: &error) { _, outStatus in
            if supplied {
                outStatus.pointee = .noDataNow
                return nil
            }
            supplied = true
            outStatus.pointee = .haveData
            return buffer
        }

        switch status {
        case .haveData, .inputRanDry, .endOfStream:
            break
        case .error:
            throw Failure.conversion(error?.localizedDescription ?? "unknown")
        @unknown default:
            throw Failure.conversion("unexpected converter status")
        }
        if let error { throw Failure.conversion(error.localizedDescription) }
        return PCMConverter.samples(of: out)
    }

    /// Drops any state the converter carries (after a mute, a route change
    /// or a reconnect: the discontinuity makes the tail meaningless).
    public func reset() { converter.reset() }

    // MARK: - Helpers shared with the tests

    /// Reads the Int16 samples out of an interleaved mono buffer.
    public static func samples(of buffer: AVAudioPCMBuffer) -> [Int16] {
        guard let channels = buffer.int16ChannelData, buffer.frameLength > 0 else { return [] }
        let n = Int(buffer.frameLength)
        return Array(UnsafeBufferPointer(start: channels[0], count: n))
    }

    /// Builds a mono Float32 buffer holding `seconds` of a sine — the test
    /// signal for the 48 k → 16 k path, and a way to exercise the pipeline
    /// without a microphone.
    public static func sineBuffer(frequency: Double, sampleRate: Double, seconds: Double, amplitude: Float = 0.5) -> AVAudioPCMBuffer? {
        guard let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: sampleRate, channels: 1, interleaved: false) else { return nil }
        let frames = AVAudioFrameCount(sampleRate * seconds)
        guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames), let data = buffer.floatChannelData else { return nil }
        buffer.frameLength = frames
        let step = 2.0 * Double.pi * frequency / sampleRate
        for i in 0 ..< Int(frames) {
            data[0][i] = amplitude * Float(sin(step * Double(i)))
        }
        return buffer
    }

    /// Goertzel magnitude of `frequency` in `samples` — how the tests check
    /// that a 440 Hz tone is still a 440 Hz tone after the rate change.
    public static func toneMagnitude(_ samples: [Int16], frequency: Double, sampleRate: Double) -> Double {
        guard samples.count > 1 else { return 0 }
        let k = 2.0 * cos(2.0 * Double.pi * frequency / sampleRate)
        var s1 = 0.0, s2 = 0.0
        for sample in samples {
            let value = Double(sample) / 32_768.0
            let s0 = value + k * s1 - s2
            s2 = s1
            s1 = s0
        }
        let power = s1 * s1 + s2 * s2 - k * s1 * s2
        return sqrt(max(0, power)) / Double(samples.count)
    }
}
