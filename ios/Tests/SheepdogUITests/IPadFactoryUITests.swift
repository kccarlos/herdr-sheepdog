import XCTest

/// iPad UI tests for app-factory PR 13a against `StubDaemon`:
/// factory projects in the iPad sidebar and All agents, selecting one to show
/// the board, D12 remote computer board without moving voice, empty and error
/// states with plain fix text, and VoiceOver labels via identifiers only.
final class IPadFactoryUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        XCUIDevice.shared.orientation = .landscapeLeft
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    private func launch(computers: String, extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", computers] + extra
        app.launch()
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    private func shot(_ name: String) {
        guard let directory = environment["SHEEPDOG_IPAD_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        let image = XCUIScreen.main.screenshot().image
        let upright = UIGraphicsImageRenderer(size: image.size, format: {
            let format = UIGraphicsImageRendererFormat()
            format.scale = image.scale
            return format
        }()).pngData { _ in image.draw(in: CGRect(origin: .zero, size: image.size)) }
        try? upright.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(name)-\(suffix).png"))
    }

    private static let sampleGraph = #"""
    {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
      {"ref":"T1","title":"Notes page","stage":"done","after":[],"dependents":["T4","T7"],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]},
      {"ref":"T6","title":"Storage","stage":"done","after":[],"dependents":["T8"],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"split","state":"ended","text":"ended without merging"}]},
      {"ref":"T9","title":"Export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"last_actor_decision":{"at":"2026-11-25T11:02:00Z","by":"owner","decision":"dropped T9","why":"skip PDF export for v1"},"steps":[{"name":"planned","state":"done"},{"name":"dropped","state":"ended","text":"ended without merging"}]},
      {"ref":"T4","title":"Search box","stage":"done","after":["T1"],"dependents":["T7","T10"],"layer":1,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]},
      {"ref":"T7","title":"Search","stage":"working","why_waiting":"In review round 2","after":["T1","T4"],"dependents":[],"layer":2,"rounds":1,"run_role":"reviewer","run_cli":"claude","run_started_at":"2026-11-25T11:26:00Z","steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"done"},{"name":"review","state":"now","text":"review round 2; needs 2 passing, at most 5"},{"name":"CI","state":"todo"},{"name":"merged","state":"todo"}]},
      {"ref":"T8","title":"Tags","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":[],"layer":2,"rounds":1,"run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z","steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo"}]},
      {"ref":"T10","title":"Import","stage":"owner_answer","why_waiting":"Delete the old notes table?","after":["T4"],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now"}]}
    ]}
    """#

    private static let sampleAsk = #"{"id":"fask-del-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"Delete the old notes table?","summary":"Delete the old notes table?","factory":{"project":"proj-1","task":"T10","kind":"delete"},"options":[{"index":1,"label":"Keep it","default":true,"recommended":true},{"index":2,"label":"Delete it"}]}"#

    func testSidebarListsFactoryProjectsOpensBoardAndNavigatesToTask() throws {
        let heads = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
         {"id":"proj-2","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"},
         {"id":"proj-3","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":8},"folder":"~/code/recipes"}]
        """#
        let agents = #"[{"pane_id":"p-pay","alias":"payments","kind":"claude","state":"working","workspace_label":"webapp"},{"pane_id":"p-w8","alias":"worker-t8","kind":"codex","state":"working","workspace_label":"notes","labels":{"sheepdog.project":"proj-1","sheepdog.role":"worker"}}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: agents, questions: "[\(Self.sampleAsk)]", factory: heads, pendingAction: Self.sampleAsk))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(computers: "Work Mac=\(host)")
        let section = element(app, "sidebar.factorySection")
        XCTAssertTrue(section.waitForExistence(timeout: 15))
        XCTAssertTrue(section.label.contains("Factory projects") && section.label.contains("2, and 1 done"), section.label)

        let notesRow = element(app, "sidebar.factory.proj-1")
        let photosRow = element(app, "sidebar.factory.proj-2")
        let paymentsRow = element(app, "sidebar.agent.payments")
        XCTAssertTrue(notesRow.exists && photosRow.exists && paymentsRow.exists)
        XCTAssertEqual(notesRow.label, "notes factory, running, 1 waiting on you")
        XCTAssertEqual(photosRow.label, "photos factory, stopped")
        XCTAssertLessThan(photosRow.frame.minY, paymentsRow.frame.minY)
        XCTAssertFalse(element(app, "sidebar.factory.proj-3").exists)
        XCTAssertFalse(element(app, "sidebar.agent.worker-t8").exists)
        shot("13a-sidebar")

        tapWhenTappable(notesRow, "notes in iPad sidebar")
        XCTAssertTrue(notesRow.isSelected)

        let board = element(app, "factoryProject.board")
        XCTAssertTrue(board.waitForExistence(timeout: 15))
        let taskT8 = scrollToVisible(app, element(app, "factoryProject.task.T8"))
        XCTAssertTrue(taskT8.exists)
        scrollToTop(app)
        XCTAssertEqual(element(app, "factoryProject.say.field").label, "Tell the notes factory")
        XCTAssertEqual(element(app, "factoryProject.coordinator").label, "Open the notes coordinator's window")
        shot("13a-board")
    }

    func testAllAgentsHeadsBlockedCoordinatorDeduplicationAndRemoteProjectD12() throws {
        let workHeads = #"[{"id":"proj-notes","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":2,"done":3},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},{"id":"proj-done","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":4},"folder":"~/code/recipes"}]"#
        let coordAgent = #"[{"pane_id":"coord-notes-1","alias":"coord-notes","kind":"claude","state":"blocked","workspace_label":"notes"}]"#
        let coordDialog = #"{"id":"ap-coord","pane_id":"coord-notes-1","kind":"answer_prompt","agent":"notes","question":"Allow gh pr list?","command":"gh pr list","options":[{"index":1,"label":"Yes","default":true},{"index":2,"label":"No"}]}"#
        let workStub = try StubDaemon(.computer(name: "Work Mac", agents: coordAgent, questions: "[\(coordDialog)]", factory: workHeads, pendingAction: coordDialog))

        let homeHeads = #"[{"id":"proj-home","name":"photos","goal":"Organise albums","state":"stopped","counts":{"done":1,"queued":1},"folder":"~/code/photos"}]"#
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[]", questions: "[]", factory: homeHeads))
        homeStub.factoryGraphs["proj-home"] = #"{"protocol":1,"project":"proj-home","folder":"~/code/photos","tasks":[{"ref":"T1","title":"Schema","stage":"done","after":[],"dependents":["T2"],"layer":0,"rounds":0,"steps":[{"name":"merged","state":"done"}]},{"ref":"T2","title":"Grid","stage":"queued","after":["T1"],"dependents":[],"layer":1,"rounds":0,"steps":[]}]}"#

        let workHost = try workStub.start(); defer { workStub.stop() }
        let homeHost = try homeStub.start(); defer { homeStub.stop() }

        let app = launch(computers: "Work Mac=\(workHost);Home Mac=\(homeHost)")
        let notesSidebar = element(app, "sidebar.factory.proj-notes")
        let homeSidebar = element(app, "sidebar.factory.proj-home")
        XCTAssertTrue(notesSidebar.waitForExistence(timeout: 15) && homeSidebar.waitForExistence(timeout: 15))
        XCTAssertEqual(notesSidebar.label, "notes factory, running, 1 waiting on you")
        XCTAssertFalse(element(app, "sidebar.agent.coord-notes").exists)

        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents")
        XCTAssertTrue(element(app, "flock.factory.proj-notes").waitForExistence(timeout: 10))
        XCTAssertFalse(element(app, "flock.agent.coord-notes").exists)
        XCTAssertFalse(element(app, "flock.factory.proj-done").exists)
        shot("13a-all-agents")
        tapWhenTappable(scrollToVisible(app, app.buttons["flock.section.done"].firstMatch), "Done fold")
        XCTAssertTrue(scrollToVisible(app, element(app, "flock.factory.proj-done")).exists)

        tapWhenTappable(homeSidebar, "Home Mac photos in sidebar")
        XCTAssertTrue(element(app, "factoryProject.board").waitForExistence(timeout: 15))
        XCTAssertTrue(scrollToVisible(app, element(app, "factoryProject.task.T2")).exists)
        XCTAssertEqual(element(app, "computer.sidebarVoiceMarker.fixture-1").label, "Voice is on Work Mac")
    }

    func testEmptyAndErrorStatesWithPlainFixTextAndVoiceOver() throws {
        let heads = #"[{"id":"proj-empty","name":"scratch","goal":"Plan the app","state":"running","counts":{},"folder":"~/code/scratch","alert":{"kind":"agents_down","cause":"claude coordinator is signed out","fix":"sign in to claude at this Mac"}},{"id":"proj-err","name":"broken","goal":"Fix builds","state":"stopped","counts":{"done":1,"queued":1},"folder":"~/code/broken"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-empty"] = #"{"protocol":1,"project":"proj-empty","folder":"~/code/scratch","tasks":[]}"#
        stub.factoryGraphErrors["proj-err"] = (503, #"{"error":"factory is not available"}"#)
        stub.startHandler = { _ in (400, #"{"error":"1 start check failed","results":[{"name":"claude","status":"fail","detail":"claude is not signed in","fix":"run: claude auth login"}]}"#) }
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(computers: "Work Mac=\(host)")
        let emptyProj = element(app, "sidebar.factory.proj-empty")
        XCTAssertTrue(emptyProj.waitForExistence(timeout: 15))
        XCTAssertFalse(element(app, "sidebar.empty").exists)

        tapWhenTappable(emptyProj, "scratch in sidebar")
        XCTAssertTrue(element(app, "factoryProject.board").waitForExistence(timeout: 15))
        XCTAssertEqual(element(app, "factoryProject.alert.cause").label, "The scratch coordinator is not running: claude coordinator is signed out")
        XCTAssertEqual(element(app, "factoryProject.alert.fix").label, "Fix: sign in to claude at this Mac")
        XCTAssertEqual(element(app, "factoryProject.emptyTasks.heading").label, "no tasks yet")
        XCTAssertEqual(element(app, "factoryProject.emptyTasks.detail").label, "The coordinator has not planned this project yet.")
        shot("13a-empty-board")

        tapWhenTappable(element(app, "sidebar.factory.proj-err"), "broken in sidebar")
        let errHeading = element(app, "factoryProject.loadError.heading")
        XCTAssertTrue(errHeading.waitForExistence(timeout: 15))
        XCTAssertEqual(errHeading.label, "Can't load broken. Trying again.")
        XCTAssertTrue(element(app, "factoryProject.loadError.detail").label.contains("The rest of the app still works."))

        stub.factoryGraphErrors["proj-err"] = nil
        stub.factoryGraphs["proj-err"] = #"{"protocol":1,"project":"proj-err","folder":"~/code/broken","tasks":[{"ref":"T1","title":"Base","stage":"done","after":[],"dependents":["T2"],"layer":0,"rounds":0,"steps":[{"name":"merged","state":"done"}]},{"ref":"T2","title":"Fix","stage":"queued","after":["T1"],"dependents":[],"layer":1,"rounds":0,"steps":[]}]}"#
        let retryBtn = app.buttons["factoryProject.retry"].firstMatch
        XCTAssertEqual(retryBtn.label, "Try again")
        tapWhenTappable(retryBtn, "Try again")
        XCTAssertTrue(element(app, "factoryProject.board").waitForExistence(timeout: 15))

        tapWhenTappable(app.buttons["factoryProject.resume"].firstMatch, "Resume broken")
        let actionErr = element(app, "factoryProject.actionError")
        XCTAssertTrue(actionErr.waitForExistence(timeout: 10))
        XCTAssertEqual(actionErr.label, "Not started: a start check failed. Fix it, then press Resume again.")
        XCTAssertEqual(app.buttons["factoryProject.check.copy"].firstMatch.label, "Copy the command claude auth login")

        let zeroStub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: "[]"))
        let zeroHost = try zeroStub.start(); defer { zeroStub.stop() }
        let zeroApp = launch(computers: "Work Mac=\(zeroHost)")
        XCTAssertTrue(element(zeroApp, "sidebar.empty").waitForExistence(timeout: 15))
        XCTAssertEqual(element(zeroApp, "sidebar.empty.message").label, "No agents running.")
        tapWhenTappable(element(zeroApp, "sidebar.allAgents"), "All agents empty")
        XCTAssertTrue(element(zeroApp, "flock.empty").waitForExistence(timeout: 10))
        shot("13a-empty-state")

        let offlineApp = launch(computers: "Work Mac=\(zeroHost)", extra: ["-sheepdogNoNetwork"])
        tapWhenTappable(element(offlineApp, "sidebar.allAgents"), "All agents offline")
        XCTAssertTrue(element(offlineApp, "flock.noNetwork").waitForExistence(timeout: 15))
    }
}
