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

    func testTheFirstMessageHintShowsOnlyBeforeTheAgentHasSaidAnything() {
        var agent = Agent(paneID: "p", alias: "docs", state: .idle)
        XCTAssertTrue(agent.awaitsFirstMessage)
        agent.lastResult = Agent.Result(spoken: "  ", detail: nil, at: nil)
        XCTAssertTrue(agent.awaitsFirstMessage, "a blank result says nothing")
        agent.lastResult = Agent.Result(spoken: "docs finished: the page is written.", detail: nil, at: nil)
        XCTAssertFalse(agent.awaitsFirstMessage, "it stopped and reported: no promise of a first message")
        agent.lastResult = nil
        agent.journal = [Agent.JournalEntry(at: nil, event: "done", lastAssistant: "Done.")]
        XCTAssertFalse(agent.awaitsFirstMessage)
    }

    func testTheDemosFinishedAgentsHaveTheirMessage() {
        var world = DemoWorld(now: Date(timeIntervalSince1970: 0))
        world.seedAttention()
        for agent in world.state.agents where agent.lastResult != nil {
            XCTAssertNotNil(agent.lastWorkerMessage, "\(agent.alias) shows its message with its result")
            XCTAssertFalse(agent.awaitsFirstMessage)
        }
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
