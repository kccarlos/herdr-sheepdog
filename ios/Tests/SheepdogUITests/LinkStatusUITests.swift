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

    /// No network (`-sheepdogNoNetwork`, a fixed unsatisfied path instead of
    /// `NWPathMonitor`): the status says so, with the plain explanation, and
    /// Connect ends with the same words instead of "can't reach" the
    /// address. `TEST_RUNNER_SHEEPDOG_LINK_SHOTS=<directory>` saves
    /// `arch-p5-no-network-<suffix>.png` and
    /// `arch-p5-no-network-connect-<suffix>.png`
    /// (`TEST_RUNNER_SHEEPDOG_LINK_SHOTS_SUFFIX`: light, dark).
    func testNoNetworkSaysSoAndConnectSaysWhy() throws {
        let stub = try StubDaemon(.daemon(update: nil))
        let host = try stub.start()
        defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", host, "-sheepdogNoNetwork"]
        app.launch()

        XCTAssertTrue(waitForMenuItem(app, menu: "voice.menu", item: "chip.connection", label: "Connection: No network", timeout: 20))
        tapMenuItem(app, menu: "voice.menu", item: "chip.connection", until: app.navigationBars["Connection"])
        let status = app.descendants(matching: .any)["chip.status"]
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertTrue(says(status, "No network"), "\(status.label) \(String(describing: status.value))")
        let detail = app.descendants(matching: .any)["chip.statusDetail"]
        XCTAssertTrue(detail.waitForExistence(timeout: 5))
        XCTAssertTrue(detail.label.contains("This phone is offline"), detail.label)
        shot("no-network")
        app.swipeDown(velocity: .fast)
        XCTAssertTrue(app.navigationBars["Connection"].waitForNonExistence(timeout: 5))

        // Connect: the link's words, not "can't reach 127.0.0.1".
        tapWhenTappable(app.buttons["controls.orb"], "the orb")
        let banner = app.descendants(matching: .any)["banner"]
        XCTAssertTrue(banner.waitForExistence(timeout: 10))
        let words = NSPredicate(format: "label CONTAINS %@", "This phone is offline.")
        XCTAssertTrue(banner.descendants(matching: .any).matching(words).firstMatch.exists || banner.label.contains("This phone is offline."), banner.debugDescription)
        XCTAssertFalse(banner.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", host)).firstMatch.exists, "not the address")
        shot("no-network-connect")
    }

    private func shot(_ name: String) {
        let environment = ProcessInfo.processInfo.environment
        guard let dir = environment["SHEEPDOG_LINK_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_LINK_SHOTS_SUFFIX"] ?? "light"
        try? XCUIScreen.main.screenshot().pngRepresentation
            .write(to: URL(fileURLWithPath: dir).appendingPathComponent("arch-p5-\(name)-\(suffix).png"))
    }

    /// The row's text, wherever SwiftUI put the value (label or value).
    private func says(_ element: XCUIElement, _ word: String) -> Bool {
        guard element.exists else { return false }
        let value = element.value as? String ?? ""
        return value == word || element.label == word || element.label.hasSuffix(", \(word)") || element.label.hasSuffix(" \(word)")
    }
}
