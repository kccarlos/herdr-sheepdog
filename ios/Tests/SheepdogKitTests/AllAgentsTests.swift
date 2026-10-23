import SheepdogKit
import XCTest

final class AllAgentsTests: XCTestCase {
    func testFilters() {
        let blocked = Agent(paneID: "a", alias: "a", state: .blocked)
        let working = Agent(paneID: "b", alias: "b", state: .working)
        let idle = Agent(paneID: "c", alias: "c", state: .idle)
        XCTAssertEqual([blocked, working, idle].filter(AgentFilter.waiting.matches).map(\.alias), ["a"])
        XCTAssertEqual([blocked, working, idle].filter(AgentFilter.working.matches).map(\.alias), ["b"])
        XCTAssertEqual([blocked, working, idle].filter(AgentFilter.all.matches).count, 3)
    }

    func testTwoColumnsKeepTheReadingOrder() {
        let (left, right) = AgentFilter.columns([1, 2, 3, 4, 5])
        XCTAssertEqual(left, [1, 3, 5])
        XCTAssertEqual(right, [2, 4])
    }

    func testEarlierIsTheThreeBeforeTheNewestNewestFirst() {
        let entries = (1 ... 6).map { Agent.JournalEntry(event: "e\($0)", lastAssistant: $0 == 3 ? "  " : "m\($0)") }
        let agent = Agent(paneID: "a", alias: "a", state: .done, journal: entries)
        XCTAssertEqual(agent.lastWorkerMessage?.lastAssistant, "m6")
        XCTAssertEqual(agent.earlierWorkerMessages().map(\.lastAssistant), ["m5", "m4", "m2"], "skips empty ones")
        XCTAssertTrue(Agent(paneID: "b", alias: "b", state: .done, journal: [entries[0]]).earlierWorkerMessages().isEmpty)
    }
}
