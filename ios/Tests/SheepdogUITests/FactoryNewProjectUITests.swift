import XCTest

/// Failing UI tests for app-factory PR 12 against `StubDaemon` (no daemon checkout):
/// `+` menu "New factory project" beside existing entries, folder picker reuse & marks,
/// empty/non-git validation, 400 start-check results with fixes keeping Start disabled
/// until retried, success navigating to the new project's board and showing its head
/// in Agents, and empty/error states.
final class FactoryNewProjectUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    private func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    private static let existingPhotos = #"[{"id":"proj-photos","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"}]"#
    private static let startedProjects = #"[{"id":"proj-notes","name":"notes","goal":"A notes page with search","state":"running","counts":{},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},{"id":"proj-photos","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"}]"#
    private static let spacesWithMixedFolders = #"""
    {"spaces":[
      {"id":"w1","name":"notes","path":"~/code/notes","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]},
      {"id":"w2","name":"photos","path":"~/code/photos","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]}
    ],"folders":[
      {"name":"drafts","path":"~/Documents/drafts","git":false}
    ],"kinds":[{"kind":"claude","name":"Claude"}],"worktrees":true,"needs_access":[]}
    """#

    func testPlusMenuNewProjectValidationCheckFailureRetryAndBoardNavigation() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.existingPhotos))
        stub.helloFeatures = ["agent-actions"]
        stub.enableEvents = true
        stub.spacesJSON = Self.spacesWithMixedFolders
        stub.checkHandler = { _ in (500, "{}") }
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        XCTAssertTrue(app.descendants(matching: .any)["flock.factory.proj-photos"].waitForExistence(timeout: 15))

        // 1. `+` menu shows "New factory project" next to "New agent" and "New shell".
        tapWhenTappable(app.buttons["toolbar.new"].firstMatch, "New menu")
        let newAgent = app.buttons["new.agent"].firstMatch
        let newFactory = app.buttons["new.factoryProject"].firstMatch
        let newShell = app.buttons["new.shell"].firstMatch
        XCTAssertTrue(newFactory.waitForExistence(timeout: 5))
        XCTAssertTrue(newAgent.exists && newShell.exists)
        XCTAssertEqual(newFactory.label, "New factory project")
        let sheet = app.descendants(matching: .any)["factoryNewProject.sheet"].firstMatch
        tapMenuItem(app, menu: "toolbar.new", item: "new.factoryProject", until: sheet)

        // 2. Folder reuse & pre-check Start gate: existing project disabled, non-git refused,
        //    and Start stays off with `np.start.off` until checks run with no failure.
        let photosRow = app.buttons["~/code/photos, already a project: open it to give it a new goal"].firstMatch
        XCTAssertTrue(photosRow.waitForExistence(timeout: 10))
        XCTAssertFalse(photosRow.isEnabled, "existing project folder cannot be picked")

        let startBtn = app.buttons["Start the project and its agents"].firstMatch
        XCTAssertTrue(startBtn.waitForExistence(timeout: 5))
        XCTAssertFalse(startBtn.isEnabled, "Start disabled before folder and goal")

        tapWhenTappable(scrollToVisible(app, app.buttons["~/Documents/drafts, not a git repository"].firstMatch), "non-git folder")
        XCTAssertTrue(app.staticTexts["This folder is not a git repository. Pick another folder, or make it one."].waitForExistence(timeout: 5))
        XCTAssertFalse(startBtn.isEnabled)

        tapWhenTappable(scrollToVisible(app, app.buttons["~/code/notes, git repository"].firstMatch), "notes git folder")
        XCTAssertFalse(startBtn.isEnabled, "empty goal keeps Start disabled")
        let goalField = scrollToVisible(app, app.descendants(matching: .any)["factoryNewProject.goal"].firstMatch)
        typeWhenFocused(goalField, "   ")
        XCTAssertFalse(startBtn.isEnabled, "whitespace-only goal keeps Start disabled")

        typeWhenFocused(goalField, "A notes page with search")
        XCTAssertFalse(startBtn.isEnabled, "Start stays off before start checks have run")
        XCTAssertTrue(app.staticTexts["Start turns on when no check fails."].exists)
        XCTAssertEqual(stub.requests("POST /api/factory/projects/start"), 0)

        // 3. Check failure shows `ck.cant`; failing check results show details (including `ok` detail) & fixes.
        tapWhenTappable(scrollToVisible(app, app.buttons["Check this folder"].firstMatch), "Check this folder (error)")
        let checkCant = "Can't run the start checks: sheepdog did not answer in time. Check that sheepdog is running, then check again."
        XCTAssertTrue(app.staticTexts[checkCant].waitForExistence(timeout: 10))
        XCTAssertFalse(startBtn.isEnabled)

        stub.checkHandler = { _ in
            (200, #"{"results":[{"name":"git version","status":"ok","detail":"git version 2.47.1"},{"name":"start codex","status":"fail","detail":"codex is not signed in","fix":"run: codex login"},{"name":"start agy","status":"fail","detail":"agy is not signed in","fix":"run agy without arguments and sign in"},{"name":"start fetch","status":"warn","detail":"git failed to fetch origin"}]}"#)
        }
        let checkRetryBtn = app.buttons.matching(NSPredicate(format: "label == 'Check this folder' OR label == 'Check again'")).firstMatch
        tapWhenTappable(scrollToVisible(app, checkRetryBtn), "Check folder (failing results)")
        XCTAssertTrue(app.staticTexts["2 checks failed. Fix them, then check again."].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'git version 2.47.1'")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'codex is not signed in'")).firstMatch.exists)
        XCTAssertTrue(app.buttons["Copy the command codex login"].firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Fix: run agy without arguments and sign in'")).firstMatch.exists)
        XCTAssertFalse(startBtn.isEnabled, "failed start check keeps Start disabled")
        XCTAssertTrue(app.staticTexts["Start turns on when no check fails."].exists)

        // 4. Slow check (> 8 s session timeout) succeeds under 45 s budget and enables Start;
        //    Start 400 `results` disables Start again until re-checked; Start 200 opens board.
        stub.checkDelay = 9.0
        let passingChecksJSON = #"{"results":[{"name":"git version","status":"ok","detail":"git version 2.47.1"},{"name":"start codex","status":"ok","detail":"codex is signed in"},{"name":"start agy","status":"ok","detail":"agy is signed in"},{"name":"start fetch","status":"warn","detail":"git failed to fetch origin"}]}"#
        stub.checkHandler = { _ in (200, passingChecksJSON) }
        tapWhenTappable(scrollToVisible(app, app.buttons["Check again"].firstMatch), "Check again (slow > 8s)")
        XCTAssertTrue(app.staticTexts["3 pass, 1 warning. You can start."].waitForExistence(timeout: 20))
        stub.checkDelay = 0
        XCTAssertTrue(stub.checkBodies().contains { $0.contains("~/code/notes") })
        XCTAssertTrue(startBtn.isEnabled, "passing checks turn Start on")
        XCTAssertTrue(app.staticTexts["Starting runs agents on your model accounts. They use your quota."].exists)

        stub.startHandler = { _ in
            (400, #"{"error":"not starting; fix these first","results":[{"name":"git version","status":"ok","detail":"git version 2.47.1"},{"name":"start codex","status":"fail","detail":"codex is not signed in","fix":"run: codex login"}]}"#)
        }
        tapWhenTappable(scrollToVisible(app, startBtn), "Start project (400 check regression)")
        XCTAssertTrue(app.staticTexts["1 check failed. Fix it, then check again."].waitForExistence(timeout: 10))
        XCTAssertFalse(startBtn.isEnabled)
        XCTAssertTrue(app.staticTexts["Start turns on when no check fails."].exists)
        XCTAssertTrue(stub.startBodies().contains { $0.contains("~/code/notes") && $0.contains("A notes page with search") })

        stub.startHandler = { [weak stub] _ in
            stub?.factoryState = Self.startedProjects
            stub?.factoryGraphs["proj-notes"] = #"{"protocol":1,"project":"proj-notes","folder":"~/code/notes","tasks":[]}"#
            stub?.emitFactoryChanged(project: "proj-notes")
            return (200, #"{"ok":true,"device_id":"dev-1"}"#)
        }
        tapWhenTappable(scrollToVisible(app, app.buttons["Check again"].firstMatch), "Check again (passing)")
        XCTAssertTrue(app.staticTexts["3 pass, 1 warning. You can start."].waitForExistence(timeout: 10))
        XCTAssertTrue(startBtn.isEnabled)

        tapWhenTappable(scrollToVisible(app, startBtn), "Start project (passing)")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["A notes page with search"].exists)
        XCTAssertTrue(app.staticTexts["no tasks yet"].exists)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        showAgentsPane(app)
        XCTAssertTrue(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-notes"].firstMatch).exists)
    }

    func testFirstProjectIntroSpacesErrorRetryUnpairedRefusalAndFactoryUnavailable() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: "[]"))
        stub.helloFeatures = ["agent-actions"]
        stub.spacesError = (401, #"{"error":"unauthorized"}"#)
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        XCTAssertTrue(app.descendants(matching: .any)["flock.empty"].waitForExistence(timeout: 15))

        let sheet = app.descendants(matching: .any)["factoryNewProject.sheet"].firstMatch
        tapMenuItem(app, menu: "toolbar.new", item: "new.factoryProject", until: sheet)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label ==[c] 'No project yet'")).firstMatch.exists)
        let herdrUnreachable = "No folders to pick: herdr cannot be reached on Work Mac. Open herdr-app on Work Mac, then try again."
        XCTAssertTrue(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'turned this phone down'")).firstMatch.waitForExistence(timeout: 10),
            "non-502 spaces failure must show RequestFailure instead of blaming herdr"
        )
        XCTAssertFalse(app.staticTexts[herdrUnreachable].exists)

        stub.spacesError = (502, #"{"error":"herdr unreachable"}"#)
        tapWhenTappable(app.buttons["Try again"].firstMatch, "Try again spaces (502)")
        XCTAssertTrue(app.staticTexts[herdrUnreachable].waitForExistence(timeout: 10))

        stub.spacesError = nil
        stub.spacesJSON = #"{"spaces":[{"id":"w1","name":"notes","path":"~/code/notes","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]}],"folders":[],"kinds":[{"kind":"claude","name":"Claude"}],"worktrees":true,"needs_access":[]}"#
        stub.checkHandler = { _ in (200, #"{"results":[{"name":"git version","status":"ok","detail":"git 2.47"}]}"#) }
        tapWhenTappable(app.buttons["Try again"].firstMatch, "Try again spaces (200)")
        tapWhenTappable(app.buttons["~/code/notes, git repository"].firstMatch, "notes folder", timeout: 10)

        let goalField = scrollToVisible(app, app.descendants(matching: .any)["factoryNewProject.goal"].firstMatch)
        typeWhenFocused(goalField, "Build search")
        tapWhenTappable(scrollToVisible(app, app.buttons["Check this folder"].firstMatch), "Check before unpaired Start")
        XCTAssertTrue(app.staticTexts["All 1 checks pass."].waitForExistence(timeout: 10))

        stub.startHandler = { _ in (403, #"{"code":"proof_required","error":"proof_required"}"#) }
        let startBtn = app.buttons["Start the project and its agents"].firstMatch
        tapWhenTappable(scrollToVisible(app, startBtn), "Start (unpaired)")
        XCTAssertTrue(app.staticTexts["Not started: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then press Start again."].waitForExistence(timeout: 10))

        stub.factoryUnavailable = true
        tapWhenTappable(scrollToVisible(app, startBtn), "Start (factory unavailable)")
        XCTAssertTrue(app.staticTexts["The factory is not available on Work Mac"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Copy the command hc server start"].firstMatch.exists)
    }
}
