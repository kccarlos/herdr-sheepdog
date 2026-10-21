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

    // MARK: - Helpers

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
