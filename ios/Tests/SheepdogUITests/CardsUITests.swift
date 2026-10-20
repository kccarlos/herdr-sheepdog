import XCTest

/// Cards in the offline demo, with no daemon: "Show me the agents" puts a
/// card on the screen. It opens once by itself as a sheet, stays in the
/// conversation as a row, and the row opens it again.
///
/// `TEST_RUNNER_SHEEPDOG_CARD_SHOTS=<directory>` saves screenshots there,
/// named `cards-sheet-<suffix>.png` and `cards-row-<suffix>.png` with
/// `TEST_RUNNER_SHEEPDOG_CARD_SHOTS_SUFFIX` (light, dark, small-light).
final class CardsUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_CARD_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_CARD_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation
            .write(to: URL(fileURLWithPath: dir).appendingPathComponent("cards-\(name)-\(suffix).png"))
    }

    /// Where the demo's conversation starts: under its agent strip.
    private func conversationTop(_ app: XCUIApplication) -> CGFloat? {
        let chips = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'agent.chip.'"))
        for i in 0 ..< chips.count where chips.element(boundBy: i).isHittable {
            return chips.element(boundBy: i).frame.maxY + 12
        }
        return nil
    }

    func testACardOpensOnceAndReopensFromItsRow() throws {
        let app = XCUIApplication()
        // -sheepdogDemoCard: the demo asks "Show me the agents" by itself.
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogDemoCard"]
        app.launch()

        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))

        // The card opens by itself, with its table.
        let title = app.descendants(matching: .any)["card.title"]
        XCTAssertTrue(title.waitForExistence(timeout: 45), "the new card opens as a sheet")
        XCTAssertEqual(title.label, "Your agents")
        XCTAssertTrue(app.descendants(matching: .any)["card.table"].firstMatch.waitForExistence(timeout: 5))
        sleep(1)
        shot("sheet")

        app.buttons["card.done"].tap()
        XCTAssertTrue(title.waitForNonExistence(timeout: 5))

        // It stays in the conversation, and does not pop up again when the
        // history catches up.
        // Only the demo's conversation has a card row. On a small phone the
        // pinned approval card leaves little room: bring the row into view.
        // (The list is lazy: an off-screen row is not there at all.) On an
        // SE the pinned approval card leaves the list no room: answer it.
        let approve = app.buttons["approval.option.1"]
        if approve.waitForExistence(timeout: 3), approve.isHittable {
            approve.tap()
            sleep(2)
        }
        let row = app.buttons.matching(identifier: "bubble.card").firstMatch
        _ = row.waitForExistence(timeout: 3)
        for _ in 0 ..< 8 where !(row.exists && row.isHittable) {
            // Drag the conversation down a little, just under the tabs.
            guard let top = conversationTop(app) else { break }
            let start = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: app.frame.midX, dy: top + 12))
            start.press(forDuration: 0.05, thenDragTo: start.withOffset(CGVector(dx: 0, dy: 70)))
        }
        if !row.isHittable { shot("debug") }
        XCTAssertTrue(row.isHittable, "the card's row on screen")
        sleep(4)
        XCTAssertFalse(title.exists, "a card opens by itself once")
        XCTAssertEqual(app.buttons.matching(identifier: "bubble.card").count, 1, "one row per card")
        shot("row")

        row.tap()
        XCTAssertTrue(title.waitForExistence(timeout: 5), "the row opens it again")
        app.buttons["card.done"].tap()
        XCTAssertTrue(title.waitForNonExistence(timeout: 5))
    }
}
