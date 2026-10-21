import XCTest

/// The live agent window on the offline demo (no daemon, no network): the
/// demo answers `/term` from its scripted sessions, so the window runs its
/// real path (channel, frames, resize, key bar). `-sheepdogDemoTerm`
/// makes every open answer `refused:busy` or `gone` for the observe banner
/// and the closed state.
///
/// `TEST_RUNNER_SHEEPDOG_DEMO_SHOTS=<directory>` saves screenshots there.
final class AgentWindowDemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_DEMO_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_DEMO_SHOTS_SUFFIX"].map { "-" + $0 } ?? ""
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + suffix + ".png"))
    }

    /// The real main view stays under the demo's cover with the same
    /// identifiers: tap the one on screen.
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

    /// Launches the demo and opens `alias`'s window.
    private func openWindow(_ alias: String, term: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent"]
        if let term { app.launchArguments += ["-sheepdogDemoTerm", term] }
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        tapVisible(app.buttons, "flock.agent.\(alias).window")
        return app
    }

    func testTheLiveWindowShowsTheTerminalAndKeyBar() throws {
        let app = openWindow("claude-auth")
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        XCTAssertTrue(terminal.waitForExistence(timeout: 10), "the terminal")
        // The scripted session reaches the surface (VoiceOver's copy of it).
        expectation(for: NSPredicate(format: "value CONTAINS 'scripted session'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 10)
        for key in ["esc", "tab", "up", "down", "ctrl+c", "enter"] {
            XCTAssertTrue(app.buttons["agentScreen.key.\(key)"].exists, key)
        }
        XCTAssertEqual(app.buttons["agentScreen.key.esc"].label, "Escape")
        XCTAssertEqual(app.buttons["agentScreen.key.ctrl+c"].label, "Control C")
        XCTAssertTrue(app.buttons["agentScreen.key.enter"].isEnabled, "live: the keys work")
        XCTAssertFalse(app.descendants(matching: .any)["agentScreen.notice"].exists, "no notice while live")
        sleep(1)
        shot("agent-window")

        // A key goes to the pane as raw input: the demo echoes typed text,
        // and Enter clears its prompt line.
        app.buttons["agentScreen.key.enter"].tap()
        XCTAssertTrue(terminal.exists)
    }

    /// The composer, the more-keys sheet with snippets, and typing mode.
    func testComposerMoreKeysAndTyping() throws {
        let app = openWindow("claude-auth")
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        expectation(for: NSPredicate(format: "value CONTAINS 'scripted session'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 10)
        XCTAssertTrue(app.buttons["agentScreen.key.more"].isEnabled)
        sleep(1)
        shot("composer")

        let field = app.textFields["agentScreen.field"]
        field.tap()
        field.typeText("Run the integration tests and summarise what fails")
        XCTAssertTrue(app.buttons["agentScreen.send"].isEnabled)
        shot("composer-active")
        app.buttons["agentScreen.key.more"].tap()

        let done = app.buttons["moreKeys.done"]
        XCTAssertTrue(done.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["moreKeys.ctrl"].exists, "live: the modifiers")
        sleep(1)
        shot("more-keys")
        for _ in 0 ..< 4 where !app.staticTexts["moreKeys.snippets.empty"].isHittable { app.swipeUp() }
        XCTAssertTrue(app.staticTexts["moreKeys.snippets.empty"].waitForExistence(timeout: 5))
        shot("more-keys-snippets")
        done.tap()

        app.buttons["agentScreen.key.typing"].tap()
        XCTAssertTrue(app.staticTexts["agentScreen.typingHint"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["agentScreen.key.ctrl"].exists, "⌃C becomes the ⌃ latch")
        sleep(1)
        shot("typing")
        app.buttons["agentScreen.key.typing"].tap()
        XCTAssertTrue(field.waitForExistence(timeout: 5), "the composer comes back")

        // The ⋯ menu's actions, each asking first.
        app.buttons["agentScreen.menu"].tap()
        XCTAssertTrue(app.buttons["agentScreen.menu.rename"].waitForExistence(timeout: 5))
        sleep(1)
        shot("actions-menu")
        app.buttons["agentScreen.menu.close"].tap()
        XCTAssertTrue(app.buttons["agentScreen.close.confirm"].waitForExistence(timeout: 5))
        sleep(1)
        shot("close-confirm")
        app.buttons["Cancel"].firstMatch.tap()
        app.buttons["agentScreen.menu"].tap()
        app.buttons["agentScreen.menu.rename"].tap()
        XCTAssertTrue(app.alerts.textFields.firstMatch.waitForExistence(timeout: 5))
        sleep(1)
        shot("rename")
        app.buttons["Cancel"].firstMatch.tap()
    }

    /// Paste is offered only where input goes somewhere: the live window,
    /// not the snapshot.
    func testPasteOnlyOnALiveWindow() throws {
        UIPasteboard.general.string = "x"
        let app = openWindow("claude-migration", term: "herdr")
        let notice = app.descendants(matching: .any)["agentScreen.notice.text"]
        XCTAssertTrue(notice.waitForExistence(timeout: 10))
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        terminal.press(forDuration: 1.2)
        sleep(1)
        XCTAssertFalse(app.menuItems["Paste"].exists, "no Paste on the snapshot")
        shot("paste-snapshot")
    }

    /// New agent from the Agents tab.
    func testNewAgentSheet() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        let new = app.buttons.matching(identifier: "flock.newAgent")
        for _ in 0 ..< 4 where !new.firstMatch.isHittable { app.swipeUp() }
        // The sheet comes from the tap; a tap lost while the list still
        // scrolls is retried once the list is still.
        let start = app.buttons["newAgent.start"]
        for _ in 0 ..< 3 where !start.exists {
            tapVisible(app.buttons, "flock.newAgent")
            if start.waitForExistence(timeout: 10) { break }
        }
        XCTAssertTrue(start.exists, "New agent opens its sheet")
        sleep(1)
        shot("new-agent")
        // A tap on the segment while the sheet still settles can be lost on
        // a slow Mac; choosing the same segment again is harmless.
        let branch = app.textFields["newAgent.branch"]
        for _ in 0 ..< 3 where !branch.exists {
            app.buttons["New worktree"].tap()
            if branch.waitForExistence(timeout: 5) { break }
        }
        XCTAssertTrue(branch.exists, "New worktree shows the branch field")
        branch.tap()
        branch.typeText("fix-login")
        XCTAssertTrue(app.buttons["newAgent.start"].isEnabled)
        shot("new-agent-worktree")
        app.buttons["newAgent.start"].tap()
        // "Started" shows for 1.4 s, then the sheet closes by itself, which
        // it does only after a successful start. The tap's own wait for the
        // app to idle can outlast the label on a slow Mac, so either counts.
        let started = app.descendants(matching: .any)["newAgent.started"]
        let startButton = app.buttons["newAgent.start"]
        let startedOrClosed = NSPredicate { _, _ in started.exists || !startButton.exists }
        wait(for: [XCTNSPredicateExpectation(predicate: startedOrClosed, object: nil)], timeout: 10)
        XCTAssertEqual(app.state, .runningForeground, "the app is still up")
        XCTAssertFalse(app.descendants(matching: .any)["newAgent.error"].exists, "the start failed")
    }

    func testAnotherDeviceInControlShowsTheObserveBanner() throws {
        let app = openWindow("claude-auth", term: "busy")
        let notice = app.descendants(matching: .any)["agentScreen.notice.text"]
        XCTAssertTrue(notice.waitForExistence(timeout: 10))
        XCTAssertTrue(notice.label.contains("Another device has control"))
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        expectation(for: NSPredicate(format: "value CONTAINS 'scripted session'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 10)
        XCTAssertFalse(app.buttons["agentScreen.key.enter"].isEnabled, "observing: no input")
        sleep(1)
        shot("agent-window-observe")

        // Take over asks first, then the window is live.
        app.buttons["agentScreen.notice.action"].tap()
        let confirm = app.buttons["agentScreen.takeoverConfirm"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5))
        confirm.tap()
        XCTAssertFalse(notice.waitForExistence(timeout: 1) && notice.label.contains("Another device"))
        let enter = app.buttons["agentScreen.key.enter"]
        expectation(for: NSPredicate(format: "isEnabled == true"), evaluatedWith: enter)
        waitForExpectations(timeout: 10)
    }

    func testAClosedAgentSaysSo() throws {
        let app = openWindow("claude-migration", term: "gone")
        // Closed before anything was drawn: the empty state, with the way back.
        let back = app.buttons["agentScreen.ended.back"]
        XCTAssertTrue(back.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["This agent closed."].exists)
        XCTAssertFalse(app.buttons["agentScreen.key.enter"].isEnabled)
        sleep(1)
        shot("agent-window-gone")
        back.tap()
        XCTAssertTrue(app.descendants(matching: .any)["flock.agent.claude-migration"].waitForExistence(timeout: 10), "back to the agents")
    }

    func testHerdrDownFallsBackToTheSnapshot() throws {
        let app = openWindow("claude-migration", term: "herdr")
        let notice = app.descendants(matching: .any)["agentScreen.notice.text"]
        XCTAssertTrue(notice.waitForExistence(timeout: 10))
        XCTAssertTrue(notice.label.contains("herdr is not running"))
        // The snapshot (the demo's screen route) is drawn in the same surface.
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        expectation(for: NSPredicate(format: "value CONTAINS 'make migrate-staging'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 10)
        sleep(1)
        shot("agent-window-herdr")
    }
}
