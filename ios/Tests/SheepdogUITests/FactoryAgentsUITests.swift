import XCTest

/// Failing UI tests for app-factory PR 8 against `StubDaemon` (no daemon
/// checkout): permanent project heads, Stopped section, collapsed Done for
/// finished projects, filtering factory workers/coordinators, factory ask
/// cards answering through `/api/pending/answer`, VoiceOver labels, and
/// empty/error states.
final class FactoryAgentsUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func launch(host: String, extraArgs: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"] + extraArgs
        app.launch()
        return app
    }

    /// Permanent heads (running, stopped, finished in collapsed Done), filtered
    /// worker row, factory ask card kind/task/VoiceOver, and answering via tap.
    func testProjectHeadsStoppedSectionAskCardAndAnswerPath() throws {
        let factory = #"""
        [
          {"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":3,"owner_answer":1,"waiting_on_dep":1,"done":9},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
          {"id":"proj-2","name":"photos","goal":"Faster thumbnails in the photo grid","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"},
          {"id":"proj-3","name":"recipes","goal":"Import recipes from a link","state":"finished","counts":{"done":8},"folder":"~/code/recipes"}
        ]
        """#
        let agents = #"""
        [
          {"pane_id":"p-pay","alias":"codex-payments","kind":"codex","state":"working","current_task":"Add retries"},
          {"pane_id":"p-w8","alias":"worker-t8","kind":"codex","state":"working","labels":{"sheepdog.project":"proj-1","sheepdog.role":"worker"}}
        ]
        """#
        let ask = #"""
        {"id":"fask-del-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"Delete the old notes table in the migration?","summary":"Delete the old notes table in the migration?","factory":{"project":"proj-1","task":"T10","kind":"delete"},"options":[{"index":1,"label":"Keep it and copy the rows","default":true,"recommended":true},{"index":2,"label":"Delete it"},{"index":3,"label":"Not now"}]}
        """#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: agents, questions: "[\(ask)]", factory: factory, pendingAction: ask))
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        let agentsTab = app.buttons["main.pane.flock"].firstMatch
        XCTAssertTrue(agentsTab.waitForExistence(timeout: 15))
        let badgePredicate = NSPredicate(format: "label == %@", "Agents, 1 waiting on you")
        expectation(for: badgePredicate, evaluatedWith: agentsTab)
        waitForExpectations(timeout: 15)

        showAgentsPane(app)

        assertListOrder(app, [
            "flock.section.needs_you",
            "factoryAsk.fask-del-1.card",
            "flock.section.working",
            "flock.factory.proj-1",
            "flock.agent.codex-payments",
            "flock.section.stopped",
            "flock.factory.proj-2",
            "flock.section.done",
        ])
        XCTAssertFalse(app.descendants(matching: .any)["flock.agent.worker-t8"].exists, "factory worker row must be filtered out")
        XCTAssertFalse(app.descendants(matching: .any)["flock.factory.proj-3"].exists, "finished project starts folded in Done")

        // Expand Done to reveal the finished project head.
        tapWhenTappable(scrollToVisible(app, app.buttons["flock.section.done"].firstMatch), "Done fold")
        XCTAssertTrue(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-3"].firstMatch).exists)

        // Head content & VoiceOver label on running head.
        scrollToTop(app)
        let notesHead = scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch)
        XCTAssertTrue(notesHead.label.contains("notes factory"), "VoiceOver label on head: \(notesHead.label)")
        XCTAssertTrue(notesHead.label.contains("3 working · 1 waiting on you · 9 of 14 done"), notesHead.label)

        // Ask card meta line, VoiceOver label, and answering through `/api/pending/answer`.
        scrollToTop(app)
        let askCard = scrollToVisible(app, app.descendants(matching: .any)["factoryAsk.fask-del-1.card"].firstMatch)
        XCTAssertTrue(app.staticTexts["notes · T10 · the coordinator asks"].exists)
        XCTAssertTrue(askCard.label.contains("notes, T10, needs your answer") || app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "notes, T10, needs your answer")).firstMatch.exists)

        let option1 = revealAboveCapsule(app, scrollToVisible(app, app.buttons["factoryAsk.fask-del-1.option.1"].firstMatch))
        tapWhenTappable(option1, "factory ask option 1")
        XCTAssertTrue(app.descendants(matching: .any)["factoryAsk.fask-del-1.answered"].waitForExistence(timeout: 10))
        XCTAssertGreaterThanOrEqual(stub.requests("POST /api/pending/answer"), 1)
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-del-1") && $0.contains("1") }, "bodies: \(stub.answerBodies())")
    }

    /// Blocked coordinator merges into its project head (single badge count, no
    /// duplicate agent row); alert shows cause + fix; Stopped section hides when none.
    func testBlockedCoordinatorSingleCountsAndAlertShowsCauseAndFix() throws {
        let factory = #"""
        [
          {"id":"proj-notes","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":3,"waiting_on_dep":2,"done":9},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
          {"id":"proj-docs","name":"docs","goal":"Docs site","state":"running","counts":{"working":1,"done":2},"folder":"~/code/docs","alert":{"kind":"agents_down","cause":"claude coordinator is signed out","fix":"sign in to claude at this Mac"}}
        ]
        """#
        let agents = #"[{"pane_id":"coord-notes-1","alias":"notes","kind":"claude","state":"blocked"}]"#
        let dialog = #"{"id":"ap-coord","pane_id":"coord-notes-1","kind":"answer_prompt","agent":"notes","question":"Allow gh pr list?","command":"gh pr list --state open","options":[{"index":1,"label":"Yes","default":true},{"index":2,"label":"No"}]}"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: agents, questions: "[\(dialog)]", factory: factory, pendingAction: dialog))
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        let agentsTab = app.buttons["main.pane.flock"].firstMatch
        XCTAssertTrue(agentsTab.waitForExistence(timeout: 15))
        expectation(for: NSPredicate(format: "label == %@", "Agents, 1 waiting on you"), evaluatedWith: agentsTab)
        waitForExpectations(timeout: 15)

        showAgentsPane(app)
        assertListOrder(app, ["flock.section.needs_you", "flock.factory.proj-notes", "flock.section.working", "flock.factory.proj-docs"])
        XCTAssertFalse(app.descendants(matching: .any)["flock.agent.notes"].exists, "blocked coordinator is merged, never duplicated")
        XCTAssertFalse(app.descendants(matching: .any)["flock.section.stopped"].exists, "Stopped section hides when no project is stopped")
        XCTAssertTrue(app.staticTexts["The notes coordinator is stuck on a prompt"].exists)
        XCTAssertTrue(scrollToVisible(app, app.staticTexts["The docs coordinator is not running: claude coordinator is signed out"].firstMatch).exists)
        XCTAssertTrue(scrollToVisible(app, app.staticTexts["Fix: sign in to claude at this Mac"].firstMatch).exists)
    }

    /// Factory-only computer (zero ordinary agents) does not show empty state;
    /// zero agents + zero projects shows empty or no-network error state.
    func testFactoryOnlyIsNotEmptyAndEmptyOrNoNetworkStatesShow() throws {
        let onlyFactory = try StubDaemon(.computer(
            name: "Work Mac", agents: "[]", questions: "[]",
            factory: #"[{"id":"proj-solo","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4}}]"#
        ))
        let soloHost = try onlyFactory.start()
        defer { onlyFactory.stop() }

        let soloApp = launch(host: soloHost)
        showAgentsPane(soloApp)
        XCTAssertTrue(soloApp.descendants(matching: .any)["flock.factory.proj-solo"].waitForExistence(timeout: 15), "stopped factory head keeps Agents out of empty state")
        XCTAssertFalse(soloApp.descendants(matching: .any)["flock.empty"].exists)

        let emptyStub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: "[]"))
        let emptyHost = try emptyStub.start()
        defer { emptyStub.stop() }

        let emptyApp = launch(host: emptyHost)
        showAgentsPane(emptyApp)
        XCTAssertTrue(emptyApp.descendants(matching: .any)["flock.empty"].waitForExistence(timeout: 15))

        let offlineApp = launch(host: emptyHost, extraArgs: ["-sheepdogNoNetwork"])
        showAgentsPane(offlineApp)
        XCTAssertTrue(offlineApp.descendants(matching: .any)["flock.noNetwork"].waitForExistence(timeout: 15))
    }
}
