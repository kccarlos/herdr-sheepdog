import Foundation
import Security
import XCTest
import SheepdogKit
@testable import SheepdogMac

/// In-process HTTP stub for local daemon pairing and auth endpoints — no socket is opened.
private final class StubBrowserPairDaemon: URLProtocol {
    enum Reply: Sendable {
        case json(status: Int, body: String)
        case failure(URLError.Code)
    }

    struct RecordedRequest: @unchecked Sendable {
        let request: URLRequest
        let body: Data
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var _recorded: [RecordedRequest] = []
    nonisolated(unsafe) private static var _replies: [Reply] = []
    nonisolated(unsafe) private static var _handler: (@Sendable (URLRequest, Data) -> Reply)?

    static func reset(replies: [Reply] = [], handler: (@Sendable (URLRequest, Data) -> Reply)? = nil) {
        lock.withLock {
            _recorded = []
            _replies = replies
            _handler = handler
        }
    }

    static var recorded: [RecordedRequest] {
        lock.withLock { _recorded }
    }

    static var requests: [URLRequest] {
        lock.withLock { _recorded.map(\.request) }
    }

    private static func readBody(from request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: 4096)
        defer { buf.deallocate() }
        while stream.hasBytesAvailable {
            let n = stream.read(buf, maxLength: 4096)
            if n <= 0 { break }
            data.append(buf, count: n)
        }
        return data
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Self.readBody(from: request)
        let (reply, handler): (Reply?, (@Sendable (URLRequest, Data) -> Reply)?) = Self.lock.withLock {
            Self._recorded.append(RecordedRequest(request: request, body: body))
            if let h = Self._handler { return (nil, h) }
            return (Self._replies.isEmpty ? nil : Self._replies.removeFirst(), nil)
        }
        let chosen = handler?(request, body) ?? reply
        switch chosen {
        case let .json(status, responseBody):
            let response = HTTPURLResponse(
                url: request.url!,
                statusCode: status,
                httpVersion: nil,
                headerFields: ["Content-Type": "application/json", "Cache-Control": "no-store"]
            )!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(responseBody.utf8))
            client?.urlProtocolDidFinishLoading(self)
        case let .failure(code):
            client?.urlProtocol(self, didFailWithError: URLError(code))
        case nil:
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
        }
    }

    override func stopLoading() {}
}

/// Ephemeral in-memory P-256 key provider — never touches the macOS Login Keychain.
private final class FakeDeviceKeyProvider: DeviceKeyProviding, @unchecked Sendable {
    private let lock = NSLock()
    private var stored: [String: SecKey] = [:]
    var createError: Error?
    private(set) var createdDaemonIDs: [String] = []
    private(set) var deletedDaemonIDs: [String] = []

    var isHardware: Bool { false }

    func load(daemonID: String) -> SecKey? {
        lock.withLock { stored[daemonID] }
    }

    func loadOrCreate(daemonID: String) throws -> SecKey {
        if let existing = load(daemonID: daemonID) { return existing }
        if let err = lock.withLock({ createError }) { throw err }
        var error: Unmanaged<CFError>?
        let attrs: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits as String: 256,
            kSecPrivateKeyAttrs as String: [kSecAttrIsPermanent as String: false],
        ]
        guard let key = SecKeyCreateRandomKey(attrs as CFDictionary, &error) else {
            throw error!.takeRetainedValue() as Error
        }
        lock.withLock {
            stored[daemonID] = key
            createdDaemonIDs.append(daemonID)
        }
        return key
    }

    func publicKey(_ key: SecKey) -> String? {
        DeviceKey.publicKey(key)
    }

    func delete(daemonID: String) {
        lock.withLock {
            stored.removeValue(forKey: daemonID)
            deletedDaemonIDs.append(daemonID)
        }
    }
}

private final class FakeBrowserPairingSessionProvider: BrowserPairingSessionProviding {
    var results: [Result<SessionTicket, Error>]
    private(set) var calls = 0
    private(set) var invalidateCalls = 0

    init(ticket: String = "mac-session-ticket") {
        results = [.success(SessionTicket(ticket: ticket, expires: Date().addingTimeInterval(3600)))]
    }

    init(tickets: [String]) {
        results = tickets.map { .success(SessionTicket(ticket: $0, expires: Date().addingTimeInterval(3600))) }
    }

    init(error: Error) {
        results = [.failure(error)]
    }

    func sessionTicket(base: URL) async throws -> SessionTicket {
        calls += 1
        let item = results.count > 1 ? results.removeFirst() : results[0]
        return try item.get()
    }

    func invalidateTicket() {
        invalidateCalls += 1
    }
}

/// Suspends the first `sessionTicket` call until `release` is invoked, for deterministic double-click tests.
@MainActor
private final class GatedSessionProvider: BrowserPairingSessionProviding {
    private(set) var calls = 0
    private var continuations: [CheckedContinuation<SessionTicket, Error>] = []

    func sessionTicket(base: URL) async throws -> SessionTicket {
        calls += 1
        return try await withCheckedThrowingContinuation { cont in
            continuations.append(cont)
        }
    }

    func invalidateTicket() {}

    func releaseAll(ticket: String = "mac-session-ticket") {
        let pending = continuations
        continuations.removeAll()
        let value = SessionTicket(ticket: ticket, expires: Date().addingTimeInterval(3600))
        pending.forEach { $0.resume(returning: value) }
    }
}

@MainActor
private final class FakeBrowserPairingPresenter: BrowserPairingPresenting {
    private(set) var shownCodes: [BrowserPairingPresentation] = []
    private(set) var shownErrors: [BrowserPairingError] = []

    func showCode(_ presentation: BrowserPairingPresentation) {
        shownCodes.append(presentation)
    }

    func showError(_ error: BrowserPairingError) {
        shownErrors.append(error)
    }
}

@MainActor
final class BrowserPairingTests: XCTestCase {
    private let listening = DaemonState(
        agents: [],
        voice: DaemonState.VoiceInfo(mode: "gemini", status: "connected", clientConnected: true)
    )

    override func tearDown() {
        StubBrowserPairDaemon.reset()
        super.tearDown()
    }

    private func stubSession() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubBrowserPairDaemon.self]
        return URLSession(configuration: config)
    }

    private func makeController(
        sessionProvider: BrowserPairingSessionProviding? = nil,
        presenter: FakeBrowserPairingPresenter? = nil
    ) -> (StatusItemController, BrowserPairingClient, FakeBrowserPairingPresenter) {
        let sessionProvider = sessionProvider ?? FakeBrowserPairingSessionProvider()
        let presenter = presenter ?? FakeBrowserPairingPresenter()
        let client = BrowserPairingClient(
            locator: FakeLocator(host: "127.0.0.1", port: 7777),
            sessionProvider: sessionProvider,
            urlSession: stubSession()
        )
        let controller = StatusItemController(
            source: FakeStatusSource(),
            voice: FakeVoice(),
            panel: FakePanel(),
            hotkey: FakeHotkey(),
            loginItem: FakeLoginItem(),
            log: FakeLog(),
            browserPairing: client,
            browserPairingPresenter: presenter
        )
        return (controller, client, presenter)
    }

    private func waitUntil(_ condition: @escaping @MainActor () -> Bool) async {
        for _ in 0 ..< 25 {
            if condition() { return }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    // MARK: - Menu and VoiceOver

    func testMenuHasPairBrowserItemAndDisablesItWhenDaemonIsDown() {
        let upRows = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false)
        XCTAssertTrue(
            upRows.contains(.item(title: "Pair browser…", action: .pairBrowser, enabled: true, checked: false)),
            "Expected enabled 'Pair browser…' row when daemon is up"
        )

        let downRows = StatusItemController.rows(for: MenuBarState(state: nil), loginItemEnabled: false)
        XCTAssertTrue(
            downRows.contains(.item(title: "Pair browser…", action: .pairBrowser, enabled: false, checked: false)),
            "Expected disabled 'Pair browser…' row when daemon is down"
        )
    }

    func testPairBrowserMenuItemHasVoiceOverLabel() throws {
        let (controller, _, _) = makeController()
        controller.update(listening)
        let item = try XCTUnwrap(controller.menu().items.first { $0.title == "Pair browser…" })
        let label = try XCTUnwrap(item.accessibilityLabel())
        XCTAssertFalse(label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertTrue(label.localizedCaseInsensitiveContains("pair"))
        XCTAssertTrue(label.localizedCaseInsensitiveContains("browser"))
    }

    // MARK: - Request contract (method, path, device session auth)

    func testRequestsBrowserCodeWithDeviceSessionAuth() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 200, body: #"{"code":"ABCD-2345","expires":"2026-11-22T12:05:00Z","expires_in":300}"#),
        ])
        let sessions = FakeBrowserPairingSessionProvider(ticket: "mac-ticket-42")
        let client = BrowserPairingClient(
            locator: FakeLocator(host: "127.0.0.1", port: 7777),
            sessionProvider: sessions,
            urlSession: stubSession()
        )

        let issued = try await client.requestCode()

        XCTAssertEqual(sessions.calls, 1)
        let req = try XCTUnwrap(StubBrowserPairDaemon.requests.first)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/api/pair/browser-code")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer sds_mac-ticket-42")
        XCTAssertEqual(issued.code, "ABCD-2345")
        XCTAssertEqual(issued.expiresIn, 300)
        XCTAssertEqual(issued.expires, ISO8601DateFormatter().date(from: "2026-11-22T12:05:00Z"))
    }

    // MARK: - Presentation (code, expiry, VoiceOver label)

    func testPairBrowserDisplaysCodeExpiryAndVoiceOverLabel() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 200, body: #"{"code":"ABCD-2345","expires":"2026-11-22T12:05:00Z","expires_in":300}"#),
        ])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownCodes.isEmpty || !presenter.shownErrors.isEmpty }

        XCTAssertTrue(presenter.shownErrors.isEmpty, "Unexpected errors: \(presenter.shownErrors)")
        let shown = try XCTUnwrap(presenter.shownCodes.first)
        XCTAssertEqual(shown.code, "ABCD-2345")
        XCTAssertFalse(shown.expiryText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(shown.codeAccessibilityLabel.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        // VoiceOver should announce the characters individually rather than as a word.
        for ch in ["A", "B", "C", "D", "2", "3", "4", "5"] {
            XCTAssertTrue(
                shown.codeAccessibilityLabel.contains(ch),
                "Expected code VoiceOver label '\(shown.codeAccessibilityLabel)' to contain '\(ch)'"
            )
        }
        XCTAssertFalse(
            shown.codeAccessibilityLabel.contains("ABCD"),
            "Expected code VoiceOver label to separate characters so VoiceOver spells them"
        )
    }

    // MARK: - MacDeviceSessionProvider (direct loopback macos enrolment, caching, revocation, cleanup)

    func testMacSessionProviderEnrolsAsMacOSOnLoopbackWithoutCallingPair() async throws {
        StubBrowserPairDaemon.reset { req, _ in
            switch req.url?.path {
            case "/api/hello":
                return .json(status: 200, body: #"{"computer":{"id":"daemon-1"},"security":{"pin":"pin-1"}}"#)
            case "/api/pair":
                return .json(status: 200, body: #"{"addresses":[],"error":"the daemon listens on 127.0.0.1 only"}"#)
            case "/api/devices/enroll":
                return .json(status: 200, body: #"{"device_id":"mac-dev-1","daemon_id":"daemon-1","name":"Studio Mac"}"#)
            case "/api/auth/challenge":
                return .json(status: 200, body: #"{"nonce":"nonce-1","daemon_id":"daemon-1","expires_in":60}"#)
            case "/api/auth/session":
                return .json(status: 200, body: #"{"ticket":"mac-ticket-1","expires_in":43200,"device":{"id":"mac-dev-1","name":"Studio Mac"}}"#)
            case "/api/pair/browser-code":
                return .json(status: 200, body: #"{"code":"ABCD-2345","expires":"2026-11-22T12:05:00Z","expires_in":300}"#)
            default:
                return .json(status: 404, body: #"{"error":"not found"}"#)
            }
        }
        let secrets = MemorySecretStore()
        let keys = FakeDeviceKeyProvider()
        let session = stubSession()
        let provider = MacDeviceSessionProvider(secrets: secrets, keys: keys, urlSession: session, deviceName: { "Studio Mac" })
        let client = BrowserPairingClient(locator: FakeLocator(host: "127.0.0.1", port: 7777), sessionProvider: provider, urlSession: session)

        let issued = try await client.requestCode()

        XCTAssertEqual(issued.code, "ABCD-2345")
        XCTAssertFalse(StubBrowserPairDaemon.requests.contains { $0.url?.path == "/api/pair" }, "Mac enrolment must never call /api/pair")
        let enrollReq = try XCTUnwrap(StubBrowserPairDaemon.recorded.first { $0.request.url?.path == "/api/devices/enroll" })
        let enrollObj = try XCTUnwrap(try JSONSerialization.jsonObject(with: enrollReq.body) as? [String: Any])
        XCTAssertEqual(enrollObj["kind"] as? String, "macos")
        XCTAssertEqual(enrollObj["code"] as? String, "")
        XCTAssertEqual(enrollObj["name"] as? String, "Studio Mac")
        XCTAssertEqual(enrollObj["hardware"] as? Bool, false)
        XCTAssertFalse((enrollObj["public_key"] as? String ?? "").isEmpty)
        XCTAssertEqual(secrets.secret(for: "mac.device.daemon-1"), "mac-dev-1")
        XCTAssertEqual(keys.createdDaemonIDs, ["daemon-1"])
        XCTAssertTrue(keys.deletedDaemonIDs.isEmpty)
    }

    func testMacSessionProviderReusesCachedTicketOnSecondClick() async throws {
        StubBrowserPairDaemon.reset { req, _ in
            switch req.url?.path {
            case "/api/hello":
                return .json(status: 200, body: #"{"computer":{"id":"daemon-1"},"security":{"pin":"pin-1"}}"#)
            case "/api/devices/enroll":
                return .json(status: 200, body: #"{"device_id":"mac-dev-1","daemon_id":"daemon-1","name":"Studio Mac"}"#)
            case "/api/auth/challenge":
                return .json(status: 200, body: #"{"nonce":"nonce-1","daemon_id":"daemon-1","expires_in":60}"#)
            case "/api/auth/session":
                return .json(status: 200, body: #"{"ticket":"mac-ticket-1","expires_in":43200,"device":{"id":"mac-dev-1","name":"Studio Mac"}}"#)
            case "/api/pair/browser-code":
                return .json(status: 200, body: #"{"code":"ABCD-2345","expires":"2026-11-22T12:05:00Z","expires_in":300}"#)
            default:
                return .json(status: 404, body: #"{"error":"not found"}"#)
            }
        }
        let session = stubSession()
        let provider = MacDeviceSessionProvider(secrets: MemorySecretStore(), keys: FakeDeviceKeyProvider(), urlSession: session, deviceName: { "Studio Mac" })
        let client = BrowserPairingClient(locator: FakeLocator(host: "127.0.0.1", port: 7777), sessionProvider: provider, urlSession: session)

        _ = try await client.requestCode()
        let firstCount = StubBrowserPairDaemon.requests.count
        _ = try await client.requestCode()

        let secondPaths = StubBrowserPairDaemon.requests.dropFirst(firstCount).compactMap { $0.url?.path }
        XCTAssertEqual(secondPaths, ["/api/pair/browser-code"], "Second click should reuse cached ticket with 1 HTTP call")
    }

    func testBrowserPairingClientRetriesOnceOn401SessionExpired() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 401, body: #"{"error":"unauthorized","reason":"session_expired"}"#),
            .json(status: 200, body: #"{"code":"WXYZ-6789","expires":"2026-11-22T12:05:00Z","expires_in":300}"#),
        ])
        let sessions = FakeBrowserPairingSessionProvider(tickets: ["stale-ticket", "fresh-ticket"])
        let client = BrowserPairingClient(locator: FakeLocator(host: "127.0.0.1", port: 7777), sessionProvider: sessions, urlSession: stubSession())

        let issued = try await client.requestCode()

        XCTAssertEqual(issued.code, "WXYZ-6789")
        XCTAssertEqual(sessions.invalidateCalls, 1)
        XCTAssertEqual(sessions.calls, 2)
        XCTAssertEqual(StubBrowserPairDaemon.requests.map { $0.value(forHTTPHeaderField: "Authorization") }, [
            "Bearer sds_stale-ticket",
            "Bearer sds_fresh-ticket",
        ])

        // If 401 persists after the single retry, fail with .notEnrolled rather than looping.
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 401, body: #"{"error":"unauthorized","reason":"session_expired"}"#),
            .json(status: 401, body: #"{"error":"unauthorized","reason":"session_expired"}"#),
        ])
        let failingSessions = FakeBrowserPairingSessionProvider(tickets: ["t1", "t2"])
        let failingClient = BrowserPairingClient(locator: FakeLocator(host: "127.0.0.1", port: 7777), sessionProvider: failingSessions, urlSession: stubSession())
        do {
            _ = try await failingClient.requestCode()
            XCTFail("Expected .notEnrolled when 401 persists after retry")
        } catch let err as BrowserPairingError {
            XCTAssertEqual(err, .notEnrolled)
            XCTAssertEqual(failingSessions.invalidateCalls, 1)
            XCTAssertEqual(failingSessions.calls, 2)
        }
    }

    func testMacSessionProviderReenrolsAsMacOSWhenDeviceRevoked() async throws {
        let lock = NSLock()
        nonisolated(unsafe) var sessionAttempts = 0
        StubBrowserPairDaemon.reset { req, _ in
            switch req.url?.path {
            case "/api/hello":
                return .json(status: 200, body: #"{"computer":{"id":"daemon-1"},"security":{"pin":"pin-1"}}"#)
            case "/api/auth/challenge":
                return .json(status: 200, body: #"{"nonce":"nonce-1","daemon_id":"daemon-1","expires_in":60}"#)
            case "/api/auth/session":
                let attempt = lock.withLock { sessionAttempts += 1; return sessionAttempts }
                if attempt == 1 {
                    return .json(status: 401, body: #"{"error":"unauthorized","reason":"device_revoked"}"#)
                }
                return .json(status: 200, body: #"{"ticket":"fresh-ticket","expires_in":43200,"device":{"id":"mac-dev-2","name":"Studio Mac"}}"#)
            case "/api/devices/enroll":
                return .json(status: 200, body: #"{"device_id":"mac-dev-2","daemon_id":"daemon-1","name":"Studio Mac"}"#)
            default:
                return .json(status: 404, body: #"{"error":"not found"}"#)
            }
        }
        let secrets = MemorySecretStore()
        secrets.setSecret("mac-dev-1", for: "mac.device.daemon-1")
        let keys = FakeDeviceKeyProvider()
        _ = try keys.loadOrCreate(daemonID: "daemon-1")

        let provider = MacDeviceSessionProvider(secrets: secrets, keys: keys, urlSession: stubSession(), deviceName: { "Studio Mac" })
        let ticket = try await provider.sessionTicket(base: URL(string: "http://127.0.0.1:7777")!)

        XCTAssertEqual(ticket.ticket, "fresh-ticket")
        XCTAssertEqual(secrets.secret(for: "mac.device.daemon-1"), "mac-dev-2")
        XCTAssertFalse(StubBrowserPairDaemon.requests.contains { $0.url?.path == "/api/pair" })
        let enrollReq = try XCTUnwrap(StubBrowserPairDaemon.recorded.first { $0.request.url?.path == "/api/devices/enroll" })
        let enrollObj = try XCTUnwrap(try JSONSerialization.jsonObject(with: enrollReq.body) as? [String: Any])
        XCTAssertEqual(enrollObj["kind"] as? String, "macos")
        XCTAssertEqual(enrollObj["code"] as? String, "")
    }

    func testMacSessionProviderDeletesNewKeyAndSurfacesRateLimitedOn429() async throws {
        StubBrowserPairDaemon.reset { req, _ in
            switch req.url?.path {
            case "/api/hello":
                return .json(status: 200, body: #"{"computer":{"id":"daemon-1"},"security":{"pin":"pin-1"}}"#)
            case "/api/devices/enroll":
                return .json(status: 429, body: #"{"error":"too many attempts","reason":"rate_limited"}"#)
            default:
                return .json(status: 404, body: #"{"error":"not found"}"#)
            }
        }
        let keys = FakeDeviceKeyProvider()
        let provider = MacDeviceSessionProvider(secrets: MemorySecretStore(), keys: keys, urlSession: stubSession(), deviceName: { "Studio Mac" })

        do {
            _ = try await provider.sessionTicket(base: URL(string: "http://127.0.0.1:7777")!)
            XCTFail("Expected .rateLimited on 429 during enroll")
        } catch let err as BrowserPairingError {
            XCTAssertEqual(err, .rateLimited)
            XCTAssertEqual(keys.deletedDaemonIDs, ["daemon-1"], "Newly created key must be deleted on definite non-enrollment")
            XCTAssertFalse(err.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            XCTAssertTrue(err.message.localizedCaseInsensitiveContains("5 minutes"), "Expected 5 minutes plain fix, got: \(err.message)")
        }
    }

    // MARK: - Double-click serialization

    func testDoubleClickSerializesPairBrowserToOneRequest() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 200, body: #"{"code":"AAAA-2222","expires":"2026-11-22T12:05:00Z","expires_in":300}"#),
            .json(status: 200, body: #"{"code":"BBBB-3333","expires":"2026-11-22T12:06:00Z","expires_in":300}"#),
        ])
        let gated = GatedSessionProvider()
        let (controller, _, presenter) = makeController(sessionProvider: gated)
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { gated.calls >= 1 }
        controller.perform(.pairBrowser)
        try? await Task.sleep(nanoseconds: 20_000_000)

        gated.releaseAll()
        await waitUntil { !presenter.shownCodes.isEmpty || !presenter.shownErrors.isEmpty }
        try? await Task.sleep(nanoseconds: 20_000_000)

        XCTAssertEqual(gated.calls, 1, "Double-click while in flight must not start a second pairing request")
        XCTAssertEqual(StubBrowserPairDaemon.requests.count, 1)
        XCTAssertEqual(presenter.shownCodes.map(\.code), ["AAAA-2222"])
        XCTAssertTrue(presenter.shownErrors.isEmpty)
    }

    // MARK: - Error cases (distinct errors with plain fix messages)

    func testDaemonDownShowsPlainFix() async throws {
        StubBrowserPairDaemon.reset(replies: [.failure(.cannotConnectToHost)])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        XCTAssertEqual(error, .daemonDown)
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertTrue(
            error.message.localizedCaseInsensitiveContains("sheepdog start")
                || error.message.localizedCaseInsensitiveContains("start"),
            "Expected plain fix for daemon down, got: \(error.message)"
        )
    }

    func testNotEnrolledShowsPlainFixWithRealCommands() async throws {
        let (controller, _, presenter) = makeController(
            sessionProvider: FakeBrowserPairingSessionProvider(error: BrowserPairingError.notEnrolled)
        )
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        XCTAssertEqual(error, .notEnrolled)
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.localizedCaseInsensitiveContains("Enable pairing"), "Must not name non-existent 'Enable pairing' control: \(error.message)")
        XCTAssertTrue(error.message.contains("sheepdog restart"), "Expected real command 'sheepdog restart', got: \(error.message)")
        XCTAssertTrue(error.message.contains("sheepdog logs"), "Expected real command 'sheepdog logs', got: \(error.message)")
        XCTAssertTrue(StubBrowserPairDaemon.requests.isEmpty, "Should not call daemon when Mac app is not enrolled")
    }

    func testRefusedByDaemon403MapsToNotEnrolled() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 403, body: #"{"error":"device_session_required","reason":"device_session_required"}"#),
        ])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        XCTAssertEqual(error, .notEnrolled)
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    func testDistinctErrorMappingForRateLimitedServerErrorAndKeychainFailure() async throws {
        // 429 rate_limited -> .rateLimited
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 429, body: #"{"error":"too many attempts","reason":"rate_limited"}"#),
        ])
        let (controller429, _, presenter429) = makeController()
        controller429.update(listening)
        controller429.perform(.pairBrowser)
        await waitUntil { !presenter429.shownErrors.isEmpty }
        let err429 = try XCTUnwrap(presenter429.shownErrors.first)
        XCTAssertEqual(err429, .rateLimited)
        XCTAssertFalse(err429.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertTrue(err429.message.localizedCaseInsensitiveContains("5 minutes"), "Got: \(err429.message)")

        // 500 server error -> .serverError(status: 500, ...) mentioning sheepdog logs
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 500, body: #"{"error":"browser pairing temporarily unavailable"}"#),
        ])
        let (controller500, _, presenter500) = makeController()
        controller500.update(listening)
        controller500.perform(.pairBrowser)
        await waitUntil { !presenter500.shownErrors.isEmpty }
        let err500 = try XCTUnwrap(presenter500.shownErrors.first)
        XCTAssertEqual(err500, .serverError(status: 500, message: "browser pairing temporarily unavailable"))
        XCTAssertFalse(err500.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertTrue(err500.message.contains("500"), "Got: \(err500.message)")
        XCTAssertTrue(err500.message.contains("sheepdog logs"), "Got: \(err500.message)")

        // 404 old daemon -> .serverError(status: 404, ...)
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 404, body: #"{"error":"not found"}"#),
        ])
        let (controller404, _, presenter404) = makeController()
        controller404.update(listening)
        controller404.perform(.pairBrowser)
        await waitUntil { !presenter404.shownErrors.isEmpty }
        let err404 = try XCTUnwrap(presenter404.shownErrors.first)
        XCTAssertEqual(err404, .serverError(status: 404, message: "not found"))
        XCTAssertTrue(err404.message.contains("404"), "Got: \(err404.message)")

        // Keychain failure (status == 0) -> .serverError(status: 0, ...) without blaming daemon logs
        StubBrowserPairDaemon.reset { req, _ in
            req.url?.path == "/api/hello"
                ? .json(status: 200, body: #"{"computer":{"id":"daemon-1"},"security":{"pin":"pin-1"}}"#)
                : .json(status: 404, body: #"{"error":"not found"}"#)
        }
        let failingKeys = FakeDeviceKeyProvider()
        failingKeys.createError = NSError(domain: NSOSStatusErrorDomain, code: Int(errSecAuthFailed), userInfo: [NSLocalizedDescriptionKey: "Keychain access denied"])
        let provider = MacDeviceSessionProvider(secrets: MemorySecretStore(), keys: failingKeys, urlSession: stubSession(), deviceName: { "Studio Mac" })
        do {
            _ = try await provider.sessionTicket(base: URL(string: "http://127.0.0.1:7777")!)
            XCTFail("Expected .serverError(status: 0, ...) on Keychain failure")
        } catch let err as BrowserPairingError {
            guard case let .serverError(status, detail) = err else {
                return XCTFail("Expected .serverError for Keychain failure, got \(err)")
            }
            XCTAssertEqual(status, 0)
            XCTAssertTrue(detail.contains("Keychain access denied"), "Got detail: \(detail)")
            XCTAssertFalse(err.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            XCTAssertFalse(err.message.contains("sheepdog logs"), "Keychain error must not tell user to check sheepdog logs: \(err.message)")
        }
    }

    // MARK: - Single-use codes (second click asks again)

    func testSecondClickRequestsFreshCodeBecauseCodesAreSingleUse() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 200, body: #"{"code":"AAAA-2222","expires":"2026-11-22T12:05:00Z","expires_in":300}"#),
            .json(status: 200, body: #"{"code":"BBBB-3333","expires":"2026-11-22T12:06:00Z","expires_in":300}"#),
        ])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { presenter.shownCodes.count >= 1 }
        controller.perform(.pairBrowser)
        await waitUntil { presenter.shownCodes.count >= 2 }

        XCTAssertEqual(StubBrowserPairDaemon.requests.count, 2)
        XCTAssertTrue(StubBrowserPairDaemon.requests.allSatisfy {
            $0.httpMethod == "POST"
                && $0.url?.path == "/api/pair/browser-code"
                && $0.value(forHTTPHeaderField: "Authorization") == "Bearer sds_mac-session-ticket"
        })
        XCTAssertEqual(presenter.shownCodes.map(\.code), ["AAAA-2222", "BBBB-3333"])
    }
}
