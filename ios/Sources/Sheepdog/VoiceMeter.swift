import SheepdogKit
import SwiftUI

/// The numbers that change 10–25 times a second while the microphone or the
/// player is running: the input level ring, the audio metrics and the
/// voice-detection diagnostics. Split out of `SessionViewModel` so that only
/// the views that actually draw them (the voice bar's orb and mic ring, and
/// Settings ▸ About / Voice detection) re-render on every tick — `MainView`
/// and `ConversationView` read neither this object nor its properties, so
/// they stay quiet while a session just sits there listening.
@MainActor
final class VoiceMeter: ObservableObject {
    /// Input level, 0…1, for the orb's and the mic button's ring.
    @Published private(set) var micLevel: Float = 0
    @Published private(set) var metrics = AudioMetrics()
    @Published private(set) var gateDiagnostics = CaptureGate.Diagnostics()

    func setLevel(_ level: Float) {
        guard level != micLevel else { return }
        micLevel = level
    }

    func setMetrics(_ next: AudioMetrics) {
        guard next != metrics else { return }
        metrics = next
    }

    func setGateDiagnostics(_ next: CaptureGate.Diagnostics) {
        guard next != gateDiagnostics else { return }
        gateDiagnostics = next
    }
}
