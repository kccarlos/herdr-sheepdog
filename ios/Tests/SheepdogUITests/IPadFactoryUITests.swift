import XCTest

/// iPad UI tests for app-factory PR 13 against `StubDaemon`:
/// factory projects in the iPad sidebar and All agents, selecting one to show
/// the board with list + graph side by side, tapping a graph node to open
/// `FactoryTaskView`, D12 remote computer board without moving voice, empty and
/// error states with plain fix text, and VoiceOver labels via identifiers only.
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

    func testSidebarListsFactoryProjectsOpensBoardWithGraphAndNavigatesToTask() throws {
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
        XCTAssertEqual(section.label, "Factory projects, 2, and 1 done")

        let notesRow = element(app, "sidebar.factory.proj-1")
        let photosRow = element(app, "sidebar.factory.proj-2")
        let paymentsRow = element(app, "sidebar.agent.payments")
        XCTAssertTrue(notesRow.exists && photosRow.exists && paymentsRow.exists)
        XCTAssertEqual(notesRow.label, "notes factory, running, 1 waiting on you")
        XCTAssertEqual(photosRow.label, "photos factory, stopped")
        XCTAssertLessThan(photosRow.frame.minY, paymentsRow.frame.minY)
        XCTAssertFalse(element(app, "sidebar.factory.proj-3").exists)
        XCTAssertFalse(element(app, "sidebar.agent.worker-t8").exists)

        tapWhenTappable(notesRow, "notes in iPad sidebar")
        XCTAssertTrue(notesRow.isSelected)

        let board = element(app, "factoryProject.board")
        let graph = element(app, "factoryProject.graph")
        XCTAssertTrue(board.waitForExistence(timeout: 15))
        XCTAssertTrue(graph.waitForExistence(timeout: 10))
        XCTAssertEqual(graph.label, "task graph: 7 tasks in 3 layers")

        let taskT8 = element(app, "factoryProject.task.T8")
        XCTAssertTrue(taskT8.exists)
        XCTAssertLessThanOrEqual(taskT8.frame.maxX, graph.frame.minX + 2, "task list and graph sit side by side")
        XCTAssertEqual(element(app, "factoryProject.say.field").label, "Tell the notes factory")
        XCTAssertEqual(element(app, "factoryProject.coordinator").label, "Open the notes coordinator's window")
        let nodeT7 = element(app, "factoryProject.graph.node.T7")
        let nodeT10 = element(app, "factoryProject.graph.node.T10")
        XCTAssertEqual(nodeT7.label, "T7, Search, working, after T1 and T4")
        XCTAssertEqual(element(app, "factoryProject.graph.node.T6").label, "T6, Storage, split")
        XCTAssertEqual(nodeT10.label, "T10, Import, waiting on you, after T4")
        XCTAssertTrue(
            element(app, "factoryProject.graph.layer.0").exists
                && element(app, "factoryProject.graph.layer.1").exists
                && element(app, "factoryProject.graph.layer.2").exists,
            "graph groups nodes into per-layer containers for layer-by-layer VoiceOver order"
        )
        XCTAssertLessThanOrEqual(
            nodeT7.frame.maxX, graph.frame.maxX - 1,
            "third layer node T7 fits inside the graph card without right-edge clipping"
        )
        XCTAssertLessThanOrEqual(
            nodeT10.frame.maxX, graph.frame.maxX - 1,
            "third layer node T10 fits inside the graph card without right-edge clipping"
        )
        shot("13b-board-graph")

        let droppedNode = element(app, "factoryProject.graph.node.T9")
        XCTAssertEqual(droppedNode.label, "T9, Export to PDF, dropped")
        tapWhenTappable(droppedNode, "T9 node in graph")
        let taskView = element(app, "factoryTask.view")
        XCTAssertTrue(taskView.waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "factoryTask.lastDecision").exists)
        tapWhenTappable(app.navigationBars.buttons.element(boundBy: 0), "Back from graph task to notes board")
        XCTAssertTrue(board.waitForExistence(timeout: 10))

        // Tap a dropped task in Done to push FactoryTaskView, verify its toolbar Back button pops cleanly,
        // and verify re-tapping the selected project in the sidebar also pops back to the board.
        let doneFold = revealAboveCapsule(app, scrollToVisible(app, app.buttons["factoryProject.doneFold"].firstMatch))
        tapWhenTappable(doneFold, "Done fold on board")
        let taskT9 = revealAboveCapsule(app, scrollToVisible(app, element(app, "factoryProject.task.T9")))
        tapWhenTappable(taskT9, "task T9 on board")
        XCTAssertTrue(taskView.waitForExistence(timeout: 10))
        XCTAssertTrue(element(app, "factoryTask.lastDecision").exists)
        tapWhenTappable(app.navigationBars.buttons.element(boundBy: 0), "Back to notes board")
        XCTAssertTrue(board.waitForExistence(timeout: 10))

        scrollToTop(app)
        let visibleT8 = revealAboveCapsule(app, scrollToVisible(app, element(app, "factoryProject.task.T8")))
        tapWhenTappable(visibleT8, "task T8 on board")
        XCTAssertTrue(taskView.waitForExistence(timeout: 10))
        tapWhenTappable(notesRow, "selected notes project in sidebar")
        XCTAssertTrue(board.waitForExistence(timeout: 10))
        XCTAssertTrue(
            taskView.waitForNonExistence(timeout: 5),
            "re-selecting the project in the sidebar pops pushed detail views"
        )
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

        let homeSidebarHeader = element(app, "computerLabel.sidebar.fixture-2")
        XCTAssertTrue(homeSidebarHeader.waitForExistence(timeout: 10))

        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents")
        XCTAssertTrue(element(app, "flock.factory.proj-notes").waitForExistence(timeout: 10))
        XCTAssertFalse(element(app, "flock.agent.coord-notes").exists)
        XCTAssertFalse(element(app, "flock.factory.proj-done").exists)
        let doneFold = scrollToVisible(app, app.buttons["flock.section.done"].firstMatch)
        let doneFoldLabel = doneFold.label
        tapWhenTappable(doneFold, "Done fold")
        XCTAssertTrue(scrollToVisible(app, element(app, "flock.factory.proj-done")).exists)

        // Tap a FactoryHeadCard directly inside All agents (covers FactoryHeadCard.onOpen + D12).
        let homeCard = revealAboveCapsule(app, scrollToVisible(app, element(app, "flock.factory.proj-home")))
        tapWhenTappable(homeCard, "Home Mac photos card in All agents")
        XCTAssertTrue(element(app, "factoryProject.board").waitForExistence(timeout: 15))
        XCTAssertTrue(scrollToVisible(app, element(app, "factoryProject.task.T2")).exists)
        XCTAssertEqual(element(app, "computer.sidebarVoiceMarker.fixture-1").label, "Voice is on Work Mac")

        // Return to All agents, check multi-computer counts/labels, and also verify sidebar remote open.
        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents again")
        let allTitle = element(app, "allAgents.title")
        XCTAssertTrue(allTitle.waitForExistence(timeout: 10))
        let homeSectionHeader = scrollToVisible(app, element(app, "computerLabel.section.fixture-2"))
        let sidebarSummary = homeSidebarHeader.label
        let sectionSummary = homeSectionHeader.label
        let titleLabel = allTitle.label

        tapWhenTappable(homeSidebar, "Home Mac photos in sidebar")
        let homeGraph = element(app, "factoryProject.graph")
        XCTAssertTrue(homeGraph.waitForExistence(timeout: 15))
        XCTAssertEqual(homeGraph.label, "task graph: 2 tasks in 2 layers")
        XCTAssertEqual(element(app, "computer.sidebarVoiceMarker.fixture-1").label, "Voice is on Work Mac")

        continueAfterFailure = true
        XCTAssertEqual(doneFoldLabel, "Done (1)", "decorative chevron in Done fold must be hidden from VoiceOver")
        XCTAssertTrue(sidebarSummary.contains("1 factory project"), sidebarSummary)
        XCTAssertFalse(sidebarSummary.contains("No agents"), sidebarSummary)
        XCTAssertTrue(sectionSummary.contains("1 factory project"), sectionSummary)
        XCTAssertFalse(sectionSummary.contains("No agents"), sectionSummary)
        XCTAssertTrue(titleLabel.contains("3 factory projects"), titleLabel)
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
        XCTAssertFalse(element(app, "factoryProject.graph").exists)
        XCTAssertEqual(element(app, "factoryProject.alert.cause").label, "The scratch coordinator is not running: claude coordinator is signed out")
        XCTAssertEqual(element(app, "factoryProject.alert.fix").label, "Fix: sign in to claude at this Mac")
        XCTAssertEqual(element(app, "factoryProject.emptyTasks.heading").label, "no tasks yet")
        XCTAssertEqual(element(app, "factoryProject.emptyTasks.detail").label, "The coordinator has not planned this project yet.")

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
        XCTAssertTrue(element(app, "factoryProject.graph").waitForExistence(timeout: 15))

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

        let offlineApp = launch(computers: "Work Mac=\(zeroHost)", extra: ["-sheepdogNoNetwork"])
        tapWhenTappable(element(offlineApp, "sidebar.allAgents"), "All agents offline")
        XCTAssertTrue(element(offlineApp, "flock.noNetwork").waitForExistence(timeout: 15))
    }

    func testBoardGraphAtLargestText() throws {
        let heads = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes",
          "coordinator_pane":"coord-notes-1"}]
        """#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(
            computers: "Work Mac=\(host)",
            extra: ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        )
        let notesRow = element(app, "sidebar.factory.proj-1")
        XCTAssertTrue(notesRow.waitForExistence(timeout: 15))
        tapWhenTappable(notesRow, "notes at AX5")
        let graph = scrollToVisible(app, element(app, "factoryProject.graph"))
        XCTAssertEqual(graph.label, "task graph: 7 tasks in 3 layers")
        let doneFold = app.buttons["factoryProject.doneFold"].firstMatch
        XCTAssertTrue(doneFold.exists)
        XCTAssertLessThanOrEqual(
            doneFold.frame.maxY, graph.frame.minY + 2,
            "at AX5 the graph must stack below the task list"
        )
        XCTAssertLessThan(
            abs(doneFold.frame.minX - graph.frame.minX), 8,
            "at AX5 the graph aligns with the full-width task column"
        )
        XCTAssertTrue(scrollToVisible(app, element(app, "factoryProject.graph.node.T7")).exists)
        shot("13b-board-graph-ax5")
    }

    func testSecondaryComputerWithoutFactoryProjectsKeepsTwoColumnGridAndOnlineTotals() throws {
        let workHeads = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes",
          "coordinator_pane":"coord-notes-1"}]
        """#
        let workAgents = #"""
        [{"pane_id":"p-pay","alias":"payments","kind":"claude","state":"working","workspace_label":"webapp"}]
        """#
        let workStub = try StubDaemon(.computer(
            name: "Work Mac", agents: workAgents, questions: "[\(Self.sampleAsk)]",
            factory: workHeads, pendingAction: Self.sampleAsk
        ))

        let homeBlocked = StubDaemon.blocked(
            alias: "writer", space: "blog", pane: "h1:p1", question: "Publish the post now?"
        )
        let homeWorking = #"""
        {"pane_id":"h1:p2","alias":"editor","kind":"codex","state":"working","workspace_label":"photos"}
        """#
        let homeStub = try StubDaemon(.computer(
            name: "Home Mac", agents: "[\(homeBlocked.agent),\(homeWorking)]",
            questions: "[\(homeBlocked.question)]", factory: "[]"
        ))

        let workHost = try workStub.start()
        defer { workStub.stop() }
        let homeHost = try homeStub.start()
        defer { homeStub.stop() }

        let app = launch(computers: "Work Mac=\(workHost);Home Mac=\(homeHost);Lab Mac=127.0.0.1:9")
        let allAgentsNav = element(app, "sidebar.allAgents")
        XCTAssertTrue(allAgentsNav.waitForExistence(timeout: 15))
        expectation(for: NSPredicate(format: "label CONTAINS %@", "2 waiting"), evaluatedWith: allAgentsNav)
        waitForExpectations(timeout: 15)

        tapWhenTappable(allAgentsNav, "All agents")
        let allTitle = element(app, "allAgents.title")
        XCTAssertTrue(allTitle.waitForExistence(timeout: 10))
        let titleLabel = allTitle.label

        let homeHeader = scrollToVisible(app, element(app, "computerLabel.section.fixture-2"))
        XCTAssertTrue(homeHeader.label.contains("2 agents · 1 waiting"), homeHeader.label)

        let writerCard = revealAboveCapsule(app, scrollToVisible(app, element(app, "flock.agent.writer")))
        let editorCard = revealAboveCapsule(app, scrollToVisible(app, element(app, "flock.agent.editor")))
        XCTAssertTrue(writerCard.exists && editorCard.exists)
        let writerMaxX = writerCard.frame.maxX
        let editorMinX = editorCard.frame.minX

        continueAfterFailure = true
        XCTAssertTrue(titleLabel.contains("3 agents"), titleLabel)
        XCTAssertTrue(titleLabel.contains("1 factory project"), titleLabel)
        XCTAssertTrue(titleLabel.contains("2 waiting"), titleLabel)
        XCTAssertLessThanOrEqual(
            writerMaxX, editorMinX,
            "secondary computer without factory projects keeps its two-column running grid in All agents"
        )
    }

    func testAnsweredFactoryAskLingersInAllAgents() throws {
        let heads = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes",
          "coordinator_pane":"coord-notes-1"}]
        """#
        let agents = #"""
        [{"pane_id":"p-pay","alias":"payments","kind":"claude","state":"working","workspace_label":"webapp"}]
        """#
        let stub = try StubDaemon(.computer(
            name: "Work Mac", agents: agents, questions: "[\(Self.sampleAsk)]",
            factory: heads, pendingAction: nil
        ))
        stub.pendingActionState = Self.sampleAsk
        stub.answerHandler = { [weak stub] _ in
            stub?.questionsState = "[]"
            stub?.pendingActionState = nil
            return (200, #"{"ok":true,"spoken":"Answered."}"#)
        }
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(computers: "Work Mac=\(host)")
        XCTAssertTrue(element(app, "sidebar.factory.proj-1").waitForExistence(timeout: 15))
        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents")
        XCTAssertTrue(element(app, "factoryAsk.fask-del-1.card").waitForExistence(timeout: 10))

        let statesBeforeAnswer = stub.requests("GET /api/state")
        let option1 = revealAboveCapsule(
            app, scrollToVisible(app, app.buttons["factoryAsk.fask-del-1.option.1"].firstMatch)
        )
        tapWhenTappable(option1, "Keep it option on factory ask")
        XCTAssertTrue(waitUntil(5) {
            stub.requests("POST /api/pending/answer") >= 1 && stub.requests("GET /api/state") > statesBeforeAnswer
        }, "post-answer /api/state refresh completed")
        usleep(150_000)
        let lingeredInAll = element(app, "factoryAsk.fask-del-1.answered").exists

        // Working filter hides settled asks, while All shows it while lingering.
        tapWhenTappable(element(app, "allAgents.filter.working"), "Working filter")
        XCTAssertFalse(element(app, "factoryAsk.fask-del-1.card").exists)
        tapWhenTappable(element(app, "allAgents.filter.all"), "All filter")

        XCTAssertTrue(
            lingeredInAll,
            "settled factory ask card must linger with confirmation after /api/state drops the open ask"
        )
    }

    func testGoneFactoryProjectOnIPadShowsProjectGoneInsteadOfAgentGone() throws {
        let heads = #"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes",
          "coordinator_pane":"coord-notes-1"}]
        """#
        let agents = #"""
        [{"pane_id":"p-pay","alias":"payments","kind":"claude","state":"working","workspace_label":"webapp"}]
        """#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: agents, questions: "[]", factory: heads))
        stub.enableEvents = true
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(computers: "Work Mac=\(host)")
        XCTAssertTrue(element(app, "sidebar.factory.proj-1").waitForExistence(timeout: 15))
        XCTAssertTrue(waitUntil(5) { stub.requests("GET /events") >= 1 })

        tapWhenTappable(element(app, "sidebar.factory.proj-1"), "notes in sidebar")
        let board = element(app, "factoryProject.board")
        XCTAssertTrue(board.waitForExistence(timeout: 15))

        stub.factoryState = "[]"
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(board.waitForNonExistence(timeout: 10))
        continueAfterFailure = true
        XCTAssertFalse(app.staticTexts["That agent is gone"].exists, "a factory project must never show IPadAgentGone")
        XCTAssertFalse(element(app, "agentGone.back").exists)
        XCTAssertTrue(app.staticTexts["This project is gone."].waitForExistence(timeout: 3))
    }

    func testSidebarAndAllAgentsAtLargestText() throws {
        let workHeads = #"""
        [{"id":"proj-1","name":"customer-portal-migration","goal":"Migrate portal","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":1},"folder":"~/code/customer-portal-migration",
          "coordinator_pane":"coord-1"},
         {"id":"proj-2","name":"recipes","goal":"Import recipes","state":"finished",
          "counts":{"done":4},"folder":"~/code/recipes"}]
        """#
        let homeHeads = #"""
        [{"id":"proj-home","name":"photos","goal":"Organise albums","state":"stopped",
          "counts":{"done":1,"queued":1},"folder":"~/code/photos"}]
        """#
        let workStub = try StubDaemon(.computer(
            name: "Work Mac", agents: "[]", questions: "[\(Self.sampleAsk)]",
            factory: workHeads, pendingAction: Self.sampleAsk
        ))
        workStub.factoryGraphs["proj-1"] = Self.sampleGraph
        let homeStub = try StubDaemon(.computer(name: "Home Mac", agents: "[]", questions: "[]", factory: homeHeads))

        let workHost = try workStub.start()
        defer { workStub.stop() }
        let homeHost = try homeStub.start()
        defer { homeStub.stop() }

        let app = launch(
            computers: "Work Mac=\(workHost);Home Mac=\(homeHost)",
            extra: ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        )
        let longRow = element(app, "sidebar.factory.proj-1")
        XCTAssertTrue(longRow.waitForExistence(timeout: 15))
        XCTAssertEqual(longRow.label, "customer-portal-migration factory, running, 1 waiting on you")
        XCTAssertLessThanOrEqual(longRow.frame.maxX, 390.5)

        let homeSidebarHeader = element(app, "computerLabel.sidebar.fixture-2")
        XCTAssertTrue(homeSidebarHeader.waitForExistence(timeout: 10))
        let homeSidebarSummary = homeSidebarHeader.label

        tapWhenTappable(element(app, "sidebar.allAgents"), "All agents at AX5")
        let allTitle = element(app, "allAgents.title")
        XCTAssertTrue(allTitle.waitForExistence(timeout: 10))
        let titleLabel = allTitle.label

        let askCard = scrollToVisible(app, element(app, "factoryAsk.fask-del-1.card"))
        let askMaxY = askCard.frame.maxY
        let headCard = scrollToVisible(app, element(app, "flock.factory.proj-1"))
        XCTAssertLessThanOrEqual(askMaxY, headCard.frame.minY, "AX5 collapses All agents to a single column")

        let doneFold = revealAboveCapsule(app, scrollToVisible(app, app.buttons["flock.section.done"].firstMatch))
        let doneFoldLabel = doneFold.label

        scrollToTop(app)
        let tappableHead = revealAboveCapsule(app, scrollToVisible(app, element(app, "flock.factory.proj-1")))
        tapWhenTappable(tappableHead, "project head in All agents at AX5")
        XCTAssertTrue(element(app, "factoryProject.board").waitForExistence(timeout: 15))

        continueAfterFailure = true
        XCTAssertTrue(homeSidebarSummary.contains("1 factory project"), homeSidebarSummary)
        XCTAssertTrue(titleLabel.contains("3 factory projects"), titleLabel)
        XCTAssertEqual(doneFoldLabel, "Done (1)")
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
