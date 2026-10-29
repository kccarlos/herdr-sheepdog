import XCTest

/// The iPad's menus under a re-render storm (`RenderChurnUITests` has the
/// iPhone's and the helpers): the sidebar's `+` and `⋯`, the capsule's and
/// the floating capsule's voice menu in portrait, a pane's split and ⋯ menus. Skipped on
/// iPhone; ios.yml runs these on an iPad simulator.
final class IPadRenderChurnUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func launchDemo() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogDemoPaused",
                               "-sheepdogOpenDemo", "-sheepdogCountRenders", "-sheepdogRenderChurn"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    func testSidebarAndPaneMenusStayOpenWhileTheSessionRerenders() throws {
        let app = launchDemo()
        XCTAssertTrue(element(app, "sidebar.conversation").waitForExistence(timeout: 10))
        // First: closing a menu taps the sidebar in landscape, which may open an agent, and a terminal has no capsule.
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "voice.menu"), item: app.buttons["voice.manageComputers"], probe: "voice")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "toolbar.new"), item: app.buttons["new.agent"], probe: "new")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "toolbar.more"), item: app.buttons["toolbar.settings"], probe: "more")

        // An agent in the detail: its split and ⋯ menus.
        tapWhenTappable(element(app, "sidebar.agent.claude-auth"), "claude-auth in the sidebar")
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10))
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "pane.split"), item: app.collectionViews.buttons["claude-migration"].firstMatch, probe: "split")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "agentScreen.menu"), item: app.buttons["agentScreen.menu.copy"], probe: "agent")
    }

    /// Portrait, sidebar hidden: the floating capsule's voice menu.
    func testPortraitCapsuleMenuStaysOpenWhileTheSessionRerenders() throws {
        XCUIDevice.shared.orientation = .portrait
        let app = launchDemo()
        let menu = app.buttons.matching(identifier: "voice.menu")
        XCTAssertTrue(menu.firstMatch.waitForExistence(timeout: 10), "the capsule")
        assertMenuStaysOpen(app, menu: menu, item: app.buttons["voice.manageComputers"], probe: "voice")
    }
}
