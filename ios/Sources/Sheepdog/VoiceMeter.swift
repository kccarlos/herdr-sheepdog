import SheepdogKit
import SwiftUI

/// The numbers that change 10–25 times a second while the microphone or the
/// player is running: the input level ring, the audio metrics and the
/// voice-detection diagnostics. Split out of `SessionViewModel` and
/// observed per property (`@Observable`), so only the views that read a
/// level re-render on its ticks: the orb (`MeteredOrb`) and the mic ring
/// (`MeteredMicButton`) for the levels, Health and Diagnostics for the
/// metrics. The capsule, its menu and the rest of the app read none of
/// them, so they stay quiet while a session sits there listening.
///
/// High-frequency (10–25 Hz): `micLevel` and `outputLevel`'s only readers
/// are `MeteredMicButton` and `MeteredOrb`.
@MainActor
@Observable
final class VoiceMeter {
    /// Input level, 0…1, for the orb's and the mic button's ring.
    private(set) var micLevel: Float = 0
    /// Output level, 0…1, while the voice speaks: the orb's waveform.
    private(set) var outputLevel: Float = 0
    private(set) var metrics = AudioMetrics()
    private(set) var gateDiagnostics = CaptureGate.Diagnostics()

    func setLevel(_ level: Float) {
        guard level != micLevel else { return }
        micLevel = level
    }

    func setOutputLevel(_ level: Float) {
        // Steps of 5 %: the waveform needs no finer, and fewer changes
        // mean fewer redraws.
        let stepped = (level * 20).rounded() / 20
        guard stepped != outputLevel else { return }
        outputLevel = stepped
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
