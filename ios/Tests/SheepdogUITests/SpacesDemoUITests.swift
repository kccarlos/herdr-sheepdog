import XCTest

/// ⋯ ▸ Spaces in the offline demo: the list, a space's page, Close refused
/// while its agents work, and removing a dirty worktree (two confirms).
final class SpacesDemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    /// `TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<dir>` saves screenshots there.
    private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_REDESIGN_SHOTS"] else { return }
        let suffix = env["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("ui-\(name)-\(suffix).png"))
    }

    func testSpacesListDetailAndWorktreeRemoval() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces")
        let shop = app.buttons["spaces.row.shop"]
        XCTAssertTrue(shop.waitForExistence(timeout: 10), "the scripted agents' space")
        XCTAssertTrue(app.buttons["spaces.row.shop-fix-login"].exists, "the worktree space")
        sleep(1)
        shot("spaces")

        // shop: its agents and commands; Close waits for the agents.
        shop.tap()
        XCTAssertTrue(app.buttons["space.agent.claude-auth"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["space.command.Run tests"].exists)
        sleep(1)
        shot("space-detail")
        for _ in 0 ..< 4 where !app.staticTexts["space.close.blocked"].exists { app.swipeUp() }
        XCTAssertTrue(app.staticTexts["space.close.blocked"].exists, "no Close while agents work")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        // The worktree: Remove asks, then asks again for the changes.
        let tree = app.buttons["spaces.row.shop-fix-login"]
        XCTAssertTrue(tree.waitForExistence(timeout: 5))
        tree.tap()
        let menu = app.buttons["space.worktree.shop-fix-login"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        let remove = app.buttons["space.worktree.remove"]
        XCTAssertTrue(remove.waitForExistence(timeout: 5))
        remove.tap()
        let confirm = app.buttons["space.worktree.remove.confirm"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5))
        shot("worktree-remove")
        confirm.tap()
        let force = app.buttons["space.worktree.remove.force"].firstMatch
        XCTAssertTrue(force.waitForExistence(timeout: 5), "a dirty checkout asks a second time")
        force.tap()
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons["spaces.row.shop-fix-login"])
        waitForExpectations(timeout: 10)
    }
}
