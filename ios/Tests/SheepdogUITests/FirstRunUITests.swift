import XCTest

/// What a stranger meets with no daemon, or a daemon the phone cannot use:
/// the first screen says what the app needs and offers the demo, and each
/// failure says the plain fix with a link to it. Needs no daemon checkout
/// (a closed port, or `StubDaemon` inside the test runner), so it runs on
/// Xcode Cloud too.
final class FirstRunUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 15) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared")
        element.tap()
    }

    func testFirstRunSaysWhatTheAppNeedsAndOffersTheDemo() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations"]
        app.launch()

        let intro = app.descendants(matching: .any)["settings.firstRun.intro"]
        XCTAssertTrue(intro.waitForExistence(timeout: 15), "the first run explains itself")
        let requirement = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'sheepdog setup'")).firstMatch
        XCTAssertTrue(requirement.exists, "the first run names `sheepdog setup`")
        XCTAssertTrue(app.descendants(matching: .any)["settings.firstRun.guide"].exists, "a link to the setup guide")
        let demo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(demo.isTappable, "Try the demo is on the first screen without scrolling")
        XCTAssertEqual(app.keyboards.count, 0, "no keyboard covers the first screen")
    }

    func testUnreachableDaemonSaysSameWiFiOrTailscale() throws {
        let app = XCUIApplication()
        // A closed loopback port: nothing answers, as with a daemon that is
        // down or a phone on another network.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", "127.0.0.1:9"]
        app.launch()
        tapWhenReady(app.buttons["controls.orb"], "the orb")

        let banner = app.descendants(matching: .any)["banner"]
        XCTAssertTrue(banner.waitForExistence(timeout: 20))
        let fix = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'same Wi-Fi'")).firstMatch
        XCTAssertTrue(fix.waitForExistence(timeout: 5), "the banner names the fix")
        XCTAssertTrue(app.descendants(matching: .any)["banner.help"].exists, "and links to it")

        tapWhenReady(app.buttons["main.pane.flock"], "the Agents pane")
        XCTAssertTrue(app.buttons["flock.unreachable.retry"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["flock.unreachable.help"].exists)
    }

    func testWrongTokenOpensTheDaemonScreenWithTheFix() throws {
        let stub = try StubDaemon(.wrongToken)
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", host]
        app.launch()
        tapWhenReady(app.buttons["controls.orb"], "the orb")

        let sheet = app.descendants(matching: .any)["pairing.scanAgain"]
        XCTAssertTrue(sheet.waitForExistence(timeout: 20), "a refused phone opens \"Please scan again\"")
        XCTAssertTrue(app.staticTexts["Please scan again"].exists)
        XCTAssertTrue(app.buttons["pairing.primary"].exists, "the scanner is one tap away")
    }

    func testNotSheepdogSaysCheckTheAddress() throws {
        let stub = try StubDaemon(.notSheepdog)
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", host]
        app.launch()
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        tapWhenReady(app.buttons["settings.daemon"], "Daemon")
        tapWhenReady(app.buttons["settings.test"], "Test connection")
        let result = app.descendants(matching: .any)["settings.probe.result"]
        XCTAssertTrue(result.waitForExistence(timeout: 20))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "isn't a sheepdog daemon")).firstMatch.exists)
    }

    /// The demo's Settings lists only what the demo has: no daemon models,
    /// languages or notifications to show as "—".
    func testDemoSettingsHaveNoEmptyDaemonPages() throws {
        addUIInterruptionMonitor(withDescription: "permissions") { alert in
            for title in ["Don’t Allow", "Don't Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                return true
            }
            return false
        }
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent"]
        app.launch()
        tapWhenReady(app.buttons["settings.firstRun.tryDemo"], "Try the demo")
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0 ..< 3 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: 4) else { break }
            for title in ["Don’t Allow", "Don't Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings", until: app.buttons["settings.leaveDemo"])
        XCTAssertTrue(app.buttons["settings.leaveDemo"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["settings.models"].exists)
        XCTAssertFalse(app.buttons["settings.notifications"].exists)
    }
}
