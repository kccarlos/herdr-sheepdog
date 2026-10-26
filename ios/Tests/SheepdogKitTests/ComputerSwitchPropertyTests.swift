import XCTest
@testable import SheepdogKit

/// Fast computer switching with requests in flight
/// (docs/dev/design/ios-state-architecture.md 3.5, decision 6): seeded
/// interleavings of switches, requests on the current computer, answers,
/// re-pairing and replies in any order. A reply is applied only to the
/// computer it was asked of, and only while that computer is still
/// current; an answer always finishes on its own computer.
@MainActor
final class ComputerSwitchPropertyTests: XCTestCase {
    static let seeds: UInt64 = 40
    static let steps = 50

    func testNoReplyIsAppliedToTheWrongComputer() async {
        for seed in 1 ... Self.seeds {
            await run(seed: seed)
        }
    }

    private func run(seed: UInt64) async {
        var random = LinkRandom(seed: seed)
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let ids = ["home", "work", "studio"]
        for id in ids {
            network.questions[id] = [PendingAction(id: "q-\(id)", paneID: "\(id):p1", kind: "question", question: "Go?",
                                                   options: [PendingOption(index: 1, label: "Yes")])]
        }
        let fleet = ComputerFleet()
        var models: [ComputerModel] = []
        for id in ids {
            let connection = ComputerConnection(computerID: id, address: id, makeRequests: { network.makeRequests($0) }, sleep: { _ in })
            models.append(ComputerModel(id: id, name: id, addresses: [id], certExpires: nil, connection: connection, scheduler: clock,
                                        demand: LinkDemand(isCurrent: id == "home"), random: LinkRandom(seed: seed)))
        }
        fleet.set(models)
        fleet.makeCurrent("home")
        for model in models { model.start() }
        // What the "session" applied: (computer asked, computer current when applied, reply's computer).
        var applied: [(asked: String, current: String?, reply: String)] = []
        var trace: [String] = []

        func setDemands() {
            for model in fleet.models { model.setDemand(LinkDemand(isCurrent: model === fleet.current)) }
        }

        for step in 0 ..< Self.steps {
            let id = ids[Int(random.next() % UInt64(ids.count))]
            switch random.next() % 10 {
            case 0, 1:
                trace.append("switch \(id)")
                fleet.makeCurrent(id)
                setDemands()
            case 2, 3:
                guard let asked = fleet.current?.id else { break }
                trace.append("request on \(asked)")
                Task { @MainActor in
                    do {
                        let state = try await fleet.onCurrent(as: FakeComputerClient.self) { try await $0.state() }
                        applied.append((asked, fleet.current?.id, String(state.agents[0].alias.prefix { $0 != "#" })))
                    } catch {}
                }
            case 4:
                let model = fleet.model(id)!
                trace.append("answer on \(id)")
                model.answer(question: "q-\(id)", option: 1)
            case 5, 6, 7:
                let pending = network.pending
                if !pending.isEmpty {
                    let pick = pending[Int(random.next() % UInt64(pending.count))]
                    trace.append("resolve \(pick.address) \(pick.kind)")
                    network.resolve(pick.id)
                }
            case 8:
                let hang = random.next() % 3 != 0
                trace.append("\(hang ? "hang" : "answer at once") \(id)")
                network.setMode(hang ? .hang : .answer, for: id)
            default:
                trace.append("repair \(id)")
                network.bump(id)
                fleet.model(id)!.renew(addresses: [id], certExpires: nil)
            }
            await settle(3)
            for entry in applied {
                XCTAssertEqual(entry.reply, entry.asked, "seed \(seed) step \(step): a reply from \(entry.reply) applied for \(entry.asked); \(trace.suffix(6))")
                XCTAssertEqual(entry.current, entry.asked, "seed \(seed) step \(step): applied after switching away; \(trace.suffix(6))")
            }
            for model in fleet.models {
                for (_, phase) in model.phases {
                    if case let .answered(_, spoken?) = phase {
                        XCTAssertTrue(spoken.hasPrefix("\(model.id)#"), "seed \(seed) step \(step): \(spoken) on \(model.id)")
                    }
                }
            }
        }
        // Every reply comes in: no answer is left sending, wherever the app
        // switched meanwhile.
        for id in ids { network.setMode(.answer, for: id) }
        for _ in 0 ..< 5 {
            for p in network.pending { network.resolve(p.id) }
            await settle(3)
        }
        for model in fleet.models {
            if case .sending = model.phase("q-\(model.id)") {
                XCTFail("seed \(seed): the answer on \(model.id) never finished; \(trace.suffix(8))")
            }
        }
        for model in models { model.stop() }
        await settle(2)
    }
}

/// The fleet itself.
@MainActor
final class ComputerFleetTests: XCTestCase {
    func testSwitchingChangesOneIDAndKeepsEveryRuntime() async {
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let fleet = ComputerFleet()
        let models = ["home", "work"].map { id in
            ComputerModel(id: id, name: id, addresses: [id], certExpires: nil,
                          connection: ComputerConnection(computerID: id, address: id, makeRequests: { network.makeRequests($0) }, sleep: { _ in }),
                          scheduler: clock, demand: LinkDemand(isCurrent: id == "home"))
        }
        fleet.set(models)
        XCTAssertNil(fleet.current)
        XCTAssertEqual(fleet.makeCurrent("home").changed, true)
        XCTAssertEqual(fleet.current?.id, "home")
        XCTAssertEqual(fleet.others.map(\.id), ["work"])
        XCTAssertEqual(fleet.makeCurrent("home").changed, false, "the same computer again changes nothing")
        let switched = fleet.makeCurrent("work")
        XCTAssertTrue(switched.changed)
        XCTAssertTrue(switched.previous === models[0], "the previous runtime is handed back, still running")
        XCTAssertEqual(fleet.makeCurrent("nowhere").previous?.id, "work")
        XCTAssertNil(fleet.current, "an unknown id is no current computer")
        // A removed model stops, and stops being current.
        fleet.makeCurrent("home")
        fleet.set([models[1]])
        XCTAssertNil(fleet.current)
        XCTAssertEqual(fleet.models.map(\.id), ["work"])
    }

    func testAReplyAfterASwitchIsNotReturned() async {
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let fleet = ComputerFleet()
        let models = ["home", "work"].map { id in
            ComputerModel(id: id, name: id, addresses: [id], certExpires: nil,
                          connection: ComputerConnection(computerID: id, address: id, makeRequests: { network.makeRequests($0) }, sleep: { _ in }),
                          scheduler: clock, demand: LinkDemand(isCurrent: id == "home"))
        }
        fleet.set(models)
        fleet.makeCurrent("home")
        network.setMode(.hang, for: "home")
        var outcome: String?
        Task {
            do {
                _ = try await fleet.onCurrent(as: FakeComputerClient.self) { try await $0.state() }
                outcome = "returned"
            } catch is ComputerFleet.Moved {
                outcome = "moved"
            } catch {
                outcome = "\(error)"
            }
        }
        await eventually("in flight") { network.pending.count == 1 }
        fleet.makeCurrent("work")
        network.resolve(network.pending[0].id)
        await eventually("finished") { outcome != nil }
        XCTAssertEqual(outcome, "moved")
    }
}
