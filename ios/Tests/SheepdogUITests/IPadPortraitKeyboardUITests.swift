import XCTest

/// The terminal's keyboard on an iPad held upright (the CEO's TestFlight
/// 0.1.23 report: in portrait the keyboard key and a shell's typing mode
/// brought no keyboard up; landscape worked). Offline demo, no daemon.
/// Skipped on iPhone; ios.yml runs these on an iPad simulator.
///
/// `TEST_RUNNER_SHEEPDOG_IPAD_SHOTS=<dir>` saves screenshots there
/// (`TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX`, light by default).
final class IPadPortraitKeyboardUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .portrait
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_IPAD_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(suffix).png"))
    }

    /// Portrait demo with an agent's window open in the detail.
    private func openAgent() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(element(app, "demo.header").waitForExistence(timeout: 20))
        tapWhenTappable(element(app, "sidebar.show"), "Show sidebar")
        tapWhenTappable(element(app, "sidebar.agent.claude-auth"), "the agent's sidebar row")
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10))
        return app
    }

    func testKeyboardKeyBringsUpTheKeyboardInPortrait() throws {
        let app = openAgent()
        tapWhenTappable(app.buttons.matching(identifier: "agentScreen.key.typing").firstMatch, "the keyboard key")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "the keyboard comes up in portrait")
        XCTAssertTrue(element(app, "agentScreen.typingHint").waitForExistence(timeout: 5))
        sleep(1)
        shot("ipad-portrait-keyboard")
    }

    /// The chat's keyboard on an iPad held upright with the sidebar hidden:
    /// the capsule's keyboard button turns it into the composer (it used to
    /// have no keyboard button there, and the toolbar's mini capsule showed
    /// one circle).
    func testChatKeyboardWithTheSidebarHiddenInPortrait() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(element(app, "demo.header").waitForExistence(timeout: 20))
        XCTAssertTrue(element(app, "sidebar.show").waitForExistence(timeout: 10), "the sidebar is hidden in portrait")
        XCTAssertTrue(element(app, "controls.orb").exists, "the capsule's orb")
        XCTAssertTrue(element(app, "controls.mute").exists, "and its mic slot")
        shot("ipad-capsule-portrait")
        tapWhenTappable(element(app, "controls.type"), "the capsule's keyboard button")
        let field = app.textFields.matching(identifier: "type.field").firstMatch
        typeWhenFocused(field, "status please")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "the keyboard comes up")
        sleep(1)
        shot("ipad-capsule-portrait-composer")
        tapWhenTappable(element(app, "type.close"), "Back to voice")
        XCTAssertTrue(element(app, "voice.capsule").waitForExistence(timeout: 5), "the capsule comes back")
    }

    func testShellWindowTypesInPortrait() throws {
        let app = openAgent()
        tapWhenTappable(app.buttons.matching(identifier: "agentScreen.menu").firstMatch, "the window menu")
        tapWhenTappable(app.buttons.matching(identifier: "agentScreen.menu.shell").firstMatch, "Open shell here")
        XCTAssertTrue(element(app, "shell.typingHint").waitForExistence(timeout: 10))
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "the shell's keyboard comes up in portrait")
        for key in ["l", "s"] { app.keyboards.keys[key].tap() }
        let ret = app.keyboards.buttons.matching(NSPredicate(format: "label ==[c] 'return'")).firstMatch
        if ret.exists { ret.tap() } else { app.keyboards.keys.matching(NSPredicate(format: "label ==[c] 'return'")).firstMatch.tap() }
        let listed = app.descendants(matching: .any).matching(identifier: "agentScreen.terminal")
            .matching(NSPredicate(format: "value CONTAINS 'package.json'")).firstMatch
        XCTAssertTrue(listed.waitForExistence(timeout: 10), "what was typed reached the shell")
        shot("ipad-portrait-shell-keyboard")
    }
}
