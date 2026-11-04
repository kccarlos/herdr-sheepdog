import XCTest

/// The grouped Agents list on the offline demo's fixed attention list
/// (`-sheepdogDemoAttention`, no daemon, nothing scripted to move):
/// codex-docs waits on you, codex-payments works, claude-auth finished a
/// turn nobody looked at, claude-migration finished one that was seen.
///
/// `TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<dir>` saves screenshots there.
///
/// `AgentAttentionLargestTextUITests` runs the same tests at the largest
/// accessibility text size, where only a few rows fit and the voice capsule
/// is a row of its own: they scroll to a row before they use it.
class AgentAttentionUITests: XCTestCase {
    /// Extra launch arguments; the largest-text subclass sets the size.
    var textSizeArguments: [String] { [] }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_REDESIGN_SHOTS"] else { return }
        let suffix = (env["SHEEPDOG_SHOTS_SUFFIX"] ?? "light") + (textSizeArguments.isEmpty ? "" : "-xxxl")
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("attention-\(name)-\(suffix).png"))
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogDemoAttention", "-sheepdogOpenDemo"] + textSizeArguments
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

    /// The agent's card header, scrolled into view from the top of the list.
    private func showToggle(_ app: XCUIApplication, _ alias: String) -> XCUIElement {
        scrollToTop(app)
        return scrollToVisible(app, toggle(app, alias))
    }

    func testSectionsAreInOrderAndAgentsSitUnderTheirs() throws {
        let app = launch()
        // Top to bottom, as the list is read: at large text only the rows
        // on screen are in the tree, so each is found by scrolling down.
        assertListOrder(app, [
            "flock.section.needs_you", "flock.agent.codex-docs",
            "flock.section.working", "flock.agent.codex-payments",
            "flock.section.done", "flock.agent.claude-auth", "flock.agent.claude-migration",
        ])
        shot("list")
    }

    func testOnlyTheUnreadAgentIsUnreadAndOpeningItMarksItSeen() throws {
        let app = launch()
        let auth = showToggle(app, "claude-auth")
        XCTAssertTrue(auth.value as? String == "Unread", "got \(String(describing: auth.value))")
        XCTAssertTrue(element(app, "flock.agent.claude-auth.unread").exists)
        XCTAssertFalse(element(app, "flock.agent.claude-migration.unread").exists)
        XCTAssertNotEqual(showToggle(app, "claude-migration").value as? String, "Unread")

        // Open its window and come back: the daemon marked the turn seen.
        _ = showToggle(app, "claude-auth")
        tapWhenTappable(scrollToVisible(app, app.buttons["flock.agent.claude-auth.window"].firstMatch), "claude-auth's window button")
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 15))
        sleep(1)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        showAgentsPane(app)
        let read = NSPredicate(format: "value != 'Unread'")
        expectation(for: read, evaluatedWith: showToggle(app, "claude-auth"))
        waitForExpectations(timeout: 15)
        XCTAssertFalse(element(app, "flock.agent.claude-auth.unread").exists)
    }

    func testPinningMovesAnAgentToTheTopOfItsSection() throws {
        let app = launch()
        let pinButton = app.buttons["flock.agent.claude-migration.pinButton"].firstMatch
        assertListOrder(app, ["flock.agent.claude-auth", "flock.agent.claude-migration"])

        // Expand the read agent and pin it from its card.
        tapWhenTappable(showToggle(app, "claude-migration"), "claude-migration's card")
        tapWhenTappable(revealAboveCapsule(app, scrollToVisible(app, pinButton)), "Pin to top")
        // The card moved up the list: find it again from the top.
        let pinned = element(app, "flock.agent.claude-migration.pinned")
        XCTAssertEqual(showToggle(app, "claude-migration").value as? String, "Pinned")
        XCTAssertTrue(pinned.waitForExistence(timeout: 10), "the pin glyph")
        assertListOrder(app, ["flock.agent.claude-migration", "flock.agent.claude-auth"])
        shot("pinned")

        // And back.
        _ = showToggle(app, "claude-migration")
        tapWhenTappable(revealAboveCapsule(app, scrollToVisible(app, pinButton)), "Unpin")
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: pinned)
        waitForExpectations(timeout: 10)
        assertListOrder(app, ["flock.agent.claude-auth", "flock.agent.claude-migration"])
    }

    func testMuteASpaceShowsItOnItsAgentsAndInSpaces() throws {
        let app = launch()
        XCTAssertFalse(element(app, "flock.agent.codex-docs.muted").exists)
        let shop = app.buttons["spaces.row.shop"]
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces", until: shop)
        shop.tap()
        tapWhenTappable(scrollToVisible(app, app.buttons["space.mute"].firstMatch), "Mute notifications")
        tapWhenTappable(scrollToVisible(app, app.buttons["mute.hour"].firstMatch), "For 1 hour")
        XCTAssertTrue(scrollToVisible(app, element(app, "space.muteStatus")).exists, "the detail says it is muted")
        shot("space-muted")

        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(scrollToVisible(app, element(app, "spaces.row.shop.muted")).exists, "the row says until when")
        tapWhenTappable(scrollToVisible(app, app.buttons["spaces.done"]), "Done")
        showAgentsPane(app)
        // codex-docs waits on you in a muted space: it is listed and answerable.
        _ = showToggle(app, "codex-docs")
        XCTAssertTrue(element(app, "flock.agent.codex-docs.muted").waitForExistence(timeout: 15), "bell.slash on its agents")
        _ = showToggle(app, "claude-auth")
        XCTAssertTrue(element(app, "flock.agent.claude-auth.muted").exists)
        XCTAssertEqual(toggle(app, "claude-auth").value as? String, "Unread, Muted space")
    }
}

/// The same tests at the largest accessibility text size.
final class AgentAttentionLargestTextUITests: AgentAttentionUITests {
    override var textSizeArguments: [String] {
        ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
    }
}
