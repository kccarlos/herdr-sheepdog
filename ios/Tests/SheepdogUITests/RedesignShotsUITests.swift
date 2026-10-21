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
        app.buttons["main.pane.flock"].firstMatch.tap()
        sleep(2)
        shot("agents")
        app.buttons["voice.menu"].firstMatch.tap()
        sleep(1)
        shot("voice-menu")
        dismissMenu(app)
        app.buttons["toolbar.more"].firstMatch.tap()
        sleep(1)
        shot("more-menu")
        dismissMenu(app)
        app.buttons["toolbar.new"].firstMatch.tap()
        sleep(1)
        shot("plus-menu")
        dismissMenu(app)
        app.buttons["main.pane.conversation"].firstMatch.tap()
        app.buttons["controls.type"].firstMatch.tap()
        let field = app.textFields["type.field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("and remind me when search is done")
        sleep(1)
        shot("composer")
    }

    // MARK: - Helpers

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
        attachment.name = "ui-\(name)-\(suffix)"
        attachment.lifetime = .keepAlways
        add(attachment)
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent("ui-\(name)-\(suffix).png"))
    }
}
