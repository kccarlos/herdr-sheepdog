import XCTest

/// Screenshots of the UI/UX review's changes, against the fixture daemon
/// built from herdr-sheepdog PR #82 (it has the push routes), in whatever
/// appearance and text size the simulator is set to. Runs only when asked:
///
///     xcodebuild … test -only-testing:SheepdogUITests/ReviewScreenshotsUITests \
///       TEST_RUNNER_SHEEPDOG_SHOTS=light TEST_RUNNER_SHEEPDOG_FIXTURE_PUSH_PORT=7797
///
/// `SHEEPDOG_SHOTS` is the file-name prefix (light, dark, xxxl). Not in CI.
final class ReviewScreenshotsUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var prefix: String { environment["SHEEPDOG_SHOTS"] ?? "" }
    private var host: String { "127.0.0.1:" + (environment["SHEEPDOG_FIXTURE_PUSH_PORT"] ?? "7797") }
    private var daemon: String { "http://\(host)" }
    private let panes = ["p-auth", "p-busy", "p-old", "p-idle"]

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard !prefix.isEmpty else { throw XCTSkip("set SHEEPDOG_SHOTS to take the review screenshots") }
        guard reachable(daemon) else { throw XCTSkip("no sheepdog daemon on \(host)") }
        addUIInterruptionMonitor(withDescription: "system dialog") { element in
            for title in ["Allow", "OK", "Not Now"] where element.buttons[title].exists {
                element.buttons[title].tap()
                return true
            }
            return false
        }
        for pane in panes { post("/api/inject/remove", ["pane_id": pane]) }
    }

    override func tearDownWithError() throws {
        for pane in panes { post("/api/inject/remove", ["pane_id": pane]) }
        try super.tearDownWithError()
    }

    func testFirstRun() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState"]
        app.launch()
        _ = app.textFields["settings.address"].waitForExistence(timeout: 10)
        sleep(2)
        shot(app, "first-run")
    }

    func testTheReviewedScreens() throws {
        // A flock with one of each: working, idle, not running.
        post("/api/inject/blocked", ["pane_id": "p-busy", "alias": "api", "kind": "codex", "screen": "Working…"])
        post("/api/inject/state", ["pane_id": "p-busy", "state": "working"])
        post("/api/inject/blocked", ["pane_id": "p-idle", "alias": "web", "kind": "claude", "screen": "Idle"])
        post("/api/inject/state", ["pane_id": "p-idle", "state": "idle"])
        post("/api/inject/blocked", ["pane_id": "p-old", "alias": "old-spike", "kind": "codex", "screen": "bye"])
        post("/api/inject/state", ["pane_id": "p-old", "state": "unknown"])

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host, "-sheepdogFakeVoice", "-sheepdogFakeProviders", "-sheepdogDemoMarkdown"]
        app.launch()
        XCTAssertTrue(app.staticTexts["Tap the orb to connect"].waitForExistence(timeout: 10))
        shot(app, "voicebar-disconnected")
        tapWhenReady(app.buttons["controls.orb"], "the orb")
        app.tap()

        // The bar, live, then muted: said once, by the red mic button.
        let mic = app.buttons["controls.mute"]
        XCTAssertTrue(mic.waitForExistence(timeout: 10))
        if app.buttons["controls.coachMark.dismiss"].waitForExistence(timeout: 3) { app.buttons["controls.coachMark.dismiss"].tap() }
        clearBanner(app, wait: 6)
        sleep(1)
        shot(app, "voicebar-live")
        tapWhenReady(mic, "Mute")
        sleep(1)
        shot(app, "voicebar-muted")
        tapWhenReady(mic, "Unmute")

        // A worker asks, with a long command and long options.
        post("/api/inject/utterance", ["text": "how is auth doing"])
        post("/api/inject/blocked", ["pane_id": "p-auth", "alias": "auth", "kind": "codex", "screen": Self.longApproval])
        XCTAssertTrue(app.otherElements["approval.card"].waitForExistence(timeout: 15))
        sleep(3)
        clearBanner(app)
        shot(app, "conversation-approval")

        // The Flock pane: running agents, blocked first, the rest folded.
        selectPane(app, "flock")
        sleep(1)
        shot(app, "flock")
        let list = app.scrollViews["flock.list"].exists ? app.scrollViews["flock.list"] : app.scrollViews.firstMatch
        let docs = app.descendants(matching: .any)["flock.agent.docs"]
        for _ in 0 ..< 6 where !(docs.exists && docs.isHittable) { list.swipeUp() }
        let notRunning = app.buttons["flock.notRunning"]
        for _ in 0 ..< 3 where !(notRunning.exists && notRunning.isHittable) { list.swipeUp() }
        if notRunning.exists { notRunning.tap() }
        sleep(1)
        shot(app, "flock-markdown")
        list.swipeUp()
        sleep(1)
        shot(app, "flock-not-running")

        // Answer it, so the card steps aside for the rest.
        selectPane(app, "conversation")
        tapWhenReady(app.buttons["approval.option.1"], "Yes, proceed")
        sleep(5)

        // Health: models read-only, grouped problems.
        selectPane(app, "health")
        XCTAssertTrue(app.descendants(matching: .any)["health.version"].waitForExistence(timeout: 10))
        sleep(1)
        shot(app, "health")
        let change = app.buttons["health.changeModels"]
        let healthList = app.collectionViews.firstMatch
        for _ in 0 ..< 6 where !(change.exists && change.isHittable) {
            healthList.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7)).press(forDuration: 0.05, thenDragTo: healthList.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)))
        }

        // Settings ▸ Models, with the push status of this daemon.
        tapWhenReady(change, "Change in Settings")
        XCTAssertTrue(app.descendants(matching: .any)["models.brainProvider"].waitForExistence(timeout: 10))
        sleep(2)
        shot(app, "models")
        let modelsList = app.collectionViews.firstMatch
        modelsList.swipeUp()
        sleep(1)
        shot(app, "models-notifications")
        backSwipe(app)
        sleep(1)
        shot(app, "settings")
    }

    /// A Codex approval with the kind of command and options the review
    /// saw truncated.
    static let longApproval = """
    Would you like to run the following command?

      $ git log --oneline -20 | tee /tmp/release-notes.txt && git push --force-with-lease origin release/v0.1.0

    › 1. Yes, proceed
      2. Yes, and don't ask again for commands that start with `git push --force-with-lease`
      3. No, and tell Codex what to do differently
    """

    // MARK: - Helpers

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isHittable, Date() < deadline { usleep(150_000) }
        element.tap()
    }

    /// The pane switcher: buttons, or a menu at the accessibility sizes.
    private func selectPane(_ app: XCUIApplication, _ name: String) {
        let button = app.buttons["main.pane.\(name)"]
        if button.waitForExistence(timeout: 3), button.isHittable {
            button.tap()
            return
        }
        let menu = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Show:'")).firstMatch
        tapWhenReady(menu, "the pane menu")
        let item = app.buttons["main.pane.\(name)"].exists ? app.buttons["main.pane.\(name)"] : app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", name.capitalized)).firstMatch
        tapWhenReady(item, "the \(name) item")
    }

    /// Back from Models to Settings.
    private func backSwipe(_ app: XCUIApplication) {
        // The main screen's bar is in the tree too, under the sheet.
        tapWhenReady(app.navigationBars["Models"].buttons.firstMatch, "Back to Settings")
    }

    /// The one-time Voice Isolation tip, out of the way of the shots.
    private func clearBanner(_ app: XCUIApplication, wait: TimeInterval = 0) {
        let dismiss = app.descendants(matching: .any).matching(identifier: "banner").buttons["Dismiss"]
        if dismiss.waitForExistence(timeout: wait), dismiss.isHittable {
            dismiss.tap()
            sleep(1)
        }
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
    private func post(_ path: String, _ body: [String: Any]) -> Int {
        var request = URLRequest(url: URL(string: daemon + path)!)
        request.httpMethod = "POST"
        request.httpBody = try? JSONSerialization.data(withJSONObject: body)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        var status = 0
        let done = expectation(description: "post")
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return status
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(prefix)-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first else { return }
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("review-\(prefix)-\(name).png"))
    }
}
