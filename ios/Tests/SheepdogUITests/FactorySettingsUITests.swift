import XCTest

/// UI tests for app-factory PR 11 against `StubDaemon`: settings list, read-back ask
/// proposal & confirmation, answer phase safety (no false "Saved" on 409/in-flight),
/// Save refusal formatting (`rb.moved`, `proof_required`), per-role model candidates
/// & real `not_launchable` refusals with `fix` + `Copy`, range clamping & editable
/// wide-range numbers, 44pt hit targets, VoiceOver labels, and D11 finished projects.
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

    private static func settingsJSON(
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

    private func wireReadBack(
        _ stub: StubDaemon,
        project: String,
        projectName: String = "notes",
        initialPlanReviewer: String = ""
    ) {
        var seq = 0, maxRunning = "3", worker = "claude/sonnet/medium", planReviewer = initialPlanReviewer, taskSizeLimit = "400"
        var openAsk: (id: String, name: String, value: String, was: String)?
        stub.enableEvents = true
        stub.factorySettings[project] = Self.settingsJSON(maxRunning: maxRunning, worker: worker, planReviewer: planReviewer, taskSizeLimit: taskSizeLimit)
        stub.proposeSettingHandler = { [weak stub] proj, body in
            guard let stub, let obj = (try? JSONSerialization.jsonObject(with: Data(body.utf8))) as? [String: String],
                  let name = obj["name"], let value = obj["value"] else { return (400, #"{"code":"bad_request","error":"bad body"}"#) }
            // Real Go daemon ProposeSetting refusals (Opus #5, Carbon §2.3 & §4.1).
            if name == "review_rounds", value == "4" {
                return (400, #"{"code":"out_of_range","error":"review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."}"#)
            }
            if name == "max_running", value == maxRunning {
                return (400, #"{"code":"unchanged","error":"max running is already \#(maxRunning)"}"#)
            }
            if name.hasPrefix("models."), value.contains("codex/") {
                return (400, #"{"code":"not_launchable","error":"codex is not signed in","fix":"run: codex login"}"#)
            }
            if name == "models.worker", value.contains("gemini-4-neo/") {
                return (400, #"{"code":"not_launchable","error":"agy does not offer gemini-4-neo. It offers: gemini-4-carbon, gemini-4-neon."}"#)
            }
            seq += 1
            let askID = "ask-rule-\(seq)"
            let was: String = switch name {
            case "models.worker": worker
            case "models.reviewer": "claude/opus/medium, claude/sonnet/medium"
            case "models.plan_reviewer": planReviewer
            case "task_size_limit": taskSizeLimit
            case "review_rounds": "2"
            default: maxRunning
            }
            let label = name.hasPrefix("models.") ? name.dropFirst("models.".count).replacingOccurrences(of: "_", with: " ") + " models" : name.replacingOccurrences(of: "_", with: " ")
            let shown = name.hasPrefix("models.") ? (value.isEmpty ? "empty" : value.replacingOccurrences(of: ", ", with: " then ")) : value
            let shownWas = name.hasPrefix("models.") ? (was.isEmpty ? "empty" : was.replacingOccurrences(of: ", ", with: " then ")) : was
            let q = "This project: \(label) \(shown) (now \(shownWas)). Save?"
            let ask = #"{"id":"\#(askID)","project":"\#(proj)","kind":"rule","gated":true,"question":"\#(q)","options":["Save","Keep current"],"recommend":"Save","set":{"name":"\#(name)","value":"\#(value)","was":"\#(was)"},"asked_by":"engine","at":"2026-11-26T09:00:00Z","status":"open"}"#
            let card = #"{"id":"fask-\#(askID)","pane_id":"factory:\#(proj)","kind":"answer_prompt","agent":"\#(projectName)","question":"\#(q)","summary":"\#(q)","factory":{"project":"\#(proj)","kind":"rule"},"options":[{"index":1,"label":"Save","default":true,"recommended":true},{"index":2,"label":"Keep current"}]}"#
            openAsk = (askID, name, value, was)
            stub.factoryAsks = #"{"protocol":1,"asks":[\#(ask)]}"#
            stub.questionsState = "[\(card)]"; stub.pendingActionState = card
            stub.emitFactoryChanged(project: proj)
            return (200, #"{"ok":true,"ask":\#(ask)}"#)
        }
        stub.answerHandler = { [weak stub] body in
            guard let stub, let cur = openAsk else { return (409, #"{"error":"that question is no longer open"}"#) }
            let choseSave = body.contains("\"option\":1") || body.contains("\"option\": 1") || body.contains("\"Save\"")
            if choseSave {
                switch cur.name {
                case "models.worker": worker = cur.value
                case "models.plan_reviewer": planReviewer = cur.value
                case "task_size_limit": taskSizeLimit = cur.value
                default: maxRunning = cur.value
                }
                stub.factorySettings[project] = Self.settingsJSON(maxRunning: maxRunning, worker: worker, planReviewer: planReviewer, taskSizeLimit: taskSizeLimit)
            }
            openAsk = nil; stub.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub.questionsState = "[]"; stub.pendingActionState = nil
            stub.emitFactoryChanged(project: project)
            return (200, #"{"ok":true,"spoken":"Answered Save."}"#)
        }
    }

    private func incrementButton(_ app: XCUIApplication, name: String, label: String) -> XCUIElement {
        let byID = app.buttons["factorySettings.increment.\(name)"].firstMatch
        if byID.exists { return byID }
        let byLabel = app.buttons["One more \(label)"].firstMatch
        if byLabel.exists { return byLabel }
        return app.descendants(matching: .any)["factorySettings.row.\(name)"].steppers.buttons["Increment"].firstMatch
    }

    private func decrementButton(_ app: XCUIApplication, name: String, label: String) -> XCUIElement {
        let byID = app.buttons["factorySettings.decrement.\(name)"].firstMatch
        if byID.exists { return byID }
        let byLabel = app.buttons["One fewer \(label)"].firstMatch
        if byLabel.exists { return byLabel }
        return app.descendants(matching: .any)["factorySettings.row.\(name)"].steppers.buttons["Decrement"].firstMatch
    }

    func testSettingsListsValuesProposesReadBackClampsRangesAndExpiresSavedNotice() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        XCTAssertTrue(app.staticTexts["For this project only. A change is read back to you and saved when you confirm."].waitForExistence(timeout: 10))
        let maxRunRow = scrollToVisible(app, app.descendants(matching: .any)["factorySettings.row.max_running"].firstMatch)
        XCTAssertTrue(maxRunRow.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'default 3 · allowed 1–20'")).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'not set'")).firstMatch.exists, "empty value shows not set")
        XCTAssertFalse(app.staticTexts["max_running"].exists || app.staticTexts["models.worker"].exists, "raw setting keys must never appear")

        // 1. 44x44pt minimum tap targets & VoiceOver value+label on numeric row (Opus #4, Carbon §2.4.2).
        let editWorker = scrollToVisible(app, app.buttons["Edit worker models"].firstMatch)
        XCTAssertGreaterThanOrEqual(editWorker.frame.height, 43.5, "Edit button must meet 44pt minimum height")
        let plusMaxRun = scrollToVisible(app, incrementButton(app, name: "max_running", label: "max running"))
        let minusMaxRun = decrementButton(app, name: "max_running", label: "max running")
        XCTAssertGreaterThanOrEqual(plusMaxRun.frame.width, 43.5, "+ button must meet 44pt minimum width")
        XCTAssertGreaterThanOrEqual(plusMaxRun.frame.height, 43.5, "+ button must meet 44pt minimum height")
        XCTAssertGreaterThanOrEqual(minusMaxRun.frame.width, 43.5, "− button must meet 44pt minimum width")
        XCTAssertGreaterThanOrEqual(minusMaxRun.frame.height, 43.5, "− button must meet 44pt minimum height")

        let maxRunField = app.textFields["max running"].firstMatch
        let maxRunStepper = app.steppers["max running"].firstMatch
        XCTAssertTrue(
            (maxRunField.exists && (maxRunField.value as? String) == "3")
                || (maxRunStepper.exists && (maxRunStepper.value as? String)?.contains("3") == true),
            "VoiceOver must expose a control labelled 'max running' together with its current value '3'"
        )

        // 2. Clamp at row.min: stepping max_running (min: 1) down 3 times from 3 stops at 1, never 0 (Opus #4).
        for _ in 0 ..< 3 { tapWhenTappable(minusMaxRun, "− max running") }
        XCTAssertFalse(maxRunRow.staticTexts["0"].exists || (maxRunField.value as? String) == "0", "max_running (min 1) must clamp at 1, never 0")
        for _ in 0 ..< 2 { tapWhenTappable(plusMaxRun, "+ max running back to 3") }
        XCTAssertFalse(app.buttons["Change max running"].exists, "returning to stored value 3 clears dirty state")

        // 3. Read-back ask proposal, Keep current, Save, and 5-second rb.saved expiry (Opus Should-fix #2).
        tapWhenTappable(plusMaxRun, "+ max running to 4")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        XCTAssertTrue(app.staticTexts["This project: max running 4 (now 3). Save?"].waitForExistence(timeout: 10))
        XCTAssertEqual(stub.requests("POST /api/pending/answer"), 0, "proposing must never answer or apply directly")
        XCTAssertTrue(stub.proposeBodies().contains { $0.project == "proj-1" && $0.body.contains("max_running") && $0.body.contains("\"4\"") })

        tapWhenTappable(scrollToVisible(app, app.buttons["Keep current"].firstMatch), "Keep current")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") })

        tapWhenTappable(scrollToVisible(app, incrementButton(app, name: "max_running", label: "max running")), "+ max running again")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running again")
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save (Recommended)")
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-2") })
        let savedNotice = app.staticTexts["Saved. The engine uses it from now on."].firstMatch
        XCTAssertTrue(savedNotice.waitForExistence(timeout: 5))
        XCTAssertTrue(scrollToVisible(app, app.buttons["Set max running back to the default, 3"].firstMatch).exists)
        XCTAssertTrue(savedNotice.waitForNonExistence(timeout: 7), "rb.saved notice must clear after 5 s")

        // 4. All settings fold chevron hidden from VoiceOver, free-text label, direct wide-range typing, and max clamping.
        let allFold = scrollToVisible(app, app.buttons["factorySettings.allFold"].firstMatch)
        XCTAssertEqual(allFold.label.lowercased(), "all settings (5)", "decorative chevron must not pollute All settings VoiceOver label")
        tapWhenTappable(allFold, "All settings")

        let workflowField = scrollToVisible(app, app.textFields["dispatch workflow"].firstMatch)
        XCTAssertTrue(workflowField.exists, "free-text TextField('not set') must carry .accessibilityLabel(row.label)")

        let sizeField = scrollToVisible(app, app.textFields["task size limit"].firstMatch)
        XCTAssertTrue(sizeField.exists, "wide-range task_size_limit (50–2000) must expose an editable TextField")
        typeWhenFocused(sizeField, String(repeating: XCUIKeyboardKey.delete.rawValue, count: 4) + "800\n")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change task size limit"].firstMatch), "Change task size limit")
        XCTAssertTrue(stub.proposeBodies().contains { $0.project == "proj-1" && $0.body.contains("task_size_limit") && $0.body.contains("\"800\"") })
        tapWhenTappable(scrollToVisible(app, app.buttons["Keep current"].firstMatch), "Keep current on task_size_limit")

        // Real cross-setting refusal within review_rounds 1–5 (max_rounds is 3, review_rounds stepped 2 -> 4), then clamp at max 5.
        let plusReview = scrollToVisible(app, incrementButton(app, name: "review_rounds", label: "review rounds"))
        for _ in 0 ..< 2 { tapWhenTappable(plusReview, "+ review rounds to 4") }
        tapWhenTappable(scrollToVisible(app, app.buttons["Change review rounds"].firstMatch), "Change review rounds")
        XCTAssertTrue(app.staticTexts["review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'out_of_range'")).firstMatch.exists)

        for _ in 0 ..< 3 { tapWhenTappable(plusReview, "+ review rounds past max 5") }
        let reviewRow = app.descendants(matching: .any)["factorySettings.row.review_rounds"].firstMatch
        let reviewField = app.textFields["review rounds"].firstMatch
        XCTAssertFalse(reviewRow.staticTexts["6"].exists || (reviewField.value as? String) == "6", "review_rounds (max 5) must clamp at 5, never 6")
    }

    func testPerRoleModelCandidatesAndReviewerMustDifferRefusal() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1", initialPlanReviewer: "claude/sonnet/medium")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        for role in ["coordinator", "worker", "reviewer", "retro", "plan reviewer"] {
            XCTAssertTrue(scrollToVisible(app, app.buttons["Edit \(role) models"].firstMatch).exists, "VoiceOver Edit \(role) models")
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
        stub.questionsState = "[\(extCard)]"; stub.pendingActionState = extCard
        stub.emitFactoryChanged(project: "proj-1")
        XCTAssertTrue(app.textFields["factoryModels.model.0"].waitForNonExistence(timeout: 10), "external open ask on edited models row must close FactoryModelsEditor")
    }

    /// Fix-round (Opus #3, Carbon §2.1 & §2.2):
    /// - Duplicate in-flight tap (`computer.answer` -> `nil`) and `409` `.gone` never announce `"Saved…"`.
    /// - `Save` refusals (`SE4` `out_of_range` and `403 proof_required`) format cleanly as `rb.moved` /
    ///   `app.pair` without leaking `"The daemon refused"` or raw refusal codes, keeping the ask open.
    func testAnswerPhasesNeverFalselyAnnounceSavedAndFormatSaveRefusals() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1")
        let host = try stub.start(); defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        tapWhenTappable(scrollToVisible(app, app.buttons["factoryProject.settings"].firstMatch), "Settings")

        // 1. Propose max_running = 4, then test SE4 out_of_range refusal on Save (Carbon §2.2).
        tapWhenTappable(scrollToVisible(app, incrementButton(app, name: "max_running", label: "max running")), "+ max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        let saveBtn = scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch)
        XCTAssertTrue(saveBtn.waitForExistence(timeout: 10))

        stub.answerHandler = { _ in
            (403, #"{"error":"out_of_range: max running 4 does not fit: review rounds is 5 (allowed 1–3). Raise review rounds first."}"#)
        }
        stub.askAnswerHandler = { _, _ in
            (400, #"{"code":"out_of_range","error":"max running 4 does not fit: review rounds is 5 (allowed 1–3). Raise review rounds first."}"#)
        }
        tapWhenTappable(saveBtn, "Save (out_of_range)")
        let movedRefusal = app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS 'Not saved: another setting changed after you asked.'")
        ).firstMatch
        XCTAssertTrue(movedRefusal.waitForExistence(timeout: 10), "SE4 out_of_range on Save must show rb.moved")
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'out_of_range' OR label CONTAINS 'Saved.'")).firstMatch.exists)
        XCTAssertTrue(saveBtn.exists, "question stays open after rb.moved refusal")

        // 2. proof_required on Save maps to app.pair without raw code or "The daemon refused" (Carbon §2.2).
        stub.answerHandler = { _ in
            (403, #"{"error":"proof_required: owner asks require a paired device session"}"#)
        }
        stub.askAnswerHandler = { _, _ in
            (403, #"{"code":"proof_required","error":"proof_required"}"#)
        }
        tapWhenTappable(saveBtn, "Save (proof_required)")
        let pairRefusal = app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS 'Work Mac no longer knows this phone'")
        ).firstMatch
        XCTAssertTrue(pairRefusal.waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'The daemon refused' OR label CONTAINS 'proof_required' OR label CONTAINS 'Saved.'")).firstMatch.exists)

        // 3. In-flight duplicate tap + 409 Conflict (.gone) must NEVER flash or show "Saved…" (Opus #3, Carbon §2.1).
        stub.postDelay = 1.5
        stub.answerHandler = { [weak stub] _ in
            stub?.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub?.questionsState = "[]"; stub?.pendingActionState = nil
            return (409, #"{"error":"that question is no longer open"}"#)
        }
        stub.askAnswerHandler = { [weak stub] _, _ in
            stub?.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub?.questionsState = "[]"; stub?.pendingActionState = nil
            return (409, #"{"code":"closed","error":"Ask \"ask-rule-1\" is already cancelled."}"#)
        }
        tapWhenTappable(saveBtn, "Save first tap (delayed 409)")
        // Rapid second tap while the first answer is still in flight (`computer.answer` returns `nil`).
        if saveBtn.exists { saveBtn.tap() }
        usleep(300_000)
        XCTAssertFalse(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Saved.' OR label CONTAINS 'Kept at'")).firstMatch.exists,
            "duplicate tap while answer is in flight must never flash 'Saved…'"
        )
        // Wait for the delayed 409 reply to finish and settle.
        usleep(1_600_000)
        stub.postDelay = 0
        XCTAssertFalse(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Saved.' OR label CONTAINS 'Kept at'")).firstMatch.exists,
            "409 Conflict (.gone) must never claim 'Saved…'"
        )
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
        tapWhenTappable(scrollToVisible(app, incrementButton(app, name: "max_running", label: "max running")), "+ max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Change max running"].firstMatch), "Change max running")
        tapWhenTappable(scrollToVisible(app, app.buttons["Save (Recommended)"].firstMatch), "Save on finished project")
        XCTAssertTrue(stub.proposeBodies().contains { $0.project == "proj-fin" && $0.body.contains("max_running") })
        XCTAssertTrue(stub.answerBodies().contains { $0.contains("fask-ask-rule-1") })

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
