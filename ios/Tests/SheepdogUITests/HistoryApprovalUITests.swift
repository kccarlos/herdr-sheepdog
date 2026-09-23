import XCTest

/// History, approve-by-tap and the cache, against the same fake daemon as
/// the walkthrough (`--brain=fake --herdr=mock --voice=off --dev --port
/// 7799`). Expects the daemon to have some conversation and to have been
/// restarted once (so a restart divider exists); see docs/status.md.
///
/// Screenshots go to the runner's Documents directory, like the
/// walkthrough's.
final class HistoryApprovalUITests: XCTestCase {
    private let daemon = "http://127.0.0.1:7799"

    override func setUpWithError() throws {
        continueAfterFailure = false
        addUIInterruptionMonitor(withDescription: "system dialog") { element in
            for title in ["Allow", "OK", "Not Now", "Cancel"] where element.buttons[title].exists {
                element.buttons[title].tap()
                return true
            }
            return false
        }
        guard status("GET", "/api/hello") == 200 else {
            throw XCTSkip("no sheepdog daemon on 127.0.0.1:7799")
        }
    }

    func testHistoryThenApproveByTapThenCache() throws {
        // A previous run leaves the synthetic pane blocked; start it over.
        _ = post("/api/inject/remove", ["pane_id": "p-billing"])

        // A clean phone: nothing cached, then the daemon's address.
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:7799"]
        app.launch()

        // 1. History on launch, without connecting: the earlier
        //    conversation with a restart divider.
        XCTAssertNotNil(find(app, "divider.notice", labelBeginsWith: "Sheepdog restarted", timeout: 15), "the restart divider comes from /api/conversation")
        shot(app, "10-history-restart-divider")

        // 2. Connect, then a worker asks for approval.
        app.buttons["controls.orb"].tap()
        app.tap() // lets the interruption monitor answer a microphone prompt
        XCTAssertEqual(post("/api/inject/blocked", [
            "pane_id": "p-billing", "alias": "billing", "kind": "codex", "screen": Fixtures.codexApproval,
        ]), 200)
        let card = app.otherElements["approval.card"]
        XCTAssertTrue(card.waitForExistence(timeout: 15), "the approval card should arrive")
        let yes = app.buttons["approval.option.1"]
        XCTAssertTrue(yes.waitForExistence(timeout: 5))
        XCTAssertEqual(yes.label, "Approve: Yes, proceed")
        XCTAssertEqual(app.buttons["approval.option.3"].label, "Decline: No, and tell Codex what to do differently")
        shot(app, "11-approval-card")

        // 3. Tap Yes, proceed: the card turns answered.
        yes.tap()
        let answered = app.staticTexts["approval.answered"]
        XCTAssertTrue(answered.waitForExistence(timeout: 10))
        XCTAssertTrue(answered.label.hasPrefix("You approved — Yes, proceed"), "got \(answered.label)")
        shot(app, "12-approval-answered")

        // 4. The pinned card steps aside; the conversation keeps the answer.
        XCTAssertNotNil(find(app, "bubble.approval", labelContains: "You approved — Yes, proceed · by tap", timeout: 15))
        XCTAssertTrue(waitForAbsence(card, timeout: 10))
        shot(app, "13-approval-answered-inline")

        // 5. Kill the app mid-stream and relaunch: the conversation is there
        //    at once, from the cache.
        app.terminate()
        let again = XCUIApplication()
        again.launchArguments = ["-sheepdogAddress", "127.0.0.1:7799"]
        again.launch()
        XCTAssertNotNil(find(again, "bubble.approval", labelContains: "You approved — Yes, proceed", timeout: 3), "the cached conversation shows straight away")
        shot(again, "14-relaunch-cached")
    }

    // MARK: - Helpers

    /// A combined element is an `other` or a `staticText` depending on
    /// what SwiftUI merged; look in both, by identifier.
    private func find(_ app: XCUIApplication, _ identifier: String, labelBeginsWith prefix: String? = nil, labelContains part: String? = nil, timeout: TimeInterval) -> XCUIElement? {
        var format = "identifier == %@"
        var args: [Any] = [identifier]
        if let prefix { format += " AND label BEGINSWITH %@"; args.append(prefix) }
        if let part { format += " AND label CONTAINS %@"; args.append(part) }
        let predicate = NSPredicate(format: format, argumentArray: args)
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            for query in [app.otherElements, app.staticTexts] {
                let match = query.matching(predicate).firstMatch
                if match.exists { return match }
            }
            usleep(250_000)
        } while Date() < deadline
        return nil
    }

    private func waitForAbsence(_ element: XCUIElement, timeout: TimeInterval) -> Bool {
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: element)
        return XCTWaiter().wait(for: [gone], timeout: timeout) == .completed
    }

    private func status(_ method: String, _ path: String) -> Int {
        var request = URLRequest(url: URL(string: daemon + path)!)
        request.httpMethod = method
        request.timeoutInterval = 3
        return send(request)
    }

    private func post(_ path: String, _ body: [String: String]) -> Int {
        var request = URLRequest(url: URL(string: daemon + path)!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: body)
        return send(request)
    }

    private func send(_ request: URLRequest) -> Int {
        let done = expectation(description: request.url!.path)
        var code = 0
        URLSession.shared.dataTask(with: request) { _, response, _ in
            code = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 20)
        return code
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
