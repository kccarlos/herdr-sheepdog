import Foundation
import XCTest
@testable import SheepdogKit

/// Failing unit tests for app-factory PR 9 (`docs/dev/design/app-factory.md`
/// §4, §5.3, §6.1–6.2, D10–D12, §10 row 9; `FactoryProject*`, `FactoryWindow*`,
/// `FactoryWindowStates*`; `slice14-wording.md`).
final class FactoryProjectBoardTests: XCTestCase {
    private let now = RFC3339Date.date(from: "2026-11-25T11:32:00Z")!

    private func head(
        id: String = "proj-1", name: String = "notes", goal: String = "A notes page with search",
        state: FactoryProjectState = .running, counts: [String: Int] = [:],
        folder: String? = "~/code/notes", coordinatorPane: String? = "coord-notes-1",
        alert: FactoryProjectAlert? = nil
    ) throws -> FactoryProjectHead {
        var o: [String: Any] = ["id": id, "name": name, "goal": goal, "state": state.rawValue, "counts": counts]
        folder.map { o["folder"] = $0 }; coordinatorPane.map { o["coordinator_pane"] = $0 }
        if let a = alert { o["alert"] = ["kind": a.kind, "cause": a.cause].merging(a.fix.map { ["fix": $0] } ?? [:]) { _, r in r } }
        return try DaemonClient.decoder.decode(FactoryProjectHead.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func decodeTasks(_ json: String) throws -> [FactoryTask] {
        try DaemonClient.decoder.decode([FactoryTask].self, from: Data(json.utf8))
    }

    /// Board header, task grouping & layer sort, step summaries, why-waiting,
    /// run lines (hidden when stopped), last decision lines, and VoiceOver labels.
    func testBoardGroupingStepsWhyWaitingRunLineLastDecisionAndVoiceOver() throws {
        let project = try head(counts: ["working": 3, "owner_answer": 1, "waiting_on_dep": 1, "done": 9])
        let tasks = try decodeTasks(#"""
        [
          {"ref":"T11","title":"feat: sync","stage":"waiting_on_dep","why_waiting":"Waiting on T8","after":["T8"],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"planned","state":"now"},{"name":"implementing","state":"todo"}]},
          {"ref":"T8","title":"feat: tags","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":["T11"],"layer":1,"rounds":1,"run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z","last_actor_decision":{"at":"2026-11-25T11:03:00Z","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo","text":"needs 2 passing, at most 5"},{"name":"CI","state":"todo"},{"name":"merged","state":"todo"}]},
          {"ref":"T7","title":"feat: search","stage":"working","why_waiting":"In review round 2","after":[],"dependents":[],"layer":0,"rounds":1,"run_role":"reviewer","run_cli":"claude","run_started_at":"2026-11-25T11:26:00Z","last_actor_decision":{"at":"2026-11-25T11:05:00Z","by":"owner","decision":"answered ask-1"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"done"},{"name":"review","state":"now","text":"review round 2; needs 2 passing, at most 5"},{"name":"CI","state":"todo"},{"name":"merged","state":"todo"}]},
          {"ref":"T10","title":"feat: import","stage":"owner_answer","why_waiting":"Delete the old notes table?","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now"}]},
          {"ref":"T12","title":"feat: hotfix","stage":"ci","why_waiting":"Merging now, by your override","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"done"},{"name":"review","state":"skipped","text":"skipped by your override"},{"name":"CI","state":"now"}]},
          {"ref":"T9","title":"feat: export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"dropped","state":"ended","text":"ended without merging"}]},
          {"ref":"T14","title":"feat: search filters","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]}
        ]
        """#)
        let ask = PendingAction(id: "fask-1", paneID: "factory:proj-1", kind: "answer_prompt", question: "Delete?", options: [PendingOption(index: 1, label: "Keep it")], agent: "notes", factory: FactoryAskMeta(project: "proj-1", task: "T10", kind: "delete"))
        let other = PendingAction(id: "fask-2", paneID: "factory:proj-2", kind: "answer_prompt", question: "Other?", options: [], agent: "photos", factory: FactoryAskMeta(project: "proj-2", task: nil, kind: "release"))

        let board = FactoryProjectBoard(head: project, tasks: tasks, asks: [ask, other], computerName: "Work Mac", now: now)
        XCTAssertEqual(board.head.goal, "A notes page with search")
        XCTAssertEqual(board.stateWord, "running")
        XCTAssertEqual(board.countsLine, "3 working · 1 waiting on you · 9 of 14 done")
        XCTAssertEqual(board.caption, "~/code/notes · Work Mac")
        XCTAssertTrue(board.showsCoordinatorButton)
        XCTAssertEqual(board.coordinatorAccessibilityLabel, "Open the notes coordinator's window")
        XCTAssertEqual(board.openAsks.map(\.id), ["fask-1"])
        XCTAssertEqual(board.asksHeading, "Asks for you · 1 open")

        XCTAssertEqual(board.activeGroups.map(\.label), ["waiting on you", "waiting on CI", "working", "waiting on others"])
        XCTAssertTrue(board.activeGroups[0].isAlert)
        XCTAssertEqual(board.activeGroups[2].tasks.map(\.ref), ["T7", "T8"], "sorted by layer ascending")
        XCTAssertEqual(board.doneTasks.map(\.ref), ["T9", "T14"])
        XCTAssertEqual(board.doneFoldLabel, "done 2")

        let t8 = board.activeGroups[2].tasks[1]
        XCTAssertEqual(t8.stepSummary, "implementing, sent back from review round 1")
        XCTAssertEqual(t8.stepAccessibilityLabel, "step 2 of 5, implementing, sent back from review round 1")
        XCTAssertEqual(t8.whyWaiting, "Worker is working")
        XCTAssertEqual(t8.runLine, "worker · codex · 31 min")
        XCTAssertEqual(t8.lastDecisionLine, "coordinator: sent back — the tag index must be case-insensitive")
        XCTAssertTrue(t8.accessibilityLabel.contains("T8") && t8.accessibilityLabel.contains("worker · codex · 31 min") && t8.accessibilityLabel.contains("the tag index must be case-insensitive"))

        let t7 = board.activeGroups[2].tasks[0]
        XCTAssertEqual(t7.stepSummary, "review round 2; needs 2 passing, at most 5")
        XCTAssertEqual(t7.runLine, "reviewer · claude · 6 min")
        XCTAssertEqual(t7.lastDecisionLine, "you: answered ask-1")
        XCTAssertEqual(board.activeGroups[1].tasks[0].stepSummary, "CI, review skipped by your override")
        XCTAssertEqual(board.doneTasks[0].stepSummary, "dropped, ended without merging")
        XCTAssertEqual(board.doneTasks[1].stepSummary, "merged")

        let stopped = FactoryProjectBoard(head: try head(state: .stopped, coordinatorPane: nil), tasks: tasks, now: now)
        XCTAssertFalse(stopped.showsCoordinatorButton)
        XCTAssertNil(stopped.activeGroups.first { $0.stage == "working" }?.tasks.first?.runLine)
    }

    /// FactorySayBox exact wording, empty/1000/1001 bounds, counter VoiceOver,
    /// sent confirmations per state, and refusal mapping (403/404/409).
    func testSayBoxValidationSentStatusTooLongAndErrorMapping() {
        let empty = FactorySayBox(projectName: "notes", state: .running, hasCoordinator: true, text: "   \n ")
        XCTAssertEqual(empty.label, "Tell the notes factory")
        XCTAssertEqual(empty.placeholder, "An instruction, in your own words")
        XCTAssertEqual(empty.sendAccessibilityLabel, "Send to the notes factory")
        XCTAssertEqual(empty.defaultNote, "Delivered when the coordinator is next free. To answer an ask, use its card.")
        XCTAssertEqual(empty.questionHint, "A question? Ask by voice, or ask Sheepdog in Conversation.")
        XCTAssertFalse(empty.canSend)
        XCTAssertNil(empty.counterText)
        XCTAssertNil(empty.tooLongMessage)

        let ready = FactorySayBox(projectName: "notes", state: .running, hasCoordinator: true, text: "Skip the export task")
        XCTAssertTrue(ready.canSend)
        XCTAssertEqual(ready.sentStatus, "Told the notes factory.")

        let noCoord = FactorySayBox(projectName: "notes", state: .running, hasCoordinator: false, text: "Wait on T11")
        XCTAssertEqual(noCoord.defaultNote, "The notes coordinator is not running. It gets this when it is back.")
        XCTAssertEqual(noCoord.sentStatus, "The notes coordinator is not running. It gets this when it is back.")

        let stopped = FactorySayBox(projectName: "notes", state: .stopped, hasCoordinator: false, text: "Use new colours")
        XCTAssertTrue(stopped.canSend)
        XCTAssertEqual(stopped.defaultNote, "notes is stopped. It gets this when you resume it.")
        XCTAssertEqual(stopped.sentStatus, "notes is stopped. It gets this when you resume it.")

        let finished = FactorySayBox(projectName: "notes", state: .finished, hasCoordinator: false, text: "More work")
        XCTAssertTrue(finished.isFinished && !finished.canSend)
        XCTAssertEqual(finished.defaultNote, "notes is finished. Give it a new goal.")

        let atCap = FactorySayBox(projectName: "notes", state: .running, hasCoordinator: true, text: String(repeating: "a", count: 1000))
        XCTAssertTrue(atCap.canSend)
        XCTAssertEqual(atCap.counterText, "1,000 / 1,000")
        XCTAssertNil(atCap.tooLongMessage)

        let over = FactorySayBox(projectName: "notes", state: .running, hasCoordinator: true, text: String(repeating: "a", count: 1001))
        XCTAssertTrue(over.isTooLong && !over.canSend)
        XCTAssertEqual(over.counterText, "1,001 / 1,000")
        XCTAssertEqual(over.counterAccessibilityLabel, "1,001 of 1,000 characters")
        XCTAssertEqual(over.tooLongMessage, "Too long. Keep it under 1,000 characters.")

        XCTAssertEqual(FactoryProjectBoard.sayErrorMessage(DaemonError.rejected(status: 403, message: "proof_required"), projectName: "notes", computerName: "Work Mac"), "Not sent: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then send again.")
        XCTAssertEqual(FactoryProjectBoard.sayErrorMessage(DaemonError.rejected(status: 409, message: "already_closed"), projectName: "notes", computerName: "Work Mac"), "notes is finished. Give it a new goal.")
        let gone = DaemonError.rejected(status: 404, message: "factory project not found")
        XCTAssertTrue(FactoryProjectBoard.isProjectGone(gone))
        XCTAssertEqual(FactoryProjectBoard.sayErrorMessage(gone, projectName: "notes", computerName: "Work Mac"), "This project is gone.")
    }

    /// Stop/Resume actions, confirm dialog copy, board alerts, empty/loading/error copy.
    func testStopResumeConfirmDialogAlertsAndEmptyErrorStates() throws {
        let running = FactoryProjectBoard(head: try head(state: .running, coordinatorPane: nil, alert: FactoryProjectAlert(kind: "agents_down", cause: "claude coordinator is signed out", fix: "sign in to claude at this Mac")), tasks: [], computerName: "Work Mac")
        XCTAssertTrue(running.canStop && !running.canResume && !running.showsCoordinatorButton)
        XCTAssertEqual(running.stopAccessibilityLabel, "Stop this project")
        XCTAssertEqual(running.stopConfirm.title, "Stop this project?")
        XCTAssertEqual(running.stopConfirm.message, "Its running agents end now. Resume goes on with the same goal.")
        XCTAssertEqual(running.stopConfirm.keepLabel, "Keep it running")
        XCTAssertEqual(running.stopConfirm.confirmLabel, "Stop the project")
        XCTAssertEqual(running.alertHeading, "The notes coordinator is not running: claude coordinator is signed out")
        XCTAssertEqual(running.alertFix, "Fix: sign in to claude at this Mac")
        XCTAssertTrue(running.isEmptyTasks)
        XCTAssertEqual(running.emptyTasksHeading, "no tasks yet")
        XCTAssertEqual(running.emptyTasksDetail, "The coordinator has not planned this project yet.")

        let stopped = FactoryProjectBoard(head: try head(state: .stopped, coordinatorPane: nil), tasks: [])
        XCTAssertTrue(!stopped.canStop && stopped.canResume)
        XCTAssertEqual(stopped.resumeAccessibilityLabel, "Resume this project")
        XCTAssertEqual(stopped.barNote, "Stopped. No agent is running. Resume goes on with the same goal.")

        let finLive = FactoryProjectBoard(head: try head(state: .finished, coordinatorPane: nil), tasks: [], live: true)
        XCTAssertTrue(finLive.canStop)
        XCTAssertEqual(finLive.barNote, "Finished. An agent is still running.")
        XCTAssertEqual(finLive.stopConfirm.message, "Its last running agent ends now. The project stays finished.")

        XCTAssertEqual(FactoryProjectBoard.stopErrorMessage(DaemonError.rejected(status: 403, message: "proof_required"), computerName: "Work Mac"), "Not stopped: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then press Stop again.")
        XCTAssertEqual(FactoryProjectBoard.loadingMessage(projectName: "notes", computerName: "Work Mac"), "Loading notes from Work Mac…")
        let fail = FactoryProjectBoard.loadFailure(projectName: "notes", computerName: "Work Mac", error: DaemonError.timedOut(endpoint: "127.0.0.1:7777"))
        XCTAssertEqual(fail.heading, "Can't load notes. Trying again.")
        XCTAssertTrue(fail.detail.contains("The rest of the app still works."), fail.detail)
        XCTAssertEqual(fail.retryLabel, "Try again")
    }

    /// Coordinator window header, VoiceOver, ask reminder strip, off states, blocked refusal.
    func testCoordinatorWindowLiveAskStripOffStatesAndBlockedRefusal() throws {
        let askT10 = PendingAction(id: "f1", paneID: "factory:proj-1", kind: "answer_prompt", question: "Delete?", options: [], agent: "notes", factory: FactoryAskMeta(project: "proj-1", task: "T10", kind: "delete"))
        let askProj = PendingAction(id: "f2", paneID: "factory:proj-1", kind: "answer_prompt", question: "Tag?", options: [], agent: "notes", factory: FactoryAskMeta(project: "proj-1", task: nil, kind: "release"))

        let live = FactoryCoordinatorWindow(head: try head(state: .running, coordinatorPane: "coord-notes-1"), openAsks: [askT10])
        XCTAssertTrue(live.isLive)
        XCTAssertEqual(live.coordinatorPane, "coord-notes-1")
        XCTAssertEqual(live.title, "notes · Coordinator")
        XCTAssertEqual(live.headerAccessibilityLabel, "notes coordinator, running")
        XCTAssertEqual(live.promptPlaceholder, "Ask the notes coordinator…")
        XCTAssertEqual(live.promptAccessibilityLabel, "Message to the notes coordinator")
        XCTAssertEqual(live.backButtonLabel, "Back to notes")
        XCTAssertEqual(live.askStrip, "T10 is waiting on your answer. Answer it on its card; typing here is not an answer.")
        XCTAssertNil(live.offState)

        XCTAssertEqual(FactoryCoordinatorWindow(head: try head(), openAsks: [askProj]).askStrip, "An ask is waiting on your answer. Answer it on its card; typing here is not an answer.")
        XCTAssertEqual(FactoryCoordinatorWindow(head: try head(), openAsks: [askT10, askProj]).askStrip, "2 asks are waiting on your answer. Answer them on their cards; typing here is not an answer.")
        XCTAssertNil(FactoryCoordinatorWindow(head: try head(), openAsks: []).askStrip)

        let offAlert = FactoryCoordinatorWindow(head: try head(state: .running, coordinatorPane: nil, alert: FactoryProjectAlert(kind: "agents_down", cause: "claude coordinator is signed out", fix: "sign in to claude at this Mac")))
        XCTAssertFalse(offAlert.isLive)
        XCTAssertEqual(offAlert.headerAccessibilityLabel, "notes coordinator, not running")
        XCTAssertEqual(offAlert.offState?.heading, "The notes coordinator is not running")
        XCTAssertEqual(offAlert.offState?.alertCause, "claude coordinator is signed out")
        XCTAssertEqual(offAlert.offState?.alertFix, "Fix: sign in to claude at this Mac")
        XCTAssertEqual(offAlert.offState?.body, "This window comes back when a coordinator runs again. To tell it something meanwhile, use the box on the notes page; it gets it when it is back.")
        XCTAssertNil(FactoryCoordinatorWindow(head: try head(state: .running, coordinatorPane: nil)).offState?.alertCause)

        let stopped = FactoryCoordinatorWindow(head: try head(state: .stopped, coordinatorPane: nil))
        XCTAssertEqual(stopped.offState?.heading, "notes is stopped")
        XCTAssertEqual(stopped.offState?.body, "Resume notes on its page; it goes on with the same goal.")

        let finished = FactoryCoordinatorWindow(head: try head(state: .finished, coordinatorPane: nil))
        XCTAssertEqual(finished.offState?.heading, "notes is finished")
        XCTAssertEqual(finished.offState?.body, "To go on, reopen notes with a new goal on its page.")

        XCTAssertEqual(FactoryCoordinatorWindow.inputErrorMessage(DaemonError.rejected(status: 409, message: "notes is waiting at a prompt; answer it first"), projectName: "notes", computerName: "Work Mac"), "Not sent: the notes coordinator is waiting at its own prompt. Answer it with the keys, or on its card in Agents.")
    }

    /// D10 edge cases: blank coordinator pane, non-running states ignoring stale panes,
    /// alerts without a fix, blank task refs on asks, and non-blocked input errors.
    func testCoordinatorWindowEdgeCasesAndNonBlockedInputErrors() throws {
        let blankPane = FactoryCoordinatorWindow(head: try head(state: .running, coordinatorPane: "   "))
        XCTAssertFalse(blankPane.isLive)
        XCTAssertNil(blankPane.coordinatorPane)
        XCTAssertEqual(blankPane.headerAccessibilityLabel, "notes coordinator, not running")
        XCTAssertEqual(blankPane.offState?.heading, "The notes coordinator is not running")
        XCTAssertNil(blankPane.offState?.alertCause)
        XCTAssertNil(blankPane.offState?.alertFix)

        let alertNoFix = FactoryCoordinatorWindow(head: try head(state: .running, coordinatorPane: nil, alert: FactoryProjectAlert(kind: "agents_down", cause: "disk is full", fix: nil)))
        XCTAssertEqual(alertNoFix.offState?.alertCause, "disk is full")
        XCTAssertNil(alertNoFix.offState?.alertFix)

        let stoppedStalePane = FactoryCoordinatorWindow(head: try head(state: .stopped, coordinatorPane: "coord-stale"))
        XCTAssertFalse(stoppedStalePane.isLive)
        XCTAssertNil(stoppedStalePane.coordinatorPane)
        XCTAssertEqual(stoppedStalePane.headerAccessibilityLabel, "notes coordinator, not running")
        XCTAssertEqual(stoppedStalePane.offState?.heading, "notes is stopped")
        XCTAssertNil(stoppedStalePane.offState?.alertCause)
        XCTAssertNil(stoppedStalePane.offState?.alertFix)

        let finishedStalePane = FactoryCoordinatorWindow(head: try head(state: .finished, coordinatorPane: "coord-stale"))
        XCTAssertFalse(finishedStalePane.isLive)
        XCTAssertNil(finishedStalePane.coordinatorPane)
        XCTAssertEqual(finishedStalePane.headerAccessibilityLabel, "notes coordinator, not running")
        XCTAssertEqual(finishedStalePane.offState?.heading, "notes is finished")
        XCTAssertNil(finishedStalePane.offState?.alertCause)
        XCTAssertNil(finishedStalePane.offState?.alertFix)

        let blankTaskAsk = PendingAction(id: "f3", paneID: "factory:proj-1", kind: "answer_prompt", question: "Continue?", options: [], agent: "notes", factory: FactoryAskMeta(project: "proj-1", task: "   ", kind: "release"))
        XCTAssertEqual(
            FactoryCoordinatorWindow(head: try head(), openAsks: [blankTaskAsk]).askStrip,
            "An ask is waiting on your answer. Answer it on its card; typing here is not an answer."
        )

        let wrappedBlocked = FactoryCoordinatorWindow.inputErrorMessage(
            DaemonError.rejected(status: 409, message: "the notes factory is waiting at a prompt; answer it first (agent is blocked)"),
            projectName: "notes",
            computerName: "Work Mac"
        )
        XCTAssertEqual(wrappedBlocked, "Not sent: the notes coordinator is waiting at its own prompt. Answer it with the keys, or on its card in Agents.")

        let nonBlocked = FactoryCoordinatorWindow.inputErrorMessage(
            DaemonError.rejected(status: 500, message: "herdr did not answer"),
            projectName: "notes",
            computerName: "Work Mac"
        )
        XCTAssertFalse(nonBlocked.isEmpty)
        XCTAssertFalse(nonBlocked.contains("waiting at its own prompt"), nonBlocked)
    }

    /// `DaemonClient.sayFactory` and `DaemonClient.stopFactory` wire shapes & refusals.
    func testFactorySayAndStopClientRoutes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in (200, Data(#"{"ok":true}"#.utf8)) }
        try await DaemonClient.$idempotencyKey.withValue("say-1") { try await StubProtocol.client().sayFactory(projectID: "a/b%1", text: "Skip export") }
        let sayReq = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(sayReq.httpMethod, "POST")
        XCTAssertEqual(sayReq.url?.absoluteString.contains("/api/factory/projects/a%2Fb%251/say"), true)
        XCTAssertEqual(sayReq.value(forHTTPHeaderField: "Idempotency-Key"), "say-1")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(sayReq.httpBody)), ["text": "Skip export"])

        try await DaemonClient.$idempotencyKey.withValue("stop-1") { try await StubProtocol.client().stopFactory(projectID: "proj-1") }
        let stopReq = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(stopReq.httpMethod, "POST")
        XCTAssertEqual(stopReq.url?.path, "/api/factory/projects/stop")
        XCTAssertEqual(stopReq.value(forHTTPHeaderField: "Idempotency-Key"), "stop-1")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(stopReq.httpBody)), ["project": "proj-1"])

        StubProtocol.handler = { _ in (409, Data(#"{"code":"already_closed","error":"notes is finished. Give it a new goal."}"#.utf8)) }
        do { try await StubProtocol.client().sayFactory(projectID: "proj-1", text: "hi"); XCTFail("expected 409") }
        catch { XCTAssertEqual(error as? DaemonError, .rejected(status: 409, message: "notes is finished. Give it a new goal.")) }
    }

    /// Fix-round: `owner_gate` and `owner_answer` never emit duplicate `"waiting on you"`
    /// group headers, and raw underscore stage keys never leak as labels.
    func testStageGroupingNeverDuplicatesWaitingOnYouOrLeaksRawStageKeys() throws {
        let tasks = try decodeTasks(#"""
        [
          {"ref":"T2","title":"feat: answer","stage":"owner_answer","why_waiting":"Delete table?","after":[],"dependents":[],"layer":1,"rounds":0,"steps":[{"name":"implementing","state":"now"}]},
          {"ref":"T1","title":"feat: gate","stage":"owner_gate","why_waiting":"Approve deploy?","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"CI","state":"now"}]},
          {"ref":"T3","title":"feat: broken","stage":"blocked_or_failed","why_waiting":"CI red","after":[],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"CI","state":"now"}]}
        ]
        """#)
        let board = FactoryProjectBoard(head: try head(), tasks: tasks, now: now)
        let labels = board.activeGroups.map(\.label)
        XCTAssertEqual(Set(labels).count, labels.count, "must not emit duplicate 'waiting on you' group headers: \(labels)")
        XCTAssertFalse(labels.contains("owner_gate") || labels.contains("owner_answer") || labels.contains("blocked_or_failed"), "labels: \(labels)")
        XCTAssertTrue(board.activeGroups.allSatisfy(\.isAlert))
        XCTAssertEqual(board.activeGroups.flatMap(\.tasks).map(\.ref), ["T1", "T2", "T3"])
    }

    /// Fix-round 2: `sayErrorMessage` maps the daemon's real HTTP 409
    /// `already_closed` reply (`{"code":"already_closed","error":"<name> is finished. Give it a new goal."}`)
    /// to `app.tell.fin` without leaking `"The daemon refused: "`.
    func testSayErrorMessageMapsDaemonFinishedConflictWithoutRefusedPrefix() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in
            (409, Data(#"{"code":"already_closed","error":"notes is finished. Give it a new goal."}"#.utf8))
        }
        do {
            try await StubProtocol.client().sayFactory(projectID: "proj-1", text: "too late")
            XCTFail("expected 409 already_closed")
        } catch {
            XCTAssertEqual(
                FactoryProjectBoard.sayErrorMessage(error, projectName: "notes", computerName: "Work Mac"),
                "notes is finished. Give it a new goal."
            )
        }
    }

    /// PR 9b fix-round: whitespace-only `coordinator_pane` values must be treated
    /// like no pane across `FactoryProjectBoard` (`showsCoordinatorButton`, `sayBox`)
    /// and `FactoryCoordinatorWindow` (`isLive`).
    func testWhitespaceCoordinatorPaneTreatedAsNotRunningOnBoardAndSayBox() throws {
        for rawPane in ["", "   ", " \n\t "] {
            let blankHead = try head(state: .running, coordinatorPane: rawPane)
            let board = FactoryProjectBoard(head: blankHead, tasks: [], now: now)
            let window = FactoryCoordinatorWindow(head: blankHead)

            XCTAssertFalse(window.isLive, "window.isLive for \(rawPane.debugDescription)")
            XCTAssertFalse(board.showsCoordinatorButton, "showsCoordinatorButton must be false for whitespace pane \(rawPane.debugDescription)")
            XCTAssertEqual(board.showsCoordinatorButton, window.isLive)

            let box = board.sayBox(text: "Wait on T11")
            XCTAssertFalse(box.hasCoordinator, "sayBox.hasCoordinator must be false for whitespace pane \(rawPane.debugDescription)")
            XCTAssertEqual(box.defaultNote, "The notes coordinator is not running. It gets this when it is back.")
            XCTAssertEqual(box.sentStatus, "The notes coordinator is not running. It gets this when it is back.")
        }

        let paddedHead = try head(state: .running, coordinatorPane: "  coord-notes-1  ")
        let paddedBoard = FactoryProjectBoard(head: paddedHead, tasks: [], now: now)
        XCTAssertTrue(paddedBoard.showsCoordinatorButton)
        XCTAssertEqual(FactoryCoordinatorWindow(head: paddedHead).coordinatorPane, "coord-notes-1")
        XCTAssertEqual(paddedBoard.sayBox(text: "Wait on T11").sentStatus, "Told the notes factory.")
    }
}
