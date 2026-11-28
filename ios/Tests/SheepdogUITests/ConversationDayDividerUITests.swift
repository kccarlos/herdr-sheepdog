import XCTest

/// The "Today" day divider must sit in flow above the first item of the day,
/// stay visible and clear of reply bubbles when the conversation fits on one
/// screen, and stay clear of overflowing replies while keeping the view
/// anchored at the newest reply. Runs against `StubDaemon`.
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
        let t1 = max(t0.addingTimeInterval(1), now.addingTimeInterval(-30))

        // Turn 1: a short reply so the whole conversation fits on one screen
        // even at AX5 (both the "Today" divider and the reply bubble stay on
        // screen after keyboard dismiss).
        let shortReplyText = "All idle."

        // Turn 2: a multi-line reply tall enough to overflow the viewport at
        // both default text size and AX5.
        let longReplyText = (1 ... 14).map { index in
            "Status line \(index): test_repo factory is running task \(index) with clean checks."
        }.joined(separator: "\n")

        stub.conversationHandler = { typed in
            var items: [String] = [
                #"{"id":1790127618456001,"ts":"\#(iso.string(from: t0))","kind":"notice","subkind":"start","text":"Sheepdog started."}"#,
            ]
            for (index, text) in typed.enumerated() {
                let userID = 1_790_127_618_456_100 + index * 2
                let dogID = userID + 1
                let userTime = t1.addingTimeInterval(Double(index * 4))
                let dogTime = userTime.addingTimeInterval(1)
                let replyText = index == 0 ? shortReplyText : longReplyText
                items.append(#"{"id":\#(userID),"ts":"\#(iso.string(from: userTime))","kind":"shepherd","via":"typed","text":\#(Self.json(text))}"#)
                items.append(#"{"id":\#(dogID),"ts":"\#(iso.string(from: dogTime))","kind":"dog","via":"typed","text":\#(Self.json(replyText))}"#)
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
        let field = app.descendants(matching: .any).matching(identifier: "type.field").firstMatch
        let sendButton = app.buttons.matching(identifier: "type.send").firstMatch

        XCTAssertTrue(divider.waitForExistence(timeout: 15), "the Today divider exists before typing")
        XCTAssertTrue(startNotice.waitForExistence(timeout: 10), "earlier item from today loaded")

        // MARK: - 1. Short conversation (fits on one screen)

        tapWhenTappable(app.buttons.matching(identifier: "controls.type").firstMatch, "the keyboard button")
        typeWhenFocused(field, "status?")
        tapWhenTappable(sendButton, "the send button")

        let shortReply = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "bubble.sheepdog", shortReplyText))
            .firstMatch
        XCTAssertTrue(shortReply.waitForExistence(timeout: 15), "the short reply bubble exists")
        waitForKeyboardDismiss(app)
        usleep(400_000)

        XCTAssertTrue(divider.exists, "the Today divider remains on screen for a short conversation")
        XCTAssertTrue(startNotice.exists, "the earlier notice remains on screen for a short conversation")

        let screen = app.frame
        let shortComposerTop = field.frame.minY
        XCTAssertGreaterThan(shortComposerTop, screen.midY, "composer sits at the bottom (\(shortComposerTop) vs \(screen))")

        let shortDividerFrame = divider.frame
        let shortNoticeFrame = startNotice.frame
        let shortReplyFrame = shortReply.frame

        assertOnScreenAboveComposer(shortDividerFrame, screen: screen, composerTop: shortComposerTop, name: "Today divider (short)")
        assertOnScreenAboveComposer(shortNoticeFrame, screen: screen, composerTop: shortComposerTop, name: "first item (short)")
        assertOnScreenAboveComposer(shortReplyFrame, screen: screen, composerTop: shortComposerTop, name: "short reply bubble")
        XCTAssertFalse(
            shortDividerFrame.intersects(shortReplyFrame),
            "Today divider (\(shortDividerFrame)) must not intersect short reply bubble (\(shortReplyFrame))"
        )
        XCTAssertLessThanOrEqual(
            shortDividerFrame.maxY,
            shortNoticeFrame.minY + 1,
            "Today divider (\(shortDividerFrame)) must sit above the first item of today (\(shortNoticeFrame))"
        )
        XCTAssertLessThanOrEqual(
            shortNoticeFrame.maxY,
            shortReplyFrame.minY + 1,
            "first item (\(shortNoticeFrame)) must sit above the short reply bubble (\(shortReplyFrame))"
        )

        // MARK: - 2. Long conversation (overflows viewport; anchored at bottom)

        typeWhenFocused(field, "what is running?")
        tapWhenTappable(sendButton, "the send button")

        let longReply = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "bubble.sheepdog", "Status line 1:"))
            .firstMatch
        XCTAssertTrue(longReply.waitForExistence(timeout: 15), "the long reply bubble exists")
        waitForKeyboardDismiss(app)
        usleep(600_000)
        saveShot()

        let longComposerTop = field.frame.minY
        let longReplyFrame = longReply.frame
        XCTAssertFalse(longReplyFrame.isEmpty, "long reply bubble frame should be non-empty, got \(longReplyFrame)")
        XCTAssertLessThanOrEqual(
            longReplyFrame.maxY,
            longComposerTop,
            "long reply bottom (\(longReplyFrame.maxY)) should sit above the composer (\(longComposerTop))"
        )
        XCTAssertGreaterThan(
            longReplyFrame.maxY,
            screen.midY,
            "conversation should stay anchored at the bottom on the new reply (reply maxY \(longReplyFrame.maxY), screen midY \(screen.midY))"
        )

        // If the divider is still in the accessibility tree while anchored at
        // the bottom (e.g. a pinned header bug, or LazyVStack retention near
        // the top edge), it must not intersect the reply bubble.
        if let mountedDividerFrame = try? divider.snapshot().frame,
           mountedDividerFrame.minY.isFinite,
           !mountedDividerFrame.isEmpty {
            XCTAssertFalse(
                mountedDividerFrame.intersects(longReplyFrame),
                "Today divider (\(mountedDividerFrame)) must not intersect long reply bubble (\(longReplyFrame))"
            )
        }

        // Scroll back to the top (needed at AX5 and on overflowing timelines
        // where LazyVStack recycles off-screen headers) and confirm the divider
        // stays in flow above the first item of today.
        scrollUpToElement(app, element: divider, composerTop: longComposerTop)
        XCTAssertTrue(divider.waitForExistence(timeout: 5), "the Today divider exists after scrolling to top")
        XCTAssertTrue(startNotice.waitForExistence(timeout: 5), "the first item of today exists after scrolling to top")

        let topDividerFrame = divider.frame
        let topNoticeFrame = startNotice.frame
        assertOnScreenAboveComposer(topDividerFrame, screen: screen, composerTop: longComposerTop, name: "Today divider (top)")
        assertOnScreenAboveComposer(topNoticeFrame, screen: screen, composerTop: longComposerTop, name: "first item (top)")
        XCTAssertLessThanOrEqual(
            topDividerFrame.maxY,
            topNoticeFrame.minY + 1,
            "Today divider (\(topDividerFrame)) must sit above the first item of today (\(topNoticeFrame))"
        )
        if let topReplyFrame = try? longReply.snapshot().frame,
           topReplyFrame.minY.isFinite,
           !topReplyFrame.isEmpty {
            XCTAssertFalse(
                topDividerFrame.intersects(topReplyFrame),
                "Today divider (\(topDividerFrame)) must not intersect long reply bubble (\(topReplyFrame)) at top"
            )
        }
    }

    private func assertOnScreenAboveComposer(
        _ frame: CGRect,
        screen: CGRect,
        composerTop: CGFloat,
        name: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) {
        XCTAssertFalse(frame.isEmpty, "\(name) frame should be non-empty, got \(frame)", file: file, line: line)
        XCTAssertGreaterThanOrEqual(frame.minY, screen.minY, "\(name) (\(frame)) should be on screen (minY >= \(screen.minY))", file: file, line: line)
        XCTAssertLessThanOrEqual(frame.maxY, composerTop, "\(name) (\(frame)) should sit above the composer (\(composerTop))", file: file, line: line)
    }

    private func waitForKeyboardDismiss(_ app: XCUIApplication, timeout: TimeInterval = 5) {
        let deadline = Date().addingTimeInterval(timeout)
        while app.keyboards.count > 0, Date() < deadline {
            usleep(100_000)
        }
    }

    private func scrollUpToElement(_ app: XCUIApplication, element: XCUIElement, composerTop: CGFloat, maxSwipes: Int = 25) {
        let screen = app.frame
        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
        for _ in 0 ..< maxSwipes {
            if let frame = try? element.snapshot().frame,
               frame.minY.isFinite,
               !frame.isEmpty,
               frame.minY >= screen.minY,
               frame.maxY <= composerTop {
                return
            }
            start.press(forDuration: 0.02, thenDragTo: end, withVelocity: .fast, thenHoldForDuration: 0)
            usleep(150_000)
        }
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
