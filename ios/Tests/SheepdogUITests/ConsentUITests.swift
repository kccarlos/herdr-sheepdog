import XCTest

/// The one-time AI-services consent: the first real connection asks, Agree
/// goes on, Settings > Privacy withdraws and then blocks with the way
/// back, and the demo never asks. Needs no daemon (a closed loopback
/// port): the screen comes before anything is sent.
final class ConsentUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func launchConfigured(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAskConsent", "-sheepdogAddress", "127.0.0.1:9"] + extra
        app.launch()
        return app
    }

    /// `TEST_RUNNER_SHEEPDOG_CONSENT_SHOTS=<directory>` saves screenshots there.
    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_CONSENT_SHOTS"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func consentScreen(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any)["consent.screen"]
    }

    private func openPrivacy(_ app: XCUIApplication) {
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        let row = app.buttons["settings.privacy"]
        tapWhenTappable(row, "Settings > Privacy")
    }

    func testFirstRealConnectAsksAndAgreeGoesOn() throws {
        let app = launchConfigured()
        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        XCTAssertTrue(consentScreen(app).waitForExistence(timeout: 15), "the first real connect shows the consent")
        XCTAssertTrue(app.staticTexts["Your voice goes to AI services"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["consent.learnMore"].exists, "Learn more")
        // The text is exactly true: the push relay and photos are named.
        for phrase in ["push relay", "forwards notifications it cannot read", "passes it to its own AI service"] {
            let match = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", phrase))
            XCTAssertTrue(match.firstMatch.exists, "the consent says: \(phrase)")
        }
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'runs no server'")).firstMatch.exists)
        // Nothing is sent before Agree: no connection banner yet.
        XCTAssertFalse(app.descendants(matching: .any)["banner"].exists)

        tapWhenTappable(app.buttons["consent.agree"], "Agree")
        // Agree goes on with the connect that asked: a closed port, so the
        // banner with the plain fix.
        XCTAssertTrue(app.descendants(matching: .any)["banner"].waitForExistence(timeout: 20), "Agree continues the connect")
        XCTAssertFalse(consentScreen(app).exists)
    }

    func testNotNowSendsNothingAndAsksAgain() throws {
        let app = launchConfigured()
        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        tapWhenTappable(app.buttons["consent.notNow"], "Not now")
        XCTAssertFalse(consentScreen(app).waitForExistence(timeout: 2))
        XCTAssertFalse(app.descendants(matching: .any)["banner"].exists, "nothing was sent")
        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        XCTAssertTrue(consentScreen(app).waitForExistence(timeout: 10), "it asks again")
    }

    func testWithdrawBlocksWithTheExplanationAndAWayBack() throws {
        let app = launchConfigured()
        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        tapWhenTappable(app.buttons["consent.agree"], "Agree")
        XCTAssertTrue(app.descendants(matching: .any)["banner"].waitForExistence(timeout: 20))

        openPrivacy(app)
        let toggle = app.switches["privacy.aiSwitch"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        XCTAssertEqual(toggle.value as? String, "1", "on after Agree")
        shot("privacy-on")
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
        let off = NSPredicate(format: "value == '0'")
        expectation(for: off, evaluatedWith: toggle)
        waitForExpectations(timeout: 8)
        shot("privacy-off")
        tapWhenTappable(app.navigationBars.buttons.firstMatch, "Back")
        tapWhenTappable(app.buttons["settings.done"], "Done")

        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        XCTAssertTrue(consentScreen(app).waitForExistence(timeout: 10), "blocked: the screen comes up instead")
        XCTAssertTrue(app.staticTexts["Sending to AI services is off"].exists, "with the explanation")
        shot("consent-withdrawn")
        tapWhenTappable(app.buttons["consent.agree"], "Turn it back on")
        XCTAssertFalse(consentScreen(app).waitForExistence(timeout: 3), "the way back")
    }

    func testDemoNeverAsks() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogAskConsent"]
        app.launch()
        tapWhenTappable(app.buttons["settings.firstRun.tryDemo"], "Try the demo")
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        XCTAssertFalse(consentScreen(app).exists, "the demo needs no consent")
        // A suggested question is a typed line: it goes through too.
        let panes = app.buttons.matching(identifier: "main.pane.conversation")
        for i in 0 ..< panes.count where panes.element(boundBy: i).isTappable {
            panes.element(boundBy: i).tap()
            break
        }
        let matches = app.buttons.matching(identifier: "demo.question.1")
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10))
        for i in 0 ..< matches.count where matches.element(boundBy: i).isTappable {
            matches.element(boundBy: i).tap()
            break
        }
        sleep(3)
        XCTAssertFalse(consentScreen(app).exists)
    }

    /// Light, dark and the largest text size, for the review of the
    /// screen. Only with `TEST_RUNNER_SHEEPDOG_CONSENT_SHOTS` set.
    func testScreenshots() throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["SHEEPDOG_CONSENT_SHOTS"] == nil, "screenshots only on request")
        let variants: [(String, [String])] = [
            ("light", []),
            ("dark", []),
            ("xxxl", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
        ]
        defer { XCUIDevice.shared.appearance = .light }
        for (name, args) in variants {
            XCUIDevice.shared.appearance = name == "dark" ? .dark : .light
            let app = launchConfigured(args)
            tapWhenTappable(app.buttons["controls.orb"], "the orb")
            XCTAssertTrue(consentScreen(app).waitForExistence(timeout: 15))
            shot("consent-\(name)")
            app.terminate()
        }
    }
}
