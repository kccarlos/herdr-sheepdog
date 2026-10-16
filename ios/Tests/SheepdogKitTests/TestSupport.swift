import AVFoundation
import Foundation
@testable import SheepdogKit

/// Test-only helpers that used to live in the shipping `SheepdogKit`
/// framework: signal generators, a fake `SecretStore`, and wire-format
/// conveniences nothing but the test target ever called.

// MARK: - PCMConverter signal generators

extension PCMConverter {
    /// Builds a mono Float32 buffer holding `seconds` of a sine — the test
    /// signal for the 48 k → 16 k path, and a way to exercise the pipeline
    /// without a microphone.
    static func sineBuffer(frequency: Double, sampleRate: Double, seconds: Double, amplitude: Float = 0.5) -> AVAudioPCMBuffer? {
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
    static func toneMagnitude(_ samples: [Int16], frequency: Double, sampleRate: Double) -> Double {
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

// MARK: - A fake SecretStore

/// A fake for the tests: the keychain needs a host app and an access group,
/// neither of which a plain XCTest bundle has.
final class InMemorySecretStore: SecretStore {
    private(set) var values: [String: String] = [:]

    init(_ values: [String: String] = [:]) {
        self.values = values
    }

    func secret(for account: String) -> String? { values[account] }

    func setSecret(_ value: String?, for account: String) {
        if let value, !value.isEmpty {
            values[account] = value
        } else {
            values.removeValue(forKey: account)
        }
    }
}

// MARK: - WSMessage wire-format conveniences

extension WSMessage {
    /// A client `text` message, for tests that simulate the wire without a
    /// real socket.
    static func simulateText(_ text: String) -> WSMessage { WSMessage(type: .text, text: text) }

    /// `encode()` as a `String`, for asserting on the exact bytes.
    func encodedString() throws -> String {
        String(decoding: try encode(), as: UTF8.self)
    }
}

// MARK: - PendingAction convenience

extension PendingAction {
    /// The option the prompt's cursor is on, if the daemon marked one.
    var defaultOption: PendingOption? { options?.first { $0.isDefault } }
}
