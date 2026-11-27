import XCTest

/// Parked PR 11c UI tests moved out of `ios/Tests/SheepdogUITests/FactorySettingsUITests.swift`.
/// Requires PR 11b + PR 11c (`pr11c-on-11b.patch`: `Edit <role> models` buttons, `FactoryModelsEditor`,
/// `not_launchable` refusal with `FactoryFixView` + `Copy`, multi-candidate `" then "` formatting,
/// and external-ask sheet dismissal). Uses `wireReadBack` from `tests-11b/FactorySettingsUITests.swift`.
extension FactorySettingsUITests {
    func testPerRoleModelCandidatesAndReviewerMustDifferRefusal() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1", initialPlanReviewer: "claude/sonnet/medium")
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        for role in ["coordinator", "worker", "reviewer", "retro", "plan reviewer"] {
            let editButton = scrollToVisible(app, app.buttons["Edit \(role) models"].firstMatch)
            XCTAssertTrue(editButton.exists, "VoiceOver Edit \(role) models")
            XCTAssertGreaterThanOrEqual(editButton.frame.height, 43.5, "Edit button must meet 44pt minimum height")
        }

        // 1. Back to the default on models.plan_reviewer (default "") renders "not set" in editor (Carbon §3.2).
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit plan reviewer models"].firstMatch), "Edit plan reviewer models")
        tapWhenTappable(scrollToVisible(app, app.buttons["Fill in the default plan reviewer models"].firstMatch), "Back to default plan reviewer")
        XCTAssertTrue(app.staticTexts["not set"].waitForExistence(timeout: 5), "empty candidate list after Back to the default must show 'not set'")
        tapWhenTappable(app.buttons["Cancel"].firstMatch, "Cancel plan reviewer models")

        // 2. Real not_launchable CLI refusal with fix ("run: codex login") + Copy, VoiceOver & 44pt targets (Opus #2 & #5, Carbon §2.3, §2.4.2, §3.1, §4.1).
        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit worker models"].firstMatch), "Edit worker models")
        XCTAssertEqual(app.descendants(matching: .any)["factoryModels.cli.0"].firstMatch.label, "CLI, first choice")
        XCTAssertEqual(app.descendants(matching: .any)["factoryModels.effort.0"].firstMatch.label, "effort, first choice")
        let modelField0 = app.textFields["factoryModels.model.0"].firstMatch
        XCTAssertEqual(modelField0.label, "model, first choice")
        XCTAssertFalse(app.staticTexts["model"].exists, "visible 'model' caption must be hidden from VoiceOver to avoid duplicate swipe stop")

        let codexSeg = app.descendants(matching: .any)["factoryModels.cli.0"].buttons["codex"].firstMatch
        XCTAssertGreaterThanOrEqual(codexSeg.frame.height, 43.5, "CLI choice control must meet 44pt minimum height")
        tapWhenTappable(codexSeg, "codex CLI")
        typeWhenFocused(modelField0, String(repeating: XCUIKeyboardKey.delete.rawValue, count: 10) + "gpt-6.2-sol\n")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change worker models"].firstMatch), "Change worker models (unsigned codex)")

        XCTAssertTrue(app.staticTexts["codex is not signed in"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Fix, in a terminal on Work Mac'")).firstMatch.exists, "must render app.np.fix header")
        XCTAssertTrue(app.staticTexts["codex login"].exists || app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS 'codex login'")).firstMatch.exists, "must render command fix 'codex login'")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label CONTAINS 'Copy'")).firstMatch.exists, "command fix must offer Copy button")
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'not_launchable'")).firstMatch.exists)
        XCTAssertTrue(modelField0.exists, "editor stays open on refusal")

        // 3. Reorder + multi-candidate proposal, 44pt on Move up / Remove, and "Kept at ... then ..." formatting (Carbon §2.4.2, §3.3).
        tapWhenTappable(scrollToVisible(app, app.buttons["Fill in the default worker models"].firstMatch), "Reset worker models")
        tapWhenTappable(scrollToVisible(app, app.buttons["Add a model"].firstMatch), "Add a model")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryModels.cli.1"].buttons["agy"].firstMatch), "agy CLI")
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["factoryModels.effort.1"].buttons["high"].firstMatch), "high effort")
        typeWhenFocused(scrollToVisible(app, app.textFields["factoryModels.model.1"].firstMatch), "gemini-4-neon\n")
        let moveUp = scrollToVisible(app, app.buttons["Move agy gemini-4-neon up"].firstMatch)
        let removeBtn = app.buttons["Remove agy gemini-4-neon"].firstMatch
        XCTAssertGreaterThanOrEqual(moveUp.frame.height, 43.5, "Move up button must meet 44pt minimum height")
        XCTAssertGreaterThanOrEqual(removeBtn.frame.height, 43.5, "Remove button must meet 44pt minimum height")
        tapWhenTappable(moveUp, "Move up")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change worker models"].firstMatch), "Change worker models")

        XCTAssertTrue(app.staticTexts["This project: worker models agy/gemini-4-neon/high then claude/sonnet/medium (now claude/sonnet/medium). Save?"].waitForExistence(timeout: 10))
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save worker models")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") })

        // Now worker is "agy/gemini-4-neon/high, claude/sonnet/medium" (2 candidates); Reset + Keep current formats with " then ".
        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit worker models"].firstMatch), "Edit worker models again")
        tapWhenTappable(scrollToVisible(app, app.buttons["Fill in the default worker models"].firstMatch), "Back to default worker models")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change worker models"].firstMatch), "Propose default worker models")
        tapWhenTappable(scrollToVisible(app, app.buttons["Keep current"].firstMatch), "Keep current on multi-candidate worker models")
        XCTAssertTrue(
            app.staticTexts["Kept at agy/gemini-4-neon/high then claude/sonnet/medium. Nothing changed."].waitForExistence(timeout: 10),
            "Kept at notice on models.* must join candidates with ' then ', not raw commas"
        )

        // 4. External ask arriving for the row open in FactoryModelsEditor closes the editor (Carbon §3.3).
        scrollToTop(app)
        tapWhenTappable(scrollToVisible(app, app.buttons["Edit worker models"].firstMatch), "Open worker editor before external ask")
        XCTAssertTrue(app.textFields["factoryModels.model.0"].waitForExistence(timeout: 5))
        let extAsk = #"{"id":"ask-ext-1","project":"proj-1","kind":"rule","gated":true,"question":"This project: worker models claude/opus/medium (now agy/gemini-4-neon/high then claude/sonnet/medium). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"models.worker","value":"claude/opus/medium","was":"agy/gemini-4-neon/high, claude/sonnet/medium"},"asked_by":"engine","at":"2026-11-26T09:05:00Z","status":"open"}"#
        let extCard = #"{"id":"fask-ask-ext-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"This project: worker models claude/opus/medium (now agy/gemini-4-neon/high then claude/sonnet/medium). Save?","summary":"Save?","factory":{"project":"proj-1","kind":"rule"},"options":[{"index":1,"label":"Save","default":true,"recommended":true},{"index":2,"label":"Keep current"}]}"#
        stub.factoryAsks = #"{"protocol":1,"asks":[\#(extAsk)]}"#
        stub.questionsState = "[\(extCard)]"
        stub.pendingActionState = extCard
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(app.textFields["factoryModels.model.0"].waitForNonExistence(timeout: 10), "external open ask on edited models row must close FactoryModelsEditor")
    }
}
