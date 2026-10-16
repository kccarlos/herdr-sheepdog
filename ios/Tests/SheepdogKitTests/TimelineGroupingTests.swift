import XCTest
@testable import SheepdogKit

/// Folding the daemon's pushes and notices into "N updates" rows.
final class TimelineGroupingTests: XCTestCase {
    private func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String = "t", urgency: String? = nil, subkind: String? = nil) -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000 + TimeInterval(id)), kind: kind, text: text, urgency: urgency, subkind: subkind)
    }

    private func shape(_ rows: [ConversationDisplayRow]) -> [String] {
        rows.map {
            switch $0 {
            case let .row(row): return row.id
            case let .updates(id, members): return "\(id)[\(members.count)]"
            }
        }
    }

    func testARunOfPushesFoldsSpeechStaysVisible() {
        let rows: [TimelineRow] = [
            .item(item(1, .shepherd, "how are they")),
            .item(item(2, .push, "billing finished")),
            .item(item(3, .push, "docs started")),
            .item(item(4, .notice, "Voice reconnected")),
            .item(item(5, .dog, "all good")),
        ]
        let grouped = ConversationTimeline.grouped(rows)
        XCTAssertEqual(shape(grouped), ["item.1", "updates.item.2[3]", "item.5"])
        guard case let .updates(_, members) = grouped[1] else { return XCTFail() }
        XCTAssertEqual(ConversationTimeline.updateText(members.last!), "Voice reconnected")
    }

    func testASingleUpdateIsNotFolded() {
        let rows: [TimelineRow] = [.item(item(1, .dog)), .item(item(2, .push)), .item(item(3, .shepherd))]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), ["item.1", "item.2", "item.3"])
    }

    func testDividersAndLiveSystemBubblesFold() {
        let notice = item(1, .notice, "Voice closed")
        let rows: [TimelineRow] = [
            .divider(notice, title: "Voice closed · 5:00 PM"),
            .live(.init(id: 7, speaker: .system, kind: .push(needsUser: false), text: "tests passed", isFinal: true)),
            .live(.init(id: 8, speaker: .system, kind: .notice, text: "Reconnected", isFinal: true)),
            .live(.init(id: 9, speaker: .sheepdog, text: "hello", isFinal: true)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), ["updates.item.1[3]", "live.9"])
    }

    func testQuestionsApprovalsAndBoundariesBreakARun() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)),
            .item(item(2, .push, "Should I deploy?", urgency: "interrupt")), // needs the user
            .item(item(3, .push)),
            .item(item(4, .approval)),
            .item(item(5, .push)),
            .divider(item(6, .notice, "Sheepdog started."), title: "Sheepdog restarted · 5:00 PM"),
            .item(item(7, .push)),
            .divider(item(8, .notice, "New conversation", subkind: "reset"), title: "New conversation · 5:01 PM"),
            .item(item(9, .push)),
            .day(Date(timeIntervalSince1970: 1_790_100_000)),
            .item(item(10, .push)),
        ]
        // Nothing here is two updates in a row.
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), rows.map(\.id))
    }

    func testDayDividerSplitsARun() {
        let rows: [TimelineRow] = [
            .item(item(1, .push)), .item(item(2, .push)),
            .day(Date(timeIntervalSince1970: 1_790_100_000)),
            .item(item(3, .push)), .item(item(4, .push)),
        ]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows)), ["updates.item.1[2]", rows[2].id, "updates.item.3[2]"])
    }

    func testPointerRowsStayVisible() {
        let rows: [TimelineRow] = [.item(item(1, .push)), .item(item(2, .push)), .item(item(3, .push))]
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows, keepVisible: ["item.2"])), ["item.1", "item.2", "item.3"])
        XCTAssertEqual(shape(ConversationTimeline.grouped(rows, keepVisible: ["item.3"])), ["updates.item.1[2]", "item.3"])
    }

    func testTheGroupKeepsItsIdAsItGrows() {
        var rows: [TimelineRow] = [.item(item(1, .dog)), .item(item(2, .push)), .item(item(3, .push))]
        let before = ConversationTimeline.grouped(rows)[1].id
        rows.append(.item(item(4, .push)))
        let after = ConversationTimeline.grouped(rows)
        XCTAssertEqual(after[1].id, before)
        XCTAssertEqual(shape(after), ["item.1", "updates.item.2[3]"])
    }

    func testFromTheTimelineBuilder() {
        // The real pipeline: history → rows → grouped.
        let history = [item(1, .shepherd), item(2, .push), item(3, .push), item(4, .dog)]
        let rows = ConversationTimeline.rows(history: history, live: [], calendar: Calendar(identifier: .gregorian))
        let grouped = ConversationTimeline.grouped(rows)
        XCTAssertEqual(grouped.count, 4) // day, user, 2 updates, dog
        XCTAssertEqual(shape(grouped).last, "item.4")
    }

    func testTitle() {
        XCTAssertEqual(ConversationTimeline.updatesTitle(1), "1 update")
        XCTAssertEqual(ConversationTimeline.updatesTitle(4), "4 updates")
    }
}
