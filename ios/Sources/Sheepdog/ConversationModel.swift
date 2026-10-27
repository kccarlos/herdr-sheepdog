import Foundation
import SheepdogKit

/// What the Conversation pane shows and the line typed under it: the rows
/// (history, live bubbles and the dividers between them), whether an earlier
/// page can be loaded, and the typed line's in-flight and error state. Split
/// out of `SessionViewModel` (phase 8), so a bubble growing or a typed word
/// re-renders the views that read these and nothing else.
///
/// `rows` is a mirror the session writes, not derived from the current
/// `ComputerModel`: it merges the history with the live voice bubbles, the
/// activity lines and the cards this phone got, and the computer's
/// conversation is `@ObservationIgnored` on purpose. `rows` and
/// `canLoadOlder` assign only when the value changed.
@MainActor
@Observable
final class ConversationModel {
    /// The conversation as the screen lists it.
    private(set) var rows: [TimelineRow] = []
    /// An earlier page of history is on the daemon.
    private(set) var canLoadOlder = false
    /// The earlier page is on its way.
    private(set) var loadingOlder = false
    /// Typed input (`POST /api/utterance`) in flight.
    private(set) var sendingTyped = false
    /// The daemon's refusal of the typed line, shown in the composer.
    var typedError: String?
    /// Bumped on every typed line the daemon took: the conversation jumps to
    /// its end, where the words and the answer are about to land.
    private(set) var typedSent = 0

    func setRows(_ next: [TimelineRow]) {
        if rows != next { rows = next }
    }

    func setCanLoadOlder(_ next: Bool) {
        if canLoadOlder != next { canLoadOlder = next }
    }

    func setLoadingOlder(_ next: Bool) {
        if loadingOlder != next { loadingOlder = next }
    }

    func setSendingTyped(_ next: Bool) {
        if sendingTyped != next { sendingTyped = next }
    }

    func setTypedError(_ next: String?) {
        if typedError != next { typedError = next }
    }

    func typedLineSent() {
        typedSent += 1
    }
}
