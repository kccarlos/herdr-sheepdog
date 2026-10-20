import XCTest

/// Settings' update line, against `StubDaemon` (no daemon checkout, so it
/// runs on Xcode Cloud too): shown when `/api/state` reports a newer
/// release, absent when the daemon is too old to report one.
final class UpdateNoticeUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 15) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared")
        element.tap()
    }

    func testSettingsShowsAnAvailableUpdate() throws {
        let stub = try StubDaemon(.daemon(update: #"{"current":"v0.1.4","latest":"v0.1.5","available":true,"how":"brew upgrade sheepdog","auto_restart":true}"#))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host]
        app.launch()
        tapWhenReady(app.buttons["toolbar.settings"], "Settings")

        let line = app.descendants(matching: .any)["settings.update"]
        XCTAssertTrue(line.waitForExistence(timeout: 15), "the update line")
        XCTAssertTrue(line.label.contains("sheepdog v0.1.5 is available"), line.label)
        XCTAssertTrue(line.label.contains("brew upgrade sheepdog"), line.label)
        XCTAssertTrue(line.label.contains("restarts itself when idle"), line.label)

        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "settings-update"
        shot.lifetime = .keepAlways
        add(shot)
        if let dir = ProcessInfo.processInfo.environment["SHEEPDOG_SHOTS_DIR"], !dir.isEmpty {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("settings-update.png"))
        }
    }

    func testNoLineWhenTheDaemonReportsNoUpdate() throws {
        let stub = try StubDaemon(.daemon(update: nil))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host]
        app.launch()
        tapWhenReady(app.buttons["toolbar.settings"], "Settings")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10))
        // Give the state refresh time to land before asserting absence.
        sleep(2)
        XCTAssertFalse(app.descendants(matching: .any)["settings.update"].exists, "no line from a daemon that reports no update")
    }
}
