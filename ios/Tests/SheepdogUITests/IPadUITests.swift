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
}
