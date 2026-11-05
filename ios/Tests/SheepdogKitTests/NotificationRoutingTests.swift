#if os(iOS)
import Foundation
import SwiftUI
import UIKit
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Notification actions, multi-computer tap routing, foreground suppression,
/// and per-computer push registration state (audit findings 6, 7, and 8).
@MainActor
final class NotificationRoutingTests: XCTestCase {
    private let suite = "sheepdog.tests.notification-routing"
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-notif-\(UUID().uuidString)", isDirectory: true)
        ActionRaceStub.reset()
    }

    override func tearDown() async throws {
        ActionRaceStub.reset()
        defaults.removePersistentDomain(forName: suite)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeModel() -> SessionViewModel {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        return SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))
    }

    // MARK: - Finding 6: Cold background action candidate endpoints & staggered answer

    func testCandidateEndpointsOrdersRememberedWinnerThenPreferredThenRemaining() {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let computer = PairedComputer(
            daemonID: "home",
            name: "Home Mac",
            addresses: ["192.0.2.10:7777", "198.51.100.20:7777", "203.0.113.30:7777"],
            pin: "pin_test_home",
            deviceID: "dev-1",
            pairedAt: Date()
        )
        store.save(computer, address: "192.0.2.10:7777")
        LinkWinnerDefaults(defaults: defaults).save([.cellular: "203.0.113.30:7777"], computerID: "home")

        let cellularOrder = store.candidateEndpoints(for: "home", interface: .cellular).map(\.display)
        XCTAssertEqual(cellularOrder, ["203.0.113.30:7777", "192.0.2.10:7777", "198.51.100.20:7777"])

        let unknownInterfaceOrder = store.candidateEndpoints(for: "home", interface: nil).map(\.display)
        XCTAssertEqual(unknownInterfaceOrder, ["192.0.2.10:7777", "198.51.100.20:7777", "203.0.113.30:7777"])
    }

    func testColdBackgroundAnswerRacesAddressesAndAnswersOnceOnWinner() async throws {
        let creds = DeviceCredentials(daemonID: "home", secrets: InMemorySecretStore())
        creds.computer = PairedComputer(
            daemonID: "home",
            name: "Home Mac",
            addresses: ["192.0.2.10:7777", "198.51.100.20:7777"],
            pin: "pin_test",
            deviceID: "dev-1",
            pairedAt: Date()
        )
        creds.adopt(SessionTicket(ticket: "tk", expires: Date().addingTimeInterval(3600)))
        let lan = try XCTUnwrap(DaemonEndpoint(text: "192.0.2.10:7777", secure: true, credentials: creds))
        let remote = try XCTUnwrap(DaemonEndpoint(text: "198.51.100.20:7777", secure: true, credentials: creds))
        ActionRaceStub.unreachableHosts = ["192.0.2.10"]

        let winner = try await PushActions.answerPending(
            id: "ap-1",
            option: 1,
            endpoints: [lan, remote],
            stagger: 0.02,
            makeClient: { endpoint in
                let config = URLSessionConfiguration.ephemeral
                config.protocolClasses = [ActionRaceStub.self]
                let session = URLSession(configuration: config)
                return DaemonClient(endpoint: endpoint, session: session)
            }
        )

        XCTAssertEqual(winner.display, "198.51.100.20:7777")
        XCTAssertEqual(ActionRaceStub.answeredHosts, ["198.51.100.20"], "POST /api/pending/answer should run once on the winning endpoint")
    }

    // MARK: - Finding 7: Notification tap routing across computers and iPad

    func testOpenInsteadUserInfoPreservesComputerID() throws {
        let payload = PushPayload(
            kind: "approval",
            paneID: "w1:p1",
            agent: "auth",
            pendingID: "ap-1",
            computer: "home-id",
            options: [PendingOption(index: 1, label: "Yes")]
        )
        let roundTrip = try XCTUnwrap(PushPayload(userInfo: payload.openInsteadUserInfo()))
        XCTAssertEqual(roundTrip.computer, "home-id")
        XCTAssertEqual(roundTrip.paneID, "w1:p1")
        XCTAssertEqual(roundTrip.agent, "auth")
    }

    func testFocusAgentRoutesToSecondaryComputerUncollapsesAndExpandsNamespacedCard() {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pin_w", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "pin_h", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()

        // Collapse Home Mac and push a terminal route on the stack first.
        model.agentsModel.toggleComputer("home-id")
        XCTAssertFalse(model.agentsModel.isComputerExpanded("home-id"))
        model.navigation.path.append(AgentWindowRoute(paneID: "%1"))

        model.focusAgent(AgentFocus(computerID: "home-id", paneID: "%1"))

        XCTAssertEqual(model.navigation.pane, .flock)
        XCTAssertTrue(model.navigation.path.isEmpty, "focusing an agent card should pop any pushed terminal route")
        XCTAssertEqual(model.navigation.focusedAgent, AgentFocus(computerID: "home-id", paneID: "%1"))
        XCTAssertTrue(model.agentsModel.isComputerExpanded("home-id"), "collapsed target computer section should open")
        XCTAssertTrue(model.agentsModel.expandedAgents.contains("home-id|%1"), "remote computer card should expand under its namespaced key")
        XCTAssertFalse(model.agentsModel.expandedAgents.contains("%1"), "current computer's colliding %1 card should not be expanded")
    }

    func testIPadRegularWidthSwitchesDetailToAllAgentsWhenFocusingAgent() {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pin_w", deviceID: "dw", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()

        let root = MainView()
            .environmentObject(model)
            .environment(\.horizontalSizeClass, .regular)
            .environment(\.appPhase, .active)
        let host = UIHostingController(rootView: root)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        XCTAssertFalse(model.viewingAgentsScreen, "iPad starts on Conversation detail")

        model.focusAgent(AgentFocus(computerID: "work-id", paneID: "%1"))
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))

        XCTAssertTrue(model.viewingAgentsScreen, "focusing an agent on regular-width iPad should switch detail to All agents")
    }

    // MARK: - Finding 8: Foreground banner suppression, multi-computer registration & retry on .online

    func testForegroundPresentationPolicyPerComputerAndScreen() {
        let approvalOnHome = PushPayload(
            kind: "approval",
            paneID: "h1:p1",
            pendingID: "q1",
            computer: "home-id",
            options: [PendingOption(index: 1, label: "Yes")]
        )
        let doneOnWork = PushPayload(kind: "done", paneID: "w1:p1", computer: "work-id")
        let approvalOnWork = PushPayload(
            kind: "approval",
            paneID: "w1:p1",
            pendingID: "q2",
            computer: "work-id",
            options: [PendingOption(index: 1, label: "Yes")]
        )
        let doneOnHome = PushPayload(kind: "done", paneID: "h1:p1", computer: "home-id")

        // Secondary computer offline / stream closed while current computer is online -> DO NOT suppress.
        XCTAssertFalse(PushPolicy.shouldSuppressForegroundBanner(
            payload: approvalOnHome,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: true,
            viewingAgents: true,
            otherComputerEventsOpen: false
        ))

        // Secondary computer stream open + answerable approval -> suppress (floating remote card shows it).
        XCTAssertTrue(PushPolicy.shouldSuppressForegroundBanner(
            payload: approvalOnHome,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: false,
            viewingAgents: false,
            otherComputerEventsOpen: true
        ))

        // Secondary computer stream open + non-question done while on Conversation -> DO NOT suppress.
        XCTAssertFalse(PushPolicy.shouldSuppressForegroundBanner(
            payload: doneOnHome,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: true,
            viewingAgents: false,
            otherComputerEventsOpen: true
        ))

        // Current computer online, voice off, on Conversation:
        // - approval -> suppress (floating card shows it)
        // - done -> DO NOT suppress (no card and voice is off)
        XCTAssertTrue(PushPolicy.shouldSuppressForegroundBanner(
            payload: approvalOnWork,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: false,
            viewingAgents: false,
            otherComputerEventsOpen: false
        ))
        XCTAssertFalse(PushPolicy.shouldSuppressForegroundBanner(
            payload: doneOnWork,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: false,
            viewingAgents: false,
            otherComputerEventsOpen: false
        ))

        // Current computer done while viewing Agents OR while voice is connected -> suppress.
        XCTAssertTrue(PushPolicy.shouldSuppressForegroundBanner(
            payload: doneOnWork,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: false,
            viewingAgents: true,
            otherComputerEventsOpen: false
        ))
        XCTAssertTrue(PushPolicy.shouldSuppressForegroundBanner(
            payload: doneOnWork,
            currentComputerID: "work-id",
            currentLinkUsable: true,
            currentEventsOpen: true,
            voiceConnected: true,
            viewingAgents: false,
            otherComputerEventsOpen: false
        ))
    }

    func testPushRegistrationStateTracksMultipleComputersAcrossFailover() {
        var state = PushRegistrationState()
        state.recordSuccess(computerID: "work-id", endpointDisplay: "192.0.2.10:7777")
        state.recordSuccess(computerID: "home-id", endpointDisplay: "198.51.100.20:7777")

        // Both computers remain registered; failing over work-id to a second IP stays registered.
        XCTAssertTrue(state.isRegistered(computerID: "work-id", endpointDisplay: "203.0.113.30:7777"))
        XCTAssertTrue(state.isRegistered(computerID: "home-id", endpointDisplay: "198.51.100.20:7777"))

        // An error on home-id does not overwrite work-id's clean status.
        state.recordNoService(computerID: "home-id", endpointDisplay: "198.51.100.20:7777")
        XCTAssertTrue(state.isRegistered(computerID: "work-id", endpointDisplay: "192.0.2.10:7777"))
        XCTAssertNil(state.error(computerID: "work-id", endpointDisplay: "192.0.2.10:7777"))
        XCTAssertFalse(state.isRegistered(computerID: "home-id", endpointDisplay: "198.51.100.20:7777"))
        XCTAssertEqual(state.error(computerID: "home-id", endpointDisplay: "198.51.100.20:7777"), "This daemon has no notification service.")

        // Unregistering work-id leaves other entries untouched.
        state.recordSuccess(computerID: "home-id", endpointDisplay: "198.51.100.20:7777")
        state.unregister(computerID: "work-id")
        XCTAssertFalse(state.isRegistered(computerID: "work-id", endpointDisplay: "192.0.2.10:7777"))
        XCTAssertTrue(state.isRegistered(computerID: "home-id", endpointDisplay: "198.51.100.20:7777"))
    }

    func testComputerModelFiresOnOnlineWhenLinkComesOnline() async {
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let connection = ComputerConnection(computerID: "home", address: "home", makeRequests: { network.makeRequests($0) })
        let computer = ComputerModel(
            id: "home",
            name: "Home Mac",
            addresses: ["home"],
            certExpires: nil,
            connection: connection,
            scheduler: clock,
            demand: LinkDemand(isCurrent: true)
        )
        var onlineTransitions = 0
        computer.onOnline = { onlineTransitions += 1 }
        computer.start()
        await settle(5)
        computer.stop()

        XCTAssertEqual(onlineTransitions, 1)
    }
}

private final class ActionRaceStub: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        unreachableHosts: Set<String>(),
        answeredHosts: [String]()
    )

    static var unreachableHosts: Set<String> {
        get { lock.withLock { state.unreachableHosts } }
        set { lock.withLock { state.unreachableHosts = newValue } }
    }

    static var answeredHosts: [String] {
        lock.withLock { state.answeredHosts }
    }

    static func reset() {
        lock.withLock { state = (unreachableHosts: [], answeredHosts: []) }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let host = request.url?.host ?? ""
        if Self.unreachableHosts.contains(host) {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let path = request.url?.path ?? ""
        switch path {
        case "/api/hello":
            respond(200, [
                "name": "sheepdog",
                "version": "test",
                "auth": "token",
                "ws": "/ws",
                "audio": ["in_hz": 16000, "out_hz": 24000, "format": "pcm16"],
            ])
        case "/api/pending/answer":
            Self.lock.withLock { Self.state.answeredHosts.append(host) }
            respond(200, ["ok": true, "spoken": "Yes"])
        default:
            respond(404, ["error": "not found"])
        }
    }

    private func respond(_ status: Int, _ object: [String: Any]) {
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: status,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        let data = (try? JSONSerialization.data(withJSONObject: object)) ?? Data()
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
}
#endif
