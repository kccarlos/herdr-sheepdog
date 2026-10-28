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
            for i in 0 ..< matches.count where matches.element(boundBy: i).isTappable {
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
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent"]
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

    /// The approval can be answered aloud: the demo's "Yes, go ahead." chip
    /// stands in for the recognised speech, and the conversation records
    /// the approval as given by voice.
    func testApproveByVoiceInTheDemo() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40), "codex-payments' question")
        tapVisible(app.buttons, "demo.sayYes")
        let approved = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'You approved' AND label CONTAINS 'by voice'")).firstMatch
        XCTAssertTrue(approved.waitForExistence(timeout: 10), "the approval says it was given by voice")
    }

    /// Settings ▸ Health ▸ Show technical events brings back the plumbing
    /// the conversation hides: the demo's seeded reconnect that lost
    /// nothing is absent, shown with the switch on, and gone again with it
    /// off (docs/dev/design/conversation-timeline.md, section 6).
    func testShowTechnicalEventsRevealsAHiddenReconnect() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        dismissPermissionAlerts()
        tapVisible(app.buttons, "main.pane.conversation")

        let reconnect = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'divider.notice' AND label CONTAINS 'Voice reconnected'")).firstMatch
        let restart = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'divider.notice' AND label CONTAINS 'Sheepdog started'")).firstMatch
        XCTAssertTrue(restart.waitForExistence(timeout: 15), "the conversation loaded, with the daemon's start")
        XCTAssertFalse(reconnect.exists, "a reconnect that lost nothing is plumbing")

        func setSwitch() {
            tapMenuItem(app, menu: "voice.menu", item: "chip.connection")
            tapWhenTappable(app.buttons["chip.health"], "Health in the chip's popover")
            let toggle = app.switches["health.technicalEvents"]
            let list = app.collectionViews["health.list"].exists ? app.collectionViews["health.list"] : app.collectionViews.firstMatch
            for _ in 0 ..< 6 where !(toggle.exists && toggle.isTappable) {
                list.swipeUp()
            }
            XCTAssertTrue(toggle.waitForExistence(timeout: 10), "Show technical events")
            // The switch is at the row's right end; the middle is its label.
            toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
            // Back to Connection, then Done.
            app.navigationBars.buttons.element(boundBy: 0).tap()
            tapWhenTappable(app.buttons["Done"], "Done")
        }

        setSwitch()
        XCTAssertTrue(app.descendants(matching: .any)["conversation.technicalTag"].waitForExistence(timeout: 10), "the tag says the view is technical")
        XCTAssertTrue(reconnect.waitForExistence(timeout: 10), "Show technical events adds the reconnect")
        shot("technical-on")

        setSwitch()
        XCTAssertTrue(waitForGone(reconnect, timeout: 10), "off again: the reconnect is hidden")
    }

    private func waitForGone(_ element: XCUIElement, timeout: TimeInterval) -> Bool {
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: element)
        return XCTWaiter().wait(for: [gone], timeout: timeout) == .completed
    }

    /// Opening the demo asks for nothing: the microphone and speech
    /// recognition prompts wait for the first tap on the microphone.
    func testOpeningTheDemoShowsNoMicrophonePrompt() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogOpenDemo"]
        // No earlier answer: a prompt would show if the demo asked.
        app.resetAuthorizationStatus(for: .microphone)
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40), "the demo is running")
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        XCTAssertFalse(springboard.alerts.firstMatch.waitForExistence(timeout: 4), "no permission prompt in the demo's first seconds")
    }
}
