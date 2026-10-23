import XCTest

/// Menus and windows that must stay still while the app re-renders around
/// them (docs/dev/design/ios-state-architecture.md, 3.7 and phase 2).
///
/// `-sheepdogRenderChurn` makes the session view models announce a change
/// ten times a second, as a live voice session does; `-sheepdogFrameChurn`
/// moves the demo terminal's status line twenty times a second. With
/// `-sheepdogCountRenders` the app counts change announcements, terminal
/// frames and menu body evaluations in a hidden element
/// (`RenderCounts`). Each menu test opens a menu under churn and checks
/// that it stays open with the same items, and that its body was not
/// evaluated again.
final class RenderChurnUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    /// The offline demo, paused at its opening (nothing finishes or asks).
    private func launchDemo(_ extra: [String] = ["-sheepdogRenderChurn"]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogDemoPaused",
                               "-sheepdogOpenDemo", "-sheepdogCountRenders"] + extra
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15))
        return app
    }

    func testMainScreenMenusStayOpenWhileTheSessionRerenders() throws {
        let app = launchDemo()
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "voice.menu"), item: app.buttons["voice.manageComputers"], probe: "voice")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "toolbar.new"), item: app.buttons["new.agent"], probe: "new")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "toolbar.more"), item: app.buttons["toolbar.settings"], probe: "more")
    }

    func testAgentWindowMenusStayOpenWhileTheSessionRerenders() throws {
        let app = launchDemo()
        openWindow(app, "claude-auth")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "agentScreen.menu"), item: app.buttons["agentScreen.menu.copy"], probe: "agent")
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "agentScreen.attach"), item: app.buttons["Photo"], probe: "attach")
    }

    // The shell and Spaces sit behind a menu item. On the CI Mac, a menu
    // item tapped while the churn runs did not open its screen, so these
    // tap their way there first and the churn starts later
    // (`-sheepdogRenderChurnAfter`).

    func testShellMenuStaysOpenWhileTheSessionRerenders() throws {
        let app = launchDemo(["-sheepdogRenderChurnAfter", "30"])
        // The Agents tab learns whether the computer offers the shell.
        showAgentsPane(app)
        let header = app.descendants(matching: .any).matching(identifier: "shell.header").firstMatch
        chooseMenuItem(app, menu: app.buttons.matching(identifier: "toolbar.new"), item: app.buttons["new.shell"], until: header)
        waitForChurn(app)
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "shell.menu"), item: app.buttons["shell.menu.copy"], probe: "shell")
    }

    func testSpacesNewMenuStaysOpenWhileTheSessionRerenders() throws {
        let app = launchDemo(["-sheepdogRenderChurnAfter", "30"])
        let tree = app.buttons["spaces.row.shop-fix-login"]
        chooseMenuItem(app, menu: app.buttons.matching(identifier: "toolbar.more"), item: app.buttons["toolbar.spaces"], until: tree)
        waitForChurn(app)
        assertMenuStaysOpen(app, menu: app.buttons.matching(identifier: "spaces.new"), item: app.buttons["website"], probe: "spaces.new")
    }

    func testWorktreeMenuStaysOpenWhileTheSessionRerenders() throws {
        let app = launchDemo(["-sheepdogRenderChurnAfter", "30"])
        let tree = app.buttons["spaces.row.shop-fix-login"]
        chooseMenuItem(app, menu: app.buttons.matching(identifier: "toolbar.more"), item: app.buttons["toolbar.spaces"], until: tree)
        tapWhenTappable(tree, "the worktree space")
        let menu = app.buttons.matching(identifier: "space.worktree.shop-fix-login")
        XCTAssertTrue(menu.firstMatch.waitForExistence(timeout: 10))
        waitForChurn(app)
        assertMenuStaysOpen(app, menu: menu, item: app.buttons["space.worktree.remove"], probe: "spaces.worktree")
    }

    /// Terminal frames reach the surface and nothing else: the session and
    /// the window's observable state stay quiet, so the header, the ⋯ menu
    /// and the composer do not re-render per frame.
    func testTerminalFramesReRenderOnlyTheSurface() throws {
        let app = launchDemo(["-sheepdogFrameChurn"])
        openWindow(app, "claude-auth")
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        // Past the first frames (the window going live, the first text).
        sleep(2)
        let before = RenderCounts(app)
        sleep(4)
        let after = RenderCounts(app)
        let frames = after["frames"] - before["frames"]
        let window = after["window"] - before["window"]
        let model = after["model"] - before["model"]
        let header = after["header"] - before["header"]
        let menu = after["menu.agent"] - before["menu.agent"]
        print("render counts over 4 s: frames \(frames), window \(window), model \(model), header \(header), menu \(menu); before: \(before.text); after: \(after.text)")
        XCTAssertGreaterThanOrEqual(frames, 20, "the demo streams frames (\(after.text))")
        XCTAssertLessThanOrEqual(window, 2, "the window's observable state stays quiet per frame (\(frames) frames)")
        XCTAssertLessThanOrEqual(model, 2, "the session stays quiet per frame (\(frames) frames)")
        XCTAssertLessThanOrEqual(header, 1, "the header does not re-render per frame")
        XCTAssertLessThanOrEqual(menu, 1, "the ⋯ menu does not re-render per frame")
        // VoiceOver's copy of the screen still follows, at most twice a second.
        expectation(for: NSPredicate(format: "value CONTAINS 'scripted session'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 10)
    }

    // MARK: - Helpers

    /// Until the delayed churn runs: the model announces changes.
    private func waitForChurn(_ app: XCUIApplication) {
        let start = RenderCounts(app)["model"]
        let deadline = Date().addingTimeInterval(60)
        while RenderCounts(app)["model"] - start < 10, Date() < deadline {}
        XCTAssertGreaterThanOrEqual(RenderCounts(app)["model"] - start, 10, "the churn started")
    }

    /// Opens `menu` and taps `item` once it has settled on screen. Under
    /// the churn a second tap on the menu button (as `tapMenuItem` retries)
    /// closes the menu again, so the button is tapped only while the item
    /// is not there.
    private func chooseMenuItem(_ app: XCUIApplication, menu: XCUIElementQuery, item: XCUIElement, until: XCUIElement) {
        let deadline = Date().addingTimeInterval(40)
        while !until.exists, Date() < deadline {
            if !item.exists {
                for i in 0 ..< menu.count where menu.element(boundBy: i).isTappable {
                    menu.element(boundBy: i).tap()
                    break
                }
                _ = item.waitForExistence(timeout: 4)
            }
            sleep(1)
            if let frame = try? item.snapshot().frame, frame.minX.isFinite, frame.minY.isFinite, !frame.isEmpty {
                app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
            }
            _ = until.waitForExistence(timeout: 4)
        }
        XCTAssertTrue(until.exists, "\(item) opened \(until)")
    }

    private func openWindow(_ app: XCUIApplication, _ alias: String) {
        showAgentsPane(app)
        let open = app.buttons.matching(identifier: "flock.agent.\(alias).window")
        XCTAssertTrue(open.firstMatch.waitForExistence(timeout: 10))
        let deadline = Date().addingTimeInterval(10)
        repeat {
            for i in 0 ..< open.count where open.element(boundBy: i).isTappable {
                open.element(boundBy: i).tap()
                XCTAssertTrue(app.buttons["agentScreen.menu"].waitForExistence(timeout: 10), "\(alias)'s window")
                return
            }
            usleep(200_000)
        } while Date() < deadline
        XCTFail("no visible window button for \(alias)")
    }
}

/// The app's render counts (`RenderProbe`, `-sheepdogCountRenders`).
struct RenderCounts {
    let text: String
    private let counts: [String: Int]

    init(_ app: XCUIApplication) {
        // The readout refreshes twice a second.
        usleep(600_000)
        let element = app.descendants(matching: .any)["debug.renders"].firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: 5), "the render counts (-sheepdogCountRenders)")
        text = element.label
        var counts: [String: Int] = [:]
        for pair in text.split(separator: " ") {
            let kv = pair.split(separator: "=")
            if kv.count == 2, let n = Int(kv[1]) { counts[String(kv[0])] = n }
        }
        self.counts = counts
    }

    subscript(_ name: String) -> Int { counts[name] ?? 0 }
}

extension XCTestCase {
    /// Opens `menu`, then for two seconds of churn checks that `item` stays
    /// on screen and the menu (`StableMenu` probe `probe`) was not drawn
    /// again: an open menu rebuilt under the user's finger flickers or
    /// closes.
    func assertMenuStaysOpen(_ app: XCUIApplication, menu: XCUIElementQuery, item: XCUIElement, probe: String,
                             file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(menu.firstMatch.waitForExistence(timeout: 10), "\(probe): the menu button", file: file, line: line)
        var opened = false
        let deadline = Date().addingTimeInterval(15)
        repeat {
            // The one on screen (the demo keeps the real screen's copy).
            for i in 0 ..< menu.count where menu.element(boundBy: i).isTappable {
                menu.element(boundBy: i).tap()
                break
            }
            opened = item.waitForExistence(timeout: 3)
        } while !opened && Date() < deadline
        if !opened { print("menu \(probe) did not open:\n\(app.debugDescription)") }
        XCTAssertTrue(opened, "\(probe): the menu opens under churn", file: file, line: line)
        let label = item.label
        // Two windows of two seconds: the first may still see the screen
        // settling after a launch (a freshly booted simulator); a menu
        // redrawn by the churn is redrawn in both.
        var drawn = 0
        var counts = ""
        for _ in 0 ..< 2 {
            let before = RenderCounts(app)
            for i in 0 ..< 10 {
                usleep(200_000)
                XCTAssertTrue(item.exists, "\(probe): the menu is still open after \(i * 200 + 200) ms", file: file, line: line)
                XCTAssertEqual(item.label, label, "\(probe): the item did not change", file: file, line: line)
            }
            let after = RenderCounts(app)
            drawn = after["menu.\(probe)"] - before["menu.\(probe)"]
            counts = after.text
            if drawn <= 1 { break }
        }
        XCTAssertLessThanOrEqual(drawn, 1, "\(probe): the open menu was drawn \(drawn) more times (\(counts))", file: file, line: line)
        closeMenu(app, item: item.identifier.isEmpty ? nil : item.identifier)
    }
}
