@testable import SheepdogKit
import XCTest

/// The review's B1 (one question shown once), B2 (a blocked chip's verb)
/// and B4 (Health counts and groups) as pure functions.
final class ReviewPolishTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)
    private func at(_ s: TimeInterval) -> Date { t0.addingTimeInterval(s) }

    private let pending = PendingAction(
        id: "ap-1", paneID: "p-auth", kind: "answer_prompt",
        question: "Would you like to run git push?",
        options: [PendingOption(index: 1, label: "Yes, proceed"), PendingOption(index: 2, label: "No")],
        agent: "auth", summary: "auth asks to run git push.", command: "git push origin main"
    )

    private func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String, agent: String? = nil, pendingID: String? = nil) -> TimelineRow {
        .item(ConversationItem(id: id, ts: at(TimeInterval(id)), kind: kind, text: text, agent: agent, pendingID: pendingID))
    }

    // MARK: - B1

    func testThePinnedQuestionsOtherCopiesFoldToPointers() {
        let rows: [TimelineRow] = [
            item(1, .shepherd, "how's auth doing?"),
            item(2, .dog, "auth is working on the login page."),
            item(3, .approval, "Would you like to run git push?", agent: "auth", pendingID: "ap-1"),
            item(4, .push, "auth wants to push to main. Yes or no?", agent: "auth"),
            item(5, .dog, "auth is asking whether it can push to main."),
            item(6, .dog, "auth also said the tests pass."),
            item(7, .push, "billing finished.", agent: "billing"),
        ]
        let ids = QuestionFocus.pointerRowIDs(rows: rows, pending: pending)
        XCTAssertEqual(ids, ["item.3", "item.4", "item.5"], "the approval, its push, and only the first dog line after it")
    }

    func testNothingFoldsWithoutAnOpenQuestionOrAfterTheUserSpeaks() {
        let rows: [TimelineRow] = [
            item(3, .approval, "Would you like to run git push?", agent: "auth", pendingID: "ap-1"),
            item(4, .shepherd, "wait, what's auth pushing?"),
            item(5, .dog, "auth wants to push the login fix."),
        ]
        XCTAssertEqual(QuestionFocus.pointerRowIDs(rows: rows, pending: nil), [])
        XCTAssertEqual(QuestionFocus.pointerRowIDs(rows: rows, pending: pending), ["item.3"], "a new exchange is left alone")
        var notAPrompt = pending
        notAPrompt.options = []
        XCTAssertEqual(QuestionFocus.pointerRowIDs(rows: rows, pending: notAPrompt), [])
    }

    func testBeforeHistorySyncsLiveBubblesAfterTheUserFold() {
        let rows: [TimelineRow] = [
            .live(.init(id: 1, speaker: .shepherd, text: "anything new?", isFinal: true, at: at(1))),
            .live(.init(id: 2, speaker: .system, kind: .push(needsUser: true), text: "auth asks to run git push.", isFinal: true, at: at(2))),
            .live(.init(id: 3, speaker: .sheepdog, text: "Auth wants to push. Shall I let it?", isFinal: true, at: at(3))),
        ]
        XCTAssertEqual(QuestionFocus.pointerRowIDs(rows: rows, pending: pending), ["live.2", "live.3"])
    }

    // MARK: - B2

    func testABlockedChipLeadsWithAShortVerb() {
        let auth = Agent(paneID: "p-auth", alias: "auth", state: .blocked)
        XCTAssertEqual(auth.chipSubtitle(pending: pending), "Needs approval · git push origin main")
        var noCommand = pending
        noCommand.command = nil
        XCTAssertEqual(auth.chipSubtitle(pending: noCommand), "Needs approval · Would you like to run git push?")
        var blockedOnly = auth
        blockedOnly.lastBlock = .init(question: "Which branch?")
        XCTAssertEqual(blockedOnly.chipSubtitle(pending: nil), "Needs you · Which branch?")
        XCTAssertEqual(auth.chipSubtitle(pending: nil), "Needs you")
        // Another agent's question is not this one's.
        let other = PendingAction(id: "x", paneID: "p-other", kind: "answer_prompt", options: [PendingOption(index: 1, label: "Yes")])
        XCTAssertEqual(auth.chipSubtitle(pending: other), "Needs you")
        // Not blocked: the usual subtitle.
        let busy = Agent(paneID: "p-b", alias: "b", state: .working, currentTask: "refactor auth")
        XCTAssertEqual(busy.chipSubtitle(pending: nil), "refactor auth")
    }

    // MARK: - B4

    private func event(_ seq: UInt64, _ type: String, _ payload: [String: JSONValue]) -> DaemonEvent {
        DaemonEvent(seq: seq, ts: at(TimeInterval(seq)), source: "test", type: type, payload: payload)
    }

    func testRepeatsGroupAndCodesStayBehindTheChevron() {
        var health = DaemonHealth()
        health.fold(event(1, "worker.session_missing", ["agent": .string("auth"), "reason": .string("not_reported")]))
        health.fold(event(2, "worker.session_missing", ["agent": .string("auth"), "reason": .string("not_reported")]))
        health.fold(event(3, "brain.fallback", ["reason": .string("timeout")]))
        health.fold(event(4, "worker.session_missing", ["agent": .string("auth"), "reason": .string("not_reported")]))
        let groups = DaemonHealth.grouped(health.problems)
        XCTAssertEqual(groups.map(\.count), [3, 1])
        XCTAssertEqual(groups.first?.latest.id, 4, "the newest occurrence is shown")
        XCTAssertNil(groups.first?.latest.detail, "not_reported is a code, not a sentence")
        XCTAssertTrue(groups.first?.latest.raw.contains("not_reported") ?? false, "still in the raw event")
        // The badge counts errors only.
        XCTAssertEqual(health.problems(since: t0).filter { $0.severity == .error }.count, 1)
    }

    func testWhatCountsAsACode() {
        XCTAssertTrue(DaemonHealth.isCode("not_reported"))
        XCTAssertTrue(DaemonHealth.isCode("ENOENT"))
        XCTAssertTrue(DaemonHealth.isCode("session.missing"))
        XCTAssertFalse(DaemonHealth.isCode("the pane closed"))
        XCTAssertFalse(DaemonHealth.isCode("timeout"))
        XCTAssertFalse(DaemonHealth.isCode("Gemini"))
    }
}
