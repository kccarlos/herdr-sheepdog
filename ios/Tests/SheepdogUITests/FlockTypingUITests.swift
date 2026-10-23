import XCTest

/// The Agents pane and typed input, against the fixture daemon
/// (`--brain=fake --herdr=mock --voice=off`): an injected worker question
/// answered from its agent card, and a typed utterance answered by the
/// daemon's text router. Not in CI.
final class FlockTypingUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        addUIInterruptionMonitor(withDescription: "system dialog") { element in
            for title in ["Allow", "OK", "Not Now", "Cancel"] where element.buttons[title].exists {
                element.buttons[title].tap()
                return true
            }
            return false
        }
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    override func tearDownWithError() throws {
        _ = try? postJSON("\(daemon)/api/inject/remove", ["pane_id": "p-flock"])
        try super.tearDownWithError()
    }

    func testFlockPaneAnswersInlineAndTypedInputReachesTheDaemon() throws {
        _ = try? postJSON("\(daemon)/api/inject/remove", ["pane_id": "p-flock"])
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()

        // Connect, then a worker asks.
        tapWhenReady(app.buttons["controls.orb"], "the orb")
        app.tap()
        _ = try postJSON("\(daemon)/api/inject/blocked", [
            "pane_id": "p-flock", "alias": "deploy", "kind": "codex", "screen": Self.codexApproval,
        ])
        XCTAssertTrue(app.otherElements["approval.card"].waitForExistence(timeout: 15), "the pinned card first")

        // The Agents pane: the agent's card, with the question on it.
        tapWhenReady(app.buttons["main.pane.flock"], "the Agents pane")
        XCTAssertTrue(element(app, "flock.agent.deploy").waitForExistence(timeout: 10), "the deploy card")
        XCTAssertTrue(element(app, "flock.agent.deploy.state").exists, "state and how long")
        XCTAssertTrue(app.otherElements["approval.card"].waitForExistence(timeout: 5), "the question sits on its agent's card")
        sleep(1)
        shot(app, "28-flock-pane-question")

        tapWhenReady(app.buttons["approval.option.1"], "Yes, proceed")
        XCTAssertTrue(app.staticTexts["approval.answered"].waitForExistence(timeout: 10), "answered from the Flock pane")
        sleep(1)
        shot(app, "29-flock-pane-answered")

        // Back to the conversation, and type instead of talking.
        tapWhenReady(app.buttons["main.pane.conversation"], "the Conversation pane")
        tapWhenReady(app.buttons["controls.type"], "the keyboard button")
        let field = element(app, "type.field")
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("what is deploy doing")
        shot(app, "30-type-instead")
        tapWhenReady(app.buttons["type.send"], "Send")

        // The daemon's text router answers; both land from /events.
        let said = app.descendants(matching: .any).matching(NSPredicate(format: "label ENDSWITH %@", "what is deploy doing")).firstMatch
        XCTAssertTrue(said.waitForExistence(timeout: 15), "the typed words show as the user's")
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "bubble.sheepdog").firstMatch.waitForExistence(timeout: 15), "and the dog answers")
        sleep(1)
        shot(app, "31-typed-answered")
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

    /// A Codex approval prompt, as herdr's screen shows it.
    static let codexApproval = """
    Would you like to run the following command?

      $ ./scripts/deploy.sh --dry-run

    › 1. Yes, proceed
      2. Yes, and don't ask again for this command
      3. No, and tell Codex what to do differently
    """
}
