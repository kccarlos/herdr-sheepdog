import XCTest

/// Health (from the connection chip's popover) against the fixture daemon (`--brain=fake --voice=off`):
/// version, uptime, the orchestrator picker from `GET /api/brain/models`, and
/// chattiness changed through `PUT /api/settings` and read back from the
/// daemon. Not in CI.
final class HealthUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    func testHealthFromTheChipShowsTheDaemonAndChangesChattiness() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()
        tapWhenReady(app.buttons["controls.orb"], "the orb")

        // Health is not a pane any more: connection chip > Health.
        XCTAssertFalse(app.buttons["main.pane.health"].exists, "Health left the pane switcher")
        tapMenuItem(app, menu: "voice.menu", item: "chip.connection")
        tapWhenReady(app.buttons["chip.health"], "Health in the chip's popover")

        XCTAssertTrue(element(app, "health.version").waitForExistence(timeout: 10))
        let uptime = element(app, "health.uptime")
        XCTAssertTrue(uptime.waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "health.brainModel").waitForExistence(timeout: 10), "the brain, read-only")
        sleep(1)
        shot(app, "32-health-pane")

        let list = app.collectionViews["health.list"].exists ? app.collectionViews["health.list"] : app.collectionViews.firstMatch
        let chattiness = app.segmentedControls["health.chattiness"]
        // A short drag, not a swipe: a swipe flings the shorter list past it.
        for _ in 0 ..< 4 where !(chattiness.exists && chattiness.isTappable) {
            let from = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
            from.press(forDuration: 0.05, thenDragTo: list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45)))
        }
        XCTAssertTrue(chattiness.waitForExistence(timeout: 10))

        chattiness.buttons["Quiet"].tap()
        var level = ""
        for _ in 0 ..< 20 {
            level = (try? JSONSerialization.jsonObject(with: get("\(daemon)/api/settings")) as? [String: Any])?["chattiness"] as? String ?? ""
            if level == "quiet" { break }
            usleep(250_000)
        }
        XCTAssertEqual(level, "quiet", "PUT /api/settings reached the daemon")
        chattiness.buttons["Normal"].tap()
        sleep(1)

        list.swipeUp()
        sleep(1)
        shot(app, "33-health-problems")

        // "Change in Settings ›" opens Settings straight on Models, where
        // the orchestrator picker from /api/brain/models now lives.
        list.swipeDown()
        list.swipeDown()
        let change = app.buttons["health.changeModels"]
        for _ in 0 ..< 6 where !(change.exists && change.isTappable) {
            list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6))
                .press(forDuration: 0.05, thenDragTo: list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)))
        }
        tapWhenReady(change, "Change in Settings")
        XCTAssertTrue(element(app, "models.brainProvider").waitForExistence(timeout: 10), "the brain picker from /api/brain/models")
        sleep(1)
        shot(app, "35-models-from-health")
    }

    private func get(_ url: String) -> Data {
        var out = Data()
        let done = expectation(description: "get")
        URLSession.shared.dataTask(with: URL(string: url)!) { data, _, _ in
            out = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 6)
        return out
    }

    // MARK: - Helpers

    private func element(_ app: XCUIApplication, _ identifier: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: identifier).firstMatch
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isTappable, Date() < deadline { usleep(150_000) }
        element.tap()
    }

    private func reachable(_ base: String) -> Bool {
        guard let url = URL(string: base + "/api/hello") else { return false }
        var ok = false
        let done = expectation(description: "hello")
        var request = URLRequest(url: url)
        request.timeoutInterval = 3
        URLSession.shared.dataTask(with: request) { _, response, _ in
            ok = (response as? HTTPURLResponse)?.statusCode == 200
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 6)
        return ok
    }

    @discardableResult
    private func postJSON(_ url: String, _ body: [String: Any]) throws -> Data {
        var request = URLRequest(url: URL(string: url)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        // The daemon answers once the change is in its state, within its
        // own tool timeout; a busy CI Mac took more than 6 s.
        request.timeoutInterval = 30
        var out = Data()
        let done = expectation(description: "post")
        URLSession.shared.dataTask(with: request) { data, _, _ in
            out = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 35)
        return out
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first else { return }
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"))
    }

}
