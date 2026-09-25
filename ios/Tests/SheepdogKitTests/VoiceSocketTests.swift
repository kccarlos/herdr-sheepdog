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

    // MARK: - The speech detector on every open, and the ping

    func testEveryOpenSaysTheSpeechDetectorsStateAfterMute() async throws {
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        socket.speaking = false
        socket.connect()
        defer { socket.disconnect() }
        let texts = try await server.texts(count: 2, timeout: 10)
        let messages = try texts.map { try WSMessage.decode(Data($0.utf8)) }
        XCTAssertEqual(messages, [.mute(false), .speaking(false)], "speaking is authoritative from the first open")
    }

    func testAPingWaitsForItsPongThenTimesTheSocketOut() async throws {
        XCTAssertEqual(VoiceSocket.pingInterval, 5, "the daemon's web.client.stats interval")
        XCTAssertEqual(VoiceSocket.pongTimeout, 10)
        let sent = Date(timeIntervalSinceReferenceDate: 0)
        XCTAssertFalse(PingSchedule.timedOut(sentAt: sent, now: sent.addingTimeInterval(5)))
        XCTAssertTrue(PingSchedule.timedOut(sentAt: sent, now: sent.addingTimeInterval(10)))

        // A server that never answers a JSON ping.
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        var closedReason: String?
        socket.onEvent = { event in
            if case let .closed(reason, _) = event { closedReason = reason }
        }
        socket.connect()
        defer { socket.disconnect() }
        _ = try await server.texts(count: 2, timeout: 10)  // mute, speaking

        let t0 = Date()
        socket.pingTick(now: t0)
        socket.pingTick(now: t0.addingTimeInterval(5))
        let texts = try await server.texts(count: 3, timeout: 5)
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(server.received.count, 3, "one ping outstanding, not two: \(server.received)")
        XCTAssertEqual(try WSMessage.decode(Data(texts[2].utf8)).type, .ping)
        XCTAssertNil(closedReason)

        socket.pingTick(now: t0.addingTimeInterval(10))
        XCTAssertEqual(closedReason, "no answer from the daemon")
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
    private var first: String?
    private var all: [String] = []
    private var waiters: [(count: Int, continuation: CheckedContinuation<[String], Error>)] = []

    /// Every text frame so far (read on the server's queue).
    var received: [String] { queue.sync { all } }

    /// Waits until at least `count` text frames arrived; returns them all.
    func texts(count: Int, timeout: TimeInterval) async throws -> [String] {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                if self.all.count >= count {
                    continuation.resume(returning: self.all)
                    return
                }
                self.waiters.append((count, continuation))
                self.queue.asyncAfter(deadline: .now() + timeout) {
                    if let i = self.waiters.firstIndex(where: { $0.count == count }) {
                        self.waiters.remove(at: i).continuation.resume(throwing: URLError(.timedOut))
                    }
                }
            }
        }
    }

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
                if let first = self.first {
                    continuation.resume(returning: first)
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
            if metadata?.opcode == .text, let data, let text = String(data: data, encoding: .utf8) {
                self.all.append(text)
                if self.first == nil {
                    self.first = text
                    self.textContinuation?.resume(returning: text)
                    self.textContinuation = nil
                }
                let ready = self.waiters.filter { $0.count <= self.all.count }
                self.waiters.removeAll { $0.count <= self.all.count }
                for waiter in ready { waiter.continuation.resume(returning: self.all) }
            }
            self.receive(on: connection)
        }
    }
}
