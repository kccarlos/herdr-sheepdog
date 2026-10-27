import XCTest

/// The launch review's evidence: every screen a stranger (or an App Store
/// reviewer) meets without a daemon, saved as PNGs in whatever appearance,
/// text size and device the simulator is set to. Runs only when asked:
///
///     xcodebuild … test -only-testing:SheepdogUITests/LaunchReviewScreenshotsUITests \
///       TEST_RUNNER_SHEEPDOG_LAUNCH_SHOTS=/path/to/dir TEST_RUNNER_SHEEPDOG_LAUNCH_PREFIX=light
///
/// Not in CI: it only takes pictures. docs/dev/launch-review.md cites the files.
final class LaunchReviewScreenshotsUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var directory: String { environment["SHEEPDOG_LAUNCH_SHOTS"] ?? "" }
    private var prefix: String { environment["SHEEPDOG_LAUNCH_PREFIX"] ?? "light" }
    private let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")

    override func setUpWithError() throws {
        continueAfterFailure = true
        guard !directory.isEmpty else { throw XCTSkip("set SHEEPDOG_LAUNCH_SHOTS to take the launch-review screenshots") }
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
    }

    private func shot(_ name: String) {
        let url = URL(fileURLWithPath: directory).appendingPathComponent("\(prefix)-\(name).png")
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: url)
    }

    /// System alerts (microphone, speech, notifications, "Open in"): the
    /// screenshots show the app, so they are declined.
    private func dismissAlerts(wait: TimeInterval = 3) {
        for _ in 0 ..< 3 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: wait) else { return }
            for title in ["Don’t Allow", "Don't Allow", "OK", "Open"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
    }

    /// The simulator's one-time "slide to type" keyboard tip.
    private func dismissKeyboardTip(_ app: XCUIApplication) {
        let tip = app.buttons["Continue"]
        if tip.waitForExistence(timeout: 2) { tip.tap() }
    }

    private func tapVisible(_ app: XCUIApplication, _ id: String, timeout: TimeInterval = 10) {
        let matches = app.descendants(matching: .any).matching(identifier: id)
        guard matches.firstMatch.waitForExistence(timeout: timeout) else { return }
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            return
        }
    }

    func testFirstRunAndDemo() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        dismissKeyboardTip(app)
        sleep(2)
        shot("01-first-run")
        app.swipeUp()
        sleep(1)
        shot("02-first-run-scrolled")
        app.swipeDown()

        tapVisible(app, "settings.firstRun.tryDemo")
        dismissAlerts()
        _ = app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10)
        sleep(3)
        shot("03-demo-conversation")
        tapVisible(app, "main.pane.flock")
        sleep(2)
        shot("04-demo-agents")
        if app.buttons["approval.option.1"].waitForExistence(timeout: 40) {
            sleep(1)
            shot("05-demo-approval")
            app.buttons["approval.option.1"].tap()
            sleep(2)
        }
        app.swipeUp(); tapVisible(app, "flock.agent.claude-migration.window"); tapVisible(app, "flock.agent.codex-docs.window", timeout: 2)
        sleep(3)
        shot("06-demo-agent-window")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tapVisible(app, "main.pane.conversation")
        tapVisible(app, "demo.question.0")
        sleep(5)
        shot("07-demo-answer")
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        sleep(1)
        shot("08-demo-settings")
    }

    /// The App Store set (docs/dev/app-store-review.md section 8): the offline
    /// demo, light mode, saved as `NN-name.png` with no prefix. Run once per
    /// device with the status bar overridden (`ios/scripts/appstore-shots.sh`).
    func testAppStoreSet() throws {
        func save(_ name: String) {
            let url = URL(fileURLWithPath: directory).appendingPathComponent("\(name).png")
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: url)
        }
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 20))
        let gotIt = app.buttons["controls.coachMark.dismiss"].firstMatch
        if gotIt.waitForExistence(timeout: 3) { gotIt.tap() }
        // claude-auth finishes at 8 s, codex-payments asks at 16 s.
        sleep(9)
        save("01-conversation")
        let approve = app.buttons.matching(identifier: "approval.option.1").firstMatch
        XCTAssertTrue(approve.waitForExistence(timeout: 40), "codex-payments' question")
        sleep(1)
        save("02-approval")
        tapVisible(app, "demo.sayYes")
        _ = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'You approved'")).firstMatch.waitForExistence(timeout: 10)
        sleep(2)
        // iPad (portrait): the sidebar lists the agents; an iPhone has the Agents pane.
        let sidebarShow = app.buttons["sidebar.show"].firstMatch
        let onIPad = sidebarShow.exists
        if onIPad {
            sidebarShow.tap()
            _ = app.descendants(matching: .any)["sidebar.agent.claude-auth"].waitForExistence(timeout: 5)
        } else {
            tapVisible(app, "main.pane.flock", timeout: 3)
        }
        sleep(2)
        save("03-agents")
        tapVisible(app, onIPad ? "sidebar.agent.claude-auth" : "flock.agent.claude-auth.window")
        _ = app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10)
        sleep(2)
        save("04-agent-window")
        app.terminate()

        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogLiveActivityGallery", "one"]
        app.launch()
        sleep(2)
        save("05-live-activity")
        app.terminate()

        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 20))
        if gotIt.waitForExistence(timeout: 3) { gotIt.tap() }
        let chips = app.buttons.matching(identifier: "demo.question.0")
        for i in 0 ..< chips.count where chips.element(boundBy: i).isHittable {
            chips.element(boundBy: i).swipeLeft()
            break
        }
        tapVisible(app, "demo.question.4")
        _ = app.descendants(matching: .any)["card.title"].waitForExistence(timeout: 10)
        sleep(2)
        save("06-card")
    }

    func testPairingLink() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        dismissKeyboardTip(app)
        app.open(URL(string: "sheepdog://pair#pair=1&d=deadbeef&n=Studio&k=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&e=code&u=127.0.0.1%3A9")!)
        dismissAlerts()
        if app.descendants(matching: .any)["pairing.confirm"].waitForExistence(timeout: 10) {
            shot("09-pair-confirm")
            app.buttons["pairing.primary"].tap()
            _ = app.descendants(matching: .any)["pairing.failed"].waitForExistence(timeout: 15)
            sleep(1)
            shot("10-pair-unreachable")
        }
    }

    func testDaemonUnreachable() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:9"]
        app.launch()
        tapVisible(app, "controls.orb")
        dismissAlerts()
        sleep(6)
        shot("11-unreachable-conversation")
        tapVisible(app, "main.pane.flock")
        sleep(2)
        shot("12-unreachable-agents")
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        tapVisible(app, "settings.daemon")
        tapVisible(app, "settings.test")
        sleep(4)
        shot("13-unreachable-test-connection")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        tapVisible(app, "settings.notifications")
        sleep(2)
        shot("16-notifications")
    }

    func testWrongToken() throws {
        let stub = try StubDaemon(.wrongToken)
        let host = try stub.start()
        defer { stub.stop() }
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host]
        app.launch()
        tapVisible(app, "controls.orb")
        dismissAlerts()
        sleep(5)
        shot("14-token-refused")
        tapVisible(app, "main.pane.flock")
        sleep(2)
        shot("15-token-refused-agents")
    }
}
