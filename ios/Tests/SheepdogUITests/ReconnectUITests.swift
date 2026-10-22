import XCTest

/// A daemon restart mid-session: the app reconnects by itself, with no
/// banner. (It used to end the session on the first failed retry and then
/// say "Reconnecting…" forever.)
///
/// Needs the fixture daemon on 127.0.0.1:7799 (`--voice=off`) and
/// `scripts/fixture-daemon-restarter.sh` running on the Mac: the test asks
/// for a restart by creating `/tmp/sd-bugs/control/restart`, and the script
/// kills the daemon and starts it again 3 s later. Skipped without either.
/// ios.yml runs it with the restarter in its own step.
final class ReconnectUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"
    private let control = URL(fileURLWithPath: Fixture.controlDir)

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
        guard FileManager.default.fileExists(atPath: control.path) else {
            throw XCTSkip("scripts/fixture-daemon-restarter.sh is not running (no \(control.path))")
        }
    }

    func testADaemonRestartReconnectsByItselfWithNoBanner() throws {
        let app = XCUIApplication()
        // The one-time Voice Isolation tip is a banner too, and it comes the
        // second time the voice listens, which is the reconnect: mark it as
        // shown (an argument-domain default outlives the reset).
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice", "-hint.micmode.shown", "YES"]
        app.launch()

        let status = app.staticTexts["controls.statusLine"]
        tapWhenReady(app.buttons["controls.orb"], "the orb")
        XCTAssertTrue(waitFor(status, label: "Listening — just talk", timeout: 15), "connected first; got \(status.label)")

        // Kill the daemon; it comes back 3 s later.
        try? FileManager.default.removeItem(at: control.appendingPathComponent("restarted"))
        FileManager.default.createFile(atPath: control.appendingPathComponent("restart").path, contents: Data())

        let banner = app.descendants(matching: .any)["banner"]
        XCTAssertTrue(waitFor(status, label: "Reconnecting…", timeout: 10, noBanner: banner), "the drop shows as Reconnecting…; got \(status.label)")
        shot(app, "28-reconnecting-quietly")
        XCTAssertTrue(waitFor(status, label: "Listening — just talk", timeout: 30, noBanner: banner), "reconnects by itself once the daemon is back; got \(status.label)")
        XCTAssertTrue(FileManager.default.fileExists(atPath: control.appendingPathComponent("restarted").path), "the daemon really was restarted")
        XCTAssertFalse(banner.exists, "no banner for a restart shorter than the quiet period")
        // The event stream retries on its own 2 s timer, so the chip may
        // lag the socket by a moment.
        XCTAssertTrue(waitForMenuItem(app, menu: "voice.menu", item: "chip.connection", label: "Connection: Connected", timeout: 20))
        XCTAssertFalse(banner.exists, "no banner")
    }

    // MARK: - Helpers

    /// Polls until `element`'s label is `label`, failing at once if a
    /// banner shows up meanwhile.
    private func waitFor(_ element: XCUIElement, label: String, timeout: TimeInterval, noBanner banner: XCUIElement? = nil, file: StaticString = #filePath, line: UInt = #line) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if let banner, banner.exists {
                XCTFail("a banner appeared: \(banner.debugDescription)", file: file, line: line)
                return false
            }
            if element.exists, element.label == label { return true }
            usleep(250_000)
        } while Date() < deadline
        return false
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isHittable, Date() < deadline { usleep(150_000) }
        element.tap()
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

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
