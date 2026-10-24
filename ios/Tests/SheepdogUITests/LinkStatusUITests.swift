import XCTest

/// The status every surface shows is the computer's link, never one
/// channel. Needs no daemon: `StubDaemon(.daemon)` answers `/api/hello`
/// and `/api/state` and 404s everything else, including `/events`, so the
/// stream fails on every try while the computer answers.
///
/// Regression for TestFlight 0.1.17/0.1.18, where Settings said
/// "Reconnecting…" for good because `/events` never opened, while the voice
/// worked.
final class LinkStatusUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testAStreamThatKeepsFailingLeavesTheStatusConnected() throws {
        let stub = try StubDaemon(.daemon(update: nil))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", host]
        app.launch()

        // With the voice off: the computer answers, so the status says so.
        XCTAssertTrue(waitForMenuItem(app, menu: "voice.menu", item: "chip.connection", label: "Connection: Connected", timeout: 20))
        tapMenuItem(app, menu: "voice.menu", item: "chip.connection", until: app.navigationBars["Connection"])
        let status = app.descendants(matching: .any)["chip.status"]
        XCTAssertTrue(status.waitForExistence(timeout: 10))

        // `/events` fails again and again meanwhile; the status stays.
        let deadline = Date().addingTimeInterval(8)
        while Date() < deadline {
            XCTAssertTrue(says(status, "Connected"), "the status left Connected: \(status.label) \(String(describing: status.value))")
            XCTAssertFalse(app.descendants(matching: .any)["chip.statusDetail"].exists, "no outage explanation while connected")
            usleep(500_000)
        }

        // Health is where the stream's trouble shows.
        tapWhenTappable(app.buttons["chip.health"], "Health")
        let live = app.descendants(matching: .any)["health.liveUpdates"]
        XCTAssertTrue(live.waitForExistence(timeout: 10))
        XCTAssertFalse(says(live, "Connected"), "Live updates is not Connected: \(live.label)")
        XCTAssertTrue(says(app.descendants(matching: .any)["health.connection"], "Connected"))
    }

    /// The row's text, wherever SwiftUI put the value (label or value).
    private func says(_ element: XCUIElement, _ word: String) -> Bool {
        guard element.exists else { return false }
        let value = element.value as? String ?? ""
        return value == word || element.label == word || element.label.hasSuffix(", \(word)") || element.label.hasSuffix(" \(word)")
    }
}
