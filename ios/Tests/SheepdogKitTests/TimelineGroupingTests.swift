import XCTest
@testable import SheepdogKit

/// Folding the agents' updates into "N agent updates" rows.
final class TimelineGroupingTests: XCTestCase {
    private func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String = "t", urgency: String? = nil, subkind: String? = nil) -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000 + TimeInterval(id)), kind: kind, text: text, urgency: urgency, subkind: subkind)
    }

    private func restart(_ id: Int64) -> TimelineRow {
        .divider(item(id, .notice, "Sheepdog started."), title: "Sheepdog restarted · 5:00 PM")
    }

    private func shape(_ rows: [ConversationDisplayRow]) -> [String] {
        rows.map {
            switch $0 {
            case let .row(row): return row.id
            case let .updates(id, members): return "\(id)[\(members.count)]"
            }
        }
    }

    func testARunOfThreePushesFoldsSpeechStaysVisible() {
        let rows: [TimelineRow] = [
            .item(item(1, .shepherd, "how are they")),
            .item(item(2, .push, "billing finished")),
            .item(item(3, .push, "docs started")),
            .item(item(4, .push, "auth finished")),
            .item(item(5, .dog, "all good")),
        ]
        let grouped = ConversationTimeline.grouped(rows)
        XCTAssertEqual(shape(grouped), ["item.1", "updates.item.2[3]", "item.5"])
        guard case let .updates(_, members) = grouped[1] else { return XCTFail() }
        XCTAssertEqual(ConversationTimeline.previewLines(members), ["docs started", "auth finished"], "the newest two, oldest first")
    }

    func testFewerThanThreeUpdatesAreNotFolded() {
        let rows: [TimelineRow] = [.item(item(1, .dog)), .item(item(2, .push)), .item(item(3, .push)), .item(item(4, .shepherd))]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testLiveAgentPushesFold() {
        let rows: [TimelineRow] = [
            .live(.init(id: 7, speaker: .system, kind: .push(needsUser: false), text: "tests passed", isFinal: true)),
            .item(item(1, .push)),
            .live(.init(id: 8, speaker: .system, kind: .push(needsUser: false), text: "docs done", isFinal: true)),
            .live(.init(id: 9, speaker: .sheepdog, text: "hello", isFinal: true)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), ["updates.live.7[3]", "live.9"])
    }

    func testOnlyAgentUpdatesFoldNeverNotices() {
        // Reconnects and audio notices are system plumbing: not a group of
        // their own, and not part of one.
        let rows: [TimelineRow] = [
            .item(item(1, .notice, "Voice reconnected")),
            .item(item(2, .notice, "Voice context lost.")),
            .live(.init(id: 8, speaker: .system, kind: .notice, text: "Audio moved to Speaker.", isFinal: true)),
            .item(item(3, .push)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testQuestionsApprovalsCardsAndOtherDividersBreakARun() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)), .item(item(2, .push)),
            .item(item(3, .push, "Should I deploy?", urgency: "interrupt")), // needs the user
            .item(item(4, .push)), .item(item(5, .push)),
            .item(item(6, .approval)),
            .item(item(7, .push)), .item(item(8, .push)),
            .divider(item(9, .notice, "New conversation", subkind: "reset"), title: "New conversation · 5:01 PM"),
            .item(item(10, .push)), .item(item(11, .push)),
            .item(item(12, .card)),
            .item(item(13, .push)),
        ]
        // Nothing here is three updates in a row.
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testDayDividerSplitsARun() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)), .item(item(2, .push)),
            .day(Date(timeIntervalSince1970: 1_790_100_000)),
            .item(item(3, .push)), .item(item(4, .push)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testARestartDividerDoesNotSplitAGroup() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)), .item(item(2, .push)),
            restart(3),
            .item(item(4, .push)),
            .item(item(5, .dog)),
        ]
        let grouped = ConversationTimeline.grouped(rows)
        XCTAssertEqual(shape(grouped), ["updates.item.1[4]", "item.5"], "two updates, the divider and one more are one group")
        guard case let .updates(_, members) = grouped[0] else { return XCTFail() }
        XCTAssertEqual(members.map(\.id), ["item.1", "item.2", "item.3", "item.4"], "the divider sits inside, in order")
        XCTAssertEqual(ConversationTimeline.updateCount(members), 3, "the divider is not an update")
    }

    func testARestartDividerAtTheEdgesStaysOutside() {
        let rows: [TimelineRow] = [
            restart(1),
            .item(item(2, .push)), .item(item(3, .push)), .item(item(4, .push)),
            restart(5),
            .item(item(6, .dog)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), ["item.1", "updates.item.2[3]", "item.5", "item.6"])
    }

    func testRestartInsideASmallRunKeepsTheOrder() {
        let rows: [TimelineRow] = [.item(item(1, .push)), restart(2), .item(item(3, .push))]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testHiddenSystemRowsDoNotSplitARun() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)),
            .item(item(2, .notice, "Voice context lost.")),
            .item(item(3, .push)),
            .item(item(4, .notice, "Voice reconnected with a fresh session; earlier context was lost.")),
            .item(item(5, .push)),
        ]
        let visible = ConversationTimeline.visibleRows(rows, showTechnical: false)
        XCTAssertEqual(shape(ConversationTimeline.grouped(visible)), ["updates.item.1[3]"])
    }

    func testPointerRowsStayVisible() {
        let rows: [TimelineRow] = (1...4).map { .item(item($0, .push)) }
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows, keepVisible: ["item.2"])), ["item.1", "item.2", "item.3", "item.4"])
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows, keepVisible: ["item.4"])), ["updates.item.1[3]", "item.4"])
    }

    func testTheGroupKeepsItsIdAsItGrows() {
        var rows: [TimelineRow] = [.item(item(1, .dog)), .item(item(2, .push)), .item(item(3, .push)), .item(item(4, .push))]
        let before = ConversationTimeline.grouped(rows)[1].id
        rows.append(.item(item(5, .push)))
        let after = ConversationTimeline.grouped(rows)
        XCTAssertEqual(after[1].id, before)
        XCTAssertEqual(shape(after), ["item.1", "updates.item.2[4]"])
    }

    func testFromTheTimelineBuilder() {
        // The real pipeline: history → rows → visible → grouped.
        let history = [item(1, .shepherd), item(2, .push), item(3, .push), item(4, .push), item(5, .dog)]
        let rows = ConversationTimeline.rows(history: history, live: [], calendar: Calendar(identifier: .gregorian))
        let grouped = ConversationTimeline.grouped(ConversationTimeline.visibleRows(rows, showTechnical: false))
        XCTAssertEqual(grouped.count, 4) // day, user, 3 updates, dog
        XCTAssertEqual(shape(grouped).last, "item.5")
    }

    func testTitle() {
        XCTAssertEqual(ConversationTimeline.updatesTitle(1), "1 agent update")
        XCTAssertEqual(ConversationTimeline.updatesTitle(4), "4 agent updates")
    }
}
