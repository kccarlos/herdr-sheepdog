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

/// Validates a daemon's enrollment reply against the scanned `PairingCode`,
/// serialises enrollments per daemon, and tracks key adoption so a failed or
/// ambiguous attempt never deletes a key in use.
public enum PairingEnrollment {
    /// A per-daemon enrollment turn. Acquire with `beginTurn(daemonID:)` and
    /// release synchronously with `end()` so concurrent attempts for the same
    /// daemon run one at a time without blocking other daemons.
    public final class Turn: @unchecked Sendable {
        private let gate: DaemonGate
        private let daemonID: String
        private let lock = NSLock()
        private var ended = false

        fileprivate init(gate: DaemonGate, daemonID: String) {
            self.gate = gate
            self.daemonID = daemonID
        }

        public func end() {
            let shouldEnd = lock.withLock { () -> Bool in
                guard !ended else { return false }
                ended = true
                return true
            }
            if shouldEnd {
                gate.release(daemonID)
            }
        }
    }

    fileprivate final class DaemonGate: @unchecked Sendable {
        private struct Queue {
            var busy = false
            var waiters: [CheckedContinuation<Void, Never>] = []
        }

        private let lock = NSLock()
        private var queues: [String: Queue] = [:]

        func acquire(_ daemonID: String) async -> Turn {
            let key = daemonID.trimmed
            await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
                let grantNow = lock.withLock { () -> Bool in
                    var queue = queues[key] ?? Queue()
                    if !queue.busy {
                        queue.busy = true
                        queues[key] = queue
                        return true
                    }
                    queue.waiters.append(continuation)
                    queues[key] = queue
                    return false
                }
                if grantNow {
                    continuation.resume()
                }
            }
            return Turn(gate: self, daemonID: key)
        }

        func release(_ key: String) {
            let next = lock.withLock { () -> CheckedContinuation<Void, Never>? in
                guard var queue = queues[key] else { return nil }
                if !queue.waiters.isEmpty {
                    let waiter = queue.waiters.removeFirst()
                    queues[key] = queue
                    return waiter
                }
                queues.removeValue(forKey: key)
                return nil
            }
            next?.resume()
        }
    }

    private static let gate = DaemonGate()

    public static func beginTurn(daemonID: String) async -> Turn {
        await gate.acquire(daemonID)
    }

    /// Tracks ownership and adoption of a daemon's `DeviceKey` across pairing
    /// attempts. A key is deleted on failure only when this attempt created it,
    /// no other attempt loaded or committed it, the computer is not paired, the
    /// failure is a definite non-enrollment, and the keychain still holds that
    /// exact key.
    public final class KeyLease: @unchecked Sendable {
        public let daemonID: String
        public let key: SecKey
        public let publicKey: String?
        public let createdNew: Bool

        private let lock = NSLock()
        private var committed = false
        private var released = false

        fileprivate init(daemonID: String, key: SecKey, publicKey: String?, createdNew: Bool) {
            self.daemonID = daemonID
            self.key = key
            self.publicKey = publicKey
            self.createdNew = createdNew
        }

        public static func acquire(daemonID: String) throws -> KeyLease {
            try KeyRegistry.shared.acquire(daemonID: daemonID)
        }

        public func markCommitted() {
            lock.withLock { committed = true }
            KeyRegistry.shared.markCommitted(daemonID: daemonID, publicKey: publicKey)
        }

        public func release(definiteNonEnrollment: Bool, isComputerPaired: Bool) {
            let state = lock.withLock { () -> (shouldRelease: Bool, wasCommitted: Bool) in
                guard !released else { return (false, committed) }
                released = true
                return (true, committed)
            }
            guard state.shouldRelease else { return }
            KeyRegistry.shared.release(
                lease: self,
                wasCommitted: state.wasCommitted,
                definiteNonEnrollment: definiteNonEnrollment,
                isComputerPaired: isComputerPaired
            )
        }
    }

    private final class KeyRegistry: @unchecked Sendable {
        static let shared = KeyRegistry()

        private struct Entry {
            var publicKey: String?
            var activeCount: Int
            var adoptedByAnother: Bool
            var committed: Bool
        }

        private let lock = NSLock()
        private var entries: [String: Entry] = [:]

        func acquire(daemonID: String) throws -> KeyLease {
            let id = daemonID.trimmed
            return try lock.withLock {
                if let existing = DeviceKey.load(daemonID: id) {
                    let pub = DeviceKey.publicKey(existing)
                    if var entry = entries[id], entry.publicKey == pub {
                        entry.activeCount += 1
                        entry.adoptedByAnother = true
                        entries[id] = entry
                    } else {
                        entries[id] = Entry(publicKey: pub, activeCount: 1, adoptedByAnother: true, committed: false)
                    }
                    return KeyLease(daemonID: id, key: existing, publicKey: pub, createdNew: false)
                }
                let created = try DeviceKey.loadOrCreate(daemonID: id)
                let pub = DeviceKey.publicKey(created)
                entries[id] = Entry(publicKey: pub, activeCount: 1, adoptedByAnother: false, committed: false)
                return KeyLease(daemonID: id, key: created, publicKey: pub, createdNew: true)
            }
        }

        func markCommitted(daemonID: String, publicKey: String?) {
            let id = daemonID.trimmed
            lock.withLock {
                if var entry = entries[id], entry.publicKey == publicKey {
                    entry.committed = true
                    entries[id] = entry
                }
            }
        }

        func release(
            lease: KeyLease,
            wasCommitted: Bool,
            definiteNonEnrollment: Bool,
            isComputerPaired: Bool
        ) {
            let id = lease.daemonID
            lock.withLock {
                let canDeleteFromEntry: Bool
                if var entry = entries[id], entry.publicKey == lease.publicKey {
                    entry.activeCount = max(0, entry.activeCount - 1)
                    canDeleteFromEntry = !entry.adoptedByAnother && !entry.committed && entry.activeCount == 0
                    if entry.activeCount == 0 {
                        entries.removeValue(forKey: id)
                    } else {
                        entries[id] = entry
                    }
                } else {
                    canDeleteFromEntry = false
                }

                guard lease.createdNew,
                      !wasCommitted,
                      definiteNonEnrollment,
                      !isComputerPaired,
                      canDeleteFromEntry
                else { return }

                if let expectedPub = lease.publicKey {
                    guard let current = DeviceKey.load(daemonID: id),
                          DeviceKey.publicKey(current) == expectedPub
                    else { return }
                }
                DeviceKey.delete(daemonID: id)
            }
        }
    }

    /// True only when an error thrown during `DeviceAuth.enroll` proves the
    /// daemon did not enroll the key (TLS pin mismatch, HTTP 410 code used, or
    /// another HTTP 4xx rejection). Transport failures, cancellations, HTTP 5xx
    /// responses, and malformed HTTP 200 bodies are ambiguous and keep the key.
    public static func isDefiniteNonEnrollment(_ error: Error) -> Bool {
        switch error {
        case is PairingProblem:
            return true
        case DaemonError.untrusted, DaemonError.pairing, DaemonError.unauthorized:
            return true
        case let DaemonError.rejected(status, _), let DaemonError.http(status, _):
            return (400 ..< 500).contains(status)
        default:
            return false
        }
    }

    public static func pairedComputer(
        code: PairingCode,
        reachableAddress: String,
        enrolled: DeviceAuth.Enrolled,
        pairedAt: Date = Date()
    ) throws -> PairedComputer {
        let daemonID = code.daemonID.trimmed
        guard !daemonID.isEmpty else {
            throw DaemonError.malformed("pairing code missing daemon id")
        }
        let replyDaemonID = enrolled.daemonID.trimmed
        guard replyDaemonID.isEmpty || replyDaemonID == daemonID else {
            throw DaemonError.malformed("the computer answered with a different id than its pairing code")
        }
        let deviceID = enrolled.deviceID.trimmed
        guard !deviceID.isEmpty else {
            throw DaemonError.malformed("the computer did not return a device id")
        }
        return PairedComputer(
            daemonID: daemonID,
            name: code.name.isEmpty ? enrolled.name : code.name,
            addresses: [reachableAddress] + code.addresses.filter { $0 != reachableAddress },
            pin: code.pin,
            deviceID: deviceID,
            pairedAt: pairedAt
        )
    }
}
