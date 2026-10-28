import XCTest
@testable import SheepdogKit

/// Phase 3 of the conversation timeline (docs/dev/design/
/// conversation-timeline.md, section 13): the daemon's notice `subkind`,
/// `reason` and `version`, with and without them (an older daemon sends
/// none, and the words decide as before).
final class DaemonNoticeFieldsTests: XCTestCase {
    private func decode(_ items: String) throws -> [ConversationItem] {
        let json = #"{"items":[\#(items)],"next_before":null}"#
        return try DaemonClient.decoder.decode(ConversationPage.self, from: Data(json.utf8)).items
    }

    private func formatter() -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        f.dateFormat = "h:mm a"
        return f
    }

    // MARK: - Decoding

    func testDecodesSubkindReasonAndVersion() throws {
        let items = try decode("""
        {"id":1,"ts":"2026-10-28T09:00:00Z","kind":"notice","subkind":"start","version":"0.9.1","text":"Sheepdog started."},
        {"id":2,"ts":"2026-10-28T09:01:00Z","kind":"notice","subkind":"closed","reason":"shutdown","text":"Voice session closed: shutdown."},
        {"id":3,"ts":"2026-10-28T09:02:00Z","kind":"approval","resolution":"cancelled","reason":"prompt_gone","text":"Codex asks to write a file."}
        """)
        XCTAssertEqual(items[0].subkind, "start")
        XCTAssertEqual(items[0].version, "0.9.1")
        XCTAssertEqual(items[1].reason, "shutdown")
        XCTAssertEqual(items[2].reason, "prompt_gone")
    }

    func testAnOlderDaemonsItemsHaveNoneOfThem() throws {
        let items = try decode(#"{"id":1,"ts":"2026-10-28T09:00:00Z","kind":"notice","text":"Sheepdog started."}"#)
        XCTAssertNil(items[0].subkind)
        XCTAssertNil(items[0].reason)
        XCTAssertNil(items[0].version)
    }

    // MARK: - Classification by field

    func testTheSubkindDecidesWhateverTheWords() {
        let table: [(String, DaemonNotice)] = [
            ("start", .restarted), ("reset", .reset), ("context_lost", .contextLost),
            ("fresh", .freshReconnect), ("resumed", .resumedReconnect), ("closed", .closed),
        ]
        for (subkind, expected) in table {
            let item = ConversationItem(id: 1, ts: Date(), kind: .notice, text: "words this build has never seen", subkind: subkind)
            XCTAssertEqual(DaemonNotice(item), expected, subkind)
        }
    }

    func testWithoutASubkindTheWordsStillDecide() {
        let table: [(String, DaemonNotice)] = [
            ("Sheepdog started.", .restarted), ("Voice context lost.", .contextLost),
            ("Voice reconnected with a fresh session; earlier context was lost.", .freshReconnect),
            ("Voice reconnected; the conversation continues.", .resumedReconnect),
            ("Voice session closed: shutdown.", .closed), ("Something new.", .other),
        ]
        for (text, expected) in table {
            XCTAssertEqual(DaemonNotice(ConversationItem(id: 1, ts: Date(), kind: .notice, text: text)), expected, text)
        }
    }

    func testAnUnknownSubkindFallsBackToTheWords() {
        let item = ConversationItem(id: 1, ts: Date(), kind: .notice, text: "Sheepdog started.", subkind: "from_the_future")
        XCTAssertEqual(DaemonNotice(item), .restarted)
        let other = ConversationItem(id: 2, ts: Date(), kind: .notice, text: "Hello.", subkind: "from_the_future")
        XCTAssertEqual(DaemonNotice(other), .other)
    }

    func testTheCategoryFollowsTheSubkind() {
        func category(_ subkind: String) -> TimelineCategory {
            ConversationTimeline.classify(ConversationItem(id: 1, ts: Date(), kind: .notice, text: "x", subkind: subkind)).category
        }
        XCTAssertEqual(category("start"), .context)
        XCTAssertEqual(category("reset"), .context)
        XCTAssertEqual(category("closed"), .system)
        XCTAssertEqual(category("context_lost"), .system)
        XCTAssertEqual(category("fresh"), .system)
        XCTAssertEqual(category("resumed"), .system)
    }

    // MARK: - The restart divider

    private func start(_ id: Int64, version: String?, text: String = "Sheepdog started.") -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000 + TimeInterval(id) * 100), kind: .notice, text: text, subkind: version == nil ? nil : "start", version: version)
    }

    private func titles(_ history: [ConversationItem]) -> [String] {
        let rows = ConversationTimeline.rows(history: history, live: [], timeFormatter: formatter())
        return rows.compactMap { if case let .divider(_, title) = $0 { return title.components(separatedBy: " · ")[0] } else { return nil } }
    }

    func testAChangedVersionSaysUpdated() {
        let said = ConversationItem(id: 5, ts: Date(timeIntervalSince1970: 1_790_000_500), kind: .shepherd, text: "hi")
        XCTAssertEqual(titles([start(1, version: "0.9.0"), said, start(6, version: "0.9.1")]), ["Sheepdog started", "Sheepdog updated to v0.9.1"])
    }

    func testTheSameVersionSaysRestarted() {
        let said = ConversationItem(id: 5, ts: Date(timeIntervalSince1970: 1_790_000_500), kind: .shepherd, text: "hi")
        XCTAssertEqual(titles([start(1, version: "0.9.1"), said, start(6, version: "0.9.1")]), ["Sheepdog started", "Sheepdog restarted"])
    }

    func testNoPreviousOrNoVersionSaysRestarted() {
        let said = ConversationItem(id: 5, ts: Date(timeIntervalSince1970: 1_790_000_500), kind: .shepherd, text: "hi")
        // The first start in the history has nothing to compare with.
        XCTAssertEqual(titles([said, start(6, version: "0.9.1")]), ["Sheepdog restarted"])
        // An older daemon's start carries no version, whatever came before.
        XCTAssertEqual(titles([start(1, version: "0.9.0"), said, start(6, version: nil)]), ["Sheepdog started", "Sheepdog restarted"])
    }

    func testANonNumericVersionIsShownAsItIs() {
        let said = ConversationItem(id: 5, ts: Date(timeIntervalSince1970: 1_790_000_500), kind: .shepherd, text: "hi")
        XCTAssertEqual(titles([start(1, version: "0.9.0"), said, start(6, version: "dev")]), ["Sheepdog started", "Sheepdog updated to dev"])
    }

    // MARK: - Why a question closed

    func testAClosedQuestionSaysWhy() {
        func why(_ resolution: ConversationItem.Resolution, _ reason: String?) -> String {
            ConversationItem(id: 1, ts: Date(), kind: .approval, text: "q", resolution: resolution, reason: reason).closedReasonText
        }
        XCTAssertEqual(why(.expired, "expired"), "It expired before anyone answered.")
        XCTAssertEqual(why(.replaced, "replaced"), "A newer question replaced it.")
        XCTAssertEqual(why(.cancelled, "prompt_gone"), "It left the agent's screen, probably answered on the computer.")
        XCTAssertEqual(why(.cancelled, "agent_exited"), "The agent exited.")
        XCTAssertEqual(why(.cancelled, "pane_closed"), "The agent's window was closed.")
        XCTAssertEqual(why(.cancelled, "closed"), "The agent's window was closed.")
    }

    func testWithoutAReasonTheFallbackStays() {
        func why(_ resolution: ConversationItem.Resolution, _ reason: String?) -> String {
            ConversationItem(id: 1, ts: Date(), kind: .approval, text: "q", resolution: resolution, reason: reason).closedReasonText
        }
        // An older daemon: the resolution alone, as before.
        XCTAssertEqual(why(.expired, nil), "It expired before anyone answered.")
        XCTAssertEqual(why(.replaced, nil), "A newer question replaced it.")
        XCTAssertEqual(why(.cancelled, nil), "It was closed on the computer.")
        // A reason this build does not know.
        XCTAssertEqual(why(.cancelled, "from_the_future"), "It was closed on the computer.")
    }
}
