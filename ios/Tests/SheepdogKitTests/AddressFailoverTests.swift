import XCTest
@testable import SheepdogKit

/// Phase 6, address failover (docs/dev/design/ios-state-architecture.md
/// 3.3, 3.4, decisions 1 and 10): the staggered, pinned race across every
/// address of a computer, the winners per interface, Prefer, and every
/// channel on the address in use. A fake network and a manual clock.
@MainActor
final class AddressFailoverTests: XCTestCase {
    private let lan = "192.168.1.20:7777"
    private let tail = "100.101.102.103:7777"
    private let forward = "203.0.113.9:7777"
    private let wifi = NetworkPath(satisfied: true, interface: .wifi)
    private let cellular = NetworkPath(satisfied: true, interface: .cellular)
    private var clock: ManualLinkClock!
    private var net: FakeLinkTransport!
    private var store: MemoryWinnerStore!

    override func setUp() async throws {
        clock = ManualLinkClock()
        net = FakeLinkTransport()
        store = MemoryWinnerStore()
    }

    private func runtime(_ addresses: [String], path: NetworkPath, certExpires: Date? = nil) -> LinkRuntime {
        let link = ComputerLink(addresses: addresses, certExpires: certExpires, demand: LinkDemand(phase: .background), path: path)
        return LinkRuntime(computerID: "mac", link: link, transport: net, scheduler: clock, winners: store, random: LinkRandom(seed: 3))
    }

    private static let ok = LinkProbeResult.success(rtt: 0.05, state: nil)

    // MARK: - The race

    func testTailscaleDownAtHomeTheLANAddressWinsAfterTheStagger() {
        // Tailscale is preferred, but its daemon is down: it hangs.
        net.plans[tail] = .hang
        net.plans[lan] = .hang
        let r = runtime([tail, lan], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(net.probed, [tail], "the preferred address first, alone")
        clock.advance(0.29)
        XCTAssertEqual(net.probed, [tail])
        clock.advance(0.01)
        XCTAssertEqual(net.probed, [tail, lan], "the next one after 300 ms")
        net.answer(lan, Self.ok)
        XCTAssertEqual(r.link.onlineAddress, lan)
        XCTAssertEqual(net.cancelled, [tail], "the loser is cancelled")
        XCTAssertEqual(net.openProbes, [])
        XCTAssertEqual(store.saved["mac"], [.wifi: lan], "remembered for Wi-Fi")
        let rows = AddressRow.rows(addresses: [tail, lan], preferred: tail, link: r.link)
        XCTAssertEqual(rows.map(\.statusText), [nil, "In use on Wi-Fi"])
        XCTAssertEqual(rows.map(\.isPreferred), [true, false])
    }

    func testOnCellularTheTailscaleAddressWinsAndBackHomeTheLANGoesFirst() {
        net.plans[tail] = .answer(.failure(.transport))
        let r = runtime([tail, lan], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.onlineAddress, lan)

        // Leaving home: the LAN address is gone, Tailscale answers.
        net.plans[tail] = .answer(Self.ok)
        net.plans[lan] = .answer(.failure(.transport))
        r.send(.pathChanged(cellular))
        XCTAssertEqual(r.link.onlineAddress, tail)
        XCTAssertEqual(store.saved["mac"], [.wifi: lan, .cellular: tail])
        XCTAssertEqual(AddressRow.rows(addresses: [tail, lan], preferred: tail, link: r.link).first?.statusText, "In use on cellular")

        // Home again: the Wi-Fi winner is tried first, before the preferred.
        net.plans[lan] = .hang
        let before = net.probed.count
        r.send(.pathChanged(wifi))
        XCTAssertEqual(Array(net.probed[before...]), [lan])
        net.answer(lan, Self.ok)
        XCTAssertEqual(r.link.onlineAddress, lan)
        XCTAssertTrue(r.link.isUsable, "online throughout the re-race")
    }

    func testAMismatchOnTheAddressInUseTriesTheOthersBeforeWarning() {
        let r = runtime([lan, tail], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.onlineAddress, lan)
        // A hostile network now answers on the LAN address with another key.
        net.plans[lan] = .answer(.failure(.tlsUntrusted))
        r.send(.requestFailed(.tlsUntrusted))
        XCTAssertEqual(r.link.onlineAddress, tail, "another address verified: online there (decision 1)")
        XCTAssertEqual(r.link.mismatched, [lan])
        let rows = AddressRow.rows(addresses: [lan, tail], preferred: lan, link: r.link)
        XCTAssertEqual(rows.map(\.statusText), ["Answered with another key", "In use on Wi-Fi"])
    }

    func testEveryAddressUnreachableBacksOff() {
        net.fallback = .answer(.failure(.transport))
        let r = runtime([tail, lan], path: wifi)
        r.send(.appPhase(.active))
        guard case .waiting = r.link.state else { return XCTFail("\(r.link.state)") }
        XCTAssertEqual(Set(net.probed), [tail, lan], "both tried")
        XCTAssertNil(store.saved["mac"])
    }

    func testEveryAddressMismatchedIsTheUntrustedWarningWithoutARescan() {
        net.fallback = .answer(.failure(.tlsUntrusted))
        let r = runtime([tail, lan], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.state, .untrustedAnswer(addresses: [tail, lan]))
        XCTAssertEqual(clock.pending, 0, "nothing more is sent to that network")
    }

    func testOneMismatchAndOneUnreachableIsStillTheWarning() {
        net.plans[tail] = .answer(.failure(.transport))
        net.plans[lan] = .answer(.failure(.tlsUntrusted))
        let r = runtime([tail, lan], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.state, .untrustedAnswer(addresses: [lan]))
    }

    func testEveryAddressMismatchedInsideTheRotationWindowAsksForAScan() {
        net.fallback = .answer(.failure(.tlsUntrusted))
        let expires = clock.now.addingTimeInterval(24 * 60 * 60)
        let r = runtime([tail, lan], path: wifi, certExpires: expires)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.state, .refused(.renewedWhileAway))
    }

    // MARK: - Prefer

    func testPreferMovesTheLinkAndForgetsTheWinners() {
        let r = runtime([lan, tail], path: wifi)
        r.send(.appPhase(.active))
        XCTAssertEqual(r.link.onlineAddress, lan)
        r.send(.pathChanged(cellular))
        XCTAssertEqual(store.saved["mac"]?[.wifi], lan)
        r.send(.preferAddress(tail))
        XCTAssertEqual(r.link.onlineAddress, tail)
        XCTAssertEqual(r.link.addresses.first, tail)
        XCTAssertEqual(store.saved["mac"], [.cellular: tail], "the old winners are forgotten; the new one remembered")
    }

    func testPreferringADeadAddressStaysOnlineWhereItWas() {
        let r = runtime([lan, tail], path: wifi)
        r.send(.appPhase(.active))
        net.plans[tail] = .answer(.failure(.transport))
        r.send(.preferAddress(tail))
        XCTAssertEqual(r.link.onlineAddress, lan)
        XCTAssertTrue(r.link.isUsable)
    }

    // MARK: - Winners, persisted per computer

    func testWinnersPersistPerInterfaceInUserDefaultsAndGoWithTheComputer() throws {
        let suite = "AddressFailoverTests.\(UUID().uuidString)"
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let winners = LinkWinnerDefaults(defaults: defaults)
        winners.save([.wifi: lan, .cellular: tail], computerID: "mac")
        winners.save([.wifi: forward], computerID: "other")

        net.plans[lan] = .hang
        let link = ComputerLink(addresses: [tail, lan], demand: LinkDemand(phase: .background), path: wifi)
        let r = LinkRuntime(computerID: "mac", link: link, transport: net, scheduler: clock, winners: winners)
        r.send(.appPhase(.active))
        XCTAssertEqual(net.probed, [lan], "this computer's Wi-Fi winner first")

        let computers = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        computers.remove("mac")
        XCTAssertEqual(winners.load(computerID: "mac"), [:], "forgetting a computer forgets its winners")
        XCTAssertEqual(winners.load(computerID: "other"), [.wifi: forward])
    }

    // MARK: - One race at a time, losers cancelled

    func testPropertyOneRaceAtATimeAndEveryLoserCancelled() {
        let addresses = [lan, tail, forward]
        for seed in UInt64(1) ... 150 {
            var rng = LinkRandom(seed: seed)
            clock = ManualLinkClock()
            net = FakeLinkTransport()
            net.fallback = .hang
            store = MemoryWinnerStore()
            let r = runtime(addresses, path: wifi)
            r.send(.appPhase(.active))
            var trace: [String] = []
            for _ in 0 ..< 120 {
                let roll = Int.random(in: 0 ..< 100, using: &rng)
                switch roll {
                case 0 ..< 35:
                    // Answer a probe in flight: a pinned success, or a failure.
                    if let address = net.openProbes.randomElement(using: &rng) {
                        let results: [LinkProbeResult] = [Self.ok, .failure(.transport), .failure(.tlsUntrusted), Self.ok]
                        let result = results.randomElement(using: &rng)!
                        trace.append("answer \(address) \(result)")
                        net.answer(address, result)
                    }
                case 35 ..< 60:
                    let seconds = [0.1, 0.3, 1, 4, 30].randomElement(using: &rng)!
                    trace.append("advance \(seconds)")
                    clock.advance(seconds)
                case 60 ..< 70:
                    let path = [wifi, cellular, NetworkPath(satisfied: true, interface: .wired)].randomElement(using: &rng)!
                    trace.append("path \(String(describing: path.interface))")
                    r.send(.pathChanged(path))
                case 70 ..< 78:
                    let address = addresses.randomElement(using: &rng)!
                    trace.append("prefer \(address)")
                    r.send(.preferAddress(address))
                case 78 ..< 86:
                    trace.append("retry")
                    r.send(.retryTapped)
                case 86 ..< 93:
                    trace.append("request failed")
                    r.send(.requestFailed([LinkError.transport, .tlsUntrusted].randomElement(using: &rng)!))
                default:
                    let phase: AppPhase = Bool.random(using: &rng) ? .active : .background
                    trace.append("phase \(phase)")
                    r.send(.appPhase(phase))
                }
                // Every probe still open on the network is the current race's,
                // and the race's are exactly those: one race at a time, and a
                // decided race (won or lost) leaves nothing running.
                let inRace = (r.link.race?.inFlight ?? []).sorted()
                if net.openProbes != inRace || r.probesInFlight != inRace {
                    return XCTFail("seed \(seed): open \(net.openProbes), runtime \(r.probesInFlight), race \(inRace)\n"
                        + trace.suffix(10).joined(separator: "\n"))
                }
                if r.link.isUsable, let address = r.link.onlineAddress, !addresses.contains(address) {
                    return XCTFail("seed \(seed): online on an unknown address \(address)")
                }
            }
        }
    }
}

/// The same race through a computer's runtime (`ComputerModel` +
/// `ComputerConnection`): requests, `/events`, `/ws` and `/term` all go to
/// the address the link uses.
@MainActor
final class AddressFailoverRuntimeTests: XCTestCase {
    private let lan = "lan"
    private let tail = "tail"
    private var network: FakeComputerNetwork!
    private var clock: ManualLinkClock!

    override func setUp() async throws {
        network = FakeComputerNetwork()
        clock = ManualLinkClock()
    }

    private func makeModel(_ addresses: [String]) -> ComputerModel {
        let network = network!
        let connection = ComputerConnection(computerID: "mac", address: addresses.first, makeRequests: { network.makeRequests($0) })
        return ComputerModel(id: "mac", name: "Mac", addresses: addresses, certExpires: nil, connection: connection, scheduler: clock,
                             demand: LinkDemand(isCurrent: true, voiceOn: true, keepListening: true),
                             path: NetworkPath(satisfied: true, interface: .wifi), random: LinkRandom(seed: 5))
    }

    func testThePreferredAddressIsDeadEveryChannelGoesToTheOther() async throws {
        network.setFailure(URLError(.cannotConnectToHost), for: tail)
        let model = makeModel([tail, lan])
        let ws = LinkChannelRuntimeTests.FakeChannel()
        let term = LinkChannelRuntimeTests.FakeChannel()
        model.attach(.ws, ws)
        model.attach(.term, term)
        model.start()
        await eventually("online on the LAN") { model.link.onlineAddress == self.lan }
        await eventually("the stream opens there") { self.network.openStreams(self.lan) == 1 }
        XCTAssertEqual(network.eventsOpened(tail), 0)
        XCTAssertEqual(model.agents.map(\.alias), ["lan#0"], "the state came from the winner")
        XCTAssertEqual(ws.openedAt, [lan], "/ws opened on the winner, not the preferred address")
        XCTAssertEqual(term.openedAt, [lan])

        // A request goes there too.
        let calls = network.stateCalls(lan)
        _ = try await model.request(as: ComputerRequests.self) { try await $0.state() }
        XCTAssertEqual(network.stateCalls(lan), calls + 1)
    }

    func testPreferMovesEveryChannelWithoutANewEpoch() async throws {
        let model = makeModel([lan, tail])
        let ws = LinkChannelRuntimeTests.FakeChannel()
        model.attach(.ws, ws)
        model.start()
        await eventually("online on the LAN") { model.link.onlineAddress == self.lan && self.network.openStreams(self.lan) == 1 }
        let epoch = model.epoch
        model.prefer(tail)
        await eventually("moved to Tailscale") { model.link.onlineAddress == self.tail }
        await eventually("the stream and /ws reopened there") { self.network.openStreams(self.tail) == 1 && ws.openedAt.last == self.tail }
        XCTAssertEqual(ws.address, tail)
        XCTAssertEqual(model.epoch, epoch, "the same computer: no new epoch")
        XCTAssertTrue(model.link.isUsable)
    }

    func testAnAddedAddressIsRacedWithoutANewEpoch() async {
        network.setFailure(URLError(.cannotConnectToHost), for: lan)
        let model = makeModel([lan])
        model.start()
        await eventually("backing off") {
            if case .waiting = model.link.state { return true }
            return false
        }
        let epoch = model.epoch
        model.setAddresses([lan, tail])
        await eventually("online on the added address") { model.link.onlineAddress == self.tail }
        XCTAssertEqual(model.epoch, epoch)
    }
}
