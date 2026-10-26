import XCTest

/// The demo daemon says macOS has not let it into Documents
/// (`-sheepdogDemoNeedsAccess`): New agent and Spaces show a calm banner
/// above the spaces that did load, and Retry clears it once allowed.
final class NeedsAccessUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogDemoNeedsAccess"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        return app
    }

    /// `TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<dir>` saves screenshots there.
    private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["SHEEPDOG_REDESIGN_SHOTS"] else { return }
        let suffix = env["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(suffix).png"))
    }

    func testNewAgentShowsTheBannerAndRetryClearsIt() throws {
        let app = launch()
        openNewAgent(app)
        let banner = app.descendants(matching: .any)["needsAccess.banner"]
        XCTAssertTrue(banner.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'needs access to Documents'")).firstMatch.exists)
        XCTAssertTrue(app.buttons["newAgent.space.shop"].exists, "the spaces that loaded stay")
        shot("needs-access")
        tapWhenTappable(app.buttons["needsAccess.retry"], "Retry")
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: banner)
        waitForExpectations(timeout: 10)
        XCTAssertTrue(app.buttons["newAgent.space.shop"].waitForExistence(timeout: 10))
    }

    func testSpacesShowsTheBanner() throws {
        let app = launch()
        let shop = app.buttons["spaces.row.shop"]
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces", until: shop)
        XCTAssertTrue(app.descendants(matching: .any)["needsAccess.banner"].exists)
        XCTAssertTrue(app.buttons["needsAccess.retry"].exists)
    }
}
