import Foundation
import SheepdogKit

/// The app's answer to "may what I say and type go to AI services?", for
/// the views and the gate in `SessionViewModel+Consent.swift`. The store is
/// `AIConsent` (UserDefaults, versioned). The demo sends nothing anywhere,
/// so it never asks; debug launches that start already configured do not
/// ask either unless `-sheepdogAskConsent` is given.
@MainActor
@Observable
final class AIConsentModel {
    @ObservationIgnored private let store: AIConsent
    @ObservationIgnored private let bypass: Bool

    private(set) var decision: AIConsent.Decision
    /// What to do once the person agrees: the connect that asked.
    @ObservationIgnored var afterAgree: (() -> Void)?

    init(store: AIConsent, bypass: Bool = false) {
        self.store = store
        self.bypass = bypass
        decision = bypass ? .allowed : store.decision
    }

    var isAllowed: Bool { decision == .allowed }

    func agree() {
        guard !bypass else { return }
        store.agree()
        decision = store.decision
    }

    func withdraw() {
        guard !bypass else { return }
        store.withdraw()
        decision = store.decision
    }
}
