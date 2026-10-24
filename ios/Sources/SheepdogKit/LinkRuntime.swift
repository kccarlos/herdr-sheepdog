import Foundation

/// Runs one computer's `ComputerLink`: it executes the link's effects
/// (probes with their 4 s timeout, timers with their tokens, the channel
/// set, the address in use, the remembered race winners) and feeds every
/// result back as a `LinkEvent` (docs/dev/design/ios-state-architecture.md
/// 3.2–3.4).
///
/// All I/O is behind two small protocols, `LinkTransport` (the pinned
/// `GET /api/state` probe) and `LinkScheduler` (the clock and its timers),
/// so the tests drive it with a fake network and a manual clock. The app
/// passes `DaemonProbeTransport` and `LinkClock`.
///
/// What the link does not own yet stays with its owner and is only
/// reported here: the voice's `/ws` socket and the terminal's `/term` keep
/// their own reconnects until phase 5, so the runtime passes the channel
/// set and `reopen` on to `onChannels` / `onReopen` and the caller acts on
/// the channels it owns (`/events`).
@MainActor
public final class LinkRuntime {
    /// The computer this runtime is for.
    public let computerID: String
    public private(set) var link: ComputerLink

    /// The link changed (state, last seen, mismatched addresses…).
    public var onChange: ((ComputerLink) -> Void)?
    /// The channels that may be open now; close any other.
    public var onChannels: ((Set<LinkChannel>) -> Void)?
    /// A lost channel was confirmed reachable: open it again.
    public var onReopen: ((LinkChannel) -> Void)?
    /// Every channel and request now goes to this address.
    public var onUseAddress: ((String) -> Void)?
    /// A probe's `/api/state` reply: the data, refreshed in the same
    /// request that proved the link.
    public var onState: ((DaemonState) -> Void)?
    /// A verified computer wants a fresh ticket.
    public var onRenewTicket: (() -> Void)?

    private let transport: LinkTransport
    private let scheduler: LinkScheduler
    private let winners: LinkWinnerStore?
    private var random: LinkRandom

    private struct ProbeKey: Hashable {
        var address: String
        var race: Int
    }

    private var probes: [ProbeKey: LinkHandle] = [:]
    private var timeouts: [ProbeKey: LinkHandle] = [:]
    private var timers: [LinkTimer.Kind: LinkHandle] = [:]
    /// Events that arrived while effects ran (a fake transport answers
    /// synchronously): applied in order once the current event is done.
    private var queue: [LinkEvent] = []
    private var applying = false
    private var stopped = false

    /// - Parameters:
    ///   - link: the link to run; its remembered winners come from
    ///     `winners` when one is given.
    ///   - random: the jitter's source; the tests pass a seeded one.
    public init(computerID: String, link: ComputerLink, transport: LinkTransport, scheduler: LinkScheduler,
                winners: LinkWinnerStore? = nil, random: LinkRandom = LinkRandom()) {
        self.computerID = computerID
        self.transport = transport
        self.scheduler = scheduler
        self.winners = winners
        self.random = random
        var link = link
        if let stored = winners?.load(computerID: computerID), !stored.isEmpty {
            link = ComputerLink(addresses: link.addresses, winners: stored, certExpires: link.certExpires, demand: link.demand, path: link.path)
        }
        self.link = link
    }

    /// Feeds one event to the link and runs what it says.
    public func send(_ event: LinkEvent) {
        guard !stopped else { return }
        queue.append(event)
        guard !applying else { return }
        applying = true
        defer { applying = false }
        while !queue.isEmpty, !stopped {
            let next = queue.removeFirst()
            let before = link
            let effects = link.apply(next, now: scheduler.now, using: &random)
            for effect in effects { run(effect) }
            if link != before { onChange?(link) }
        }
        queue.removeAll()
    }

    /// Cancels every probe and timer and ignores anything that still
    /// arrives: the computer was switched away from or removed.
    public func stop() {
        stopped = true
        for handle in probes.values { handle.cancel() }
        for handle in timeouts.values { handle.cancel() }
        for handle in timers.values { handle.cancel() }
        probes = [:]
        timeouts = [:]
        timers = [:]
        queue = []
    }

    /// Probes and timers in flight, for the tests.
    public var probesInFlight: [String] { probes.keys.map(\.address).sorted() }

    // MARK: - Effects

    private func run(_ effect: LinkEffect) {
        switch effect {
        case let .probe(address, race):
            startProbe(ProbeKey(address: address, race: race))
        case let .cancelProbe(address, race):
            finishProbe(ProbeKey(address: address, race: race))?.cancel()
        case let .setTimer(timer, date):
            timers[timer.kind]?.cancel()
            timers[timer.kind] = scheduler.schedule(at: date) { [weak self] in
                guard let self, !self.stopped else { return }
                self.timers[timer.kind] = nil
                self.send(.timerFired(timer))
            }
        case let .cancelTimer(kind):
            timers.removeValue(forKey: kind)?.cancel()
        case let .useAddress(address):
            onUseAddress?(address)
        case let .channels(channels):
            onChannels?(channels)
        case let .reopen(channel):
            onReopen?(channel)
        case let .rememberWinners(remembered):
            winners?.save(remembered, computerID: computerID)
        case .renewTicket:
            onRenewTicket?()
        }
    }

    private func startProbe(_ key: ProbeKey) {
        finishProbe(key)?.cancel()
        // The timeout first: a transport that answers synchronously
        // (the tests) finishes the probe inside `probe(address:)`.
        timeouts[key] = scheduler.schedule(at: scheduler.now.addingTimeInterval(ComputerLink.probeTimeout)) { [weak self] in
            guard let self, !self.stopped, let handle = self.finishProbe(key) else { return }
            handle.cancel()
            self.send(.probeFailed(address: key.address, race: key.race, error: .transport))
        }
        let handle = LinkHandle()
        probes[key] = handle
        let inner = transport.probe(address: key.address) { [weak self] result in
            guard let self, !self.stopped, self.probes[key] === handle else { return }
            self.finishProbe(key)
            switch result {
            case let .success(rtt, state):
                if let state { self.onState?(state) }
                self.send(.probeSucceeded(address: key.address, race: key.race, rtt: rtt))
            case let .failure(error):
                self.send(.probeFailed(address: key.address, race: key.race, error: error))
            }
        }
        // Cancelling the outer handle cancels the transport's.
        handle.onCancel = { inner.cancel() }
    }

    /// Forgets a probe and its timeout; returns its handle if it was still
    /// in flight.
    @discardableResult
    private func finishProbe(_ key: ProbeKey) -> LinkHandle? {
        timeouts.removeValue(forKey: key)?.cancel()
        return probes.removeValue(forKey: key)
    }
}

// MARK: - The I/O seams

/// Something that can be cancelled: a probe, a timer.
public final class LinkHandle: @unchecked Sendable {
    private let lock = NSLock()
    fileprivate(set) var onCancel: (() -> Void)?
    public private(set) var isCancelled = false

    public init(onCancel: (() -> Void)? = nil) {
        self.onCancel = onCancel
    }

    public func cancel() {
        lock.lock()
        let action = isCancelled ? nil : onCancel
        isCancelled = true
        onCancel = nil
        lock.unlock()
        action?()
    }
}

/// How one probe ended.
public enum LinkProbeResult: Equatable, Sendable {
    /// The pinned `GET /api/state` answered; `state` is its reply.
    case success(rtt: TimeInterval, state: DaemonState?)
    case failure(LinkError)
}

/// The probe: a pinned `GET /api/state` on one address. `done` runs once on
/// the main actor, unless the returned handle was cancelled first.
@MainActor
public protocol LinkTransport: AnyObject {
    func probe(address: String, done: @escaping @MainActor (LinkProbeResult) -> Void) -> LinkHandle
}

/// The clock: what time it is, and a callback at a moment.
@MainActor
public protocol LinkScheduler: AnyObject {
    /// Wall-clock time: `LinkPresentation` shows `lastSeen` and the
    /// outage's start as times of day.
    var now: Date { get }
    func schedule(at date: Date, _ fire: @escaping @MainActor () -> Void) -> LinkHandle
}

/// Where the race winners per interface are kept between launches.
public protocol LinkWinnerStore {
    func load(computerID: String) -> [NetworkInterface: String]
    func save(_ winners: [NetworkInterface: String], computerID: String)
}

/// The jitter's random source: the system's, or a seeded one for tests.
public struct LinkRandom: RandomNumberGenerator {
    private var state: UInt64
    private let seeded: Bool

    /// The system's generator.
    public init() {
        state = 0
        seeded = false
    }

    /// A deterministic generator (SplitMix64).
    public init(seed: UInt64) {
        state = seed
        seeded = true
    }

    public mutating func next() -> UInt64 {
        guard seeded else {
            var system = SystemRandomNumberGenerator()
            return system.next()
        }
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}

// MARK: - The app's implementations

/// The real clock: `Date()` and main-actor tasks that sleep.
@MainActor
public final class LinkClock: LinkScheduler {
    public init() {}

    public var now: Date { Date() }

    public func schedule(at date: Date, _ fire: @escaping @MainActor () -> Void) -> LinkHandle {
        let delay = max(0, date.timeIntervalSinceNow)
        let task = Task { @MainActor in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard !Task.isCancelled else { return }
            fire()
        }
        return LinkHandle { task.cancel() }
    }
}

/// The real probe: `GET /api/state` through one immutable `DaemonClient`
/// per address, so a new address is a new client and nothing is
/// repointed. `invalidate()` drops them (new credentials, a switch).
@MainActor
public final class DaemonProbeTransport: LinkTransport {
    private let makeClient: (String) -> DaemonClient?
    private var clients: [String: DaemonClient] = [:]

    /// - Parameter makeClient: the client for an address, nil when the
    ///   address cannot be used (not a URL).
    public init(makeClient: @escaping (String) -> DaemonClient?) {
        self.makeClient = makeClient
    }

    public func probe(address: String, done: @escaping @MainActor (LinkProbeResult) -> Void) -> LinkHandle {
        guard let client = client(for: address) else {
            done(.failure(.transport))
            return LinkHandle()
        }
        let task = Task { @MainActor in
            let started = Date()
            let result: LinkProbeResult
            do {
                let state = try await client.state()
                result = .success(rtt: Date().timeIntervalSince(started), state: state)
            } catch {
                result = .failure(LinkError(error))
            }
            guard !Task.isCancelled else { return }
            done(result)
        }
        return LinkHandle { task.cancel() }
    }

    /// The client for an address, made once.
    public func client(for address: String) -> DaemonClient? {
        if let client = clients[address] { return client }
        guard let client = makeClient(address) else { return nil }
        clients[address] = client
        return client
    }

    /// Forgets every client and closes their sessions.
    public func invalidate() {
        for client in clients.values { client.invalidate() }
        clients = [:]
    }
}

/// The winners in `UserDefaults`, one small dictionary per computer.
public struct LinkWinnerDefaults: LinkWinnerStore {
    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    static func key(_ computerID: String) -> String { "computers.linkWinners.\(computerID)" }

    public func load(computerID: String) -> [NetworkInterface: String] {
        guard let raw = defaults.dictionary(forKey: Self.key(computerID)) as? [String: String] else { return [:] }
        var winners: [NetworkInterface: String] = [:]
        for (interface, address) in raw {
            if let interface = NetworkInterface(rawValue: interface) { winners[interface] = address }
        }
        return winners
    }

    public func save(_ winners: [NetworkInterface: String], computerID: String) {
        if winners.isEmpty {
            defaults.removeObject(forKey: Self.key(computerID))
        } else {
            defaults.set(Dictionary(uniqueKeysWithValues: winners.map { ($0.key.rawValue, $0.value) }), forKey: Self.key(computerID))
        }
    }
}

// MARK: - Classifying one attempt's error

extension LinkError {
    /// What one request's failure means for the link, from that request's
    /// own error (never from a shared flag): a key that matches no pin is
    /// `tlsUntrusted`, a 401 with a known reason is final, the app's own
    /// cancellation is never an error.
    public init(_ error: Error) {
        switch error {
        case is CancellationError:
            self = .cancelled
        case let error as URLError:
            self = error.code == .cancelled ? .cancelled : .transport
        case let error as DaemonError:
            switch error {
            case .unreachable, .timedOut:
                self = .transport
            case .untrusted:
                self = .tlsUntrusted
            case .unauthorized:
                // `DaemonClient.perform` already signed in once more: a 401
                // that survives that is a device the computer does not
                // know, as the old `/ws` token probe read it.
                self = .unauthorized(.unknownDevice)
            case let .pairing(problem):
                self = LinkError(problem)
            case .notSheepdog:
                self = .httpOther(404)
            case let .http(status, _), let .rejected(status, _):
                self = .httpOther(status)
            case .pendingGone:
                self = .httpOther(409)
            case .malformed:
                self = .decode
            }
        case let problem as PairingProblem:
            self = LinkError(problem)
        case is DecodingError:
            self = .decode
        default:
            self = .transport
        }
    }

    private init(_ problem: PairingProblem) {
        switch problem {
        case .pinMismatch, .renewedWhileAway:
            self = .tlsUntrusted
        default:
            self = .unauthorized(problem)
        }
    }
}
