import Foundation
import SheepdogKit

/// What the Health pane shows from `/events`: uptime, GPT-Live's billed
/// seconds and the recent problems (`DaemonHealth`), and whether the pane
/// is fetching. Split out of `SessionViewModel` (phase 8), so an event that
/// changes a problem count re-renders the Health pane and nothing else.
/// The fetch that fills it stays in `SessionViewModel.loadHealth`, which
/// also refreshes the models, settings and providers the pane shows.
@MainActor
@Observable
final class HealthModel {
    private(set) var health = DaemonHealth()
    private(set) var loading = false

    /// One `/events` event; assigns only when it changed something.
    func fold(_ event: DaemonEvent) {
        var next = health
        if next.fold(event) { health = next }
    }

    /// The computer changed: the problems were the old one's.
    func clear() { health.clear() }

    /// The fetch started; false when one is already running.
    func begin() -> Bool {
        guard !loading else { return false }
        loading = true
        return true
    }

    func end() { loading = false }

    func replace(with next: DaemonHealth) {
        if next != health { health = next }
    }
}
