import Foundation

/// What the top-left chip shows: the health of the connection to the
/// daemon itself — the `/ws` socket, the `/events` stream, and whether the
/// token was accepted — never the *voice* state. Those are different
/// questions ("is the phone talking to the Mac at all" vs. "can the dog
/// hear me right now"), and conflating them is what made the old dot read
/// grey (disconnected-looking) while happily connected and just quiet.
public struct ConnectionChip: Equatable, Sendable {
    public enum Status: Equatable, Sendable {
        case notConnected
        case connecting
        case connected
        case offline
        case tokenRefused
    }

    /// A colour name, kept as a word so SheepdogKit stays free of UIKit or
    /// SwiftUI — the app maps this to a `Color`.
    public enum Tint: Equatable, Sendable {
        case green, amber, red, grey
    }

    public var status: Status

    public init(status: Status) {
        self.status = status
    }

    public var word: String {
        switch status {
        case .notConnected: return "Not connected"
        case .connecting: return "Reconnecting…"
        case .connected: return "Connected"
        case .offline: return "Offline"
        case .tokenRefused: return "Token refused"
        }
    }

    public var tint: Tint {
        switch status {
        case .connected: return .green
        case .connecting: return .amber
        case .offline, .tokenRefused: return .red
        case .notConnected: return .grey
        }
    }

    /// The signals the status is derived from.
    public struct Inputs: Equatable, Sendable {
        public enum State: Equatable, Sendable {
            case idle, connecting, open, reconnecting
        }

        public var socket: State
        public var events: State
        /// A connection attempt ended in a failure that will not retry by
        /// itself (unreachable, malformed, timed out) — distinct from the
        /// socket simply never having been opened.
        public var failed: Bool
        /// The daemon refused the token (401 on `/api/state` or the
        /// WebSocket upgrade).
        public var tokenRefused: Bool

        public init(socket: State = .idle, events: State = .idle, failed: Bool = false, tokenRefused: Bool = false) {
            self.socket = socket
            self.events = events
            self.failed = failed
            self.tokenRefused = tokenRefused
        }
    }

    /// The whole rule, most urgent first: a refused token outranks a plain
    /// failure, which outranks "still connecting", which outranks the two
    /// steady states.
    public init(_ inputs: Inputs) {
        if inputs.tokenRefused {
            status = .tokenRefused
        } else if inputs.failed {
            status = .offline
        } else if inputs.socket == .connecting || inputs.socket == .reconnecting
            || inputs.events == .connecting || inputs.events == .reconnecting {
            status = .connecting
        } else if inputs.socket == .open, inputs.events == .open {
            status = .connected
        } else {
            status = .notConnected
        }
    }
}
