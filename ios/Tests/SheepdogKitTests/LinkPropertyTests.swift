import XCTest
@testable import SheepdogKit

/// Random interleavings of network results, timers, path changes, the app
/// phase, demand and taps, from a seeded generator. A fake runtime runs the
/// effects (probes in flight, timers, channels) and every step checks the
/// invariants. A failure names its seed so it can be replayed.
final class LinkPropertyTests: XCTestCase {
    private struct Probe: Hashable {
        var address: String
        var race: Int
    }

    /// What the runtime would hold after running every effect so far.
    private struct FakeRuntime {
        var probes: Set<Probe> = []
        var timers: [LinkTimer.Kind: (timer: LinkTimer, at: Date)] = [:]
        var channels: Set<LinkChannel> = []
        var address: String?
        var lastRace = 0

        mutating func run(_ effects: [LinkEffect], now: Date, cap: TimeInterval, fail: (String) -> Void) {
            for effect in effects {
                switch effect {
                case let .probe(address, race):
                    if probes.contains(Probe(address: address, race: race)) { fail("probed \(address) twice in race \(race)") }
                    probes.insert(Probe(address: address, race: race))
                    lastRace = max(lastRace, race)
                case let .cancelProbe(address, race):
                    if probes.remove(Probe(address: address, race: race)) == nil { fail("cancelled a probe not in flight: \(address) \(race)") }
                case let .setTimer(timer, at):
                    if at < now { fail("a timer in the past: \(timer)") }
                    if timer.kind == .backoff, at.timeIntervalSince(now) > cap + 1e-9 {
                        fail("backoff \(at.timeIntervalSince(now)) s over the cap \(cap)")
                    }
                    timers[timer.kind] = (timer, at)
                case let .cancelTimer(kind):
                    if timers.removeValue(forKey: kind) == nil { fail("cancelled a timer that was not set: \(kind)") }
                case let .useAddress(address):
                    self.address = address
                case let .channels(channels):
                    self.channels = channels
                case .reopen, .rememberWinners, .renewTicket:
                    break
                }
            }
        }
    }

    private let pool = ["lan:7777", "tail:7777", "fwd:443"]

    func testRandomEventSequencesKeepTheInvariants() {
        for seed in UInt64(1)...400 {
            run(seed: seed, steps: 400)
        }
    }

    private func run(seed: UInt64, steps: Int) {
        var rng = SeededGenerator(seed: seed)
        var jitter = SeededGenerator(seed: seed &+ 1_000)
        var now = Date(timeIntervalSinceReferenceDate: 50_000)
        var link = ComputerLink(addresses: pool, certExpires: Bool.random(using: &rng) ? now.addingTimeInterval(86_400) : nil, demand: LinkDemand(phase: .background), path: NetworkPath(satisfied: true, interface: .wifi))
        var runtime = FakeRuntime()
        var trace: [String] = []
        var failed = false

        func fail(_ message: String) {
            guard !failed else { return }
            failed = true
            XCTFail("seed \(seed), step \(trace.count): \(message)\n" + trace.suffix(12).joined(separator: "\n"))
        }

        func send(_ event: LinkEvent) {
            trace.append("\(event) -> ")
            let cap = link.demand.backoffCap
            let effects = link.apply(event, now: now, using: &jitter)
            trace[trace.count - 1] += "\(link.state) \(effects)"
            // The cap of the demand in force after the event.
            runtime.run(effects, now: now, cap: max(cap, link.demand.backoffCap), fail: fail)
            check()
        }

        func check() {
            // One race at a time: every probe in flight belongs to it.
            let expected = Set((link.race?.inFlight ?? []).map { Probe(address: $0, race: link.race!.id) })
            if runtime.probes != expected { fail("probes in flight \(runtime.probes) != race \(expected)") }
            if Set(runtime.timers.keys) != Set(link.timers.keys) || runtime.timers.contains(where: { link.timers[$0.key] != $0.value.timer }) {
                fail("timers \(runtime.timers.mapValues(\.timer)) != link \(link.timers)")
            }
            if runtime.channels != link.channels { fail("channels \(runtime.channels) != \(link.channels)") }
            if !link.channels.isEmpty && !link.isUsable { fail("channels open while \(link.state)") }
            if !link.demand.needed {
                switch link.state {
                case .idle, .refused: break
                default: fail("no demand but \(link.state)")
                }
            }
            if link.demand.needed && !link.path.satisfied {
                switch link.state {
                case .noNetwork, .refused, .incompatible: break
                default: fail("no network but \(link.state)")
                }
            }
            switch link.state {
            case .idle, .noNetwork, .untrustedAnswer, .refused, .incompatible:
                if link.race != nil { fail("a race while \(link.state)") }
                if !link.timers.isEmpty { fail("timers \(link.timers) while \(link.state)") }
            case .connecting:
                if link.race == nil { fail("connecting without a race") }
                if link.timers[.backoff] != nil { fail("a backoff timer while connecting") }
            case let .waiting(retryAt, attempt, outage), let .offline(retryAt, attempt, outage):
                if link.race != nil { fail("a race while backing off") }
                if runtime.timers[.backoff]?.at != retryAt { fail("the backoff timer is not at retryAt") }
                if attempt < 1 { fail("waiting with attempt \(attempt)") }
                if case .offline = link.state, !outage.long { fail("offline but not long") }
                if case .waiting = link.state, outage.long { fail("waiting but long") }
                if retryAt.timeIntervalSince(now) > LinkBackoff.otherComputerCap + 1e-9 { fail("retry too far out") }
            case .online, .degraded:
                if link.timers[.refresh] == nil { fail("online without the safety-net refresh") }
                if runtime.address != link.onlineAddress { fail("runtime on \(runtime.address ?? "-") but online on \(link.onlineAddress ?? "-")") }
                if LinkPresentation(link).tint != .green { fail("usable but not green") }
            }
            _ = LinkPresentation(link, name: "Home Mac", voiceOn: true)
        }

        func randomError() -> LinkError {
            switch Int.random(in: 0..<20, using: &rng) {
            case 0..<11: return .transport
            case 11..<14: return .tlsUntrusted
            case 14: return .unauthorized(nil)
            case 15: return .unauthorized(.revoked)
            case 16: return .incompatible("update")
            case 17: return .httpOther(500)
            case 18: return .decode
            default: return .cancelled
            }
        }

        for _ in 0..<steps where !failed {
            switch Int.random(in: 0..<100, using: &rng) {
            case 0..<30:
                // A probe in flight answers.
                guard let probe = runtime.probes.randomElement(using: &rng) else { continue }
                if Int.random(in: 0..<3, using: &rng) == 0 {
                    runtime.probes.remove(probe)
                    send(.probeSucceeded(address: probe.address, race: probe.race, rtt: Double.random(in: 0...3, using: &rng)))
                } else {
                    let error = randomError()
                    if error != .cancelled { runtime.probes.remove(probe) }
                    send(.probeFailed(address: probe.address, race: probe.race, error: error))
                }
            case 30..<33:
                // A late answer from an older race.
                let address = pool.randomElement(using: &rng)!
                send(.probeSucceeded(address: address, race: Int.random(in: 0...max(0, runtime.lastRace - 1), using: &rng), rtt: 0.1))
            case 33..<53:
                // The earliest timer fires.
                guard let next = runtime.timers.values.min(by: { $0.at < $1.at }) else { continue }
                now = max(now, next.at)
                runtime.timers[next.timer.kind] = nil
                send(.timerFired(next.timer))
            case 53..<55:
                send(.timerFired(LinkTimer(kind: LinkTimer.Kind.allCases.randomElement(using: &rng)!, token: 0)))
            case 55..<62:
                now = now.addingTimeInterval(Double.random(in: 0...5, using: &rng))
            case 62..<67:
                let satisfied = Int.random(in: 0..<4, using: &rng) > 0
                send(.pathChanged(NetworkPath(satisfied: satisfied, interface: NetworkInterface.allCases.randomElement(using: &rng), constrained: Bool.random(using: &rng))))
            case 67..<72:
                send(.appPhase(Bool.random(using: &rng) ? .active : .background))
            case 72..<75:
                var demand = link.demand
                demand.isCurrent = Bool.random(using: &rng)
                demand.voiceOn = Bool.random(using: &rng)
                demand.keepListening = Bool.random(using: &rng)
                demand.actionsInFlight = Int.random(in: 0..<4, using: &rng) == 0
                demand.voiceOnAnotherComputer = Bool.random(using: &rng)
                send(.demandChanged(demand))
            case 75..<81:
                let channel = LinkChannel.allCases.randomElement(using: &rng)!
                send(Bool.random(using: &rng) ? .channelLost(channel, randomError()) : .channelOpened(channel))
            case 81..<86:
                send(Bool.random(using: &rng) ? .requestSucceeded(rtt: Double.random(in: 0...3, using: &rng)) : .requestFailed(randomError()))
            case 86..<92:
                send(.retryTapped)
            case 92..<95:
                var addresses = pool.filter { _ in Int.random(in: 0..<3, using: &rng) > 0 }
                if addresses.isEmpty { addresses = [pool[0]] }
                send(.addressesChanged(addresses))
            case 95..<97:
                send(.preferAddress(pool.randomElement(using: &rng)!))
            case 97:
                send(.credentialsChanged(.newPairing(certExpires: nil)))
            default:
                send(.credentialsChanged(.pinPromoted(certExpires: now.addingTimeInterval(365 * 86_400))))
            }
        }
    }

    /// Many events at the same instant: never two races, never a probe
    /// started twice, and the state settles to one of the defined states.
    func testABurstOfKicksAtOneInstantStartsOneRace() {
        var d = LinkDriver(addresses: pool)
        d.send(.demandChanged(LinkDemand()))
        var probes = 0
        for _ in 0..<50 {
            for effect in d.send(.retryTapped) + d.send(.appPhase(.active)) + d.send(.channelLost(.events, .transport)) {
                if case .probe = effect { probes += 1 }
            }
        }
        XCTAssertEqual(probes, 0, "kicks while connecting do nothing")
        XCTAssertEqual(d.link.race?.id, 1)
    }
}
