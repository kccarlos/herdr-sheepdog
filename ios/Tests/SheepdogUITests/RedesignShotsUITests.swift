import XCTest

/// Screenshots of the redesigned screens (docs/dev/design/app-redesign.md),
/// in the offline demo: no daemon, no network. Runs only when asked:
///
///     xcodebuild … test -only-testing:SheepdogUITests/RedesignShotsUITests \
///       TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<directory> \
///       TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX=light
///
/// Each screen lands as `ui-<screen>-<suffix>.png`. The appearance and the
/// text size are the simulator's (`xcrun simctl ui <id> appearance dark`,
/// `xcrun simctl ui <id> content_size accessibility-extra-large`).
final class RedesignShotsUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var directory: String? { environment["SHEEPDOG_REDESIGN_SHOTS"] }
    private var suffix: String { environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light" }
    /// `ui` by default; `SHEEPDOG_SHOTS_PREFIX=ui2` keeps a later round apart.
    private var prefix: String { environment["SHEEPDOG_SHOTS_PREFIX"] ?? "ui" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard directory != nil else { throw XCTSkip("set SHEEPDOG_REDESIGN_SHOTS to take the redesign screenshots") }
    }

    func testSplash() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:9", "-sheepdogSplashHold"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["splash"].waitForExistence(timeout: 10))
        sleep(1)
        shot("splash")
    }

    func testCapsuleModes() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogCapsuleGallery"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["capsule.gallery"].waitForExistence(timeout: 10))
        sleep(2)
        shot("capsule-modes")
        app.swipeUp()
        sleep(1)
        shot("capsule-modes-2")
    }

    func testTopBarCapsuleAndMenus() throws {
        let app = launchDemo()
        sleep(3)
        shot("conversation")
        selectPane(app, "flock")
        sleep(2)
        shot("agents")
        tapOnScreen(app, "voice.menu")
        sleep(1)
        shot("voice-menu")
        dismissMenu(app)
        tapOnScreen(app, "toolbar.more")
        sleep(1)
        shot("more-menu")
        dismissMenu(app)
        tapOnScreen(app, "toolbar.new")
        sleep(1)
        shot("plus-menu")
        dismissMenu(app)
        selectPane(app, "conversation")
        tapOnScreen(app, "controls.type")
        let field = app.textFields["type.field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("and remind me when search is done")
        sleep(1)
        shot("composer")
    }

    func testAgentsPage() throws {
        let app = launchDemo()
        selectPane(app, "flock")
        // codex-payments asks after about 16 s; claude-auth is done at 8 s.
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40))
        sleep(2)
        shot("agents-waiting")
        let toggle = app.buttons["flock.agent.claude-auth.toggle"].firstMatch
        if toggle.waitForExistence(timeout: 5) { toggle.tap() }
        app.swipeUp()
        sleep(1)
        shot("agents-expanded")
        tapOnScreen(app, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.buttons["agentScreen.key.more"].waitForExistence(timeout: 10))
        sleep(2)
        shot("agent-window")
        app.buttons["agentScreen.key.more"].tap()
        XCTAssertTrue(app.buttons["moreKeys.ctrlC"].waitForExistence(timeout: 5))
        sleep(1)
        shot("more-keys")
    }

    /// The Conversation once the demo has news and a question: the agent
    /// strip, the rows and the floating question.
    func testConversationWithQuestion() throws {
        let app = launchDemo()
        // codex-payments asks after about 16 s.
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40))
        sleep(2)
        shot("conversation-question")
        app.swipeDown()
        sleep(1)
        shot("conversation-top")
    }

    /// A ScreenCard's sheet, then its row and a question asked by tap.
    func testCardSheetAndRows() throws {
        let app = launchDemo(["-sheepdogDemoCard"])
        let title = app.descendants(matching: .any)["card.title"]
        XCTAssertTrue(title.waitForExistence(timeout: 45))
        sleep(2)
        shot("card-sheet")
        app.buttons["card.done"].firstMatch.tap()
        sleep(1)
        tapOnScreen(app, "demo.question.0")
        sleep(4)
        shot("conversation-rows")
    }

    func testEmptyConversation() throws {
        // A saved address with nothing behind it: no history, no banner
        // until the orb is tapped.
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:9"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["conversation.empty"].waitForExistence(timeout: 10))
        sleep(1)
        shot("conversation-empty")
    }

    func testPrimers() throws {
        for primer in ["microphone", "notifications"] {
            let app = XCUIApplication()
            app.launchArguments = ["-sheepdogShowPrimer", primer]
            app.launch()
            XCTAssertTrue(app.descendants(matching: .any)["primer.\(primer)"].waitForExistence(timeout: 10))
            sleep(1)
            shot("primer-\(primer)")
            app.terminate()
        }
    }

    func testSettings() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host]
        app.launch()
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10))
        sleep(2)
        shot("settings")
        app.buttons["settings.daemon"].tap()
        sleep(2)
        shot("settings-connection")
    }

    // MARK: - Helpers

    /// Conversation or Agents: the copy on screen (the demo keeps the
    /// real main view under its cover).
    func selectPane(_ app: XCUIApplication, _ pane: String) {
        let matches = app.buttons.matching(identifier: "main.pane.\(pane)")
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 5))
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            break
        }
        sleep(1)
    }

    /// Taps the copy of an element that is on screen.
    func tapOnScreen(_ app: XCUIApplication, _ id: String) {
        let matches = app.buttons.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            return
        }
        XCTFail("no visible \(id)")
    }

    /// Closes an open menu by tapping the top of the screen.
    func dismissMenu(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.04)).tap()
        sleep(2)
    }

    /// The demo, opened at launch, silent (no speech, no permission alerts).
    func launchDemo(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo"] + extra
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15), "the demo opened")
        return app
    }

    func shot(_ name: String) {
        guard let directory else { return }
        let screenshot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(prefix)-\(name)-\(suffix)"
        attachment.lifetime = .keepAlways
        add(attachment)
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(prefix)-\(name)-\(suffix).png"))
    }
}
