import Foundation

/// The pairing code the daemon shows (docs/dev/ws-contract.md "Pairing"):
///
///     https://100.101.102.103:7777/voice#pair=1&d=<daemon id>&n=<name>&k=<pin>&e=<code>&u=<host:port>&u=…
///
/// A camera opens it in the browser; the app's scanner reads the same link
/// (or the `sheepdog://pair#…` hand-off the voice page offers on iPhone).
/// Everything is in the fragment, form-encoded.
public struct PairingCode: Equatable, Sendable {
    /// The daemon id: the computer the app keys everything by.
    public var daemonID: String
    /// The computer's name (the daemon's `computer.name`).
    public var name: String
    /// The TLS key's pin, base64url(SHA-256(SubjectPublicKeyInfo)).
    public var pin: String
    /// The one-time enroll code.
    public var code: String
    /// `host:port` addresses in the code's order (Tailscale, LAN, advertised).
    public var addresses: [String]

    public init(daemonID: String, name: String, pin: String, code: String, addresses: [String]) {
        self.daemonID = daemonID
        self.name = name
        self.pin = pin
        self.code = code
        self.addresses = addresses
    }

    /// Reads a scanned code; nil when it is not a pairing code.
    public init?(_ text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let hash = trimmed.firstIndex(of: "#") else { return nil }
        let head = trimmed[trimmed.startIndex ..< hash].lowercased()
        guard head.hasPrefix("https://") || head.hasPrefix("sheepdog://pair") else { return nil }
        var fields: [String: [String]] = [:]
        for part in trimmed[trimmed.index(after: hash)...].split(separator: "&") {
            let kv = part.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard let key = Self.decode(kv[0]) else { continue }
            let value = kv.count > 1 ? Self.decode(kv[1]) ?? "" : ""
            fields[key, default: []].append(value)
        }
        guard fields["pair"]?.first == "1",
              let d = fields["d"]?.first, !d.isEmpty,
              let k = fields["k"]?.first, !k.isEmpty,
              let e = fields["e"]?.first, !e.isEmpty
        else { return nil }
        var addresses: [String] = []
        for u in fields["u"] ?? [] where !u.isEmpty && !u.contains("/") && !addresses.contains(u) {
            addresses.append(u)
        }
        guard !addresses.isEmpty else { return nil }
        self.init(daemonID: d, name: fields["n"]?.first ?? "", pin: k, code: e, addresses: addresses)
    }

    /// Form decoding, as the daemon writes it (Go's url.Values): "+" is a
    /// space.
    private static func decode(_ raw: Substring) -> String? {
        String(raw).replacingOccurrences(of: "+", with: " ").removingPercentEncoding
    }
}
