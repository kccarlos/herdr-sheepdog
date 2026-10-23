import Foundation

/// Why one attempt failed, decided for that attempt alone (never from a
/// shared, sticky flag).
public enum LinkError: Equatable, Sendable {
    /// Unreachable, timed out or reset.
    case transport
    /// Something answered over TLS with a key that matches no pin.
    case tlsUntrusted
    /// A 401 on a TLS session that matched the pin. A reason that names a
    /// `PairingProblem` (revoked, unknown device) is final; nil is a ticket
    /// that signing in again fixes.
    case unauthorized(PairingProblem?)
    /// Another HTTP status: the computer answered.
    case httpOther(Int)
    /// The reply could not be read.
    case decode
    /// The protocol handshake found the app and the daemon cannot talk.
    case incompatible(String)
    /// The app cancelled it. Never an error.
    case cancelled
}

/// The staggered, pinned race across a computer's addresses
/// (docs/dev/design/ios-state-architecture.md 3.3), as a value.
///
/// The remembered winner for the current interface goes first. If it has
/// not answered after `stagger`, the next one starts, and so on; a failure
/// starts the next one at once. The first pinned success wins and the
/// others are cancelled. The pin is the check, never the host, so trying
/// several addresses at once is safe.
public struct AddressRace: Equatable, Sendable {
    public static let stagger: TimeInterval = 0.3

    /// Identifies this race: probe results carry it, and a result for any
    /// other race is stale.
    public let id: Int
    /// The addresses, in the order they start.
    public let order: [String]
    /// How many of `order` have started.
    public private(set) var started = 0
    /// The failed addresses and why, in the order they failed.
    public private(set) var failures: [Failure] = []
    public private(set) var winner: String?

    public struct Failure: Equatable, Sendable {
        public var address: String
        public var error: LinkError

        public init(address: String, error: LinkError) {
            self.address = address
            self.error = error
        }
    }

    public init(id: Int, order: [String]) {
        self.id = id
        self.order = order
    }

    /// The order to race in: the address that won last time on this
    /// interface first, then the computer's list (preferred first), without
    /// repeats.
    public static func order(addresses: [String], winners: [NetworkInterface: String], interface: NetworkInterface?) -> [String] {
        var order: [String] = []
        if let interface, let winner = winners[interface], addresses.contains(winner) {
            order.append(winner)
        }
        for address in addresses where !order.contains(address) {
            order.append(address)
        }
        return order
    }

    /// Started, and neither failed nor won yet.
    public var inFlight: [String] {
        guard winner == nil else { return [] }
        return order.prefix(started).filter { address in !failures.contains { $0.address == address } }
    }

    public var hasMoreToStart: Bool { winner == nil && started < order.count }

    /// Every address failed.
    public var isLost: Bool { winner == nil && started == order.count && failures.count == order.count }

    /// Starts the first address; returns what to probe now.
    public mutating func start() -> [String] { startNext() }

    /// The stagger elapsed with no answer: start the next address.
    public mutating func staggerElapsed() -> [String] { startNext() }

    /// A pinned success. Returns the addresses still in flight, to cancel;
    /// nil when the result does not belong to this race any more.
    public mutating func succeeded(_ address: String) -> [String]? {
        guard inFlight.contains(address) else { return nil }
        let others = inFlight.filter { $0 != address }
        winner = address
        return others
    }

    /// A failed attempt. Returns what to probe now (the next address
    /// starts at once); nil when the result does not belong to this race.
    public mutating func failed(_ address: String, _ error: LinkError) -> [String]? {
        guard inFlight.contains(address) else { return nil }
        failures.append(Failure(address: address, error: error))
        return startNext()
    }

    private mutating func startNext() -> [String] {
        guard hasMoreToStart else { return [] }
        started += 1
        return [order[started - 1]]
    }

    // MARK: - The verdict when every address failed

    public enum Verdict: Equatable, Sendable {
        /// A computer answered and said it cannot talk to this app.
        case incompatible(String)
        /// A verified computer refused this device: only a scan fixes it.
        case refused(PairingProblem)
        /// Something answered with a key that matches no pin, and nothing
        /// verified: warn, send nothing, no forced rescan.
        case untrusted([String])
        /// Nothing usable answered: back off and retry. `renewTicket` when a
        /// verified computer only wants a fresh ticket.
        case unreachable(LinkError, renewTicket: Bool)
    }

    /// What every address failing means, most decisive first:
    /// - a handshake mismatch;
    /// - a 401 with a known reason on a verified session (revoked, unknown
    ///   device);
    /// - a pin mismatch: `renewedWhileAway` (scan again) only when *every*
    ///   address mismatched and the certificate may have rotated, else the
    ///   untrusted-answer warning (decision 1);
    /// - anything else: unreachable, with the first error as the reason.
    public static func verdict(_ failures: [Failure], certExpires: Date?, now: Date) -> Verdict {
        for failure in failures {
            if case let .incompatible(message) = failure.error { return .incompatible(message) }
        }
        for failure in failures {
            if case let .unauthorized(problem?) = failure.error { return .refused(problem) }
        }
        let mismatched = failures.filter { $0.error == .tlsUntrusted }.map(\.address)
        if !mismatched.isEmpty {
            if mismatched.count == failures.count,
               PairingProblem.forPinMismatch(certExpires: certExpires, now: now) == .renewedWhileAway {
                return .refused(.renewedWhileAway)
            }
            return .untrusted(mismatched)
        }
        let renewTicket = failures.contains { $0.error == .unauthorized(nil) }
        return .unreachable(failures.first?.error ?? .transport, renewTicket: renewTicket)
    }
}
