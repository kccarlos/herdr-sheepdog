import XCTest

/// Pairing by code (docs/dev/ws-contract.md "Pairing"). The Simulator has
/// no camera, so the tests open the code the way the voice page hands it
/// to the app (`sheepdog://pair#…`): the app asks first, then pairs. The
/// first two need no daemon (the code points at a closed port); the
/// walkthrough pairs with the pair fixture daemon over pinned TLS, with a
/// fresh one-time code from its loopback `/api/pair?new=1`.
final class PairingLinkUITests: XCTestCase {
    private let deadLink = URL(string: "sheepdog://pair#pair=1&d=deadbeef&n=Studio&k=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&e=code&u=127.0.0.1%3A9")!

    /// Opens the link the way another app does, tapping iOS's "Open in
    /// Sheepdog?" if it comes; once more if neither it nor the sheet came.
    private func open(_ link: URL, in app: XCUIApplication) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let open = springboard.buttons["Open"]
        for _ in 0 ..< 2 {
            app.open(link)
            let deadline = Date().addingTimeInterval(10)
            repeat {
                if open.exists {
                    open.tap()
                    return
                }
                if app.descendants(matching: .any)["pairing.confirm"].exists { return }
                usleep(250_000)
            } while Date() < deadline
        }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testAsksBeforePairingAndCancelGoesBackToSettings() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10), "first run opens Settings")

        open(deadLink, in: app)
        XCTAssertTrue(app.descendants(matching: .any)["pairing.confirm"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Pair with Studio?"].exists)
        app.buttons["pairing.secondary"].tap()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10), "declining returns to the first-run Settings")
    }

    /// The same, with animations on and the session re-rendering ten times
    /// a second: Settings slides away, then the question shows (queued
    /// until Settings reported its dismissal, not after a guessed delay),
    /// stays up, and declining brings Settings back.
    func testPairingLinkOverSettingsIsQueuedNotDropped() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogRenderChurn"]
        app.launch()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10), "first run opens Settings")

        open(deadLink, in: app)
        let confirm = app.descendants(matching: .any)["pairing.confirm"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 10), "the question shows after Settings closed")
        for i in 0 ..< 10 {
            usleep(200_000)
            XCTAssertTrue(confirm.exists, "the question is still up after \(i * 200 + 200) ms")
        }
        app.buttons["pairing.secondary"].tap()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10), "declining returns to the first-run Settings")
    }

    func testPairReportsAnUnreachableCode() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10))

        open(deadLink, in: app)
        XCTAssertTrue(app.buttons["pairing.primary"].waitForExistence(timeout: 10))
        app.buttons["pairing.primary"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["pairing.failed"].waitForExistence(timeout: 20))
        XCTAssertTrue(app.staticTexts["None of the code's addresses answered"].exists)
        XCTAssertTrue(app.buttons["pairing.primary"].exists, "Scan again")
    }

    /// Scan → enroll → a TLS-pinned session → Settings shows the pairing →
    /// Remove. The code is one-time, so each run asks for a new one.
    func testPairingWalkthrough() throws {
        let link = try freshLink()
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogShowStreamErrors"]
        app.launch()
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 10))

        open(link, in: app)
        XCTAssertTrue(app.descendants(matching: .any)["pairing.confirm"].waitForExistence(timeout: 10))
        app.buttons["pairing.primary"].tap()
        let paired = app.descendants(matching: .any)["pairing.paired"]
        XCTAssertTrue(paired.waitForExistence(timeout: 30), "enrolled and signed in")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Paired with'")).firstMatch.exists)
        app.buttons["pairing.primary"].tap()

        // Settings ▸ Connection: the address, the date and the pin.
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        tapWhenReady(app.buttons["settings.daemon"], "Settings ▸ Connection")
        let line = app.descendants(matching: .any)["settings.pairedLine"]
        XCTAssertTrue(line.waitForExistence(timeout: 10))
        app.buttons["settings.test"].tap()
        XCTAssertTrue(app.staticTexts["Encrypted, key checked."].waitForExistence(timeout: 20), "Test connection goes over pinned TLS with a ticket")

        // Remove: the computer forgets this phone; back to the first run.
        app.buttons["settings.remove"].tap()
        tapWhenReady(app.buttons["settings.remove.confirm"].firstMatch, "Remove")
        XCTAssertTrue(app.buttons["settings.scan"].waitForExistence(timeout: 15))
        XCTAssertFalse(app.descendants(matching: .any)["settings.pairedLine"].exists)
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 15) {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline, !(element.exists && element.isTappable) { usleep(200_000) }
        XCTAssertTrue(element.exists, "\(what) never appeared")
        element.tap()
    }

    /// A new pairing code from the pair fixture's loopback-only route, as
    /// the `sheepdog://pair#…` form the voice page hands to the app.
    private func freshLink() throws -> URL {
        let url = URL(string: "http://\(Fixture.pairHost)/api/pair?new=1")!
        var body: Data?
        let done = expectation(description: "pair code")
        URLSession.shared.dataTask(with: url) { data, _, _ in
            body = data
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        guard let body, let object = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
              let link = object["link"] as? String, let hash = link.firstIndex(of: "#")
        else { throw XCTSkip("no pair fixture daemon on \(Fixture.pairHost)") }
        return try XCTUnwrap(URL(string: "sheepdog://pair" + link[hash...]))
    }
}
