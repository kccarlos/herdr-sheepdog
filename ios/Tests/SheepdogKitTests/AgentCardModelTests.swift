import XCTest
@testable import SheepdogKit

final class AgentCardModelTests: XCTestCase {
    func testTheCardLineIsTheTaskThenTheLastTaskThenTheTitle() {
        var agent = Agent(paneID: "p", alias: "e2e", state: .idle)
        XCTAssertNil(agent.cardLine)
        agent.title = "Checkout e2e flake"
        XCTAssertEqual(agent.cardLine, "Checkout e2e flake", "an agent started by hand has only its title")
        agent.lastTask = "  Fix the flake  "
        XCTAssertEqual(agent.cardLine, "Fix the flake")
        agent.currentTask = "Run it 20 times"
        XCTAssertEqual(agent.cardLine, "Run it 20 times")
        agent.currentTask = "   "
        XCTAssertEqual(agent.cardLine, "Fix the flake", "blank is absent")
    }

    func testTheAnsweredQuestionCarriesTheChosenAnswer() {
        var agent = Agent(paneID: "p", alias: "search", state: .working)
        agent.lastBlock = Agent.Block(question: "Run the benchmark against staging?", answered: false)
        XCTAssertNil(agent.answeredQuestion, "still open")
        agent.lastBlock?.answered = true
        XCTAssertEqual(agent.answeredQuestion, "Run the benchmark against staging?")
        agent.lastBlock?.answer = "Yes"
        XCTAssertEqual(agent.answeredQuestion, "Run the benchmark against staging? — Yes")
    }

    func testTheDemoSendsItsQuestionAsAList() {
        var world = DemoWorld(now: Date(timeIntervalSince1970: 0))
        XCTAssertEqual(world.state.questions, [])
        world.advance(to: Date(timeIntervalSince1970: 20))
        XCTAssertEqual(world.state.questions?.map(\.paneID), [DemoWorld.paymentsPane])
    }
}
