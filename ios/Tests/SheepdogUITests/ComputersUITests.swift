import XCTest

/// Several computers (docs/dev/design/app-redesign.md "The one rule for one
/// computer"), against `StubDaemon`s on loopback (no daemon checkout).
///
/// The one-computer snapshot: with one computer paired, nothing names a
/// computer and there is no Talk to. Every computer label in the app
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
        app.launchArguments = ["-sheepdogResetState", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()

        // The voice menu: the connection, Manage computers… and Start; no Talk to.
        tapMenuItem(app, menu: "voice.menu", item: "voice.manageComputers")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10), "Settings ▸ the one computer row")
        XCTAssertEqual(computerLabels(app).count, 0, "no computer label in Settings with one computer")
        app.buttons["settings.done"].firstMatch.tapIfExists()

        let menu = app.buttons["voice.menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        XCTAssertTrue(app.buttons["voice.manageComputers"].waitForExistence(timeout: 5))
        XCTAssertEqual(computerLabels(app).count, 0, "no Talk to with one computer")
        closeMenu(app, item: "voice.manageComputers")

        // The conversation and the Agents page name no computer.
        XCTAssertFalse(app.staticTexts["Work Mac"].exists, "the main screen never names the only computer")
        XCTAssertEqual(computerLabels(app).count, 0)
        showAgentsPane(app)
        sleep(1)
        XCTAssertFalse(app.staticTexts["Work Mac"].exists)
        XCTAssertEqual(computerLabels(app).count, 0, "one list, no computer sections")
    }
}

private extension XCUIElement {
    func tapIfExists() {
        if waitForExistence(timeout: 2), isHittable { tap() }
    }
}
