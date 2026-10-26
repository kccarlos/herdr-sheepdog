import XCTest
@testable import SheepdogKit

/// Random interleavings of two computers' runtimes with requests in flight:
/// refreshes, answers, replies in any order, re-pairing (a new epoch), the
/// app leaving and coming back, Low Data Mode. After every step, each
/// model shows only its own computer's replies, from its current pairing.
@MainActor
final class ComputerRuntimePropertyTests: XCTestCase {
    static let seeds: UInt64 = 40
    static let steps = 40

    func testRepliesNeverLandOnTheWrongComputerOrAnOlderPairing() async {
        for seed in 1 ... Self.seeds {
            await run(seed: seed)
        }
    }

    private func run(seed: UInt64) async {
        var random = LinkRandom(seed: seed)
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let addresses = ["home", "work"]
        for address in addresses {
            network.questions[address] = [PendingAction(id: "q-\(address)", paneID: "\(address):p1", kind: "question", question: "Go?",
                                                        options: [PendingOption(index: 1, label: "Yes")])]
        }
        var models: [String: ComputerModel] = [:]
        for address in addresses {
            let connection = ComputerConnection(computerID: address, address: address, makeRequests: { network.makeRequests($0) })
            let model = ComputerModel(id: address, name: address, addresses: [address], certExpires: nil, connection: connection,
                                      scheduler: clock, demand: LinkDemand(isCurrent: address == "home"), random: LinkRandom(seed: seed))
            models[address] = model
            model.start()
        }
        var trace: [String] = []
        for step in 0 ..< Self.steps {
            let address = addresses[Int(random.next() % 2)]
            let model = models[address]!
            switch random.next() % 10 {
            case 0, 1:
                trace.append("refresh \(address)")
                Task { await model.refresh() }
            case 2:
                trace.append("answer \(address)")
                model.answer(question: "q-\(address)", option: 1)
            case 3, 4, 5:
                let pending = network.pending
                if !pending.isEmpty {
                    let pick = pending[Int(random.next() % UInt64(pending.count))]
                    trace.append("resolve \(pick.address) \(pick.kind) g\(pick.generation)")
                    network.resolve(pick.id)
                }
            case 6:
                trace.append("repair \(address)")
                network.bump(address)
                model.renew(addresses: [address], certExpires: nil)
            case 7:
                let hang = random.next() % 2 == 0
                trace.append("\(hang ? "hang" : "answer at once") \(address)")
                network.setMode(hang ? .hang : .answer, for: address)
            case 8:
                let phase: AppPhase = random.next() % 2 == 0 ? .background : .active
                trace.append("\(phase) \(address)")
                model.setAppPhase(phase)
            default:
                trace.append("event \(address)")
                network.push(address)
            }
            await settle(3)
            for (id, model) in models {
                let generation = network.generation(id)
                for agent in model.agents {
                    XCTAssertEqual(agent.alias, "\(id)#\(generation)", "seed \(seed) step \(step): \(trace.suffix(6))")
                }
                for (_, phase) in model.phases {
                    if case let .answered(_, spoken?) = phase {
                        XCTAssertTrue(spoken.hasPrefix("\(id)#\(generation):"), "seed \(seed) step \(step): \(spoken) on \(id); \(trace.suffix(6))")
                    }
                }
            }
        }
        for model in models.values { model.stop() }
        for p in network.pending { network.resolve(p.id) }
        await settle(2)
    }
}
