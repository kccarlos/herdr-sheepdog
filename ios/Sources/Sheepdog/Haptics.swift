import UIKit

/// The moments worth feeling: a session opening, something that needs a
/// decision (and its answer landing), and stopping the dog mid-sentence.
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

    /// A worker is waiting on an approval.
    static func approvalAppeared() {
        UINotificationFeedbackGenerator().notificationOccurred(.warning)
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
