import Foundation

/// Hands the daemon's `screen` pushes (`/ws`) straight to the window that
/// shows that pane, by a call: no observable property carries them, so a
/// frame re-renders nothing but that window's surface
/// (docs/dev/design/ios-state-architecture.md, 3.7).
///
/// High-frequency: up to a frame per screen change. Its only readers are
/// the agent windows' controllers (`AgentWindowController`).
@MainActor
public final class ScreenStreamRouter {
    public typealias Handler = (AgentScreenEvent) -> Void

    /// Ends one window's subscription; also ends when released.
    public final class Subscription {
        private var cancel: (() -> Void)?

        init(cancel: @escaping () -> Void) { self.cancel = cancel }

        public func end() {
            cancel?()
            cancel = nil
        }

        deinit {
            let cancel = self.cancel
            if let cancel { MainActor.assumeIsolated { cancel() } }
        }
    }

    private var handlers: [String: [Int: Handler]] = [:]
    private var next = 0

    public init() {}

    /// `handler` gets every push for `paneID` until the subscription ends.
    public func subscribe(paneID: String, _ handler: @escaping Handler) -> Subscription {
        next += 1
        let id = next
        handlers[paneID, default: [:]][id] = handler
        return Subscription { [weak self] in
            self?.handlers[paneID]?[id] = nil
            if self?.handlers[paneID]?.isEmpty == true { self?.handlers[paneID] = nil }
        }
    }

    /// One push for `paneID`; dropped when no window shows it.
    public func deliver(_ event: AgentScreenEvent, to paneID: String) {
        guard let forPane = handlers[paneID] else { return }
        for handler in forPane.values { handler(event) }
    }

    /// How many windows listen to `paneID` (tests).
    public func listeners(_ paneID: String) -> Int { handlers[paneID]?.count ?? 0 }
}
