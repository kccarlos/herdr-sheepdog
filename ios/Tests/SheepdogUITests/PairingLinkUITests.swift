import XCTest

/// `sheepdog://pair` opened from outside the app, as the Camera app does
/// after scanning `sheepdog pair`'s code. Needs no daemon: the code points
/// at a closed loopback port, so the app asks, then reports that nothing
/// answered and saves nothing.
final class PairingLinkUITests: XCTestCase {
    private let link = URL(string: "sheepdog://pair?u=http%3A%2F%2F127.0.0.1%3A9&t=abc")!

    /// Opens the link the way another app does; iOS asks "Open in
    /// Sheepdog?" first.
    private func openLink(in app: XCUIApplication) {
        app.open(link)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let open = springboard.buttons["Open"]
        if open.waitForExistence(timeout: 5) { open.tap() }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testAsksBeforePairingAndCancelGoesBackToSettings() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        XCTAssertTrue(app.textFields["settings.address"].waitForExistence(timeout: 10), "first run opens Settings")

        openLink(in: app)
        let alert = app.alerts["Pair with 127.0.0.1?"]
        XCTAssertTrue(alert.waitForExistence(timeout: 10))
        alert.buttons["Cancel"].tap()
        XCTAssertTrue(app.textFields["settings.address"].waitForExistence(timeout: 10), "declining returns to the first-run Settings")
    }

    func testPairReportsAnUnreachableCode() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        XCTAssertTrue(app.textFields["settings.address"].waitForExistence(timeout: 10))

        openLink(in: app)
        let alert = app.alerts["Pair with 127.0.0.1?"]
        XCTAssertTrue(alert.waitForExistence(timeout: 10))
        alert.buttons["Pair"].tap()
        // The failure is shown where the fix is: back on the first-run
        // Daemon screen (a banner alone would sit under that sheet).
        let result = app.descendants(matching: .any)["settings.probe.result"]
        XCTAssertTrue(result.waitForExistence(timeout: 20), "the unreachable code is reported on the Daemon screen")
        XCTAssertTrue(app.staticTexts["None of the code's addresses answered."].exists)
        XCTAssertTrue(app.descendants(matching: .any)["settings.probe.help"].exists)
    }
}
