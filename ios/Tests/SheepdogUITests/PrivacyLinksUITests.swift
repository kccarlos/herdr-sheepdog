import XCTest

/// The privacy policy and open-source licences are reachable from the
/// first-run Welcome screen and from Settings > About. Needs no daemon: a
/// closed port is enough to open Settings.
///
/// Set `TEST_RUNNER_SHEEPDOG_SHOTS=<dir>` to also save the About screen in
/// light and dark as `about-privacy-{light,dark}.png`.
final class PrivacyLinksUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func tapWhenReady(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared")
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isTappable, Date() < deadline { usleep(150_000) }
        element.tap()
    }

    func testWelcomeLinksToThePrivacyPolicy() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["settings.firstRun.intro"].waitForExistence(timeout: 15))
        let link = app.descendants(matching: .any)["legal.privacy"]
        XCTAssertTrue(link.waitForExistence(timeout: 5), "the Welcome screen links to the privacy policy")
    }

    func testAboutHasThePrivacyPolicyAndLicences() throws {
        addTeardownBlock { XCUIDevice.shared.appearance = .light }
        for style in ["light", "dark"] {
            let app = XCUIApplication()
            app.terminate()
            XCUIDevice.shared.appearance = style == "dark" ? .dark : .light
            app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogAddress", "127.0.0.1:7791"]
            app.launch()
            tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
            for _ in 0 ..< 4 where !app.buttons["settings.about"].isHittable { app.swipeUp() }
            tapWhenReady(app.buttons["settings.about"], "About")

            XCTAssertTrue(app.descendants(matching: .any)["legal.privacy"].waitForExistence(timeout: 10), "About links to the privacy policy")
            let licences = app.buttons["settings.licences"]
            XCTAssertTrue(licences.waitForExistence(timeout: 5), "About links to the licences")
            save(app, "about-privacy-\(style)")

            tapWhenReady(licences, "Licences")
            let swiftTerm = app.buttons["legal.licence.SwiftTerm"]
            tapWhenReady(swiftTerm, "SwiftTerm")
            let text = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'Permission is hereby granted'")).firstMatch
            XCTAssertTrue(text.waitForExistence(timeout: 5), "the SwiftTerm licence text is in the app")
            if style == "dark" { save(app, "about-licence-dark") }
            app.terminate()
        }
    }

    private func save(_ app: XCUIApplication, _ name: String) {
        let shot = app.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_SHOTS"], !dir.isEmpty else { return }
        try? shot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
    }
}
