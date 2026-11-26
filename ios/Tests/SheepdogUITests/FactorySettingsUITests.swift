import XCTest

/// UI tests for app-factory PR 11a against `StubDaemon`: read-only settings screen from
/// `FactoryProjectView`, top rows & "All settings (N)" VoiceOver fold, chips & notes,
/// stored-value problem alerts, live `factory.changed` reloads, finished projects (D11),
/// and load error / retry / empty states.
final class FactorySettingsUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    static let runningNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1,"done":2},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
    static let emptyGraph = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[]}"#

    static func settingsJSON(
        maxRunning: String = "3",
        worker: String = "claude/sonnet/medium",
        planReviewer: String = "",
        maxRounds: String = "3",
        taskSizeLimit: String = "400"
    ) -> String {
        #"{"protocol":1,"rows":[{"name":"models.coordinator","label":"coordinator models","value":"claude/opus/medium","default":"claude/opus/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},"#
            + #"{"name":"models.worker","label":"worker models","value":"\#(worker)","default":"claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"models":{"agy":["gemini-4-carbon","gemini-4-neon"]},"top":true},"#
            + #"{"name":"models.reviewer","label":"reviewer models","value":"claude/opus/medium, claude/sonnet/medium","default":"claude/opus/medium, claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},"#
            + #"{"name":"models.retro","label":"retro models","value":"claude/opus/high","default":"claude/opus/high","min":0,"max":0,"clis":["claude","agy"],"top":true},"#
            + #"{"name":"models.plan_reviewer","label":"plan reviewer models","value":"\#(planReviewer)","default":"","min":0,"max":0,"clis":["claude","agy"],"top":true},"#
            + #"{"name":"max_running","label":"max running","value":"\#(maxRunning)","default":"3","min":1,"max":20,"top":true},"#
            + #"{"name":"review_rounds","label":"review rounds","value":"2","default":"2","min":1,"max":5},"#
            + #"{"name":"max_rounds","label":"max rounds","value":"\#(maxRounds)","default":"5","min":2,"max":10},"#
            + #"{"name":"task_size_limit","label":"task size limit","value":"\#(taskSizeLimit)","default":"400","min":50,"max":2000},"#
            + #"{"name":"second_reviewer","label":"second reviewer","value":"opus","default":"opus","min":0,"max":0,"allowed":["opus","codex"],"note":"not used while reviewer models are set"},"#
            + #"{"name":"dispatch_workflow","label":"dispatch workflow","value":"","default":"","min":0,"max":0}]}"#
    }

    func testSettingsListsValuesMetadataAndAllSettingsFold() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.enableEvents = true
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        stub.factorySettings["proj-1"] = Self.settingsJSON()
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        XCTAssertTrue(app.staticTexts["For this project only."].waitForExistence(timeout: 10))
        let maxRunRow = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.max_running"].firstMatch)
        XCTAssertTrue(maxRunRow.exists)
        XCTAssertTrue(maxRunRow.staticTexts["3"].exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'default 3 · allowed 1–20'")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'not set'")).firstMatch.exists, "empty value shows not set")
        XCTAssertFalse(app.staticTexts["max_running"].exists || app.staticTexts["models.worker"].exists, "raw setting keys must never appear")

        // All settings fold chevron is hidden from VoiceOver; expanding reveals folded rows and note chip.
        let allFold = scrollToVisible(app, app.buttons["factorySettings.allFold"].firstMatch)
        XCTAssertEqual(allFold.label.lowercased(), "all settings (5)", "decorative chevron must not pollute All settings VoiceOver label")
        XCTAssertEqual(allFold.value as? String, "collapsed")
        tapWhenTappable(allFold, "All settings")
        XCTAssertEqual(allFold.value as? String, "expanded")

        let secondRevRow = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.second_reviewer"].firstMatch)
        XCTAssertTrue(secondRevRow.exists)
        XCTAssertTrue(secondRevRow.staticTexts["not used"].exists)
        XCTAssertTrue(secondRevRow.staticTexts["not used while reviewer models are set"].exists)

        // Transient reload failure preserves already-loaded rows; subsequent factory.changed updates live.
        stub.factorySettingsErrors["proj-1"] = (503, #"{"error":"herdr busy"}"#)
        stub.emitFactoryChanged(project: "proj-1")
        usleep(400_000)
        XCTAssertFalse(app.staticTexts["Can't load the settings. Trying again."].exists, "transient reload failure must keep loaded rows")

        stub.factorySettingsErrors["proj-1"] = nil
        stub.factorySettings["proj-1"] = Self.settingsJSON(maxRunning: "5")
        stub.emitFactoryChanged(project: "proj-1")
        scrollToTop(app)
        let updatedMaxRun = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.max_running"].firstMatch)
        XCTAssertTrue(updatedMaxRun.staticTexts["changed"].waitForExistence(timeout: 10), "factory.changed reload must show changed chip")
    }

    func testFinishedProjectSettingsProblemErrorAndEmptyStates() throws {
        let heads = #"[{"id":"proj-fin","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":8},"folder":"~/code/recipes"},{"id":"proj-err","name":"broken","goal":"Fix builds","state":"running","counts":{"working":1},"folder":"~/code/broken"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-fin"] = #"{"protocol":1,"project":"proj-fin","folder":"~/code/recipes","tasks":[]}"#
        stub.factoryGraphs["proj-err"] = #"{"protocol":1,"project":"proj-err","folder":"~/code/broken","tasks":[]}"#
        stub.factorySettings["proj-fin"] = #"{"protocol":1,"rows":[{"name":"max_running","label":"max running","value":"5","default":"3","min":1,"max":20,"top":true},{"name":"ci_stalled_minutes","label":"ci stalled minutes","value":"0","default":"90","min":10,"max":600,"problem":"ci stalled minutes 0: allowed 10–600","top":true}]}"#
        stub.factorySettingsErrors["proj-err"] = (503, #"{"error":"herdr busy"}"#)
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)

        // Finished project opens Settings and renders changed & stored-problem states.
        tapWhenTappable(scrollToVisible(app, app.buttons["flock.section.done"].firstMatch), "Done fold")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-fin"].firstMatch), "recipes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings on finished project")
        let finMaxRun = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.max_running"].firstMatch)
        XCTAssertTrue(finMaxRun.waitForExistence(timeout: 10))
        XCTAssertTrue(finMaxRun.staticTexts["changed"].exists)
        let stalledRow = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.ci_stalled_minutes"].firstMatch)
        XCTAssertTrue(stalledRow.staticTexts["not allowed"].exists)
        XCTAssertTrue(stalledRow.staticTexts["The stored value 0 is not allowed (allowed 10–600). Change it before the next start."].exists)

        // Error state, Retry, and empty state.
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
        XCTAssertTrue(app.staticTexts["No settings available."].waitForExistence(timeout: 5))
    }
}
