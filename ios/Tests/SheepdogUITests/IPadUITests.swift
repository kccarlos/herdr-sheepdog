import XCTest

/// The iPad layout, in the offline demo (no daemon). Skipped on iPhone;
/// ios.yml runs these on an iPad simulator.
///
/// `TEST_RUNNER_SHEEPDOG_IPAD_SHOTS=<dir>` also saves screenshots there as
/// `ipad-<screen>-<suffix>.png` (`TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX`, light
/// by default). The appearance and text size are the simulator's.
final class IPadUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var windowedCleanup: (() -> Void)?

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDown() {
        windowedCleanup?()
        windowedCleanup = nil
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func launchDemo(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent", "-sheepdogOpenDemo"] + extra
        app.launch()
        var header = app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15)
        if !header {
            // A cold or overloaded simulator can leave the first process
            // unresponsive (its UI query times out): start it afresh once.
            shot("launch-failed")
            app.terminate()
            app.launch()
            header = app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 30)
        }
        XCTAssertTrue(header)
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    private func shot(_ name: String) {
        guard let directory = environment["SHEEPDOG_IPAD_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        // Drawn upright: in landscape the raw capture is sideways.
        let image = XCUIScreen.main.screenshot().image
        let upright = UIGraphicsImageRenderer(size: image.size, format: {
            let format = UIGraphicsImageRendererFormat()
            format.scale = image.scale
            return format
        }()).pngData { _ in image.draw(in: CGRect(origin: .zero, size: image.size)) }
        try? upright.write(to: URL(fileURLWithPath: directory).appendingPathComponent("ipad-\(name)-\(suffix).png"))
    }

    /// Landscape: the sidebar with Conversation, All agents and the agents
    /// by space; the Conversation in the detail with the capsule floating at
    /// its foot, clear of the sidebar. A sidebar row opens the agent's
    /// window in the detail.
    func testSidebarAndDetail() throws {
        let app = launchDemo()
        XCTAssertTrue(element(app, "sidebar.conversation").waitForExistence(timeout: 10))
        let capsule = element(app, "voice.capsule")
        XCTAssertTrue(capsule.waitForExistence(timeout: 10), "the capsule floats over the detail")
        XCTAssertGreaterThanOrEqual(capsule.frame.minX, element(app, "sidebar.conversation").frame.maxX, "never over the sidebar")
        // The element is the capsule's content inside its 7 pt side padding.
        XCTAssertEqual(capsule.frame.width, 366 - 14, accuracy: 2, "366 pt wide")
        XCTAssertFalse(element(app, "type.field").exists, "the composer is the capsule's keyboard button, not a field of its own")
        let agent = element(app, "sidebar.agent.claude-auth")
        XCTAssertTrue(agent.waitForExistence(timeout: 10))
        sleep(2)
        shot("conversation")

        agent.tap()
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10), "the window opens in the detail")
        XCTAssertTrue(element(app, "agentScreen.header").exists)
        sleep(2)
        shot("agent")

        element(app, "sidebar.allAgents").tap()
        XCTAssertTrue(element(app, "flock.agent.claude-auth").waitForExistence(timeout: 10))
        sleep(1)
        shot("all-agents")
    }

    /// All agents: two columns of cards, the filters, and Expand all,
    /// which opens every card that has no question.
    func testAllAgentsFiltersAndExpandAll() throws {
        let app = launchDemo(["-sheepdogDemoMarkdown"])
        let all = element(app, "sidebar.allAgents")
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.tap()
        XCTAssertTrue(element(app, "allAgents.grid").waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "flock.agent.claude-auth").exists)
        element(app, "allAgents.expandAll").tap()
        XCTAssertTrue(app.buttons["Collapse all"].waitForExistence(timeout: 5), "every card is open")
        sleep(1)
        shot("all-agents-expanded")
        element(app, "allAgents.filter.working").tap()
        XCTAssertFalse(element(app, "flock.agent.docs").waitForExistence(timeout: 2), "a finished agent is not working")
        element(app, "allAgents.filter.waiting").tap()
        // The demo's codex-payments asks a question 16 s in (DemoWorld) and
        // waits until it is answered. Before that the filter may show the
        // empty state for a moment; how long depends on the Mac's speed, so
        // wait for the state that lasts: only the waiting agent.
        XCTAssertTrue(element(app, "flock.agent.codex-payments").waitForExistence(timeout: 20), "the waiting agent")
        XCTAssertFalse(element(app, "flock.agent.docs").exists, "a finished agent is not waiting")
        sleep(1)
        shot("all-agents-waiting")
        element(app, "allAgents.filter.all").tap()
        XCTAssertTrue(element(app, "flock.agent.claude-auth").waitForExistence(timeout: 5))
    }

    /// A filter that matches no agent shows its empty state, and its Show
    /// all brings every agent back. The demo is paused, so no agent asks
    /// a question and Waiting stays empty however slow the Mac is.
    func testAllAgentsEmptyFilterShowsAll() throws {
        let app = launchDemo(["-sheepdogDemoPaused"])
        let all = element(app, "sidebar.allAgents")
        XCTAssertTrue(all.waitForExistence(timeout: 10))
        all.tap()
        XCTAssertTrue(element(app, "flock.agent.codex-payments").waitForExistence(timeout: 10))
        element(app, "allAgents.filter.waiting").tap()
        XCTAssertTrue(element(app, "allAgents.empty").waitForExistence(timeout: 5), "no agent is waiting")
        XCTAssertFalse(element(app, "flock.agent.codex-payments").exists)
        sleep(1)
        shot("all-agents-waiting-empty")
        let showAll = element(app, "allAgents.showAll")
        XCTAssertTrue(showAll.exists)
        showAll.tap()
        XCTAssertTrue(element(app, "flock.agent.codex-payments").waitForExistence(timeout: 5), "Show all lists every agent again")
        XCTAssertTrue(element(app, "flock.agent.claude-auth").exists)
        XCTAssertFalse(element(app, "allAgents.empty").exists)
        XCTAssertTrue(element(app, "allAgents.filter.all").isSelected, "the All filter is on")
    }

    /// Portrait: the sidebar hides, the detail's header carries Show
    /// sidebar and the capsule floats at the window's centre; it moves to
    /// the content's centre when the sidebar slides over.
    func testPortraitHidesTheSidebar() throws {
        XCUIDevice.shared.orientation = .portrait
        let app = launchDemo()
        let capsule = element(app, "voice.capsule")
        XCTAssertTrue(capsule.waitForExistence(timeout: 10), "the capsule floats over the detail")
        XCTAssertTrue(element(app, "sidebar.show").exists)
        XCTAssertEqual(capsule.frame.midX, app.windows.firstMatch.frame.midX, accuracy: 2, "centred with the sidebar hidden")
        sleep(2)
        shot("portrait-conversation")
        element(app, "sidebar.show").tap()
        let agent = element(app, "sidebar.agent.claude-migration")
        XCTAssertTrue(agent.waitForExistence(timeout: 5))
        sleep(1)
        let sidebarEdge = element(app, "sidebar.conversation").frame.maxX
        XCTAssertGreaterThanOrEqual(capsule.frame.minX, sidebarEdge, "the capsule moved clear of the sidebar")
        shot("portrait-sidebar")
        agent.tap()
        XCTAssertTrue(element(app, "agentScreen.terminal").waitForExistence(timeout: 10))
        shot("portrait-agent")
        // Nothing floats over the terminal's key bar: its keys take the touch.
        let keyboardKey = app.buttons.matching(identifier: "agentScreen.key.typing").firstMatch
        XCTAssertTrue(keyboardKey.waitForExistence(timeout: 10), "the key bar")
        XCTAssertTrue(keyboardKey.isHittable, "a terminal's key bar is never covered")
        sleep(2)
    }

    /// Two agents side by side: the split menu puts one beside the other,
    /// each pane has its own keys and composer and ✕, and a sidebar row
    /// dragged onto the detail replaces the pane that is not focused.
    func testTwoPanesSideBySide() throws {
        let app = launchDemo()
        let auth = element(app, "sidebar.agent.claude-auth")
        tapWhenTappable(auth, "claude-auth in the sidebar")
        tapWhenTappable(element(app, "pane.split"), "Split")
        tapWhenTappable(app.collectionViews.buttons["claude-migration"].firstMatch, "claude-migration in the picker")
        let terminals = app.descendants(matching: .any).matching(identifier: "agentScreen.terminal")
        let two = NSPredicate(format: "count == 2")
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 10)
        XCTAssertTrue(element(app, "panes.divider").exists)
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "pane.close").count, 2)
        sleep(2)
        shot("split")

        // Drag codex-docs onto the detail: it replaces the pane not focused
        // (claude-auth; the new one is focused).
        // Drop it on the pane it replaces, slowly, and hold there before
        // lifting: a quick drag that lifts at once can leave the drop
        // session hanging on a slow simulator (the drop target stays up and
        // nothing lands).
        let docs = element(app, "sidebar.agent.codex-docs")
        let target = terminals.element(boundBy: 0)
        docs.press(forDuration: 1.2, thenDragTo: target, withVelocity: .slow, thenHoldForDuration: 1.0)
        let docsHeader = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'agentScreen.header' AND label BEGINSWITH 'codex-docs'")).firstMatch
        XCTAssertTrue(docsHeader.waitForExistence(timeout: 20), "the dropped agent is on screen")
        XCTAssertTrue(element(app, "panes.dropTarget").waitForNonExistence(timeout: 10), "the drop target is gone after the drop")
        // A working agent's terminal keeps the tree busy, so a snapshot can
        // take seconds on a CI runner: give the counts room.
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 30)

        app.descendants(matching: .any).matching(identifier: "pane.close").firstMatch.tap()
        let one = NSPredicate(format: "count == 1")
        expectation(for: one, evaluatedWith: terminals)
        waitForExpectations(timeout: 30)

        XCUIDevice.shared.orientation = .portrait
        // Right after the rotation a tap on Split can be lost while the
        // layout settles: tap again until the picker is up.
        let splitButton = element(app, "pane.split")
        let payments = app.collectionViews.buttons["codex-payments"].firstMatch
        for _ in 0 ..< 3 where !payments.exists {
            tapWhenTappable(splitButton, "Split", timeout: 15)
            _ = payments.waitForExistence(timeout: 5)
        }
        tapWhenTappable(payments, "codex-payments in the picker", timeout: 15)
        expectation(for: two, evaluatedWith: terminals)
        waitForExpectations(timeout: 30)
        sleep(2)
        shot("split-portrait")
    }

    /// ⋯ ▸ Open in New Window gives the agent a window of its own. The demo
    /// is paused: this test is about the windows, and a second scene whose terminal streams beside the first keeps the main
    /// thread busy enough on a CI Mac for an accessibility snapshot to time
    /// out ("Failed to get matching snapshots"), twice in a row on one run.
    func testOpenInNewWindow() throws {
        let app = launchDemo(["-sheepdogDemoPaused"])
        let auth = element(app, "sidebar.agent.claude-auth")
        tapWhenTappable(auth, "claude-auth in the sidebar")
        XCTAssertTrue(element(app, "agentScreen.menu").waitForExistence(timeout: 10))
        // Each window has its own ⋯ menu: two once the new window is open.
        let menus = app.buttons.matching(identifier: "agentScreen.menu")
        tapMenuItem(app, menu: "agentScreen.menu", item: "agentScreen.menu.newWindow")
        let deadline = Date().addingTimeInterval(15)
        while menus.count < 2, Date() < deadline { usleep(300_000) }
        XCTAssertEqual(menus.count, 2, "the agent has a window of its own")
        sleep(2)
        shot("new-window")
        // Close it, so the next launch does not restore it.
        tapWhenTappable(element(app, "agentScreen.menu"), "the new window's menu")
        tapWhenTappable(element(app, "agentWindow.closeWindow"), "Close Window")
        XCTAssertTrue(element(app, "sidebar.conversation").waitForExistence(timeout: 10), "back to the main window")
    }

    /// A hardware keyboard: ⌘1…9 jump to the agents in sidebar order (also
    /// from inside a terminal), ⌘⇧M mutes, ⌘N opens New agent.
    func testKeyboardShortcuts() throws {
        let app = launchDemo()
        XCTAssertTrue(element(app, "sidebar.agent.claude-migration").waitForExistence(timeout: 10))
        app.typeKey("2", modifierFlags: .command)
        let header = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'agentScreen.header' AND label BEGINSWITH 'claude-migration'")).firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), "⌘2 opens the second agent")
        element(app, "agentScreen.terminal").tap()
        app.typeKey("1", modifierFlags: .command)
        let first = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'agentScreen.header' AND label BEGINSWITH 'claude-auth'")).firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 10), "⌘1 works from the terminal too")

        // The capsule floats over the Conversation, not over a terminal.
        tapWhenTappable(element(app, "sidebar.conversation"), "Conversation in the sidebar")
        // Any status line that says so: the real main view stays under the
        // demo's cover with its own (unmuted) one, and `firstMatch` was
        // sometimes that one. A key press sent while the window was still
        // switching agents was lost now and then, so press again until it
        // reads muted (a toggle: a late first press is undone by the second
        // and redone by the third).
        let muted = NSPredicate(format: "label CONTAINS[c] 'muted' OR label CONTAINS[c] 'mic off'")
        let mutedStatus = app.staticTexts.matching(identifier: "controls.statusLine").matching(muted).firstMatch
        for _ in 0 ..< 3 where !mutedStatus.exists {
            app.typeKey("m", modifierFlags: [.command, .shift])
            _ = mutedStatus.waitForExistence(timeout: 5)
        }
        XCTAssertTrue(mutedStatus.exists, "⌘⇧M mutes")
        app.typeKey("m", modifierFlags: [.command, .shift])

        app.typeKey("n", modifierFlags: .command)
        XCTAssertTrue(app.navigationBars["New agent"].waitForExistence(timeout: 10), "⌘N opens New agent as a form sheet")
        sleep(2)
        shot("new-agent-sheet")
    }

    /// Several computers: the sidebar's computer level (in the store's
    /// order, with voice markers), and All agents' sections. With one
    /// computer there is no computer row (`testSidebarAndDetail`).
    func testSidebarComputerLevel() throws {
        let home = StubDaemon.blocked(alias: "writer", space: "blog", pane: "h1:p1", question: "Publish the post now?")
        let workStub = try StubDaemon(.computer(name: "Work Mac", agents: #"[{"pane_id":"w1:p1","alias":"api","kind":"claude","state":"working","workspace_label":"webapp"}]"#, questions: "[]"))
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[\(home.agent)]", questions: "[\(home.question)]"))
        let workHost = try workStub.start()
        let homeHost = try homeStub.start()
        defer { workStub.stop(); homeStub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(workHost);Home Mac=\(homeHost)"]
        app.launch()
        XCTAssertTrue(element(app, "computerLabel.sidebar.fixture-1").waitForExistence(timeout: 15), "the current computer's row")
        XCTAssertEqual(element(app, "computer.sidebarVoiceMarker.fixture-1").label, "Voice is on Work Mac")
        let homeMarker = element(app, "computer.sidebarVoiceMarker.fixture-2")
        XCTAssertTrue(homeMarker.waitForExistence(timeout: 15), "Move the voice to Home Mac")
        XCTAssertEqual(homeMarker.label, "Move the voice to Home Mac")
        XCTAssertTrue(element(app, "computerLabel.sidebarAgent.writer").waitForExistence(timeout: 15), "Home Mac's waiting agent")
        element(app, "sidebar.allAgents").tap()
        XCTAssertTrue(element(app, "computerLabel.section.fixture-1").waitForExistence(timeout: 10), "All agents: Work Mac's section")
        XCTAssertTrue(element(app, "computerLabel.section.fixture-2").waitForExistence(timeout: 10), "All agents: Home Mac's section")
        XCTAssertLessThan(element(app, "computerLabel.section.fixture-1").frame.minY, element(app, "computerLabel.section.fixture-2").frame.minY, "All agents keeps pairing order")
        XCTAssertTrue(element(app, "computer.voiceMarker.fixture-2").exists, "All agents: Home Mac's marker")
        sleep(1)
        shot("several-computers")
        tapWhenTappable(element(app, "computer.sidebarVoiceMarker.fixture-2"), "Home Mac's voice marker")
        XCTAssertTrue(element(app, "computer.sidebarVoiceMarker.fixture-1").waitForExistence(timeout: 10))
        XCTAssertEqual(element(app, "computer.sidebarVoiceMarker.fixture-1").label, "Move the voice to Work Mac")
        XCTAssertLessThan(element(app, "computerLabel.sidebar.fixture-1").frame.minY, element(app, "computerLabel.sidebar.fixture-2").frame.minY, "Work Mac stays above Home Mac after moving the voice")
    }

    /// iPadOS 26/27 Windowed Apps / Stage Manager: the system window-controls
    /// pill must never overlap the sidebar title, the detail header's Show
    /// sidebar / All agents / agent-pane controls, or the compact top bar's
    /// Conversation / Agents switcher.
    func testWindowedControlsClearHeaders() throws {
        let agents = #"[{"pane_id":"p-1","alias":"ordinary-qa","kind":"claude","state":"working","workspace_label":"webapp"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: agents, questions: "[]", factory: "[]"))
        let host = try stub.start()

        let app = XCUIApplication()
        let sb = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let screenW = max(sb.frame.width, sb.frame.height)
        let screenH = min(sb.frame.width, sb.frame.height)

        func dragCorner(to target: CGPoint) {
            let win = app.windows.firstMatch
            guard win.exists else { return }
            let frame = win.frame
            guard frame.minX.isFinite, frame.minY.isFinite, !frame.isEmpty else { return }
            let start = sb.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.maxX - 6, dy: frame.maxY - 6))
            let end = sb.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: target.x, dy: target.y))
            start.press(forDuration: 0.15, thenDragTo: end, withVelocity: .default, thenHoldForDuration: 0.1)
            sleep(2)
        }

        func restoreFullScreen() {
            for _ in 0 ..< 4 {
                let win = app.windows.firstMatch
                guard app.state == .runningForeground, win.exists else { break }
                let frame = win.frame
                if frame.width >= screenW - 20 && frame.height >= screenH - 20 { break }
                dragCorner(to: CGPoint(x: screenW - 4, y: screenH - 4))
            }
        }

        // Registered for tearDown() (not defer) because continueAfterFailure = false
        // unwinds via Objective-C exception on assertion failure, bypassing defer.
        windowedCleanup = {
            restoreFullScreen()
            stub.stop()
        }

        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 15), "app window appeared")
        restoreFullScreen()
        XCTAssertTrue(element(app, "sidebar.agent.ordinary-qa").waitForExistence(timeout: 15))

        let wc = sb.buttons["window-controls:com.github.kccarlos.herdr-sheepdog-ios"].firstMatch

        func assertClearsWindowControls(_ target: XCUIElement, _ name: String, file: StaticString = #filePath, line: UInt = #line) {
            XCTAssertTrue(wc.waitForExistence(timeout: 5), "window controls must exist when checking \(name)", file: file, line: line)
            XCTAssertTrue(target.waitForExistence(timeout: 5), "\(name) must exist", file: file, line: line)
            let winFrame = app.windows.firstMatch.frame
            let wcFrame = wc.frame
            let targetFrame = target.frame
            XCTAssertTrue(winFrame.minX.isFinite && winFrame.minY.isFinite && !winFrame.isEmpty, "app window frame \(winFrame) must be non-empty", file: file, line: line)
            XCTAssertTrue(wcFrame.minX.isFinite && wcFrame.minY.isFinite && !wcFrame.isEmpty, "window controls frame \(wcFrame) must be non-empty", file: file, line: line)
            XCTAssertTrue(targetFrame.minX.isFinite && targetFrame.minY.isFinite && !targetFrame.isEmpty, "\(name) frame \(targetFrame) must be non-empty", file: file, line: line)
            XCTAssertTrue(winFrame.intersects(wcFrame), "window controls \(wcFrame) must lie inside app window \(winFrame) when checking \(name)", file: file, line: line)
            XCTAssertFalse(wcFrame.intersects(targetFrame), "window controls \(wcFrame) must not overlap \(name) \(targetFrame)", file: file, line: line)
        }

        shot("windowed-full")

        // Regular windowed, landscape-aspect on both 11-inch (820 pt tall) and
        // 13-inch (1032 pt tall) iPads so GeometryProxy.isPortrait stays false.
        let regularTarget = CGPoint(x: screenW - (screenW - 898) / 2, y: min(screenH - 35, 780))
        for _ in 0 ..< 3 {
            let frame = app.windows.firstMatch.frame
            if frame.width < screenW - 20 && wc.waitForExistence(timeout: 3) { break }
            dragCorner(to: regularTarget)
        }
        let regularWin = app.windows.firstMatch.frame
        XCTAssertLessThan(regularWin.width, screenW - 20, "corner drag must shrink window from full screen (\(regularWin))")
        XCTAssertGreaterThan(regularWin.width, regularWin.height, "regular windowed frame must stay landscape (\(regularWin))")
        XCTAssertTrue(wc.waitForExistence(timeout: 5), "window controls must appear after resizing window to \(regularWin)")

        let title = app.staticTexts["Sheepdog"].firstMatch
        assertClearsWindowControls(title, "sidebar title")
        XCTAssertGreaterThanOrEqual(title.frame.width, 95, "sidebar title must not truncate in windowed mode")
        shot("windowed-898")

        // Regular windowed with sidebar hidden (Conversation).
        tapWhenTappable(element(app, "sidebar.hide"), "Hide sidebar")
        let showSidebar = element(app, "sidebar.show")
        XCTAssertTrue(showSidebar.waitForExistence(timeout: 5), "Show sidebar appeared on Conversation")
        assertClearsWindowControls(showSidebar, "Show sidebar on Conversation")
        shot("windowed-898-hidden")

        // Regular windowed with sidebar hidden (All agents).
        tapWhenTappable(showSidebar, "Show sidebar")
        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents")
        tapWhenTappable(element(app, "sidebar.hide"), "Hide sidebar on All agents")
        XCTAssertTrue(showSidebar.waitForExistence(timeout: 5), "Show sidebar appeared on All agents")
        let allTitle = element(app, "allAgents.title")
        assertClearsWindowControls(showSidebar, "Show sidebar on All agents")
        assertClearsWindowControls(allTitle, "All agents header")
        shot("windowed-898-allagents-hidden")

        // Regular windowed with sidebar hidden (agent pane).
        tapWhenTappable(showSidebar, "Show sidebar for agent pane")
        tapWhenTappable(element(app, "sidebar.agent.ordinary-qa"), "ordinary-qa in sidebar")
        let agentHeader = element(app, "agentScreen.header")
        XCTAssertTrue(agentHeader.waitForExistence(timeout: 5), "agent pane header appeared")
        tapWhenTappable(element(app, "sidebar.hide"), "Hide sidebar on agent pane")
        XCTAssertTrue(showSidebar.waitForExistence(timeout: 5), "Show sidebar appeared on agent pane")
        assertClearsWindowControls(showSidebar, "Show sidebar on agent pane")
        assertClearsWindowControls(agentHeader, "agent pane header")

        // Return to Conversation and resize to compact windowed (~375 pt wide).
        tapWhenTappable(showSidebar, "Show sidebar before compact resize")
        tapWhenTappable(element(app, "sidebar.conversation"), "Conversation")
        let convSegment = element(app, "main.pane.conversation")
        let compactTarget = CGPoint(x: screenW - (screenW - 375) / 2, y: screenH - 20)
        for _ in 0 ..< 3 {
            if convSegment.exists && app.windows.firstMatch.frame.width < 500 { break }
            dragCorner(to: compactTarget)
        }
        let compactWin = app.windows.firstMatch.frame
        XCTAssertLessThan(compactWin.width, 500, "corner drag must shrink window to compact width (\(compactWin))")
        XCTAssertTrue(convSegment.waitForExistence(timeout: 10), "narrow window switches to compact TopBar")
        assertClearsWindowControls(convSegment, "Conversation control")
        let agentsSegment = element(app, "main.pane.flock")
        assertClearsWindowControls(agentsSegment, "Agents control")
        shot("windowed-375")
    }
}
