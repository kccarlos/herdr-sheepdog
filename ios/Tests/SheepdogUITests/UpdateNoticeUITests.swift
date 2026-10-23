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

    /// `-sheepdogDemoUpdate`: the offline demo shows the notice, for the
    /// launch video's shot 10b without an older daemon.
    func testDemoUpdateFlagShowsTheNotice() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogDemoPaused", "-sheepdogDemoUpdate"]
        app.launch()
        let banner = app.descendants(matching: .any)["updateNotice"]
        XCTAssertTrue(banner.waitForExistence(timeout: 15), "the update notice in the demo")
        XCTAssertTrue(banner.label.contains("v0.1.17") || app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'v0.1.17'")).firstMatch.exists)
        if let dir = ProcessInfo.processInfo.environment["SHEEPDOG_DEMO_SHOTS"] {
            sleep(1)
            let suffix = ProcessInfo.processInfo.environment["SHEEPDOG_DEMO_SHOTS_SUFFIX"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("demo-update-\(suffix).png"))
        }
    }

    func testDemoHasNoUpdateWithoutTheFlag() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogDemoPaused"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        sleep(3)
        XCTAssertFalse(app.descendants(matching: .any)["updateNotice"].exists)
    }

    func testSettingsShowsAnAvailableUpdate() throws {
        let stub = try StubDaemon(.daemon(update: #"{"current":"v0.1.4","latest":"v0.1.5","available":true,"how":"brew upgrade sheepdog","auto_restart":true}"#))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host]
        app.launch()
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")

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
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10))
        // Give the state refresh time to land before asserting absence.
        sleep(2)
        XCTAssertFalse(app.descendants(matching: .any)["settings.update"].exists, "no line from a daemon that reports no update")
    }
}
