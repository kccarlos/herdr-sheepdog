import XCTest

/// Screenshots of the redesigned screens (docs/dev/design/app-redesign.md),
/// in the offline demo: no daemon, no network. Runs only when asked:
///
///     xcodebuild … test -only-testing:SheepdogUITests/RedesignShotsUITests \
///       TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS=<directory> \
///       TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX=light
///
/// Each screen lands as `ui-<screen>-<suffix>.png`. The appearance and the
/// text size are the simulator's (`xcrun simctl ui <id> appearance dark`,
/// `xcrun simctl ui <id> content_size accessibility-extra-large`).
final class RedesignShotsUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    private var directory: String? { environment["SHEEPDOG_REDESIGN_SHOTS"] }
    private var suffix: String { environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light" }
    /// `ui` by default; `SHEEPDOG_SHOTS_PREFIX=ui2` keeps a later round apart.
    private var prefix: String { environment["SHEEPDOG_SHOTS_PREFIX"] ?? "ui" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard directory != nil else { throw XCTSkip("set SHEEPDOG_REDESIGN_SHOTS to take the redesign screenshots") }
    }

    func testSplash() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:9", "-sheepdogSplashHold"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["splash"].waitForExistence(timeout: 10))
        sleep(1)
        shot("splash")
    }

    func testCapsuleModes() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogCapsuleGallery"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["capsule.gallery"].waitForExistence(timeout: 10))
        sleep(2)
        shot("capsule-modes")
        app.swipeUp()
        sleep(1)
        shot("capsule-modes-2")
    }

    func testTopBarCapsuleAndMenus() throws {
        let app = launchDemo()
        sleep(3)
        shot("conversation")
        selectPane(app, "flock")
        sleep(2)
        shot("agents")
        tapOnScreen(app, "voice.menu")
        sleep(1)
        shot("voice-menu")
        dismissMenu(app)
        tapOnScreen(app, "toolbar.more")
        sleep(1)
        shot("more-menu")
        dismissMenu(app)
        tapOnScreen(app, "toolbar.new")
        sleep(1)
        shot("plus-menu")
        dismissMenu(app)
        selectPane(app, "conversation")
        tapOnScreen(app, "controls.type")
        let field = app.textFields["type.field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("and remind me when search is done")
        sleep(1)
        shot("composer")
    }

    func testAgentsPage() throws {
        let app = launchDemo()
        selectPane(app, "flock")
        // codex-payments asks after about 16 s; claude-auth is done at 8 s.
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40))
        sleep(2)
        shot("agents-waiting")
        let toggle = app.buttons["flock.agent.claude-auth.toggle"].firstMatch
        if toggle.waitForExistence(timeout: 5) { toggle.tap() }
        app.swipeUp()
        sleep(1)
        shot("agents-expanded")
        tapOnScreen(app, "flock.agent.claude-auth.window")
        XCTAssertTrue(app.buttons["agentScreen.key.more"].waitForExistence(timeout: 10))
        sleep(2)
        shot("agent-window")
        app.buttons["agentScreen.key.more"].tap()
        XCTAssertTrue(app.buttons["moreKeys.ctrlC"].waitForExistence(timeout: 5))
        sleep(1)
        shot("more-keys")
    }

    /// Two questions above the capsule: both cards when they fit, else the
    /// "2 questions" bar and its sheet.
    func testFloatingQuestions() throws {
        let app = launchDemo(["-sheepdogDemoSecondQuestion"])
        XCTAssertTrue(app.buttons["question.blog.option.1"].waitForExistence(timeout: 15), "the second agent's question floats")
        // codex-payments asks after about 16 s.
        let bar = app.buttons["questions.answer"]
        let deadline = Date().addingTimeInterval(40)
        while !bar.exists, !app.buttons["approval.option.1"].exists, Date() < deadline { usleep(200_000) }
        sleep(2)
        shot("floating-questions")
        if bar.exists { bar.tap() }
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 10))
        app.buttons["approval.option.1"].tap()
        sleep(3)
        shot("floating-answered")
    }

    /// The Conversation once the demo has news and a question: the agent
    /// strip, the rows and the floating question.
    func testConversationWithQuestion() throws {
        let app = launchDemo()
        // codex-payments asks after about 16 s.
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40))
        sleep(2)
        shot("conversation-question")
        app.swipeDown()
        sleep(1)
        shot("conversation-top")
    }

    /// Phase 2 of the conversation timeline: missed updates, a settled
    /// question, Details, dividers.
    func testTimelineRows() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogCapsuleGallery", "timeline", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["capsule.gallery"].waitForExistence(timeout: 10))
        sleep(2)
        shot("timeline")
        let group = app.descendants(matching: .any)["conversation.updates"].firstMatch
        if group.exists { group.tap() }
        sleep(1)
        shot("timeline-open")
    }

    /// A machine-ID computer name gets a suggestion when pairing ends.
    func testPairingNameSuggestion() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogPairingScreen", "pairedMachineID", "-sheepdogNoAnimations"]
        app.launch()
        XCTAssertTrue(app.textFields["pairing.name"].waitForExistence(timeout: 10), "the suggested name")
        XCTAssertEqual(app.textFields["pairing.name"].value as? String, "Work Mac")
        sleep(1)
        shot("pairing-name")
    }

    func testBanners() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogCapsuleGallery", "banners"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["capsule.gallery"].waitForExistence(timeout: 10))
        sleep(1)
        shot("banners")
    }

    /// A ScreenCard's sheet, then its row and a question asked by tap.
    func testCardSheetAndRows() throws {
        let app = launchDemo(["-sheepdogDemoCard"])
        let title = app.descendants(matching: .any)["card.title"]
        XCTAssertTrue(title.waitForExistence(timeout: 45))
        sleep(2)
        shot("card-sheet")
        app.buttons["card.done"].firstMatch.tap()
        sleep(1)
        tapOnScreen(app, "demo.question.0")
        sleep(4)
        shot("conversation-rows")
    }

    func testEmptyConversation() throws {
        // A saved address with nothing behind it: no history, no banner
        // until the orb is tapped.
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", "127.0.0.1:9"]
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["conversation.empty"].waitForExistence(timeout: 10))
        sleep(1)
        shot("conversation-empty")
    }

    func testPrimers() throws {
        for primer in ["microphone", "notifications"] {
            let app = XCUIApplication()
            app.launchArguments = ["-sheepdogShowPrimer", primer]
            app.launch()
            XCTAssertTrue(app.descendants(matching: .any)["primer.\(primer)"].waitForExistence(timeout: 10))
            sleep(1)
            shot("primer-\(primer)")
            app.terminate()
        }
    }

    func testSettings() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogAddress", Fixture.host]
        app.launch()
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.settings")
        XCTAssertTrue(app.buttons["settings.daemon"].waitForExistence(timeout: 10))
        sleep(2)
        shot("settings")
        app.buttons["settings.daemon"].tap()
        sleep(2)
        shot("settings-connection")
    }

    /// Factory screens for README and docs/iphone-app.md against `StubDaemon`
    /// (no live daemon, no network, demo names only): project board with tasks
    /// and the Tell box, coordinator window with the ask reminder strip,
    /// project settings with a read-back ask, and the new-project sheet with
    /// start checks.
    func testFactorySet() throws {
        let iso = ISO8601DateFormatter()
        let started12m = iso.string(from: Date().addingTimeInterval(-12 * 60))
        let started6m = iso.string(from: Date().addingTimeInterval(-6 * 60))
        let runningProjects = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":2,"done":4},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
         {"id":"proj-2","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"}]
        """#
        let boardGraph = #"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T7","title":"Search box and filters","stage":"working","why_waiting":"In review round 2","after":["T1","T4"],"dependents":[],"layer":2,"rounds":1,"run_role":"reviewer","run_cli":"claude","run_started_at":"\#(started12m)","steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"done"},{"name":"review","state":"now","text":"review round 2; needs 2 passing, at most 5"},{"name":"CI","state":"todo"},{"name":"merged","state":"todo"}]},
          {"ref":"T8","title":"Tag index and autocomplete","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":[],"layer":2,"rounds":1,"run_role":"worker","run_cli":"codex","run_started_at":"\#(started6m)","last_actor_decision":{"at":"\#(started6m)","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo"}]},
          {"ref":"T1","title":"Notes page layout","stage":"done","after":[],"dependents":["T4","T7"],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]},
          {"ref":"T4","title":"Search index migration","stage":"done","after":["T1"],"dependents":["T7"],"layer":1,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]}
        ]}
        """#
        let sampleAsk = #"{"id":"fask-del-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"Delete the old notes table?","summary":"Delete the old notes table?","factory":{"project":"proj-1","task":"T10","kind":"delete"},"options":[{"index":1,"label":"Keep it","default":true,"recommended":true},{"index":2,"label":"Delete it"}]}"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: runningProjects))
        stub.enableEvents = true
        stub.enableTerm = true
        stub.helloFeatures = ["agent-actions", "term"]
        stub.factoryGraphs["proj-1"] = boardGraph
        stub.screens["coord-notes-1"] = """
        ● notes coordinator · 3 active tasks
          T7  Search box and filters    review round 2 (claude)
          T8  Tag index autocomplete    implementing (codex)
          T10 Import legacy notes       waiting on your answer

        ● factory ask T10 "Delete the old notes table?"
          Options: Keep it (Recommended) · Delete it

        > Monitoring T7 and T8 while waiting on T10.
        """
        stub.factorySettings["proj-1"] = FactorySettingsUITests.settingsJSON()
        stub.proposeSettingHandler = { [weak stub] proj, _ in
            let q = "This project: max running 4 (now 3). Save?"
            let ask = #"{"id":"ask-rule-1","project":"\#(proj)","kind":"rule","gated":true,"question":"\#(q)","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"4","was":"3"},"asked_by":"engine","at":"2026-11-26T09:00:00Z","status":"open"}"#
            let card = #"{"id":"fask-ask-rule-1","pane_id":"factory:\#(proj)","kind":"answer_prompt","agent":"notes","question":"\#(q)","summary":"\#(q)","factory":{"project":"\#(proj)","kind":"rule"},"options":[{"index":1,"label":"Save","default":true,"recommended":true},{"index":2,"label":"Keep current"}]}"#
            stub?.factoryAsks = #"{"protocol":1,"asks":[\#(ask)]}"#
            stub?.questionsState = "[\(card)]"
            stub?.pendingActionState = card
            stub?.emitFactoryChanged(project: proj)
            return (200, #"{"ok":true,"ask":\#(ask)}"#)
        }
        stub.spacesJSON = #"""
        {"spaces":[
          {"id":"w1","name":"notes","path":"~/code/notes","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]},
          {"id":"w2","name":"bookmarks","path":"~/code/bookmarks","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]}
        ],"folders":[],"kinds":[{"kind":"claude","name":"Claude"}],"worktrees":true,"needs_access":[]}
        """#
        stub.checkHandler = { _ in
            (200, #"{"results":[{"name":"git version","status":"ok","detail":"git version 2.47.1"},{"name":"start claude","status":"ok","detail":"claude is signed in"},{"name":"start codex","status":"fail","detail":"codex is not signed in","fix":"run: codex login"},{"name":"start fetch","status":"warn","detail":"git failed to fetch origin"}]}"#)
        }
        let host = try stub.start(); defer { stub.stop() }

        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()

        // 1. Project board with tasks and the Tell box.
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.task.T7"].waitForExistence(timeout: 10))
        sleep(1)
        shot("factory-board")

        // 2. Coordinator window with the ask reminder strip.
        stub.questionsState = "[\(sampleAsk)]"
        stub.pendingActionState = sampleAsk
        stub.emitFactoryChanged(project: "proj-1")
        let coordBtn = app.buttons["factoryProject.coordinator"].firstMatch
        XCTAssertTrue(coordBtn.waitForExistence(timeout: 10))
        tapWhenTappable(coordBtn, "Coordinator")
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"].firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        expectation(for: NSPredicate(format: "value CONTAINS 'notes coordinator'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 15)
        XCTAssertFalse(app.descendants(matching: .any)["agentScreen.notice"].exists, "live coordinator window shows no snapshot banner")
        XCTAssertTrue(app.descendants(matching: .any)["coordinatorWindow.askStrip"].waitForExistence(timeout: 10))
        sleep(1)
        shot("factory-coordinator")

        // 3. Project settings with a read-back confirmation ask.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 10))
        stub.questionsState = "[]"
        stub.pendingActionState = nil
        stub.emitFactoryChanged(project: "proj-1")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")
        XCTAssertTrue(app.staticTexts["For this project only. A change is read back to you and saved when you confirm."].waitForExistence(timeout: 10))
        let plusMaxRun = scrollToVisible(app, app.buttons["factorySettings.increment.max_running"].firstMatch)
        tapWhenTappable(plusMaxRun, "+ max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        XCTAssertTrue(app.staticTexts["This project: max running 4 (now 3). Save?"].waitForExistence(timeout: 10))
        _ = scrollToVisible(app, app.buttons["factorySettings.allFold"].firstMatch)
        sleep(1)
        shot("factory-settings")

        // 4. New factory project sheet with start checks.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 10))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        showAgentsPane(app)
        let sheet = app.descendants(matching: .any)["factoryNewProject.sheet"].firstMatch
        tapMenuItem(app, menu: "toolbar.new", item: "new.factoryProject", until: sheet)
        tapWhenTappable(scrollToVisible(app, app.buttons["~/code/bookmarks, git repository"].firstMatch), "bookmarks folder")
        let goalField = scrollToVisible(app, app.descendants(matching: .any)["factoryNewProject.goal"].firstMatch)
        typeWhenFocused(goalField, "Tag and search saved links")
        // Dismiss software keyboard before running checks so the full start-checks card is visible.
        app.navigationBars["New project"].firstMatch.tap()
        tapWhenTappable(scrollToVisible(app, app.buttons["Check this folder"].firstMatch), "Check this folder")
        XCTAssertTrue(app.staticTexts["1 check failed. Fix it, then check again."].waitForExistence(timeout: 10))
        _ = scrollToVisible(app, app.buttons["Start the project and its agents"].firstMatch)
        sleep(1)
        shot("factory-new-project")
    }

    // MARK: - Helpers

    /// Conversation or Agents: the copy on screen (the demo keeps the
    /// real main view under its cover).
    func selectPane(_ app: XCUIApplication, _ pane: String) {
        let matches = app.buttons.matching(identifier: "main.pane.\(pane)")
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 5))
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            break
        }
        sleep(1)
    }

    /// Taps the copy of an element that is on screen.
    func tapOnScreen(_ app: XCUIApplication, _ id: String) {
        let matches = app.buttons.matching(identifier: id)
        XCTAssertTrue(matches.firstMatch.waitForExistence(timeout: 10), id)
        for i in 0 ..< matches.count where matches.element(boundBy: i).isHittable {
            matches.element(boundBy: i).tap()
            return
        }
        XCTFail("no visible \(id)")
    }

    /// Closes an open menu by tapping the top of the screen.
    func dismissMenu(_ app: XCUIApplication) {
        // A tap at the left edge, halfway down: outside every menu.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.03, dy: 0.45)).tap()
        sleep(2)
    }

    /// The demo, opened at launch, silent (no speech, no permission alerts).
    func launchDemo(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo"] + extra
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15), "the demo opened")
        return app
    }

    func shot(_ name: String) {
        guard let directory else { return }
        let screenshot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "\(prefix)-\(name)-\(suffix)"
        attachment.lifetime = .keepAlways
        add(attachment)
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(prefix)-\(name)-\(suffix).png"))
    }
}
