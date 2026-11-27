import XCTest

/// Parked PR 11b UI tests and helpers moved out of `ios/Tests/SheepdogUITests/FactorySettingsUITests.swift`.
/// Requires PR 11b (`pr11b-on-11a.patch`: non-model editing, read-back ask flow, answer phase safety,
/// Save refusal formatting, and finished-project editing).
/// Note: when merging into `FactorySettingsUITests.swift` for PR 11b, update 11a's intro-line check
/// (`"For this project only."`) to the 11b copy checked below.
extension FactorySettingsUITests {
    func wireReadBack(
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
            stub.questionsState = "[\(card)]"
            stub.pendingActionState = card
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
            openAsk = nil
            stub.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub.questionsState = "[]"
            stub.pendingActionState = nil
            stub.emitFactoryChanged(project: project)
            return (200, #"{"ok":true,"spoken":"Answered Save."}"#)
        }
    }

    func incrementButton(_ app: XCUIApplication, name: String, label: String) -> XCUIElement {
        let byID = app.buttons["factorySettings.increment.\(name)"].firstMatch
        if byID.exists { return byID }
        let byLabel = app.buttons["One more \(label)"].firstMatch
        if byLabel.exists { return byLabel }
        return app.descendants(matching: .any)["factorySettings.row.\(name)"].steppers.buttons["Increment"].firstMatch
    }

    func decrementButton(_ app: XCUIApplication, name: String, label: String) -> XCUIElement {
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
        let host = try stub.start()
        defer { stub.stop() }

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

    /// Fix-round (Opus #3, Carbon §2.1 & §2.2):
    /// - Duplicate in-flight tap (`computer.answer` -> `nil`) and `409` `.gone` never announce `"Saved…"`.
    /// - `Save` refusals (`SE4` `out_of_range` and `403 proof_required`) format cleanly as `rb.moved` /
    ///   `app.pair` without leaking `"The daemon refused"` or raw refusal codes, keeping the ask open.
    func testAnswerPhasesNeverFalselyAnnounceSavedAndFormatSaveRefusals() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.emptyGraph
        wireReadBack(stub, project: "proj-1")
        let host = try stub.start()
        defer { stub.stop() }

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
            stub?.questionsState = "[]"
            stub?.pendingActionState = nil
            return (409, #"{"error":"that question is no longer open"}"#)
        }
        stub.askAnswerHandler = { [weak stub] _, _ in
            stub?.factoryAsks = #"{"protocol":1,"asks":[]}"#
            stub?.questionsState = "[]"
            stub?.pendingActionState = nil
            return (409, #"{"code":"already_closed","error":"Ask \"ask-rule-1\" is already cancelled."}"#)
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

    func testFinishedProjectSettingsStayEditable() throws {
        let heads = #"[{"id":"proj-fin","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":8},"folder":"~/code/recipes"}]"#
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: heads))
        stub.factoryGraphs["proj-fin"] = #"{"protocol":1,"project":"proj-fin","folder":"~/code/recipes","tasks":[]}"#
        wireReadBack(stub, project: "proj-fin", projectName: "recipes")
        let host = try stub.start()
        defer { stub.stop() }

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
    }
}
