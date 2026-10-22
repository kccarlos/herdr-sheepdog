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
        for key in ["esc", "tab", "up", "down", "enter"] {
            XCTAssertTrue(app.buttons["agentScreen.key.\(key)"].exists, key)
        }
        XCTAssertFalse(app.buttons["agentScreen.key.ctrl+c"].exists, "an agent window keeps ⌃C in More keys")
        XCTAssertEqual(app.buttons["agentScreen.key.esc"].label, "Escape")
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
        // The sheet comes from + ▸ New agent; a tap lost while something
        // still animates is retried.
        let space = app.buttons["newAgent.space.shop"]
        for _ in 0 ..< 3 where !space.exists {
            tapMenuItem(app, menu: "toolbar.new", item: "new.agent")
            if space.waitForExistence(timeout: 10) { break }
        }
        XCTAssertTrue(space.exists, "New agent opens on Where")
        shot("new-agent")
        space.tap()
        let start = app.buttons["newAgent.start"]
        XCTAssertTrue(start.waitForExistence(timeout: 10), "Which agent, with Start")
        XCTAssertTrue(app.buttons["newAgent.kind.agy"].exists, "every installed kind, agy too")
        let toggle = app.switches["newAgent.worktree"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0", "New worktree is off by default")
        toggle.switches.firstMatch.tap()
        let branch = app.textFields["newAgent.branch"]
        XCTAssertTrue(branch.waitForExistence(timeout: 5), "New worktree shows the branch field")
        branch.tap()
        branch.typeText("fix-login")
        XCTAssertTrue(start.isEnabled)
        shot("new-agent-worktree")
        start.tap()
        // "Started" shows for 1.4 s, then the sheet closes by itself, which
        // it does only after a successful start.
        let started = app.descendants(matching: .any)["newAgent.started"]
        let startedOrClosed = NSPredicate { _, _ in started.exists || !start.exists }
        wait(for: [XCTNSPredicateExpectation(predicate: startedOrClosed, object: nil)], timeout: 10)
        XCTAssertEqual(app.state, .runningForeground, "the app is still up")
        XCTAssertFalse(app.descendants(matching: .any)["newAgent.error"].exists, "the start failed")
    }

    /// A folder that is not a repository has no worktree switch; a start
    /// that times out says so and offers its pane.
    func testNewAgentFailureStates() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        let folder = app.buttons["newAgent.folder.notes"]
        for _ in 0 ..< 3 where !folder.exists {
            tapMenuItem(app, menu: "toolbar.new", item: "new.agent")
            if folder.waitForExistence(timeout: 10) { break }
        }
        folder.tap()
        XCTAssertTrue(app.descendants(matching: .any)["newAgent.notGit"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.switches["newAgent.worktree"].exists, "no worktree outside a repository")
        let prompt = app.textFields["newAgent.prompt"]
        prompt.tap()
        prompt.typeText("make it fail")
        app.buttons["newAgent.start"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["newAgent.error"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["newAgent.openPane"].exists, "the pane it left open")
        shot("new-agent-failed")
        app.buttons["newAgent.openPane"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10), "Open the pane opens its window")
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
