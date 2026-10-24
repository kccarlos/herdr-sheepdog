import Foundation
import Security

/// Why the app must scan the pairing code again (docs/dev/design/security.md
/// "When the app must scan again"), each with what the app says.
public enum PairingProblem: Error, Equatable, Sendable {
    /// Every address offered a key that matches no pin.
    case pinMismatch
    /// The pin stopped matching after the old certificate's expiry date.
    case renewedWhileAway
    /// `401 device_revoked`: removed on the computer.
    case revoked
    /// `401 unknown_device`: the computer lost its devices list.
    case unknownDevice
    /// `410`: the code was used or has expired.
    case codeUsed
    /// No key in the keychain for this computer (a restored phone).
    case noKey
    /// A plain-HTTP address that is not this device: the app talks only to
    /// a paired computer, over pinned TLS (`DaemonEndpoint.allowsConnection`).
    case notPaired

    public var title: String {
        switch self {
        case .codeUsed: return "This code has expired or was used"
        default: return "Please scan again"
        }
    }

    public var message: String {
        switch self {
        case .pinMismatch:
            return "This computer's security key changed. That happens when sheepdog is reinstalled or pairing is reset. If you did not do that, someone may be intercepting the connection: do not continue on this network."
        case .renewedWhileAway:
            return "This computer renewed its security certificate while this phone was away. Scan the pairing code again."
        case .revoked:
            return "This phone was removed on your computer. Scan the pairing code again to reconnect."
        case .unknownDevice:
            return "Your computer no longer knows this phone. Scan the pairing code again."
        case .codeUsed:
            return "Show a new code on your computer and scan it. Each code works once, for 10 minutes."
        case .noKey:
            return "This is a new phone to your computer. Scan the pairing code again."
        case .notPaired:
            return "This address isn't paired. Open sheepdog's setup page on your computer and scan its pairing code."
        }
    }

    /// A key that matches no pin: "renewed while away" once the computer
    /// may have switched to its next key (from 7 days before the served
    /// certificate expires, docs/dev/design/security.md "Certificate
    /// rotation"), else the interception warning.
    public static func forPinMismatch(certExpires: Date?, now: Date = Date()) -> PairingProblem {
        guard let certExpires, now >= certExpires.addingTimeInterval(-certificateSwitchLead) else { return .pinMismatch }
        return .renewedWhileAway
    }

    /// How long before its expiry the daemon serves its next key.
    public static let certificateSwitchLead: TimeInterval = 7 * 24 * 60 * 60

    /// The problem a `401` reason names, nil for one signing again fixes.
    public init?(reason: String) {
        switch reason {
        case "device_revoked": self = .revoked
        case "unknown_device": self = .unknownDevice
        default: return nil
        }
    }
}

/// A session ticket (`Authorization: Bearer sds_<ticket>`).
public struct SessionTicket: Equatable, Sendable {
    public var ticket: String
    public var expires: Date
    /// The computer's next pin, during a certificate rotation.
    public var nextPin: String?
    /// When the served certificate expires (`cert_expires`).
    public var certExpires: Date?

    public var header: String { "Bearer sds_\(ticket)" }

    public init(ticket: String, expires: Date, nextPin: String? = nil, certExpires: Date? = nil) {
        self.ticket = ticket
        self.expires = expires
        self.nextPin = nextPin
        self.certExpires = certExpires
    }
}

/// Enrolling and signing in against one computer, over a pinned session.
public struct DeviceAuth: Sendable {
    public var base: URL
    public var session: URLSession
    /// The session's pin check, asked per request so that a key that
    /// matches no pin is told apart from any other failure
    /// (`DaemonError.untrusted`). Nil for a session without pinning (the
    /// tests' plain fakes).
    public var trust: PinnedTrustDelegate?

    public init(base: URL, session: URLSession, trust: PinnedTrustDelegate? = nil) {
        self.base = base
        self.session = session
        self.trust = trust
    }

    public struct Enrolled: Decodable, Equatable, Sendable {
        public var deviceID: String
        public var daemonID: String
        public var name: String

        enum CodingKeys: String, CodingKey {
            case deviceID = "device_id", daemonID = "daemon_id", name
        }
    }

    /// `POST /api/devices/enroll`. Throws `PairingProblem.codeUsed` on 410.
    public func enroll(code: String, publicKey: String, name: String, kind: String, hardware: Bool) async throws -> Enrolled {
        let body = try JSONSerialization.data(withJSONObject: ["code": code, "public_key": publicKey, "name": name, "kind": kind, "hardware": hardware])
        let (data, status) = try await send("/api/devices/enroll", method: "POST", body: body)
        if status == 410 { throw PairingProblem.codeUsed }
        guard status == 200 else { throw DaemonError.rejected(status: status, message: Self.errorText(data)) }
        return try JSONDecoder().decode(Enrolled.self, from: data)
    }

    /// Signs a challenge with key and returns a ticket. A refusal that
    /// needs a new scan throws its `PairingProblem`.
    public func signIn(daemonID: String, pin: String, deviceID: String, key: SecKey, now: Date = Date()) async throws -> SessionTicket {
        try await signIn(daemonID: daemonID, pins: { [pin] }, deviceID: deviceID, key: key, now: now)
    }

    /// Signs in with the first of `pins`, read after the challenge's TLS
    /// handshake (which may have promoted the next pin, `PinnedTrustDelegate`
    /// `onMatch`). On `bad_signature` it signs once more with the other
    /// trusted pin, on a new challenge: a connection kept from before the
    /// computer switched keys can sign over the wrong one.
    public func signIn(daemonID: String, pins: @Sendable () -> [String], deviceID: String, key: SecKey, now: Date = Date()) async throws -> SessionTicket {
        var tried: [String] = []
        while true {
            let (chData, chStatus) = try await send("/api/auth/challenge", method: "GET", body: nil)
            guard chStatus == 200,
                  let ch = try JSONSerialization.jsonObject(with: chData) as? [String: Any],
                  let nonce = ch["nonce"] as? String
            else { throw DaemonError.malformed("the sign-in challenge") }
            guard let pin = pins().first(where: { !$0.isEmpty && !tried.contains($0) }) else {
                throw PairingProblem.pinMismatch
            }
            tried.append(pin)
            let unix = Int64(now.timeIntervalSince1970)
            let sig = try DeviceKey.sign(key, SignedText.session(daemonID: daemonID, pin: pin, deviceID: deviceID, nonce: nonce, unix: unix))
            let body = try JSONSerialization.data(withJSONObject: ["device_id": deviceID, "nonce": nonce, "time": unix, "signature": sig.base64EncodedString(), "client": "app"] as [String: Any])
            let (data, status) = try await send("/api/auth/session", method: "POST", body: body)
            let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
            guard status == 200, let ticket = obj["ticket"] as? String else {
                let reason = obj["reason"] as? String ?? ""
                if let problem = PairingProblem(reason: reason) { throw problem }
                if reason == "bad_signature", pins().contains(where: { !$0.isEmpty && !tried.contains($0) }) { continue }
                throw DaemonError.rejected(status: status, message: obj["error"] as? String ?? "")
            }
            let life = obj["expires_in"] as? Double ?? 43_200
            let certExpires = (obj["cert_expires"] as? String).flatMap { ISO8601DateFormatter().date(from: $0) }
            return SessionTicket(ticket: ticket, expires: now.addingTimeInterval(life), nextPin: obj["next_pin"] as? String, certExpires: certExpires)
        }
    }

    /// `DELETE /api/devices/self`, best effort (Remove in Settings).
    public func unpair(ticket: SessionTicket) async {
        var request = URLRequest(url: base.appendingPathComponent("api/devices/self"))
        request.httpMethod = "DELETE"
        request.timeoutInterval = 4
        request.setValue(ticket.header, forHTTPHeaderField: "Authorization")
        _ = try? await session.data(for: request)
    }

    private func send(_ path: String, method: String, body: Data?) async throws -> (Data, Int) {
        var request = URLRequest(url: base.appendingPathComponent(String(path.dropFirst())))
        request.httpMethod = method
        request.httpBody = body
        if body != nil { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        let attempt = trust?.attempt()
        do {
            let (data, response) = try await session.data(for: request, delegate: attempt)
            return (data, (response as? HTTPURLResponse)?.statusCode ?? 0)
        } catch is URLError where attempt?.sawMismatch == true {
            let port = base.port.map { ":\($0)" } ?? ""
            throw DaemonError.untrusted(endpoint: "\(base.host ?? "")\(port)")
        }
    }

    private static func errorText(_ data: Data) -> String {
        ((try? JSONSerialization.jsonObject(with: data)) as? [String: Any])?["error"] as? String ?? ""
    }
}
