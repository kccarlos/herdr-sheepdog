import XCTest

/// The launch review's evidence: every screen a stranger (or an App Store
/// reviewer) meets without a daemon, saved as PNGs in whatever appearance,
/// text size and device the simulator is set to. Runs only when asked:
///
///     xcodebuild … test -only-testing:SheepdogUITests/LaunchReviewScreenshotsUITests \
///       TEST_RUNNER_SHEEPDOG_LAUNCH_SHOTS=/path/to/dir TEST_RUNNER_SHEEPDOG_LAUNCH_PREFIX=light
///
/// Not in CI: it only takes pictures. docs/launch-review.md cites the files.
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
        tapVisible(app, "toolbar.settings")
        sleep(1)
        shot("08-demo-settings")
    }

    func testPairingLink() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        dismissKeyboardTip(app)
        app.open(URL(string: "sheepdog://pair?u=http%3A%2F%2F127.0.0.1%3A9&t=abc")!)
        dismissAlerts()
        let alert = app.alerts["Pair with 127.0.0.1?"]
        if alert.waitForExistence(timeout: 10) {
            shot("09-pair-confirm")
            alert.buttons["Pair"].tap()
            _ = app.descendants(matching: .any)["banner"].waitForExistence(timeout: 15)
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
        tapVisible(app, "toolbar.settings")
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
