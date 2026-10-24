import XCTest
@testable import SheepdogKit

/// A clock that moves only when the test says so; timers fire in order.
@MainActor
final class ManualLinkClock: LinkScheduler {
    private struct Entry {
        var at: Date
        var order: Int
        var handle: LinkHandle
        var fire: @MainActor () -> Void
    }

    var now: Date
    private var entries: [Entry] = []
    private var counter = 0

    init(now: Date = Date(timeIntervalSince1970: 1_800_000_000)) {
        self.now = now
    }

    func schedule(at date: Date, _ fire: @escaping @MainActor () -> Void) -> LinkHandle {
        counter += 1
        let handle = LinkHandle()
        entries.append(Entry(at: date, order: counter, handle: handle, fire: fire))
        return handle
    }

    /// Timers still set (not cancelled, not fired).
    var pending: Int { entries.filter { !$0.handle.isCancelled }.count }

    func advance(_ seconds: TimeInterval) {
        let end = now.addingTimeInterval(seconds)
        while true {
            entries.removeAll { $0.handle.isCancelled }
            guard let next = entries.filter({ $0.at <= end }).min(by: { ($0.at, $0.order) < ($1.at, $1.order) }) else { break }
            entries.removeAll { $0.order == next.order }
            now = max(now, next.at)
            next.fire()
        }
        now = end
    }
}

/// A network with a script per address: answer at once, fail, or hang until
/// the test answers or the probe is cancelled.
@MainActor
final class FakeLinkTransport: LinkTransport {
    enum Plan {
        case answer(LinkProbeResult)
        case hang
    }

    var plans: [String: Plan] = [:]
    var fallback: Plan = .answer(.success(rtt: 0.05, state: nil))
    private(set) var probed: [String] = []
    private(set) var cancelled: [String] = []
    private var hanging: [(address: String, handle: LinkHandle, done: @MainActor (LinkProbeResult) -> Void)] = []

    func probe(address: String, done: @escaping @MainActor (LinkProbeResult) -> Void) -> LinkHandle {
        probed.append(address)
        switch plans[address] ?? fallback {
        case let .answer(result):
            done(result)
            return LinkHandle()
        case .hang:
            let handle = LinkHandle { [weak self] in self?.cancelled.append(address) }
            hanging.append((address, handle, done))
            return handle
        }
    }

    /// Answers the oldest hanging probe of an address.
    func answer(_ address: String, _ result: LinkProbeResult) {
        guard let index = hanging.firstIndex(where: { $0.address == address && !$0.handle.isCancelled }) else {
            return XCTFail("no hanging probe of \(address)")
        }
        let probe = hanging.remove(at: index)
        probe.done(result)
    }
}

final class MemoryWinnerStore: LinkWinnerStore {
    var saved: [String: [NetworkInterface: String]] = [:]
    func load(computerID: String) -> [NetworkInterface: String] { saved[computerID] ?? [:] }
    func save(_ winners: [NetworkInterface: String], computerID: String) { saved[computerID] = winners }
}

/// `LinkRuntime` against a fake network and a manual clock: it runs the
/// link's effects (probes and their timeout, timers and their tokens, the
/// channel set, the winners) and the status every surface shows follows
/// the link.
@MainActor
final class LinkRuntimeTests: XCTestCase {
    private let lan = "lan:7777"
    private let tail = "tail:7777"
    private var clock: ManualLinkClock!
    private var net: FakeLinkTransport!
    private var channels: [Set<LinkChannel>] = []
    private var reopened: [LinkChannel] = []
    private var states = 0
    private var renewed = 0

    override func setUp() async throws {
        clock = ManualLinkClock()
        net = FakeLinkTransport()
        channels = []
        reopened = []
        states = 0
        renewed = 0
    }

    private func runtime(addresses: [String]? = nil, path: NetworkPath = NetworkPath(satisfied: true, interface: .wifi), winners: LinkWinnerStore? = nil, phase: AppPhase = .background) -> LinkRuntime {
        let link = ComputerLink(addresses: addresses ?? [lan], demand: LinkDemand(phase: phase), path: path)
        let runtime = LinkRuntime(computerID: "mac", link: link, transport: net, scheduler: clock, winners: winners, random: LinkRandom(seed: 7))
        runtime.onChannels = { [weak self] in self?.channels.append($0) }
        runtime.onReopen = { [weak self] in self?.reopened.append($0) }
        runtime.onState = { [weak self] _ in self?.states += 1 }
        runtime.onRenewTicket = { [weak self] in self?.renewed += 1 }
        return runtime
    }

    private func chip(_ runtime: LinkRuntime) -> String { LinkPresentation(runtime.link).chip }

    private static let state = DaemonState(agents: [], voice: .init(mode: "off", status: "off"))

    // MARK: - Probes

    func testTheFirstProbeGoesOnlineOpensChannelsAndHandsOverTheState() {
        net.fallback = .answer(.success(rtt: 0.05, state: Self.state))
        let r = runtime()
        XCTAssertEqual(r.link.state, .idle)
        r.send(.appPhase(.active))
        XCTAssertEqual(net.probed, [lan])
        XCTAssertEqual(r.link.state, .online(address: lan, since: clock.now))
        XCTAssertEqual(channels.last, [.events, .term])
        XCTAssertEqual(states, 1, "the probe's /api/state reply is the data")
        XCTAssertEqual(chip(r), "Connected")
        XCTAssertEqual(clock.pending, 1, "only the 30 s refresh is set")
    }

    func testAProbeThatHangsTimesOutAfterFourSeconds() {
        net.fallback = .hang
        let r = runtime()
        r.send(.appPhase(.active))
        XCTAssertEqual(chip(r), "Connecting…")
        clock.advance(3.9)
        XCTAssertEqual(r.link.state, .connecting(attempt: 0, outage: nil))
        clock.advance(0.1)
        XCTAssertEqual(net.cancelled, [lan], "the timed-out probe is cancelled")
        guard case let .waiting(_, attempt, outage) = r.link.state else { return XCTFail("\(r.link.state)") }
        XCTAssertEqual(attempt, 1)
        XCTAssertEqual(outage.lastError, .transport)
        XCTAssertEqual(chip(r), "Reconnecting…")
        XCTAssertTrue(r.probesInFlight.isEmpty)
    }

    func testABackoffTimerRetriesAndTheQuietPeriodTurnsItOffline() {
        net.fallback = .answer(.failure(.transport))
        let r = runtime()
        r.send(.appPhase(.active))
        XCTAssertEqual(chip(r), "Reconnecting…")
        // Retries on the jittered schedule (capped at 15 s), then offline
        // once the 30 s quiet period is over.
        clock.advance(29)
        XCTAssertGreaterThan(net.probed.count, 2)
        XCTAssertEqual(chip(r), "Reconnecting…")
        clock.advance(2)
        XCTAssertEqual(chip(r), "Offline")
        XCTAssertEqual(LinkPresentation(r.link).inline?.actions, [.tryNow])
        // The computer comes back on the next retry.
        net.fallback = .answer(.success(rtt: 0.05, state: nil))
        clock.advance(16)
        XCTAssertEqual(chip(r), "Connected")
    }

    func testTryNowRetriesAtOnce() {
        net.fallback = .answer(.failure(.transport))
        let r = runtime()
        r.send(.appPhase(.active))
        let before = net.probed.count
        net.fallback = .answer(.success(rtt: 0.05, state: nil))
        r.send(.retryTapped)
        XCTAssertEqual(net.probed.count, before + 1)
        XCTAssertEqual(chip(r), "Connected")
    }

    // MARK: - The status follows the link (the stale "Reconnecting…" class)

    /// TestFlight 0.1.17/0.1.18: Settings stayed on "Reconnecting…" while
    /// the voice worked, because `/events` kept failing. The status is the
    /// link's: `/events` reconnecting with the computer answering is
    /// Connected, said only in Health.
    func testEventsThatKeepFailingLeaveTheStatusConnected() {
        let r = runtime()
        r.send(.appPhase(.active))
        r.send(.channelOpened(.ws))
        for _ in 0 ..< 5 {
            clock.advance(2)
            r.send(.channelLost(.events, .transport))
            XCTAssertEqual(chip(r), "Connected")
            XCTAssertEqual(LinkPresentation(r.link).health, "Live updates are reconnecting")
        }
        XCTAssertEqual(reopened.filter { $0 == .events }.count, 5, "each loss is confirmed by one probe, then reopened")
        r.send(.channelOpened(.events))
        guard case .online = r.link.state else { return XCTFail("\(r.link.state)") }
        XCTAssertNil(LinkPresentation(r.link).health)
    }

    /// The opposite direction: a status that says Connected while nothing
    /// answers any more. A lost channel plus a failed confirming probe is
    /// Reconnecting at once, Offline after the quiet period, and Connected
    /// again with the first answer.
    func testALostChannelWithAFailedProbeIsReconnectingThenConnectedAgain() {
        let r = runtime()
        r.send(.appPhase(.active))
        XCTAssertEqual(chip(r), "Connected")
        net.fallback = .answer(.failure(.transport))
        r.send(.channelLost(.ws, .transport))
        XCTAssertEqual(chip(r), "Reconnecting…")
        XCTAssertEqual(channels.last, [], "every channel closes while the link is down")
        net.fallback = .answer(.success(rtt: 0.05, state: nil))
        clock.advance(16)
        XCTAssertEqual(chip(r), "Connected")
        XCTAssertEqual(channels.last, [.events, .term], "and opens again with the link")
    }

    func testOneLostChannelWithAGoodProbeStaysOnline() {
        let r = runtime()
        r.send(.appPhase(.active))
        let online = r.link.state
        r.send(.channelLost(.ws, .transport))
        XCTAssertEqual(r.link.state, online, "hysteresis: a good probe keeps it online")
    }

    func testOurOwnCancellationIsNeverAnError() {
        let r = runtime()
        r.send(.appPhase(.active))
        let online = r.link.state
        let probes = net.probed.count
        r.send(.channelLost(.events, .cancelled))
        r.send(.requestFailed(.cancelled))
        XCTAssertEqual(r.link.state, online)
        XCTAssertEqual(net.probed.count, probes, "nothing to confirm")
    }

    /// The New agent sheet's 9 s `/api/spaces`: one request that times out
    /// while the computer answers is not "couldn't reach". The link checks
    /// once and stays online; the sheet says "taking longer than usual".
    func testARequestThatTimesOutWhileOnlineStaysOnline() {
        let r = runtime()
        r.send(.appPhase(.active))
        net.fallback = .hang
        r.send(.requestFailed(LinkError(DaemonError.timedOut(endpoint: lan))))
        XCTAssertEqual(net.probed.last, lan, "one confirming probe")
        XCTAssertTrue(r.link.isUsable)
        XCTAssertEqual(chip(r), "Connected")
        let failure = RequestFailure(DaemonError.timedOut(endpoint: lan), linkUsable: r.link.isUsable)
        XCTAssertTrue(failure.isSlow)
        XCTAssertEqual(failure.title, "Taking longer than usual")
        net.answer(lan, .success(rtt: 0.05, state: nil))
        XCTAssertEqual(chip(r), "Connected")
    }

    // MARK: - Trust and refusals

    /// Decision 1: a key that matches no pin is not "scan again" on its
    /// own. With one address it is the untrusted-answer warning, with Try
    /// again.
    func testAPinMismatchIsTheUntrustedWarningNotARescan() {
        net.fallback = .answer(.failure(.tlsUntrusted))
        let r = runtime()
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.state, .untrustedAnswer(addresses: [lan]))
        XCTAssertEqual(chip(r), "Not verified")
        XCTAssertEqual(LinkPresentation(r.link).inline?.actions, [.tryAgain, .useAnotherAddress])
        XCTAssertEqual(clock.pending, 0, "no retry on a timer: nothing is sent to that network")
    }

    func testAMismatchOnOneAddressWithAnotherVerifiedGoesOnlineThere() {
        net.plans[lan] = .answer(.failure(.tlsUntrusted))
        net.plans[tail] = .answer(.success(rtt: 0.1, state: nil))
        let r = runtime(addresses: [lan, tail])
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.onlineAddress, tail)
        XCTAssertEqual(r.link.mismatched, [lan])
    }

    func testARevokedPhoneIsRefusedAndOutlivesDemand() {
        net.fallback = .answer(.failure(.unauthorized(.revoked)))
        let r = runtime()
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.state, .refused(.revoked))
        XCTAssertEqual(chip(r), "Scan again")
        r.send(.appPhase(.background))
        XCTAssertEqual(r.link.state, .refused(.revoked))
    }

    func testAnExpiredTicketIsRenewedNotRefused() {
        let r = runtime()
        r.send(.appPhase(.active))
        r.send(.requestFailed(.unauthorized(nil)))
        XCTAssertEqual(renewed, 1)
        XCTAssertTrue(r.link.isUsable)
    }

    // MARK: - Timers, winners, stopping

    func testTheRefreshTimerProbesEveryThirtySeconds() {
        net.fallback = .answer(.success(rtt: 0.05, state: Self.state))
        let r = runtime()
        r.send(.appPhase(.active))
        clock.advance(30)
        clock.advance(30)
        XCTAssertEqual(net.probed.count, 3)
        XCTAssertEqual(states, 3, "each refresh hands over fresh data")
        XCTAssertTrue(r.link.isUsable)
    }

    func testWinnersArePersistedPerInterfaceAndLoadedNextTime() {
        let store = MemoryWinnerStore()
        net.plans[lan] = .answer(.failure(.transport))
        net.plans[tail] = .answer(.success(rtt: 0.1, state: nil))
        let r = runtime(addresses: [lan, tail], path: NetworkPath(satisfied: true, interface: .cellular), winners: store)
        r.send(.appPhase(.active))
        XCTAssertEqual(store.saved["mac"], [.cellular: tail])

        // The next launch on cellular tries the remembered address first.
        net = FakeLinkTransport()
        let next = runtime(addresses: [lan, tail], path: NetworkPath(satisfied: true, interface: .cellular), winners: store)
        XCTAssertEqual(next.link.winners, [.cellular: tail])
        next.send(.appPhase(.active))
        XCTAssertEqual(net.probed.first, tail)
    }

    func testAStoppedRuntimeIgnoresLateRepliesAndTimers() {
        net.fallback = .hang
        let r = runtime()
        r.send(.appPhase(.active))
        var changes = 0
        r.onChange = { _ in changes += 1 }
        r.stop()
        XCTAssertEqual(net.cancelled, [lan])
        clock.advance(60)
        r.send(.retryTapped)
        XCTAssertEqual(changes, 0)
        XCTAssertEqual(clock.pending, 0)
    }

    func testNoDemandStopsEverything() {
        let r = runtime()
        r.send(.appPhase(.active))
        r.send(.appPhase(.background))
        XCTAssertEqual(r.link.state, .idle)
        XCTAssertEqual(channels.last, [])
        XCTAssertEqual(clock.pending, 0, "nothing polls or retries in the background")
    }

    /// The real clock fires on the main actor after the delay.
    func testTheRealClockFires() async {
        let real = LinkClock()
        let fired = expectation(description: "fired")
        _ = real.schedule(at: Date().addingTimeInterval(0.05)) { fired.fulfill() }
        let cancelled = real.schedule(at: Date().addingTimeInterval(0.05)) { XCTFail("a cancelled timer fired") }
        cancelled.cancel()
        await fulfillment(of: [fired], timeout: 2)
        try? await Task.sleep(nanoseconds: 100_000_000)
    }

    func testWinnerDefaultsRoundTrip() {
        let suite = "LinkRuntimeTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = LinkWinnerDefaults(defaults: defaults)
        store.save([.wifi: lan, .cellular: tail], computerID: "mac")
        XCTAssertEqual(store.load(computerID: "mac"), [.wifi: lan, .cellular: tail])
        store.save([:], computerID: "mac")
        XCTAssertEqual(store.load(computerID: "mac"), [:])
    }
}

/// Each attempt's error, classified on its own.
final class LinkErrorClassificationTests: XCTestCase {
    func testDaemonErrorsMapToLinkErrors() {
        XCTAssertEqual(LinkError(DaemonError.unreachable(endpoint: "a", detail: "")), .transport)
        XCTAssertEqual(LinkError(DaemonError.timedOut(endpoint: "a")), .transport)
        XCTAssertEqual(LinkError(DaemonError.untrusted(endpoint: "a")), .tlsUntrusted)
        XCTAssertEqual(LinkError(DaemonError.pairing(.pinMismatch)), .tlsUntrusted)
        XCTAssertEqual(LinkError(DaemonError.pairing(.revoked)), .unauthorized(.revoked))
        // A 401 that signing in again did not fix (`DaemonClient.perform`
        // already signed in once more): only a new scan helps.
        XCTAssertEqual(LinkError(DaemonError.unauthorized(endpoint: "a")), .unauthorized(.unknownDevice))
        XCTAssertEqual(LinkError(DaemonError.http(status: 502, endpoint: "a")), .httpOther(502))
        XCTAssertEqual(LinkError(DaemonError.rejected(status: 422, message: "")), .httpOther(422))
        XCTAssertEqual(LinkError(DaemonError.malformed("x")), .decode)
        XCTAssertEqual(LinkError(DaemonError.pendingGone), .httpOther(409))
    }

    func testCancellationIsNeverAnError() {
        XCTAssertEqual(LinkError(CancellationError()), .cancelled)
        XCTAssertEqual(LinkError(URLError(.cancelled)), .cancelled)
        XCTAssertEqual(LinkError(URLError(.timedOut)), .transport)
    }

    /// A mismatch is decided by the request's own handshake: the same
    /// cancellation without one is unreachable, with one it is untrusted
    /// (never "scan again" by itself).
    func testTranslateUsesThisRequestsVerdictOnly() {
        let secure = DaemonEndpoint(host: "mac", port: 7777, secure: true)
        XCTAssertEqual(DaemonClient.translate(URLError(.cancelled), endpoint: secure, sawMismatch: true), .untrusted(endpoint: "mac:7777"))
        XCTAssertEqual(DaemonClient.translate(URLError(.cancelled), endpoint: secure, sawMismatch: false), .unreachable(endpoint: "mac:7777", detail: ""))
        XCTAssertEqual(DaemonClient.translate(URLError(.timedOut), endpoint: secure, sawMismatch: false), .timedOut(endpoint: "mac:7777"))
    }

    func testRequestFailureWords() {
        let slow = RequestFailure(DaemonError.timedOut(endpoint: "mac:7777"), linkUsable: true)
        XCTAssertTrue(slow.isSlow)
        XCTAssertEqual(slow.message, "Your computer is taking longer than usual to answer. Try again.")
        let down = RequestFailure(DaemonError.timedOut(endpoint: "mac:7777"), linkUsable: false, name: "Home Mac")
        XCTAssertFalse(down.isSlow)
        XCTAssertEqual(down.title, "Couldn't reach Home Mac")
        let unreachable = RequestFailure(DaemonError.unreachable(endpoint: "mac:7777", detail: ""), linkUsable: true)
        XCTAssertEqual(unreachable.title, "Couldn't reach your computer")
        let refused = RequestFailure(DaemonError.rejected(status: 422, message: "no such folder"), linkUsable: true)
        XCTAssertEqual(refused.title, "Something went wrong")
        XCTAssertEqual(refused.message, "The daemon refused: no such folder")
    }
}
