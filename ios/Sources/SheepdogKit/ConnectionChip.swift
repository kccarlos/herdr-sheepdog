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
        /// The first attempt of this session, before anything has dropped.
        case connecting
        /// A connection dropped and is coming back.
        case reconnecting
        case connected
        case offline
        case tokenRefused
        /// The `/ws` protocol handshake found the app and the daemon
        /// cannot talk (one of them needs updating).
        case updateNeeded
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
        case .connecting: return "Connecting…"
        case .reconnecting: return "Reconnecting…"
        case .connected: return "Connected"
        case .offline: return "Offline"
        case .tokenRefused: return "Token refused"
        case .updateNeeded: return "Update needed"
        }
    }

    public var tint: Tint {
        switch status {
        case .connected: return .green
        case .connecting, .reconnecting: return .amber
        case .offline, .tokenRefused, .updateNeeded: return .red
        case .notConnected: return .grey
        }
    }

    /// A spinner belongs next to the word: something is in flight.
    public var isBusy: Bool { status == .connecting || status == .reconnecting }

    /// The signals the status is derived from.
    public struct Inputs: Equatable, Sendable {
        public var socket: SocketLife
        public var events: SocketLife
        /// A connection attempt ended in a failure that will not retry by
        /// itself (unreachable, malformed, timed out) — distinct from the
        /// socket simply never having been opened.
        public var failed: Bool
        /// The daemon refused the token (401 on `/api/state` or the
        /// WebSocket upgrade).
        public var tokenRefused: Bool
        /// The protocol handshake ended in a mismatch.
        public var updateNeeded: Bool
        /// A socket or the event stream dropped since the session started,
        /// so whatever is in flight now is a retry, not a first connect.
        public var dropped: Bool

        public init(socket: SocketLife = .idle, events: SocketLife = .idle, failed: Bool = false, tokenRefused: Bool = false, updateNeeded: Bool = false, dropped: Bool = false) {
            self.dropped = dropped
            self.socket = socket
            self.events = events
            self.failed = failed
            self.tokenRefused = tokenRefused
            self.updateNeeded = updateNeeded
        }
    }

    /// The whole rule, most urgent first: a refused token outranks a plain
    /// failure, which outranks "still connecting", which outranks the two
    /// steady states.
    public init(_ inputs: Inputs) {
        if inputs.tokenRefused {
            status = .tokenRefused
        } else if inputs.updateNeeded {
            status = .updateNeeded
        } else if inputs.failed {
            status = .offline
        } else if inputs.socket == .connecting || inputs.socket == .reconnecting
            || inputs.events == .connecting || inputs.events == .reconnecting {
            let retrying = inputs.dropped || inputs.socket == .reconnecting || inputs.events == .reconnecting
            status = retrying ? .reconnecting : .connecting
        } else if inputs.socket == .open, inputs.events == .open {
            status = .connected
        } else {
            status = .notConnected
        }
    }
}
