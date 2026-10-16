import AVFoundation
import CoreHaptics
import UIKit

/// The moments worth feeling: a session opening, something that needs a
/// decision (and its answer landing), and stopping the voice mid-sentence.
@MainActor
enum Haptics {
    static func connected() {
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }

    /// A push arrived. Light whatever its urgency: the "needs you" cue
    /// belongs to a new question with options, not to how loud the orchestrator
    /// wanted a push to be (review A3).
    static func push() {
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
    }

    static func stop() {
        UIImpactFeedbackGenerator(style: .rigid).impactOccurred()
    }

    /// A new question with options is waiting on you (`pending.created`):
    /// its own pattern — tap, tap, then a short swell — and a short
    /// two-note chime, so it is never mistaken for a push or a warning.
    static func needsYou() {
        NeedsYouCue.shared.play()
    }

    /// The daemon took the tapped answer.
    static func answered() {
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }

    /// The answer did not land (gone, or refused).
    static func answerFailed() {
        UINotificationFeedbackGenerator().notificationOccurred(.error)
    }

    /// Something needs a look, but nothing failed (the mic went quiet).
    static func warning() {
        UINotificationFeedbackGenerator().notificationOccurred(.warning)
    }

    static func tap() {
        UISelectionFeedbackGenerator().selectionChanged()
    }

    /// A new conversation started.
    static func newConversation() {
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
    }
}

/// The needs-you cue: a Core Haptics pattern and a 0.3 s chime. The chime
/// plays through `AVAudioPlayer` on the app's shared audio session — the
/// running voice session's route, AirPods included — rather than as a
/// system sound, which is not heard in AirPods mid-session.
@MainActor
final class NeedsYouCue {
    static let shared = NeedsYouCue()

    private var engine: CHHapticEngine?
    private var player: AVAudioPlayer?

    private init() {
        if CHHapticEngine.capabilitiesForHardware().supportsHaptics {
            engine = try? CHHapticEngine()
            engine?.isAutoShutdownEnabled = true
        }
        player = try? AVAudioPlayer(data: Self.chime())
        player?.volume = 0.6
        player?.prepareToPlay()
    }

    func play() {
        playHaptic()
        player?.currentTime = 0
        player?.play()
    }

    private func playHaptic() {
        guard let engine else {
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
            return
        }
        let sharp = [CHHapticEventParameter(parameterID: .hapticIntensity, value: 1), CHHapticEventParameter(parameterID: .hapticSharpness, value: 0.9)]
        let events = [
            CHHapticEvent(eventType: .hapticTransient, parameters: sharp, relativeTime: 0),
            CHHapticEvent(eventType: .hapticTransient, parameters: sharp, relativeTime: 0.12),
            CHHapticEvent(eventType: .hapticContinuous, parameters: [
                CHHapticEventParameter(parameterID: .hapticIntensity, value: 0.6),
                CHHapticEventParameter(parameterID: .hapticSharpness, value: 0.3),
            ], relativeTime: 0.26, duration: 0.18),
        ]
        do {
            try engine.start()
            let pattern = try CHHapticPattern(events: events, parameters: [])
            try engine.makePlayer(with: pattern).start(atTime: CHHapticTimeImmediate)
        } catch {
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
        }
    }

    /// Two short notes (E6 then A6) with soft edges, as a 16-bit mono WAV.
    static func chime(sampleRate: Double = 44_100) -> Data {
        var samples: [Int16] = []
        for (frequency, seconds) in [(1318.5, 0.11), (1760.0, 0.17)] {
            let count = Int(sampleRate * seconds)
            for i in 0 ..< count {
                let t = Double(i) / sampleRate
                let attack = min(1, t / 0.008)
                let release = min(1, Double(count - i) / (sampleRate * 0.06))
                let value = sin(2 * .pi * frequency * t) * 0.35 * attack * release
                samples.append(Int16(value * Double(Int16.max)))
            }
        }
        var data = Data()
        func append<T: FixedWidthInteger>(_ v: T) { withUnsafeBytes(of: v.littleEndian) { data.append(contentsOf: $0) } }
        let bytes = samples.count * 2
        data.append(contentsOf: Array("RIFF".utf8)); append(UInt32(36 + bytes))
        data.append(contentsOf: Array("WAVE".utf8)); data.append(contentsOf: Array("fmt ".utf8))
        append(UInt32(16)); append(UInt16(1)); append(UInt16(1))
        append(UInt32(sampleRate)); append(UInt32(sampleRate * 2)); append(UInt16(2)); append(UInt16(16))
        data.append(contentsOf: Array("data".utf8)); append(UInt32(bytes))
        for s in samples { append(s) }
        return data
    }
}
