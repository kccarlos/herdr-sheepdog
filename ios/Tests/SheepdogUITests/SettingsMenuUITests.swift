import XCTest

/// Settings as a shallow menu that applies changes at once, and the Flock
/// pane's error state. The first needs no daemon beyond a saved address; the
/// second points the app at a port nothing listens on.
final class SettingsMenuUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testSettingsIsAShallowMenuWithNoSaveOrCancel() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice", "-sheepdogFakeProviders"]
        app.launch()
        tapWhenReady(app.buttons["toolbar.settings"], "Settings")

        for row in ["daemon", "voice", "models", "languages", "notifications", "diagnostics", "about"] {
            XCTAssertTrue(app.buttons["settings.\(row)"].waitForExistence(timeout: 10), "Settings ▸ \(row) is on the menu")
        }
        XCTAssertFalse(app.buttons["Save"].exists, "changes apply at once")
        XCTAssertFalse(app.buttons["Cancel"].exists, "changes apply at once")
        XCTAssertTrue(app.buttons["settings.done"].isEnabled)

        // A switch applies without anything to confirm it: flip it, go
        // back, come in again, and it is still flipped.
        tapWhenReady(app.buttons["settings.voice"], "Voice")
        let keep = app.switches["settings.keepListening"]
        XCTAssertTrue(keep.waitForExistence(timeout: 5))
        let before = keep.value as? String
        keep.switches.firstMatch.tap()
        tapWhenReady(app.navigationBars["Voice"].buttons.firstMatch, "Back")
        tapWhenReady(app.buttons["settings.voice"], "Voice again")
        XCTAssertNotEqual(app.switches["settings.keepListening"].value as? String, before)
        app.switches["settings.keepListening"].switches.firstMatch.tap()
        XCTAssertEqual(app.switches["settings.keepListening"].value as? String, before, "put back for the next run")
        tapWhenReady(app.navigationBars["Voice"].buttons.firstMatch, "Back")

        // Health is under Diagnostics.
        tapWhenReady(app.buttons["settings.diagnostics"], "Diagnostics")
        XCTAssertTrue(app.buttons["settings.health"].waitForExistence(timeout: 5))
    }

    func testAnUnreachableDaemonOffersOneActionInTheFlockPane() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:7791"]
        app.launch()
        tapWhenReady(app.buttons["controls.orb"], "the orb")
        tapWhenReady(app.buttons["main.pane.flock"], "the Flock pane")
        XCTAssertTrue(app.buttons["flock.unreachable.retry"].waitForExistence(timeout: 20), "Daemon unreachable offers Try again")
        XCTAssertTrue(app.staticTexts["Daemon unreachable"].exists)
        XCTAssertFalse(app.buttons["main.pane.health"].exists, "Health is not a pane")
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isHittable, Date() < deadline { usleep(150_000) }
        element.tap()
    }
}
