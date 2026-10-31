import XCTest
import SheepdogKit
@testable import SheepdogMac

/// Answers the daemon's two endpoints in process: no socket is opened.
private final class StubDaemon: URLProtocol {
    /// nil: refuse the connection.
    nonisolated(unsafe) static var stateBody: String?
    nonisolated(unsafe) static var eventsBody = ""

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url?.path ?? ""
        guard let state = Self.stateBody else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let body = path == "/events" ? Self.eventsBody : state
        let type = path == "/events" ? "text/event-stream" : "application/json"
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": type])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

final class DaemonStatusMonitorTests: XCTestCase {
    private func monitor() -> DaemonStatusMonitor {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubDaemon.self]
        return DaemonStatusMonitor(locator: FakeLocator(), session: URLSession(configuration: config), retryDelay: 0.05, coalesce: 0.01)
    }

    private let state = #"{"agents":[],"voice":{"mode":"gemini","status":"connected","muted":true,"stalled":false,"speaking":false,"client_connected":true,"client":{"name":"sheepdog-mac","platform":"macos"}}}"#

    func testPublishesTheStateAndAnEventRereadsIt() {
        StubDaemon.stateBody = state
        StubDaemon.eventsBody = "id: 1\nevent: log\ndata: {\"type\":\"voice.client\",\"source\":\"web\"}\n\n"
        let m = monitor()
        let got = expectation(description: "muted state, twice")
        got.expectedFulfillmentCount = 2
        m.onChange = { daemon in
            if daemon?.voice.muted == true { got.fulfill() }
        }
        m.start()
        wait(for: [got], timeout: 5)
        m.stop()
    }

    func testADaemonThatIsNotThereIsNil() {
        StubDaemon.stateBody = nil
        let m = monitor()
        let down = expectation(description: "nil")
        down.assertForOverFulfill = false
        m.onChange = { daemon in
            if daemon == nil { down.fulfill() }
        }
        m.start()
        wait(for: [down], timeout: 5)
        m.stop()
    }
}
