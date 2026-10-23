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
