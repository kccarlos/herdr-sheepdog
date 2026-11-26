import XCTest

/// Failing UI tests for app-factory PR 10 against `StubDaemon` (no daemon
/// checkout): tapping a task opens the read-only task view (steps, run line,
/// last decision + why, rounds, empty states, no worker window), New goal on
/// a stopped project, and Reopen on a finished project with real daemon refusals.
final class FactoryTaskAndGoalUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    private func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    private static let runningNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1,"queued":1,"done":1},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
    private static let stoppedNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"stopped","counts":{"queued":1,"done":1},"folder":"~/code/notes"}]"#
    private static let finishedNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"finished","counts":{"done":2},"folder":"~/code/notes"}]"#
    private static let sampleGraph = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T8","title":"feat: tags","brief":"Tags on a note, and a tag filter on the notes page.","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":["T11"],"layer":1,"rounds":2,"model":"gpt-6.2-sol","terminal":"w-8","run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z","last_actor_decision":{"at":"2026-11-25T11:05:00Z","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo","text":"needs 2 passing, at most 5"}]},{"ref":"T11","title":"feat: sync","stage":"queued","after":[],"dependents":[],"layer":2,"rounds":0,"steps":[]},{"ref":"T9","title":"feat: export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"last_actor_decision":{"at":"2026-11-25T11:04:00Z","by":"coordinator","decision":"dropped T9","why":"owner said to skip the export"},"steps":[{"name":"planned","state":"done"},{"name":"dropped","state":"ended","text":"ended without merging"}]}]}"#

    /// Tapping a task opens the task view with steps, run role/cli/model, last decision + why,
    /// rounds, brief, VoiceOver labels, dropped task view, empty states, and no worker window.
    func testTapTaskOpensTaskViewWithStepsRunLastDecisionRoundsEmptyAndNoWorkerWindow() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))

        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryProject.task.T8"].firstMatch), "T8 row")
        XCTAssertTrue(app.descendants(matching: .any)["factoryTask.view"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["feat: tags"].exists && app.staticTexts["after T6"].exists && app.staticTexts["2 review rounds"].exists)
        let runLabel = app.descendants(matching: .any)["factoryTask.run"].label
        XCTAssertTrue(runLabel.contains("worker") && runLabel.contains("codex") && runLabel.contains("gpt-6.2-sol"), runLabel)
        XCTAssertFalse(app.descendants(matching: .any)["agentScreen.terminal"].exists || app.buttons["factoryTask.window"].exists, "no worker window in v1")

        let step1 = app.descendants(matching: .any)["factoryTask.step.1"].firstMatch
        XCTAssertTrue(scrollToVisible(app, step1).label.contains("implementing, now: sent back from review round 1; 1 round done"), step1.label)
        XCTAssertTrue(scrollToVisible(app, app.descendants(matching: .any)["factoryTask.lastDecision"].firstMatch).exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'the tag index must be case-insensitive'")).firstMatch.exists)
        XCTAssertTrue(scrollToVisible(app, app.descendants(matching: .any)["factoryTask.brief"].firstMatch).exists)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.doneFold"].firstMatch), "done fold")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryProject.task.T9"].firstMatch), "T9 dropped row")
        XCTAssertTrue(app.descendants(matching: .any)["factoryTask.view"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'ended without merging'")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'owner said to skip the export'")).firstMatch.exists)
        XCTAssertFalse(app.descendants(matching: .any)["factoryTask.run"].exists)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryProject.task.T11"].firstMatch), "T11 empty row")
        XCTAssertTrue(app.staticTexts["No steps recorded yet."].waitForExistence(timeout: 10))
        XCTAssertFalse(app.descendants(matching: .any)["factoryTask.lastDecision"].exists || app.descendants(matching: .any)["factoryTask.brief"].exists)
    }

    /// New goal on a stopped project: Cancel sends nothing; submit posts `/api/factory/projects/start`
    /// with folder + new goal and updates the board to running.
    func testNewGoalOnStoppedProjectPostsStartAndUpdatesBoard() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.stoppedNotes))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        stub.startHandler = { [weak stub] _ in
            stub?.factoryState = #"[{"id":"proj-1","name":"notes","goal":"Add keyboard shortcuts","state":"running","counts":{"working":1,"done":1},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
            return (200, #"{"ok":true,"device_id":"dev-1"}"#)
        }
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "stopped notes head")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))

        let newGoalBtn = app.buttons["factoryProject.newGoal"].firstMatch
        XCTAssertEqual(newGoalBtn.label, "New goal")
        tapWhenTappable(newGoalBtn, "New goal")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.goalSheet"].waitForExistence(timeout: 10))
        let submit = app.buttons["factoryProject.goal.submit"].firstMatch
        XCTAssertEqual(submit.label, "Start with this goal")
        XCTAssertFalse(submit.isEnabled, "empty goal disables submit")
        tapWhenTappable(app.buttons["factoryProject.goal.cancel"].firstMatch, "Cancel")
        XCTAssertEqual(stub.requests("POST /api/factory/projects/start"), 0)

        tapWhenTappable(newGoalBtn, "New goal again")
        typeWhenFocused(app.descendants(matching: .any)["factoryProject.goal.field"].firstMatch, "Add keyboard shortcuts")
        tapWhenTappable(submit, "Start with this goal")

        XCTAssertTrue(app.staticTexts["Add keyboard shortcuts"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["factoryProject.stop"].waitForExistence(timeout: 10))
        XCTAssertTrue(stub.startBodies().contains { $0.contains("~/code/notes") && $0.contains("Add keyboard shortcuts") })
    }

    /// Reopen on a finished project: 409 same-goal refusal keeps draft in place, 400 start-check
    /// refusal surfaces checks and fix, and a valid new goal transitions the board to running.
    func testReopenFinishedProjectHandlesSameGoalAndCheckRefusalsThenUpdatesBoard() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.finishedNotes))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        stub.startHandler = { _ in (409, #"{"code":"already_closed","error":"This project is finished. Give it a new goal."}"#) }
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["flock.section.done"].firstMatch), "Done fold")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "finished notes head")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["Finished. To go on, reopen it with a new goal."].exists)

        let reopenBtn = app.buttons["factoryProject.reopen"].firstMatch
        XCTAssertEqual(reopenBtn.label, "Reopen with a new goal")
        tapWhenTappable(reopenBtn, "Reopen with a new goal")
        let submit = app.buttons["factoryProject.goal.submit"].firstMatch
        XCTAssertEqual(submit.label, "Reopen with this goal")

        let field = app.descendants(matching: .any)["factoryProject.goal.field"].firstMatch
        typeWhenFocused(field, "A notes page with search")
        tapWhenTappable(submit, "Submit same goal")
        let err = app.descendants(matching: .any)["factoryProject.goal.error"].firstMatch
        XCTAssertTrue(err.waitForExistence(timeout: 10))
        XCTAssertEqual(err.label, "This project is finished. Give it a new goal.")
        XCTAssertEqual(field.value as? String, "A notes page with search", "refusal preserves typed goal")

        tapWhenTappable(app.buttons["factoryProject.goal.cancel"].firstMatch, "Cancel after 409")
        stub.startHandler = { _ in (400, #"{"error":"start checks failed","results":[{"name":"start claude","status":"fail","detail":"claude is not signed in","fix":"run: claude auth login"}]}"#) }
        tapWhenTappable(reopenBtn, "Reopen for new goal")
        typeWhenFocused(app.descendants(matching: .any)["factoryProject.goal.field"].firstMatch, "Add dark mode")
        tapWhenTappable(submit, "Submit failing checks")
        XCTAssertTrue(err.waitForExistence(timeout: 10))
        XCTAssertEqual(err.label, "Not started: a start check failed. Fix it, then press Reopen again.")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'claude is not signed in'")).firstMatch.exists)

        stub.startHandler = { [weak stub] _ in
            stub?.factoryState = #"[{"id":"proj-1","name":"notes","goal":"Add dark mode","state":"running","counts":{"working":1,"done":2},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
            return (200, #"{"ok":true,"device_id":"dev-1"}"#)
        }
        tapWhenTappable(submit, "Reopen with dark mode")
        XCTAssertTrue(app.staticTexts["Add dark mode"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["factoryProject.stop"].waitForExistence(timeout: 10))
        XCTAssertTrue(stub.startBodies().contains { $0.contains("~/code/notes") && $0.contains("Add dark mode") })
    }
}
