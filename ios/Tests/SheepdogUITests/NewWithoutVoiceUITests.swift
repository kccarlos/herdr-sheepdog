import XCTest

/// New agent works before any voice session: the fixture daemon runs with
/// `--voice=off`, so the app learns its features from `/api/hello` only.
/// Not in CI (needs the fixture daemon).
final class NewWithoutVoiceUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        guard (try? Data(contentsOf: URL(string: "http://\(Fixture.host)/api/hello")!)) != nil else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    func testNewAgentIsEnabledWithoutTalking() {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()

        // The New menu's New agent turns on once hello lands; the orb is never tapped.
        let item = app.buttons.matching(identifier: "new.agent")
        let deadline = Date().addingTimeInterval(30)
        var enabled = false
        repeat {
            tapWhenTappable(app.buttons["toolbar.new"].firstMatch, "the New menu")
            if item.firstMatch.waitForExistence(timeout: 5), item.firstMatch.isEnabled { enabled = true; break }
            app.tap() // close the menu and look again
            sleep(1)
        } while Date() < deadline
        XCTAssertTrue(enabled, "New agent is available without a voice session")
        XCTAssertTrue(app.buttons["controls.orb"].exists, "still no voice session")
    }
}
