import SheepdogKit
import XCTest

final class QuestionOrderTests: XCTestCase {
    private func q(_ id: String) -> PendingAction {
        PendingAction(id: id, paneID: "p-" + id, kind: "answer_prompt", options: [PendingOption(index: 1, label: "Yes")], agent: id)
    }

    func testTheCurrentComputerFirstThenByAge() {
        // Oldest first, as /api/state sends them; "home" ones are another computer's.
        let all = ["a", "home1", "b", "home2", "c"]
        let ordered = QuestionOrder.ordered(all) { !$0.hasPrefix("home") }
        XCTAssertEqual(ordered, ["a", "b", "c", "home1", "home2"])
    }

    func testOneComputerKeepsTheAgeOrder() {
        XCTAssertEqual(QuestionOrder.ordered(["x", "y", "z"]) { _ in true }, ["x", "y", "z"])
    }

    func testTheVoicesSettledQuestionStaysInFrontUntilItGoes() {
        let open = [q("b"), q("c")]
        XCTAssertEqual(QuestionOrder.floating(open: open, voice: q("a"), voiceSettled: true).map(\.id), ["a", "b", "c"])
        XCTAssertEqual(QuestionOrder.floating(open: open, voice: q("a"), voiceSettled: false).map(\.id), ["b", "c"], "an open voice question is among `open` already")
        XCTAssertEqual(QuestionOrder.floating(open: open, voice: q("b"), voiceSettled: true).map(\.id), ["b", "c"], "no duplicate")
        XCTAssertEqual(QuestionOrder.floating(open: open, voice: nil, voiceSettled: false).map(\.id), ["b", "c"])
    }
}
