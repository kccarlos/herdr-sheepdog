import XCTest

/// The "Today" day divider must sit above the first item of the day and never
/// overlay a reply bubble after a typed turn. Runs against `StubDaemon`.
final class ConversationDayDividerUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testTodayDividerDoesNotOverlapTypedReplyBubble() throws {
        let factoryHead = #"[{"id":"proj-1","name":"test_repo","goal":"Create 2 tasks","state":"running","counts":{"working":1,"done":0},"folder":"~/code/test_repo"}]"#
        let stub = try StubDaemon(.computer(name: "QA factory", agents: "[]", questions: "[]", history: 0, factory: factoryHead))

        let now = Date()
        let startOfToday = Calendar.current.startOfDay(for: now)
        let iso = ISO8601DateFormatter()
        iso.formatOptions = [.withInternetDateTime]
        let t0 = max(startOfToday.addingTimeInterval(1), now.addingTimeInterval(-900))
        let t1 = max(t0.addingTimeInterval(1), now.addingTimeInterval(-10))
        let t2 = t1.addingTimeInterval(1)

        // A multi-paragraph reply tall enough that after sending the typed turn,
        // the conversation overflows the viewport slightly and the top of the
        // reply bubble sits near the top of the scroll view. A pinned "Today"
        // section header overlays the reply bubble here instead of staying above
        // the first item of today.
        let replyText = (1 ... 14).map { index in
            "Status line \(index): test_repo factory is running task \(index) with clean checks."
        }.joined(separator: "\n")

        stub.conversationHandler = { typed in
            var items: [String] = [
                #"{"id":1790127618456001,"ts":"\#(iso.string(from: t0))","kind":"notice","subkind":"start","text":"Sheepdog started."}"#,
            ]
            for (index, text) in typed.enumerated() {
                let userID = 1_790_127_618_456_100 + index * 2
                let dogID = userID + 1
                items.append(#"{"id":\#(userID),"ts":"\#(iso.string(from: t1))","kind":"shepherd","via":"typed","text":\#(Self.json(text))}"#)
                items.append(#"{"id":\#(dogID),"ts":"\#(iso.string(from: t2))","kind":"dog","via":"typed","text":\#(Self.json(replyText))}"#)
            }
            return #"{"items":[\#(items.joined(separator: ","))]}"#
        }

        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "QA factory=\(host)"]
        app.launch()

        let divider = app.descendants(matching: .any).matching(identifier: "divider.day").firstMatch
        let startNotice = app.descendants(matching: .any).matching(identifier: "divider.notice").firstMatch
        XCTAssertTrue(divider.waitForExistence(timeout: 15), "the Today divider exists before typing")
        XCTAssertTrue(startNotice.waitForExistence(timeout: 10), "earlier item from today loaded")

        // Open the typing composer, send a typed question, and wait for the reply.
        tapWhenTappable(app.buttons["controls.type"], "the keyboard button")
        typeWhenFocused(app.descendants(matching: .any)["type.field"], "what is running?")
        tapWhenTappable(app.buttons["type.send"], "the send button")

        let reply = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "bubble.sheepdog", "Status line 1:"))
            .firstMatch
        XCTAssertTrue(reply.waitForExistence(timeout: 15), "the reply bubble exists")
        XCTAssertTrue(divider.waitForExistence(timeout: 5), "the Today divider exists")
        usleep(600_000)
        saveShot()

        let dividerFrame = divider.frame
        let replyFrame = reply.frame
        let noticeFrame = startNotice.frame

        XCTAssertFalse(dividerFrame.isEmpty, "Today divider frame should be non-empty, got \(dividerFrame)")
        XCTAssertFalse(replyFrame.isEmpty, "reply bubble frame should be non-empty, got \(replyFrame)")
        XCTAssertFalse(noticeFrame.isEmpty, "first item frame should be non-empty, got \(noticeFrame)")
        XCTAssertFalse(
            dividerFrame.intersects(replyFrame),
            "Today divider (\(dividerFrame)) must not intersect reply bubble (\(replyFrame))"
        )
        XCTAssertLessThanOrEqual(
            dividerFrame.maxY,
            noticeFrame.minY + 1,
            "Today divider (\(dividerFrame)) must sit above the first item of today (\(noticeFrame))"
        )
    }

    private func saveShot() {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_TODAY_SHOTS"], let name = env["SHEEPDOG_TODAY_SHOT_NAME"] else { return }
        let url = URL(fileURLWithPath: dir)
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: url.appendingPathComponent("\(name).png"))
    }

    private static func json(_ text: String) -> String {
        let data = (try? JSONSerialization.data(withJSONObject: [text])) ?? Data(#"[""]"#.utf8)
        return String(String(decoding: data, as: UTF8.self).dropFirst().dropLast())
    }
}
