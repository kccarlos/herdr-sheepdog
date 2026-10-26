import XCTest
@testable import SheepdogKit

/// Phase 5 of docs/dev/design/ios-state-architecture.md, the pure part:
/// `/ws`, `/events` and `/term` are channels of the link, which alone says
/// when a lost one opens again; the network path and the foreground kick
/// it.
final class LinkChannelTests: XCTestCase {
    private let lan = "lan:7777"

    // MARK: - One schedule: a channel that keeps failing waits on the link

    func testAFirstLossReopensAtOnceAndAStreakWaitsOnTheLinksTimer() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        d.send(.channelOpened(.ws))
        d.advance(1)
        // Lost within 10 s of opening, once: reopened as soon as the probe
        // says the computer answers.
        d.send(.channelLost(.ws, .transport))
        XCTAssertEqual(d.succeed(lan), [.reopen(.ws)])
        // It fails again at once (the upgrade is refused while REST works):
        // the reopen waits, on the link's own timer, within the cap.
        d.send(.channelLost(.ws, .transport))
        let fx = d.succeed(lan)
        XCTAssertFalse(fx.contains(.reopen(.ws)), "no spin at the probe's pace")
        var due: Date?
        for case let .setTimer(timer, at) in fx where timer.kind == .reopen { due = at }
        guard let at = due else { return XCTFail("a reopen timer: \(fx)") }
        XCTAssertLessThanOrEqual(at.timeIntervalSince(d.now), LinkBackoff.ceiling(attempt: 1, cap: LinkBackoff.cap) + 1e-9)
        XCTAssertEqual(d.link.reopening, [.ws])
        d.now = at
        XCTAssertEqual(d.fire(.reopen), [.reopen(.ws)])
        XCTAssertEqual(d.link.reopening, [])
    }

    func testAHealthyRunResetsTheStreak() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        for _ in 0 ..< 4 {
            d.send(.channelLost(.ws, .transport))
            d.succeed(lan)
            if d.link.timers[.reopen] != nil { d.fire(.reopen) }
        }
        XCTAssertEqual(d.link.channelFailures[.ws], 4)
        d.send(.channelOpened(.ws))
        d.advance(ComputerLink.healthyChannel + 1)
        d.send(.channelLost(.ws, .transport))
        XCTAssertEqual(d.link.channelFailures[.ws], 1)
        XCTAssertEqual(d.succeed(lan), [.reopen(.ws)], "after a healthy run, at once")
    }

    func testTheStreaksWaitNeverPassesTheCap() {
        var d = LinkDriver(demand: LinkDemand(isCurrent: false, voiceOnAnotherComputer: true))
        d.online()
        for _ in 0 ..< 40 {
            d.send(.channelLost(.events, .transport))
            let fx = d.succeed(lan)
            for case let .setTimer(timer, at) in fx where timer.kind == .reopen {
                XCTAssertLessThanOrEqual(at.timeIntervalSince(d.now), LinkBackoff.otherComputerCap + 1e-9)
            }
            if d.link.timers[.reopen] != nil { d.fire(.reopen) }
        }
    }

    func testAPendingReopenIsDroppedWhenTheLinkGoesDown() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        d.send(.channelLost(.ws, .transport))
        d.succeed(lan)
        d.send(.channelLost(.ws, .transport))
        d.succeed(lan)
        XCTAssertNotNil(d.link.timers[.reopen])
        let fx = d.send(.pathChanged(NetworkPath(satisfied: false)))
        XCTAssertTrue(fx.contains(.cancelTimer(.reopen)))
        XCTAssertEqual(d.link.reopening, [])
    }

    func testAReopenTimerDuringARaceWaitsForTheRace() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        d.send(.channelLost(.ws, .transport))
        d.succeed(lan)
        d.send(.channelLost(.ws, .transport))
        d.succeed(lan)
        // A refresh probe is in flight when the timer fires.
        d.advance(ComputerLink.refreshInterval)
        d.fire(.refresh)
        XCTAssertNotNil(d.link.race)
        XCTAssertEqual(d.fire(.reopen), [], "no reopen while a race is in flight")
        XCTAssertEqual(d.succeed(lan), [.reopen(.ws)], "the race's win reopens it")
    }

    // MARK: - The network path

    func testTheFirstInterfaceTheMonitorReportsIsNotAChange() {
        var d = LinkDriver(path: NetworkPath())
        d.online()
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: true, interface: .wifi))), [], "no probe at launch")
    }

    func testAnInterfaceChangeWhileOnlineReRacesAndReopensEveryChannel() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        d.send(.channelOpened(.ws))
        d.send(.channelOpened(.events))
        d.send(.channelOpened(.term))
        let since = d.now
        let fx = d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertEqual(fx, [.probe(address: lan, race: 2)])
        XCTAssertEqual(d.link.state, .online(address: lan, since: since), "online while it re-checks")
        let won = d.succeed(lan)
        // Sockets on the old interface are dead even if no keepalive has
        // noticed: every channel opens again on the new one.
        XCTAssertEqual(Set(won.filter { if case .reopen = $0 { return true } else { return false } }), [.reopen(.ws), .reopen(.events), .reopen(.term)])
    }

    func testNoNetworkStopsEverythingAndItsReturnTriesAtOnce() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        let down = d.send(.pathChanged(NetworkPath(satisfied: false)))
        XCTAssertEqual(d.link.state, .noNetwork)
        XCTAssertTrue(down.contains(.channels([])))
        XCTAssertTrue(d.link.timers.isEmpty, "no retries without a network")
        let up = d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertEqual(up.first, .probe(address: lan, race: 2), "at once")
        d.succeed(lan)
        XCTAssertEqual(d.link.channels, [.ws, .events, .term])
    }

    func testLowDataModeClosesAnotherComputersEventsOnly() {
        var other = LinkDriver(demand: LinkDemand(isCurrent: false))
        other.online()
        other.send(.pathChanged(NetworkPath(satisfied: true, interface: .wifi, constrained: true)))
        XCTAssertEqual(other.link.channels, [.term])
        var current = LinkDriver(demand: LinkDemand(isCurrent: true))
        current.online()
        current.send(.pathChanged(NetworkPath(satisfied: true, interface: .wifi, constrained: true)))
        XCTAssertEqual(current.link.channels, [.events, .term])
    }

    // MARK: - The background (3.2's table)

    func testTheBackgroundKeepsOnlyWhatTheTableAllows() {
        // The voice on with Keep listening: `/ws` only.
        var voice = LinkDriver(demand: LinkDemand(voiceOn: true, keepListening: true))
        voice.online()
        voice.send(.appPhase(.background))
        XCTAssertTrue(voice.link.isUsable)
        XCTAssertEqual(voice.link.channels, [.ws])
        // Keep listening off: idle; nothing stays open.
        var paused = LinkDriver(demand: LinkDemand(voiceOn: true, keepListening: false))
        paused.online()
        paused.send(.appPhase(.background))
        XCTAssertEqual(paused.link.state, .idle)
        XCTAssertEqual(paused.link.channels, [])
        // Another computer: idle.
        var other = LinkDriver(demand: LinkDemand(isCurrent: false))
        other.online()
        other.send(.appPhase(.background))
        XCTAssertEqual(other.link.state, .idle)
    }

    func testComingToTheForegroundRetriesAtOnce() {
        var d = LinkDriver(demand: LinkDemand(phase: .background, voiceOn: true, keepListening: true))
        d.send(.demandChanged(d.link.demand))
        d.fail(lan)
        guard case .waiting = d.link.state else { return XCTFail("\(d.link.state)") }
        for _ in 0 ..< 4 {
            d.fire(.backoff)
            d.fail(lan)
        }
        XCTAssertNotNil(d.link.timers[.backoff])
        let fx = d.send(.appPhase(.active))
        XCTAssertTrue(fx.contains(.probe(address: lan, race: d.raceID)), "a probe now, not at the backoff: \(fx)")
        guard case .connecting(0, _) = d.link.state else { return XCTFail("the backoff starts over: \(d.link.state)") }
    }
}

/// Phase 5 on a computer's runtime: a fake network path, a manual clock, a
/// scripted network, and the voice's and the terminal's channels as fakes.
@MainActor
final class LinkChannelRuntimeTests: XCTestCase {
    var network: FakeComputerNetwork!
    var clock: ManualLinkClock!
    var pathSource: ManualNetworkPath!
    var fleet: ComputerFleet!

    /// A channel the session would attach: it opens when allowed and
    /// wanted, and records what the link told it.
    final class FakeChannel: LinkChannelClient {
        var wanted = true
        var allowed = false
        var isOpen = false
        var opens = 0
        var reopens = 0
        var reporter: LinkChannelReporter?
        /// The address the link told it to use, and where each open went.
        var address: String?
        var openedAt: [String?] = []

        func linkUses(address: String) {
            self.address = address
        }

        func linkAllows(_ allowed: Bool, reporter: LinkChannelReporter) {
            self.reporter = reporter
            self.allowed = allowed
            if allowed, wanted, !isOpen { open() }
            if !allowed { isOpen = false }
        }

        func linkReopens(reporter: LinkChannelReporter) {
            self.reporter = reporter
            reopens += 1
            guard allowed, wanted else { return }
            open()
        }

        private func open() {
            isOpen = true
            opens += 1
            openedAt.append(address)
            reporter?.opened()
        }

        /// The socket dropped by itself.
        func drop() {
            isOpen = false
            reporter?.lost(.transport)
        }
    }

    override func setUp() async throws {
        network = FakeComputerNetwork()
        clock = ManualLinkClock()
        pathSource = ManualNetworkPath(NetworkPath(satisfied: true, interface: .wifi))
        fleet = ComputerFleet(path: pathSource.path)
        pathSource.start { [weak self] path in self?.fleet.setPath(path) }
    }

    func makeModel(_ address: String, demand: LinkDemand) -> ComputerModel {
        let network = network!
        let connection = ComputerConnection(computerID: address, address: address, makeRequests: { network.makeRequests($0) })
        return ComputerModel(id: address, name: address, addresses: [address], certExpires: nil, connection: connection,
                             scheduler: clock, demand: demand, path: fleet.path, random: LinkRandom(seed: 11))
    }

    /// The current computer, online, with the voice on and both channels
    /// attached and open.
    func currentOnline(keepListening: Bool = true) async -> (ComputerModel, FakeChannel, FakeChannel) {
        let home = makeModel("home", demand: LinkDemand(isCurrent: true, voiceOn: true, keepListening: keepListening))
        fleet.set([home])
        fleet.makeCurrent("home")
        let ws = FakeChannel()
        let term = FakeChannel()
        home.attach(.ws, ws)
        home.attach(.term, term)
        home.start()
        await eventually("online") { home.link.isUsable }
        await eventually("streams open") { self.network.openStreams("home") == 1 }
        XCTAssertTrue(ws.isOpen)
        XCTAssertTrue(term.isOpen)
        return (home, ws, term)
    }

    func testWiFiToCellularReRacesAndReopensEveryChannel() async {
        let (home, ws, term) = await currentOnline()
        let probes = network.stateCalls("home")
        let opened = network.eventsOpened("home")
        pathSource.set(NetworkPath(satisfied: true, interface: .cellular))
        await eventually("re-raced") { self.network.stateCalls("home") > probes }
        await eventually("every channel reopened") { ws.reopens == 1 && term.reopens == 1 && self.network.eventsOpened("home") == opened + 1 }
        XCTAssertTrue(home.link.isUsable, "online throughout")
    }

    func testNoNetworkThenBack() async {
        let (home, ws, term) = await currentOnline()
        pathSource.set(NetworkPath(satisfied: false))
        XCTAssertEqual(home.link.state, .noNetwork)
        XCTAssertFalse(ws.allowed)
        XCTAssertFalse(term.allowed)
        await eventually("stream closed") { self.network.openStreams("home") == 0 }
        XCTAssertEqual(LinkPresentation(home.link).chip, "No network")
        let probes = network.stateCalls("home")
        clock.advance(300)
        await settle()
        XCTAssertEqual(network.stateCalls("home"), probes, "no retries without a network")
        pathSource.set(NetworkPath(satisfied: true, interface: .wifi))
        await eventually("back at once") { home.link.isUsable }
        XCTAssertTrue(ws.allowed && ws.isOpen)
        XCTAssertTrue(term.allowed && term.isOpen)
        await eventually("stream reopened") { self.network.openStreams("home") == 1 }
    }

    func testANewModelGetsTheCurrentPath() async {
        pathSource.set(NetworkPath(satisfied: false))
        let late = makeModel("late", demand: LinkDemand(isCurrent: false))
        fleet.set([late])
        late.start()
        XCTAssertEqual(late.link.state, .noNetwork)
    }

    func testComingToTheForegroundRetriesAtOnce() async {
        let (home, _, _) = await currentOnline()
        home.setAppPhase(.background)
        XCTAssertTrue(home.link.isUsable, "the voice keeps it in the background")
        // The computer goes away: the link backs off.
        network.setFailure(DaemonError.unreachable(endpoint: "home", detail: "gone"), for: "home")
        await home.refresh()
        await eventually("waiting") {
            if case .waiting = home.link.state { return true }
            return false
        }
        for _ in 0 ..< 3 {
            clock.advance(LinkBackoff.cap)
            await settle()
        }
        let retryAt: Date
        switch home.link.state {
        case let .waiting(at, _, _), let .offline(at, _, _): retryAt = at
        default: return XCTFail("\(home.link.state)")
        }
        network.setFailure(nil, for: "home")
        let probes = network.stateCalls("home")
        home.setAppPhase(.active)
        await eventually("probed now") { self.network.stateCalls("home") > probes }
        XCTAssertLessThan(clock.now, retryAt, "before the backoff was due")
        await eventually("online") { home.link.isUsable }
    }

    func testTheBackgroundClosesWhatTheTableSays() async {
        // Voice on, Keep listening on: `/ws` stays, `/events` and `/term` close.
        let (home, ws, term) = await currentOnline(keepListening: true)
        home.setAppPhase(.background)
        XCTAssertTrue(ws.allowed && ws.isOpen)
        XCTAssertFalse(term.allowed)
        await eventually("events closed") { self.network.openStreams("home") == 0 }
        home.setAppPhase(.active)
        await eventually("back") { term.isOpen && self.network.openStreams("home") == 1 }

        // Keep listening off: nothing stays open, the link is idle.
        home.setDemand(LinkDemand(isCurrent: true, voiceOn: true, keepListening: false))
        home.setAppPhase(.background)
        XCTAssertEqual(home.link.state, .idle)
        XCTAssertFalse(ws.allowed)
        XCTAssertFalse(term.allowed)
        await eventually("events closed") { self.network.openStreams("home") == 0 }
        home.setAppPhase(.active)
        await eventually("everything back") { ws.isOpen && term.isOpen && self.network.openStreams("home") == 1 }
    }

    func testALostChannelIsReopenedByTheLinkAlone() async {
        let (home, ws, _) = await currentOnline()
        let probes = network.stateCalls("home")
        ws.drop()
        await eventually("probed once") { self.network.stateCalls("home") == probes + 1 }
        await eventually("reopened") { ws.reopens == 1 && ws.isOpen }
        XCTAssertTrue(home.link.isUsable)
    }

    func testAChannelThatKeepsFailingDoesNotSpin() async {
        let (_, ws, _) = await currentOnline()
        ws.wanted = true
        // Every reopen fails at once while the computer answers.
        var drops = 0
        for _ in 0 ..< 6 {
            ws.drop()
            drops += 1
            await settle()
        }
        let reopensBefore = ws.reopens
        XCTAssertLessThan(reopensBefore, drops, "later reopens wait on the link's timer")
        clock.advance(LinkBackoff.cap)
        await eventually("reopened after the wait") { ws.reopens > reopensBefore }
    }

    func testReportsFromAnOlderPairingOrADetachedChannelLandNowhere() async {
        let (home, ws, _) = await currentOnline()
        let old = ws.reporter!
        XCTAssertTrue(old.isCurrent)
        home.renew(addresses: ["home"], certExpires: nil)
        XCTAssertFalse(old.isCurrent, "a new epoch")
        let probes = network.stateCalls("home")
        await eventually("online again") { home.link.isUsable }
        let afterRenew = network.stateCalls("home")
        XCTAssertGreaterThan(afterRenew, probes)
        old.lost(.transport)
        await settle()
        XCTAssertEqual(network.stateCalls("home"), afterRenew, "the old pairing's report is dropped")
        // A switch detaches the channel: its reports go nowhere.
        let current = ws.reporter!
        home.attach(.ws, nil)
        XCTAssertFalse(current.isCurrent)
        current.lost(.transport)
        await settle()
        XCTAssertEqual(network.stateCalls("home"), afterRenew)
    }
}
