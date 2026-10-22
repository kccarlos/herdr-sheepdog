import XCTest

/// Drives the real app against a real daemon started with
/// `--brain=fake --herdr=mock --voice=off --dev --port 7799`, and saves a
/// screenshot of each step into the runner's Documents directory (the host
/// copies them out into `docs/screenshots/`).
///
/// It is deliberately **not** in the `Sheepdog` scheme: CI has no daemon.
/// Run it with `-scheme SheepdogUITests` while the daemon is up.
final class WalkthroughUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"
    private let tokenDaemon = "http://\(Fixture.tokenHost)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        // iOS's "Save Password?" dialog belongs to a system process XCUITest
        // cannot address by name; an interruption monitor is the way to it.
        addUIInterruptionMonitor(withDescription: "system dialog") { element in
            for title in ["Not Now", "Not now", "Cancel", "Dismiss"] {
                let button = element.buttons[title]
                if button.exists {
                    button.tap()
                    return true
                }
            }
            return false
        }
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host) — start one with --voice=off --herdr=mock --brain=fake")
        }
    }

    // MARK: - The walkthrough

    func testWalkthrough() throws {
        // A previous run may have left a synthetic pane behind; the empty
        // The agent list has to be genuinely empty for the first screenshot.
        for pane in ["w1:p1", "w1:p2"] { _ = post("/api/inject/remove", ["pane_id": pane]) }
        let app = launch()

        // 1. First launch: no address saved, so Settings opens itself.
        let address = app.textFields["settings.address"]
        XCTAssertTrue(address.waitForExistence(timeout: 10), "first launch should open Settings")
        XCTAssertFalse(app.buttons["settings.done"].isEnabled, "there is nothing to close Settings on yet")
        shot(app, "01-first-launch-settings")

        // 2. Type the daemon's address and prove it.
        address.tap()
        address.typeText(Fixture.host)
        app.buttons["settings.test"].tap()
        XCTAssertTrue(app.staticTexts["Connected to \(Fixture.host)"].waitForExistence(timeout: 15), "Test connection should report the hello payload")
        XCTAssertTrue(app.staticTexts["Voice name: Sheepdog"].exists)
        XCTAssertTrue(app.staticTexts["Auth: open (loopback)"].exists)
        XCTAssertTrue(app.staticTexts["Audio: 16000 Hz in / 24000 Hz out, pcm16le mono"].exists)
        shot(app, "02-settings-test-connection")

        // 3. Done (there is no Save: the address applies as you leave the
        //    screen): the main screen, not connected yet.
        tapWhenReady(app.buttons["settings.done"], "Done")
        XCTAssertTrue(app.buttons["controls.orb"].waitForExistence(timeout: 10))
        // The daemon's history (if this one has any) now fills the
        // conversation; a fresh data dir leaves it empty.
        shot(app, "03-main-empty")
        // The voice bar before Connect: Mute and End hidden, the orb is a
        // Connect button, and the top-left chip reads the daemon connection
        // itself (grey "Not connected"), never the voice state.
        XCTAssertEqual(menuItemLabel(app, menu: "voice.menu", item: "chip.connection"), "Connection: Not connected")
        shot(app, "15-voicebar-not-connected")

        // 4. Connect. The daemon runs without a voice session and says so
        //    rather than pretending to listen.
        tapWhenReady(app.buttons["controls.orb"], "Connect")
        XCTAssertTrue(app.staticTexts["Voice is off on the daemon"].waitForExistence(timeout: 15), "the orb must be honest about --voice=off")
        shot(app, "16-voicebar-connected-voice-off")
        XCTAssertTrue(app.staticTexts["No agents yet — start one in herdr."].waitForExistence(timeout: 10), "connected, with no agents")

        // The chip's own sheet: address, round trip, daemon/voice info,
        // Reconnect and Open Settings.
        tapMenuItem(app, menu: "voice.menu", item: "chip.connection")
        XCTAssertTrue(app.navigationBars["Connection"].waitForExistence(timeout: 10), "the chip should open its sheet")
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "chip.address").firstMatch.waitForExistence(timeout: 5), "the sheet should show the full address")
        XCTAssertTrue(app.buttons["chip.reconnect"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["chip.openSettings"].exists)
        shot(app, "17-voicebar-chip-popover")
        tapWhenReady(app.buttons["Done"], "Done")

        // 5. A blocked agent appears through /events, then an utterance.
        try inject("/api/inject/blocked", [
            "pane_id": "w1:p1",
            "alias": "auth",
            "kind": "codex",
            "screen": Fixtures.codexApproval,
        ])
        let chip = app.buttons["agent.chip.auth"]
        XCTAssertTrue(chip.waitForExistence(timeout: 15), "the chip should arrive over the event stream")


        try inject("/api/utterance", ["text": "what's going on right now"])
        // Each bubble is one accessibility element, read as a sentence.
        guard let said = bubble(app, "bubble.shepherd", label: { $0.hasSuffix("what's going on right now") }) else {
            shot(app, "debug-before-utterance")
            return XCTFail("the user's words belong in the conversation")
        }
        XCTAssertTrue(said.label.hasSuffix("what's going on right now"), "got \(said.label)")
        guard let answered = bubble(app, "bubble.sheepdog", label: { $0.contains("auth") }) else {
            return XCTFail("and so does the daemon's answer")
        }
        XCTAssertTrue(answered.label.contains("auth"), "got \(answered.label)")
        shot(app, "04-main-agents-conversation")

        // 6. The strip opens the agent window (the same one an Agents card
        //    opens): state, kind, workspace and the open question up top.
        tapWhenReady(chip, "the auth chip")
        if !app.navigationBars["auth"].waitForExistence(timeout: 10) {
            shot(app, "debug-agent-window")
            XCTFail("tapping a chip should open the agent window")
        }
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.state"].waitForExistence(timeout: 5), "the window should show the state")
        XCTAssertTrue(app.descendants(matching: .any)["approval.card"].waitForExistence(timeout: 5), "the window should show the open question (the approval card)")
        shot(app, "05-agent-window")
        tapWhenReady(app.navigationBars["auth"].buttons.firstMatch, "Back")

        // 7. A push: something the daemon decided to say on its own. It is a
        //    system card in the conversation (and a haptic on a device).
        try inject("/api/inject/blocked", ["pane_id": "w1:p2", "alias": "docs", "kind": "claude", "screen": Fixtures.claudePermission])
        XCTAssertTrue(app.buttons["agent.chip.docs"].waitForExistence(timeout: 15))
        // Its pane closing is a push the daemon always makes (the code
        // sentence, not a judgement the fake orchestrator can dedupe away).
        try inject("/api/inject/remove", ["pane_id": "w1:p2"])
        // The newest push about docs (the fixture daemon's history may hold
        // older pushes about other agents).
        let push = app.descendants(matching: .any).matching(identifier: "bubble.push").matching(NSPredicate(format: "label CONTAINS 'docs'")).firstMatch
        // Back-to-back pushes fold into one "N updates" row: open the newest.
        let groups = app.descendants(matching: .any).matching(identifier: "conversation.updates")
        let deadline = Date().addingTimeInterval(20)
        while !push.exists, Date() < deadline {
            if groups.count > 0, groups.element(boundBy: groups.count - 1).label.contains("docs") {
                groups.element(boundBy: groups.count - 1).tap()
            }
            _ = push.waitForExistence(timeout: 1)
        }
        XCTAssertTrue(push.waitForExistence(timeout: 2), "a push belongs in the conversation as a system card")
        shot(app, "06-push")

        // 8. Point it at a port with nothing behind it: a plain sentence,
        //    not a raw error.
        tapMenuItem(app, menu: "voice.menu", item: "controls.end")
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        tapWhenReady(app.buttons["settings.daemon"], "Settings ▸ Daemon")
        let field = app.textFields["settings.address"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.press(forDuration: 1.1)
        if app.menuItems["Select All"].waitForExistence(timeout: 3) { app.menuItems["Select All"].tap() }
        field.typeText("127.0.0.1:7791")
        closeSettings(app)
        tapWhenReady(app.buttons["controls.orb"], "Connect")
        XCTAssertTrue(app.staticTexts["Can't reach the daemon at 127.0.0.1:7791 — is it running?"].waitForExistence(timeout: 20))
        shot(app, "07-error-banner")
        // The failed attempt reads as red "Offline" on the chip — not the
        // grey "Not connected" of a session that was never opened.
        XCTAssertEqual(menuItemLabel(app, menu: "voice.menu", item: "chip.connection"), "Connection: Offline")
        shot(app, "18-voicebar-offline")
    }

    /// The token path, against a second daemon started with
    /// `SHEEPDOG_WEB_BIND=0.0.0.0 SHEEPDOG_WEB_TOKEN=devtoken … --port 7798`.
    func testTokenIsCheckedBeforeAnythingElse() throws {
        try XCTSkipUnless(reachable(tokenDaemon), "no token daemon on \(Fixture.tokenHost)")
        let app = launch()

        let address = app.textFields["settings.address"]
        XCTAssertTrue(address.waitForExistence(timeout: 10))
        address.tap()
        address.typeText(Fixture.tokenHost)

        // A wrong token still passes /api/hello (it is public), so the probe
        // has to go on to /api/state.
        let token = app.secureTextFields["settings.token"]
        token.tap()
        token.typeText("wrong-token")
        app.buttons["settings.test"].tap()
        XCTAssertTrue(
            app.staticTexts["\(Fixture.tokenHost) turned the token down. Check the token in Settings."].waitForExistence(timeout: 15),
            "a wrong token must be named as such"
        )
        shot(app, "08-settings-wrong-token")

        // The right one connects.
        token.tap()
        token.press(forDuration: 1.1)
        if app.menuItems["Select All"].waitForExistence(timeout: 3) { app.menuItems["Select All"].tap() }
        token.typeText("devtoken")
        app.buttons["settings.test"].tap()
        XCTAssertTrue(app.staticTexts["Token accepted."].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["Auth: token required"].exists)
        shot(app, "09-settings-token-accepted")
        tapWhenReady(app.buttons["settings.done"], "Done")
        dismissPasswordPrompt(app)
        if !app.buttons["controls.orb"].isHittable {
            shot(app, "debug-after-token-save")
        }
        tapWhenReady(app.buttons["controls.orb"], "Connect")
        if !app.staticTexts["Voice is off on the daemon"].waitForExistence(timeout: 15) {
            shot(app, "debug-token-connect")
            XCTFail("connecting with the right token should reach the daemon")
        }
    }

    // MARK: - Helpers

    /// Leaves Settings from the Daemon screen: back to the menu (which
    /// applies the address), then Done.
    private func closeSettings(_ app: XCUIApplication) {
        if !app.buttons["settings.done"].isHittable {
            tapWhenReady(app.navigationBars["Daemon"].buttons.firstMatch, "Back to Settings")
        }
        tapWhenReady(app.buttons["settings.done"], "Done")
    }

    /// A sheet that is still animating away leaves the button underneath in
    /// the hierarchy but not hittable, and XCUITest happily taps nothing.
    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isHittable, Date() < deadline { usleep(150_000) }
        XCTAssertTrue(element.isHittable, "\(what) never became tappable", file: file, line: line)
        element.tap()
    }

    /// iOS offers to save anything typed into a `SecureField` to the
    /// keychain. It is a system alert over our window, so it has to go
    /// before the next tap lands.
    private func dismissPasswordPrompt(_ app: XCUIApplication) {
        usleep(700_000)
        // Poking the app lets the interruption monitor run.
        app.tap()
        usleep(400_000)
        guard !app.buttons["controls.orb"].isHittable else { return }
        // Last resort: the dialog's "Not Now" sits at a fixed place, and a
        // coordinate tap hits whatever is topmost on screen.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.315, dy: 0.625)).tap()
        usleep(500_000)
    }

    /// A bubble is one combined accessibility element; SwiftUI reports it as
    /// an `other` or a `staticText` depending on what it merged, so look in
    /// both — and by identifier, because a `.any` query over a growing
    /// conversation times out.
    /// `.firstMatch`, not the exact-one subscript: a push identifier can
    /// legitimately match more than one bubble (e.g. the orchestrator's own
    /// "dropping the earlier question" notice alongside the one this step
    /// is looking for), and either reads fine for what the caller checks.
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

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogShowStreamErrors"]
        app.launch()
        return app
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

    private func inject(_ path: String, _ body: [String: String]) throws {
        XCTAssertEqual(post(path, body), 200, "POST \(path)")
    }

    @discardableResult
    private func post(_ path: String, _ body: [String: String]) -> Int {
        var request = URLRequest(url: URL(string: daemon + path)!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        var status = 0
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 20)
        return status
    }

    /// Saves a PNG into the runner's Documents directory and attaches it to
    /// the result bundle as well.
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

enum Fixtures {
    /// A captured Claude Code permission prompt.
    static let claudePermission = """
    ❯ Create a file named claude.txt in this directory containing the single line: hello from claude.

    ⏺ Write(claude.txt)

     Create file
     claude.txt
      1 hello from claude

     Do you want to create claude.txt?
     ❯ 1. Yes
       2. Yes, and switch to accept edits for this session (shift+tab)
       3. No

     Esc to cancel · Tab to amend
    """

    /// A captured Codex approval prompt (the daemon's own
    /// `testdata/herdr/*.screen.txt`, as `/debug` uses it).
    static let codexApproval = """
    › Create a new file named hello.txt in this directory containing the single line: hello from codex.

    • I'll write hello.txt in the current directory with the requested single line using a shell command.

    • Running printf '%s\\n' 'hello from codex.' > hello.txt


      Would you like to run the following command?

      Environment: local

      Reason: May I create the requested hello.txt file in the current workspace?

      $ printf '%s\\n' 'hello from codex.' > hello.txt

    › 1. Yes, proceed (y)
      2. Yes, and don't ask again for commands that start with `printf` (p)
      3. No, and tell Codex what to do differently (esc)

      Press enter to confirm or esc to cancel
    """
}
