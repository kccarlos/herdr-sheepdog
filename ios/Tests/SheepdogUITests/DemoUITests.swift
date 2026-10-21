import XCTest

/// The offline demo, as an App Store reviewer would meet it: no daemon, no
/// network. From the first-run screen, "Try the demo" shows the scripted
/// agents; codex-payments' approval can be answered; a suggested question gets an
/// answer; Leave returns to the setup.
final class DemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        addUIInterruptionMonitor(withDescription: "permissions") { alert in
            // The demo may ask for the microphone and speech recognition;
            // the test answers questions by tap.
            for title in ["Don’t Allow", "Don't Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                return true
            }
            return false
        }
    }

    /// Where the device can recognise speech on-device (Xcode Cloud's
    /// simulators can), the demo asks for speech recognition and the
    /// microphone. Answer both before tapping anything, so no alert
    /// interrupts a tap. The test asks its questions by tap.
    private func dismissPermissionAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for _ in 0 ..< 3 {
            let alert = springboard.alerts.firstMatch
            guard alert.waitForExistence(timeout: 4) else { return }
            for title in ["Don’t Allow", "Don't Allow", "OK"] where alert.buttons[title].exists {
                alert.buttons[title].tap()
                break
            }
        }
    }

    /// The real main view stays under the demo's full-screen cover and
    /// keeps its identifiers: tap the one on screen.
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

    /// `TEST_RUNNER_SHEEPDOG_DEMO_SHOTS=<directory>` saves screenshots there.
    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_DEMO_SHOTS"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    func testTryTheDemoFromTheFirstRun() throws {
        let app = XCUIApplication()
        // Silent: no speech synthesis or recognition. On Xcode Cloud the
        // simulator's audio server hangs and the app aborts starting it.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent"]
        app.launch()

        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        dismissPermissionAlerts()

        // The scripted agents, in the real Agents pane.
        showAgentsPane(app)
        for alias in ["claude-auth", "codex-payments", "claude-migration", "codex-docs"] {
            XCTAssertTrue(app.descendants(matching: .any)["flock.agent.\(alias)"].waitForExistence(timeout: 10), alias)
        }

        // codex-payments asks after ~16 s; approve it on the card.
        let approve = app.buttons["approval.option.1"]
        XCTAssertTrue(approve.waitForExistence(timeout: 40))
        shot("demo-approval")
        approve.tap()
        // The card shows "answered" only briefly before the demo agent moves
        // on and its question closes: either one proves the answer landed.
        let answered = app.descendants(matching: .any)["approval.answered"]
        let deadline = Date().addingTimeInterval(15)
        while !answered.exists, approve.exists, Date() < deadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
        }
        XCTAssertTrue(answered.exists || !approve.exists, "the approval was answered")

        // A suggested question gets the scripted answer in the conversation.
        tapVisible(app.buttons, "main.pane.conversation")
        tapVisible(app.buttons, "demo.question.0")
        let answer = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'claude-auth is done'")).firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 15))
        shot("demo-conversation")

        app.buttons["demo.leave"].tap()
        XCTAssertTrue(app.buttons["settings.firstRun.tryDemo"].waitForExistence(timeout: 10), "back to the first-run setup")
    }
}
