import XCTest
@testable import SheepdogKit

/// Failing unit tests for app-factory PR 8 (`docs/dev/design/app-factory.md`
/// §3.1, §4, D1–D12, §10 row 8; `FactoryAgents`, `FactoryHead`,
/// `FactoryHeadStates`, `FactoryAskCard`, `FactoryAskCardStates`;
/// `slice14-wording.md`).
final class FactoryAgentsTests: XCTestCase {
    private func head(
        id: String,
        name: String,
        goal: String = "A notes page with search",
        state: FactoryProjectState = .running,
        counts: [String: Int] = [:],
        folder: String? = "~/code/notes",
        coordinatorPane: String? = nil,
        alert: FactoryProjectAlert? = nil
    ) throws -> FactoryProjectHead {
        var obj: [String: Any] = ["id": id, "name": name, "goal": goal, "state": state.rawValue, "counts": counts]
        if let folder { obj["folder"] = folder }
        if let coordinatorPane { obj["coordinator_pane"] = coordinatorPane }
        if let alert {
            var a: [String: Any] = ["kind": alert.kind, "cause": alert.cause]
            if let fix = alert.fix { a["fix"] = fix }
            obj["alert"] = a
        }
        return try DaemonClient.decoder.decode(FactoryProjectHead.self, from: JSONSerialization.data(withJSONObject: obj))
    }

    private func factoryAsk(
        id: String,
        project: String,
        name: String,
        task: String? = nil,
        kind: String,
        question: String,
        command: String? = nil,
        options: [PendingOption]? = nil
    ) -> PendingAction {
        PendingAction(
            id: id,
            paneID: "factory:\(project)",
            kind: "answer_prompt",
            question: question,
            options: options ?? [
                PendingOption(index: 1, label: "Yes", isDefault: true, isRecommended: true),
                PendingOption(index: 2, label: "No"),
            ],
            agent: name,
            summary: question,
            command: command,
            factory: FactoryAskMeta(project: project, task: task, kind: kind)
        )
    }

    /// Pins app-factory.md §3.1 & §4: one permanent head per project, filter
    /// coordinator/worker/reviewer agent rows (by `coordinator_pane` or
    /// `sheepdog.project` label), merge a blocked coordinator into its head
    /// under Needs you, and never double-count it in `waitingCount`.
    func testFiltersFactoryAgentsMergesBlockedCoordinatorAndSingleCountsBadge() throws {
        let notes = try head(id: "proj-1", name: "notes", state: .running, counts: ["working": 3, "waiting_on_dep": 2, "done": 9], coordinatorPane: "coord-notes-1")
        let photos = try head(id: "proj-2", name: "photos", goal: "Faster thumbnails in the photo grid", state: .stopped, counts: ["done": 2, "queued": 4], folder: "~/code/photos")
        let recipes = try head(id: "proj-3", name: "recipes", goal: "Import recipes from a link", state: .finished, counts: ["done": 8], folder: "~/code/recipes")

        let ordinaryWorking = Agent(paneID: "p-auth", alias: "auth", kind: "codex", state: .working)
        let ordinaryExited = Agent(paneID: "p-old", alias: "old", kind: "claude", state: .exited)
        let blockedCoord = Agent(paneID: "coord-notes-1", alias: "notes", kind: "claude", state: .blocked)
        let worker = Agent(paneID: "p-w1", alias: "worker-t8", kind: "codex", state: .working, labels: ["sheepdog.project": "proj-1", "sheepdog.role": "worker"])
        let stuckWorker = Agent(paneID: "p-w2", alias: "worker-t9", kind: "codex", state: .blocked, labels: ["sheepdog.project": "proj-1", "sheepdog.role": "worker"])
        let reviewer = Agent(paneID: "p-r1", alias: "rev-t7", kind: "claude", state: .working, labels: ["sheepdog.project": "proj-1", "sheepdog.role": "reviewer"])

        let coordDialog = PendingAction(
            id: "ap-coord", paneID: "coord-notes-1", kind: "answer_prompt",
            question: "Allow gh pr list?", options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")],
            agent: "notes", command: "gh pr list --state open"
        )
        let ask = factoryAsk(id: "fask-1", project: "proj-1", name: "notes", task: "T10", kind: "delete", question: "Delete the old notes table in the migration?")

        let list = FactoryAgentsList(
            agents: [ordinaryWorking, ordinaryExited, blockedCoord, worker, stuckWorker, reviewer],
            heads: [notes, photos, recipes],
            questions: [coordDialog, ask]
        )

        XCTAssertEqual(list.agents.all.map(\.paneID), ["p-auth", "p-old"], "only non-factory agents remain in ordinary rows")
        XCTAssertEqual(list.needsYouHeads.map(\.head.id), ["proj-1"])
        XCTAssertEqual(list.needsYouHeads.first?.coordinatorDialog?.id, "ap-coord")
        XCTAssertTrue(list.workingHeads.isEmpty, "a blocked coordinator head moves to Needs you, not Working")
        XCTAssertEqual(list.stoppedHeads.map(\.head.id), ["proj-2"])
        XCTAssertTrue(list.hasStoppedSection)
        XCTAssertEqual(list.finishedHeads.map(\.head.id), ["proj-3"])
        XCTAssertEqual(list.factoryAsks.map(\.id), ["fask-1"])
        XCTAssertEqual(list.waitingCount, 2, "1 blocked coordinator + 1 factory ask; never double-counts coordinator or stuck worker")
    }

    /// Pins app-factory.md §3.1 & slice14-wording.md (`app.sec.stopped`): a
    /// running project with an open ask stays under Working with its waiting
    /// count while its ask card sits under Needs you; Stopped hides when none.
    func testRunningHeadWithOpenAskStaysInWorkingAndStoppedSectionHidesWhenNone() throws {
        let notes = try head(id: "proj-1", name: "notes", state: .running, counts: ["working": 3, "owner_answer": 1, "waiting_on_dep": 1, "done": 9], coordinatorPane: "coord-notes-1")
        let blockedOrdinary = Agent(paneID: "p-docs", alias: "docs", kind: "codex", state: .blocked)
        let ask = factoryAsk(id: "fask-1", project: "proj-1", name: "notes", task: "T10", kind: "delete", question: "Delete the old notes table in the migration?")

        let list = FactoryAgentsList(agents: [blockedOrdinary], heads: [notes], questions: [ask])
        XCTAssertTrue(list.needsYouHeads.isEmpty)
        XCTAssertEqual(list.workingHeads.map(\.head.id), ["proj-1"])
        XCTAssertEqual(list.workingHeads.first?.waitingOnYouCount, 1)
        XCTAssertFalse(list.hasStoppedSection)
        XCTAssertEqual(list.waitingCount, 2, "1 ordinary blocked agent + 1 open factory ask")
    }

    /// Pins FactoryHead, FactoryHeadStates, and slice14-wording.md exact words
    /// (kind, state words, counts, no tasks yet, alert cause + fix, stuck copy,
    /// and VoiceOver labels).
    func testHeadContentExactWordingAlertsAndVoiceOver() throws {
        let running = try head(id: "p1", name: "notes", goal: "A notes page with search", state: .running, counts: ["working": 3, "owner_answer": 1, "waiting_on_dep": 1, "done": 9], coordinatorPane: "c1")
        let runningItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [running], questions: []).workingHeads.first)
        XCTAssertEqual(runningItem.kindWord, "factory")
        XCTAssertEqual(runningItem.stateWord, "running")
        XCTAssertEqual(runningItem.countsLine, "3 working · 1 waiting on you · 9 of 14 done")
        XCTAssertEqual(runningItem.accessibilityLabel, "notes factory, A notes page with search, running, 3 working · 1 waiting on you · 9 of 14 done")

        let noTasks = try head(id: "p0", name: "scratch", goal: "Plan the app", state: .running, counts: [:])
        let noTasksItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [noTasks], questions: []).workingHeads.first)
        XCTAssertEqual(noTasksItem.countsLine, "no tasks yet")

        let signedOut = try head(
            id: "p2", name: "docs", goal: "Docs site", state: .running, counts: ["working": 1, "done": 2], coordinatorPane: nil,
            alert: FactoryProjectAlert(kind: "agents_down", cause: "claude coordinator is signed out", fix: "sign in to claude at this Mac")
        )
        let signedOutItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [signedOut], questions: []).workingHeads.first)
        XCTAssertEqual(signedOutItem.alertLine, "The docs coordinator is not running: claude coordinator is signed out")
        XCTAssertEqual(signedOutItem.alertFixLine, "Fix: sign in to claude at this Mac")
        XCTAssertTrue(signedOutItem.accessibilityLabel.contains("claude coordinator is signed out"))
        XCTAssertTrue(signedOutItem.accessibilityLabel.contains("Fix: sign in to claude at this Mac"))

        let stopped = try head(
            id: "p3", name: "photos", goal: "Faster thumbnails in the photo grid", state: .stopped, counts: ["done": 2, "queued": 4],
            alert: FactoryProjectAlert(kind: "ci_stalled", cause: "CI pending", fix: nil)
        )
        let stoppedItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [stopped], questions: []).stoppedHeads.first)
        XCTAssertEqual(stoppedItem.stateWord, "stopped")
        XCTAssertEqual(stoppedItem.countsLine, "2 of 6 done")
        XCTAssertEqual(stoppedItem.alertLine, "CI pending")
        XCTAssertNil(stoppedItem.alertFixLine)

        let finished = try head(id: "p4", name: "recipes", goal: "Import recipes from a link", state: .finished, counts: ["done": 8])
        let finishedItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [finished], questions: []).finishedHeads.first)
        XCTAssertEqual(finishedItem.stateWord, "finished")
        XCTAssertEqual(finishedItem.countsLine, "8 of 8 done")
        XCTAssertEqual(finishedItem.accessibilityLabel, "recipes factory, Import recipes from a link, finished, 8 of 8 done")

        let blockedCoord = Agent(paneID: "c1", alias: "notes", state: .blocked)
        let stuckHead = try head(id: "p5", name: "notes", goal: "A notes page with search", state: .running, counts: ["working": 3, "waiting_on_dep": 2, "done": 9], coordinatorPane: "c1")
        let stuckItem = try XCTUnwrap(FactoryAgentsList(agents: [blockedCoord], heads: [stuckHead], questions: []).needsYouHeads.first)
        XCTAssertEqual(stuckItem.stuckHeading, "The notes coordinator is stuck on a prompt")
        XCTAssertEqual(stuckItem.stuckNoHint, "To say what to do instead, use the box on the notes page. The coordinator gets it when it is next free.")
        XCTAssertEqual(stuckItem.accessibilityLabel, "notes factory, A notes page with search, running, 3 working · 9 of 14 done, the coordinator is stuck on a prompt")
    }

    func testHeadCaptionUsesOnlyNonblankFolderAndComputerName() throws {
        let cases: [(folder: String?, computer: String?, caption: String?)] = [
            (nil, nil, nil),
            ("", "Home Mac", "Home Mac"),
            (" \n ", "Home Mac", "Home Mac"),
            ("~/code/notes", nil, "~/code/notes"),
            (" ~/code/notes ", "Home Mac", "~/code/notes · Home Mac"),
            (nil, " \n ", nil),
        ]
        for entry in cases {
            let project = try head(id: "p1", name: "notes", folder: entry.folder)
            let item = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [project], questions: []).workingHeads.first)
            XCTAssertEqual(item.caption(computerName: entry.computer), entry.caption)
        }
    }

    /// Pins FactoryAskCard, FactoryAskCardStates, app-factory.md §4, and
    /// slice14-wording.md (`rb.card.meta`): all four drawn kinds, fallback kind,
    /// override footer, and VoiceOver labels.
    func testFactoryAskCardPresentationKindsAndVoiceOver() throws {
        let ordinary = PendingAction(id: "ap-1", paneID: "p1", kind: "answer_prompt", agent: "auth", summary: "Run tests?")
        XCTAssertNil(FactoryAskCardPresentation(ordinary))

        let deleteCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(id: "f1", project: "p1", name: "notes", task: "T10", kind: "delete", question: "Delete the old notes table in the migration?")))
        XCTAssertEqual(deleteCard.headerLine, "notes · T10 · the coordinator asks")
        XCTAssertNil(deleteCard.overrideFooter)
        XCTAssertEqual(deleteCard.accessibilityLabel, "notes, T10, needs your answer")

        let releaseCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(id: "f2", project: "p1", name: "notes", task: nil, kind: "release", question: "Tag 0.2.0 now?")))
        XCTAssertEqual(releaseCard.headerLine, "notes · the coordinator asks")
        XCTAssertEqual(releaseCard.accessibilityLabel, "notes needs your answer")

        let ruleCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(id: "f3", project: "p1", name: "notes", task: nil, kind: "rule", question: "This project: review rounds 3 (now 2). Save?")))
        XCTAssertEqual(ruleCard.headerLine, "notes · setting · the engine asks")
        XCTAssertEqual(ruleCard.accessibilityLabel, "notes, a setting, needs your answer")

        let overrideCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(id: "f4", project: "p1", name: "notes", task: "T7", kind: "override", question: "Merge T7 now, without waiting for review and CI?")))
        XCTAssertEqual(overrideCard.headerLine, "notes · T7 · override")
        XCTAssertEqual(overrideCard.overrideFooter, "Only you can answer this. A conflict, or a commit newer than this card, is not merged.")
        XCTAssertEqual(overrideCard.accessibilityLabel, "notes, T7, override, needs your answer")

        let otherCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(id: "f5", project: "p1", name: "notes", task: "T4", kind: "gate", question: "Approve T4?")))
        XCTAssertEqual(otherCard.headerLine, "notes · T4")
        XCTAssertNil(otherCard.overrideFooter)
        XCTAssertEqual(otherCard.accessibilityLabel, "notes, T4, needs your answer")
    }

    /// Pins empty/error states with factory heads and lenient `Agent.labels` decode.
    func testEmptyErrorStatesAndAgentLabelsDecode() throws {
        let stoppedOnly = FactoryAgentsList(agents: [], heads: [try head(id: "p1", name: "photos", state: .stopped)], questions: [])
        XCTAssertFalse(stoppedOnly.isEmpty, "a stopped project head keeps Agents out of the empty state")

        var online = LinkDriver()
        online.online()
        XCTAssertEqual(FlockPaneState.resolve(link: online.link, hasAgents: !stoppedOnly.isEmpty), .content)

        let emptyList = FactoryAgentsList(agents: [], heads: [], questions: [])
        XCTAssertTrue(emptyList.isEmpty)
        XCTAssertEqual(FlockPaneState.resolve(link: online.link, hasAgents: !emptyList.isEmpty), .empty)

        var refused = LinkDriver()
        refused.send(.demandChanged(refused.link.demand))
        refused.fail("lan:7777", .unauthorized(.revoked))
        XCTAssertEqual(FlockPaneState.resolve(link: refused.link, hasAgents: !emptyList.isEmpty), .tokenRefused)
        XCTAssertEqual(FlockPaneState.resolve(link: refused.link, hasAgents: !stoppedOnly.isEmpty), .content)

        let labelledJSON = #"{"pane_id":"p-w","alias":"worker","state":"working","labels":{"sheepdog.project":"proj-1","sheepdog.role":"worker"}}"#
        let labelled = try DaemonClient.decoder.decode(Agent.self, from: Data(labelledJSON.utf8))
        XCTAssertEqual(labelled.labels?["sheepdog.project"], "proj-1")
        XCTAssertTrue(labelled.isFactoryAgent)

        let plainJSON = #"{"pane_id":"p-a","alias":"auth","state":"working"}"#
        let plain = try DaemonClient.decoder.decode(Agent.self, from: Data(plainJSON.utf8))
        XCTAssertNil(plain.labels)
        XCTAssertFalse(plain.isFactoryAgent)
    }

    /// Pins FactoryAskCard.dc.html, FactoryAskCardStates.dc.html, internal/factory/flow/asks.go
    /// (`FormatAskCommand`), and slice14-wording.md (`rb.save` / `rb.keep`):
    /// non-override factory asks suppress `pending.command` ("delete · T10", "rule", "release",
    /// "gate · T4"), `override` keeps trimmed multi-line targets, and option VoiceOver labels
    /// never say "Approve:" or "Decline:" ("No, wait (Recommended)", "Save (Recommended)").
    func testFactoryAskCardCommandSuppressionAndOptionVoiceOver() throws {
        let deleteAsk = factoryAsk(
            id: "f-del", project: "p1", name: "notes", task: "T10", kind: "delete",
            question: "Delete the old notes table in the migration?",
            command: "delete · T10",
            options: [
                PendingOption(index: 1, label: "Keep it and copy the rows", isDefault: true, isRecommended: true),
                PendingOption(index: 2, label: "Delete it"),
                PendingOption(index: 3, label: "Not now"),
            ]
        )
        let deleteCard = try XCTUnwrap(FactoryAskCardPresentation(deleteAsk))
        XCTAssertNil(deleteCard.command, "delete FormatAskCommand payload must not render a command box")
        XCTAssertNil(try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-del-sha", project: "p1", name: "notes", task: "T10", kind: "delete",
            question: "Delete?", command: "delete · T10 @ 3f9c2ab"
        ))).command)

        let releaseCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-rel", project: "p1", name: "notes", task: nil, kind: "release",
            question: "Tag 0.2.0 now?", command: "release"
        )))
        XCTAssertNil(releaseCard.command, "release FormatAskCommand payload must not render a command box")

        let ruleCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-rule", project: "p1", name: "notes", task: nil, kind: "rule",
            question: "This project: review rounds 3 (now 2). Save?", command: "rule",
            options: [
                PendingOption(index: 1, label: "Save", isDefault: true, isRecommended: true),
                PendingOption(index: 2, label: "Keep current"),
            ]
        )))
        XCTAssertNil(ruleCard.command, "rule FormatAskCommand payload must not leak internal kind 'rule'")

        let gateCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-gate", project: "p1", name: "notes", task: "T4", kind: "gate",
            question: "Approve T4?", command: "gate · T4"
        )))
        XCTAssertNil(gateCard.command, "non-override gate payload must not render a command box")

        let overrideTargets = "T7 #41 @ 3f9c2ab · running\nT8 #42 @ 9a1b2c3 · has unreported work"
        let overrideCard = try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-ovr", project: "p1", name: "notes", task: "T7", kind: "override",
            question: "Merge T7 now, without waiting for review and CI?",
            command: "  \(overrideTargets)\n",
            options: [
                PendingOption(index: 1, label: "Yes, merge now"),
                PendingOption(index: 2, label: "No, wait", isDefault: true, isRecommended: true),
            ]
        )))
        XCTAssertEqual(overrideCard.command, overrideTargets, "override multi-line FormatAskCommand targets must be preserved")
        XCTAssertNil(try XCTUnwrap(FactoryAskCardPresentation(factoryAsk(
            id: "f-ovr-blank", project: "p1", name: "notes", task: "T7", kind: "override",
            question: "Merge T7 now?", command: "  \n "
        ))).command)

        XCTAssertEqual(overrideCard.optionAccessibilityLabel(PendingOption(index: 1, label: "Yes, merge now")), "Yes, merge now")
        XCTAssertEqual(
            overrideCard.optionAccessibilityLabel(PendingOption(index: 2, label: "No, wait", isDefault: true, isRecommended: true)),
            "No, wait (Recommended)"
        )
        XCTAssertEqual(
            deleteCard.optionAccessibilityLabel(PendingOption(index: 1, label: "Keep it and copy the rows", isDefault: true, isRecommended: true)),
            "Keep it and copy the rows (Recommended)"
        )
        XCTAssertEqual(deleteCard.optionAccessibilityLabel(PendingOption(index: 2, label: "Delete it")), "Delete it")
        XCTAssertEqual(deleteCard.optionAccessibilityLabel(PendingOption(index: 3, label: "Not now")), "Not now")
        XCTAssertEqual(
            ruleCard.optionAccessibilityLabel(PendingOption(index: 1, label: "Save", isDefault: true, isRecommended: true)),
            "Save (Recommended)"
        )
        XCTAssertEqual(ruleCard.optionAccessibilityLabel(PendingOption(index: 2, label: "Keep current")), "Keep current")
        XCTAssertEqual(
            overrideCard.optionAccessibilityLabel(PendingOption(index: 2, label: "No, wait (Recommended)", detail: "Let review finish", isRecommended: true, title: "No, wait")),
            "No, wait (Recommended). Let review finish"
        )
        XCTAssertEqual(
            PendingOption(index: 2, label: "No, wait", isRecommended: true).accessibilityLabel,
            "Decline: No, wait, recommended",
            "ordinary non-factory CLI option VoiceOver wording remains unchanged"
        )
    }

    /// Pins internal/factory/view.go (`owner_gate` stage + project-level asks with `Task == ""`)
    /// and FactoryAgents.dc.html (`state="readback"`): `waitingOnYouCount` includes `owner_gate`
    /// and open project-level (`rule` / `release`) asks without double-counting task asks.
    func testWaitingOnYouCountsOwnerGateAndProjectLevelAsksWithoutDoubleCounting() throws {
        let gateHead = try head(id: "proj-gate", name: "notes", state: .running, counts: ["working": 2, "owner_gate": 1, "done": 4])
        let gateItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [gateHead], questions: []).workingHeads.first)
        XCTAssertEqual(gateItem.waitingOnYouCount, 1)
        XCTAssertEqual(gateItem.countsLine, "2 working · 1 waiting on you · 4 of 7 done")

        let bothHead = try head(id: "proj-both", name: "notes", state: .running, counts: ["working": 1, "owner_answer": 1, "owner_gate": 1, "done": 3])
        let bothItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [bothHead], questions: []).workingHeads.first)
        XCTAssertEqual(bothItem.waitingOnYouCount, 2)
        XCTAssertEqual(bothItem.countsLine, "1 working · 2 waiting on you · 3 of 6 done")

        let readbackHead = try head(id: "proj-rb", name: "notes", state: .running, counts: ["working": 2, "done": 5])
        let ruleAsk = factoryAsk(id: "fask-rule", project: "proj-rb", name: "notes", task: nil, kind: "rule", question: "Save?", command: "rule")
        let readbackItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [readbackHead], questions: [ruleAsk]).workingHeads.first)
        XCTAssertEqual(readbackItem.waitingOnYouCount, 1)
        XCTAssertEqual(readbackItem.countsLine, "2 working · 1 waiting on you · 5 of 7 done")

        let releaseHead = try head(id: "proj-rel", name: "notes", state: .running, counts: ["done": 8])
        let releaseAsk = factoryAsk(id: "fask-rel", project: "proj-rel", name: "notes", task: nil, kind: "release", question: "Tag 0.2.0?", command: "release")
        let releaseItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [releaseHead], questions: [releaseAsk]).workingHeads.first)
        XCTAssertEqual(releaseItem.waitingOnYouCount, 1)
        XCTAssertEqual(releaseItem.countsLine, "1 waiting on you · 8 of 8 done")

        let taskHead = try head(id: "proj-task", name: "notes", state: .running, counts: ["working": 3, "owner_answer": 1, "done": 9])
        let deleteAsk = factoryAsk(id: "fask-del", project: "proj-task", name: "notes", task: "T10", kind: "delete", question: "Delete?", command: "delete · T10")
        let taskItem = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [taskHead], questions: [deleteAsk]).workingHeads.first)
        XCTAssertEqual(taskItem.waitingOnYouCount, 1, "task ask already reflected in owner_answer must not double-count")
    }

    /// Pins app-factory.md §8, slice14-wording.md (`app.pair.stop` pattern), and Go's
    /// non-empty `sheepdog.project` / `coordinator_pane` / folder / ask-field invariants.
    func testResumeRefusalWordingResumableFolderAndBlankFieldGuards() throws {
        let proofMsg = FactoryAgentsList.resumeErrorMessage(
            DaemonError.rejected(status: 403, message: "proof_required"),
            computerName: "Work Mac"
        )
        XCTAssertFalse(proofMsg.contains("proof_required"), "raw error code must not leak: \(proofMsg)")
        XCTAssertTrue(proofMsg.contains("Work Mac no longer knows this phone"), proofMsg)
        XCTAssertTrue(proofMsg.contains("Pair this phone again in Settings ▸ Work Mac"), proofMsg)
        XCTAssertTrue(proofMsg.contains("press Resume again"), proofMsg)

        let settingMsg = FactoryAgentsList.resumeErrorMessage(
            DaemonError.rejected(status: 400, message: "Not started: a setting is not allowed. Change it in settings below."),
            computerName: "Work Mac"
        )
        XCTAssertTrue(settingMsg.contains("Not started: a setting is not allowed. Change it in settings below."), settingMsg)

        let timeoutMsg = FactoryAgentsList.resumeErrorMessage(
            DaemonError.timedOut(endpoint: "127.0.0.1:7777"),
            computerName: "Work Mac"
        )
        XCTAssertTrue(timeoutMsg.contains(DaemonError.slowAnswer), timeoutMsg)

        let stoppedValid = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [try head(id: "p-ok", name: "photos", state: .stopped, folder: "~/code/photos")], questions: []).stoppedHeads.first)
        XCTAssertEqual(stoppedValid.resumableFolder, "~/code/photos")
        let stoppedEmpty = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [try head(id: "p-empty", name: "photos", state: .stopped, folder: "")], questions: []).stoppedHeads.first)
        XCTAssertNil(stoppedEmpty.resumableFolder)
        let stoppedBlank = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [try head(id: "p-blank", name: "photos", state: .stopped, folder: "   ")], questions: []).stoppedHeads.first)
        XCTAssertNil(stoppedBlank.resumableFolder)
        let stoppedNil = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [try head(id: "p-nil", name: "photos", state: .stopped, folder: nil)], questions: []).stoppedHeads.first)
        XCTAssertNil(stoppedNil.resumableFolder)
        let runningValid = try XCTUnwrap(FactoryAgentsList(agents: [], heads: [try head(id: "p-run", name: "notes", state: .running, folder: "~/code/notes")], questions: []).workingHeads.first)
        XCTAssertNil(runningValid.resumableFolder)

        let emptyLabelAgent = Agent(paneID: "p-empty-lbl", alias: "auth", state: .working, labels: ["sheepdog.project": ""])
        let blankLabelAgent = Agent(paneID: "p-blank-lbl", alias: "docs", state: .working, labels: ["sheepdog.project": "   "])
        XCTAssertFalse(emptyLabelAgent.isFactoryAgent)
        XCTAssertFalse(blankLabelAgent.isFactoryAgent)

        let emptyCoordHead = try head(id: "p-coord-empty", name: "notes", state: .running, counts: ["working": 1], coordinatorPane: "")
        let orphanBlankQuestion = PendingAction(
            id: "ap-blank", paneID: "", kind: "answer_prompt",
            question: "Allow?", options: [PendingOption(index: 1, label: "Yes")]
        )
        let list = FactoryAgentsList(agents: [emptyLabelAgent, blankLabelAgent], heads: [emptyCoordHead], questions: [orphanBlankQuestion])
        XCTAssertEqual(list.agents.all.map(\.paneID), ["p-empty-lbl", "p-blank-lbl"])
        XCTAssertTrue(list.needsYouHeads.isEmpty, "empty coordinator_pane must not match a question with empty pane_id")
        XCTAssertEqual(list.workingHeads.map(\.head.id), ["p-coord-empty"])
        XCTAssertNil(list.workingHeads.first?.coordinatorDialog)

        let blankAsk = PendingAction(
            id: "f-blank", paneID: "factory:proj-1", kind: "answer_prompt",
            question: "Delete?", options: [PendingOption(index: 1, label: "Delete it")],
            agent: "   ", summary: "Delete?",
            factory: FactoryAskMeta(project: "proj-1", task: "  ", kind: "delete")
        )
        let blankCard = try XCTUnwrap(FactoryAskCardPresentation(blankAsk))
        XCTAssertEqual(blankCard.headerLine, "proj-1 · the coordinator asks")
        XCTAssertEqual(blankCard.accessibilityLabel, "proj-1 needs your answer")
    }
}

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

@MainActor
final class FactoryAgentsAppViewTests: XCTestCase {
    private func renderCard(_ pending: PendingAction, style: UIUserInterfaceStyle = .light) -> (size: CGSize, dangerPixels: Int) {
        let view = ApprovalCardView(
            card: ApprovalFlow.Card(pending: pending),
            idPrefix: "factoryAsk.\(pending.id)",
            answer: { _ in }
        )
        .frame(width: 358)
        .fixedSize(horizontal: false, vertical: true)

        let host = UIHostingController(rootView: view)
        host.traitOverrides.userInterfaceStyle = style
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 900))
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        let fit = host.sizeThatFits(in: CGSize(width: 358, height: 900))
        let drawSize = CGSize(width: 358, height: max(1, ceil(fit.height)))
        host.view.frame = CGRect(origin: .zero, size: drawSize)
        host.view.layoutIfNeeded()

        let format = UIGraphicsImageRendererFormat()
        format.scale = 2
        let image = UIGraphicsImageRenderer(size: drawSize, format: format).image { _ in
            host.view.drawHierarchy(in: CGRect(origin: .zero, size: drawSize), afterScreenUpdates: true)
        }
        guard let cg = image.cgImage else { return (fit, 0) }
        let w = cg.width, h = cg.height
        var buf = [UInt8](repeating: 0, count: w * h * 4)
        let ctx = CGContext(
            data: &buf, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        )!
        ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))

        var danger = 0
        for i in stride(from: 0, to: buf.count, by: 4) {
            let r = Int(buf[i]), g = Int(buf[i + 1]), b = Int(buf[i + 2])
            if style == .light {
                // Theme.danger light (#B42318 = 180,35,24); all non-danger tokens have (r - g) <= 69.
                if r > 155, g < 65, b < 60, r - g > 95 { danger += 1 }
            } else {
                // Theme.danger dark (#F26B5B = 242,107,91); non-danger tokens have (r - g) <= 71 or b <= 61.
                if r > 210, g < 130, b > 70, r - g > 95 { danger += 1 }
            }
        }
        return (fit, danger)
    }

    /// Pins ApprovalCardView end-to-end: non-override factory asks ("delete · T10", "rule")
    /// render no command box, whereas multi-line `override` targets and ordinary CLI prompts do.
    func testApprovalCardSuppressesNonOverrideFactoryCommandAndKeepsOverrideTargets() {
        let deleteOptions = [
            PendingOption(index: 1, label: "Keep it and copy the rows", isDefault: true, isRecommended: true),
            PendingOption(index: 2, label: "Delete it"),
        ]
        let deleteWithCmd = PendingAction(
            id: "f-del-cmd", paneID: "factory:p1", kind: "answer_prompt",
            question: "Delete the old notes table in the migration?", options: deleteOptions,
            agent: "notes", summary: "Delete the old notes table in the migration?",
            command: "delete · T10",
            factory: FactoryAskMeta(project: "p1", task: "T10", kind: "delete")
        )
        var deleteWithoutCmd = deleteWithCmd
        deleteWithoutCmd.command = nil
        XCTAssertEqual(
            renderCard(deleteWithCmd).size.height,
            renderCard(deleteWithoutCmd).size.height,
            accuracy: 0.5,
            "ApprovalCardView must suppress pending.command on a delete factory ask"
        )

        let ruleWithCmd = PendingAction(
            id: "f-rule-cmd", paneID: "factory:p1", kind: "answer_prompt",
            question: "This project: review rounds 3 (now 2). Save?",
            options: [PendingOption(index: 1, label: "Save", isDefault: true, isRecommended: true), PendingOption(index: 2, label: "Keep current")],
            agent: "notes", summary: "This project: review rounds 3 (now 2). Save?",
            command: "rule",
            factory: FactoryAskMeta(project: "p1", task: nil, kind: "rule")
        )
        var ruleWithoutCmd = ruleWithCmd
        ruleWithoutCmd.command = nil
        XCTAssertEqual(
            renderCard(ruleWithCmd).size.height,
            renderCard(ruleWithoutCmd).size.height,
            accuracy: 0.5,
            "ApprovalCardView must suppress pending.command ('rule') on a setting read-back ask"
        )

        let overrideWithTargets = PendingAction(
            id: "f-ovr-cmd", paneID: "factory:p1", kind: "answer_prompt",
            question: "Merge T7 now, without waiting for review and CI?",
            options: [PendingOption(index: 1, label: "Yes, merge now"), PendingOption(index: 2, label: "No, wait", isDefault: true, isRecommended: true)],
            agent: "notes", summary: "Merge T7 now, without waiting for review and CI?",
            command: "T7 #41 @ 3f9c2ab · running\nT8 #42 @ 9a1b2c3 · has unreported work",
            factory: FactoryAskMeta(project: "p1", task: "T7", kind: "override")
        )
        var overrideWithoutTargets = overrideWithTargets
        overrideWithoutTargets.command = nil
        XCTAssertGreaterThan(
            renderCard(overrideWithTargets).size.height,
            renderCard(overrideWithoutTargets).size.height + 24,
            "ApprovalCardView must render multi-line override targets"
        )

        let ordinaryWithCmd = PendingAction(
            id: "ap-cmd", paneID: "p-auth", kind: "answer_prompt",
            question: "Allow gh pr list?",
            options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")],
            agent: "auth", summary: "Allow gh pr list?",
            command: "gh pr list --state open"
        )
        var ordinaryWithoutCmd = ordinaryWithCmd
        ordinaryWithoutCmd.command = nil
        XCTAssertGreaterThan(
            renderCard(ordinaryWithCmd).size.height,
            renderCard(ordinaryWithoutCmd).size.height + 24,
            "ordinary non-factory CLI prompt cards keep their command box"
        )
    }

    /// Pins FactoryAskCard.dc.html:28, 40 & Theme.swift:71-72: factory ask options
    /// (including safe, recommended `"No, wait"` on `override`) never render in `Theme.danger` red.
    func testFactoryAskOptionsAreNeverPaintedRedEvenForRecommendedNoWait() {
        let overrideAsk = PendingAction(
            id: "f-ovr-red", paneID: "factory:p1", kind: "answer_prompt",
            question: "Merge T7 now, without waiting for review and CI?",
            options: [
                PendingOption(index: 1, label: "Yes, merge now"),
                PendingOption(index: 2, label: "No, wait", isDefault: true, isRecommended: true),
            ],
            agent: "notes", summary: "Merge T7 now, without waiting for review and CI?",
            command: "T7 #41 @ 3f9c2ab · running",
            factory: FactoryAskMeta(project: "p1", task: "T7", kind: "override")
        )
        let ordinaryCLI = PendingAction(
            id: "ap-red", paneID: "p-auth", kind: "answer_prompt",
            question: "Run destructive migration?",
            options: [
                PendingOption(index: 1, label: "Yes, run"),
                PendingOption(index: 2, label: "No, stop"),
            ],
            agent: "auth", summary: "Run destructive migration?"
        )

        for style in [UIUserInterfaceStyle.light, .dark] {
            let mode = style == .light ? "light" : "dark"
            let ordinaryRender = renderCard(ordinaryCLI, style: style)
            XCTAssertGreaterThan(ordinaryRender.dangerPixels, 0, "positive control: ordinary CLI 'No, stop' renders in Theme.danger (\(mode))")

            let overrideRender = renderCard(overrideAsk, style: style)
            XCTAssertEqual(overrideRender.dangerPixels, 0, "factory override 'No, wait' (recommended) must render in Theme.ink, never Theme.danger (\(mode))")
        }
    }

    /// Pins SessionViewModel+Computers.swift: `computerSnapshots` includes `factoryHeads`
    /// on the current computer snapshot as well as secondary computers.
    func testCurrentComputerSnapshotIncludesFactoryHeads() throws {
        let suite = "FactoryAgentsAppViewTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent(suite, isDirectory: true)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let model = SessionViewModel(settings: SettingsStore(defaults: defaults, computers: store), cache: ConversationCache(directory: cacheDir))
        defer {
            model.fleet.set([])
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: cacheDir)
        }

        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pw", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "ph", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()

        let workStateJSON = #"{"agents":[],"factory":[{"id":"proj-1","name":"notes","goal":"Notes","state":"running","counts":{"working":1}}],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#
        let homeStateJSON = #"{"agents":[],"factory":[{"id":"proj-2","name":"photos","goal":"Photos","state":"stopped","counts":{"done":2}}],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#
        model.fleet.current?.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(workStateJSON.utf8)))
        model.session("home-id")?.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(homeStateJSON.utf8)))

        let snapshots = model.computerSnapshots
        XCTAssertEqual(snapshots.first { $0.id == "work-id" }?.factoryHeads.map(\.id), ["proj-1"])
        XCTAssertEqual(snapshots.first { $0.id == "home-id" }?.factoryHeads.map(\.id), ["proj-2"])
    }
}
#endif
