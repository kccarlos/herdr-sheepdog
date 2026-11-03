import XCTest

/// The grouped Agents list on the offline demo's fixed attention list
/// (`-sheepdogDemoAttention`, no daemon, nothing scripted to move):
/// codex-docs waits on you, codex-payments works, claude-auth finished a
/// turn nobody looked at, claude-migration finished one that was seen.
///
/// `TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<dir>` saves screenshots there.
final class AgentAttentionUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_REDESIGN_SHOTS"] else { return }
        let suffix = env["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("attention-\(name)-\(suffix).png"))
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogDemoAttention", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        showAgentsPane(app)
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func toggle(_ app: XCUIApplication, _ alias: String) -> XCUIElement {
        app.buttons["flock.agent.\(alias).toggle"].firstMatch
    }

    func testSectionsAreInOrderAndAgentsSitUnderTheirs() throws {
        let app = launch()
        let needs = element(app, "flock.section.needs_you")
        let working = element(app, "flock.section.working")
        let done = element(app, "flock.section.done")
        for header in [needs, working, done] { XCTAssertTrue(header.waitForExistence(timeout: 10), header.identifier) }
        XCTAssertLessThan(needs.frame.minY, working.frame.minY)
        XCTAssertLessThan(working.frame.minY, done.frame.minY)

        let docs = element(app, "flock.agent.codex-docs")
        let payments = element(app, "flock.agent.codex-payments")
        let auth = element(app, "flock.agent.claude-auth")
        let migration = element(app, "flock.agent.claude-migration")
        XCTAssertTrue(migration.waitForExistence(timeout: 5))
        XCTAssertTrue(needs.frame.minY < docs.frame.minY && docs.frame.minY < working.frame.minY, "codex-docs is under Needs you")
        XCTAssertTrue(working.frame.minY < payments.frame.minY && payments.frame.minY < done.frame.minY, "codex-payments is under Working")
        XCTAssertTrue(done.frame.minY < auth.frame.minY, "claude-auth is under Done")
        XCTAssertLessThan(auth.frame.minY, migration.frame.minY, "unread first within Done")
        shot("list")
    }

    func testOnlyTheUnreadAgentIsUnreadAndOpeningItMarksItSeen() throws {
        let app = launch()
        XCTAssertTrue(toggle(app, "claude-auth").waitForExistence(timeout: 10))
        XCTAssertTrue(toggle(app, "claude-auth").value as? String == "Unread", "got \(String(describing: toggle(app, "claude-auth").value))")
        XCTAssertTrue(element(app, "flock.agent.claude-auth.unread").exists)
        XCTAssertFalse(element(app, "flock.agent.claude-migration.unread").exists)
        XCTAssertNotEqual(toggle(app, "claude-migration").value as? String, "Unread")

        // Open its window and come back: the daemon marked the turn seen.
        tapWhenTappable(app.buttons["flock.agent.claude-auth.window"].firstMatch, "claude-auth's window button")
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 15))
        sleep(1)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        showAgentsPane(app)
        let read = NSPredicate(format: "value != 'Unread'")
        expectation(for: read, evaluatedWith: toggle(app, "claude-auth"))
        waitForExpectations(timeout: 15)
        XCTAssertFalse(element(app, "flock.agent.claude-auth.unread").exists)
    }

    func testPinningMovesAnAgentToTheTopOfItsSection() throws {
        let app = launch()
        let auth = element(app, "flock.agent.claude-auth")
        let migration = element(app, "flock.agent.claude-migration")
        XCTAssertTrue(migration.waitForExistence(timeout: 10))
        XCTAssertLessThan(auth.frame.minY, migration.frame.minY)

        // Expand the read agent and pin it from its card.
        tapWhenTappable(toggle(app, "claude-migration"), "claude-migration's card")
        tapWhenTappable(app.buttons["flock.agent.claude-migration.pinButton"].firstMatch, "Pin to top")
        let pinned = element(app, "flock.agent.claude-migration.pinned")
        XCTAssertTrue(pinned.waitForExistence(timeout: 10), "the pin glyph")
        XCTAssertEqual(toggle(app, "claude-migration").value as? String, "Pinned")
        XCTAssertLessThan(migration.frame.minY, auth.frame.minY, "pinned first within Done")
        shot("pinned")

        // And back.
        tapWhenTappable(app.buttons["flock.agent.claude-migration.pinButton"].firstMatch, "Unpin")
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: pinned)
        waitForExpectations(timeout: 10)
        XCTAssertLessThan(auth.frame.minY, migration.frame.minY)
    }

    func testMuteASpaceShowsItOnItsAgentsAndInSpaces() throws {
        let app = launch()
        XCTAssertFalse(element(app, "flock.agent.codex-docs.muted").exists)
        let shop = app.buttons["spaces.row.shop"]
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces", until: shop)
        shop.tap()
        tapWhenTappable(app.buttons["space.mute"].firstMatch, "Mute notifications")
        tapWhenTappable(app.buttons["mute.hour"].firstMatch, "For 1 hour")
        XCTAssertTrue(element(app, "space.muteStatus").waitForExistence(timeout: 10), "the detail says it is muted")
        shot("space-muted")

        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(element(app, "spaces.row.shop.muted").waitForExistence(timeout: 10), "the row says until when")
        app.buttons["spaces.done"].tap()
        showAgentsPane(app)
        XCTAssertTrue(element(app, "flock.agent.codex-docs.muted").waitForExistence(timeout: 15), "bell.slash on its agents")
        XCTAssertTrue(element(app, "flock.agent.claude-auth.muted").exists)
        XCTAssertEqual(toggle(app, "claude-auth").value as? String, "Unread, Muted space")
        // codex-docs waits on you in a muted space: it is listed and answerable.
        XCTAssertTrue(toggle(app, "codex-docs").exists)
    }
}
