import XCTest

/// The agent window against a fixture daemon that has the
/// `/api/agents/{pane}/screen` and `/input` routes (herdr-sheepdog's
/// agent-window PR) on `SHEEPDOG_FIXTURE_PUSH_PORT` (default 7797), run
/// with `--herdr=mock`. Opt-in like the review screenshots:
///
///     xcodebuild … test -only-testing:SheepdogUITests/AgentScreenUITests \
///       TEST_RUNNER_SHEEPDOG_SHOTS=light TEST_RUNNER_SHEEPDOG_FIXTURE_PUSH_PORT=7797
///
/// Opens an agent from its Flock card, checks the screen text arrives,
/// sends Enter and a prompt, confirms Ctrl-C asks first, and saves
/// `review-<prefix>-agent-screen*.png` to the runner's Documents. Not in CI.
final class AgentScreenUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var prefix: String { environment["SHEEPDOG_SHOTS"] ?? "" }
    private var host: String { "127.0.0.1:" + (environment["SHEEPDOG_FIXTURE_PUSH_PORT"] ?? "7797") }
    private var daemon: String { "http://\(host)" }
    private let pane = "p-window"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard !prefix.isEmpty else { throw XCTSkip("set SHEEPDOG_SHOTS to run the agent window test") }
        guard status("GET", "/api/hello") == 200 else { throw XCTSkip("no sheepdog daemon on \(host)") }
        addUIInterruptionMonitor(withDescription: "system dialog") { element in
            for title in ["Allow", "OK", "Not Now"] where element.buttons[title].exists {
                element.buttons[title].tap()
                return true
            }
            return false
        }
        status("POST", "/api/inject/remove", ["pane_id": pane])
    }

    override func tearDownWithError() throws {
        status("POST", "/api/inject/remove", ["pane_id": pane])
        try super.tearDownWithError()
    }

    func testOpenAndOperateAnAgentWindow() throws {
        status("POST", "/api/inject/blocked", ["pane_id": pane, "alias": "auth", "kind": "codex", "screen": Self.screen])
        status("POST", "/api/inject/state", ["pane_id": pane, "state": "working", "screen": Self.screen])
        guard status("GET", "/api/agents/\(pane)/screen") == 200 else {
            throw XCTSkip("the daemon on \(host) has no agent window routes")
        }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", host, "-sheepdogFakeVoice"]
        app.launch()
        let flock = app.buttons["main.pane.flock"]
        XCTAssertTrue(flock.waitForExistence(timeout: 10))
        flock.tap()

        let open = app.buttons["flock.agent.auth.window"]
        XCTAssertTrue(open.waitForExistence(timeout: 15), "the card's Open window button")
        open.tap()

        let text = app.descendants(matching: .any)["agentScreen.text"]
        XCTAssertTrue(text.waitForExistence(timeout: 10))
        let arrived = NSPredicate(format: "label CONTAINS 'make test'")
        expectation(for: arrived, evaluatedWith: text)
        waitForExpectations(timeout: 10)
        sleep(1)
        shot(app, "agent-screen")

        // Enter goes straight through.
        let enter = app.buttons["agentScreen.key.enter"]
        XCTAssertTrue(enter.isEnabled)
        enter.tap()
        sleep(1)

        // Ctrl-C asks first; cancel it.
        app.buttons["agentScreen.key.ctrl+c"].tap()
        let confirm = app.buttons["agentScreen.confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "Ctrl-C asks for confirmation")
        shot(app, "agent-screen-confirm")
        // iOS 26+ shows the dialog as a popover without a Cancel button:
        // a tap outside dismisses it.
        if app.buttons["Cancel"].exists {
            app.buttons["Cancel"].firstMatch.tap()
        } else {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75)).tap()
        }
        XCTAssertFalse(confirm.waitForExistence(timeout: 2), "cancelling sends nothing")

        // A prompt shows up on the (mock) screen.
        let field = app.textFields["agentScreen.field"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("run the integration tests too")
        app.buttons["agentScreen.send"].tap()
        let echoed = NSPredicate(format: "label CONTAINS 'run the integration tests too'")
        expectation(for: echoed, evaluatedWith: text)
        waitForExpectations(timeout: 10)
        if app.keyboards.firstMatch.exists { app.descendants(matching: .any)["agentScreen.text"].tap() }
        sleep(1)
        shot(app, "agent-screen-sent")
    }

    /// A Codex TUI mid-turn, with lines wider than the phone.
    static let screen = """
    ╭──────────────────────────────────────────────────────────────────────────────────╮
    │ >_ OpenAI Codex (v0.44.0)                                                        │
    │ model: gpt-5-codex high   directory: ~/src/auth-service                          │
    ╰──────────────────────────────────────────────────────────────────────────────────╯

    › fix the token refresh race in middleware/session.go and add a test

    • Explored
      └ Read session.go, session_test.go, refresh.go

    • Edited middleware/session.go (+18 -6)
        41  -   if tok.Expired() { tok = refresh(ctx, tok) }
        41  +   s.mu.Lock()
        42  +   defer s.mu.Unlock()
        43  +   if tok.Expired() { tok, err = s.refresher.Refresh(ctx, tok); if err != nil { return err } }

    • Ran make test
      └ ok   github.com/acme/auth-service/middleware   0.412s
        ok   github.com/acme/auth-service/store        0.128s

    • Working (42s • esc to interrupt)

    ▌ Ask Codex to do anything
      ⏎ send   ⌃J newline   ⌃T transcript   ⌃C quit                        62% context left
    """

    // MARK: - Helpers

    @discardableResult
    private func status(_ method: String, _ path: String, _ body: [String: Any]? = nil) -> Int {
        var request = URLRequest(url: URL(string: daemon + path)!)
        request.httpMethod = method
        request.timeoutInterval = 5
        if let body {
            request.httpBody = try? JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        var code = 0
        let done = expectation(description: "\(method) \(path)")
        URLSession.shared.dataTask(with: request) { _, response, _ in
            code = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return code
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
