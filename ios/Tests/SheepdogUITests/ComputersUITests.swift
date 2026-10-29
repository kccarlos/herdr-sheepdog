import XCTest

/// Several computers (docs/dev/design/app-redesign.md "The one rule for one
/// computer"), against `StubDaemon`s on loopback (no daemon checkout).
///
/// The one-computer snapshot: with one computer paired, nothing names a
/// computer and there is no computer-level switch. Every computer label in the app
/// carries an identifier starting with `computerLabel.`, so this test
/// catches any new one.
final class ComputersUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func computerLabels(_ app: XCUIApplication) -> XCUIElementQuery {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'computerLabel.' OR identifier BEGINSWITH 'voice.talkTo'"))
    }

    func testOneComputerLooksExactlyAsItAlwaysDid() throws {
        let stub = try StubDaemon(.daemon(update: nil))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()

        // The voice menu: the connection, Manage computers… and Start; no Talk to.
        tapMenuItem(app, menu: "voice.menu", item: "voice.manageComputers")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10), "Settings ▸ the one computer row")
        XCTAssertEqual(computerLabels(app).count, 0, "no computer label in Settings with one computer")
        shot(app, "settings-one")
        app.buttons["settings.done"].firstMatch.tapIfExists()

        let menu = app.buttons["voice.menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        XCTAssertTrue(app.buttons["voice.manageComputers"].waitForExistence(timeout: 5))
        XCTAssertEqual(computerLabels(app).count, 0, "no Talk to with one computer")
        XCTAssertEqual(app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Talk to'")).count, 0, "no Talk to with one computer")
        XCTAssertFalse(starting(app, "Work Mac").exists, "the menu does not list the only computer")
        shot(app, "voice-menu-one")
        closeMenu(app, item: "voice.manageComputers")

        // The conversation and the Agents page name no computer.
        XCTAssertFalse(app.staticTexts["Work Mac"].exists, "the main screen never names the only computer")
        XCTAssertEqual(computerLabels(app).count, 0)
        showAgentsPane(app)
        sleep(1)
        XCTAssertFalse(app.staticTexts["Work Mac"].exists)
        XCTAssertEqual(computerLabels(app).count, 0, "one list, no computer sections")
        shot(app, "agents-one")
    }

    /// Two computers that answer and one that does not: the markers, the
    /// sections, the labelled question from another computer answered there
    /// by tap, Settings ▸ Computers, and switching.
    func testSeveralComputers() throws {
        let work = StubDaemon.blocked(alias: "api", space: "webapp", pane: "w1:p1", question: "Run the database migration?", created: "2026-10-23T09:05:00Z")
        let home = StubDaemon.blocked(alias: "writer", space: "blog", pane: "h1:p1", question: "Publish the post now?", created: "2026-10-23T09:00:00Z")
        let workStub = try StubDaemon(.computer(name: "Work Mac", agents: "[\(work.agent)]", questions: "[\(work.question)]"))
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[\(home.agent)]", questions: "[\(home.question)]"))
        let workHost = try workStub.start()
        let homeHost = try homeStub.start()
        defer { workStub.stop(); homeStub.stop() }

        let app = XCUIApplication()
        // Studio's port is closed: an offline computer.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(workHost);Home Mac=\(homeHost);Studio=127.0.0.1:9"]
        app.launch()

        // The conversation: Work Mac's question first, then Home Mac's,
        // labelled, with Talk to.
        let label = app.descendants(matching: .any).matching(identifier: "computerLabel.question.fixture-2").firstMatch
        XCTAssertTrue(label.waitForExistence(timeout: 20), "Home Mac's question is labelled")
        XCTAssertTrue(label.label.contains("Home Mac · blog"), label.label)
        XCTAssertTrue(app.staticTexts["Answers by tap go to Home Mac."].exists)
        XCTAssertTrue(app.buttons["computerLabel.talkTo.fixture-2"].exists)
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Hi from Work Mac")).firstMatch.waitForExistence(timeout: 10), "Work Mac's conversation")
        shot(app, "conversation")

        // Talk to in the voice menu, with every computer.
        let menu = app.buttons["voice.menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        // (A menu drops its items' identifiers, so these go by label.)
        openMenu(app, "voice.menu", until: "voice.manageComputers")
        XCTAssertTrue(starting(app, "Work Mac, talking here").waitForExistence(timeout: 5), "the current computer, checked")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Talk to Home Mac'")).count >= 2, "the menu's and the question's")
        let studio = starting(app, "Talk to Studio")
        XCTAssertTrue(studio.exists)
        XCTAssertFalse(studio.isEnabled, "an offline computer cannot be picked")
        shot(app, "voice-menu")
        closeMenu(app, item: "voice.manageComputers")

        // The Agents page: one section per computer, in the store's order.
        showAgentsPane(app)
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "computerLabel.section.fixture-1").firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "computerLabel.section.fixture-2").firstMatch.exists)
        let workMarker = app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-1").firstMatch
        let homeMarker = app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-2").firstMatch
        XCTAssertTrue(workMarker.exists)
        XCTAssertTrue(workMarker.isSelected)
        XCTAssertEqual(workMarker.label, "Voice is on Work Mac")
        XCTAssertEqual(homeMarker.label, "Move the voice to Home Mac")
        assertHeadersAlignedAndInOrder(app, ["fixture-1", "fixture-2"])
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "computerLabel.offline.Studio").firstMatch.waitForExistence(timeout: 45), "Studio's dashed row, after the 30 s quiet period")
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-3").firstMatch.label, "Retry Studio")
        shot(app, "agents")

        // Answer Home Mac's question by tap: it goes to Home Mac.
        let yes = app.buttons["question.writer.option.1"]
        XCTAssertTrue(yes.waitForExistence(timeout: 5))
        yes.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "question.writer.answered").firstMatch.waitForExistence(timeout: 10), "answered on Home Mac")

        // Settings ▸ Computers and one computer's page.
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        let homeRow = app.buttons["computerLabel.settings.fixture-2"]
        XCTAssertTrue(homeRow.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["settings.addComputer"].exists)
        shot(app, "settings")
        homeRow.tap()
        XCTAssertTrue(app.textFields["computer.name"].waitForExistence(timeout: 5))
        shot(app, "computer-page")

        // Talk to Home Mac from its page: the voice moves there.
        app.buttons["computer.talkTo"].tap()
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        XCTAssertTrue(menu.label.contains("Home Mac"), menu.label)
        openMenu(app, "voice.menu", until: "voice.manageComputers")
        XCTAssertTrue(starting(app, "Talk to Work Mac").waitForExistence(timeout: 5))
        XCTAssertTrue(starting(app, "Home Mac, talking here").exists, "Home Mac is current now")
        closeMenu(app, item: "voice.manageComputers")

        // The conversation is Home Mac's now, caught up at once, with
        // nothing of Work Mac's.
        tapWhenTappable(app.buttons.matching(identifier: "main.pane.conversation").firstMatch, "the Conversation tab")
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Hi from Home Mac")).firstMatch.waitForExistence(timeout: 10), "Home Mac's conversation after the switch")
        XCTAssertFalse(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Hi from Work Mac")).firstMatch.exists, "nothing of Work Mac's conversation")

        // Moving the voice did not reorder anything: Work Mac is still above
        // Home Mac on the Agents page, Home Mac (current) in its place, and
        // the headers still start at the same x.
        showAgentsPane(app)
        assertHeadersAlignedAndInOrder(app, ["fixture-1", "fixture-2"])
        shot(app, "agents-after-talk-to")
    }

    /// At a large text size the voice marker stays at the trailing edge and
    /// every header is still leading-aligned.
    func testStackedSectionHeadersAreLeadingAligned() throws {
        let workStub = try StubDaemon(.computer(name: "Work Mac", agents: #"[{"pane_id":"w1:p1","alias":"api","kind":"claude","state":"working","workspace_label":"webapp"}]"#, questions: "[]"))
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[]", questions: "[]"))
        let workHost = try workStub.start()
        let homeHost = try homeStub.start()
        defer { workStub.stop(); homeStub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(workHost);Home Mac=\(homeHost)",
                               "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityL"]
        app.launch()
        showAgentsPane(app)
        assertHeadersAlignedAndInOrder(app, ["fixture-1", "fixture-2"])
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-2").firstMatch.exists)
    }

    /// A section's name folds only its agents; its marker is the switch.
    func testSectionNameCollapsesAgentsAndMarkerMovesVoice() throws {
        let workStub = try StubDaemon(.computer(
            name: "Work Mac",
            agents: #"[{"pane_id":"w1:p1","alias":"api","kind":"claude","state":"working","workspace_label":"webapp"}]"#,
            questions: "[]"
        ))
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[]", questions: "[]"))
        let workHost = try workStub.start()
        let homeHost = try homeStub.start()
        defer { workStub.stop(); homeStub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(workHost);Home Mac=\(homeHost)"]
        app.launch()
        showAgentsPane(app)

        let workName = app.buttons["computerLabel.section.fixture-1"]
        let workCard = app.descendants(matching: .any).matching(identifier: "flock.agent.api").firstMatch
        XCTAssertTrue(workName.waitForExistence(timeout: 10))
        XCTAssertTrue(workCard.waitForExistence(timeout: 10))
        tapWhenTappable(workName, "Work Mac's section name")
        XCTAssertTrue(workCard.waitForNonExistence(timeout: 5), "the name folds the current computer's agents")
        tapWhenTappable(workName, "Work Mac's section name")
        XCTAssertTrue(workCard.waitForExistence(timeout: 5), "the name expands them again")

        let homeMarker = app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-2").firstMatch
        XCTAssertEqual(homeMarker.label, "Move the voice to Home Mac")
        tapWhenTappable(homeMarker, "Home Mac's voice marker")

        let current = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == %@ AND label == %@",
            "computer.voiceMarker.fixture-2",
            "Voice is on Home Mac"
        )).firstMatch
        let previous = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == %@ AND label == %@",
            "computer.voiceMarker.fixture-1",
            "Move the voice to Work Mac"
        )).firstMatch
        XCTAssertTrue(current.waitForExistence(timeout: 10))
        XCTAssertTrue(current.isSelected)
        XCTAssertTrue(previous.waitForExistence(timeout: 10))
    }

    /// The Agents page's section headers (ids as in
    /// `computerLabel.section.<id>`): leading aligned (same minX) and top to
    /// bottom in `order`, wherever the current computer is.
    private func assertHeadersAlignedAndInOrder(_ app: XCUIApplication, _ order: [String], file: StaticString = #filePath, line: UInt = #line) {
        let frames = order.map { id -> CGRect in
            let element = app.descendants(matching: .any).matching(identifier: "computerLabel.section.\(id)").firstMatch
            XCTAssertTrue(element.waitForExistence(timeout: 10), "header \(id)", file: file, line: line)
            return element.frame
        }
        for frame in frames.dropFirst() {
            XCTAssertEqual(frame.minX, frames[0].minX, accuracy: 0.5, "headers are leading-aligned", file: file, line: line)
        }
        XCTAssertEqual(frames.map(\.minY), frames.map(\.minY).sorted(), "sections keep the store's order", file: file, line: line)
    }

    /// Talk to from the voice menu while the Conversation is in front,
    /// scrolled up in a long conversation: the new computer's conversation
    /// shows at its latest row at once, with no Jump to latest tap and
    /// nothing of the old computer's.
    func testSwitchShowsTheNewConversationAtItsLatestRow() throws {
        let workStub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", history: 60))
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[]", questions: "[]", history: 12, idBase: 5000))
        let workHost = try workStub.start()
        let homeHost = try homeStub.start()
        defer { workStub.stop(); homeStub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(workHost);Home Mac=\(homeHost)"]
        app.launch()

        func text(_ needle: String) -> XCUIElement {
            app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", needle)).firstMatch
        }
        XCTAssertTrue(text("Latest from Work Mac").waitForExistence(timeout: 20), "Work Mac's latest row")
        // Scroll up, away from the latest row.
        for _ in 0 ..< 2 { app.swipeDown() }

        openMenu(app, "voice.menu", until: "voice.manageComputers")
        let talk = starting(app, "Talk to Home Mac")
        XCTAssertTrue(talk.waitForExistence(timeout: 5))
        talk.tap()

        let latest = text("Latest from Home Mac")
        XCTAssertTrue(latest.waitForExistence(timeout: 10), "Home Mac's latest row")
        XCTAssertTrue(latest.isHittable, "the latest row is on screen without tapping Jump to latest")
        XCTAssertFalse(text("Work Mac.").exists, "nothing of Work Mac's conversation")
    }

    /// Settings ▸ Add a computer while the session view model re-renders
    /// ten times a second (as during a live voice session): the scan sheet
    /// opens once and stays. It used to close and reopen on every
    /// re-render, because its identity was minted in the binding's getter.
    func testAddComputerSheetStaysWhileTheSessionRerenders() throws {
        let stub = try StubDaemon(.daemon(update: nil))
        let host = try stub.start()
        defer { stub.stop() }
        let churnAfter = 20.0

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogComputers", "Work Mac=\(host)", "-sheepdogNoAnimations", "-sheepdogRenderChurnAfter", "\(churnAfter)"]
        let launched = Date()
        app.launch()

        // Reach Settings first, with the session still: the churn starts
        // later (as #407 did for the shell and Spaces), so a tap on the
        // capsule or its menu is not lost to ten re-renders a second.
        let add = app.buttons["settings.addComputer"]
        tapMenuItem(app, menu: "voice.menu", item: "voice.manageComputers", until: add)
        tapWhenTappable(add, "Add a computer")

        let sheet = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'pairing.'")).firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 10), "the scan sheet opens")
        let id = sheet.identifier
        // The churn is on from here on.
        let wait = churnAfter - Date().timeIntervalSince(launched)
        if wait > 0 { usleep(UInt32(wait * 1_000_000)) }
        // Stays up, the same screen, for several seconds of re-renders.
        let done = app.buttons["settings.done"]
        for i in 0 ..< 40 {
            usleep(200_000)
            let current = app.descendants(matching: .any).matching(identifier: id).firstMatch
            XCTAssertTrue(current.exists, "the scan sheet is still up after \(i * 200 + 200) ms")
            XCTAssertFalse(done.isHittable, "Settings stays covered after \(i * 200 + 200) ms")
        }
        shot(app, "add-computer")
    }

    /// The first button whose label starts with `prefix`.
    private func starting(_ app: XCUIApplication, _ prefix: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
    }

    /// Opens a menu until one of its items shows (a tap can be lost while
    /// something still animates).
    private func openMenu(_ app: XCUIApplication, _ menu: String, until item: String) {
        var seen: [String] = []
        for _ in 0 ..< 4 {
            let button = app.buttons[menu]
            if button.waitForExistence(timeout: 5), button.isTappable { button.tap() }
            if app.buttons[item].waitForExistence(timeout: 3) { return }
            seen = app.buttons.allElementsBoundByIndex.map { "\($0.identifier)=\($0.label)" }
            closeMenu(app)
        }
        XCTFail("no \(item) in \(menu): \(seen)")
    }

    /// Light and dark screenshots for the design review, when
    /// SHEEPDOG_SHOTS_DIR is set (SHEEPDOG_SHOTS_MODE: light or dark).
    private func shot(_ app: XCUIApplication, _ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_SHOTS_DIR"], !dir.isEmpty else { return }
        sleep(1)
        let mode = env["SHEEPDOG_SHOTS_MODE"] ?? "light"
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("multi-computer-\(name)-\(mode).png"))
    }
}

private extension XCUIElement {
    func tapIfExists() {
        if waitForExistence(timeout: 2), isTappable { tap() }
    }
}
