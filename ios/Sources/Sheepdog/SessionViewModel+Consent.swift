import Foundation
import SheepdogKit

/// The gate in front of everything that sends the person's voice or typed
/// words to the computer, and from there to AI services (App Review
/// Guidelines 5.1.2(i)). Two paths cross it: starting the voice session
/// (`connect()`, and the probe that follows) and a typed line
/// (`sendTyped(_:)`). The offline demo never asks.
extension SessionViewModel {
    /// The explanation shown when a typed line is blocked.
    static let aiOffMessage = "Sending to AI services is off. Turn it on in Settings > Privacy."

    /// True when sending is allowed. Otherwise the consent screen is up
    /// (first ask, or a way back after a withdrawal) and the caller stops.
    /// `onAgree` runs if the person agrees there.
    func requireAIConsent(onAgree: (() -> Void)? = nil) -> Bool {
        if consent.isAllowed { return true }
        consent.afterAgree = onAgree
        Task { await loadHello() }
        presentation.present(.consent)
        return false
    }

    /// Agree on the consent screen or turn the Settings switch on.
    func agreeToAIServices() {
        consent.agree()
        presentation.dismiss(.consent)
        let next = consent.afterAgree
        consent.afterAgree = nil
        next?()
    }

    /// Not now on the consent screen: nothing was sent, nothing changes.
    func declineAIServices() {
        consent.afterAgree = nil
        presentation.dismiss(.consent)
    }

    /// The Settings switch off: a running voice session ends at once.
    func withdrawFromAIServices() {
        consent.withdraw()
        if wantsConnection { disconnect() }
    }

    /// "Google Gemini (voice) and DeepSeek (answers)", or the generic
    /// words when this computer has not said yet.
    var aiServicesSummary: String {
        AIServices.summary(voice: hello?.voiceProvider ?? voiceProviders?.provider, brain: brain?.provider)
    }
}
