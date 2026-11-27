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

    /// Fix-round (Opus B1 & Carbon 1/2): `resumeFactory` + `FactoryAgentsList.resumeErrorMessage`
    /// on a real HTTP 400 start-check reply must return `ac.refused` ("…press Resume again."),
    /// never Foundation's `localizedDescription` fallback; `FactoryStartChecksError.localizedDescription`
    /// must not leak NSError domain/code; and `statusWord` covers `ok`/`warn`/`fail`/`skip`.
    func testResumeStartCheckFailureUsesPlainRefusalInsteadOfLocalizedDescription() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let daemonChecksJSON = #"""
        {"error":"not starting; fix these first (sheepdog factory doctor /work/notes shows every check):\n✗ start claude: claude is not signed in — fix: run: claude auth login",
         "results":[
           {"name":"git version","status":"ok","detail":"git 2.47"},
           {"name":"start claude","status":"fail","detail":"claude is not signed in","fix":"run: claude auth login"},
           {"name":"review rule","status":"warn","detail":"branch protection optional"},
           {"name":"retro cli","status":"skip","detail":"not configured"}
         ]}
        """#
        StubProtocol.handler = { _ in (400, Data(daemonChecksJSON.utf8)) }

        do {
            try await StubProtocol.client().resumeFactory(folder: "/work/notes", goal: "A notes page with search")
            XCTFail("expected FactoryStartChecksError on 400 results")
        } catch {
            let checksErr = try XCTUnwrap(error as? FactoryStartChecksError)
            XCTAssertEqual(checksErr.results.map(\.statusWord), ["pass", "fail", "warning", "skipped"])

            let msg = FactoryAgentsList.resumeErrorMessage(error, computerName: "Work Mac")
            XCTAssertEqual(msg, "Not started: a start check failed. Fix it, then press Resume again.")
            XCTAssertFalse(msg.contains("operation couldn"), "must not leak Foundation NSError text: \(msg)")
            XCTAssertFalse(msg.contains("FactoryStartChecksError"), "must not leak Swift type name: \(msg)")
            XCTAssertFalse(error.localizedDescription.contains("FactoryStartChecksError"), error.localizedDescription)
            XCTAssertFalse(error.localizedDescription.contains("operation couldn"), error.localizedDescription)
        }

        XCTAssertEqual(
            FactoryAgentsList.resumeErrorMessage(
                DaemonError.rejected(status: 400, message: "Not started: a setting is not allowed. Change it in settings below."),
                computerName: "Work Mac"
            ),
            "Not started: a setting is not allowed. Change it in settings below."
        )
        XCTAssertEqual(
            FactoryAgentsList.resumeErrorMessage(
                DaemonError.rejected(status: 404, message: "factory project not found"),
                computerName: "Work Mac"
            ),
            "This project is gone."
        )
    }
}

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

/// Fix-round view unit tests for PR 10: running-project `New goal` reloads `factoryGraph`
/// even without SSE `factory.changed`, and `FactoryGoalSheet` keeps submit/Cancel inside its
/// `ScrollView` at Accessibility Dynamic Type.
@MainActor
final class FactoryPR10ViewFixTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        suiteName = "sheepdog.tests.pr10-fix-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent(suiteName, isDirectory: true)
        PR10FixStub.reset()
    }

    override func tearDown() async throws {
        PR10FixStub.reset()
        defaults.removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeSessionAndComputer(initialStateJSON: String) throws -> (SessionViewModel, ComputerModel) {
        let initialState = try DaemonClient.decoder.decode(DaemonState.self, from: Data(initialStateJSON.utf8))
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        settings.address = "127.0.0.1:7799"
        settings.hasBeenConfigured = true
        let session = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

        let endpoint = DaemonEndpoint(host: "127.0.0.1", port: 7777)
        let connection = ComputerConnection(computerID: "work-mac", address: endpoint.display) { _ in
            let config = URLSessionConfiguration.ephemeral
            config.protocolClasses = [PR10FixStub.self]
            return DaemonClient(endpoint: endpoint, session: URLSession(configuration: config))
        }
        let workMac = ComputerModel(
            id: "work-mac",
            name: "Work Mac",
            addresses: [endpoint.display],
            certExpires: nil,
            connection: connection,
            scheduler: ManualLinkClock(),
            demand: LinkDemand(isCurrent: false)
        )
        workMac.accept(initialState)
        session.fleet.set([workMac])
        return (session, workMac)
    }

    func testRunningProjectGoalChangeReloadsGraphWithoutFactoryChangedEvent() async throws {
        let stateV1 = #"""
        {"agents":[],"questions":[],"factory":[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1},"folder":"~/code/notes","coordinator_pane":"coord-1"}],
         "voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        let stateV2 = #"""
        {"agents":[],"questions":[],"factory":[{"id":"proj-1","name":"notes","goal":"Add keyboard shortcuts","state":"running","counts":{"working":1},"folder":"~/code/notes","coordinator_pane":"coord-1"}],
         "voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        PR10FixStub.graphJSON = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T1","title":"feat: search","stage":"working","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"implementing","state":"now"}]}]}"#

        let (session, workMac) = try makeSessionAndComputer(initialStateJSON: stateV1)
        let initialHead = try XCTUnwrap(workMac.factoryHeads.first)
        let root = NavigationStack {
            FactoryProjectView(initialHead: initialHead, computerID: "work-mac")
        }.environmentObject(session)

        let host = UIHostingController(rootView: root)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        defer {
            workMac.stop()
            session.fleet.set([])
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        let initialLoaded = await waitBriefly { PR10FixStub.graphRequests >= 1 }
        XCTAssertTrue(initialLoaded)
        XCTAssertEqual(PR10FixStub.graphRequests, 1)

        // Simulate `await computer.refresh()` after submitting New goal on an already-running
        // project when `/events` is closed (Low Data Mode) or delayed (`factoryChanges` stays 0).
        PR10FixStub.graphJSON = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T2","title":"feat: shortcuts","stage":"queued","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]}]}"#
        workMac.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(stateV2.utf8)))
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()

        let reloaded = await waitBriefly { PR10FixStub.graphRequests >= 2 }
        XCTAssertTrue(reloaded, "changing head.goal on an already-running project must reload factoryGraph even without a factory.changed SSE event")
    }

    func testGoalSheetButtonsScrollInsideScrollViewAtAccessibilityDynamicType() throws {
        let finishedState = #"""
        {"agents":[],"questions":[],"factory":[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"finished","counts":{"done":2},"folder":"~/code/notes"}],
         "voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        let (session, workMac) = try makeSessionAndComputer(initialStateJSON: finishedState)
        let finishedHead = try XCTUnwrap(workMac.factoryHeads.first)
        defer {
            workMac.stop()
            session.fleet.set([])
        }

        let axView = FactoryGoalSheet(head: finishedHead, computer: workMac, onGone: {})
            .environment(\.dynamicTypeSize, .accessibility5)
        let axHost = UIHostingController(rootView: axView)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 320, height: 480))
        window.rootViewController = axHost
        window.makeKeyAndVisible()
        axHost.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        let axScroll = try XCTUnwrap(findSubview(ofType: UIScrollView.self, in: axHost.view))
        XCTAssertLessThan(
            axScroll.adjustedContentInset.bottom,
            60,
            "At Accessibility Dynamic Type sizes, FactoryGoalSheet must keep submit and Cancel inside the ScrollView rather than reserving a large sticky bottom safeAreaInset"
        )
    }

    private func findSubview<T: UIView>(ofType type: T.Type, in view: UIView) -> T? {
        if let matched = view as? T { return matched }
        for subview in view.subviews {
            if let found = findSubview(ofType: type, in: subview) { return found }
        }
        return nil
    }

    private func waitBriefly(timeout: TimeInterval = 0.8, _ condition: () -> Bool) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            if Date() > deadline { return false }
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        return true
    }
}

private final class PR10FixStub: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (graphJSON: "{}", graphRequests: 0)

    static var graphJSON: String {
        get { lock.withLock { state.graphJSON } }
        set { lock.withLock { state.graphJSON = newValue } }
    }

    static var graphRequests: Int {
        lock.withLock { state.graphRequests }
    }

    static func reset() {
        lock.withLock { state = (graphJSON: "{}", graphRequests: 0) }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let url = request.url ?? URL(string: "http://127.0.0.1:7777")!
        if url.path.hasSuffix("/graph") {
            let body = Self.lock.withLock { () -> String in
                Self.state.graphRequests += 1
                return Self.state.graphJSON
            }
            guard let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"]) else { return }
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(body.utf8))
            client?.urlProtocolDidFinishLoading(self)
            return
        }
        guard let response = HTTPURLResponse(url: url, statusCode: 404, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"]) else { return }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"error":"not found"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}
#endif
