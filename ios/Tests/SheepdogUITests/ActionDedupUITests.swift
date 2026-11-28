import XCTest

/// A double tap on a slow computer does the thing once
/// (docs/dev/design/ios-state-architecture.md 3.5): Approve and Send go
/// through the computer's `ActionRunner`, and the second tap, landing while
/// the first is in flight, sends nothing. Against `StubDaemon`, which
/// counts the requests; needs no daemon checkout.
final class ActionDedupUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testADoubleTapOnApproveAndSendActsOnce() throws {
        let work = StubDaemon.blocked(alias: "api", space: "webapp", pane: "w1:p1", question: "Run the database migration?")
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[\(work.agent)]", questions: "[\(work.question)]"))
        stub.postDelay = 1.5
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()

        // Approve, twice in a row: one answer reaches the computer.
        let approve = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["question.api.option.1", "approval.option.1"])).firstMatch
        var appeared = approve.waitForExistence(timeout: 20)
        if !appeared {
            // A cold or overloaded simulator can leave the first process
            // unresponsive on initial launch: start it afresh once.
            app.terminate()
            app.launch()
            appeared = approve.waitForExistence(timeout: 30)
        }
        XCTAssertTrue(appeared, "the question's Yes")
        let tappable = Date().addingTimeInterval(10)
        while !approve.isTappable, Date() < tappable { usleep(150_000) }
        XCTAssertTrue(approve.isTappable, "the question's Yes never became tappable")
        approve.doubleTap()
        let answered = app.descendants(matching: .any).matching(NSPredicate(format: "identifier IN %@", ["question.api.answered", "approval.answered"])).firstMatch
        XCTAssertTrue(answered.waitForExistence(timeout: 10), "answered")
        sleep(2)
        XCTAssertEqual(stub.requests("POST /api/pending/answer"), 1, "one answer for two taps")

        // Send, twice in a row: one typed line.
        tapWhenTappable(app.buttons["controls.type"], "the keyboard button")
        typeWhenFocused(app.textFields["type.field"].firstMatch, "status please")
        let send = app.buttons["type.send"]
        XCTAssertTrue(send.waitForExistence(timeout: 10), "Send")
        let ready = Date().addingTimeInterval(10)
        while !send.isTappable, Date() < ready { usleep(150_000) }
        XCTAssertTrue(send.isTappable, "Send never became tappable")
        send.doubleTap()
        let sent = Date().addingTimeInterval(10)
        while stub.requests("POST /api/turn") == 0, Date() < sent { usleep(200_000) }
        sleep(3)
        XCTAssertEqual(stub.requests("POST /api/turn"), 1, "one line for two taps")
    }
}
