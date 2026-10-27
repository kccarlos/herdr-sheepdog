import XCTest
@testable import SheepdogKit

/// Every row kind has one category, and the timeline, the grouping and
/// "Show technical events" all follow it (docs/dev/design/
/// conversation-timeline.md, sections 4 and 6).
final class ConversationClassificationTests: XCTestCase {
    private func item(
        _ id: Int64, _ kind: ConversationItem.Kind, _ text: String = "t",
        urgency: String? = nil, resolution: ConversationItem.Resolution? = nil, subkind: String? = nil
    ) -> ConversationItem {
        ConversationItem(
            id: id, ts: Date(timeIntervalSince1970: 1_790_000_000 + TimeInterval(id)), kind: kind, text: text,
            urgency: urgency, resolution: resolution, subkind: subkind
        )
    }

    private func notice(_ id: Int64, _ text: String, subkind: String? = nil) -> TimelineRow {
        .item(item(id, .notice, text, subkind: subkind))
    }

    private func local(_ id: Int, _ kind: ConversationReducer.Bubble.Kind, _ text: String, speaker: ConversationReducer.Bubble.Speaker = .system) -> TimelineRow {
        .live(.init(id: id, speaker: speaker, kind: kind, text: text, isFinal: true))
    }

    private func category(_ row: TimelineRow) -> TimelineCategory { ConversationTimeline.classify(row).category }

    // MARK: - The table

    func testEveryKindOfRowHasItsCategory() {
        let table: [(String, TimelineRow, TimelineCategory)] = [
            ("you", .item(item(1, .shepherd)), .dialogue),
            ("the voice", .item(item(2, .dog)), .dialogue),
            ("live speech", local(1, .speech, "hi", speaker: .shepherd), .dialogue),
            ("orchestrator reply", .brain(.init(id: 3, ts: Date(), spoken: "ok"), compact: false), .dialogue),
            ("open question", .item(item(4, .approval)), .needsYou),
            ("open question, explicit", .item(item(5, .approval, resolution: .open)), .needsYou),
            ("interrupt push", .item(item(6, .push, urgency: "interrupt")), .needsYou),
            ("live interrupt push", local(2, .push(needsUser: true), "Deploy?"), .needsYou),
            ("answered question", .item(item(7, .approval, resolution: .confirmed)), .outcome),
            ("expired question", .item(item(8, .approval, resolution: .expired)), .outcome),
            ("cancelled question", .item(item(9, .approval, resolution: .cancelled)), .outcome),
            ("replaced question", .item(item(10, .approval, resolution: .replaced)), .outcome),
            ("news push", .item(item(11, .push)), .outcome),
            ("live news push", local(3, .push(needsUser: false), "done"), .outcome),
            ("held push", .held(.init(key: "k", id: 12, ts: Date(), text: "x")), .outcome),
            ("card item", .item(item(13, .card)), .outcome),
            ("live card", .card(ScreenCard(cardID: "c", kind: .card, at: Date(), title: "T", markdown: "- one")), .outcome),
            ("day", .day(Date()), .context),
            ("new conversation", notice(14, "New conversation", subkind: "reset"), .context),
            ("restart", notice(15, "Sheepdog started."), .context),
            ("talking to", local(4, .notice, "Talking to Work Mac"), .context),
            ("context lost", notice(16, "Voice context lost."), .system),
            ("fresh reconnect", notice(17, "Voice reconnected with a fresh session; earlier context was lost."), .system),
            ("resumed reconnect", notice(18, "Voice reconnected; the conversation continues."), .system),
            ("voice closed", notice(19, "Voice session closed: shutdown."), .system),
            ("unknown kind", .item(item(20, .unknown, "")), .system),
            ("audio route", local(5, .notice, "Audio moved to Speaker."), .system),
            ("audio needs a tap", local(6, .notice, "iOS kept the audio for something else. Tap the orb to resume."), .system),
        ]
        for (name, row, expected) in table {
            XCTAssertEqual(category(row), expected, name)
        }
    }

    func testEveryDaemonItemKindIsClassified() {
        // A new `ConversationItem.Kind` must be a conscious choice here.
        let kinds: [ConversationItem.Kind] = [.shepherd, .dog, .push, .approval, .card, .notice, .unknown]
        for kind in kinds {
            _ = ConversationTimeline.classify(item(1, kind))
        }
        XCTAssertEqual(Set(TimelineCategory.allCases.map(\.symbol)).count, TimelineCategory.allCases.count, "one icon per category")
    }

    func testOnlyPlumbingIsHiddenByDefault() {
        let hidden: [TimelineRow] = [
            notice(1, "Voice context lost."), notice(2, "Voice reconnected; the conversation continues."),
            notice(3, "Voice session closed: shutdown."), .item(item(4, .unknown, "")),
            local(1, .notice, "Audio moved to Speaker."), local(2, .notice, "The audio route changed."),
            local(3, .notice, "Paused for a call or Siri."), local(4, .notice, "Listening again."),
        ]
        for row in hidden {
            XCTAssertFalse(ConversationTimeline.classify(row).shownByDefault, row.id)
            XCTAssertEqual(category(row), .system, row.id)
        }
        // Whatever needs a tap stays on the page until the voice bar takes it over.
        let actionable = [
            "iOS kept the audio for something else. Tap the orb to resume.",
            "Could not pick the audio back up (x). Tap the orb to try again.",
            "Audio was reset by iOS — reconnect to start listening again.",
            "The microphone went away with the route change.",
            "Voice is off on the daemon (`--voice=off`), so it cannot hear the microphone.",
            "A notice this build has never seen",
        ]
        for text in actionable {
            XCTAssertTrue(ConversationTimeline.classify(local(9, .notice, text)).shownByDefault, text)
        }
        for category in [TimelineCategory.dialogue, .needsYou, .outcome, .context] {
            XCTAssertTrue(RowClass.plain(category).shownByDefault)
        }
    }

    func testOnlyNewsPushesFoldAndOnlyRestartsBridge() {
        XCTAssertTrue(ConversationTimeline.classify(.item(item(1, .push))).folds)
        XCTAssertTrue(ConversationTimeline.classify(local(1, .push(needsUser: false), "x")).folds)
        let others: [TimelineRow] = [
            .item(item(2, .push, urgency: "interrupt")), .item(item(3, .approval)), .item(item(4, .approval, resolution: .confirmed)),
            .item(item(5, .card)), .item(item(6, .dog)), .item(item(7, .shepherd)), .day(Date()),
            notice(8, "Voice context lost."), notice(9, "New conversation", subkind: "reset"),
        ]
        for row in others { XCTAssertFalse(ConversationTimeline.classify(row).folds, row.id) }
        XCTAssertTrue(ConversationTimeline.classify(notice(10, "Sheepdog started.")).bridgesRuns)
        XCTAssertFalse(ConversationTimeline.classify(notice(11, "New conversation", subkind: "reset")).bridgesRuns)
    }

    func testTheDisplayRowOfAGroupIsAScreenRow() {
        let rows: [TimelineRow] = (1...3).map { .item(item($0, .push)) }
        XCTAssertEqual(ConversationTimeline.grouped(rows).map(\.category), [.screen])
    }

    // MARK: - Nothing is lost

    private var mixed: [TimelineRow] {
        [
            .day(Date(timeIntervalSince1970: 1_790_000_000)),
            notice(1, "Voice context lost."),
            notice(2, "Voice reconnected with a fresh session; earlier context was lost."),
            .item(item(3, .shepherd, "status?")),
            .item(item(4, .approval)),
            local(1, .notice, "Audio moved to Speaker."),
            .item(item(5, .push)), .item(item(6, .push)), .item(item(7, .push)),
            notice(8, "Voice session closed: shutdown."),
            notice(9, "Sheepdog started."),
            .item(item(10, .dog, "ok")),
        ]
    }

    func testTheSwitchBringsBackEveryRowUnchanged() {
        let rows = mixed
        XCTAssertEqual(ConversationTimeline.visibleRows(rows, showTechnical: true), rows)
        let shown = ConversationTimeline.visibleRows(rows, showTechnical: false)
        XCTAssertEqual(shown.count, rows.count - 4, "two reconnect lines, the audio route and the shutdown")
        // Every hidden row is plumbing; nothing else is lost.
        let hidden = rows.filter { !shown.contains($0) }
        XCTAssertEqual(hidden.count, 4)
        XCTAssertTrue(hidden.allSatisfy { category($0) == .system })
    }

    func testQuestionsAndOutcomesAreNeverHiddenOrFolded() {
        let rows: [TimelineRow] = [
            .item(item(1, .approval)), .item(item(2, .approval, resolution: .confirmed)),
            .item(item(3, .push, urgency: "interrupt")), .item(item(4, .card)),
            .held(.init(key: "k", id: 5, ts: Date(timeIntervalSince1970: 1_790_000_005), text: "x")),
            .item(item(6, .push)),
        ]
        let shown = ConversationTimeline.visibleRows(rows, showTechnical: false)
        XCTAssertEqual(shown, rows)
        for display in ConversationTimeline.grouped(shown) {
            if case .updates = display { XCTFail("nothing here may fold: \(display.id)") }
        }
    }

    func testADayHeadingWithNothingUnderItIsDropped() {
        let day1 = Date(timeIntervalSince1970: 1_790_000_000)
        let day2 = Date(timeIntervalSince1970: 1_790_100_000)
        let rows: [TimelineRow] = [
            .day(day1), .item(item(1, .dog, "hi")),
            .day(day2), notice(2, "Voice context lost."),
        ]
        XCTAssertEqual(ConversationTimeline.visibleRows(rows, showTechnical: false).map(\.id), ["day.\(Int(day1.timeIntervalSince1970))", "item.1"])
        XCTAssertEqual(ConversationTimeline.visibleRows(rows, showTechnical: true).count, 4)
    }

    func testEveryRowHasRawJSON() {
        let rows = mixed + [
            .brain(.init(id: 20, ts: Date(), spoken: "ok", raw: "{}"), compact: false),
            .held(.init(key: "k", id: 21, ts: Date(), text: "x")),
            .card(ScreenCard(cardID: "c", kind: .card, at: Date(), title: "T", markdown: "- one")),
        ]
        for row in rows {
            XCTAssertTrue(row.rawJSON.hasPrefix("{"), "\(row.id): \(row.rawJSON)")
        }
        XCTAssertTrue(notice(1, "Voice context lost.").rawJSON.contains("Voice context lost."))
    }
}
