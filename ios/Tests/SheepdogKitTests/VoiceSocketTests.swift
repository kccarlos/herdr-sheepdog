import Network
import XCTest
@testable import SheepdogKit

/// `VoiceSocket` against a real local WebSocket server (Network.framework,
/// in-process) and against a port nobody listens on.
@MainActor
final class VoiceSocketTests: XCTestCase {
    // MARK: - A daemon that is down (the restart case)

    func testAnUnreachableDaemonFailsAsRetryingAndTheSocketKeepsRetrying() async throws {
        // Port 9 (discard) on loopback: nothing listens, the connect is refused.
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: 9))
        var failedRetrying: Bool?
        var closedRetrying: Bool?
        let closed = expectation(description: "closed")
        socket.onEvent = { event in
            switch event {
            case let .failed(_, retrying): failedRetrying = retrying
            case let .closed(_, retrying):
                if closedRetrying == nil {
                    closedRetrying = retrying
                    closed.fulfill()
                }
            default: break
            }
        }
        socket.connect()
        await fulfillment(of: [closed], timeout: 10)
        XCTAssertEqual(failedRetrying, true, "the view model must know this failure is being retried")
        XCTAssertEqual(closedRetrying, true)
        XCTAssertEqual(socket.life, .reconnecting, "the socket goes on retrying by itself")
        socket.disconnect()
        XCTAssertEqual(socket.life, .idle)
    }

    // MARK: - Mute on every open (the app owns mute)

    func testEveryOpenSaysNotMutedToo() async throws {
        let first = try await firstMessage(muted: false)
        XCTAssertEqual(first.type, .mute)
        XCTAssertEqual(first.on, false, "an unmuted app says so, so a daemon's stale mute is cleared")
    }

    func testEveryOpenSaysMuted() async throws {
        let first = try await firstMessage(muted: true)
        XCTAssertEqual(first.type, .mute)
        XCTAssertEqual(first.on, true)
    }

    // MARK: - Helpers

    /// Opens a `VoiceSocket` to a local WebSocket server and returns the
    /// first control message the server receives.
    private func firstMessage(muted: Bool) async throws -> WSMessage {
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        socket.muted = muted
        socket.connect()
        defer { socket.disconnect() }
        let text = try await server.firstText(timeout: 10)
        return try WSMessage.decode(Data(text.utf8))
    }
}

/// A one-shot WebSocket server on loopback. It greets each client with a
/// `status` (the socket reports "open" on the first message it receives,
/// as it does against the daemon) and records the first text frame.
private final class LocalWebSocketServer: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "test.ws.server")
    private var connections: [NWConnection] = []
    private var readyContinuation: CheckedContinuation<UInt16, Error>?
    private var textContinuation: CheckedContinuation<String, Error>?
    private var received: String?

    init() throws {
        let parameters = NWParameters.tcp
        let options = NWProtocolWebSocket.Options()
        options.autoReplyPing = true
        parameters.defaultProtocolStack.applicationProtocols.insert(options, at: 0)
        parameters.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
    }

    func start() async throws -> UInt16 {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                self.readyContinuation = continuation
                self.listener.stateUpdateHandler = { [weak self] state in
                    guard let self else { return }
                    switch state {
                    case .ready:
                        self.readyContinuation?.resume(returning: self.listener.port?.rawValue ?? 0)
                        self.readyContinuation = nil
                    case let .failed(error):
                        self.readyContinuation?.resume(throwing: error)
                        self.readyContinuation = nil
                    default:
                        break
                    }
                }
                self.listener.newConnectionHandler = { [weak self] connection in
                    self?.accept(connection)
                }
                self.listener.start(queue: self.queue)
            }
        }
    }

    func firstText(timeout: TimeInterval) async throws -> String {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                if let received = self.received {
                    continuation.resume(returning: received)
                    return
                }
                self.textContinuation = continuation
                self.queue.asyncAfter(deadline: .now() + timeout) {
                    self.textContinuation?.resume(throwing: URLError(.timedOut))
                    self.textContinuation = nil
                }
            }
        }
    }

    func stop() {
        queue.async {
            for connection in self.connections { connection.cancel() }
            self.listener.cancel()
        }
    }

    private func accept(_ connection: NWConnection) {
        connections.append(connection)
        connection.stateUpdateHandler = { [weak self] state in
            guard let self, case .ready = state else { return }
            let status = #"{"type":"status","voice":"connected","muted":false}"#
            let metadata = NWProtocolWebSocket.Metadata(opcode: .text)
            let context = NWConnection.ContentContext(identifier: "status", metadata: [metadata])
            connection.send(content: Data(status.utf8), contentContext: context, isComplete: true, completion: .idempotent)
            self.receive(on: connection)
        }
        connection.start(queue: queue)
    }

    private func receive(on connection: NWConnection) {
        connection.receiveMessage { [weak self] data, context, _, error in
            guard let self, error == nil else { return }
            let metadata = context?.protocolMetadata(definition: NWProtocolWebSocket.definition) as? NWProtocolWebSocket.Metadata
            if metadata?.opcode == .text, let data, let text = String(data: data, encoding: .utf8), self.received == nil {
                self.received = text
                self.textContinuation?.resume(returning: text)
                self.textContinuation = nil
            }
            self.receive(on: connection)
        }
    }
}
