import Foundation

/// What the Agents pane shows when it has nothing to list, and why. Pure, so
/// the rule is unit-tested: an empty list means different things depending
/// on whether the daemon is reachable.
///
/// Agents already on screen are never hidden by a connection problem; the
/// chip says the list may be stale.
public enum FlockPaneState: Equatable, Sendable {
    /// Agents to show.
    case content
    /// The first connection is in flight and nothing has arrived yet.
    case loading
    /// A connection was lost and is coming back; nothing to show meanwhile.
    case reconnecting
    /// Connected, and herdr reports no agent.
    case empty
    /// Nothing needs the link (no computer paired yet).
    case notConnected
    /// The computer cannot be reached past the quiet period; Try now helps.
    case unreachable
    /// The phone itself has no network.
    case noNetwork
    /// Something on this network answered with another key.
    case untrusted
    /// The computer refused this device; a new scan helps.
    case tokenRefused
    /// App and daemon protocols differ; updating one helps.
    case updateNeeded

    /// From the computer's link, the same truth as the chip.
    public static func resolve(link: ComputerLink, hasAgents: Bool) -> FlockPaneState {
        if hasAgents { return .content }
        switch link.state {
        case .idle: return .notConnected
        case let .connecting(_, outage):
            guard let outage else { return .loading }
            return outage.long ? .unreachable : .reconnecting
        case .waiting: return .reconnecting
        case .online, .degraded: return .empty
        case .offline: return .unreachable
        case .noNetwork: return .noNetwork
        case .untrustedAnswer: return .untrusted
        case .refused: return .tokenRefused
        case .incompatible: return .updateNeeded
        }
    }
}
