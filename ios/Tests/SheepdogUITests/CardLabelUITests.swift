import XCTest

/// The small "Card: <title>" label: a card that arrives while an agent's
/// window is in front shows a label at the top edge instead of a sheet. The
/// terminal's composer keeps the keyboard (typing continues), and a tap on
/// the label opens the card. Offline demo, no daemon; `-sheepdogDemoCard`
/// with `-sheepdogDemoCardAfter` sends the card late enough to open the
/// window first.
///
/// `TEST_RUNNER_SHEEPDOG_CARD_LABEL_SHOTS=<directory>` saves screenshots
/// there as `card-label-<name>-<suffix>.png` (suffix from
/// `TEST_RUNNER_SHEEPDOG_CARD_LABEL_SUFFIX`, light by default).
final class CardLabelUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_CARD_LABEL_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_CARD_LABEL_SUFFIX"] ?? "light"
        let image = XCUIScreen.main.screenshot().image
        let upright = UIGraphicsImageRenderer(size: image.size).pngData { _ in image.draw(in: CGRect(origin: .zero, size: image.size)) }
        try? upright.write(to: URL(fileURLWithPath: dir).appendingPathComponent("card-label-\(name)-\(suffix).png"))
    }

    func testALabelAppearsOverAnAgentWindowAndOpensTheCard() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent",
                               "-sheepdogDemoCard", "-sheepdogDemoCardAfter", "50", "-sheepdogCardLabelSeconds", "120"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        let window = app.buttons.matching(identifier: "flock.agent.claude-auth.window")
        XCTAssertTrue(window.firstMatch.waitForExistence(timeout: 10))
        for i in 0 ..< window.count where window.element(boundBy: i).isTappable {
            window.element(boundBy: i).tap()
            break
        }

        let field = app.textFields["agentScreen.field"]
        tapWhenTappable(field, "the composer")
        typeWhenFocused(field, "ab")

        // The card arrives while the window is in front: a label, no sheet.
        let label = app.buttons["cardLabel"]
        XCTAssertTrue(label.waitForExistence(timeout: 120), "the card label")
        XCTAssertEqual(label.label, "Card ready: Your agents")
        XCTAssertFalse(app.descendants(matching: .any)["card.title"].exists, "no sheet over the window")
        shot("agent-window")

        // The composer keeps the keyboard: typing goes on.
        XCTAssertTrue(NSPredicate(format: "hasKeyboardFocus == true").evaluate(with: field), "the composer keeps the keyboard focus")
        field.typeText("c")
        XCTAssertEqual(field.value as? String, "abc")

        label.tap()
        let title = app.descendants(matching: .any)["card.title"]
        XCTAssertTrue(title.waitForExistence(timeout: 10), "the label opens the card")
        XCTAssertEqual(title.label, "Your agents")
        XCTAssertTrue(label.waitForNonExistence(timeout: 5), "the label goes once the card is open")
    }

    func testTheLabelHidesByItself() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent",
                               "-sheepdogDemoCard", "-sheepdogDemoCardAfter", "20", "-sheepdogCardLabelSeconds", "10"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        let label = app.buttons["cardLabel"]
        XCTAssertTrue(label.waitForExistence(timeout: 60), "the card label over the Agents pane")
        XCTAssertTrue(label.waitForNonExistence(timeout: 40), "the label hides after a few seconds")
        XCTAssertFalse(app.descendants(matching: .any)["card.title"].exists)
    }
}
