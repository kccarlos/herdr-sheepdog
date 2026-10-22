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
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host]
        app.launch()
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
