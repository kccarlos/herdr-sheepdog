import XCTest

/// The Conversation must still be anchored to its newest row after its pane
/// is removed and reinserted. This runs against the paused offline demo so
/// none of the scripted agents add rows while the test switches panes.
final class ConversationPaneSwitchUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testNewestMessageStaysVisibleAfterFivePaneSwitches() throws {
        let app = XCUIApplication()
        app.launchArguments = [
            "-sheepdogResetState",
            "-sheepdogNoAnimations",
            "-sheepdogDemoSilent",
            "-sheepdogDemoPaused",
            "-sheepdogOpenDemo",
        ]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15), "the demo opened")

        // Four exchanges make the lazy stack taller than the viewport.
        let question = visibleButton(app, "demo.question.0")
        for _ in 1 ... 4 {
            tapWhenTappable(question, "the status question")
            waitUntilTappable(question, "the status question")
        }
        let replies = app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS 'claude-auth is still fixing the login'")
        )
        XCTAssertTrue(replies.firstMatch.waitForExistence(timeout: 10), "the conversation has several messages")

        for pass in 1 ... 5 {
            showAgentsPane(app)
            // A row arriving while the Conversation is offscreen must not
            // leave its lazy list scrolled into empty space.
            tapWhenTappable(question, "the status question", file: #filePath, line: #line)
            waitUntilTappable(question, "the status question")
            showConversationPane(app)
            let newestRow = replies.element(boundBy: replies.count - 1)
            XCTAssertTrue(newestRow.waitForExistence(timeout: 2), "pass \(pass): the newest row is still in the list")
            XCTAssertTrue(newestRow.isHittable, "pass \(pass): the newest row is visible without scrolling")
        }
    }

    /// The demo keeps the real screen under its cover, so each tab exists
    /// more than once. Pick the copy that is actually on screen.
    private func visibleButton(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        let matches = app.buttons.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        let deadline = Date().addingTimeInterval(10)
        repeat {
            for index in 0 ..< matches.count where matches.element(boundBy: index).isTappable {
                return matches.element(boundBy: index)
            }
            usleep(150_000)
        } while Date() < deadline
        XCTFail("no visible \(id)")
        return matches.firstMatch
    }

    private func waitUntilTappable(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10) {
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isTappable, Date() < deadline { usleep(150_000) }
        XCTAssertTrue(element.isTappable, "\(what) never became tappable")
    }

    private func showConversationPane(_ app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        let tabs = app.buttons.matching(identifier: "main.pane.conversation")
        XCTAssertTrue(tabs.firstMatch.waitForExistence(timeout: 10), "main.pane.conversation", file: file, line: line)
        let deadline = Date().addingTimeInterval(20)
        repeat {
            for index in 0 ..< tabs.count where tabs.element(boundBy: index).isTappable {
                let tab = tabs.element(boundBy: index)
                if tab.isSelected { return }
                tapWhenTappable(tab, "the Conversation tab", file: file, line: line)
                break
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("the Conversation pane never opened", file: file, line: line)
    }

}
