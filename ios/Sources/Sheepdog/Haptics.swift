import UIKit

/// The three moments worth feeling: a session opening, something that needs
/// a decision, and stopping the dog mid-sentence.
@MainActor
enum Haptics {
    static func connected() {
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }

    static func push(needsUser: Bool) {
        if needsUser {
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
        } else {
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
        }
    }

    static func stop() {
        UIImpactFeedbackGenerator(style: .rigid).impactOccurred()
    }

    static func tap() {
        UISelectionFeedbackGenerator().selectionChanged()
    }
}
