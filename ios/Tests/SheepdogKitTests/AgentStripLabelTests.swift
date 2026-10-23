import SheepdogKit
import XCTest

final class AgentStripLabelTests: XCTestCase {
    func testOneComputerNeverNamesOne() {
        for state in [Agent.State.blocked, .working, .idle, .done] {
            XCTAssertNil(AgentStripLabel.computer(state: state, agentComputer: "Home Mac", pairedComputers: 1))
        }
    }

    func testABlockedAgentOnAnotherComputerNamesIt() {
        XCTAssertEqual(AgentStripLabel.computer(state: .blocked, agentComputer: "Home Mac", pairedComputers: 2), "Home Mac")
    }

    func testOnlyBlockedAgentsAndOnlyOtherComputers() {
        XCTAssertNil(AgentStripLabel.computer(state: .working, agentComputer: "Home Mac", pairedComputers: 2))
        // The current computer's agents carry no computer.
        XCTAssertNil(AgentStripLabel.computer(state: .blocked, agentComputer: nil, pairedComputers: 2))
        XCTAssertNil(AgentStripLabel.computer(state: .blocked, agentComputer: "  ", pairedComputers: 3))
    }
}
