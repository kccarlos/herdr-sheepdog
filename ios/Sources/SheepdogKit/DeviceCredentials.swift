import Foundation
import Security

/// The computer this phone is paired with (docs/dev/design/security.md
/// "Key storage"): what the pairing code said, plus the device id the
/// computer gave this phone. Kept in the keychain as one record; the key
/// itself is `DeviceKey` (tagged with the daemon id).
public struct PairedComputer: Codable, Equatable, Sendable {
    public var daemonID: String
    public var name: String
    /// `host:port` addresses from the code, in its order.
    public var addresses: [String]
    /// The TLS key's pin, and the next one during a certificate renewal.
    public var pin: String
    public var nextPin: String?
    public var deviceID: String
    public var pairedAt: Date

    public init(daemonID: String, name: String, addresses: [String], pin: String, nextPin: String? = nil, deviceID: String, pairedAt: Date) {
        self.daemonID = daemonID
        self.name = name
        self.addresses = addresses
        self.pin = pin
        self.nextPin = nextPin
        self.deviceID = deviceID
        self.pairedAt = pairedAt
    }

    /// The pin as Settings shows it: its first 8 characters.
    public var shortPin: String { String(pin.prefix(8)) }
}

/// The one place the app keeps what it needs to talk to a paired computer:
/// the record, the pins every `URLSession` checks, and the session ticket
/// every request carries. Shared by the whole process (the view model, push
/// registration, notification actions, App Intents), so a ticket is signed
/// for once and parallel refusals never each sign in again.
public final class DeviceCredentials: @unchecked Sendable {
    public static let shared = DeviceCredentials(secrets: KeychainSecretStore(accessibleAfterFirstUnlock: true))

    /// Refresh a ticket this long before it expires.
    static let refreshMargin: TimeInterval = 30 * 60
    private static let account = "daemon.computer"

    private let secrets: SecretStore
    private let lock = NSLock()
    private var loaded = false
    private var cached: PairedComputer?
    private var ticket: SessionTicket?
    private var signing: Task<SessionTicket, Error>?

    public init(secrets: SecretStore) {
        self.secrets = secrets
    }

    /// The paired computer, nil before the first pairing (or after Remove).
    /// Setting it drops the ticket.
    public var computer: PairedComputer? {
        get {
            lock.lock()
            defer { lock.unlock() }
            if !loaded {
                loaded = true
                cached = secrets.secret(for: Self.account).flatMap { try? JSONDecoder().decode(PairedComputer.self, from: Data($0.utf8)) }
            }
            return cached
        }
        set {
            let text = newValue.flatMap { try? JSONEncoder().encode($0) }.flatMap { String(data: $0, encoding: .utf8) }
            secrets.setSecret(text, for: Self.account)
            lock.lock()
            loaded = true
            cached = newValue
            ticket = nil
            signing?.cancel()
            signing = nil
            lock.unlock()
        }
    }

    /// The pins a server's key may match: the paired computer's current and
    /// next one. Read at every TLS handshake.
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

    /// A ticket for the paired computer, signing in at `base` when there is
    /// none or it is about to expire. Concurrent callers share one sign-in.
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
                let auth = DeviceAuth(base: base, session: DeviceCredentials.session(timeout: 8))
                return try await auth.signIn(daemonID: computer.daemonID, pin: computer.pin, deviceID: computer.deviceID, key: key)
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
            if let next = fresh.nextPin, next != computer.nextPin, var updated = self.computer, updated.daemonID == computer.daemonID {
                updated.nextPin = next
                let ticketKept = fresh
                self.computer = updated
                lock.lock()
                ticket = ticketKept
                lock.unlock()
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
        if let computer { DeviceKey.delete(daemonID: computer.daemonID) }
        self.computer = nil
    }

    // MARK: - Pinned sessions

    /// The delegate behind every app session: a server must offer a key
    /// that matches one of the paired computer's pins. Plain HTTP to
    /// loopback (the fixture daemons) never reaches it.
    public static let trust = PinnedTrustDelegate { DeviceCredentials.shared.pins }

    /// A pinned session with the given configuration.
    public static func session(configuration: URLSessionConfiguration) -> URLSession {
        URLSession(configuration: configuration, delegate: trust, delegateQueue: nil)
    }

    /// A pinned ephemeral session with a request timeout.
    public static func session(timeout: TimeInterval) -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = timeout
        config.waitsForConnectivity = false
        return session(configuration: config)
    }
}
