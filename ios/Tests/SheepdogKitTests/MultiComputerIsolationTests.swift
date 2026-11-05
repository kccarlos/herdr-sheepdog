#if os(iOS)
import Foundation
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Multi-computer failover channel openings, `TermHub` endpoint transitions,
/// cross-computer `AgentsModel` / `SettingsModel` state isolation, and active
/// failover `endpointDisplay` (audit findings 9, 10, 13, and 14).
@MainActor
final class MultiComputerIsolationTests: XCTestCase {
    private let suite = "sheepdog.tests.multi-computer-isolation"
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-iso-\(UUID().uuidString)", isDirectory: true)
        IsolationStub.reset()
    }

    override func tearDown() async throws {
        IsolationStub.reset()
        defaults.removePersistentDomain(forName: suite)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeSessionModel() -> SessionViewModel {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        return SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))
    }

    // MARK: - Finding 9: Reconnecting on a secondary address after outage opens channels once

    func testFailoverFromOfflineToSecondaryAddressOpensChannelsOnce() async {
        let network = FakeComputerNetwork()
        let clock = ManualLinkClock()
        let connection = ComputerConnection(computerID: "mac", address: "lan", makeRequests: { network.makeRequests($0) })
        let computer = ComputerModel(
            id: "mac",
            name: "Mac",
            addresses: ["lan", "tail"],
            certExpires: nil,
            connection: connection,
            scheduler: clock,
            demand: LinkDemand(isCurrent: true, phase: .active, voiceOn: true)
        )
        let wsSpy = ChannelClientSpy()
        let termSpy = ChannelClientSpy()
        computer.attach(.ws, wsSpy)
        computer.attach(.term, termSpy)

        var streamOpenings = 0
        computer.onStreamOpening = { streamOpenings += 1 }

        // 1. Come online initially on "lan".
        computer.start()
        await settle(10)
        XCTAssertEqual(computer.link.onlineAddress, "lan")
        XCTAssertEqual(network.eventsOpened("lan"), 1)
        XCTAssertEqual(streamOpenings, 1)

        // 2. Both addresses fail -> link drops into backoff (.waiting) with channels == [].
        let down = DaemonError.unreachable(endpoint: "down", detail: "down")
        network.setFailure(down, for: "lan")
        network.setFailure(down, for: "tail")
        computer.send(.requestFailed(.transport))
        await settle(5)
        clock.advance(0.5)
        await settle(10)
        XCTAssertTrue(computer.link.channels.isEmpty, "channels must close while waiting in backoff")

        // Reset counters before reconnecting on "tail".
        streamOpenings = 0
        wsSpy.resetCounts()
        termSpy.resetCounts()

        // 3. "tail" recovers while "lan" remains down; retry kicks the race immediately.
        network.setFailure(nil, for: "tail")
        computer.retry()
        await settle(5)
        clock.advance(0.5)
        await settle(15)

        XCTAssertEqual(computer.link.onlineAddress, "tail")
        XCTAssertEqual(network.eventsOpened("tail"), 1, "/events on tail must open once, not be torn down and reopened")
        XCTAssertEqual(streamOpenings, 1, "onStreamOpening (syncHistory + backfillActivity) must fire once")
        XCTAssertEqual(wsSpy.allowTrueCount, 1)
        XCTAssertEqual(wsSpy.reopenCount, 0, "/ws was not open before winning race on tail, so reopen must not fire")
        XCTAssertEqual(termSpy.allowTrueCount, 1)
        XCTAssertEqual(termSpy.reopenCount, 0, "/term was not open before winning race on tail, so reopen must not fire")

        // 4. Now while ALREADY online on "tail" (channels open), prefer "lan" once "lan" recovers:
        //    already-open channels SHOULD reopen once on "lan".
        network.setFailure(nil, for: "lan")
        wsSpy.resetCounts()
        termSpy.resetCounts()
        computer.prefer("lan")
        await settle(15)

        XCTAssertEqual(computer.link.onlineAddress, "lan")
        XCTAssertEqual(wsSpy.reopenCount, 1, "moving addresses while channels stay open should reopen /ws once")
        XCTAssertEqual(termSpy.reopenCount, 1, "moving addresses while channels stay open should reopen /term once")

        computer.stop()
    }

    // MARK: - Finding 10: TermHub.update(endpoint:) notifies handlers on switch and avoids orphan sockets

    func testTermHubUpdateEndpointOnComputerSwitchNotifiesHandlersAndStaysIdle() throws {
        var transports: [SpyTermTransport] = []
        let workCreds = DeviceCredentials(daemonID: "work-id", secrets: InMemorySecretStore())
        workCreds.adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))
        let homeCreds = DeviceCredentials(daemonID: "home-id", secrets: InMemorySecretStore())
        homeCreds.adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))

        let workEndpoint = try XCTUnwrap(DaemonEndpoint(text: "192.0.2.10:7777", secure: true, credentials: workCreds))
        let homeEndpoint = try XCTUnwrap(DaemonEndpoint(text: "198.51.100.20:7777", secure: true, credentials: homeCreds))

        let hub = TermHub(endpoint: workEndpoint, makeTransport: { request in
            let t = SpyTermTransport(request: request)
            transports.append(t)
            return t
        })

        var closedEvents: [(Int, TermCloseReason)] = []
        let ch = try XCTUnwrap(hub.open(paneID: "%1", mode: .control, cols: 80, rows: 24) { event in
            if case let .closed(closedCh, reason, _) = event {
                closedEvents.append((closedCh, reason))
            }
        })
        XCTAssertEqual(transports.count, 1)
        XCTAssertEqual(hub.openChannels, [ch])

        // Switch to a different computer: open handler must be closed cleanly and no idle socket opened.
        hub.update(endpoint: homeEndpoint)

        XCTAssertEqual(closedEvents.map(\.0), [ch], "open window controller must be notified that its channel closed")
        XCTAssertEqual(closedEvents.map(\.1), [.gone])
        XCTAssertEqual(hub.openChannels, [])
        XCTAssertEqual(hub.state, .idle, "TermHub must not open an un-lingered /term socket with zero handlers")
        XCTAssertEqual(transports.count, 1, "no new WebSocket transport should open until a window requests a channel")
    }

    func testTermHubUpdateEndpointForSameComputerPreservesOpenChannels() throws {
        var transports: [SpyTermTransport] = []
        let workCreds = DeviceCredentials(daemonID: "work-id", secrets: InMemorySecretStore())
        workCreds.adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))

        let workLAN = try XCTUnwrap(DaemonEndpoint(text: "192.0.2.10:7777", secure: true, credentials: workCreds))
        let workTail = try XCTUnwrap(DaemonEndpoint(text: "198.51.100.20:7777", secure: true, credentials: workCreds))

        let hub = TermHub(endpoint: workLAN, makeTransport: { request in
            let t = SpyTermTransport(request: request)
            transports.append(t)
            return t
        })

        var states: [TermSocket.State] = []
        var closedCount = 0
        let ch = try XCTUnwrap(hub.open(paneID: "%1", mode: .control, cols: 80, rows: 24) { event in
            switch event {
            case let .state(s): states.append(s)
            case .closed: closedCount += 1
            default: break
            }
        })
        transports.last?.sendReady()
        XCTAssertEqual(hub.openChannels, [ch])

        // Updating to another address of the SAME paired computer keeps the channel alive and reconnects there.
        hub.update(endpoint: workTail)

        XCTAssertEqual(closedCount, 0, "same-computer endpoint update should keep open terminal channels")
        XCTAssertEqual(hub.openChannels, [ch])
        XCTAssertTrue(states.contains(.reconnecting), "open window should learn the socket is reconnecting")
        XCTAssertEqual(transports.last?.request.url?.host, "198.51.100.20")
    }

    // MARK: - Finding 13: AgentsModel & SettingsModel isolation across computer switches

    func testSwitchingComputersResetsSeenTrackerOverlayAndExpandedAgents() {
        let model = makeSessionModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pin_w", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "pin_h", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()

        // On Work Mac: view %1 at turn 5, pin %1 optimistically, and expand %1's card.
        let workAgent = Agent(
            paneID: "%1",
            alias: "auth",
            state: .done,
            attention: AgentAttention.classify(state: .done, turn: 5, seen: 4, word: "done", error: false, pinned: false)
        )
        let firstWrites = model.agentsModel.takeSeenWrites(visible: [workAgent], appActive: true)
        XCTAssertEqual(firstWrites, [SeenTracker.Write(paneID: "%1", turn: 5)])
        model.agentsModel.change { $0.setPinned("%1", true, at: Date()) }
        model.agentsModel.expandedAgents.insert("%1")
        XCTAssertFalse(model.agentsModel.overlay.isEmpty)

        // Switch to Home Mac.
        model.talk(to: "home-id")

        XCTAssertTrue(model.agentsModel.overlay.isEmpty, "optimistic attention overlay from previous computer must clear")
        XCTAssertTrue(model.agentsModel.expandedAgents.isEmpty, "expanded card keys from previous computer must clear")

        // Home Mac also has a pane "%1", currently unread at turn 2 (< turn 5 on Work Mac):
        // SeenTracker must still emit a markSeen write for Home Mac's %1!
        let homeAgent = Agent(
            paneID: "%1",
            alias: "billing",
            state: .done,
            attention: AgentAttention.classify(state: .done, turn: 2, seen: 1, word: "done", error: false, pinned: false)
        )
        let homeWrites = model.agentsModel.takeSeenWrites(visible: [homeAgent], appActive: true)
        XCTAssertEqual(homeWrites, [SeenTracker.Write(paneID: "%1", turn: 2)], "SeenTracker from Work Mac must not suppress markSeen on Home Mac")
    }

    func testSettingsModelClearResetsVoiceProvidersAndErrorsAndIgnoresMoved() async throws {
        let model = makeSessionModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pin_w", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "pin_h", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()

        let workCreds = model.settings.computers.credentials(for: "work-id")
        workCreds.adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))
        let workEndpoint = try XCTUnwrap(model.settings.computers.endpoint(for: "work-id"))
        let homeCreds = model.settings.computers.credentials(for: "home-id")
        homeCreds.adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))
        let homeEndpoint = try XCTUnwrap(model.settings.computers.endpoint(for: "home-id"))

        let workModel = makeStubbedComputerModel(id: "work-id", name: "Work Mac", endpoint: workEndpoint)
        let homeModel = makeStubbedComputerModel(id: "home-id", name: "Home Mac", endpoint: homeEndpoint)
        model.fleet.set([workModel, homeModel])
        model.fleet.makeCurrent("work-id")

        // 1. Load voiceProviders on Work Mac and populate error strings, then clear().
        IsolationStub.mode = .immediate
        await model.settingsModel.loadVoiceProviders()
        XCTAssertEqual(model.settingsModel.voiceProviders?.provider, "openai")
        model.settingsModel.brainError = "stale brain error"
        model.settingsModel.settingsError = "stale settings error"
        model.settingsModel.providerError = "stale provider error"

        model.settingsModel.clear()
        XCTAssertNil(model.settingsModel.voiceProviders, "clear() must drop previous computer's voiceProviders")
        XCTAssertNil(model.settingsModel.brainError, "clear() must drop previous computer's brainError")
        XCTAssertNil(model.settingsModel.settingsError, "clear() must drop previous computer's settingsError")
        XCTAssertNil(model.settingsModel.providerError, "clear() must drop previous computer's providerError")

        // 2. Start loadBrainModels() and loadDaemonSettings() while on Work Mac, then switch to Home Mac
        //    before the HTTP replies land -> ComputerFleet.Moved must NOT surface as an error banner!
        IsolationStub.mode = .hang
        let brainTask = Task { await model.settingsModel.loadBrainModels() }
        let settingsTask = Task { await model.settingsModel.loadDaemonSettings() }
        try await waitForRequests(count: 2)

        model.fleet.makeCurrent("home-id")
        IsolationStub.releaseAll()
        await brainTask.value
        await settingsTask.value

        XCTAssertNil(model.settingsModel.brainError, "ComputerFleet.Moved on switch must not set brainError")
        XCTAssertNil(model.settingsModel.settingsError, "ComputerFleet.Moved on switch must not set settingsError")

        workModel.stop()
        homeModel.stop()
    }

    // MARK: - Finding 14: endpointDisplay reflects active failover address

    func testEndpointDisplayReflectsActiveFailoverAddress() async {
        let model = makeSessionModel()
        let work = PairedComputer(
            daemonID: "work-id",
            name: "Work Mac",
            addresses: ["192.0.2.10:7777", "198.51.100.20:7777"],
            pin: "pin_w",
            deviceID: "dw",
            pairedAt: Date()
        )
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()

        let network = FakeComputerNetwork()
        network.setFailure(DaemonError.unreachable(endpoint: "192.0.2.10:7777", detail: "down"), for: "192.0.2.10:7777")
        let clock = ManualLinkClock()
        let connection = ComputerConnection(
            computerID: "work-id",
            address: "192.0.2.10:7777",
            makeRequests: { network.makeRequests($0) }
        )
        let workRuntime = ComputerModel(
            id: "work-id",
            name: "Work Mac",
            addresses: ["192.0.2.10:7777", "198.51.100.20:7777"],
            certExpires: nil,
            connection: connection,
            scheduler: clock,
            demand: LinkDemand(isCurrent: true, phase: .active, voiceOn: true)
        )
        model.fleet.set([workRuntime])
        model.fleet.makeCurrent("work-id")
        workRuntime.start()
        await settle(5)
        clock.advance(0.5)
        await settle(10)

        XCTAssertEqual(workRuntime.link.addressInUse, "198.51.100.20:7777")
        XCTAssertEqual(model.settings.endpoint?.display, "192.0.2.10:7777", "preferred address remains LAN")
        XCTAssertEqual(model.endpointDisplay, "198.51.100.20:7777", "endpointDisplay should report the active failover address")

        workRuntime.stop()
    }

    // MARK: - Helpers

    private func makeStubbedComputerModel(id: String, name: String, endpoint: DaemonEndpoint) -> ComputerModel {
        let connection = ComputerConnection(computerID: id, address: endpoint.display) { _ in
            let config = URLSessionConfiguration.ephemeral
            config.protocolClasses = [IsolationStub.self]
            let session = URLSession(configuration: config)
            return DaemonClient(endpoint: endpoint, session: session)
        }
        return ComputerModel(
            id: id,
            name: name,
            addresses: [endpoint.display],
            certExpires: nil,
            connection: connection,
            scheduler: ManualLinkClock(),
            demand: LinkDemand(isCurrent: false)
        )
    }

    private func waitForRequests(count: Int) async throws {
        let deadline = Date().addingTimeInterval(2.0)
        while IsolationStub.waitingCount < count {
            if Date() > deadline {
                XCTFail("timed out waiting for \(count) stubbed requests (got \(IsolationStub.waitingCount))")
                return
            }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }
}

@MainActor
private final class ChannelClientSpy: LinkChannelClient {
    var allowTrueCount = 0
    var allowFalseCount = 0
    var reopenCount = 0
    var usedAddresses: [String] = []
    var lastReporter: LinkChannelReporter?

    func resetCounts() {
        allowTrueCount = 0
        allowFalseCount = 0
        reopenCount = 0
        usedAddresses = []
    }

    func linkAllows(_ allowed: Bool, reporter: LinkChannelReporter) {
        lastReporter = reporter
        if allowed { allowTrueCount += 1 } else { allowFalseCount += 1 }
    }

    func linkReopens(reporter: LinkChannelReporter) {
        lastReporter = reporter
        reopenCount += 1
    }

    func linkUses(address: String) {
        usedAddresses.append(address)
    }
}

@MainActor
private final class SpyTermTransport: TermTransport {
    var onEvent: ((TermTransportEvent) -> Void)?
    let request: URLRequest
    var isClosed = false

    init(request: URLRequest) {
        self.request = request
    }

    func start() {}
    func send(text _: String) {}
    func send(data _: Data) {}
    func ping(_ completion: @escaping (Bool) -> Void) { completion(true) }
    func close() { isClosed = true }

    func sendReady() {
        onEvent?(.text(#"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":false}"#))
    }
}

private final class IsolationStub: URLProtocol {
    enum Mode { case immediate, hang }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        mode: Mode.immediate,
        waiting: [() -> Void]()
    )

    static var mode: Mode {
        get { lock.withLock { state.mode } }
        set { lock.withLock { state.mode = newValue } }
    }

    static var waitingCount: Int {
        lock.withLock { state.waiting.count }
    }

    static func releaseAll() {
        let callbacks = lock.withLock { () -> [() -> Void] in
            let current = state.waiting
            state.waiting.removeAll()
            return current
        }
        for cb in callbacks { cb() }
    }

    static func reset() {
        releaseAll()
        lock.withLock { state.mode = .immediate }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let shouldHang = Self.lock.withLock { Self.state.mode == .hang }
        if shouldHang {
            Self.lock.withLock {
                Self.state.waiting.append { [weak self] in self?.finish() }
            }
            return
        }
        finish()
    }

    private func finish() {
        guard let url = request.url else { return }
        let body: [String: Any]
        switch url.path {
        case "/api/voice/providers":
            body = [
                "provider": "openai",
                "model": "gpt-live-1",
                "providers": [
                    ["id": "openai", "label": "GPT-Live", "model": "gpt-live-1", "available": true],
                ],
            ]
        case "/api/brain/models":
            body = ["provider": "anthropic", "providers": [], "model": "claude-sonnet-4-6", "models": []]
        case "/api/settings":
            body = ["chattiness": "normal", "levels": ["quiet", "normal", "chatty"], "languages": ["en"]]
        default:
            body = ["ok": true]
        }
        let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        let data = (try? JSONSerialization.data(withJSONObject: body)) ?? Data()
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
}
#endif
