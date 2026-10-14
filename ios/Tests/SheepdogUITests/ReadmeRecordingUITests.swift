import XCTest

/// Drives the offline demo at a watchable pace for the daemon repo's
/// README GIF (herdr-sheepdog scripts/readme-media/phone.sh records the
/// simulator while this runs). Skipped unless
/// `TEST_RUNNER_SHEEPDOG_README_MARKS=<file>` is set: the test then appends
/// `<step> <unix seconds>` lines to that file so the recording script can
/// cut the waits out of the video.
///
/// The path: "Try the demo" → the four agents → billing asks → Approve →
/// migrate fails → "What failed?" → the answer in the conversation.
final class ReadmeRecordingUITests: XCTestCase {
    private var marksPath: String?

    override func setUpWithError() throws {
        continueAfterFailure = false
        marksPath = ProcessInfo.processInfo.environment["SHEEPDOG_README_MARKS"]
        try XCTSkipIf(marksPath == nil, "only for the README recording")
        addUIInterruptionMonitor(withDescription: "permissions") { alert in
            for title in ["Allow", "OK", "Don’t Allow", "Don't Allow"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                return true
            }
            return false
        }
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
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            return
        }
        XCTFail("no visible \(id)")
    }

    func testRecordDemoForReadme() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()

        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 20))
        // Warm-up, cut from the video: open the demo once and answer the
        // speech-recognition question (simctl privacy cannot grant it),
        // so the recorded take shows no alert.
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0 ..< 2 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: 4) else { break }
            for title in ["Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
        // Relaunch rather than Leave: Leave lands on the address field
        // with the keyboard up.
        app.terminate()
        app.launch()
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 20))
        // The address field takes the focus on a fresh start: put the
        // keyboard away so the setup screen shows whole.
        if app.keyboards.firstMatch.waitForExistence(timeout: 3) {
            app.typeText("\n")
            _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 3)
        }
        mark("firstrun")
        sleep(2)
        mark("tap_demo")
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        let started = Date()
        mark("demo_open")

        tapVisible(app.buttons, "main.pane.flock")
        XCTAssertTrue(app.descendants(matching: .any)["flock.agent.docs"].waitForExistence(timeout: 10))
        mark("agents")

        let approve = app.buttons["approval.option.1"]
        XCTAssertTrue(approve.waitForExistence(timeout: 40))
        mark("approval")
        Thread.sleep(forTimeInterval: 1.2)
        mark("tap_approve")
        approve.tap()
        XCTAssertTrue(app.descendants(matching: .any)["approval.answered"].waitForExistence(timeout: 10))
        sleep(1)
        mark("approved")

        // migrate fails 26 s into the script; "What failed?" before that
        // answers "Nothing has failed so far."
        let wait = 27.5 - Date().timeIntervalSince(started)
        if wait > 0 { Thread.sleep(forTimeInterval: wait) }
        mark("migrate_failed")
        sleep(1)

        tapVisible(app.buttons, "main.pane.conversation")
        mark("conversation")
        Thread.sleep(forTimeInterval: 1.5)
        mark("tap_question")
        app.buttons["demo.question.1"].tap()
        let answer = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'Migration 0042'")).firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 10))
        mark("answer")
        sleep(4)
        mark("end")
    }
}
