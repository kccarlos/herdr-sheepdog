import XCTest

/// Phase 4 on the offline demo (no daemon): the ⋯ menu opens the agent's
/// changes, a file opens its diff, and the composer's 📎 offers its menu.
///
/// `TEST_RUNNER_SHEEPDOG_DEMO_SHOTS=<directory>` saves screenshots there.
final class ChangesDemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_DEMO_SHOTS"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func tapVisible(_ query: XCUIElementQuery, _ id: String) {
        let matches = query.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        // It can exist a moment before it is hittable (a sheet or the
        // demo's cover still animating, slower on a CI Mac): keep looking.
        let deadline = Date().addingTimeInterval(10)
        repeat {
            for i in 0 ..< matches.count where matches.element(boundBy: i).isTappable {
                matches.element(boundBy: i).tap()
                return
            }
            usleep(200_000)
        } while Date() < deadline
        XCTFail("no visible \(id)")
    }

    private func openWindow() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent"]
        app.launch()
        let tryDemo = app.buttons["settings.firstRun.tryDemo"]
        XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
        tryDemo.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 10))
        showAgentsPane(app)
        tapVisible(app.buttons, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.terminal"].waitForExistence(timeout: 10))
        return app
    }

    func testChangesListAndDiff() throws {
        let app = openWindow()
        let file = app.buttons["changes.file.internal/auth/token.go"]
        tapMenuItem(app, menu: "agentScreen.menu", item: "agentScreen.menu.changes", until: file)
        XCTAssertTrue(file.waitForExistence(timeout: 10))
        XCTAssertTrue(file.label.contains("9 added"), file.label)
        sleep(1)
        shot("changes-list")

        file.tap()
        let lines = app.descendants(matching: .any)["diff.lines"]
        XCTAssertTrue(lines.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Added:'")).firstMatch.exists || app.descendants(matching: .any).containing(NSPredicate(format: "label BEGINSWITH 'Added:'")).firstMatch.exists)
        sleep(1)
        shot("changes-diff")
        app.buttons["diff.wrap"].tap()
        XCTAssertTrue(lines.exists)
    }

    func testAttachMenu() throws {
        let app = openWindow()
        let attach = app.buttons["agentScreen.attach"]
        XCTAssertTrue(attach.waitForExistence(timeout: 10))
        XCTAssertEqual(attach.label, "Attach")
        attach.tap()
        XCTAssertTrue(app.buttons["Photo"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["File"].exists)
        sleep(1)
        shot("attach-menu")
    }
}
