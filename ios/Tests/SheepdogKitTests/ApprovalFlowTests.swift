import XCTest
@testable import SheepdogKit

/// The phase machine every surface reads: each question's phase and the
/// voice's pinned question (`ApprovalFlow`, owned by `ComputerModel`).
final class ApprovalFlowTests: XCTestCase {
    private func pending(_ id: String = "ap-1") throws -> PendingAction {
        var p = try DaemonClient.decoder.decode(PendingAction.self, from: Data(Fixtures.statePendingAction.utf8))
        p.id = id
        return p
    }

    func testShownThenTappedThenAnswered() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        XCTAssertEqual(flow.present(question), .appeared)
        XCTAssertEqual(flow.card?.phase, .open(error: nil))
        XCTAssertEqual(flow.present(question), .none, "the same question again is not news")

        let option = try XCTUnwrap(flow.tap(question, index: 1))
        XCTAssertEqual(option.label, "Yes, proceed")
        XCTAssertEqual(flow.card?.phase, .sending(option))
        XCTAssertNil(flow.tap(question, index: 3), "a second tap while sending is ignored")

        XCTAssertEqual(flow.succeeded(pendingID: "ap-1", spoken: "Told billing Yes, proceed."), .settled)
        XCTAssertEqual(flow.card?.phase, .answered(outcome: "You approved — Yes, proceed · by tap", spoken: "Told billing Yes, proceed."))
        XCTAssertNil(flow.tap(question, index: 1))

        // The state refresh right after no longer has the question: the
        // settled card stays until its moment is over.
        XCTAssertEqual(flow.present(nil), .none)
        flow.retain(open: [])
        XCTAssertNotNil(flow.card)
        flow.dismiss(pendingID: "ap-1")
        XCTAssertNil(flow.card)
        XCTAssertEqual(flow.phase("ap-1").isSettled, true, "the question stays answered for the other cards")
    }

    func testDecliningSaysSo() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        _ = flow.tap(question, index: 3)
        flow.succeeded(pendingID: "ap-1", spoken: nil)
        XCTAssertEqual(flow.card?.phase, .answered(outcome: "You declined — No, and tell Codex what to do differently · by tap", spoken: nil))
    }

    func testAnotherQuestionSaysItsAnswerShort() throws {
        var flow = ApprovalFlow()
        flow.present(try pending("voice"))
        let other = try pending("other")
        _ = flow.tap(other, index: 1)
        XCTAssertEqual(flow.card?.phase, .open(error: nil), "the voice's card is not the question tapped")
        XCTAssertEqual(flow.succeeded(pendingID: "other", spoken: " "), .none, "only the voice's card lingers")
        XCTAssertEqual(flow.phase("other"), .answered(outcome: "Answered: Yes, proceed", spoken: nil))
    }

    func test409MeansGone() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        _ = flow.tap(question, index: 1)
        XCTAssertEqual(flow.gone(pendingID: "ap-1"), .settled)
        XCTAssertEqual(flow.card?.phase, .gone(message: "That question was already answered or has gone."))
    }

    func testAFailureReopensWithTheReason() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        _ = flow.tap(question, index: 1)
        flow.failed(pendingID: "ap-1", message: "Can't reach the daemon")
        XCTAssertEqual(flow.card?.phase, .open(error: "Can't reach the daemon"))
        XCTAssertNotNil(flow.tap(question, index: 1), "and the user can try again")
    }

    func testALateReplyDoesNotOverwriteASettledQuestion() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        _ = flow.tap(question, index: 1)
        // The voice's question expired while the tap was on its way.
        XCTAssertEqual(flow.resolved(pendingID: "ap-1", as: .expired), .settled)
        XCTAssertEqual(flow.succeeded(pendingID: "ap-1", spoken: "ok"), .none)
        flow.failed(pendingID: "ap-1", message: "late")
        XCTAssertEqual(flow.gone(pendingID: "ap-1"), .none)
        XCTAssertEqual(flow.card?.phase, .gone(message: ApprovalFlow.goneMessage))
    }

    func testResolvedElsewhereByVoice() throws {
        var flow = ApprovalFlow()
        flow.present(try pending())
        XCTAssertEqual(flow.resolved(pendingID: "ap-1", as: .confirmed, answer: "Yes, proceed", via: "fast_path"), .settled)
        XCTAssertEqual(flow.card?.phase, .answered(outcome: "You approved — Yes, proceed · by voice", spoken: nil))
    }

    func testExpiredOrReplacedOrGoneFromStateDismisses() throws {
        var flow = ApprovalFlow()
        flow.present(try pending())
        XCTAssertEqual(flow.resolved(pendingID: "other", as: .expired), .none, "another question's fate is not ours")
        XCTAssertEqual(flow.resolved(pendingID: "ap-1", as: .expired), .dismissed)
        XCTAssertNil(flow.card)

        flow.present(try pending("ap-2"))
        XCTAssertEqual(flow.present(nil), .dismissed, "a state refresh without it: resolved elsewhere")
    }

    func testOurOwnConfirmedEventDoesNotBeatTheReply() throws {
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        _ = flow.tap(question, index: 1)
        XCTAssertEqual(flow.resolved(pendingID: "ap-1", as: .confirmed, answer: "Yes, proceed", via: "tap"), .none)
        XCTAssertEqual(flow.succeeded(pendingID: "ap-1", spoken: "ok"), .settled)
    }

    func testANewQuestionReplacesAnOpenCardButWaitsForASendingOne() throws {
        var flow = ApprovalFlow()
        flow.present(try pending("ap-1"))
        XCTAssertEqual(flow.present(try pending("ap-2")), .appeared)
        XCTAssertEqual(flow.card?.pending.id, "ap-2")
        _ = flow.tap(try pending("ap-2"), index: 1)
        XCTAssertEqual(flow.present(try pending("ap-3")), .none)
        XCTAssertEqual(flow.card?.pending.id, "ap-2")
    }

    func testATapOnAQuestionCardShowsOnTheVoiceCard() throws {
        // The voice's question tapped on its agent's card or the floating
        // stack: one phase, so the voice's card shows it on its way too.
        var flow = ApprovalFlow()
        let question = try pending()
        flow.present(question)
        let option = try XCTUnwrap(flow.tap(question, index: 1))
        XCTAssertEqual(flow.card, ApprovalFlow.Card(pending: question, phase: .sending(option)))
        XCTAssertFalse(flow.card!.acceptsTaps)
    }

    func testAStateForgetsOnlyOpenQuestionsThatClosed() throws {
        var flow = ApprovalFlow()
        let a = try pending("a"), b = try pending("b"), c = try pending("c")
        _ = flow.tap(a, index: 1)
        flow.failed(pendingID: "a", message: "no")
        _ = flow.tap(b, index: 1)
        _ = flow.tap(c, index: 1)
        flow.succeeded(pendingID: "c", spoken: nil)
        flow.retain(open: [])
        XCTAssertNil(flow.phases["a"], "an open question that closed is forgotten")
        XCTAssertNotNil(flow.phases["b"], "one in flight finishes on its own")
        XCTAssertNotNil(flow.phases["c"], "a settled one keeps saying how it ended")
    }

    func testOnlyAnswerPromptsWithOptionsGetACard() {
        var flow = ApprovalFlow()
        XCTAssertEqual(flow.present(PendingAction(id: "c", paneID: "p", kind: "confirm_send", spokenSummary: "send?")), .none)
        XCTAssertEqual(flow.present(PendingAction(id: "a", paneID: "p", kind: "answer_prompt", options: [])), .none)
        XCTAssertNil(flow.card)
    }
}
