import XCTest

/// The orchestrator's suggested replies under the voice's bubble
/// (docs/dev/design/conversation-timeline.md, section 14): a tap says the
/// option as a typed line, once, the line shows as your typed bubble, and
/// the options stop being buttons. Against `StubDaemon`; needs no daemon
/// checkout.
final class SuggestedRepliesUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testATappedOptionIsSentAsATypedLineAndTheOptionsSettle() throws {
        let stub = try StubDaemon(.computer(
            name: "Work Mac", agents: "[]", questions: "[]",
            reply: (spoken: "Shall it start the echo logging?", options: ["Yes, start the logging", "Not yet"])
        ))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()

        // The reply folds under the voice's bubble: open its Details.
        let details = app.buttons["reply.details"]
        XCTAssertTrue(details.waitForExistence(timeout: 20), "the reply's Details")
        let notYet = app.buttons["Reply: Not yet"]
        let yes = app.buttons["Reply: Yes, start the logging"]
        // The first sync after launch can redraw the row and close it
        // again: open it until the options show.
        let opened = Date().addingTimeInterval(20)
        while !notYet.exists, Date() < opened {
            if details.exists, details.value as? String == "Collapsed" { tapWhenTappable(details, "Details") }
            _ = notYet.waitForExistence(timeout: 2)
        }
        XCTAssertTrue(notYet.exists, "the options are buttons")
        XCTAssertTrue(yes.exists)
        tapWhenTappable(notYet, "Not yet")

        // Your line, as a typed bubble.
        let typed = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label == %@", "bubble.shepherd", "You typed, Not yet")).firstMatch
        XCTAssertTrue(typed.waitForExistence(timeout: 15), "the typed bubble")

        // The options are no longer buttons; the chosen one is marked.
        let gone = Date().addingTimeInterval(5)
        while (notYet.exists || yes.exists), Date() < gone { usleep(200_000) }
        XCTAssertFalse(notYet.exists, "the chosen option is not a button any more")
        XCTAssertFalse(yes.exists, "the other option is not a button any more")
        let chosen = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "reply.option.sent", "Not yet")).firstMatch
        XCTAssertTrue(chosen.waitForExistence(timeout: 5), "the chosen option is marked")

        XCTAssertEqual(stub.requests("POST /api/utterance"), 1, "said once")
    }
}
