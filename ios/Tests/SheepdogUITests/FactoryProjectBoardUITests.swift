import XCTest

/// Failing UI tests for app-factory PR 9 against `StubDaemon` (no daemon
/// checkout): board open, `/say` text box, Stop confirm & Resume, coordinator
/// window controls & restrictions + off states, empty/error states & VoiceOver.
final class FactoryProjectBoardUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    private func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    private static let runningNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":2,"owner_answer":1,"done":1},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
    private static let stoppedNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"stopped","counts":{"owner_answer":1,"done":1},"folder":"~/code/notes"}]"#
    private static let sampleGraph = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T10","title":"feat: import","stage":"owner_answer","why_waiting":"Delete the old notes table?","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now"}]},{"ref":"T8","title":"feat: tags","stage":"working","why_waiting":"Worker is working","after":[],"dependents":[],"layer":0,"rounds":1,"run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z","last_actor_decision":{"at":"2026-11-25T11:03:00Z","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo"}]},{"ref":"T14","title":"feat: search filters","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]}]}"#
    private static let sampleAsk = #"{"id":"fask-del-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"Delete the old notes table?","summary":"Delete the old notes table?","factory":{"project":"proj-1","task":"T10","kind":"delete"},"options":[{"index":1,"label":"Keep it","default":true,"recommended":true},{"index":2,"label":"Delete it"}]}"#

    func testTapHeadOpensBoardSayBoxStopConfirmAndResume() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[\(Self.sampleAsk)]", factory: Self.runningNotes, pendingAction: Self.sampleAsk))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        stub.stopHandler = { [weak stub] _ in stub?.factoryState = Self.stoppedNotes; return (200, #"{"ok":true}"#) }
        stub.startHandler = { [weak stub] _ in stub?.factoryState = Self.runningNotes; return (200, #"{"ok":true}"#) }
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")

        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["A notes page with search"].exists)
        XCTAssertTrue(scrollToVisible(app, app.descendants(matching: .any)["factoryProject.task.T8"].firstMatch).exists)
        XCTAssertTrue(app.staticTexts["implementing, sent back from review round 1"].exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'the tag index must be case-insensitive'")).firstMatch.exists)
        XCTAssertTrue(scrollToVisible(app, app.buttons["factoryProject.doneFold"].firstMatch).exists)

        scrollToTop(app)
        let send = app.buttons["factoryProject.say.send"].firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 10))
        XCTAssertEqual(send.label, "Send to the notes factory")
        XCTAssertFalse(send.isEnabled, "empty text disables Send")
        typeWhenFocused(app.textFields["factoryProject.say.field"].firstMatch, "Skip the export task")
        tapWhenTappable(send, "Send instruction")
        let status = app.descendants(matching: .any)["factoryProject.say.status"].firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertEqual(status.label, "Told the notes factory.")
        XCTAssertTrue(stub.sayBodies().contains { $0.project == "proj-1" && $0.body.contains("Skip the export task") })

        let stopBtn = app.buttons["factoryProject.stop"].firstMatch
        XCTAssertEqual(stopBtn.label, "Stop this project")
        tapWhenTappable(stopBtn, "Stop")
        XCTAssertTrue(app.staticTexts["Stop this project?"].waitForExistence(timeout: 10))
        tapWhenTappable(app.buttons["Keep it running"].firstMatch, "Keep it running")
        XCTAssertEqual(stub.requests("POST /api/factory/projects/stop"), 0)

        tapWhenTappable(stopBtn, "Stop again")
        tapWhenTappable(app.buttons["Stop the project"].firstMatch, "Stop the project")
        let resumeBtn = app.buttons["factoryProject.resume"].firstMatch
        XCTAssertTrue(resumeBtn.waitForExistence(timeout: 10))
        XCTAssertEqual(resumeBtn.label, "Resume this project")
        XCTAssertFalse(app.buttons["factoryProject.coordinator"].exists)
        XCTAssertTrue(stub.stopBodies().contains { $0.contains("proj-1") })

        tapWhenTappable(resumeBtn, "Resume")
        XCTAssertTrue(app.buttons["factoryProject.coordinator"].waitForExistence(timeout: 10))
        XCTAssertTrue(stub.startBodies().contains { $0.contains("~/code/notes") && $0.contains("A notes page with search") })
    }

    func testCoordinatorWindowLiveControlsRestrictionsAndOffStates() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[\(Self.sampleAsk)]", factory: Self.runningNotes, pendingAction: Self.sampleAsk))
        stub.enableEvents = true
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        stub.screens["coord-notes-1"] = "● factory ask T10 \"Delete the old notes table?\"\n> Ready\n"
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        let coordBtn = app.buttons["factoryProject.coordinator"].firstMatch
        XCTAssertTrue(coordBtn.waitForExistence(timeout: 15))
        XCTAssertEqual(coordBtn.label, "Open the notes coordinator's window")
        tapWhenTappable(coordBtn, "Coordinator")

        let header = app.descendants(matching: .any)["agentScreen.header"].firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10))
        XCTAssertTrue(header.label.contains("notes") && header.label.lowercased().contains("coordinator"), header.label)
        let terminal = app.descendants(matching: .any)["agentScreen.terminal"].firstMatch
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        expectation(for: NSPredicate(format: "value CONTAINS 'factory ask T10'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 15)

        XCTAssertTrue(app.descendants(matching: .any)["coordinatorWindow.askStrip"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["agentScreen.keyBar"].exists)
        let field = app.textFields["agentScreen.field"].firstMatch
        XCTAssertTrue(field.exists && field.label == "Message to the notes coordinator")
        for id in ["agentScreen.stop", "agentScreen.attach", "agentScreen.menu", "agentScreen.menu.rename", "agentScreen.menu.stop", "agentScreen.menu.close", "agentScreen.menu.files"] {
            XCTAssertFalse(app.descendants(matching: .any)[id].exists, "\(id) must be absent in coordinator window")
        }
        tapWhenTappable(app.buttons["agentScreen.key.more"].firstMatch, "More keys")
        XCTAssertTrue(app.buttons["moreKeys.done"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["moreKeys.ctrlC"].exists, "no Interrupt row in coordinator More keys")
        tapWhenTappable(app.buttons["moreKeys.done"].firstMatch, "Done")

        stub.factoryState = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1},"folder":"~/code/notes","alert":{"kind":"agents_down","cause":"claude coordinator is signed out","fix":"sign in to claude at this Mac"}}]"#
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(app.staticTexts["The notes coordinator is not running"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["claude coordinator is signed out"].exists)
        XCTAssertTrue(app.staticTexts["Fix: sign in to claude at this Mac"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["agentScreen.keyBar"].exists)
        XCTAssertFalse(app.textFields["agentScreen.field"].exists)

        stub.factoryState = Self.stoppedNotes
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(app.staticTexts["notes is stopped"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Resume notes on its page; it goes on with the same goal."].exists)
        XCTAssertTrue(app.buttons["Back to notes"].exists)
    }

    func testBoardEmptyTasksAndLoadErrorStates() throws {
        let heads = #"[{"id":"proj-empty","name":"scratch","goal":"Plan the app","state":"running","counts":{},"folder":"~/code/scratch"},{"id":"proj-err","name":"broken","goal":"Fix builds","state":"running","counts":{"working":1},"folder":"~/code/broken"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-empty"] = #"{"protocol":1,"project":"proj-empty","folder":"~/code/scratch","tasks":[]}"#
        stub.factoryGraphErrors["proj-err"] = (503, #"{"error":"factory is not available"}"#)
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-empty"].firstMatch), "scratch head")
        XCTAssertTrue(app.staticTexts["no tasks yet"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["The coordinator has not planned this project yet."].exists)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-err"].firstMatch), "broken head")
        XCTAssertTrue(app.staticTexts["Can't load broken. Trying again."].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The rest of the app still works.'")).firstMatch.exists)

        stub.factoryGraphErrors["proj-err"] = nil
        stub.factoryGraphs["proj-err"] = #"{"protocol":1,"project":"proj-err","folder":"~/code/broken","tasks":[]}"#
        tapWhenTappable(app.buttons["factoryProject.retry"].firstMatch, "Try again")
        XCTAssertTrue(app.staticTexts["no tasks yet"].waitForExistence(timeout: 15))
    }
}
