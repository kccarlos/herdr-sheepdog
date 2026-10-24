import Foundation
import Security

/// A computer this phone is paired with (docs/dev/design/security.md "Key
/// storage"): what the pairing code said, plus the device id the computer
/// gave this phone. Kept in the keychain as one record per computer
/// (`ComputerStore`); the key itself is `DeviceKey` (tagged with the daemon
/// id).
public struct PairedComputer: Codable, Equatable, Sendable {
    public var daemonID: String
    public var name: String
    /// `host:port` addresses from the code, in its order.
    public var addresses: [String]
    /// The TLS key's pin, and the next one during a certificate renewal.
    /// Empty only for a loopback fixture in the tests (`isLoopbackFixture`).
    public var pin: String
    public var nextPin: String?
    public var deviceID: String
    public var pairedAt: Date
    /// When the certificate behind `pin` expires (the session's
    /// `cert_expires`), nil until the first sign-in or right after a
    /// switch: it tells a missed rotation from an interception
    /// (`PairingProblem.forPinMismatch`).
    public var certExpires: Date?
    /// The name the user gave it on this phone (Settings ▸ Computers), nil
    /// for the daemon's own. Never sent to the computer.
    public var localName: String?

    public init(daemonID: String, name: String, addresses: [String], pin: String, nextPin: String? = nil, deviceID: String,
                pairedAt: Date, certExpires: Date? = nil, localName: String? = nil) {
        self.daemonID = daemonID
        self.name = name
        self.addresses = addresses
        self.pin = pin
        self.nextPin = nextPin
        self.deviceID = deviceID
        self.pairedAt = pairedAt
        self.certExpires = certExpires
        self.localName = localName
    }

    /// The pin as Settings shows it: its first 8 characters.
    public var shortPin: String { String(pin.prefix(8)) }

    /// What the app calls it: the local name, else the daemon's, else a
    /// plain stand-in.
    public var displayName: String {
        for candidate in [localName, name] {
            if let s = candidate?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty { return s }
        }
        return "Your computer"
    }

    /// Plain HTTP on loopback with no pin: a fixture daemon in the tests
    /// (`-sheepdogComputers`). Pairing never makes one.
    public var isLoopbackFixture: Bool { pin.isEmpty }
}

/// The one place the app keeps what it needs to talk to one paired
/// computer: the record, the pins its `URLSession`s check, and the session
/// ticket its requests carry. `ComputerStore` hands out one instance per
/// computer, shared by the whole process (the view model, push
/// registration, notification actions, App Intents), so a ticket is signed
/// for once and parallel refusals never each sign in again. Every
/// `DaemonEndpoint` to a paired computer names its instance.
public final class DeviceCredentials: @unchecked Sendable {
    /// Refresh a ticket this long before it expires.
    static let refreshMargin: TimeInterval = 30 * 60

    /// The keychain account of one computer's record.
    public static func account(for daemonID: String) -> String { "daemon.computer.\(daemonID)" }

    /// The computer this instance is for.
    public let daemonID: String
    private let account: String
    private let secrets: SecretStore
    private let lock = NSLock()
    private let updating = NSLock()
    private var loaded = false
    private var cached: PairedComputer?
    private var ticket: SessionTicket?
    private var signing: Task<SessionTicket, Error>?

    public init(daemonID: String, secrets: SecretStore) {
        self.daemonID = daemonID
        self.account = Self.account(for: daemonID)
        self.secrets = secrets
        let me = WeakCredentials()
        trust = PinnedTrustDelegate(pins: { me.value?.pins ?? [] }, onMatch: { me.value?.served(pin: $0) })
        me.value = self
    }

    /// The computer's record, nil before it is paired (or after Remove).
    /// Setting it drops the ticket.
    public var computer: PairedComputer? {
        get {
            lock.lock()
            defer { lock.unlock() }
            if !loaded {
                loaded = true
                cached = secrets.secret(for: account).flatMap { try? JSONDecoder().decode(PairedComputer.self, from: Data($0.utf8)) }
            }
            return cached
        }
        set {
            let text = newValue.flatMap { try? JSONEncoder().encode($0) }.flatMap { String(data: $0, encoding: .utf8) }
            secrets.setSecret(text, for: account)
            lock.lock()
            loaded = true
            cached = newValue
            ticket = nil
            signing?.cancel()
            signing = nil
            lock.unlock()
        }
    }

    /// Saves an updated record of the same computer without dropping the
    /// ticket (a new next pin, the certificate's expiry, a switch, a local
    /// name). It never cancels a sign-in in flight: the TLS delegate calls
    /// it in the middle of one (`served(pin:)`).
    public func update(_ change: (inout PairedComputer) -> Void) {
        updating.lock()
        defer { updating.unlock() }
        guard var updated = computer else { return }
        let before = updated
        change(&updated)
        guard updated != before else { return }
        let text = (try? JSONEncoder().encode(updated)).flatMap { String(data: $0, encoding: .utf8) }
        secrets.setSecret(text, for: account)
        lock.lock()
        loaded = true
        cached = updated
        lock.unlock()
    }

    /// The pin a TLS handshake matched (`PinnedTrustDelegate` `onMatch`).
    /// When it is the next pin, the computer has switched keys: it becomes
    /// the pin and the old one is dropped. The ticket stays valid.
    public func served(pin: String) {
        guard let computer, computer.nextPin == pin, computer.pin != pin else { return }
        update { c in
            guard c.nextPin == pin else { return }
            c.pin = pin
            c.nextPin = nil
            c.certExpires = nil // the new certificate's comes with the next sign-in
        }
    }

    /// Adds an address the user typed (Settings ▸ Connection ▸ Addresses),
    /// after it answered with the computer's key.
    public func addAddress(_ address: String) {
        update { c in
            if !c.addresses.contains(address) { c.addresses.append(address) }
        }
    }

    /// Forgets one address; the last one stays.
    public func removeAddress(_ address: String) {
        update { c in
            guard c.addresses.count > 1 else { return }
            c.addresses.removeAll { $0 == address }
        }
    }

    /// The pins a server's key may match: the computer's current and next
    /// one. Read at every TLS handshake.
    public var pins: [String] {
        guard let computer else { return [] }
        return [computer.pin, computer.nextPin].compactMap { $0 }.filter { !$0.isEmpty }
    }

    /// The `Authorization` header of a ticket that has not expired, for
    /// requests built synchronously (the `/ws` and `/term` upgrades).
    public func header(now: Date = Date()) -> String? {
        lock.lock()
        defer { lock.unlock() }
        guard let ticket, ticket.expires > now else { return nil }
        return ticket.header
    }

    /// Uses a ticket signed elsewhere (the tests).
    public func adopt(_ ticket: SessionTicket) {
        lock.lock()
        self.ticket = ticket
        lock.unlock()
    }

    /// Forgets the ticket (a 401 `no_session` or `session_expired`).
    public func dropTicket() {
        lock.lock()
        ticket = nil
        lock.unlock()
    }

    /// A ticket for the computer, signing in at `base` when there is none
    /// or it is about to expire. Concurrent callers share one sign-in.
    public func ticket(base: URL, now: Date = Date()) async throws -> SessionTicket {
        guard let computer else { throw PairingProblem.noKey }
        let task: Task<SessionTicket, Error>
        lock.lock()
        if let ticket, ticket.expires.timeIntervalSince(now) > Self.refreshMargin {
            lock.unlock()
            return ticket
        }
        if let signing {
            task = signing
        } else {
            task = Task {
                guard let key = DeviceKey.load(daemonID: computer.daemonID) else { throw PairingProblem.noKey }
                let auth = DeviceAuth(base: base, session: self.session(timeout: 8), trust: self.trust)
                // Read after the challenge's handshake, which may have
                // switched to the next pin.
                return try await auth.signIn(daemonID: computer.daemonID, pins: { [weak self] in self?.pins ?? [computer.pin] },
                                             deviceID: computer.deviceID, key: key)
            }
            signing = task
        }
        lock.unlock()
        do {
            let fresh = try await task.value
            lock.lock()
            if signing == task { signing = nil }
            ticket = fresh
            lock.unlock()
            update { c in
                guard c.daemonID == computer.daemonID else { return }
                if let next = fresh.nextPin, next != c.pin { c.nextPin = next }
                if let expires = fresh.certExpires { c.certExpires = expires }
            }
            return fresh
        } catch {
            lock.lock()
            if signing == task { signing = nil }
            lock.unlock()
            throw error
        }
    }

    /// Forgets the computer and this phone's key for it (Remove).
    public func forget() {
        DeviceKey.delete(daemonID: daemonID)
        self.computer = nil
    }

    // MARK: - Pinned sessions

    /// The delegate behind this computer's sessions: a server must offer a
    /// key that matches one of its pins. Its own, so a mismatch on another
    /// computer never makes this one ask for a new scan. Plain HTTP to
    /// loopback (the fixture daemons) never reaches it.
    public let trust: PinnedTrustDelegate

    /// A pinned ephemeral session with a request timeout.
    public func session(timeout: TimeInterval) -> URLSession {
        URLSession(configuration: Self.ephemeral(timeout: timeout), delegate: trust, delegateQueue: nil)
    }

    /// An ephemeral configuration with a request timeout.
    public static func ephemeral(timeout: TimeInterval) -> URLSessionConfiguration {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = timeout
        config.waitsForConnectivity = false
        return config
    }
}

/// The trust delegate's way back to its credentials, without a cycle.
private final class WeakCredentials: @unchecked Sendable {
    weak var value: DeviceCredentials?
}
