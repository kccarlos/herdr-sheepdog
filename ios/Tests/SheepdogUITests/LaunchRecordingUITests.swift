import XCTest

/// Drives the offline demo for the daemon repo's launch video
/// (herdr-sheepdog scripts/readme-media/launch/phone.sh records the
/// simulator while this runs). Skipped unless
/// `TEST_RUNNER_SHEEPDOG_LAUNCH_MARKS=<file>` is set: the test then appends
/// `<step> <unix seconds>` lines to that file, which the script cuts the
/// video by.
///
/// Unlike HeroRecordingUITests it never locks the phone: on the iOS 27
/// simulator a lock and unlock leaves the display frozen for `simctl io
/// recordVideo` (every frame after it is the same one).
///
/// The take (docs/dev/launch-video.md), silent (the video's voices are
/// added in the edit), in two launches:
///   1. the demo paused (`-sheepdogDemoPaused`, so every agent stays at
///      work): the Agents page → claude-auth's window → Changes → a diff;
///   2. the demo running: codex-payments' question → Approve → "What's
///      going on?" → "Show me the agents" and its card.
final class LaunchRecordingUITests: XCTestCase {
    private var marksPath: String?

    override func setUpWithError() throws {
        continueAfterFailure = false
        marksPath = ProcessInfo.processInfo.environment["SHEEPDOG_LAUNCH_MARKS"]
        try XCTSkipIf(marksPath == nil, "only for the launch video recording")
    }

    private func mark(_ step: String) {
        guard let marksPath else { return }
        let line = String(format: "%@ %.3f\n", step, Date().timeIntervalSince1970)
        if let handle = FileHandle(forWritingAtPath: marksPath) {
            handle.seekToEndOfFile()
            handle.write(Data(line.utf8))
            try? handle.close()
        } else {
            try? Data(line.utf8).write(to: URL(fileURLWithPath: marksPath))
        }
    }

    private func tapVisible(_ query: XCUIElementQuery, _ id: String) {
        let matches = query.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        // It can exist a moment before it is hittable (a sheet or the
        // demo's cover still animating, slower on a CI Mac): keep looking.
        let deadline = Date().addingTimeInterval(10)
        repeat {
            for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
                matches.element(boundBy: i).tap()
                return
            }
            usleep(200_000)
        } while Date() < deadline
        XCTFail("no visible \(id)")
    }

    /// Allows notifications for the lock-screen shot (`simctl privacy`
    /// cannot): connects to the fixture daemon once so the app asks, and
    /// taps Allow wherever the system shows the question (the iOS 27
    /// simulator does not always put it in SpringBoard's alert list).
    func testAllowNotifications() throws {
        let app = XCUIApplication()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice", "-sheepdogAskPush"]
        app.launch()
        let orb = app.buttons["controls.orb"]
        XCTAssertTrue(orb.waitForExistence(timeout: 20), "the orb")
        orb.tap()
        let deadline = Date().addingTimeInterval(25)
        var answered = 0
        while Date() < deadline, answered < 2 {
            let candidates = [springboard.buttons["Allow"], app.buttons["Allow"], springboard.alerts.buttons["Allow"], springboard.buttons["OK"]]
            if let button = candidates.first(where: { $0.exists }) {
                button.tap()
                answered += 1
                mark("allowed")
                Thread.sleep(forTimeInterval: 1)
            } else {
                Thread.sleep(forTimeInterval: 0.5)
            }
        }
        if answered == 0 {
            try? springboard.debugDescription.write(toFile: (marksPath ?? "/tmp/x") + ".springboard.txt", atomically: true, encoding: .utf8)
            try? app.debugDescription.write(toFile: (marksPath ?? "/tmp/x") + ".app.txt", atomically: true, encoding: .utf8)
        }
        XCTAssertGreaterThan(answered, 0, "no permission question appeared")
    }

    /// The first-run coach mark would cover the bottom of every shot.
    private func dismissCoachMark(_ app: XCUIApplication) {
        let gotIt = app.buttons["controls.coachMark.dismiss"].firstMatch
        if gotIt.waitForExistence(timeout: 3) { gotIt.tap() }
    }

    func testRecordLaunchVideo() throws {
        let app = XCUIApplication()

        // 1. Four agents at work, one window, its changes.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogDemoPaused"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        dismissCoachMark(app)
        showAgentsPane(app)
        mark("agents")
        Thread.sleep(forTimeInterval: 4)
        mark("tap_window")
        tapVisible(app.buttons, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10))
        mark("terminal")
        Thread.sleep(forTimeInterval: 5)
        mark("tap_menu")
        tapVisible(app.buttons, "agentScreen.menu")
        Thread.sleep(forTimeInterval: 1.2)
        mark("tap_changes")
        tapVisible(app.buttons, "agentScreen.menu.changes")
        let file = app.buttons["changes.file.internal/auth/token.go"]
        // On a busy Mac the first tap can land while the menu still opens.
        if !file.waitForExistence(timeout: 5), app.buttons["agentScreen.menu.changes"].isHittable {
            app.buttons["agentScreen.menu.changes"].tap()
        }
        XCTAssertTrue(file.waitForExistence(timeout: 10))
        mark("changes")
        Thread.sleep(forTimeInterval: 3)
        mark("tap_file")
        file.tap()
        XCTAssertTrue(app.descendants(matching: .any)["diff.lines"].waitForExistence(timeout: 10))
        mark("diff")
        Thread.sleep(forTimeInterval: 5)
        mark("part1_end")
        app.terminate()

        // 2. The script running. Its clock starts with the launch:
        // claude-auth finishes at 8 s, claude-migration fails at 12 s,
        // codex-payments asks at 16 s.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        mark("launched")
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        dismissCoachMark(app)
        let approve = app.buttons.matching(identifier: "approval.option.1").firstMatch
        XCTAssertTrue(approve.waitForExistence(timeout: 40), "codex-payments' question")
        mark("ask")
        Thread.sleep(forTimeInterval: 4)
        mark("tap_approve")
        tapVisible(app.buttons, "approval.option.1")
        let approved = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'You approved'")).firstMatch
        XCTAssertTrue(approved.waitForExistence(timeout: 10))
        mark("approved")
        Thread.sleep(forTimeInterval: 3)

        mark("tap_status")
        tapVisible(app.buttons, "demo.question.0")
        let status = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'codex-docs is still writing'")).firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        mark("status")
        Thread.sleep(forTimeInterval: 5)

        // "Show me the agents" is the last suggestion: scroll the row to it.
        let chips = app.buttons.matching(identifier: "demo.question.0")
        for i in 0 ..< chips.count where chips.element(boundBy: i).isHittable {
            chips.element(boundBy: i).swipeLeft()
            break
        }
        Thread.sleep(forTimeInterval: 1)
        mark("tap_card")
        tapVisible(app.buttons, "demo.question.4")
        XCTAssertTrue(app.descendants(matching: .any)["card.title"].waitForExistence(timeout: 10))
        mark("card")
        Thread.sleep(forTimeInterval: 6)
        mark("end")
    }
}
