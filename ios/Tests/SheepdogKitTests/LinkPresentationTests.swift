import XCTest
@testable import SheepdogKit

/// The copy every status surface shows for each link state
/// (docs/dev/design/ios-state-architecture.md 3.2 "What the UI shows").
final class LinkPresentationTests: XCTestCase {
    private let lan = "lan:7777"
    private func time(_ date: Date) -> String { "9:12" }

    private func present(_ d: LinkDriver, name: String? = nil, voiceOn: Bool = false) -> LinkPresentation {
        LinkPresentation(d.link, name: name, voiceOn: voiceOn, time: time)
    }

    private func waiting() -> LinkDriver {
        var d = LinkDriver()
        d.online()
        d.send(.channelLost(.events, .transport))
        d.fail(lan)
        return d
    }

    private func offline() -> LinkDriver {
        var d = waiting()
        d.fire(.quiet)
        return d
    }

    func testIdle() {
        let p = present(LinkDriver(demand: LinkDemand(phase: .background)))
        XCTAssertEqual(p.chip, "Not connected")
        XCTAssertEqual(p.tint, .grey)
    }

    func testAFirstConnectDoesNotActOnCachedQuestions() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        let p = present(d)
        XCTAssertEqual(p.chip, "Connecting…")
        XCTAssertEqual(p.tint, .amber)
        XCTAssertTrue(p.isBusy)
        XCTAssertEqual(p.data, .lastKnown)
        XCTAssertEqual(p.answers, .disabled("Connecting…"))
        XCTAssertEqual(p.composer, .enabled)
        XCTAssertNil(p.inline)
    }

    func testOnlineAndTalkingHere() {
        var d = LinkDriver()
        d.online()
        XCTAssertEqual(present(d).chip, "Connected")
        XCTAssertEqual(present(d).tint, .green)
        XCTAssertEqual(present(d).statusLine, "Connected")
        XCTAssertEqual(present(d, voiceOn: true).statusLine, "Talking here · lan:7777")
        XCTAssertEqual(present(d).data, .live)
        XCTAssertEqual(present(d).answers, .enabled)
        XCTAssertNil(present(d).health)
    }

    func testDegradedShowsConnectedAndSaysItOnlyInHealth() {
        var d = LinkDriver()
        d.online()
        d.send(.channelLost(.events, .transport))
        d.succeed(lan)
        let p = present(d)
        XCTAssertEqual(p.chip, "Connected")
        XCTAssertEqual(p.tint, .green)
        XCTAssertEqual(p.health, "Live updates are reconnecting")
        XCTAssertNil(p.inline)
    }

    func testABlipIsReconnectingWithTheDataUntouched() {
        let d = waiting()
        let p = present(d)
        XCTAssertEqual(p.chip, "Reconnecting…")
        XCTAssertEqual(p.tint, .amber)
        XCTAssertEqual(p.data, .lastKnown)
        XCTAssertEqual(p.answers, .enabled)
        XCTAssertEqual(p.composer, .enabled)
        XCTAssertNil(p.inline)

        // The retry after a blip says Reconnecting, not Connecting.
        var retry = d
        retry.fire(.backoff)
        XCTAssertEqual(present(retry).chip, "Reconnecting…")
    }

    func testOffline() {
        let p = present(offline())
        XCTAssertEqual(p.chip, "Offline")
        XCTAssertEqual(p.tint, .red)
        XCTAssertEqual(p.inline, LinkPresentation.Inline(message: "Can't reach your computer. Last seen 9:12. Is it awake, and is this phone on a network that reaches it?", actions: [.tryNow]))
        XCTAssertEqual(p.data, .dimmed(asOf: "9:12"))
        XCTAssertEqual(p.answers, .disabled("Reconnect to answer"))
        XCTAssertEqual(p.composer, .disabled("Reconnect to send"))
        XCTAssertEqual(p.statusLine, "Offline since 9:12")
    }

    func testOfflineWithNoLastSeenLeavesThatSentenceOut() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan)
        d.fire(.quiet)
        XCTAssertEqual(present(d).inline?.message, "Can't reach your computer. Is it awake, and is this phone on a network that reaches it?")
        XCTAssertEqual(present(d).data, .dimmed(asOf: nil))
    }

    func testSeveralComputersUseTheName() {
        let p = present(offline(), name: "Home Mac")
        XCTAssertEqual(p.statusLine, "Home Mac · Offline since 9:12")
        XCTAssertTrue(p.inline!.message.hasPrefix("Can't reach Home Mac."))
        XCTAssertFalse(present(offline()).inline!.message.contains("Home Mac"), "one computer: no names")
    }

    func testNoNetwork() {
        var d = LinkDriver()
        d.online()
        d.send(.pathChanged(NetworkPath(satisfied: false)))
        let p = present(d)
        XCTAssertEqual(p.chip, "No network")
        XCTAssertEqual(p.tint, .grey)
        XCTAssertEqual(p.inline, LinkPresentation.Inline(message: "This phone is offline. Sheepdog reconnects when the network is back.", actions: []))
        XCTAssertEqual(p.data, .dimmed(asOf: "9:12"))
        XCTAssertEqual(p.answers, .disabled("Reconnect to answer"))
    }

    func testUntrustedAnswer() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .tlsUntrusted)
        let p = present(d)
        XCTAssertEqual(p.chip, "Not verified")
        XCTAssertEqual(p.tint, .red)
        XCTAssertEqual(p.inline, LinkPresentation.Inline(message: "Something on this network answered instead of your computer, so Sheepdog didn't connect.", actions: [.tryAgain, .useAnotherAddress]))
        XCTAssertEqual(p.answers, .hidden)
        XCTAssertEqual(present(d, name: "Home Mac").inline?.message, "Something on this network answered instead of Home Mac, so Sheepdog didn't connect.")
    }

    func testRefusedUsesThePairingProblemsOwnWords() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .unauthorized(.revoked))
        let p = present(d)
        XCTAssertEqual(p.chip, "Scan again")
        XCTAssertEqual(p.tint, .red)
        XCTAssertEqual(p.inline, LinkPresentation.Inline(message: PairingProblem.revoked.message, actions: [.scan]))
        XCTAssertEqual(p.data, .cleared)
        XCTAssertEqual(p.answers, .hidden)
    }

    func testIncompatibleShowsTheHandshakeMessage() {
        var d = LinkDriver()
        d.send(.demandChanged(LinkDemand()))
        d.fail(lan, .incompatible("Update the app."))
        let p = present(d)
        XCTAssertEqual(p.chip, "Update needed")
        XCTAssertEqual(p.tint, .red)
        XCTAssertEqual(p.inline?.message, "Update the app.")
        XCTAssertEqual(p.data, .lastKnown)
    }

    func testOnlyUsableLinksAreGreen() {
        var d = LinkDriver()
        d.online()
        XCTAssertEqual(present(d).tint, .green)
        XCTAssertNotEqual(present(waiting()).tint, .green)
        XCTAssertNotEqual(present(offline()).tint, .green)
    }
}

/// The demand table and the race on their own.
final class LinkDemandAndRaceTests: XCTestCase {
    func testDemand() {
        XCTAssertTrue(LinkDemand(phase: .active).needed)
        XCTAssertTrue(LinkDemand(isCurrent: false, phase: .active).needed, "other computers in the foreground")
        XCTAssertTrue(LinkDemand(isCurrent: true, phase: .background, voiceOn: true, keepListening: true).needed)
        XCTAssertFalse(LinkDemand(isCurrent: true, phase: .background, voiceOn: true, keepListening: false).needed, "the core suspends")
        XCTAssertFalse(LinkDemand(isCurrent: true, phase: .background, voiceOn: false).needed)
        XCTAssertFalse(LinkDemand(isCurrent: false, phase: .background, voiceOn: true).needed)
        XCTAssertTrue(LinkDemand(isCurrent: false, phase: .background, actionsInFlight: true).needed)
    }

    func testChannels() {
        let path = NetworkPath()
        let constrained = NetworkPath(constrained: true)
        XCTAssertEqual(LinkDemand(voiceOn: true).channels(on: path), [.ws, .events, .term])
        XCTAssertEqual(LinkDemand(voiceOn: false).channels(on: path), [.events, .term])
        XCTAssertEqual(LinkDemand(isCurrent: false, voiceOn: true).channels(on: path), [.events, .term], "the voice is the current computer's")
        XCTAssertEqual(LinkDemand(isCurrent: false).channels(on: constrained), [.term], "Low Data Mode: no /events for others")
        XCTAssertEqual(LinkDemand(isCurrent: true).channels(on: constrained), [.events, .term])
        XCTAssertEqual(LinkDemand(phase: .background, voiceOn: true, keepListening: true).channels(on: path), [.ws])
        XCTAssertEqual(LinkDemand(phase: .background, actionsInFlight: true).channels(on: path), [])
    }

    func testBackoffCeilings() {
        XCTAssertEqual((0..<8).map { LinkBackoff.ceiling(attempt: $0, cap: 15) }, [0.5, 1, 2, 4, 8, 15, 15, 15])
        XCTAssertEqual(LinkBackoff.delay(attempt: 3, cap: 15, jitter: 0), 0)
        XCTAssertEqual(LinkBackoff.delay(attempt: 3, cap: 15, jitter: 1), 4)
        XCTAssertEqual(LinkBackoff.delay(attempt: 3, cap: 15, jitter: 7), 4, "the fraction is clamped")
    }

    func testRaceOrder() {
        XCTAssertEqual(AddressRace.order(addresses: ["a", "b", "c"], winners: [.cellular: "c"], interface: .cellular), ["c", "a", "b"])
        XCTAssertEqual(AddressRace.order(addresses: ["a", "b", "c"], winners: [.cellular: "c"], interface: .wifi), ["a", "b", "c"])
        XCTAssertEqual(AddressRace.order(addresses: ["a", "b"], winners: [.wifi: "gone"], interface: .wifi), ["a", "b"])
        XCTAssertEqual(AddressRace.order(addresses: ["a", "a"], winners: [:], interface: nil), ["a"])
    }

    func testRaceSteps() {
        var race = AddressRace(id: 4, order: ["a", "b", "c"])
        XCTAssertEqual(race.start(), ["a"])
        XCTAssertEqual(race.staggerElapsed(), ["b"])
        XCTAssertEqual(race.inFlight, ["a", "b"])
        XCTAssertNil(race.failed("c", .transport), "not started")
        XCTAssertEqual(race.failed("a", .transport), ["c"])
        XCTAssertFalse(race.hasMoreToStart)
        XCTAssertEqual(race.succeeded("c"), ["b"])
        XCTAssertEqual(race.winner, "c")
        XCTAssertNil(race.succeeded("b"), "the race is over")
        XCTAssertFalse(race.isLost)
    }

    func testVerdicts() {
        let now = Date(timeIntervalSinceReferenceDate: 0)
        let inWindow = now.addingTimeInterval(86_400)
        let outside = now.addingTimeInterval(300 * 86_400)
        func f(_ pairs: (String, LinkError)...) -> [AddressRace.Failure] { pairs.map { AddressRace.Failure(address: $0.0, error: $0.1) } }

        XCTAssertEqual(AddressRace.verdict(f(("a", .transport), ("b", .httpOther(502))), certExpires: nil, now: now), .unreachable(.transport, renewTicket: false))
        XCTAssertEqual(AddressRace.verdict(f(("a", .unauthorized(nil))), certExpires: nil, now: now), .unreachable(.unauthorized(nil), renewTicket: true))
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .unauthorized(.revoked))), certExpires: nil, now: now), .refused(.revoked), "a verified refusal outranks a mismatch")
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .incompatible("x"))), certExpires: nil, now: now), .incompatible("x"))
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .tlsUntrusted)), certExpires: outside, now: now), .untrusted(["a", "b"]))
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .tlsUntrusted)), certExpires: nil, now: now), .untrusted(["a", "b"]))
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .tlsUntrusted)), certExpires: inWindow, now: now), .refused(.renewedWhileAway))
        XCTAssertEqual(AddressRace.verdict(f(("a", .tlsUntrusted), ("b", .transport)), certExpires: inWindow, now: now), .untrusted(["a"]))
        XCTAssertEqual(AddressRace.verdict([], certExpires: nil, now: now), .unreachable(.transport, renewTicket: false))
    }
}
