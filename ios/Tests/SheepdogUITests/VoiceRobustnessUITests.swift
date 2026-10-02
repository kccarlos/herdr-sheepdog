import XCTest

/// Screenshots of the voice-robustness UI: the mic button's states, the
/// first-run coach mark, hold-to-talk, the microphone-mode row, the voice
/// provider section and the detector's diagnostics.
///
/// Needs the fixture daemon on 127.0.0.1:7799 (`--voice=off`); the app is
/// launched with `-sheepdogFakeVoice` (pretend the live session is up, no
/// microphone) and `-sheepdogFakeProviders` (the provider contract's
/// example when the daemon has no `/api/voice/providers` yet). Not in CI.
final class VoiceRobustnessUITests: XCTestCase {
    private let daemon = "http://\(Fixture.host)"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard reachable(daemon) else {
            throw XCTSkip("no sheepdog daemon on \(Fixture.host)")
        }
    }

    func testVoiceRobustnessScreens() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host, "-sheepdogFakeVoice", "-sheepdogFakeProviders", "-sheepdogResetHints"]
        app.launch()

        // Connect: the first time the dog listens, the coach mark.
        tapWhenReady(app.buttons["controls.orb"], "the orb")
        XCTAssertTrue(app.staticTexts["Listening — just talk"].waitForExistence(timeout: 15), "the status line says what to do")
        XCTAssertTrue(app.descendants(matching: .any)["controls.coachMark"].waitForExistence(timeout: 5), "first run shows the coach mark")
        let mic = app.buttons["controls.mute"]
        XCTAssertEqual(mic.label, "Mute microphone", "the mic button names its action")
        shot(app, "19-coach-mark")

        tapWhenReady(app.buttons["controls.coachMark.dismiss"], "Got it")
        sleep(1)
        shot(app, "20-mic-live")

        // Muted: red and slashed — the one loud "muted" — and, the first
        // time, the status line says what Mute does instead of a caption of
        // its own; then how to undo it.
        mic.tap()
        XCTAssertTrue(app.staticTexts["Mic off — Sheepdog can still talk"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Muted — tap to unmute"].waitForExistence(timeout: 10))
        XCTAssertEqual(app.buttons["controls.mute"].label, "Unmute microphone")
        sleep(1)
        shot(app, "21-mic-muted")
        app.buttons["controls.mute"].tap()
        XCTAssertTrue(app.staticTexts["Listening — just talk"].waitForExistence(timeout: 5))

        // Settings: Models (voice provider), microphone mode, hold to
        // talk, diagnostics.
        tapWhenReady(app.buttons["toolbar.settings"], "Settings")
        tapWhenReady(app.buttons["settings.models"], "Settings ▸ Models")
        XCTAssertTrue(app.buttons["settings.voiceProvider.openai"].waitForExistence(timeout: 10), "the provider section shows")
        sleep(1)
        shot(app, "34-settings-models")
        tapWhenReady(app.navigationBars["Models"].buttons.firstMatch, "Back to Settings")
        let micMode = app.buttons["settings.microphoneMode"]
        for _ in 0 ..< 4 where !(micMode.exists && micMode.isHittable) {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
                .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)))
        }
        XCTAssertTrue(micMode.waitForExistence(timeout: 5))
        sleep(1)
        shot(app, "22-settings-voice-microphone")

        // The diagnostics, at the bottom.
        for _ in 0 ..< 4 { app.swipeUp() }
        XCTAssertTrue(app.descendants(matching: .any)["settings.diag.gate"].waitForExistence(timeout: 5))
        sleep(1)
        shot(app, "23-settings-voice-detection")

        // Hold to talk on.
        let hold = app.switches["settings.holdToTalk"]
        for _ in 0 ..< 8 where !(hold.exists && hold.isHittable) { app.swipeDown() }
        XCTAssertTrue(hold.waitForExistence(timeout: 5))
        hold.switches.firstMatch.tap()
        tapWhenReady(app.buttons["Cancel"], "Cancel")
        XCTAssertTrue(app.staticTexts["Hold the orb to talk"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["controls.mute"].isHittable, "no mute in hold-to-talk")
        shot(app, "24-hold-to-talk")

        // Hold the orb: the gate opens for as long as the thumb is down.
        app.descendants(matching: .any)["controls.orb"].press(forDuration: 1.0)
        XCTAssertTrue(app.staticTexts["Hold the orb to talk"].waitForExistence(timeout: 5), "released: closed again")

        // Back to open mic for the next run.
        tapWhenReady(app.buttons["toolbar.settings"], "Settings")
        let hold2 = app.switches["settings.holdToTalk"]
        for _ in 0 ..< 6 where !(hold2.exists && hold2.isHittable) { app.swipeUp() }
        hold2.switches.firstMatch.tap()
        tapWhenReady(app.buttons["Cancel"], "Cancel")

        // The connection sheet names the voice provider.
        tapWhenReady(app.buttons["chip.connection"], "the connection chip")
        XCTAssertTrue(app.descendants(matching: .any)["chip.voiceProvider"].waitForExistence(timeout: 10))
        sleep(1)
        shot(app, "25-chip-voice-provider")
    }

    // MARK: - Helpers

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
        guard let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first else { return }
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"))
    }
}
