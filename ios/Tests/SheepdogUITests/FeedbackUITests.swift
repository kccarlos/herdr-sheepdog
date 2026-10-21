import XCTest

/// The feedback flag over the conversation: tap it, tap Paused, see
/// "Noted", and the daemon has the mark (`user.feedback`).
///
/// Needs the fixture daemon on 127.0.0.1:7799 (`--voice=off`); skips
/// without it, and against a daemon whose `bot-ready` does not list
/// `feedback` (the flag never shows). Not in CI.
final class FeedbackUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard get("/api/hello") != nil else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    func testAFlagTapAndPausedFilesAMark() throws {
        let before = feedbackCount()
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice"]
        app.launch()
        let orb = app.buttons["controls.orb"]
        XCTAssertTrue(orb.waitForExistence(timeout: 10))
        orb.tap()

        let flag = app.buttons["feedback.flag"]
        guard flag.waitForExistence(timeout: 15) else {
            throw XCTSkip("the daemon on \(Fixture.host) does not take feedback marks")
        }
        XCTAssertEqual(flag.label, "Mark this moment")
        flag.tap()

        let paused = app.buttons["feedback.tag.paused"]
        XCTAssertTrue(paused.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["feedback.tag.misheard"].exists, "the other tags are offered too")
        paused.tap()

        // The notice is brief: match its label in the same query, since
        // reading it after the wait can find it already gone on a slow Mac.
        let notice = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == 'feedback.notice' AND label CONTAINS 'Noted'")).firstMatch
        XCTAssertTrue(notice.waitForExistence(timeout: 10), "\"Noted\" after the mark")
        XCTAssertTrue(waitFor { self.feedbackCount() == before + 1 }, "the daemon logged one user.feedback")
        XCTAssertTrue(orb.exists, "the voice session is still there")
    }

    // MARK: - Helpers

    private func feedbackCount() -> Int {
        guard let data = get("/api/events?type=user.feedback&n=1000"),
              let events = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] else { return -1 }
        return events.count
    }

    private func waitFor(timeout: TimeInterval = 10, _ condition: @escaping () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if condition() { return true }
            usleep(250_000)
        } while Date() < deadline
        return false
    }

    private func get(_ path: String) -> Data? {
        guard let url = URL(string: daemon + path) else { return nil }
        var result: Data?
        let done = expectation(description: "GET \(path)")
        URLSession.shared.dataTask(with: url) { data, response, _ in
            if (response as? HTTPURLResponse)?.statusCode == 200 { result = data }
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        return result
    }
}
