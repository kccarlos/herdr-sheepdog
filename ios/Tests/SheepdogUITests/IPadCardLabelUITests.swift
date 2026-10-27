import XCTest

/// The card label on iPad: a card that arrives over an agent's window in
/// the detail shows the label, the composer keeps the keyboard, and a tap
/// opens the card. Skipped on iPhone. Screenshots as in
/// `CardLabelUITests` (`ipad-card-label-<name>-<suffix>.png`).
final class IPadCardLabelUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_CARD_LABEL_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_CARD_LABEL_SUFFIX"] ?? "light"
        let image = XCUIScreen.main.screenshot().image
        let upright = UIGraphicsImageRenderer(size: image.size).pngData { _ in image.draw(in: CGRect(origin: .zero, size: image.size)) }
        try? upright.write(to: URL(fileURLWithPath: dir).appendingPathComponent("ipad-card-label-\(name)-\(suffix).png"))
    }

    func testALabelAppearsOverTheDetailAndOpensTheCard() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo",
                               "-sheepdogDemoCard", "-sheepdogDemoCardAfter", "30"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 30))
        tapWhenTappable(app.descendants(matching: .any)["sidebar.agent.claude-auth"].firstMatch, "claude-auth in the sidebar")

        let field = app.textFields["agentScreen.field"]
        tapWhenTappable(field, "the composer")
        typeWhenFocused(field, "ab")

        let label = app.buttons["cardLabel"]
        XCTAssertTrue(label.waitForExistence(timeout: 60), "the card label")
        XCTAssertFalse(app.descendants(matching: .any)["card.title"].exists, "no sheet over the window")
        shot("detail")

        XCTAssertTrue(NSPredicate(format: "hasKeyboardFocus == true").evaluate(with: field), "the composer keeps the keyboard focus")
        field.typeText("c")
        XCTAssertEqual(field.value as? String, "abc")

        label.tap()
        XCTAssertTrue(app.descendants(matching: .any)["card.title"].waitForExistence(timeout: 10), "the label opens the card")
    }
}
