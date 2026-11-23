import Foundation
import XCTest
import SheepdogKit
@testable import SheepdogMac

/// In-process HTTP stub for `POST /api/pair/browser-code` — no socket is opened.
private final class StubBrowserPairDaemon: URLProtocol {
    enum Reply {
        case json(status: Int, body: String)
        case failure(URLError.Code)
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var _requests: [URLRequest] = []
    nonisolated(unsafe) private static var _replies: [Reply] = []

    static func reset(replies: [Reply] = []) {
        lock.withLock {
            _requests = []
            _replies = replies
        }
    }

    static var requests: [URLRequest] {
        lock.withLock { _requests }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let reply: Reply? = Self.lock.withLock {
            Self._requests.append(request)
            return Self._replies.isEmpty ? nil : Self._replies.removeFirst()
        }
        switch reply {
        case let .json(status, body):
            let response = HTTPURLResponse(
                url: request.url!,
                statusCode: status,
                httpVersion: nil,
                headerFields: ["Content-Type": "application/json", "Cache-Control": "no-store"]
            )!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(body.utf8))
            client?.urlProtocolDidFinishLoading(self)
        case let .failure(code):
            client?.urlProtocol(self, didFailWithError: URLError(code))
        case nil:
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
        }
    }

    override func stopLoading() {}
}

private final class FakeBrowserPairingSessionProvider: BrowserPairingSessionProviding {
    var result: Result<SessionTicket, Error>
    private(set) var calls = 0

    init(ticket: String = "mac-session-ticket") {
        result = .success(SessionTicket(ticket: ticket, expires: Date().addingTimeInterval(3600)))
    }

    init(error: Error) {
        result = .failure(error)
    }

    func sessionTicket(base: URL) async throws -> SessionTicket {
        calls += 1
        return try result.get()
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

    func testPairBrowserMenuItemHasVoiceOverLabel() {
        let label = StatusItemController.pairBrowserAccessibilityLabel
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

    // MARK: - Error cases (plain fix messages)

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
            error.message.localizedCaseInsensitiveContains("running")
                || error.message.localizedCaseInsensitiveContains("start"),
            "Expected plain fix for daemon down, got: \(error.message)"
        )
    }

    func testNotEnrolledShowsPlainFix() async throws {
        let (controller, _, presenter) = makeController(
            sessionProvider: FakeBrowserPairingSessionProvider(error: BrowserPairingError.notEnrolled)
        )
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        XCTAssertEqual(error, .notEnrolled)
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertTrue(StubBrowserPairDaemon.requests.isEmpty, "Should not call daemon when Mac app is not enrolled")
    }

    func testRefusedByDaemonShowsPlainFix() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 403, body: #"{"error":"device_session_required","reason":"device_session_required"}"#),
        ])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        switch error {
        case .notEnrolled, .refused:
            break
        default:
            XCTFail("Expected .notEnrolled or .refused for 403, got \(error)")
        }
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    func testServerRefusalShowsPlainFix() async throws {
        StubBrowserPairDaemon.reset(replies: [
            .json(status: 500, body: #"{"error":"browser pairing temporarily unavailable"}"#),
        ])
        let (controller, _, presenter) = makeController()
        controller.update(listening)

        controller.perform(.pairBrowser)
        await waitUntil { !presenter.shownErrors.isEmpty }

        let error = try XCTUnwrap(presenter.shownErrors.first)
        XCTAssertEqual(error, .refused(status: 500, message: "browser pairing temporarily unavailable"))
        XCTAssertFalse(error.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        XCTAssertFalse(error.message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
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
