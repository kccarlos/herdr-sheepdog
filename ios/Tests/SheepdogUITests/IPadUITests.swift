import XCTest

/// The iPad layout, in the offline demo (no daemon). Skipped on iPhone;
/// ios.yml runs these on an iPad simulator.
///
/// `TEST_RUNNER_SHEEPDOG_IPAD_SHOTS=<dir>` also saves screenshots there as
/// `ipad-<screen>-<suffix>.png` (`TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX`, light
/// by default). The appearance and text size are the simulator's.
final class IPadUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func launchDemo(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo"] + extra
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    private func shot(_ name: String) {
        guard let directory = environment["SHEEPDOG_IPAD_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        let screenshot = XCUIScreen.main.screenshot()
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent("ipad-\(name)-\(suffix).png"))
    }

    /// Landscape: the sidebar with Conversation, All agents and the agents
    /// by space, the capsule at its foot; the Conversation in the detail.
    /// A sidebar row opens the agent's window in the detail.
    func testSidebarAndDetail() throws {
        let app = launchDemo()
        XCTAssertTrue(element(app, "sidebar.conversation").waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "voice.capsule").exists, "the capsule sits at the sidebar's foot")
        XCTAssertTrue(element(app, "type.field").exists, "the Conversation's composer")
        XCTAssertFalse(element(app, "mini.capsule").exists, "one capsule on screen")
        let agent = element(app, "sidebar.agent.claude-auth")
        XCTAssertTrue(agent.waitForExistence(timeout: 10))
        sleep(2)
        shot("conversation")

        agent.tap()
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10), "the window opens in the detail")
        XCTAssertTrue(element(app, "agentScreen.header").exists)
        sleep(2)
        shot("agent")

        element(app, "sidebar.allAgents").tap()
        XCTAssertTrue(element(app, "flock.agent.claude-auth").waitForExistence(timeout: 10))
        sleep(1)
        shot("all-agents")
    }

    /// All agents: two columns of cards, the filters, and Expand all,
    /// which opens every card that has no question.
    func testAllAgentsFiltersAndExpandAll() throws {
        let app = launchDemo(["-sheepdogDemoMarkdown"])
        let all = element(app, "sidebar.allAgents")
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.tap()
        XCTAssertTrue(element(app, "allAgents.grid").waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "flock.agent.claude-auth").exists)
        element(app, "allAgents.expandAll").tap()
        XCTAssertTrue(element(app, "flock.agent.docs.message").waitForExistence(timeout: 5), "expanded cards show the newest message")
        sleep(1)
        shot("all-agents-expanded")
        element(app, "allAgents.filter.working").tap()
        XCTAssertFalse(element(app, "flock.agent.docs").waitForExistence(timeout: 2), "a finished agent is not working")
        element(app, "allAgents.filter.waiting").tap()
        // The demo's agents are not waiting at the start.
        if element(app, "allAgents.empty").waitForExistence(timeout: 3) {
            shot("all-agents-waiting-empty")
            element(app, "allAgents.showAll").tap()
            XCTAssertTrue(element(app, "flock.agent.docs").waitForExistence(timeout: 5))
        }
    }

    /// Portrait: the sidebar hides, and the detail's header carries Show
    /// sidebar and the mini capsule.
    func testPortraitHidesTheSidebar() throws {
        XCUIDevice.shared.orientation = .portrait
        let app = launchDemo()
        let mini = element(app, "mini.capsule")
        XCTAssertTrue(mini.waitForExistence(timeout: 10), "the mini capsule stands in for the sidebar's")
        XCTAssertTrue(element(app, "sidebar.show").exists)
        sleep(2)
        shot("portrait-conversation")
        element(app, "sidebar.show").tap()
        let agent = element(app, "sidebar.agent.claude-migration")
        XCTAssertTrue(agent.waitForExistence(timeout: 5))
        sleep(1)
        shot("portrait-sidebar")
        agent.tap()
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "mini.capsule").exists)
        sleep(2)
        shot("portrait-agent")
    }

    /// Two agents side by side: the split menu puts one beside the other,
    /// each pane has its own keys and composer and ✕, and a sidebar row
    /// dragged onto the detail replaces the pane that is not focused.
    func testTwoPanesSideBySide() throws {
        let app = launchDemo()
        let auth = element(app, "sidebar.agent.claude-auth")
        XCTAssertTrue(auth.waitForExistence(timeout: 10))
        auth.tap()
        let split = element(app, "pane.split")
        XCTAssertTrue(split.waitForExistence(timeout: 10))
        split.tap()
        let migration = app.buttons["claude-migration"].firstMatch
        XCTAssertTrue(migration.waitForExistence(timeout: 5))
        migration.tap()
        let terminals = app.descendants(matching: .any).matching(identifier: "agentScreen.terminal")
        let two = NSPredicate(format: "count == 2")
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 10)
        XCTAssertTrue(element(app, "panes.divider").exists)
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "pane.close").count, 2)
        sleep(2)
        shot("split")

        // Drag codex-docs onto the detail: it replaces the pane not focused
        // (claude-auth; the new one is focused).
        let docs = element(app, "sidebar.agent.codex-docs")
        docs.press(forDuration: 1.2, thenDragTo: element(app, "panes.divider"))
        let docsHeader = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'agentScreen.header' AND label BEGINSWITH 'codex-docs'")).firstMatch
        XCTAssertTrue(docsHeader.waitForExistence(timeout: 10), "the dropped agent is on screen")
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 10)

        app.descendants(matching: .any).matching(identifier: "pane.close").firstMatch.tap()
        let one = NSPredicate(format: "count == 1")
        expectation(for: one, evaluatedWith: terminals)
        waitForExpectations(timeout: 10)

        XCUIDevice.shared.orientation = .portrait
        sleep(2)
        element(app, "pane.split").tap()
        let payments = app.buttons["codex-payments"].firstMatch
        XCTAssertTrue(payments.waitForExistence(timeout: 5))
        payments.tap()
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 10)
        sleep(2)
        shot("split-portrait")
    }

    /// ⋯ ▸ Open in New Window gives the agent a window of its own, with the
    /// mini capsule.
    func testOpenInNewWindow() throws {
        let app = launchDemo()
        let auth = element(app, "sidebar.agent.claude-auth")
        XCTAssertTrue(auth.waitForExistence(timeout: 10))
        auth.tap()
        let menu = element(app, "agentScreen.menu")
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        let item = element(app, "agentScreen.menu.newWindow")
        XCTAssertTrue(item.waitForExistence(timeout: 5))
        item.tap()
        XCTAssertTrue(element(app, "mini.capsule").waitForExistence(timeout: 10), "the new window has the mini capsule")
        sleep(2)
        shot("new-window")
        // Close it, so the next launch does not restore it.
        element(app, "agentScreen.menu").tap()
        let close = element(app, "agentWindow.closeWindow")
        XCTAssertTrue(close.waitForExistence(timeout: 5))
        close.tap()
        XCTAssertTrue(element(app, "sidebar.conversation").waitForExistence(timeout: 10), "back to the main window")
    }
}
