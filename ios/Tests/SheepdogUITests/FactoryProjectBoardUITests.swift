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
        // The coordinator button returning after Resume is pinned in PR 9b (coordinator window).
        XCTAssertTrue(stopBtn.waitForExistence(timeout: 10))
        XCTAssertTrue(stub.startBodies().contains { $0.contains("~/code/notes") && $0.contains("A notes page with search") })
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

    /// Fix-round: VoiceOver labels (hidden decorative icon, non-duplicated Say label,
    /// in-flight Resuming label), Stop refusal placed at Stop control rather than under
    /// the Say box, Say refusal replacing rather than stacking `defaultNote`, clearing
    /// stale `refusal`/`sent` on edits & state changes, keeping ask cards enabled during
    /// in-flight Say, preserving loaded board on transient reload error, and refreshing
    /// the deleted head away on `404` project-gone.
    func testStopRefusalPlacementComposerStateVoiceOverAndTransientReload() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[\(Self.sampleAsk)]", factory: Self.runningNotes, pendingAction: Self.sampleAsk))
        stub.enableEvents = true
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")

        let board = app.descendants(matching: .any)["factoryProject.board"].firstMatch
        XCTAssertTrue(board.waitForExistence(timeout: 15))
        let field = app.textFields["factoryProject.say.field"].firstMatch
        let send = app.buttons["factoryProject.say.send"].firstMatch
        let stopBtn = app.buttons["factoryProject.stop"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))

        // 1. VoiceOver: decorative header icon hidden; "Tell the notes factory" spoken once on the text field.
        XCTAssertEqual(field.label, "Tell the notes factory")
        XCTAssertFalse(board.staticTexts["Tell the notes factory"].exists, "visible label must be accessibilityHidden because the text field already carries accessibilityLabel")
        let headerImages = board.images.allElementsBoundByIndex.filter { ((try? $0.snapshot().frame.maxY) ?? .infinity) < field.frame.minY }
        XCTAssertTrue(headerImages.isEmpty, "decorative header icon must be hidden from VoiceOver")

        // 2. Stop refusal appears at the Stop control above the Say box, not inside the composer.
        stub.stopHandler = { _ in (403, #"{"code":"proof_required","error":"proof_required"}"#) }
        tapWhenTappable(stopBtn, "Stop (unpaired)")
        tapWhenTappable(app.buttons["Stop the project"].firstMatch, "Confirm Stop (unpaired)")
        let stopRefusal = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Not stopped: Work Mac no longer knows this phone.")).firstMatch
        XCTAssertTrue(stopRefusal.waitForExistence(timeout: 10))
        XCTAssertLessThanOrEqual(stopRefusal.frame.maxY, field.frame.minY, "Stop refusal must render at the Stop control, not under the Say box")

        // 3. In-flight Resume on the board announces "Resuming…" to VoiceOver (and successful Stop clears stale Stop refusal).
        stub.stopHandler = { [weak stub] _ in stub?.factoryState = Self.stoppedNotes; return (200, #"{"ok":true}"#) }
        tapWhenTappable(stopBtn, "Stop")
        tapWhenTappable(app.buttons["Stop the project"].firstMatch, "Confirm Stop")
        let resumeBtn = app.buttons["factoryProject.resume"].firstMatch
        XCTAssertTrue(resumeBtn.waitForExistence(timeout: 10))
        XCTAssertFalse(stopRefusal.exists, "successful Stop clears stale Stop refusal")
        XCTAssertEqual(resumeBtn.label, "Resume this project")

        stub.postDelay = 1.5
        stub.startHandler = { [weak stub] _ in stub?.factoryState = Self.runningNotes; return (200, #"{"ok":true}"#) }
        let startsBefore = stub.requests("POST /api/factory/projects/start")
        tapWhenTappable(resumeBtn, "Resume")
        XCTAssertTrue(waitUntil(5) { stub.requests("POST /api/factory/projects/start") > startsBefore })
        XCTAssertTrue(resumeBtn.label.hasPrefix("Resuming"), "in-flight Resume must announce Resuming, got: \(resumeBtn.label)")
        XCTAssertTrue(stopBtn.waitForExistence(timeout: 10))
        stub.postDelay = 0

        // 4. Say refusal replaces defaultNote (never stacks), preserves text, clears on edit; in-flight Say keeps ask cards enabled.
        let defaultNote = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Delivered when the coordinator is next free. To answer an ask, use its card.")).firstMatch
        XCTAssertTrue(defaultNote.waitForExistence(timeout: 5))

        stub.sayHandler = { _, _ in (403, #"{"code":"proof_required","error":"proof_required"}"#) }
        typeWhenFocused(field, "First try")
        tapWhenTappable(send, "Send (unpaired)")
        let sayRefusal = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Not sent: Work Mac no longer knows this phone.")).firstMatch
        XCTAssertTrue(sayRefusal.waitForExistence(timeout: 10))
        XCTAssertEqual(field.value as? String, "First try")
        XCTAssertFalse(defaultNote.exists, "defaultNote must not stack under a Say refusal")

        typeWhenFocused(field, "!")
        XCTAssertTrue(waitUntil(3) { !sayRefusal.exists }, "editing text clears stale Say refusal")

        stub.sayHandler = nil
        stub.postDelay = 1.5
        let saysBefore = stub.requests("POST /api/factory/projects/proj-1/say")
        tapWhenTappable(send, "Send (valid)")
        XCTAssertTrue(waitUntil(5) { stub.requests("POST /api/factory/projects/proj-1/say") > saysBefore })
        XCTAssertTrue(app.buttons["question.fask-del-1.option.1"].firstMatch.isEnabled, "in-flight Say must not disable open ask cards")
        let told = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Told the notes factory.")).firstMatch
        XCTAssertTrue(told.waitForExistence(timeout: 10))
        stub.postDelay = 0

        // 5. Typing clears stale `sent`; transient reload error keeps loaded board & draft; state change clears stale `sent`.
        XCTAssertTrue(waitUntil(5) { stub.requests("GET /events") >= 1 })
        typeWhenFocused(field, "Draft kept")
        XCTAssertTrue(waitUntil(3) { !told.exists }, "typing a new instruction clears stale sent confirmation")

        let graphsBefore = stub.requests("GET /api/factory/projects/proj-1/graph")
        stub.factoryGraphErrors["proj-1"] = (503, #"{"error":"herdr busy"}"#)
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(waitUntil(5) { stub.requests("GET /api/factory/projects/proj-1/graph") > graphsBefore })
        usleep(300_000)
        XCTAssertFalse(app.staticTexts["Can't load notes. Trying again."].exists, "transient reload failure must keep loaded board")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.task.T8"].exists)
        XCTAssertEqual(field.value as? String, "Draft kept", "transient reload failure must preserve unsent text")
        stub.factoryGraphErrors["proj-1"] = nil

        tapWhenTappable(send, "Send draft")
        XCTAssertTrue(told.waitForExistence(timeout: 10))

        stub.factoryState = Self.stoppedNotes
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(app.staticTexts["notes is stopped. It gets this when you resume it."].waitForExistence(timeout: 10))
        XCTAssertFalse(told.exists, "project state change clears stale sent confirmation")

        // 6. 404 project-gone dismisses with notice and refreshes the deleted head out of Agents.
        stub.factoryState = "[]"
        stub.sayHandler = { _, _ in (404, #"{"error":"factory project not found"}"#) }
        scrollToTop(app)
        typeWhenFocused(field, "Still there?")
        tapWhenTappable(send, "Send to deleted project")
        XCTAssertTrue(app.staticTexts["This project is gone."].waitForExistence(timeout: 10))
        let goneHead = app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch
        XCTAssertTrue(waitUntil(5) { !goneHead.exists }, "projectGone must refresh the computer so the deleted project head disappears")
    }

    private func waitUntil(_ timeout: TimeInterval = 5, _ check: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if check() { return true }
            usleep(100_000)
        }
        return check()
    }
}
