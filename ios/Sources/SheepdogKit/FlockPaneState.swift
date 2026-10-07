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
    /// The session was never started (or was ended).
    case notConnected
    /// The daemon cannot be reached; Retry helps.
    case unreachable
    /// The daemon refused the token; Settings helps.
    case tokenRefused
    /// App and daemon protocols differ; updating one helps.
    case updateNeeded

    public static func resolve(chip: ConnectionChip.Status, hasAgents: Bool) -> FlockPaneState {
        if hasAgents { return .content }
        switch chip {
        case .connecting: return .loading
        case .reconnecting: return .reconnecting
        case .connected: return .empty
        case .notConnected: return .notConnected
        case .offline: return .unreachable
        case .tokenRefused: return .tokenRefused
        case .updateNeeded: return .updateNeeded
        }
    }
}
