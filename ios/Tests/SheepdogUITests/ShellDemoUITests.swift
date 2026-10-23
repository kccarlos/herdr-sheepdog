import XCTest

/// The phone shell on the offline demo (no daemon): "Open shell here" in an
/// agent's ⋯ menu opens a shell window in typing mode with the key bar and
/// no composer; Close shell asks first. With `-sheepdogDemoTerm noShell`
/// the daemon offers no shell: no entry point, and Settings says why.
///
/// `TEST_RUNNER_SHEEPDOG_DEMO_SHOTS=<directory>` saves screenshots there
/// (`TEST_RUNNER_SHEEPDOG_DEMO_SHOTS_SUFFIX` is added to each name).
final class ShellDemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_DEMO_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_DEMO_SHOTS_SUFFIX"].map { "-" + $0 } ?? ""
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + suffix + ".png"))
    }

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

    private func launch(term: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent"]
        if let term { app.launchArguments += ["-sheepdogDemoTerm", term] }
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        return app
    }

    func testOpenShellHereFromTheMenu() throws {
        let app = launch()
        tapVisible(app.buttons, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10))
        tapVisible(app.buttons, "agentScreen.menu")
        tapVisible(app.buttons, "agentScreen.menu.shell")

        let header = app.descendants(matching: .any).matching(identifier: "shell.header").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10))
        XCTAssertTrue(header.label.contains("Shell · shop"), header.label)
        // Typing mode from the start; the key bar, and no composer.
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "shell.typingHint").firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons.matching(identifier: "agentScreen.key.esc").firstMatch.exists)
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "agentScreen.field").allElementsBoundByIndex.contains { $0.isTappable }, "no composer")
        let terminal = app.descendants(matching: .any).matching(identifier: "agentScreen.terminal")
            .matching(NSPredicate(format: "value CONTAINS 'shop %'")).firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10), "the shell's prompt")
        sleep(1)
        shot("shell")
        // A few scripted commands answer, about the sample project. The
        // terminal takes keys, not synthesized text: tap them.
        for key in ["l", "s"] { app.keyboards.keys[key].tap() }
        let ret = app.keyboards.buttons.matching(NSPredicate(format: "label ==[c] 'return'")).firstMatch
        if ret.exists { ret.tap() } else { app.keyboards.keys.matching(NSPredicate(format: "label ==[c] 'return'")).firstMatch.tap() }
        let listed = app.descendants(matching: .any).matching(identifier: "agentScreen.terminal")
            .matching(NSPredicate(format: "value CONTAINS 'package.json'")).firstMatch
        if !listed.waitForExistence(timeout: 10) {
            shot("shell-ls-failed")
            XCTFail("ls answers")
        }
        sleep(1)
        shot("shell-ls")

        // Close shell asks first.
        tapVisible(app.buttons, "shell.menu")
        tapVisible(app.buttons, "shell.menu.close")
        let confirm = app.buttons.matching(identifier: "shell.close.confirm").firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5))
        sleep(1)
        shot("shell-close")
        confirm.tap()
        // Back at the agent window: the shell window is gone.
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.descendants(matching: .any).matching(identifier: "shell.header").firstMatch)
        waitForExpectations(timeout: 10)
    }

    func testTheAgentsTabOffersAShell() throws {
        let app = launch()
        let header = app.descendants(matching: .any).matching(identifier: "shell.header").firstMatch
        tapMenuItem(app, menu: "toolbar.new", item: "new.shell", until: header)
        XCTAssertTrue(header.waitForExistence(timeout: 10))
        XCTAssertTrue(header.label.contains("Shell"), header.label)
    }

    func testNoShellOfferedMeansNoEntryAndSettingsSaysWhy() throws {
        let app = launch(term: "noShell")
        tapVisible(app.buttons, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10))
        sleep(1)
        tapVisible(app.buttons, "agentScreen.menu")
        XCTAssertTrue(app.buttons["agentScreen.menu.changes"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["agentScreen.menu.shell"].exists, "no button that fails")

        // Settings says why, with the fix.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap() // close the menu
        sleep(1)
        let navButtons = app.navigationBars.buttons
        for i in 0 ..< navButtons.count where navButtons.element(boundBy: i).isTappable && navButtons.element(boundBy: i).label != "More" {
            navButtons.element(boundBy: i).tap()
            break
        }
        sleep(1)
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        let row = app.descendants(matching: .any).matching(identifier: "settings.shell").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'setup page'")).firstMatch.exists)
        sleep(1)
        shot("settings-shell-off")
    }
}
