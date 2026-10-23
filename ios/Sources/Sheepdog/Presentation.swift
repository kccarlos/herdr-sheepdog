import Observation
import SheepdogKit
import SwiftUI

/// A sheet or cover the session shows over the main screen. Its `id`
/// comes from what it shows, never from `UUID()`: asking again for the
/// same thing is the same presentation, so a re-render cannot close and
/// reopen it (docs/dev/design/ios-state-architecture.md, 3.6).
enum Presentation: Identifiable, Equatable {
    case settings
    /// A pairing code from outside the app, or "Please scan again".
    case pairing(PairingStage)
    case card(ScreenCard)
    case newAgent
    case spaces
    /// The capsule menu's connection row.
    case connection
    /// The screen before a system permission prompt.
    case primer(PermissionPrimer)
    /// The offline demo.
    case demo

    var id: String {
        switch self {
        case .settings: "settings"
        case let .pairing(stage): stage.presentationID
        case let .card(card): "card.\(card.cardID)"
        case .newAgent: "newAgent"
        case .spaces: "spaces"
        case .connection: "connection"
        case let .primer(primer): "primer.\(primer.rawValue)"
        case .demo: "demo"
        }
    }

    /// Full screen (a cover) rather than a sheet.
    var isCover: Bool {
        switch self {
        case .primer, .demo: true
        default: false
        }
    }
}

/// What waits for the screen to be clear of sheets.
enum PresentationFollowup: Equatable {
    /// Open an agent's or a shell's window (a push made under a closing
    /// sheet is lost).
    case openWindow(WindowRequest)
}

/// Which sheet or cover the session shows, one at a time, with the rest
/// queued (`PresentationQueue`). One for the app, shown by the presenter
/// window only (`SceneCoordinator.presenter`), so two iPad windows never
/// show the same sheet.
///
/// Views bind to `sheet` and `cover` and report `appeared` and
/// `didDismiss`; everything else asks with an intent (`present`,
/// `dismiss`), never by setting a flag.
@MainActor
@Observable
final class PresentationCoordinator {
    private(set) var queue = PresentationQueue<Presentation, PresentationFollowup>()
    /// Runs what waited for a clear screen.
    @ObservationIgnored var onFollowup: (PresentationFollowup) -> Void = { _ in }
    /// A presentation finished closing (the pairing sheet goes back to
    /// first-run Settings when nothing is paired).
    @ObservationIgnored var onClosed: (Presentation) -> Void = { _ in }

    var current: Presentation? { queue.current }

    func isShowing(_ item: Presentation) -> Bool { queue.current?.id == item.id }

    /// The card on screen, if one is.
    var card: ScreenCard? {
        if case let .card(card) = queue.current { return card }
        return nil
    }

    func present(_ item: Presentation) { queue.present(item) }

    /// Closes what is up, then shows `item`.
    func presentInstead(_ item: Presentation) { apply { $0.presentInstead(item) } }

    /// In place of what is up, with no dismissal in between (a newer card).
    func replace(_ item: Presentation) { queue.replace(item) }

    func dismiss(_ item: Presentation) { apply { $0.dismiss(item.id) } }

    func dismissAll() { apply { $0.dismiss() } }

    /// Closes what is up, then runs `followup`.
    func dismiss(then followup: PresentationFollowup) { apply { $0.dismiss(then: followup) } }

    func appeared(_ item: Presentation) { queue.appeared(item.id) }

    /// The sheet or cover finished closing.
    func didDismiss() {
        let closing = queue.closing
        apply { $0.didDismiss() }
        if let closing, queue.closing == nil { onClosed(closing) }
    }

    /// Runs a change, then what it handed back. An item taken down that
    /// was never on screen closes at once, with no `didDismiss` to report
    /// it: `onClosed` hears about it here.
    private func apply(_ change: (inout PresentationQueue<Presentation, PresentationFollowup>) -> [PresentationFollowup]) {
        let before = queue.current
        let ready = change(&queue)
        if let before, queue.current?.id != before.id, queue.closing?.id != before.id,
           !queue.pending.contains(where: { $0.id == before.id }) {
            onClosed(before)
        }
        for followup in ready { onFollowup(followup) }
    }
}

extension PairingStage {
    /// The pairing sheet's identity: where it started, with its code or
    /// problem (`id` alone names only the screen).
    var presentationID: String {
        switch self {
        case .primer: "pairing.primer"
        case .denied: "pairing.denied"
        case .noCamera: "pairing.noCamera"
        case .scanning: "pairing.scanning"
        case let .confirm(code): "pairing.confirm.\(code.daemonID).\(code.code)"
        case let .working(name): "pairing.working.\(name)"
        case let .paired(name): "pairing.paired.\(name)"
        case let .problem(problem): "pairing.problem.\(problem)"
        case let .failed(title, _): "pairing.failed.\(title)"
        }
    }
}
