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

    private func factoryAsk(id: String, project: String, name: String, task: String? = nil, kind: String, question: String) -> PendingAction {
        PendingAction(
            id: id,
            paneID: "factory:\(project)",
            kind: "answer_prompt",
            question: question,
            options: [
                PendingOption(index: 1, label: "Yes", isDefault: true, isRecommended: true),
                PendingOption(index: 2, label: "No"),
            ],
            agent: name,
            summary: question,
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
}
