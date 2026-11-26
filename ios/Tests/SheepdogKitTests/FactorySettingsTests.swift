import Foundation
import XCTest
@testable import SheepdogKit

/// Unit tests for app-factory PR 11 (`docs/dev/design/app-factory.md` §8, D11, §10 row 11;
/// `docs/dev/design/canvas/slice14-wording.md`; `internal/web/factory.go`,
/// `internal/factory/settings.go`, `internal/factory/flow/settings.go`).
final class FactorySettingsTests: XCTestCase {
    static let sampleRowsJSON = #"""
    {"protocol":1,"rows":[
      {"name":"models.coordinator","label":"coordinator models","value":"claude/opus/medium","default":"claude/opus/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},
      {"name":"models.worker","label":"worker models","value":"claude/sonnet/medium","default":"claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"models":{"agy":["gemini-3.8-flash","gemini-4-carbon","gemini-4-hydrogen","gemini-4-neon"]},"top":true},
      {"name":"models.reviewer","label":"reviewer models","value":"claude/opus/medium, claude/sonnet/medium","default":"claude/opus/medium, claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},
      {"name":"models.retro","label":"retro models","value":"claude/opus/high","default":"claude/opus/high","min":0,"max":0,"clis":["claude","agy"],"top":true},
      {"name":"models.plan_reviewer","label":"plan reviewer models","value":"","default":"","min":0,"max":0,"clis":["claude","agy"],"top":true},
      {"name":"models.integrator","label":"integrator models","value":"codex/gpt-6.2-sol/high","default":"codex/gpt-6.2-sol/high","min":0,"max":0,"clis":["claude","codex"],"top":true},
      {"name":"max_running","label":"max running","value":"3","default":"3","min":1,"max":20,"top":true},
      {"name":"review_rounds","label":"review rounds","value":"2","default":"2","min":1,"max":5},
      {"name":"ci_stalled_minutes","label":"ci stalled minutes","value":"0","default":"90","min":10,"max":600,"problem":"ci stalled minutes 0: allowed 10–600"},
      {"name":"second_reviewer","label":"second reviewer","value":"opus","default":"opus","min":0,"max":0,"allowed":["opus","codex"],"note":"not used while reviewer models are set"},
      {"name":"design_gate","label":"design gate","value":"","default":"","min":0,"max":0,"allowed":["","owner","review"]},
      {"name":"dispatch_workflow","label":"dispatch workflow","value":"","default":"","min":0,"max":0}
    ]}
    """#

    func testSettingRowsDecodeAndModelCandidateParseFormat() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }
        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }

        let rows = try await StubProtocol.client().factorySettings(projectID: "proj-1")
        XCTAssertEqual(rows.filter(\.top).map(\.name), [
            "models.coordinator", "models.worker", "models.reviewer", "models.retro",
            "models.plan_reviewer", "models.integrator", "max_running",
        ])
        let worker = try XCTUnwrap(rows.first { $0.name == "models.worker" })
        XCTAssertEqual(worker.id, "models.worker")
        XCTAssertEqual(worker.label, "worker models")
        XCTAssertEqual(worker.defaultValue, "claude/sonnet/medium")
        XCTAssertEqual(worker.clis, ["claude", "codex", "agy"])
        XCTAssertEqual(worker.models["agy"], ["gemini-3.8-flash", "gemini-4-carbon", "gemini-4-hydrogen", "gemini-4-neon"])
        XCTAssertNil(worker.models["claude"])
        XCTAssertEqual(rows.first { $0.name == "models.retro" }?.clis, ["claude", "agy"])
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.value, "")
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.displayValue, "not set")
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.metaLine, "default not set")
        let stalled = try XCTUnwrap(rows.first { $0.name == "ci_stalled_minutes" })
        XCTAssertEqual(stalled.problem, "ci stalled minutes 0: allowed 10–600")
        XCTAssertEqual(stalled.storedProblemMessage, "The stored value 0 is not allowed (allowed 10–600). Change it before the next start.")
        XCTAssertEqual(rows.first { $0.name == "second_reviewer" }?.note, "not used while reviewer models are set")
        XCTAssertEqual(rows.first { $0.name == "design_gate" }?.allowed, ["", "owner", "review"])
        XCTAssertThrowsError(try DaemonClient.decoder.decode(FactorySettingRow.self, from: Data(#"{"label":"x","value":"1","default":"1","min":0,"max":1}"#.utf8)))

        let parsed = FactoryModelCandidate.parse("agy/gemini-4-neon/high, claude/sonnet/medium, codex/openai/gpt-6.2/high")
        XCTAssertEqual(parsed, [
            FactoryModelCandidate(cli: "agy", model: "gemini-4-neon", effort: "high"),
            FactoryModelCandidate(cli: "claude", model: "sonnet", effort: "medium"),
            FactoryModelCandidate(cli: "codex", model: "openai/gpt-6.2", effort: "high"),
        ])
        XCTAssertEqual(FactoryModelCandidate.format(parsed), "agy/gemini-4-neon/high, claude/sonnet/medium, codex/openai/gpt-6.2/high")
        XCTAssertEqual(FactoryModelCandidate.parse("   "), [])
    }

    func testFactorySettingsGetAndProposeClientRoutes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }
        _ = try await StubProtocol.client().factorySettings(projectID: "a/b%1")
        XCTAssertEqual(StubProtocol.seen.last?.url?.absoluteString.contains("/api/factory/projects/a%2Fb%251/settings"), true)

        let proposeReply = #"{"ok":true,"ask":{"id":"ask-set-1","project":"proj-1","kind":"rule","gated":true,"question":"This project: max running 4 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"4","was":"3"},"asked_by":"engine","at":"2026-11-26T09:00:00Z","status":"open"}}"#
        StubProtocol.handler = { _ in (200, Data(proposeReply.utf8)) }
        let ask = try await DaemonClient.$idempotencyKey.withValue("set-key-1") {
            try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
        }
        let req = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/api/factory/projects/proj-1/settings")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Idempotency-Key"), "set-key-1")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(req.httpBody)), ["name": "max_running", "value": "4"])
        XCTAssertEqual(ask.id, "ask-set-1")
        XCTAssertEqual(ask.kind, "rule")
        XCTAssertEqual(ask.options, ["Save", "Keep current"])
        XCTAssertEqual(ask.set, FactorySettingChange(name: "max_running", value: "4", was: "3"))

        // Real refusals emitted by internal/factory/settings.go & internal/web/factory.go (Opus #5, Carbon §4.1).
        let realRefusals: [(status: Int, name: String, value: String, body: String, expectedText: String)] = [
            (400, "models.retro", "codex/gpt-6.2-sol/high",
             #"{"code":"not_launchable","error":"the engine cannot launch codex as retro yet"}"#,
             "the engine cannot launch codex as retro yet"),
            (400, "max_running", "3",
             #"{"code":"unchanged","error":"max running is already 3"}"#,
             "max running is already 3"),
            (400, "review_rounds", "4",
             #"{"code":"out_of_range","error":"review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."}"#,
             "review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."),
        ]
        for item in realRefusals {
            StubProtocol.handler = { _ in (item.status, Data(item.body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: item.name, value: item.value)
                XCTFail("expected refusal \(item.expectedText)")
            } catch {
                let msg = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertEqual(msg, item.expectedText)
                XCTAssertFalse(msg.contains("The daemon refused") || msg.contains("not_launchable") || msg.contains("out_of_range") || msg.contains("unchanged"))
            }
        }

        StubProtocol.handler = { _ in (404, Data(#"{"code":"not_found","error":"No project \"proj-1\" exists."}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
            XCTFail("expected 404 project gone")
        } catch {
            XCTAssertTrue(FactoryProjectBoard.isProjectGone(error), "404 not_found on proposeFactorySetting must be recognized by isProjectGone")
        }
    }

    /// Fix-round (Opus #2, Carbon §2.3): `writeFactoryRefusal` includes `"fix"` on `not_launchable`
    /// CLI refusals (`mo.r.signin`, `mo.r.install`); `proposeFactorySetting` must not discard it.
    func testProposeSettingPreservesDaemonRefusalFix() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let cases: [(body: String, expectedWhy: String, expectedFixFragment: String)] = [
            (#"{"code":"not_launchable","error":"codex is not signed in","fix":"run: codex login"}"#,
             "codex is not signed in",
             "codex login"),
            (#"{"code":"not_launchable","error":"agy is not installed","fix":"install Antigravity CLI (agy), then run agy without arguments and sign in"}"#,
             "agy is not installed",
             "install Antigravity CLI (agy)"),
        ]

        for item in cases {
            StubProtocol.handler = { _ in (400, Data(item.body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "models.worker", value: "codex/gpt-6.2-sol/medium")
                XCTFail("expected 400 not_launchable with fix")
            } catch {
                let msg = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertTrue(msg.contains(item.expectedWhy), "error message should contain why \(item.expectedWhy.debugDescription), got \(msg.debugDescription)")
                XCTAssertFalse(msg.contains("The daemon refused") || msg.contains("not_launchable"), "got \(msg)")
                let reflected = "\(String(reflecting: error)) | \(msg)"
                XCTAssertTrue(
                    reflected.contains(item.expectedFixFragment),
                    "proposeFactorySetting threw \(String(reflecting: error)), dropping daemon fix \(item.expectedFixFragment.debugDescription)"
                )
            }
        }
    }

    /// Fix-round (Carbon §2.2, Opus What-can-be-deleted #1): all real `403 proof_required` wire shapes
    /// (`handleFactorySettings`, `Engine.ProposeSetting`, and `Supervisor.answerFactoryTap`) must map
    /// to the plain paired-phone recovery sentence without leaking `"proof_required"` or `"The daemon refused"`.
    func testErrorMessageFormatsAllProofRequiredDaemonShapes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let wireBodies = [
            #"{"code":"proof_required","error":"proof_required"}"#,
            #"{"code":"proof_required","error":"Changing a setting requires a paired device or browser session."}"#,
            #"{"error":"proof_required: owner asks require a paired device session"}"#,
        ]

        for body in wireBodies {
            StubProtocol.handler = { _ in (403, Data(body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
                XCTFail("expected 403 proof_required for \(body)")
            } catch {
                let formatted = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertTrue(formatted.contains("Work Mac no longer knows this phone"), "for \(body), got: \(formatted)")
                XCTAssertTrue(formatted.contains("Pair this phone again in Settings ▸ Work Mac"), "for \(body), got: \(formatted)")
                XCTAssertFalse(formatted.contains("proof_required") || formatted.contains("The daemon refused"), "for \(body), leaked: \(formatted)")
            }
        }
    }
}

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

/// Fix-round iOS view & layout unit tests for `FactorySettingsView` (`SheepdogKitTests` hosted in `Sheepdog.app`).
/// Note: never inspect `accessibilityTraits` in unit tests (not readable outside XCUITest).
@MainActor
final class FactorySettingsViewFixRoundTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var cacheDir: URL!
    private weak var activeWindow: UIWindow?

    private static let stateJSON = #"""
    {
      "agents": [],
      "questions": [],
      "factory": [{
        "id": "proj-1",
        "name": "notes",
        "goal": "A notes page with search",
        "state": "running",
        "counts": {"working": 1},
        "folder": "~/code/notes",
        "coordinator_pane": "coord-notes-1"
      }],
      "voice": {"mode": "off", "status": "closed", "muted": false, "stalled": false, "client_connected": false}
    }
    """#

    private static let topRowsForLayoutJSON = #"""
    {"protocol":1,"rows":[
      {"name":"models.worker","label":"worker models","value":"claude/sonnet/medium","default":"claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},
      {"name":"max_running","label":"max running","value":"3","default":"3","min":1,"max":20,"top":true},
      {"name":"task_size_limit","label":"task size limit","value":"400","default":"400","min":50,"max":2000,"top":true},
      {"name":"dispatch_workflow","label":"dispatch workflow","value":"","default":"","min":0,"max":0,"top":true}
    ]}
    """#

    override func setUp() async throws {
        try await super.setUp()
        suiteName = "sheepdog.tests.pr11-fix1-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-pr11-fix1-\(UUID().uuidString)", isDirectory: true)
        SettingsViewStubProtocol.reset()
    }

    override func tearDown() async throws {
        SettingsViewStubProtocol.reset()
        defaults.removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeHarness(
        dynamicType: DynamicTypeSize = .large
    ) throws -> (session: SessionViewModel, computer: ComputerModel, host: UIHostingController<AnyView>, window: UIWindow) {
        let initialState = try DaemonClient.decoder.decode(DaemonState.self, from: Data(Self.stateJSON.utf8))
        let head = try XCTUnwrap(initialState.factory?.first)

        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        settings.address = "127.0.0.1:7799"
        settings.hasBeenConfigured = true
        let session = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

        let endpoint = DaemonEndpoint(host: "127.0.0.1", port: 7777)
        let connection = ComputerConnection(computerID: "work-mac", address: endpoint.display) { _ in
            let config = URLSessionConfiguration.ephemeral
            config.protocolClasses = [SettingsViewStubProtocol.self]
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

        let root = AnyView(
            NavigationStack {
                FactorySettingsView(initialHead: head, computerID: "work-mac", onGone: {})
            }
            .environmentObject(session)
            .environment(\.dynamicTypeSize, dynamicType)
        )
        let host = UIHostingController(rootView: root)
        let frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        let scene = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .first { $0.activationState == .foregroundActive }
            ?? UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let window: UIWindow
        if let scene {
            window = UIWindow(windowScene: scene)
            window.frame = frame
        } else {
            window = UIWindow(frame: frame)
        }
        activeWindow = window
        window.rootViewController = host
        window.makeKeyAndVisible()
        window.frame = frame
        flushSubtreeLayout(window)
        return (session, workMac, host, window)
    }

    private func tearDownHarness(_ session: SessionViewModel, _ computer: ComputerModel, _ window: UIWindow) {
        let previousKey = window.windowScene?.windows.first { $0 !== window && !$0.isHidden }
        computer.stop()
        session.fleet.set([])
        window.isHidden = true
        window.rootViewController = nil
        window.windowScene = nil
        previousKey?.makeKeyAndVisible()
        if activeWindow === window { activeWindow = nil }
        pumpRunLoop(0.02)
    }

    private func pumpRunLoop(_ seconds: TimeInterval = 0.01) {
        RunLoop.main.run(until: Date().addingTimeInterval(seconds))
    }

    /// Fix-round (Opus Should-fix #3): `FactorySettingsView` must match `ask.project == head.id`
    /// only, never `$0.project.isEmpty`, so an ask with an empty or foreign project never attaches.
    func testRowIgnoresOpenAskWithEmptyOrDifferentProjectID() async throws {
        SettingsViewStubProtocol.settingsResponse = (200, Self.topRowsForLayoutJSON)

        let baselineHeight = try await measuredContentHeight(asksJSON: #"{"protocol":1,"asks":[]}"#)
        let ownAskJSON = #"{"protocol":1,"asks":[{"id":"ask-own","project":"proj-1","kind":"rule","gated":true,"question":"This project: max running 4 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"4","was":"3"},"status":"open"}]}"#
        let ownAskHeight = try await measuredContentHeight(asksJSON: ownAskJSON)
        XCTAssertGreaterThan(ownAskHeight, baselineHeight + 80, "positive control: proj-1's own open rule ask expands the row with ApprovalCardView")

        let foreignAndEmptyProjectAsksJSON = #"{"protocol":1,"asks":[{"id":"ask-empty-proj","project":"","kind":"rule","gated":true,"question":"This project: max running 9 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"9","was":"3"},"status":"open"},{"id":"ask-other","project":"proj-2","kind":"rule","gated":true,"question":"This project: max running 8 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"8","was":"3"},"status":"open"}]}"#
        let foreignHeight = try await measuredContentHeight(asksJSON: foreignAndEmptyProjectAsksJSON)
        XCTAssertEqual(
            foreignHeight,
            baselineHeight,
            accuracy: 2.0,
            "an open rule ask with an empty or different project id must never attach to proj-1's row"
        )
    }

    /// Fix-round (Carbon §3.3): if `GET /api/factory/asks` transiently fails during a reload while
    /// `factorySettings(projectID:)` succeeds, `load()` must preserve existing `asks` instead of
    /// overwriting them with `[]` and hiding the open confirmation card.
    func testTransientFactoryAsksErrorPreservesOpenRowAsk() async throws {
        SettingsViewStubProtocol.settingsResponse = (200, Self.topRowsForLayoutJSON)
        let baselineHeight = try await measuredContentHeight(asksJSON: #"{"protocol":1,"asks":[]}"#)

        SettingsViewStubProtocol.reset()
        SettingsViewStubProtocol.settingsResponse = (200, Self.topRowsForLayoutJSON)
        SettingsViewStubProtocol.asksResponse = (
            200,
            #"{"protocol":1,"asks":[{"id":"ask-keep","project":"proj-1","kind":"rule","gated":true,"question":"This project: max running 4 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"4","was":"3"},"status":"open"}]}"#
        )

        let (session, workMac, host, window) = try makeHarness()
        defer { tearDownHarness(session, workMac, window) }

        try await pumpUntil { SettingsViewStubProtocol.settingsRequests >= 1 && SettingsViewStubProtocol.asksRequests >= 1 }
        try await settleLayout(host)
        let scroll = try XCTUnwrap(findSubviews(ofType: UIScrollView.self, in: host.view).first)
        XCTAssertGreaterThan(scroll.contentSize.height, baselineHeight + 80, "open rule ask card is rendered before reload")

        let priorSettingsReqs = SettingsViewStubProtocol.settingsRequests
        let priorAsksReqs = SettingsViewStubProtocol.asksRequests
        SettingsViewStubProtocol.asksResponse = (503, #"{"error":"herdr busy"}"#)
        let refreshedStateJSON = #"""
        {
          "agents": [],
          "questions": [{"id":"fask-ask-keep","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes","question":"This project: max running 4 (now 3). Save?","summary":"Save?","factory":{"project":"proj-1","kind":"rule"},"options":[{"index":1,"label":"Save","default":true,"recommended":true},{"index":2,"label":"Keep current"}]}],
          "factory": [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}],
          "voice": {"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}
        }
        """#
        workMac.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(refreshedStateJSON.utf8)))

        try await pumpUntil {
            SettingsViewStubProtocol.settingsRequests > priorSettingsReqs && SettingsViewStubProtocol.asksRequests > priorAsksReqs
        }
        try await settleLayout(host)

        XCTAssertGreaterThan(
            scroll.contentSize.height,
            baselineHeight + 80,
            "transient failure of GET /api/factory/asks must not wipe self.asks to [] and hide the open confirmation card"
        )
    }

    /// Fix-round (Opus #6, Carbon §2.4.1 & §2.4.3 & §3.1):
    /// 1. Wide-range numeric rows (`task_size_limit` 50–2000) expose an editable `UITextField` so
    ///    changing 400 -> 800 does not require 400 taps.
    /// 2. At `.accessibility5` Dynamic Type, row controls stack vertically under their labels
    ///    (leading-aligned rather than squeezed on the right of an `HStack`), and the free-text
    ///    `dispatch_workflow` field is not clipped to `maxWidth: 160`.
    func testAccessibilityDynamicTypeStacksRowsExpandsTextFieldAndNumericRowsAreEditable() async throws {
        SettingsViewStubProtocol.settingsResponse = (200, Self.topRowsForLayoutJSON)
        SettingsViewStubProtocol.asksResponse = (200, #"{"protocol":1,"asks":[]}"#)

        let (session, workMac, host, window) = try makeHarness(dynamicType: .accessibility5)
        defer { tearDownHarness(session, workMac, window) }

        try await pumpUntil {
            SettingsViewStubProtocol.settingsRequests >= 1 && !self.findSubviews(ofType: UITextField.self, in: host.view).isEmpty
        }
        try await settleLayout(host)

        let textFields = findSubviews(ofType: UITextField.self, in: host.view)
        let workflowField = try XCTUnwrap(
            textFields.first { $0.placeholder == "not set" },
            "dispatch_workflow free-text row must render a TextField with placeholder 'not set'"
        )
        let workflowFrame = workflowField.convert(workflowField.bounds, to: host.view)
        XCTAssertLessThan(
            workflowFrame.minX,
            80,
            "at .accessibility5 Dynamic Type, row control must stack under its label (leading-aligned) rather than sitting on the right of an HStack (got minX=\(workflowFrame.minX))"
        )
        XCTAssertGreaterThan(
            workflowFrame.width,
            200,
            "at .accessibility5 Dynamic Type, dispatch_workflow TextField must not be capped at maxWidth: 160 (got width=\(workflowFrame.width))"
        )

        XCTAssertTrue(
            textFields.contains { $0.text == "400" },
            "wide-range numeric row task_size_limit (50–2000) must include an editable TextField showing '400' so owners can type values directly"
        )
    }

    private func measuredContentHeight(asksJSON: String) async throws -> CGFloat {
        SettingsViewStubProtocol.asksRequests = 0
        SettingsViewStubProtocol.settingsRequests = 0
        SettingsViewStubProtocol.asksResponse = (200, asksJSON)

        let (session, workMac, host, window) = try makeHarness()
        defer { tearDownHarness(session, workMac, window) }

        try await pumpUntil { SettingsViewStubProtocol.settingsRequests >= 1 && SettingsViewStubProtocol.asksRequests >= 1 }
        try await settleLayout(host)
        let scroll = try XCTUnwrap(findSubviews(ofType: UIScrollView.self, in: host.view).first)
        return scroll.contentSize.height
    }

    private func flushSubtreeLayout(_ root: UIView) {
        root.setNeedsLayout()
        root.layoutIfNeeded()
        for subview in root.subviews {
            flushSubtreeLayout(subview)
        }
    }

    private func settleLayout(_ host: UIHostingController<AnyView>) async throws {
        for _ in 0 ..< 5 {
            flushSubtreeLayout(host.view.window ?? host.view)
            pumpRunLoop(0.02)
            try await Task.sleep(nanoseconds: 15_000_000)
        }
        flushSubtreeLayout(host.view.window ?? host.view)
    }

    private func findSubviews<T: UIView>(ofType type: T.Type, in root: UIView) -> [T] {
        var result: [T] = []
        if let match = root as? T { result.append(match) }
        for subview in root.subviews {
            result.append(contentsOf: findSubviews(ofType: type, in: subview))
        }
        return result
    }

    private func pumpUntil(timeout: TimeInterval = 3.0, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        if let activeWindow { flushSubtreeLayout(activeWindow) }
        while !condition() {
            if Date() > deadline { throw URLError(.timedOut) }
            pumpRunLoop(0.01)
            try await Task.sleep(nanoseconds: 10_000_000)
            if let activeWindow { flushSubtreeLayout(activeWindow) }
        }
    }
}

private final class SettingsViewStubProtocol: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        settingsResponse: (200, #"{"protocol":1,"rows":[]}"#),
        asksResponse: (200, #"{"protocol":1,"asks":[]}"#),
        settingsRequests: 0,
        asksRequests: 0
    )

    static var settingsResponse: (Int, String) {
        get { lock.withLock { state.settingsResponse } }
        set { lock.withLock { state.settingsResponse = newValue } }
    }

    static var asksResponse: (Int, String) {
        get { lock.withLock { state.asksResponse } }
        set { lock.withLock { state.asksResponse = newValue } }
    }

    static var settingsRequests: Int {
        get { lock.withLock { state.settingsRequests } }
        set { lock.withLock { state.settingsRequests = newValue } }
    }

    static var asksRequests: Int {
        get { lock.withLock { state.asksRequests } }
        set { lock.withLock { state.asksRequests = newValue } }
    }

    static func reset() {
        lock.withLock {
            state = (
                settingsResponse: (200, #"{"protocol":1,"rows":[]}"#),
                asksResponse: (200, #"{"protocol":1,"asks":[]}"#),
                settingsRequests: 0,
                asksRequests: 0
            )
        }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let url = request.url ?? URL(string: "http://127.0.0.1:7777")!
        if url.path.hasSuffix("/settings") {
            let reply = Self.lock.withLock { () -> (Int, String) in
                Self.state.settingsRequests += 1
                return Self.state.settingsResponse
            }
            finish(url: url, status: reply.0, body: Data(reply.1.utf8))
            return
        }
        if url.path == "/api/factory/asks" {
            let reply = Self.lock.withLock { () -> (Int, String) in
                Self.state.asksRequests += 1
                return Self.state.asksResponse
            }
            finish(url: url, status: reply.0, body: Data(reply.1.utf8))
            return
        }
        if url.path == "/api/state" {
            finish(url: url, status: 200, body: Data(#"{"agents":[],"questions":[],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#.utf8))
            return
        }
        finish(url: url, status: 404, body: Data(#"{"error":"not found"}"#.utf8))
    }

    override func stopLoading() {}

    private func finish(url: URL, status: Int, body: Data) {
        guard let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"]) else { return }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
}
#endif
