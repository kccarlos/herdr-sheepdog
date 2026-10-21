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
/// recordVideo` (every frame after it is the same one). The lock screen is
/// shot separately.
///
/// The take: "Try the demo" → codex-payments' approval card → Approve →
/// "What's going on?" → the answer → "Why did the migration fail?" → the
/// answer → the Agents pane.
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

    func testRecordLaunchVideo() throws {
        let app = XCUIApplication()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")

        // Warm-up, cut from the video: open the demo once and answer the
        // speech-recognition question, so the take shows no alert.
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 20))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        for _ in 0 ..< 2 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: 4) else { break }
            for title in ["Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
        app.terminate()

        // The take. The demo's clock starts with the launch: claude-migration
        // fails at 12 s, codex-payments asks at 16 s, codex-docs finishes at 36 s.
        app.launch()
        let started = Date()
        mark("launched")
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 20))
        if app.keyboards.firstMatch.waitForExistence(timeout: 3) {
            app.typeText("\n")
            _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 3)
        }
        mark("tap_demo")
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        mark("demo_open")

        let approve = app.buttons.matching(identifier: "approval.option.1").firstMatch
        XCTAssertTrue(approve.waitForExistence(timeout: 40), "codex-payments' card")
        mark("approval")
        Thread.sleep(forTimeInterval: 2)
        mark("tap_approve")
        tapVisible(app.buttons, "approval.option.1")
        XCTAssertTrue(app.descendants(matching: .any)["approval.answered"].waitForExistence(timeout: 10))
        mark("approved")
        Thread.sleep(forTimeInterval: 2.5)

        mark("tap_status")
        tapVisible(app.buttons, "demo.question.0")
        let status = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'claude-auth is done, claude-migration failed'")).firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        mark("status")
        Thread.sleep(forTimeInterval: 3)

        mark("tap_failed")
        tapVisible(app.buttons, "demo.question.1")
        let answer = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'The column already existed'")).firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 10))
        mark("answer")
        Thread.sleep(forTimeInterval: 3)

        // Where everyone is now (codex-docs finishes at 36 s).
        let wait = 38 - Date().timeIntervalSince(started)
        if wait > 0 { Thread.sleep(forTimeInterval: wait) }
        mark("tap_agents")
        tapVisible(app.buttons, "main.pane.flock")
        mark("agents")
        Thread.sleep(forTimeInterval: 4)
        mark("end")
    }
}
