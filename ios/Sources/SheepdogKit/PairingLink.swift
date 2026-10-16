import Foundation

/// The pairing code `sheepdog pair` prints (and the daemon's /debug
/// shows): `sheepdog://pair?u=<base URL>&u=<base URL>…&t=<token>`
/// (docs/ws-contract.md "Pairing" in the daemon repository). The base
/// URLs come LAN first, then Tailscale; `t` is `web.token`, absent when
/// the daemon has none.
public struct PairingLink: Equatable, Sendable {
    /// The daemon's addresses as Settings stores them (`host:port`, no
    /// scheme), in the code's order.
    public var addresses: [String]
    public var token: String

    public init(addresses: [String], token: String) {
        self.addresses = addresses
        self.token = token
    }

    /// Reads a scanned code; nil when it is not a pairing link or holds no
    /// usable address.
    public init?(_ text: String) {
        guard let components = URLComponents(string: text.trimmingCharacters(in: .whitespacesAndNewlines)),
              components.scheme?.lowercased() == AppRoute.scheme,
              components.host?.lowercased() == "pair"
        else { return nil }
        var addresses: [String] = []
        var token = ""
        // Form encoding, as the daemon writes it (Go's url.Values): "+" is a
        // space, which URLComponents' own decoding leaves alone.
        func decoded(_ raw: String?) -> String? {
            raw.map { $0.replacingOccurrences(of: "+", with: " ") }?.removingPercentEncoding
        }
        for item in components.percentEncodedQueryItems ?? [] {
            switch decoded(item.name) {
            case "u":
                guard let value = decoded(item.value), let endpoint = DaemonEndpoint(text: value) else { continue }
                let lower = value.lowercased()
                // Plain HTTP only, like the rest of the app.
                guard lower.hasPrefix("http://") || !lower.contains("://") else { continue }
                let address = Self.address(of: endpoint)
                if !addresses.contains(address) { addresses.append(address) }
            case "t":
                token = decoded(item.value) ?? ""
            default:
                continue
            }
        }
        guard !addresses.isEmpty else { return nil }
        self.init(addresses: addresses, token: token)
    }

    /// A `sheepdog://pair?…` link opened from outside the app (the system
    /// Camera app scanning the code); nil for any other link.
    public init?(url: URL) {
        self.init(url.absoluteString)
    }

    /// What the app asks before it applies a link that arrived from outside
    /// (the Camera app): nothing is contacted and nothing is saved until the
    /// user says yes, because the link carries a token and replaces the
    /// daemon the app talks to.
    public struct Confirmation: Equatable, Sendable {
        public var title: String
        public var message: String
    }

    public func confirmation(currentAddress: String) -> Confirmation {
        let first = addresses[0]
        let host = first.hasPrefix("[") ? String(first.dropFirst().prefix { $0 != "]" }) : String(first.split(separator: ":").first ?? Substring(first))
        var lines: [String] = []
        if addresses.count > 1 {
            lines.append("The code lists \(addresses.count) addresses; the app keeps the first that answers: \(addresses.joined(separator: ", ")).")
        } else {
            lines.append("Address: \(first).")
        }
        let current = currentAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        if !current.isEmpty, !addresses.contains(current) {
            lines.append("This replaces the daemon at \(current).")
        }
        lines.append(token.isEmpty ? "The code has no token." : "The token in the code is saved in the keychain.")
        lines.append("Only pair with a computer you trust.")
        return Confirmation(title: "Pair with \(host)?", message: lines.joined(separator: " "))
    }

    /// `host:port` as Settings shows it (an IPv6 literal in brackets).
    static func address(of endpoint: DaemonEndpoint) -> String {
        let host = endpoint.host.contains(":") ? "[\(endpoint.host)]" : endpoint.host
        return "\(host):\(endpoint.port)"
    }

    /// The first address, in the code's order, that answers `probe` within
    /// `timeout`. All are tried at once, so an unreachable LAN address away
    /// from home costs one timeout, not one per address. Nil when none
    /// answers.
    public func firstReachable(
        timeout: Duration = .seconds(3),
        probe: @escaping @Sendable (String) async -> Bool
    ) async -> String? {
        let ordered = addresses
        let answered = await withTaskGroup(of: (Int, Bool).self) { group -> Set<Int> in
            for (index, address) in ordered.enumerated() {
                group.addTask {
                    (index, await Self.withTimeout(timeout) { await probe(address) } ?? false)
                }
            }
            var ok = Set<Int>()
            for await (index, reached) in group where reached {
                ok.insert(index)
            }
            return ok
        }
        guard let best = answered.min() else { return nil }
        return ordered[best]
    }

    /// The result of `body`, or nil when it takes longer than `timeout`.
    static func withTimeout<T: Sendable>(_ timeout: Duration, _ body: @escaping @Sendable () async -> T) async -> T? {
        await withTaskGroup(of: T?.self) { group in
            group.addTask { await body() }
            group.addTask {
                try? await Task.sleep(for: timeout)
                return nil
            }
            let first = await group.next() ?? nil
            group.cancelAll()
            return first
        }
    }
}
