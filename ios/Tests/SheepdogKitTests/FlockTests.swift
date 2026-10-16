@testable import SheepdogKit
import XCTest

final class FlockTests: XCTestCase {
    private func agent(_ alias: String, _ state: Agent.State) -> Agent {
        Agent(paneID: "p-\(alias)", alias: alias, state: state)
    }

    func testRunningStates() {
        XCTAssertTrue(Agent.State.idle.isRunning)
        XCTAssertTrue(Agent.State.working.isRunning)
        XCTAssertTrue(Agent.State.blocked.isRunning)
        XCTAssertTrue(Agent.State.done.isRunning)
        XCTAssertFalse(Agent.State.unknown.isRunning)
        XCTAssertFalse(Agent.State.exited.isRunning)
    }

    func testSortsBlockedThenWorkingThenIdleAndDoneByAlias() {
        let flock = Flock([
            agent("zeta", .done), agent("auth", .idle), agent("deploy", .working),
            agent("billing", .blocked), agent("api", .working), agent("old", .exited), agent("ghost", .unknown),
        ])
        XCTAssertEqual(flock.running.map(\.alias), ["billing", "api", "deploy", "auth", "zeta"])
        XCTAssertEqual(flock.notRunning.map(\.alias), ["ghost", "old"])
        XCTAssertEqual(flock.all.count, 7)
    }

    func testABlockedAgentIsNeverFolded() {
        let flock = Flock([agent("a", .blocked), agent("b", .exited)])
        XCTAssertEqual(flock.running.map(\.alias), ["a"])
        XCTAssertFalse(flock.notRunning.contains { $0.state == .blocked })
    }

    func testWaitingCountsOnlyBlocked() {
        XCTAssertEqual(Flock([agent("a", .blocked), agent("b", .blocked), agent("c", .working)]).waitingCount, 2)
        XCTAssertEqual(Flock([agent("c", .working), agent("d", .exited)]).waitingCount, 0)
        XCTAssertTrue(Flock([]).isEmpty)
    }
}
