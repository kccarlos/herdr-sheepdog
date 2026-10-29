import XCTest

/// Two agents waiting at once, against the fixture daemon: each card on
/// the Agents page has its own answer buttons (`/api/state` `questions`),
/// and the one the voice is not on answers by tap with its own id.
final class QuestionsUITests: XCTestCase {
    private var daemon: String { "http://\(Fixture.host)" }
    private let panes = ["p-qa", "p-qb"]

    private static func screen(_ command: String) -> String {
        """
        Would you like to run the following command?

          $ \(command)

        › 1. Yes, proceed
          2. No, and tell Codex what to do differently
        """
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard post("/api/inject/remove", ["pane_id": "none"]) != 0 else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
        for pane in panes { _ = post("/api/inject/remove", ["pane_id": pane]) }
    }

    override func tearDownWithError() throws {
        for pane in panes { _ = post("/api/inject/remove", ["pane_id": pane]) }
    }

    func testEveryWaitingAgentHasItsOwnButtons() throws {
        XCTAssertEqual(post("/api/inject/blocked", ["pane_id": "p-qa", "alias": "qa", "kind": "codex", "screen": Self.screen("npm test")]), 200)
        XCTAssertEqual(post("/api/inject/blocked", ["pane_id": "p-qb", "alias": "qb", "kind": "codex", "screen": Self.screen("npm run lint")]), 200)

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()

        // On the Conversation both float above the capsule, or, when the
        // two cards do not fit together, one bar says two questions wait.
        let bar = app.buttons["questions.answer"]
        let inline = app.buttons["approval.option.1"]
        let otherFloats = { ["qa", "qb"].contains { app.buttons["question.\($0).option.1"].exists } }
        let shown = NSPredicate { _, _ in (bar.exists && bar.label.hasPrefix("2 questions: ")) || (inline.exists && otherFloats()) }
        expectation(for: shown, evaluatedWith: NSNull())
        waitForExpectations(timeout: 20)
        showAgentsPane(app)

        // The voice is on one of them (approval.*); the other has its own.
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 20), "the voice's question")
        let other = ["qa", "qb"].first { app.buttons["question.\($0).option.1"].exists }
        guard let alias = other else {
            return XCTFail("the second waiting agent has no buttons")
        }
        XCTAssertEqual(app.buttons["question.\(alias).option.2"].label, "Decline: No, and tell Codex what to do differently", "options verbatim")

        app.buttons["question.\(alias).option.1"].tap()
        // Answered, gone, or its agent moved on: the buttons go away.
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons["question.\(alias).option.1"])
        waitForExpectations(timeout: 15)
        XCTAssertTrue(app.buttons["approval.option.1"].exists, "the voice's question is still open")
    }

    /// The common case stays one tap: Codex's real three-option command
    /// approval (testdata/herdr/codex-approval-prompt.screen.txt), whose
    /// middle option wraps, floats inline at the default text size.
    func testCommandApprovalFloatsInline() throws {
        let screen = """
        • Running printf '%s\\n' 'hello from codex.' > hello.txt


          Would you like to run the following command?

          Environment: local

          Reason: May I create the requested hello.txt file in the current workspace?

          $ printf '%s\\n' 'hello from codex.' > hello.txt

        › 1. Yes, proceed (y)
          2. Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt` (p)
          3. No, and tell Codex what to do differently (esc)

          Press enter to confirm or esc to cancel
        """
        XCTAssertEqual(post("/api/inject/blocked", ["pane_id": "p-qa", "alias": "qa", "kind": "codex", "screen": screen]), 200)

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()

        let first = app.buttons["approval.option.1"]
        XCTAssertTrue(first.waitForExistence(timeout: 20), "the approval floats inline")
        XCTAssertTrue(app.buttons["approval.option.3"].exists)
        XCTAssertFalse(app.buttons["questions.answer"].exists, "no bar for a command approval")
        let card = app.descendants(matching: .any)["approval.card"].firstMatch
        XCTContext.runActivity(named: "card \(card.frame.height) pt in a \(app.windows.firstMatch.frame.height) pt window") { _ in }
    }

    /// Claude Code's AskUserQuestion with three described options, read by
    /// the daemon's own parser: each description reaches the app as its own
    /// field, the long question goes to the compact bar, and the sheet
    /// answers it.
    func testDescribedOptionsGoCompactAndAnswerFromTheSheet() throws {
        let rule = String(repeating: "─", count: 72)
        let screen = """
        \(rule)
         ☐ Store

        Where should the agent keep the session store for the new login flow?

        ❯ 1. Keep the file store (Recommended)
             Nothing to migrate; the files stay under the data directory and
             the current backup script keeps working, but two machines cannot
             share them and a crash in the middle of a write can lose a session.
          2. Move everything into SQLite
             One database file with transactions; needs a migration step and
             a new backup command before the switch, and every test that reads
             the files directly has to move to the store's own API first.
          3. Use the existing Redis
             Fast and shared between machines, but the store is lost when Redis
             restarts without persistence, and the login flow then fails until
             the user signs in again on every device they use.
          4. Type something.
        \(rule)
          5. Chat about this

        Enter to select · ↑/↓ to navigate · Esc to cancel
        """
        XCTAssertEqual(post("/api/inject/blocked", ["pane_id": "p-qa", "alias": "qa", "kind": "claude", "screen": screen]), 200)

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", Fixture.host]
        app.launch()

        let bar = app.buttons["questions.answer"]
        let first = app.buttons["approval.option.1"]
        if UIDevice.current.userInterfaceIdiom == .pad {
            // The iPad's column has room for it inline.
            XCTAssertTrue(first.waitForExistence(timeout: 20), "the question floats inline on an iPad")
        } else {
            XCTAssertTrue(bar.waitForExistence(timeout: 20), "the long question collapses to the bar")
            tapWhenTappable(bar, "the Answer bar")
        }
        XCTAssertTrue(first.waitForExistence(timeout: 10), "the sheet shows the options")
        XCTAssertEqual(first.label, "Approve: Keep the file store, recommended. Nothing to migrate; the files stay under the data directory and the current backup script keeps working, but two machines cannot share them and a crash in the middle of a write can lose a session.")
        XCTAssertEqual(app.buttons["approval.option.2"].label, "Approve: Move everything into SQLite. One database file with transactions; needs a migration step and a new backup command before the switch, and every test that reads the files directly has to move to the store's own API first.")
        XCTAssertFalse(app.buttons["approval.option.4"].exists, "Type something. is not an answer")

        first.tap()
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons["approval.option.1"])
        waitForExpectations(timeout: 15)
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
}
