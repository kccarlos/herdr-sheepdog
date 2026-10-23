import XCTest
@testable import SheepdogKit

/// Drives one `ComputerLink` with a fake clock and a seeded jitter.
struct LinkDriver {
    var link: ComputerLink
    var now: Date
    var rng: SeededGenerator

    init(addresses: [String] = ["lan:7777"], winners: [NetworkInterface: String] = [:], certExpires: Date? = nil, demand: LinkDemand = LinkDemand(), path: NetworkPath = NetworkPath(satisfied: true, interface: .wifi), seed: UInt64 = 1) {
        link = ComputerLink(addresses: addresses, winners: winners, certExpires: certExpires, demand: demand, path: path)
        now = Date(timeIntervalSinceReferenceDate: 10_000)
        rng = SeededGenerator(seed: seed)
    }

    @discardableResult
    mutating func send(_ event: LinkEvent) -> [LinkEffect] {
        link.apply(event, now: now, using: &rng)
    }

    mutating func advance(_ seconds: TimeInterval) { now = now.addingTimeInterval(seconds) }

    var raceID: Int { link.race?.id ?? -1 }

    @discardableResult
    mutating func fire(_ kind: LinkTimer.Kind) -> [LinkEffect] {
        guard let timer = link.timers[kind] else {
            XCTFail("no \(kind) timer")
            return []
        }
        return send(.timerFired(timer))
    }

    @discardableResult
    mutating func succeed(_ address: String, rtt: TimeInterval = 0.05) -> [LinkEffect] {
        send(.probeSucceeded(address: address, race: raceID, rtt: rtt))
    }

    @discardableResult
    mutating func fail(_ address: String, _ error: LinkError = .transport) -> [LinkEffect] {
        send(.probeFailed(address: address, race: raceID, error: error))
    }

    /// Demand on, the first address answers.
    mutating func online(_ address: String? = nil) {
        send(.demandChanged(link.demand))
        if link.state == .idle { send(.demandChanged(link.demand)) }
        succeed(address ?? link.addresses[0])
    }
}

/// Event sequences through `ComputerLink`: the state after each event and
/// the exact effects it asks for.
final class LinkTests: XCTestCase {
    private let lan = "lan:7777"
    private let tail = "tail:7777"

    private func idleDriver(addresses: [String]? = nil, certExpires: Date? = nil, demand: LinkDemand = LinkDemand(phase: .background), path: NetworkPath = NetworkPath(satisfied: true, interface: .wifi)) -> LinkDriver {
        LinkDriver(addresses: addresses ?? [lan], certExpires: certExpires, demand: demand, path: path)
    }

    private func timer(_ d: LinkDriver, _ kind: LinkTimer.Kind) -> LinkTimer? { d.link.timers[kind] }

    // MARK: - The golden walk

    func testFirstConnectProbesThenGoesOnlineAndOpensChannels() {
        var d = idleDriver()
        XCTAssertEqual(d.link.state, .idle)
        XCTAssertEqual(d.send(.appPhase(.active)), [.probe(address: lan, race: 1)])
        XCTAssertEqual(d.link.state, .connecting(attempt: 0, outage: nil))
        XCTAssertTrue(d.link.timers.isEmpty, "one address: no stagger")

        d.advance(0.2)
        let fx = d.succeed(lan)
        XCTAssertEqual(d.link.state, .online(address: lan, since: d.now))
        XCTAssertEqual(fx, [
            .rememberWinners([.wifi: lan]),
            .useAddress(lan),
            .channels([.events, .term]),
            .setTimer(LinkTimer(kind: .refresh, token: 1), at: d.now.addingTimeInterval(30)),
        ])
        XCTAssertNil(d.link.race)
        XCTAssertEqual(d.link.lastSeen, d.now)
        XCTAssertEqual(d.link.addressInUse, lan)
    }

    func testDemandWithoutNetworkGoesStraightToNoNetwork() {
        var d = idleDriver(path: NetworkPath(satisfied: false))
        XCTAssertEqual(d.send(.appPhase(.active)), [])
        XCTAssertEqual(d.link.state, .noNetwork)
    }

    func testTheVoiceOnTheCurrentComputerAllowsTheSocket() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        XCTAssertEqual(d.link.channels, [.ws, .events, .term])
    }

    // MARK: - The address race

    func testTheRaceStaggersThenTheFirstSuccessWinsAndCancelsTheRest() {
        var d = LinkDriver(addresses: [lan, tail, "fwd:7777"])
        XCTAssertEqual(d.send(.demandChanged(LinkDemand())), [
            .probe(address: lan, race: 1),
            .setTimer(LinkTimer(kind: .stagger, token: 1), at: d.now.addingTimeInterval(0.3)),
        ])
        d.advance(0.3)
        XCTAssertEqual(d.fire(.stagger), [
            .probe(address: tail, race: 1),
            .setTimer(LinkTimer(kind: .stagger, token: 2), at: d.now.addingTimeInterval(0.3)),
        ])
        d.advance(0.1)
        let fx = d.succeed(tail)
        XCTAssertEqual(Array(fx.prefix(3)), [
            .cancelTimer(.stagger),
            .cancelProbe(address: lan, race: 1),
            .rememberWinners([.wifi: tail]),
        ])
        XCTAssertTrue(fx.contains(.useAddress(tail)))
        XCTAssertFalse(fx.contains(.probe(address: "fwd:7777", race: 1)), "the third never started")
        XCTAssertEqual(d.link.onlineAddress, tail)
    }

    func testAFailureStartsTheNextAddressAtOnce() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        XCTAssertEqual(d.fail(lan), [.probe(address: tail, race: 1), .cancelTimer(.stagger)])
        XCTAssertEqual(d.link.race?.inFlight, [tail])
    }

    func testTheRememberedWinnerForThisInterfaceGoesFirst() {
        var d = LinkDriver(addresses: [lan, tail], winners: [.cellular: tail, .wifi: lan], path: NetworkPath(interface: .cellular))
        let fx = d.send(.demandChanged(LinkDemand()))
        XCTAssertEqual(fx.first, .probe(address: tail, race: 1))
    }

    func testAnInterfaceChangeWhileOnlineReRacesWithoutLeavingOnline() {
        var d = LinkDriver(addresses: [lan, tail])
        d.online(lan)
        let since = d.now
        d.advance(5)
        let fx = d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertEqual(fx.first, .probe(address: lan, race: 2))
        XCTAssertEqual(d.link.state, .online(address: lan, since: since), "no amber flash")
        d.fail(lan)
        let won = d.succeed(tail)
        XCTAssertTrue(won.contains(.rememberWinners([.wifi: lan, .cellular: tail])))
        XCTAssertTrue(won.contains(.useAddress(tail)))
        XCTAssertEqual(d.link.onlineAddress, tail)
        XCTAssertEqual(d.link.winners, [.wifi: lan, .cellular: tail])
    }

    func testAStaleRaceResultDoesNothing() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        // The path changes mid-race: race 2 replaces race 1.
        d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertEqual(d.raceID, 2)
        let before = d.link
        XCTAssertEqual(d.send(.probeSucceeded(address: lan, race: 1, rtt: 0.1)), [])
        XCTAssertEqual(d.send(.probeFailed(address: lan, race: 1, error: .transport)), [])
        XCTAssertEqual(d.link.state, before.state)
        XCTAssertEqual(d.link.race, before.race)
    }

    func testAResultForAnAddressNotInFlightDoesNothing() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        XCTAssertEqual(d.succeed(tail), [], "tail has not started")
        XCTAssertEqual(d.succeed("elsewhere:1"), [])
    }

    func testAStaleTimerDoesNothing() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        let old = d.link.timers[.stagger]!
        d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertNotEqual(d.link.timers[.stagger], old)
        XCTAssertEqual(d.send(.timerFired(old)), [])
    }

    func testNoAddressesFailsAtOnce() {
        var d = LinkDriver(addresses: [])
        d.send(.demandChanged(LinkDemand()))
        guard case .waiting = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertNil(d.link.race)
    }

    // MARK: - Backoff, the quiet period and offline

    func testAllAddressesFailingWaitsWithJitterWithinTheCeiling() {
        var d = LinkDriver(addresses: [lan])
        d.send(.demandChanged(LinkDemand()))
        let start = d.now
        d.advance(4)
        let fx = d.fail(lan)
        guard case let .waiting(retryAt, attempt, outage) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertEqual(attempt, 1)
        XCTAssertEqual(outage, ComputerLink.Outage(since: d.now, lastError: .transport))
        let delay = retryAt.timeIntervalSince(d.now)
        XCTAssertGreaterThanOrEqual(delay, 0)
        XCTAssertLessThanOrEqual(delay, 0.5)
        XCTAssertEqual(fx, [
            .setTimer(LinkTimer(kind: .quiet, token: 1), at: d.now.addingTimeInterval(30)),
            .setTimer(LinkTimer(kind: .backoff, token: 2), at: retryAt),
        ])
        XCTAssertNotEqual(start, d.now)
    }

    func testTheBackoffDoublesUpToTheCapAndStaysThere() {
        var d = LinkDriver(addresses: [lan])
        d.send(.demandChanged(LinkDemand()))
        for attempt in 0..<12 {
            d.fail(lan)
            let (retryAt, counted): (Date, Int)
            switch d.link.state {
            case let .waiting(at, n, _), let .offline(at, n, _): (retryAt, counted) = (at, n)
            default: return XCTFail("\(d.link.state)")
            }
            XCTAssertEqual(counted, attempt + 1)
            let delay = retryAt.timeIntervalSince(d.now)
            XCTAssertLessThanOrEqual(delay, min(15, 0.5 * pow(2, Double(attempt))) + 1e-9)
            d.now = retryAt
            d.fire(.backoff)
            XCTAssertEqual(d.link.state, .connecting(attempt: attempt + 1, outage: d.link.state.outageForTest))
        }
    }

    func testAnotherComputerBacksOffToSixtySecondsWhileTheVoiceIsElsewhere() {
        XCTAssertEqual(LinkDemand(isCurrent: false, voiceOnAnotherComputer: true).backoffCap, 60)
        XCTAssertEqual(LinkDemand(isCurrent: false).backoffCap, 15)
        XCTAssertEqual(LinkDemand(isCurrent: true, voiceOnAnotherComputer: true).backoffCap, 15)
        XCTAssertEqual(LinkBackoff.ceiling(attempt: 20, cap: 60), 60)
        XCTAssertEqual(LinkBackoff.ceiling(attempt: Int.max, cap: 15), 15, "the exponent is clamped")
    }

    func testTheJitterIsTheInjectedSourceSoTheSameSeedGivesTheSameRetry() {
        func retryAt(seed: UInt64) -> Date? {
            var d = LinkDriver(seed: seed)
            d.send(.demandChanged(LinkDemand()))
            d.fail(lan)
            if case let .waiting(at, _, _) = d.link.state { return at }
            return nil
        }
        XCTAssertEqual(retryAt(seed: 7), retryAt(seed: 7))
        XCTAssertNotEqual(retryAt(seed: 7), retryAt(seed: 8))
    }

    func testTheQuietPeriodTurnsWaitingIntoOffline() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        let outageStart = d.now
        // Keep failing until the quiet timer is the next to fire.
        while true {
            guard case let .waiting(retryAt, _, _) = d.link.state else { break }
            let quietAt = outageStart.addingTimeInterval(30)
            if retryAt >= quietAt {
                d.now = quietAt
                let fx = d.fire(.quiet)
                XCTAssertEqual(fx, [])
                guard case let .offline(_, _, outage) = d.link.state else { return XCTFail("\(d.link.state)") }
                XCTAssertTrue(outage.long)
                XCTAssertEqual(outage.since, outageStart)
                XCTAssertNotNil(d.link.timers[.backoff], "offline keeps retrying")
                return
            }
            d.now = retryAt
            d.fire(.backoff)
            d.fail(lan)
        }
        // Each retry failed past the quiet period first: still offline.
        guard case .offline = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testAFailurePastTheQuietPeriodIsOfflineAtOnceAndStaysOfflineWhileRetrying() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        guard case let .waiting(retryAt, _, _) = d.link.state else { return XCTFail() }
        d.now = retryAt
        d.fire(.backoff)
        d.advance(40)
        d.fail(lan)
        guard case let .offline(_, _, outage) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertTrue(outage.long)
        XCTAssertNil(d.link.timers[.quiet])
        d.advance(15)
        d.fire(.backoff)
        guard case let .connecting(_, retrying?) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertTrue(retrying.long, "a retry in progress still reads as offline")
        XCTAssertEqual(LinkPresentation(d.link).chip, "Offline")
    }

    // MARK: - Kicks

    func testRetryResetsTheBackoffAndConnectsAtOnce() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        let fx = d.send(.retryTapped)
        XCTAssertEqual(fx, [.cancelTimer(.backoff), .cancelTimer(.quiet), .probe(address: lan, race: 2)])
        guard case let .connecting(attempt, outage?) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertEqual(attempt, 0)
        XCTAssertEqual(outage.lastError, .transport, "still the same outage")
    }

    func testRetryWhileConnectingIsANoOp() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        let before = d.link
        XCTAssertEqual(d.send(.retryTapped), [])
        XCTAssertEqual(d.link, before)
    }

    func testComingToTheForegroundKicksAWaitingLink() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.send(.demandChanged(d.link.demand))
        d.fail(lan)
        d.send(.appPhase(.background))
        guard case .waiting = d.link.state else { return XCTFail("voice keeps it: \(d.link.state)") }
        let fx = d.send(.appPhase(.active))
        XCTAssertTrue(fx.contains(.probe(address: lan, race: 2)))
        guard case .connecting(0, _) = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testComingToTheForegroundWhileOnlineProbesWithoutLeavingOnline() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        d.send(.appPhase(.background))
        XCTAssertEqual(d.link.channels, [.ws])
        let fx = d.send(.appPhase(.active))
        XCTAssertEqual(fx.first, .probe(address: lan, race: 2))
        XCTAssertTrue(d.link.isUsable)
        XCTAssertEqual(d.link.channels, [.ws, .events, .term])
    }

    func testTheSafetyNetRefreshProbesEveryThirtySeconds() {
        var d = LinkDriver()
        d.online()
        d.advance(30)
        let fx = d.fire(.refresh)
        XCTAssertEqual(fx, [.setTimer(LinkTimer(kind: .refresh, token: 2), at: d.now.addingTimeInterval(30)), .probe(address: lan, race: 2)])
        XCTAssertTrue(d.link.isUsable)
    }

    // MARK: - No network

    func testNoNetworkStopsEverythingAndTheNetworkComingBackConnectsAtOnce() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        d.fail(tail)
        let fx = d.send(.pathChanged(NetworkPath(satisfied: false)))
        XCTAssertEqual(Set(fx), [.cancelTimer(.backoff), .cancelTimer(.quiet)])
        XCTAssertEqual(d.link.state, .noNetwork)
        XCTAssertTrue(d.link.timers.isEmpty)
        XCTAssertNil(d.link.race)
        XCTAssertEqual(d.send(.retryTapped), [])
        XCTAssertEqual(d.send(.appPhase(.active)), [])

        let back = d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular)))
        XCTAssertEqual(back.first, .probe(address: lan, race: 2))
        XCTAssertEqual(d.link.state, .connecting(attempt: 0, outage: nil))
    }

    func testLosingTheNetworkWhileOnlineClosesTheChannels() {
        var d = LinkDriver()
        d.online()
        let fx = d.send(.pathChanged(NetworkPath(satisfied: false)))
        XCTAssertTrue(fx.contains(.channels([])))
        XCTAssertTrue(fx.contains(.cancelTimer(.refresh)))
        XCTAssertEqual(d.link.state, .noNetwork)
    }

    // MARK: - Hysteresis and degraded

    func testALostSocketWithAGoodProbeStaysOnlineAndReopens() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.online()
        let since = d.now
        d.advance(3)
        XCTAssertEqual(d.send(.channelLost(.ws, .transport)), [.probe(address: lan, race: 2)])
        XCTAssertEqual(d.link.state, .online(address: lan, since: since), "online until the probe says otherwise")
        XCTAssertEqual(d.send(.channelLost(.term, .transport)), [], "one probe at a time")
        let fx = d.succeed(lan)
        XCTAssertEqual(fx, [.reopen(.ws), .reopen(.term)])
        XCTAssertEqual(d.link.state, .online(address: lan, since: since))
    }

    func testLostEventsWithAGoodProbeIsDegradedUntilTheyReopen() {
        var d = LinkDriver()
        d.online()
        d.send(.channelLost(.events, .transport))
        XCTAssertEqual(d.succeed(lan), [.reopen(.events)])
        guard case .degraded(lan, _, .liveUpdates) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertEqual(LinkPresentation(d.link).chip, "Connected")
        XCTAssertEqual(d.send(.channelOpened(.events)), [])
        guard case .online = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testALostChannelWithAFailedProbeWaits() {
        var d = LinkDriver()
        d.online()
        d.send(.channelLost(.events, .transport))
        let fx = d.fail(lan)
        guard case let .waiting(_, 1, outage) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertEqual(outage.since, d.now)
        XCTAssertTrue(fx.contains(.channels([])))
        XCTAssertTrue(fx.contains(.cancelTimer(.refresh)))
    }

    func testSlowRepliesAreDegradedAndRecover() {
        var d = LinkDriver()
        d.online()
        d.send(.requestSucceeded(rtt: 2.5))
        guard case .degraded(_, _, .slow) = d.link.state else { return XCTFail("\(d.link.state)") }
        XCTAssertEqual(LinkPresentation(d.link).health, "Replies are slow")
        d.send(.requestSucceeded(rtt: 0.2))
        guard case .online = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testASlowProbeIsDegradedFromTheStart() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.succeed(lan, rtt: 3)
        guard case .degraded(_, _, .slow) = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testRequestFailuresWhileOnline() {
        var d = LinkDriver()
        d.online()
        XCTAssertEqual(d.send(.requestFailed(.httpOther(500))), [], "the computer answered")
        XCTAssertEqual(d.send(.requestFailed(.decode)), [])
        XCTAssertEqual(d.send(.requestFailed(.unauthorized(nil))), [.renewTicket])
        XCTAssertTrue(d.link.isUsable)
        XCTAssertEqual(d.send(.requestFailed(.transport)), [.probe(address: lan, race: 2)])
        XCTAssertTrue(d.link.isUsable)
    }

    // MARK: - Cancellation is never an error

    func testCancellationNeverCounts() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        let connecting = d.link
        XCTAssertEqual(d.fail(lan, .cancelled), [])
        XCTAssertEqual(d.link, connecting)
        d.succeed(lan)
        let online = d.link
        XCTAssertEqual(d.send(.channelLost(.events, .cancelled)), [])
        XCTAssertEqual(d.send(.requestFailed(.cancelled)), [])
        XCTAssertEqual(d.link, online)
    }

    // MARK: - Trust (decision 1)

    func testAMismatchOnOneAddressWithAnotherVerifiedGoesOnlineAndMarksIt() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .tlsUntrusted)
        d.succeed(tail)
        XCTAssertEqual(d.link.onlineAddress, tail)
        XCTAssertEqual(d.link.mismatched, [lan])
        // The same address verifying later clears the mark.
        d.send(.preferAddress(lan))
        d.succeed(lan)
        XCTAssertEqual(d.link.mismatched, [])
    }

    func testEveryAddressMismatchingIsAnUntrustedAnswerWithNoTimers() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .tlsUntrusted)
        d.fail(tail, .tlsUntrusted)
        XCTAssertEqual(d.link.state, .untrustedAnswer(addresses: [lan, tail]))
        XCTAssertTrue(d.link.timers.isEmpty, "no retry loop against a hostile network")
        XCTAssertEqual(LinkPresentation(d.link).inline?.actions, [.tryAgain, .useAnotherAddress])
        XCTAssertEqual(d.send(.appPhase(.background)), [])
        XCTAssertEqual(d.link.state, .idle)
    }

    func testAnUntrustedAnswerLeavesOnRetryOrAPathChangeButNotTheForeground() {
        var d = LinkDriver(addresses: [lan], demand: LinkDemand(voiceOn: true))
        d.send(.demandChanged(d.link.demand))
        d.fail(lan, .tlsUntrusted)
        XCTAssertEqual(d.link.state, .untrustedAnswer(addresses: [lan]))
        d.send(.appPhase(.background))
        XCTAssertEqual(d.send(.appPhase(.active)), [])
        XCTAssertEqual(d.link.state, .untrustedAnswer(addresses: [lan]))

        XCTAssertEqual(d.send(.retryTapped), [.probe(address: lan, race: 2)])
        d.fail(lan, .tlsUntrusted)
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular))), [.probe(address: lan, race: 3)])
        d.succeed(lan)
        XCTAssertTrue(d.link.isUsable)
    }

    func testAMismatchAndAnUnreachableAddressIsUntrustedNotARescan() {
        let expires = Date(timeIntervalSinceReferenceDate: 10_000 + 3 * 86_400)
        var d = LinkDriver(addresses: [lan, tail], certExpires: expires)
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .tlsUntrusted)
        d.fail(tail, .transport)
        XCTAssertEqual(d.link.state, .untrustedAnswer(addresses: [lan]))
    }

    func testEveryAddressMismatchingInsideTheRotationWindowAsksForAScan() {
        // The certificate expires in 3 days: the computer may have switched.
        let expires = Date(timeIntervalSinceReferenceDate: 10_000 + 3 * 86_400)
        var d = LinkDriver(addresses: [lan, tail], certExpires: expires)
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .tlsUntrusted)
        d.fail(tail, .tlsUntrusted)
        XCTAssertEqual(d.link.state, .refused(.renewedWhileAway))
    }

    func testAMismatchOnTheAddressInUseTriesTheOthersFirst() {
        var d = LinkDriver(addresses: [lan, tail])
        d.online(lan)
        d.send(.requestFailed(.tlsUntrusted))
        XCTAssertEqual(d.link.race?.order, [lan])
        let fx = d.fail(lan, .tlsUntrusted)
        XCTAssertTrue(fx.contains(.probe(address: lan, race: 3)), "a full race")
        XCTAssertEqual(d.link.state, .connecting(attempt: 0, outage: nil))
        d.fail(lan, .tlsUntrusted)
        d.succeed(tail)
        XCTAssertEqual(d.link.onlineAddress, tail)
        XCTAssertEqual(d.link.mismatched, [lan])
    }

    func testAPinPromotionIsNotAnError() {
        var d = LinkDriver()
        d.online()
        let before = d.link.state
        XCTAssertEqual(d.send(.credentialsChanged(.pinPromoted(certExpires: d.now.addingTimeInterval(365 * 86_400)))), [])
        XCTAssertEqual(d.link.state, before)
    }

    // MARK: - Refused and incompatible

    func testARevocationIsFinalUntilANewPairing() {
        var d = LinkDriver(addresses: [lan, tail])
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .unauthorized(.revoked))
        d.fail(tail, .transport)
        XCTAssertEqual(d.link.state, .refused(.revoked))
        XCTAssertTrue(d.link.timers.isEmpty)
        XCTAssertEqual(d.send(.retryTapped), [])
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular))), [])
        d.send(.appPhase(.background))
        XCTAssertEqual(d.link.state, .refused(.revoked), "a refusal outlives demand")
        d.send(.appPhase(.active))
        XCTAssertEqual(d.link.state, .refused(.revoked))

        let fx = d.send(.credentialsChanged(.newPairing(certExpires: nil)))
        XCTAssertEqual(fx.first, .probe(address: lan, race: 2))
        d.fail(lan, .unauthorized(nil))
        XCTAssertTrue(d.succeed(tail).contains(.useAddress(tail)))
    }

    func testARevocationWhileOnlineIsRefused() {
        var d = LinkDriver()
        d.online()
        let fx = d.send(.requestFailed(.unauthorized(.unknownDevice)))
        XCTAssertEqual(d.link.state, .refused(.unknownDevice))
        XCTAssertTrue(fx.contains(.channels([])))
        XCTAssertEqual(LinkPresentation(d.link).data, .cleared)
    }

    func testAnUnknown401RenewsTheTicketAndRetries() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        let fx = d.fail(lan, .unauthorized(nil))
        XCTAssertEqual(fx.first, .renewTicket)
        guard case .waiting = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testIncompatibleWaitsForAnUpdateThenRetryOrTheForeground() {
        var d = LinkDriver(demand: LinkDemand(voiceOn: true))
        d.send(.demandChanged(d.link.demand))
        d.fail(lan, .incompatible("Update sheepdog on your computer."))
        XCTAssertEqual(d.link.state, .incompatible(message: "Update sheepdog on your computer."))
        XCTAssertTrue(d.link.timers.isEmpty, "no timer leaves it")
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: true, interface: .cellular))), [])
        d.send(.appPhase(.background))
        XCTAssertEqual(d.send(.appPhase(.active)), [.probe(address: lan, race: 2)])
        d.fail(lan, .incompatible("x"))
        XCTAssertEqual(d.send(.retryTapped), [.probe(address: lan, race: 3)])
    }

    func testIncompatibleOutlivesALostNetworkAndARetryThenWaitsForIt() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .incompatible("x"))
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: false))), [])
        XCTAssertEqual(d.link.state, .incompatible(message: "x"))
        XCTAssertEqual(d.send(.retryTapped), [])
        XCTAssertEqual(d.link.state, .noNetwork, "found by the property test (seed 55)")
    }

    // MARK: - Demand

    func testNoDemandGoesIdleAndClosesEverything() {
        var d = LinkDriver()
        d.online()
        let fx = d.send(.appPhase(.background))
        XCTAssertEqual(d.link.state, .idle)
        XCTAssertEqual(fx, [.cancelTimer(.refresh), .channels([])])
        XCTAssertTrue(d.link.timers.isEmpty)
    }

    func testAnActionInFlightKeepsTheLinkInTheBackground() {
        var d = LinkDriver(demand: LinkDemand(actionsInFlight: true))
        d.online()
        d.send(.appPhase(.background))
        XCTAssertTrue(d.link.isUsable)
        XCTAssertEqual(d.link.channels, [], "REST only")
        var done = d.link.demand
        done.actionsInFlight = false
        d.send(.demandChanged(done))
        XCTAssertEqual(d.link.state, .idle)
    }

    func testIdleIgnoresEverythingButDemand() {
        var d = idleDriver()
        XCTAssertEqual(d.send(.retryTapped), [])
        XCTAssertEqual(d.send(.pathChanged(NetworkPath(satisfied: false))), [])
        XCTAssertEqual(d.send(.channelLost(.events, .transport)), [])
        XCTAssertEqual(d.send(.requestFailed(.transport)), [])
        XCTAssertEqual(d.link.state, .idle)
    }

    // MARK: - Addresses

    func testRemovingTheAddressInUseReconnects() {
        var d = LinkDriver(addresses: [lan, tail])
        d.online(lan)
        let fx = d.send(.addressesChanged([tail]))
        XCTAssertTrue(fx.contains(.rememberWinners([:])))
        XCTAssertTrue(fx.contains(.probe(address: tail, race: 2)))
        XCTAssertEqual(d.link.state, .connecting(attempt: 0, outage: nil))
    }

    func testAddingAnAddressWhileWaitingTriesItAtOnce() {
        var d = LinkDriver(addresses: [lan])
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        let fx = d.send(.addressesChanged([lan, tail]))
        XCTAssertTrue(fx.contains(.probe(address: lan, race: 2)))
        guard case .connecting(0, _?) = d.link.state else { return XCTFail("\(d.link.state)") }
    }

    func testPreferringAnAddressPutsItFirstAndForgetsTheWinners() {
        var d = LinkDriver(addresses: [lan, tail])
        d.online(lan)
        let fx = d.send(.preferAddress(tail))
        XCTAssertEqual(d.link.addresses, [tail, lan])
        XCTAssertEqual(fx.prefix(2), [.rememberWinners([:]), .probe(address: tail, race: 2)])
        XCTAssertTrue(d.link.isUsable, "stays online while it tries")
        XCTAssertEqual(d.send(.preferAddress("nope:1")), [])
    }
}

extension ComputerLink.State {
    /// The outage a connecting or retrying state carries, for tests.
    var outageForTest: ComputerLink.Outage? {
        switch self {
        case let .connecting(_, outage): return outage
        case let .waiting(_, _, outage), let .offline(_, _, outage): return outage
        default: return nil
        }
    }
}
