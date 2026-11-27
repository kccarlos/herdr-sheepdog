import Foundation
import XCTest
@testable import SheepdogKit

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

/// Parked PR 11b view & layout unit tests moved out of `ios/Tests/SheepdogKitTests/FactorySettingsTests.swift`.
/// Requires PR 11b (`pr11b-on-11a.patch`: `/api/factory/asks` loading, embedded `ApprovalCardView` on rows,
/// editable numeric & free-text `UITextField` controls).
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
