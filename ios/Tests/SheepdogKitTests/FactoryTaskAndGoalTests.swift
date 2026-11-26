import Foundation
import XCTest
@testable import SheepdogKit

/// Failing unit tests for app-factory PR 10 (`docs/dev/design/app-factory.md`
/// §4, §5.3, §8, D8, D10–D11, §10 row 10; `FactoryTask`, `FactoryProject`,
/// `FactoryProjectActions`; `slice14-wording.md`; `internal/web/factory.go`).
final class FactoryTaskAndGoalTests: XCTestCase {
    private let now = RFC3339Date.date(from: "2026-11-25T11:32:00Z")!

    private func head(
        id: String = "proj-1", name: String = "notes", goal: String = "A notes page with search",
        state: FactoryProjectState = .running, counts: [String: Int] = [:], folder: String? = "~/code/notes"
    ) throws -> FactoryProjectHead {
        var o: [String: Any] = ["id": id, "name": name, "goal": goal, "state": state.rawValue, "counts": counts]
        folder.map { o["folder"] = $0 }
        return try DaemonClient.decoder.decode(FactoryProjectHead.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func decodeTask(_ json: String) throws -> FactoryTask {
        try DaemonClient.decoder.decode(FactoryTask.self, from: Data(json.utf8))
    }

    /// Task view model: steps + VoiceOver, run role/cli/model/elapsed (suppressed when
    /// stopped/finished), last decision + why, rounds, dependencies, brief, no worker window.
    func testTaskDetailStepsRunLastDecisionRoundsBriefAndNoWorkerWindow() throws {
        let task = try decodeTask(#"{"ref":"T8","title":"feat: tags","brief":"Tags on a note, and a tag filter on the notes page.","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":["T11"],"layer":1,"rounds":2,"model":"gpt-6.2-sol","terminal":"w-8","run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z","last_actor_decision":{"at":"2026-11-25T11:05:00Z","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},"steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo","text":"needs 2 passing, at most 5"},{"name":"CI","state":"skipped","text":"skipped by your override"},{"name":"merged","state":"todo"}]}"#)
        let detail = FactoryTaskDetail(task: task, projectName: "notes", projectState: .running, now: now)
        XCTAssertEqual(detail.stageLabel, "working")
        XCTAssertEqual(detail.afterLine, "after T6")
        XCTAssertEqual(detail.runLine, "worker · codex · gpt-6.2-sol · 31 min")
        XCTAssertEqual(detail.runAccessibilityLabel, "Running now: worker, codex, gpt-6.2-sol, started 31 minutes ago")
        XCTAssertEqual(detail.roundsLine, "2 review rounds")
        XCTAssertEqual(detail.brief, "Tags on a note, and a tag filter on the notes page.")
        XCTAssertFalse(detail.showsWorkerWindowButton, "worker/reviewer windows are not in v1 even when terminal is set")
        XCTAssertFalse(detail.isEmptySteps)

        XCTAssertEqual(detail.steps.map(\.stateWord), ["done", "now", "to come", "skipped", "to come"])
        XCTAssertEqual(detail.steps[0].accessibilityLabel, "planned, done")
        XCTAssertEqual(detail.steps[1].accessibilityLabel, "implementing, now: sent back from review round 1; 1 round done")
        XCTAssertEqual(detail.steps[2].accessibilityLabel, "review, to come: needs 2 passing, at most 5")
        XCTAssertEqual(detail.steps[3].accessibilityLabel, "CI, skipped: skipped by your override")

        let last = try XCTUnwrap(detail.lastDecision)
        XCTAssertEqual(last.heading, "Last decision")
        XCTAssertEqual(last.actorLabel, "coordinator")
        XCTAssertEqual(last.decision, "sent back")
        XCTAssertEqual(last.whyLine, "Why: the tag index must be case-insensitive")
        XCTAssertEqual(last.at, RFC3339Date.date(from: "2026-11-25T11:05:00Z"))
        XCTAssertTrue(last.accessibilityLabel.contains("coordinator") && last.accessibilityLabel.contains("sent back") && last.accessibilityLabel.contains("the tag index must be case-insensitive"))

        XCTAssertNil(FactoryTaskDetail(task: task, projectName: "notes", projectState: .stopped, now: now).runLine)
        XCTAssertNil(FactoryTaskDetail(task: task, projectName: "notes", projectState: .finished, now: now).runLine)
    }

    /// Dropped/closed tasks, owner decision wording (`you`), singular review round,
    /// and empty states (no steps, no brief, no run, no last decision).
    func testTaskDetailDroppedClosedOwnerDecisionAndEmptyStates() throws {
        let dropped = try decodeTask(#"{"ref":"T9","title":"feat: export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":1,"last_actor_decision":{"at":"2026-11-25T11:04:00Z","by":"owner","decision":"answered ask-1"},"steps":[{"name":"planned","state":"done"},{"name":"dropped","state":"ended","text":"ended without merging"}]}"#)
        let droppedDetail = FactoryTaskDetail(task: dropped, projectName: "notes", projectState: .finished, now: now)
        XCTAssertEqual(droppedDetail.stageLabel, "done")
        XCTAssertNil(droppedDetail.afterLine)
        XCTAssertNil(droppedDetail.runLine)
        XCTAssertNil(droppedDetail.runAccessibilityLabel)
        XCTAssertEqual(droppedDetail.roundsLine, "1 review round")
        XCTAssertNil(droppedDetail.brief)
        XCTAssertEqual(droppedDetail.steps.last?.stateWord, "ended")
        XCTAssertEqual(droppedDetail.steps.last?.accessibilityLabel, "dropped, ended: ended without merging")
        let ownerLast = try XCTUnwrap(droppedDetail.lastDecision)
        XCTAssertEqual(ownerLast.actorLabel, "you")
        XCTAssertNil(ownerLast.whyLine)
        XCTAssertFalse(ownerLast.accessibilityLabel.contains("owner"))

        let empty = try decodeTask(#"{"ref":"T11","title":"feat: sync","brief":"  \n ","stage":"waiting_on_dep","after":["T7","T8"],"dependents":[],"layer":2,"rounds":0,"steps":[]}"#)
        let emptyDetail = FactoryTaskDetail(task: empty, projectName: "notes", projectState: .running, now: now)
        XCTAssertEqual(emptyDetail.stageLabel, "waiting on others")
        XCTAssertEqual(emptyDetail.afterLine, "after T7, T8")
        XCTAssertNil(emptyDetail.roundsLine)
        XCTAssertNil(emptyDetail.brief)
        XCTAssertNil(emptyDetail.lastDecision)
        XCTAssertTrue(emptyDetail.isEmptySteps)
        XCTAssertEqual(emptyDetail.emptyStepsMessage, "No steps recorded yet.")
    }

    /// Board & goal form wording for running/stopped New goal and finished Reopen
    /// (slice14-wording.md, FactoryProject.dc.html, D11).
    func testGoalFormWordingValidationAndErrorMappingAcrossProjectStates() throws {
        let running = FactoryProjectBoard(head: try head(state: .running), tasks: [])
        XCTAssertTrue(running.canChangeGoal)
        XCTAssertEqual(running.goalActionLabel, "New goal")
        let runForm = running.goalForm(draft: "  ")
        XCTAssertFalse(runForm.isReopen || runForm.canSubmit)
        XCTAssertEqual(runForm.title, "New goal")
        XCTAssertEqual(runForm.fieldLabel, "New goal")
        XCTAssertEqual(runForm.hint, "The folder and the settings stay as they are. The coordinator plans the new goal.")
        XCTAssertEqual(runForm.quotaHint, "Starting runs agents on your model accounts. They use your quota.")
        XCTAssertEqual(runForm.submitLabel, "Give it this goal")
        XCTAssertEqual(runForm.cancelLabel, "Cancel")
        XCTAssertEqual(runForm.emptyValidationMessage, "Write a goal first.")

        let stopForm = FactoryProjectBoard(head: try head(state: .stopped), tasks: []).goalForm(draft: "Add tags")
        XCTAssertEqual(stopForm.submitLabel, "Start with this goal")
        XCTAssertTrue(stopForm.canSubmit)

        let finished = FactoryProjectBoard(head: try head(state: .finished), tasks: [])
        XCTAssertTrue(finished.canChangeGoal)
        XCTAssertEqual(finished.goalActionLabel, "Reopen with a new goal")
        XCTAssertEqual(finished.barNote, "Finished. To go on, reopen it with a new goal.")
        let reopenForm = finished.goalForm(draft: "Add dark mode")
        XCTAssertTrue(reopenForm.isReopen && reopenForm.canSubmit)
        XCTAssertEqual(reopenForm.title, "Reopen with a new goal")
        XCTAssertEqual(reopenForm.hint, "The folder and the settings stay as they are. To change a model first, use settings.")
        XCTAssertEqual(reopenForm.submitLabel, "Reopen with this goal")

        let noFolder = FactoryProjectBoard(head: try head(state: .finished, folder: nil), tasks: [])
        XCTAssertFalse(noFolder.canChangeGoal || noFolder.goalForm(draft: "Add dark mode").canSubmit)

        let checkErr = FactoryStartChecksError(message: "start checks failed", results: [
            FactoryStartCheck(name: "git version", status: "ok", detail: "git 2.47", fix: nil),
            FactoryStartCheck(name: "start claude", status: "fail", detail: "claude is not signed in", fix: "run: claude auth login"),
        ])
        XCTAssertEqual(checkErr.results.map(\.statusWord), ["pass", "fail"])
        XCTAssertEqual(FactoryGoalForm.errorMessage(checkErr, state: .stopped, computerName: "Work Mac"), "Not started: a start check failed. Fix it, then press New goal again.")
        XCTAssertEqual(FactoryGoalForm.errorMessage(checkErr, state: .finished, computerName: "Work Mac"), "Not started: a start check failed. Fix it, then press Reopen again.")
        XCTAssertEqual(FactoryGoalForm.errorMessage(DaemonError.rejected(status: 409, message: "This project is finished. Give it a new goal."), state: .finished, computerName: "Work Mac"), "This project is finished. Give it a new goal.")
        XCTAssertEqual(FactoryGoalForm.errorMessage(DaemonError.rejected(status: 400, message: "Not started: a setting is not allowed. Change it in settings below."), state: .stopped, computerName: "Work Mac"), "Not started: a setting is not allowed. Change it in settings below.")
        XCTAssertEqual(FactoryGoalForm.errorMessage(DaemonError.rejected(status: 403, message: "proof_required"), state: .finished, computerName: "Work Mac"), "Not started: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then press Reopen again.")
        XCTAssertEqual(FactoryGoalForm.errorMessage(DaemonError.rejected(status: 404, message: "factory project not found"), state: .running, computerName: "Work Mac"), "This project is gone.")
    }

    /// Real `POST /api/factory/projects/start` wire shapes (`internal/web/factory.go`):
    /// success, 409 `already_closed`, 400 `results` start checks, and 403 `proof_required`.
    func testStartFactoryRoutePostsGoalAndDecodesRealDaemonRefusals() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"device_id":"dev-1"}"#.utf8)) }
        try await DaemonClient.$idempotencyKey.withValue("goal-1") { try await StubProtocol.client().startFactory(folder: "/work/notes", goal: "Add dark mode") }
        let req = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/api/factory/projects/start")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Idempotency-Key"), "goal-1")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(req.httpBody)), ["folder": "/work/notes", "goal": "Add dark mode"])

        StubProtocol.handler = { _ in (409, Data(#"{"code":"already_closed","error":"This project is finished. Give it a new goal."}"#.utf8)) }
        do { try await StubProtocol.client().startFactory(folder: "/work/notes", goal: "A notes page with search"); XCTFail("expected 409") }
        catch { XCTAssertEqual(error as? DaemonError, .rejected(status: 409, message: "This project is finished. Give it a new goal.")) }

        let checksJSON = #"{"error":"start checks failed","results":[{"name":"git version","status":"ok","detail":"git 2.47"},{"name":"start claude","status":"fail","detail":"claude is not signed in","fix":"run: claude auth login"}]}"#
        StubProtocol.handler = { _ in (400, Data(checksJSON.utf8)) }
        do { try await StubProtocol.client().startFactory(folder: "/work/notes", goal: "Add dark mode"); XCTFail("expected start checks error") }
        catch let err as FactoryStartChecksError {
            XCTAssertEqual(err.results.count, 2)
            XCTAssertEqual(err.results[1], FactoryStartCheck(name: "start claude", status: "fail", detail: "claude is not signed in", fix: "run: claude auth login"))
        }

        StubProtocol.handler = { _ in (403, Data(#"{"code":"proof_required","error":"proof_required"}"#.utf8)) }
        do { try await StubProtocol.client().startFactory(folder: "/work/notes", goal: "Add dark mode"); XCTFail("expected 403") }
        catch { XCTAssertEqual(error as? DaemonError, .rejected(status: 403, message: "proof_required")) }
    }
}
