import Foundation

/// An address the user adds for the paired computer (Settings ▸ Connection
/// ▸ Addresses): a router's port forward, whose address the computer
/// cannot know and so cannot put in the pairing code
/// (docs/remote-access.md "Through a router"). The app checks it like
/// every other address, by the computer's pin, before it keeps it.
public enum ComputerAddress {
    /// Why a typed address is refused before anything is sent.
    public enum Problem: Error, Equatable, Sendable {
        case empty
        case unreadable
        case tailscaleName
        case thisPhone
        case known

        public var message: String {
            switch self {
            case .empty, .unreadable:
                return "Type an address and port, like 100.101.1.1:7777."
            case .tailscaleName:
                return "Use the IP address instead. On a .ts.net name the computer can answer with its Tailscale certificate, which the app doesn't use."
            case .thisPhone:
                return "That address is this phone, not your computer."
            case .known:
                return "This computer already has that address."
            }
        }
    }

    /// The `host:port` form the app stores (IPv6 in brackets, port 7777
    /// when none is given), or why the text is refused.
    public static func normalize(_ text: String, known: [String] = []) -> Result<String, Problem> {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return .failure(.empty) }
        guard let endpoint = DaemonEndpoint(text: trimmed, secure: true), !endpoint.host.contains(" ") else {
            return .failure(.unreadable)
        }
        let host = endpoint.host.lowercased()
        if host.hasSuffix(".ts.net") || host.hasSuffix(".ts.net.") { return .failure(.tailscaleName) }
        if DaemonEndpoint.isLoopback(host) { return .failure(.thisPhone) }
        let address = host.contains(":") ? "[\(host)]:\(endpoint.port)" : "\(host):\(endpoint.port)"
        if known.contains(where: { $0.lowercased() == address }) { return .failure(.known) }
        return .success(address)
    }
}

/// One row of Settings ▸ Connection ▸ Addresses: the address, whether it
/// is the preferred one (first in the race), and what the link says about
/// it now (docs/dev/design/ios-state-architecture.md 3.3 and 3.4).
public struct AddressRow: Equatable, Identifiable, Sendable {
    public enum Status: Equatable, Sendable {
        /// The link is online here; the interface says why ("on Wi-Fi").
        case inUse(NetworkInterface?)
        /// A race is trying it now.
        case checking
        /// It answered with a key that matches no pin: not this computer.
        case anotherKey
    }

    public let address: String
    public let isPreferred: Bool
    public let status: Status?

    public var id: String { address }

    public init(address: String, isPreferred: Bool, status: Status?) {
        self.address = address
        self.isPreferred = isPreferred
        self.status = status
    }

    /// The rows in the computer's order, the preferred address first.
    public static func rows(addresses: [String], preferred: String, link: ComputerLink) -> [AddressRow] {
        var ordered = addresses
        if let index = ordered.firstIndex(of: preferred) {
            ordered.insert(ordered.remove(at: index), at: 0)
        }
        let checking = Set(link.race?.inFlight ?? [])
        var untrusted = link.mismatched
        if case let .untrustedAnswer(addresses) = link.state { untrusted.formUnion(addresses) }
        return ordered.map { address in
            let status: Status?
            if link.onlineAddress == address {
                status = .inUse(link.path.interface)
            } else if checking.contains(address) {
                status = .checking
            } else if untrusted.contains(address) {
                status = .anotherKey
            } else {
                status = nil
            }
            return AddressRow(address: address, isPreferred: address == preferred, status: status)
        }
    }

    /// The status in a few words, nil when there is nothing to say.
    public var statusText: String? {
        switch status {
        case let .inUse(interface):
            switch interface {
            case .wifi: return "In use on Wi-Fi"
            case .cellular: return "In use on cellular"
            case .wired: return "In use on a wired network"
            case .other, nil: return "In use"
            }
        case .checking: return "Checking…"
        case .anotherKey: return "Answered with another key"
        case nil: return nil
        }
    }

    public var isInUse: Bool {
        if case .inUse = status { return true }
        return false
    }
}
