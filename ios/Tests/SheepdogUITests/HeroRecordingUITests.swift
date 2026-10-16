import XCTest

/// Drives the app for the daemon repo's hero video (herdr-sheepdog
/// scripts/readme-media/hero/phone.sh records the simulator while this
/// runs, and sends the approval notification with `xcrun simctl push` when
/// this test marks `push_now`). Skipped unless
/// `TEST_RUNNER_SHEEPDOG_HERO_MARKS=<file>` is set: the test then appends
/// `<step> <unix seconds>` lines to that file, which the script both cuts
/// the video by and waits on.
///
/// Warm-up (cut from the video): connect once to the `--voice=off`
/// fixture daemon with `-sheepdogAskPush` so notifications are allowed
/// (`simctl privacy` cannot grant them, and `simctl push` needs them), and
/// open the demo once to answer the speech-recognition question.
///
/// The take: "Try the demo" → lock the phone → (claude-migration fails)
/// → codex-payments' approval arrives as a notification on the lock
/// screen → tap it → codex-payments' card → Approve → "What's going on?"
/// → "Why did the migration fail?" → the Agents pane with the work done.
final class HeroRecordingUITests: XCTestCase {
    private var marksPath: String?
    private let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")

    override func setUpWithError() throws {
        continueAfterFailure = false
        marksPath = ProcessInfo.processInfo.environment["SHEEPDOG_HERO_MARKS"]
        try XCTSkipIf(marksPath == nil, "only for the hero video recording")
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

    /// Taps the visible element with this id by coordinate: a plain tap
    /// waits for any banner on screen to go away first, which stalls the
    /// take (and the demo's clock does not wait).
    private func tapVisible(_ query: XCUIElementQuery, _ id: String) {
        let matches = query.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
            return
        }
        XCTFail("no visible \(id)")
    }

    /// Swipes away a notification banner over the app, if one shows.
    private func dismissBanner() {
        let banner = springboard.descendants(matching: .any)["NotificationShortLookView"]
        if banner.waitForExistence(timeout: 1) {
            banner.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: banner.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -3)))
        }
    }

    /// Taps Allow / OK on up to `count` system alerts.
    private func allowAlerts(_ count: Int, timeout: TimeInterval) {
        for _ in 0 ..< count {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: timeout) else { return }
            for title in ["Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
    }

    private func sleepUntil(_ seconds: TimeInterval, after start: Date) {
        let wait = seconds - Date().timeIntervalSince(start)
        if wait > 0 { Thread.sleep(forTimeInterval: wait) }
    }

    func testRecordHeroVideo() throws {
        let app = XCUIApplication()

        // Warm-up 1: notifications, asked after the first connection.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice", "-sheepdogAskPush"]
        app.launch()
        let orb = app.buttons["controls.orb"]
        XCTAssertTrue(orb.waitForExistence(timeout: 20), "the orb")
        orb.tap()
        allowAlerts(2, timeout: 10)
        app.terminate()

        // Warm-up 2: the demo's speech-recognition question.
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 20))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        allowAlerts(2, timeout: 4)
        app.terminate()

        // The take. The demo's script clock starts when the app does
        // (the demo's DemoDaemon is made at launch), so every wait below
        // counts from the launch: claude-migration fails at 12 s,
        // codex-payments asks at 16 s, codex-docs finishes at 36 s.
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
        Thread.sleep(forTimeInterval: 0.5)

        // The phone goes in the pocket.
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        mark("locked")

        // The notification lands just before codex-payments' card (the script
        // pushes on this mark) and is tapped once it has been on screen
        // long enough to read.
        sleepUntil(9, after: started)
        mark("push_now")
        let notification = springboard.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS 'npm install p-retry'")).firstMatch
        XCTAssertTrue(notification.waitForExistence(timeout: 15), "the notification")
        mark("notification")
        sleepUntil(15, after: started)
        mark("tap_notification")
        notification.tap()

        // No passcode, but the lock screen still wants a swipe up from
        // the bottom edge to open the app.
        Thread.sleep(forTimeInterval: 0.6)
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.995))
        bottom.press(forDuration: 0.05, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35)))
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10), "the app")
        // The simulator's recording can freeze across an unlock: the script
        // starts a new one here and says so with the ready file.
        mark("unlocked")
        if let ready = ProcessInfo.processInfo.environment["SHEEPDOG_HERO_READY"] {
            let deadline = Date().addingTimeInterval(15)
            while !FileManager.default.fileExists(atPath: ready), Date() < deadline {
                Thread.sleep(forTimeInterval: 0.1)
            }
        }
        dismissBanner()
        let approve = app.buttons.matching(identifier: "approval.option.1").firstMatch
        XCTAssertTrue(approve.waitForExistence(timeout: 15), "codex-payments' card")
        mark("approval")
        Thread.sleep(forTimeInterval: 1.5)
        mark("tap_approve")
        tapVisible(app.buttons, "approval.option.1")
        XCTAssertTrue(app.descendants(matching: .any)["approval.answered"].waitForExistence(timeout: 10))
        mark("approved")
        Thread.sleep(forTimeInterval: 1.2)

        // The app came back on the conversation, where the card is.
        mark("conversation")
        mark("tap_status")
        tapVisible(app.buttons, "demo.question.0")
        let status = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'claude-auth is done, claude-migration failed'")).firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        mark("status")

        sleep(2)
        mark("tap_failed")
        tapVisible(app.buttons, "demo.question.1")
        let answer = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'The column already existed'")).firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 10))
        mark("answer")
        sleep(3)

        // Where everyone is now: codex-payments finished after the
        // approval, codex-docs at 36 s.
        sleepUntil(35, after: started)
        tapVisible(app.buttons, "main.pane.flock")
        mark("agents_end")
        sleepUntil(40, after: started)
        mark("end")
    }
}
