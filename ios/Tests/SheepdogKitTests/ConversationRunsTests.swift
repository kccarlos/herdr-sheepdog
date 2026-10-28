import XCTest
@testable import SheepdogKit

/// Which lines share a ledger card, and the space between rows (docs/dev/
/// design/conversation-timeline.md, section 12).
final class ConversationRunsTests: XCTestCase {
    private func item(_ id: Int64, _ kind: ConversationItem.Kind, resolution: ConversationItem.Resolution? = nil, urgency: String? = nil, via: String? = nil) -> TimelineRow {
        .item(ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000 + TimeInterval(id)), kind: kind, text: "t", via: via, urgency: urgency, resolution: resolution))
    }

    private func display(_ rows: [TimelineRow]) -> [ConversationDisplayRow] { rows.map(ConversationDisplayRow.row) }

    func testConsecutiveOutcomesShareOneCardAndAnythingElseEndsTheRun() {
        let rows = display([
            item(1, .approval, resolution: .confirmed), // answered
            item(2, .approval, resolution: .cancelled), // closed
            item(3, .push), // an update
            item(4, .card), // a card is its own object
            item(5, .push),
            item(6, .dog),
            item(7, .push),
        ])
        let positions = ConversationTimeline.ledgerPositions(rows)
        XCTAssertEqual(positions["item.1"], LedgerPosition(joinsAbove: false, joinsBelow: true))
        XCTAssertEqual(positions["item.2"], LedgerPosition(joinsAbove: true, joinsBelow: true))
        XCTAssertEqual(positions["item.3"], LedgerPosition(joinsAbove: true, joinsBelow: false))
        XCTAssertNil(positions["item.4"], "a card is never in a run")
        XCTAssertEqual(positions["item.5"], .single)
        XCTAssertNil(positions["item.6"], "dialogue is not a ledger line")
        XCTAssertEqual(positions["item.7"], .single)
    }

    func testAnOpenQuestionAndAFoldedPointerAreNotLedgerLines() {
        let rows = display([item(1, .push), item(2, .approval), item(3, .push, urgency: "interrupt"), item(4, .push)])
        XCTAssertEqual(ConversationTimeline.ledgerPositions(rows)["item.1"], .single)
        XCTAssertNil(ConversationTimeline.ledgerPositions(rows)["item.2"])
        XCTAssertNil(ConversationTimeline.ledgerPositions(rows)["item.3"])
        // A said push folded to its question's pointer leaves the run.
        let pointed = display([item(1, .push), item(2, .push), item(3, .push)])
        let positions = ConversationTimeline.ledgerPositions(pointed, excluding: ["item.2"])
        XCTAssertEqual(positions["item.1"], .single)
        XCTAssertNil(positions["item.2"])
        XCTAssertEqual(positions["item.3"], .single)
    }

    func testAFoldedGroupIsOneLedgerLine() {
        let group = ConversationDisplayRow.updates(id: "updates.item.1", rows: [item(1, .push), item(2, .push), item(3, .push)])
        let rows = [group] + display([item(4, .approval, resolution: .confirmed)])
        let positions = ConversationTimeline.ledgerPositions(rows)
        XCTAssertEqual(positions["updates.item.1"], LedgerPosition(joinsAbove: false, joinsBelow: true))
        XCTAssertEqual(positions["item.4"], LedgerPosition(joinsAbove: true, joinsBelow: false))
    }

    func testEveryCategoryHasItsGutterCode() {
        XCTAssertEqual(TimelineCategory.allCases.map(\.code), ["DLG", "ASK", "UPD", "CTX", "SYS", "SCR"])
    }

    func testSpacingFollowsTheDesign() {
        let you = ConversationDisplayRow.row(item(1, .shepherd))
        let voice = ConversationDisplayRow.row(item(2, .dog))
        let divider = ConversationDisplayRow.row(.day(Date()))
        let pointer = ConversationDisplayRow.row(item(3, .approval))
        let outcome = ConversationDisplayRow.row(item(4, .push))
        let card = ConversationDisplayRow.row(item(5, .card))
        let gap = ConversationTimeline.gap
        XCTAssertEqual(gap(divider, you, false, false), 14)
        XCTAssertEqual(gap(you, voice, false, false), 6)
        XCTAssertEqual(gap(voice, you, false, false), 18)
        XCTAssertEqual(gap(voice, pointer, false, false), 4)
        XCTAssertEqual(gap(voice, outcome, false, false), 18)
        XCTAssertEqual(gap(outcome, outcome, true, false), 0)
        XCTAssertEqual(gap(outcome, card, false, false), 8)
        XCTAssertEqual(gap(card, outcome, false, false), 8)
        XCTAssertEqual(gap(voice, outcome, false, true), 6, "the technical view is a log")
    }

    func testOnePointerPerQuestion() {
        let approval = TimelineRow.item(ConversationItem(id: 3, ts: Date(), kind: .approval, text: "q", agent: "Codex", pendingID: "p", resolution: .open))
        let push = item(2, .push)
        let said = item(4, .dog)
        let lines = QuestionFocus.pointerLines(rows: [push, approval, said], pointerIDs: [push.id, approval.id, said.id])
        XCTAssertEqual(lines, ["item.3": "Codex"])
        // No approval item yet: the first folded row stands for it.
        XCTAssertEqual(QuestionFocus.pointerLines(rows: [push, said], pointerIDs: [push.id, said.id]), ["item.2": "An agent"])
    }

    func testCatchMeUpWaitsForTheVoice() {
        let since = Date()
        let early = item(1, .dog)
        XCTAssertFalse(ConversationTimeline.voiceAnswered([early, item(2, .shepherd)], since: since.addingTimeInterval(60)))
        let live = TimelineRow.live(.init(id: 1, speaker: .sheepdog, text: "Two things.", isFinal: false, at: since.addingTimeInterval(1)))
        XCTAssertTrue(ConversationTimeline.voiceAnswered([live], since: since))
    }

    func testOnlyTheTextRouterWrites() {
        guard case let .item(router) = item(1, .dog, via: "text_router"), case let .item(spoken) = item(2, .dog) else { return XCTFail() }
        XCTAssertTrue(router.isWritten)
        XCTAssertFalse(spoken.isWritten)
    }
}
