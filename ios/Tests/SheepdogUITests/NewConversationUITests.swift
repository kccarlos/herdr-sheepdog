import XCTest

/// Screenshots of "New conversation": the ⋯ menu, the confirmation dialog,
/// and the divider it leaves in the conversation.
///
/// Needs the fixture daemon on 127.0.0.1:7799 (`--voice=off`, empty
/// config), and runs against its real `POST /api/conversation/reset`: the
/// divider comes from the daemon's own `conversation.reset` notice.
final class NewConversationUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    func testNewConversationMenuDialogAndDivider() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host]
        // Scrollback to keep and see a divider land under: an utterance
        // logged on the daemon ahead of time, so it is already on the
        // newest `/api/conversation` page the app fetches at launch —
        // no need to be connected for "New conversation" to work.
        _ = try? postJSON("\(daemon)/api/inject/utterance", ["text": "what's going on"])

        app.launch()
        // Each bubble is one accessibility element, read as a sentence
        // ("You said, what's going on"), not a plain-text label.
        guard let said = bubble(app, "bubble.shepherd", label: { $0.hasSuffix("what's going on") }) else {
            return XCTFail("the scrollback should load from GET /api/conversation at launch")
        }
        XCTAssertTrue(said.label.hasSuffix("what's going on"), "got \(said.label)")

        // The ⋯ menu: New conversation.
        tapWhenReady(app.buttons["toolbar.more"], "the ⋯ menu")
        tapWhenReady(app.buttons["toolbar.newConversation"], "New conversation")

        // The confirmation dialog.
        XCTAssertTrue(app.staticTexts["Start a new conversation?"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["Start fresh"].waitForExistence(timeout: 5))
        sleep(1)
        shot(app, "26-new-conversation-confirm")

        tapWhenReady(app.buttons["Start fresh"], "Start fresh")

        // The divider lands in the conversation; the scrollback above it stays.
        XCTAssertNotNil(bubble(app, "bubble.shepherd"), "scrollback above the divider stays")
        XCTAssertTrue(app.descendants(matching: .any)["divider.notice"].waitForExistence(timeout: 10), "the reset divider appears")
        sleep(1)
        shot(app, "27-new-conversation-divider")
    }

    // MARK: - Helpers

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isHittable, Date() < deadline { usleep(150_000) }
        element.tap()
    }

    /// The first bubble with this identifier whose label passes `label`.
    /// The fixture daemon is shared by the whole suite, so earlier tests'
    /// bubbles may already be in the history; look for this test's own.
    private func bubble(_ app: XCUIApplication, _ identifier: String, timeout: TimeInterval = 15, label: (String) -> Bool = { _ in true }) -> XCUIElement? {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            for query in [app.otherElements, app.staticTexts, app.buttons] {
                for element in query.matching(identifier: identifier).allElementsBoundByIndex where element.exists && label(element.label) {
                    return element
                }
            }
            usleep(250_000)
        } while Date() < deadline
        return nil
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
        var out = Data()
        let done = expectation(description: "post")
        URLSession.shared.dataTask(with: request) { data, _, _ in
            out = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 6)
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
