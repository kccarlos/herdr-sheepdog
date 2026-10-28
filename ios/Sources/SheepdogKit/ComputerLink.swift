import Foundation

/// Can this app talk to this computer right now, and if not, why, and what
/// is it doing about it: one computer's connection as a pure value
/// (docs/dev/design/ios-state-architecture.md 3.2–3.4).
///
/// Written like `SessionCore`: every socket, probe, timer, network and
/// lifecycle callback becomes a `LinkEvent`, `apply(_:now:using:)` updates
/// the link and returns the `LinkEffect`s to run (probe an address, set a
/// timer, open the channels…). It does no I/O. Time comes in as `now` and
/// the jitter's random source is injected, so tests are deterministic.
///
/// The voice session's own state stays in `SessionCore`; this is only the
/// link. Every status surface renders it through `LinkPresentation`.
///
/// It is called `ComputerLink`, not `Link`, so it does not collide with
/// SwiftUI's `Link` view in files that import both.
public struct ComputerLink: Equatable, Sendable {
    /// An outage in progress: when it began and the last reason.
    public struct Outage: Equatable, Sendable {
        public var since: Date
        public var lastError: LinkError
        /// Past the quiet period: the outage is shown as offline, and a
        /// retry in progress does not flip it back to "Reconnecting…".
        public var long: Bool

        public init(since: Date, lastError: LinkError, long: Bool = false) {
            self.since = since
            self.lastError = lastError
            self.long = long
        }
    }

    /// Why an online link is degraded. Both still show Connected; only
    /// Health says it.
    public enum Degradation: Equatable, Sendable {
        /// `/events` dropped and is reconnecting while the computer answers.
        case liveUpdates
        /// Replies take longer than `slowReply`.
        case slow
    }

    public enum State: Equatable, Sendable {
        /// Not needed: opens nothing.
        case idle
        /// Racing the addresses. `attempt` counts the failed attempts of
        /// this outage; `outage` is nil on a first connect.
        case connecting(attempt: Int, outage: Outage?)
        /// The last request or probe succeeded.
        case online(address: String, since: Date)
        /// Reachable, but part of it is not working.
        case degraded(address: String, since: Date, why: Degradation)
        /// Backing off between attempts, within the quiet period.
        case waiting(retryAt: Date, attempt: Int, outage: Outage)
        /// Unreachable past the quiet period; still retrying.
        case offline(retryAt: Date, attempt: Int, outage: Outage)
        /// The phone has no network: no retries.
        case noNetwork
        /// Something answered with a key that matches no pin, and no
        /// address verified. Nothing is sent; Try again, a path change or a
        /// new pairing leaves it.
        case untrustedAnswer(addresses: [String])
        /// A verified computer refused this device. Only a new pairing
        /// leaves it.
        case refused(PairingProblem)
        /// The app and the daemon cannot talk; an update, then Retry or
        /// coming to the foreground, leaves it.
        case incompatible(message: String)

        /// The computer cannot be used right now, as opposed to idle, still
        /// connecting, or briefly reconnecting (within the quiet period).
        public var isUnreachable: Bool {
            switch self {
            case .offline, .noNetwork, .untrustedAnswer, .refused, .incompatible: return true
            case let .connecting(_, outage): return outage?.long == true
            case .idle, .online, .degraded, .waiting: return false
            }
        }
    }

    /// What a new pairing or a certificate rotation changed.
    public enum CredentialChange: Equatable, Sendable {
        /// New credentials (a scan): everything starts over.
        case newPairing(certExpires: Date?)
        /// The computer's next pin was promoted: a normal event, not an
        /// error.
        case pinPromoted(certExpires: Date?)
    }

    // MARK: - Policy

    /// The outage stays "Reconnecting…" this long before it is offline.
    public static let quietPeriod: TimeInterval = 30
    /// The probe (`GET /api/state`) gives up after this long.
    public static let probeTimeout: TimeInterval = 4
    /// Replies slower than this make the link degraded.
    public static let slowReply: TimeInterval = 2
    /// While online, a safety-net refresh (a confirming probe) runs this
    /// often.
    public static let refreshInterval: TimeInterval = 30
    /// A channel that stayed open this long was healthy: its next loss
    /// reopens at once. One lost sooner counts towards its failure streak.
    public static let healthyChannel: TimeInterval = 10

    // MARK: - State

    public private(set) var state: State = .idle
    public private(set) var demand: LinkDemand
    public private(set) var path: NetworkPath
    /// The computer's addresses, preferred first.
    public private(set) var addresses: [String]
    /// The address that last won a race, per interface.
    public private(set) var winners: [NetworkInterface: String]
    /// When the computer's served certificate expires, for the rotation
    /// window.
    public private(set) var certExpires: Date?
    /// Addresses that answered with another key while another address
    /// verified (Settings ▸ Addresses marks them).
    public private(set) var mismatched: Set<String> = []
    /// The last time anything from the computer succeeded.
    public private(set) var lastSeen: Date?
    /// The race in flight, if any. There is never more than one.
    public private(set) var race: AddressRace?
    /// The channels the runtime was last told may be open.
    public private(set) var channels: Set<LinkChannel> = []
    /// The timers that are set, with the token each was set with.
    public private(set) var timers: [LinkTimer.Kind: LinkTimer] = [:]
    /// The address every channel was last told to use.
    public private(set) var addressInUse: String?
    /// Each channel's losses in a row (a loss within `healthyChannel` of
    /// its opening counts). From the second, the reopen waits on the link's
    /// `reopen` timer with the backoff, so a channel that keeps failing
    /// while the computer answers cannot spin.
    public private(set) var channelFailures: [LinkChannel: Int] = [:]
    /// Lost channels confirmed reachable, waiting for the `reopen` timer.
    public private(set) var reopening: Set<LinkChannel> = []

    private var slow = false
    private var eventsReconnecting = false
    /// Channels lost while online, waiting for the confirming probe.
    private var lost: Set<LinkChannel> = []
    /// When each channel last opened, for its failure streak.
    private var channelOpenedAt: [LinkChannel: Date] = [:]
    /// The interface changed while online: once the re-race wins, every
    /// open channel reopens, since a socket on the old interface is dead
    /// even if no keepalive has noticed yet.
    private var reopenAllOnWin = false
    private var nextRaceID = 0
    private var nextTimerToken = 0
    /// The jitter fraction drawn for the event being applied (0 between
    /// events).
    private var jitter: Double = 0

    public init(addresses: [String], winners: [NetworkInterface: String] = [:], certExpires: Date? = nil, demand: LinkDemand = LinkDemand(), path: NetworkPath = NetworkPath()) {
        self.addresses = addresses
        self.winners = winners.filter { addresses.contains($0.value) }
        self.certExpires = certExpires
        self.demand = demand
        self.path = path
    }

    // MARK: - Derived

    /// Online or degraded: requests may go out.
    public var isUsable: Bool {
        switch state {
        case .online, .degraded: return true
        default: return false
        }
    }

    /// The address requests go to while usable.
    public var onlineAddress: String? {
        switch state {
        case let .online(address, _), let .degraded(address, _, _): return address
        default: return nil
        }
    }

    // MARK: - Apply

    public mutating func apply(_ event: LinkEvent, now: Date) -> [LinkEffect] {
        var rng = SystemRandomNumberGenerator()
        return apply(event, now: now, using: &rng)
    }

    public mutating func apply<R: RandomNumberGenerator>(_ event: LinkEvent, now: Date, using rng: inout R) -> [LinkEffect] {
        var fx: [LinkEffect] = []
        // One draw per event, used only if this event schedules a retry.
        jitter = Double.random(in: 0...1, using: &rng)
        defer { jitter = 0 }
        switch event {
        case let .demandChanged(demand):
            demandChanged(demand, now: now, &fx)
        case let .appPhase(phase):
            var demand = self.demand
            demand.phase = phase
            let foreground = phase == .active && self.demand.phase != .active
            demandChanged(demand, now: now, &fx)
            if foreground { kick(now: now, &fx, from: .foreground) }
        case let .pathChanged(path):
            pathChanged(path, now: now, &fx)
        case let .probeSucceeded(address, raceID, rtt):
            probeSucceeded(address, raceID: raceID, rtt: rtt, now: now, &fx)
        case let .probeFailed(address, raceID, error):
            probeFailed(address, raceID: raceID, error: error, now: now, &fx)
        case let .channelOpened(channel):
            guard isUsable else { break }
            lastSeen = now
            lost.remove(channel)
            channelOpenedAt[channel] = now
            if channel == .events { eventsReconnecting = false }
            settle()
        case let .channelLost(channel, error):
            guard isUsable, error != .cancelled else { break }
            if let opened = channelOpenedAt.removeValue(forKey: channel), now.timeIntervalSince(opened) >= Self.healthyChannel {
                channelFailures[channel] = 1
            } else {
                channelFailures[channel, default: 0] += 1
            }
            reopening.remove(channel)
            if reopening.isEmpty { cancelTimer(.reopen, &fx) }
            lost.insert(channel)
            confirm(now: now, &fx)
        case let .requestSucceeded(rtt):
            guard isUsable else { break }
            lastSeen = now
            slow = rtt > Self.slowReply
            settle()
        case let .requestFailed(error):
            requestFailed(error, now: now, &fx)
        case let .timerFired(timer):
            timerFired(timer, now: now, &fx)
        case .retryTapped:
            kick(now: now, &fx, from: .retry)
        case let .credentialsChanged(change):
            credentialsChanged(change, now: now, &fx)
        case let .addressesChanged(addresses):
            addressesChanged(addresses, now: now, &fx)
        case let .preferAddress(address):
            preferAddress(address, now: now, &fx)
        }
        syncChannels(now: now, &fx)
        return fx
    }

    // MARK: - Demand, lifecycle and the network path

    private mutating func demandChanged(_ demand: LinkDemand, now: Date, _ fx: inout [LinkEffect]) {
        self.demand = demand
        guard demand.needed else {
            goIdle(&fx)
            return
        }
        if state == .idle { begin(now: now, &fx) }
    }

    /// Starts from scratch: a first connect, or no network.
    private mutating func begin(now: Date, _ fx: inout [LinkEffect]) {
        if path.satisfied {
            connect(attempt: 0, outage: nil, now: now, &fx)
        } else {
            goNoNetwork(&fx)
        }
    }

    private mutating func pathChanged(_ path: NetworkPath, now: Date, _ fx: inout [LinkEffect]) {
        let old = self.path
        self.path = path
        guard demand.needed else { return }
        switch state {
        case .idle, .refused, .incompatible:
            return
        default:
            break
        }
        guard path.satisfied else {
            goNoNetwork(&fx)
            return
        }
        if state == .noNetwork {
            connect(attempt: 0, outage: nil, now: now, &fx)
        } else if !old.satisfied || (old.interface != nil && old.interface != path.interface) {
            // The first interface the monitor reports is not a change.
            kick(now: now, &fx, from: .pathChange)
        }
    }

    private enum Kick { case foreground, pathChange, retry, addresses }

    /// Try now: the backoff starts over. From online it re-checks without
    /// leaving online.
    private mutating func kick(now: Date, _ fx: inout [LinkEffect], from source: Kick) {
        guard demand.needed else { return }
        switch state {
        case .idle:
            begin(now: now, &fx)
        case .connecting:
            // A path change may make the race's results wrong: start over.
            // Anything else while connecting is a no-op.
            if source == .pathChange || source == .addresses {
                let outage = connectingOutage
                connect(attempt: 0, outage: outage, now: now, &fx)
            }
        case let .waiting(_, _, outage), let .offline(_, _, outage):
            connect(attempt: 0, outage: outage, now: now, &fx)
        case .online, .degraded:
            if source == .pathChange || source == .addresses {
                if source == .pathChange { reopenAllOnWin = true }
                startRace(order: fullOrder, now: now, &fx)
            } else {
                confirm(now: now, &fx)
            }
        case .untrustedAnswer:
            // Coming back to the foreground on the same hostile network
            // changes nothing; Retry, a path change or new addresses do.
            if source != .foreground { begin(now: now, &fx) }
        case .incompatible:
            // It outlives a lost network, so a kick checks the path first.
            if source == .foreground || source == .retry { begin(now: now, &fx) }
        case .noNetwork, .refused:
            break
        }
    }

    private var connectingOutage: Outage? {
        if case let .connecting(_, outage) = state { return outage }
        return nil
    }

    // MARK: - Credentials and addresses

    private mutating func credentialsChanged(_ change: CredentialChange, now: Date, _ fx: inout [LinkEffect]) {
        switch change {
        case let .pinPromoted(certExpires):
            self.certExpires = certExpires
        case let .newPairing(certExpires):
            self.certExpires = certExpires
            mismatched = []
            addressInUse = nil
            channelFailures = [:]
            channelOpenedAt = [:]
            stop(&fx)
            state = .idle
            if demand.needed { begin(now: now, &fx) }
        }
    }

    private mutating func addressesChanged(_ addresses: [String], now: Date, _ fx: inout [LinkEffect]) {
        self.addresses = addresses
        mismatched.formIntersection(addresses)
        let kept = winners.filter { addresses.contains($0.value) }
        if kept != winners {
            winners = kept
            fx.append(.rememberWinners(kept))
        }
        if let address = onlineAddress, !addresses.contains(address) {
            connect(attempt: 0, outage: nil, now: now, &fx)
            return
        }
        if !isUsable { kick(now: now, &fx, from: .addresses) }
    }

    private mutating func preferAddress(_ address: String, now: Date, _ fx: inout [LinkEffect]) {
        guard let index = addresses.firstIndex(of: address) else { return }
        addresses.remove(at: index)
        addresses.insert(address, at: 0)
        if !winners.isEmpty {
            winners = [:]
            fx.append(.rememberWinners([:]))
        }
        if onlineAddress == address { return }
        kick(now: now, &fx, from: .addresses)
    }

    // MARK: - The race

    private var fullOrder: [String] {
        AddressRace.order(addresses: addresses, winners: winners, interface: path.interface)
    }

    private mutating func connect(attempt: Int, outage: Outage?, now: Date, _ fx: inout [LinkEffect]) {
        cancelTimer(.backoff, &fx)
        cancelTimer(.quiet, &fx)
        cancelTimer(.reopen, &fx)
        state = .connecting(attempt: attempt, outage: outage)
        slow = false
        eventsReconnecting = false
        lost = []
        reopening = []
        reopenAllOnWin = false
        startRace(order: fullOrder, now: now, &fx)
    }

    /// One probe of the address in use, without leaving online
    /// (hysteresis). A race already in flight answers the same question.
    private mutating func confirm(now: Date, _ fx: inout [LinkEffect]) {
        guard isUsable, race == nil, let address = onlineAddress else { return }
        startRace(order: [address], now: now, &fx)
    }

    private mutating func startRace(order: [String], now: Date, _ fx: inout [LinkEffect]) {
        cancelRace(&fx)
        nextRaceID += 1
        var race = AddressRace(id: nextRaceID, order: order)
        for address in race.start() {
            fx.append(.probe(address: address, race: race.id))
        }
        self.race = race
        if race.hasMoreToStart { setTimer(.stagger, at: now.addingTimeInterval(AddressRace.stagger), &fx) }
        if order.isEmpty { raceLost(race, now: now, &fx) }
    }

    private mutating func cancelRace(_ fx: inout [LinkEffect]) {
        if let race {
            for address in race.inFlight {
                fx.append(.cancelProbe(address: address, race: race.id))
            }
        }
        race = nil
        cancelTimer(.stagger, &fx)
    }

    private mutating func probeSucceeded(_ address: String, raceID: Int, rtt: TimeInterval, now: Date, _ fx: inout [LinkEffect]) {
        guard var race, race.id == raceID, let others = race.succeeded(address) else { return }
        self.race = nil
        cancelTimer(.stagger, &fx)
        for other in others {
            fx.append(.cancelProbe(address: other, race: raceID))
        }
        lastSeen = now
        slow = rtt > Self.slowReply
        mismatched.remove(address)
        for failure in race.failures where failure.error == .tlsUntrusted {
            mismatched.insert(failure.address)
        }
        if race.order.count > 1 || !isUsable, let interface = path.interface, winners[interface] != address {
            winners[interface] = address
            fx.append(.rememberWinners(winners))
        }
        cancelTimer(.backoff, &fx)
        cancelTimer(.quiet, &fx)
        let since: Date
        if isUsable, onlineAddress == address, case let .online(_, was) = state {
            since = was
        } else if isUsable, onlineAddress == address, case let .degraded(_, was, _) = state {
            since = was
        } else {
            since = now
        }
        let wasUsable = isUsable
        state = .online(address: address, since: since)
        if addressInUse != address {
            addressInUse = address
            fx.append(.useAddress(address))
            lost = []
            reopening = []
            cancelTimer(.reopen, &fx)
        } else if wasUsable {
            // A lost channel, confirmed: open it again (and say so while
            // `/events` comes back). After an interface change, every open
            // channel reopens on the new one.
            for channel in LinkChannel.allCases where channels.contains(channel) {
                if lost.contains(channel) {
                    scheduleReopen(channel, now: now, &fx)
                } else if reopenAllOnWin, !reopening.contains(channel) {
                    channelOpenedAt[channel] = nil
                    fx.append(.reopen(channel))
                } else if reopening.contains(channel), timers[.reopen] == nil {
                    // Its timer fired while this race was in flight.
                    reopening.remove(channel)
                    fx.append(.reopen(channel))
                }
            }
            if lost.contains(.events) && channels.contains(.events) { eventsReconnecting = true }
            lost = []
        }
        reopenAllOnWin = false
        settle()
    }

    /// Opens a lost channel again: at once after a healthy run or its first
    /// loss, else after the backoff for its streak on the `reopen` timer.
    private mutating func scheduleReopen(_ channel: LinkChannel, now: Date, _ fx: inout [LinkEffect]) {
        let failures = channelFailures[channel] ?? 0
        guard failures > 1 else {
            fx.append(.reopen(channel))
            return
        }
        reopening.insert(channel)
        if timers[.reopen] == nil {
            let delay = LinkBackoff.delay(attempt: failures - 1, cap: demand.backoffCap, jitter: jitter)
            setTimer(.reopen, at: now.addingTimeInterval(delay), &fx)
        }
    }

    private mutating func probeFailed(_ address: String, raceID: Int, error: LinkError, now: Date, _ fx: inout [LinkEffect]) {
        // A cancellation is never an error: the probe that the app itself
        // cancelled belongs to a race that is over, and any other ends in
        // the runtime's own timeout.
        guard error != .cancelled, var race, race.id == raceID, let next = race.failed(address, error) else { return }
        for address in next {
            fx.append(.probe(address: address, race: raceID))
        }
        self.race = race
        if !next.isEmpty {
            if race.hasMoreToStart {
                setTimer(.stagger, at: now.addingTimeInterval(AddressRace.stagger), &fx)
            } else {
                cancelTimer(.stagger, &fx)
            }
        }
        if race.isLost { raceLost(race, now: now, &fx) }
    }

    /// Every address of the race failed.
    private mutating func raceLost(_ lostRace: AddressRace, now: Date, _ fx: inout [LinkEffect]) {
        race = nil
        cancelTimer(.stagger, &fx)
        let partial = lostRace.order != fullOrder
        switch AddressRace.verdict(lostRace.failures, certExpires: certExpires, now: now) {
        case let .incompatible(message):
            stop(&fx)
            state = .incompatible(message: message)
        case let .refused(problem):
            stop(&fx)
            state = .refused(problem)
        case let .untrusted(addresses):
            if partial {
                // A mismatch on the address in use: try the computer's
                // other addresses before warning (decision 1).
                connect(attempt: 0, outage: nil, now: now, &fx)
            } else {
                stop(&fx)
                state = .untrustedAnswer(addresses: addresses)
            }
        case let .unreachable(error, renewTicket):
            if renewTicket { fx.append(.renewTicket) }
            let attempt: Int
            var outage: Outage
            switch state {
            case let .connecting(was, existing?):
                attempt = was
                outage = existing
                outage.lastError = error
            case let .connecting(was, nil):
                attempt = was
                outage = Outage(since: now, lastError: error)
            default:
                attempt = 0
                outage = Outage(since: now, lastError: error)
            }
            let retryAt = now.addingTimeInterval(LinkBackoff.delay(attempt: attempt, cap: demand.backoffCap, jitter: jitter))
            stop(&fx)
            if outage.long || now.timeIntervalSince(outage.since) >= Self.quietPeriod {
                outage.long = true
                state = .offline(retryAt: retryAt, attempt: attempt + 1, outage: outage)
            } else {
                state = .waiting(retryAt: retryAt, attempt: attempt + 1, outage: outage)
                setTimer(.quiet, at: outage.since.addingTimeInterval(Self.quietPeriod), &fx)
            }
            setTimer(.backoff, at: retryAt, &fx)
        }
    }

    // MARK: - Requests and timers

    private mutating func requestFailed(_ error: LinkError, now: Date, _ fx: inout [LinkEffect]) {
        guard isUsable else { return }
        switch error {
        case .cancelled, .httpOther, .decode:
            // The computer answered (or the app cancelled): still online.
            return
        case let .unauthorized(problem?):
            stop(&fx)
            state = .refused(problem)
        case .unauthorized(nil):
            fx.append(.renewTicket)
        case let .incompatible(message):
            stop(&fx)
            state = .incompatible(message: message)
        case .transport, .tlsUntrusted:
            confirm(now: now, &fx)
        }
    }

    private mutating func timerFired(_ timer: LinkTimer, now: Date, _ fx: inout [LinkEffect]) {
        guard timers[timer.kind] == timer else { return }
        timers[timer.kind] = nil
        switch timer.kind {
        case .stagger:
            guard var race else { return }
            for address in race.staggerElapsed() {
                fx.append(.probe(address: address, race: race.id))
            }
            self.race = race
            if race.hasMoreToStart { setTimer(.stagger, at: now.addingTimeInterval(AddressRace.stagger), &fx) }
        case .backoff:
            switch state {
            case let .waiting(_, attempt, outage), let .offline(_, attempt, outage):
                connect(attempt: attempt, outage: outage, now: now, &fx)
            default:
                break
            }
        case .quiet:
            if case let .waiting(retryAt, attempt, outage) = state {
                var outage = outage
                outage.long = true
                state = .offline(retryAt: retryAt, attempt: attempt, outage: outage)
            }
        case .refresh:
            guard isUsable else { return }
            setTimer(.refresh, at: now.addingTimeInterval(Self.refreshInterval), &fx)
            confirm(now: now, &fx)
        case .reopen:
            // A race in flight answers first; its win reopens them.
            guard isUsable, race == nil else { return }
            for channel in LinkChannel.allCases where reopening.contains(channel) && channels.contains(channel) {
                fx.append(.reopen(channel))
            }
            reopening = []
        }
    }

    // MARK: - Transitions

    private mutating func goIdle(_ fx: inout [LinkEffect]) {
        // A refusal is a fact about the credentials: it outlives demand.
        if case .refused = state { return }
        stop(&fx)
        channelFailures = [:]
        channelOpenedAt = [:]
        state = .idle
    }

    private mutating func goNoNetwork(_ fx: inout [LinkEffect]) {
        stop(&fx)
        state = .noNetwork
    }

    /// Cancels the race and every timer.
    private mutating func stop(_ fx: inout [LinkEffect]) {
        cancelRace(&fx)
        for kind in LinkTimer.Kind.allCases { cancelTimer(kind, &fx) }
        slow = false
        eventsReconnecting = false
        lost = []
        reopening = []
        reopenAllOnWin = false
    }

    /// Online or degraded, from the facts.
    private mutating func settle() {
        guard let address = onlineAddress else { return }
        let since: Date
        switch state {
        case let .online(_, was), let .degraded(_, was, _): since = was
        default: return
        }
        if eventsReconnecting {
            state = .degraded(address: address, since: since, why: .liveUpdates)
        } else if slow {
            state = .degraded(address: address, since: since, why: .slow)
        } else {
            state = .online(address: address, since: since)
        }
    }

    /// Tells the runtime which channels may be open, and keeps the
    /// safety-net refresh running only while usable.
    private mutating func syncChannels(now: Date, _ fx: inout [LinkEffect]) {
        let allowed = isUsable ? demand.channels(on: path) : []
        if allowed != channels {
            channels = allowed
            fx.append(.channels(allowed))
            // A closed channel is not open: it reopens with the set.
            for channel in LinkChannel.allCases where !allowed.contains(channel) {
                channelOpenedAt[channel] = nil
            }
            reopening.formIntersection(allowed)
            if reopening.isEmpty { cancelTimer(.reopen, &fx) }
        }
        if isUsable, timers[.refresh] == nil {
            setTimer(.refresh, at: now.addingTimeInterval(Self.refreshInterval), &fx)
        } else if !isUsable {
            cancelTimer(.refresh, &fx)
        }
    }

    private mutating func setTimer(_ kind: LinkTimer.Kind, at date: Date, _ fx: inout [LinkEffect]) {
        nextTimerToken += 1
        let timer = LinkTimer(kind: kind, token: nextTimerToken)
        timers[kind] = timer
        fx.append(.setTimer(timer, at: date))
    }

    private mutating func cancelTimer(_ kind: LinkTimer.Kind, _ fx: inout [LinkEffect]) {
        guard timers[kind] != nil else { return }
        timers[kind] = nil
        fx.append(.cancelTimer(kind))
    }
}

/// A timer the link set. A fired timer whose token is not the current one
/// for its kind is stale and does nothing.
public struct LinkTimer: Hashable, Sendable {
    public enum Kind: String, CaseIterable, Hashable, Sendable {
        /// The next attempt after a failure.
        case backoff
        /// Start the race's next address.
        case stagger
        /// The quiet period is over: the outage shows as offline.
        case quiet
        /// The safety-net refresh while online.
        case refresh
        /// Reopen the channels in `reopening` (a failure streak's wait).
        case reopen
    }

    public var kind: Kind
    public var token: Int

    public init(kind: Kind, token: Int) {
        self.kind = kind
        self.token = token
    }
}

/// Everything that happens to a link.
public enum LinkEvent: Equatable, Sendable {
    case demandChanged(LinkDemand)
    /// Foreground or background. Coming to the foreground kicks the link.
    case appPhase(AppPhase)
    case pathChanged(NetworkPath)
    /// A pinned `GET /api/state` on `address` succeeded, for race `race`.
    case probeSucceeded(address: String, race: Int, rtt: TimeInterval)
    case probeFailed(address: String, race: Int, error: LinkError)
    case channelOpened(LinkChannel)
    case channelLost(LinkChannel, LinkError)
    case requestSucceeded(rtt: TimeInterval)
    case requestFailed(LinkError)
    case timerFired(LinkTimer)
    /// Try now, Try again, Test connection.
    case retryTapped
    case credentialsChanged(ComputerLink.CredentialChange)
    /// Settings ▸ Addresses added or removed one.
    case addressesChanged([String])
    /// Settings ▸ Addresses ▸ Prefer this address.
    case preferAddress(String)
}

/// What the runtime does for the link.
public enum LinkEffect: Hashable, Sendable {
    /// A pinned `GET /api/state` with `ComputerLink.probeTimeout`; answer
    /// with `probeSucceeded` or `probeFailed` carrying the same race.
    case probe(address: String, race: Int)
    case cancelProbe(address: String, race: Int)
    /// Replaces any timer of the same kind.
    case setTimer(LinkTimer, at: Date)
    case cancelTimer(LinkTimer.Kind)
    /// Every channel and request now goes to this address (a new immutable
    /// client); open channels reopen there.
    case useAddress(String)
    /// The channels that may be open; close any other.
    case channels(Set<LinkChannel>)
    /// A channel that was lost, confirmed reachable (or an open one after
    /// the interface changed): open it again. The only way a channel
    /// reconnects; no socket retries by itself.
    case reopen(LinkChannel)
    /// Persist the winners per interface.
    case rememberWinners([NetworkInterface: String])
    /// A verified computer wants a fresh ticket: sign in again.
    case renewTicket
}
