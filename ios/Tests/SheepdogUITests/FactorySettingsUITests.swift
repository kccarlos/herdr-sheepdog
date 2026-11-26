import XCTest

/// Failing UI tests for app-factory PR 11 against `StubDaemon`: settings list,
/// read-back ask proposal and confirmation through the existing card path,
/// per-role model candidates & reviewer-must-differ refusal, finished projects
/// editable (D11), error/empty states, and VoiceOver labels.
final class FactorySettingsUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    private func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    private static let runningNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1,"done":2},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
    private static let emptyGraph = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[]}"#

    private static func settingsJSON(maxRunning: String = "3", worker: String = "claude/sonnet/medium") -> String {
        #"{"protocol":1,"rows":[{"name":"models.coordinator","label":"coordinator models","value":"claude/opus/medium","default":"claude/opus/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},"#
            + #"{"name":"models.worker","label":"worker models","value":"\#(worker)","default":"claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"models":{"agy":["gemini-4-carbon","gemini-4-neon"]},"top":true},"#
            + #"{"name":"models.reviewer","label":"reviewer models","value":"claude/opus/medium, claude/sonnet/medium","default":"claude/opus/medium, claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},"#
            + #"{"name":"models.retro","label":"retro models","value":"claude/opus/high","default":"claude/opus/high","min":0,"max":0,"clis":["claude","agy"],"top":true},"#
            + #"{"name":"models.plan_reviewer","label":"plan reviewer models","value":"","default":"","min":0,"max":0,"clis":["claude","agy"],"top":true},"#
            + #"{"name":"max_running","label":"max running","value":"\#(maxRunning)","default":"3","min":1,"max":20,"top":true},"#
            + #"{"name":"review_rounds","label":"review rounds","value":"2","default":"2","min":1,"max":5},"#
            + #"{"name":"second_reviewer","label":"second reviewer","value":"opus","default":"opus","min":0,"max":0,"allowed":["opus","codex"],"note":"not used while reviewer models are set"}]}"#
    }

    private func wireReadBack(_ stub: StubDaemon, project: String, projectName: String = "notes") {
        var seq = 0, maxRunning = "3", worker = "claude/sonnet/medium"
        var openAsk: (id: String, name: String, value: String)?
        stub.enableEvents = true
        stub.factorySettings[project] = Self.settingsJSON(maxRunning: maxRunning, worker: worker)
        stub.proposeSettingHandler = { [weak stub] proj, body in
            guard let stub, let obj = (try? JSONSerialization.jsonObject(with: Data(body.utf8))) as? [String: String],
                  let name = obj["name"], let value = obj["value"] else { return (400, #"{"code":"bad_request","error":"bad body"}"#) }
            if name == "review_rounds", value == "6" {
                return (400, #"{"code":"out_of_range","error":"review rounds 6 does not fit: max rounds is 5 (allowed 6–10). Raise max rounds first."}"#)
            }
            if name == "models.reviewer", value == "claude/sonnet/medium" {
                return (400, #"{"code":"not_launchable","error":"the reviewer must differ from the worker: the worker models are claude/sonnet/medium"}"#)
            }
            seq += 1
            let askID = "ask-rule-\(seq)", was = name == "models.worker" ? worker : maxRunning
            let label = name == "models.worker" ? "worker models" : "max running"
            let shown = name == "models.worker" ? value.replacingOccurrences(of: ", ", with: " then ") : value
            let q = "This project: \(label) \(shown) (now \(was)). Save?"
            let ask = #"{"id":"\#(askID)","project":"\#(proj)","kind":"rule","gated":true,"question":"\#(q)","options":["Save","Keep current"],"recommend":"Save","set":{"name":"\#(name)","value":"\#(value)","was":"\#(was)"},"asked_by":"engine","at":"2026-11-26T09:00:00Z","status":"open"}"#
            let card = #"{"id":"fask-\#(askID)","pane_id":"factory:\#(proj)","kind":"answer_prompt","agent":"\#(projectName)","question":"\#(q)","summary":"\#(q)","factory":{"project":"\#(proj)","kind":"rule"},"options":[{"index":1,"label":"Save","default":true,"recommended":true},{"index":2,"label":"Keep current"}]}"#
            openAsk = (askID, name, value)
            stub.factoryAsks = #"{"protocol":1,"asks":[\#(ask)]}"#
            stub.questionsState = "[\(card)]"; stub.pendingActionState = card
            stub.emitFactoryChanged(project: proj)
            return (200, #"{"ok":true,"ask":\#(ask)}"#)
        }
        stub.answerHandler = { [weak stub] body in
            guard let stub, let cur = openAsk else { return (409, #"{"error":"that question is no longer open"}"#) }
            if body.contains("\"option\":1") || body.contains("\"option\": 1") {
                if cur.name == "models.worker" { worker = cur.value } else { maxRunning = cur.value }
                stub.factorySettings[project] = Self.settingsJSON(maxRunning: maxRunning, worker: worker)
            }
            openAsk = nil; stub.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub.questionsState = "[]"; stub.pendingActionState = nil
            stub.emitFactoryChanged(project: project)
            return (200, #"{"ok":true,"spoken":"Answered Save."}"#)
        }
    }

    func testSettingsListsValuesProposesReadBackAndConfirmsThroughCard() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        XCTAssertTrue(app.staticTexts["For this project only. A change is read back to you and saved when you confirm."].waitForExistence(timeout: 10))
        XCTAssertTrue(app.descendants(matching: .any)["factorySettings.row.max_running"].firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'default 3 · allowed 1–20'")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'not set'")).firstMatch.exists, "empty value shows not set")
        XCTAssertFalse(app.staticTexts["max_running"].exists || app.staticTexts["models.worker"].exists, "raw setting keys must never appear")

        tapWhenTappable(scrollToVisible(app, app.buttons["factorySettings.increment.max_running"].firstMatch), "+ max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        XCTAssertTrue(app.staticTexts["This project: max running 4 (now 3). Save?"].waitForExistence(timeout: 10))
        XCTAssertEqual(stub.requests("POST /api/pending/answer"), 0, "proposing must never answer or apply directly")
        XCTAssertTrue(stub.proposeBodies().contains { $0.project == "proj-1" && $0.body.contains("max_running") && $0.body.contains("\"4\"") })

        tapWhenTappable(scrollToVisible(app, app.buttons["Keep current"].firstMatch), "Keep current")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") && $0.contains("2") })

        tapWhenTappable(scrollToVisible(app, app.buttons["factorySettings.increment.max_running"].firstMatch), "+ max running again")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running again")
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save (Recommended)")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-2") && $0.contains("1") })
        XCTAssertTrue(scrollToVisible(app, app.buttons["Set max running back to the default, 3"].firstMatch).waitForExistence(timeout: 10))

        tapWhenTappable(scrollToVisible(app, app.buttons["factorySettings.allFold"].firstMatch), "All settings")
        for _ in 0 ..< 4 { tapWhenTappable(scrollToVisible(app, app.buttons["factorySettings.increment.review_rounds"].firstMatch), "+ review rounds") }
        tapWhenTappable(scrollToVisible(app, app.buttons["Change review rounds"].firstMatch), "Change review rounds")
        XCTAssertTrue(app.staticTexts["review rounds 6 does not fit: max rounds is 5 (allowed 6–10). Raise max rounds first."].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'out_of_range'")).firstMatch.exists)
    }

    func testPerRoleModelCandidatesAndReviewerMustDifferRefusal() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        for role in ["coordinator", "worker", "reviewer", "retro"] {
            XCTAssertTrue(scrollToVisible(app, app.buttons["Edit \(role) models"].firstMatch).exists, "VoiceOver Edit \(role) models")
        }

        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit reviewer models"].firstMatch), "Edit reviewer models")
        tapWhenTappable(scrollToVisible(app, app.buttons["Remove claude opus"].firstMatch), "Remove claude opus")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change reviewer models"].firstMatch), "Change reviewer models")
        XCTAssertTrue(app.staticTexts["the reviewer must differ from the worker: the worker models are claude/sonnet/medium"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'not_launchable'")).firstMatch.exists)
        XCTAssertTrue(app.textFields["factoryModels.model.0"].exists, "editor stays open on refusal")
        tapWhenTappable(app.buttons["Cancel"].firstMatch, "Cancel reviewer models")

        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit worker models"].firstMatch), "Edit worker models")
        XCTAssertEqual(app.descendants(matching: .any)["factoryModels.cli.0"].firstMatch.label, "CLI, first choice")
        XCTAssertEqual(app.descendants(matching: .any)["factoryModels.effort.0"].firstMatch.label, "effort, first choice")
        tapWhenTappable(scrollToVisible(app, app.buttons["Add a model"].firstMatch), "Add a model")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryModels.cli.1"].buttons["agy"].firstMatch), "agy CLI")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryModels.effort.1"].buttons["high"].firstMatch), "high effort")
        typeWhenFocused(scrollToVisible(app, app.textFields["factoryModels.model.1"].firstMatch), "gemini-4-neon\n")
        tapWhenTappable(scrollToVisible(app, app.buttons["Move agy gemini-4-neon up"].firstMatch), "Move up")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change worker models"].firstMatch), "Change worker models")

        XCTAssertTrue(app.staticTexts["This project: worker models agy/gemini-4-neon/high then claude/sonnet/medium (now claude/sonnet/medium). Save?"].waitForExistence(timeout: 10))
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save worker models")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") && $0.contains("1") })
    }

    func testFinishedProjectSettingsStayEditableAndErrorEmptyStates() throws {
        let heads = #"[{"id":"proj-fin","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":8},"folder":"~/code/recipes"},{"id":"proj-err","name":"broken","goal":"Fix builds","state":"running","counts":{"working":1},"folder":"~/code/broken"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-fin"] = #"{"protocol":1,"project":"proj-fin","folder":"~/code/recipes","tasks":[]}"#
        stub.factoryGraphs["proj-err"] = #"{"protocol":1,"project":"proj-err","folder":"~/code/broken","tasks":[]}"#
        stub.factorySettingsErrors["proj-err"] = (503, #"{"error":"herdr busy"}"#)
        wireReadBack(stub, project: "proj-fin", projectName: "recipes")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)

        tapWhenTappable(scrollToVisible(app, app.buttons["flock.section.done"].firstMatch), "Done fold")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-fin"].firstMatch), "recipes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings on finished project")
        tapWhenTappable(scrollToVisible(app, app.buttons["factorySettings.increment.max_running"].firstMatch), "+ max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save on finished project")
        XCTAssertTrue(stub.proposeBodies().contains { $0.project == "proj-fin" && $0.body.contains("max_running") })
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") && $0.contains("1") })

        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-err"].firstMatch), "broken head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings on broken")
        let loadErr = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Can't load the settings. Trying again.")).firstMatch
        XCTAssertTrue(loadErr.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'still works.'")).firstMatch.exists)

        stub.factorySettingsErrors["proj-err"] = nil
        stub.factorySettings["proj-err"] = #"{"protocol":1,"rows":[]}"#
        tapWhenTappable(app.buttons["factorySettings.retry"].firstMatch, "Retry settings")
        XCTAssertTrue(loadErr.waitForNonExistence(timeout: 10))
    }
}
