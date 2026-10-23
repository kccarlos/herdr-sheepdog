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

    func testEveryOpenStartsWithClientReady() async throws {
        let first = try await openingMessages(muted: false)[0]
        XCTAssertEqual(first, .clientReady(client: Self.client), "the handshake goes before anything else")
    }

    func testEveryOpenSaysNotMutedToo() async throws {
        let second = try await openingMessages(muted: false)[1]
        XCTAssertEqual(second.type, .mute)
        XCTAssertEqual(second.on, false, "an unmuted app says so, so a daemon's stale mute is cleared")
    }

    func testEveryOpenSaysMuted() async throws {
        let second = try await openingMessages(muted: true)[1]
        XCTAssertEqual(second.type, .mute)
        XCTAssertEqual(second.on, true)
    }

    // MARK: - The speech detector on every open, and the ping

    func testEveryOpenSaysTheSpeechDetectorsStateAfterMute() async throws {
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        socket.speaking = false
        socket.client = Self.client
        socket.connect()
        defer { socket.disconnect() }
        let texts = try await server.texts(count: 3, timeout: 10)
        let messages = try texts.map { try WSMessage.decode(Data($0.utf8)) }
        XCTAssertEqual(messages, [.clientReady(client: Self.client), .mute(false), .speaking(false)], "speaking is authoritative from the first open")
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
        _ = try await server.texts(count: 3, timeout: 10)  // client-ready, mute, speaking

        let t0 = Date()
        socket.pingTick(now: t0)
        socket.pingTick(now: t0.addingTimeInterval(5))
        let texts = try await server.texts(count: 4, timeout: 5)
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertEqual(server.received.count, 4, "one ping outstanding, not two: \(server.received)")
        XCTAssertEqual(try WSMessage.decode(Data(texts[3].utf8)).type, .ping)
        XCTAssertNil(closedReason)

        socket.pingTick(now: t0.addingTimeInterval(10))
        XCTAssertEqual(closedReason, "no answer from the daemon")
    }

    // MARK: - The protocol handshake

    func testBotReadyCompletesTheHandshakeAndIsNotHandedOn() async throws {
        let (server, socket, events) = try await handshakeRig()
        defer { server.stop(); socket.disconnect() }
        server.send(#"{"type":"bot-ready","protocol":1,"min_protocol":1,"daemon":{"version":"0.4.2"},"features":["playback"]}"#)
        try await waitUntil { socket.handshake.state.negotiatedProtocol == 1 }
        XCTAssertEqual(socket.handshake.state, .ready(protocol: 1, daemonVersion: "0.4.2", features: ["playback"]))
        XCTAssertTrue(events.handshakes.contains(.waiting))
        XCTAssertFalse(events.controls.contains { $0.type == .botReady }, "the session never sees bot-ready")
    }

    func testAnOlderDaemonsUnknownTypeErrorIsNotShown() async throws {
        let (server, socket, events) = try await handshakeRig()
        defer { server.stop(); socket.disconnect() }
        server.send(#"{"type":"error","message":"wsproto: unknown type \"client-ready\""}"#)
        try await waitUntil { socket.handshake.state == .legacy }
        XCTAssertFalse(events.controls.contains { $0.type == .error }, "an old daemon's answer is not a problem")
        XCTAssertEqual(socket.life, .open, "and the session carries on")
    }

    func testNoBotReadyMeansProtocolOneAfterTheTimeout() async throws {
        let (server, socket, _) = try await handshakeRig()
        defer { server.stop(); socket.disconnect() }
        XCTAssertEqual(socket.handshake.state, .waiting)
        socket.handshakeTimedOut()
        XCTAssertEqual(socket.handshake.state, .legacy)
        XCTAssertEqual(socket.life, .open)
    }

    func testARefusedAppClosesWithoutRetrying() async throws {
        let (server, socket, events) = try await handshakeRig()
        defer { server.stop(); socket.disconnect() }
        server.send(#"{"type":"error","message":"This app speaks /ws protocol 1; the daemon (version 2.0) needs protocol 2 or newer. Update the app."}"#)
        server.send(#"{"type":"status","voice":"closed","muted":false,"reason":"protocol_too_old"}"#)
        try await waitUntil { events.closed != nil }
        XCTAssertEqual(events.closed?.reason, ProtocolHandshake.closeReason)
        XCTAssertEqual(events.closed?.retrying, false, "a mismatch does not fix itself")
        XCTAssertEqual(socket.life, .idle)
        XCTAssertTrue(socket.handshake.isMismatch)
        XCTAssertFalse(events.controls.contains { $0.type == .status && $0.voice == .closed }, "no \"voice closed\" notice on top of the banner")
    }

    // MARK: - Helpers

    static let client = WSMessage.ClientInfo(name: "sheepdog-ios", version: "test", platform: "ios")

    /// What a socket reported, for the handshake tests.
    @MainActor
    final class Recorder {
        var handshakes: [ProtocolHandshake.State] = []
        var controls: [WSMessage] = []
        var closed: (reason: String, retrying: Bool)?
    }

    /// A socket opened against a local server, past its opening frames.
    private func handshakeRig() async throws -> (LocalWebSocketServer, VoiceSocket, Recorder) {
        let server = try LocalWebSocketServer()
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        socket.client = Self.client
        let recorder = Recorder()
        socket.onEvent = { event in
            switch event {
            case let .handshake(state): recorder.handshakes.append(state)
            case let .control(message): recorder.controls.append(message)
            case let .closed(reason, retrying): recorder.closed = (reason, retrying)
            default: break
            }
        }
        socket.connect()
        _ = try await server.texts(count: 3, timeout: 10)
        return (server, socket, recorder)
    }

    private func waitUntil(timeout: TimeInterval = 5, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            if Date() > deadline { throw URLError(.timedOut) }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
    }

    /// Opens a `VoiceSocket` to a local WebSocket server and returns the
    /// first two control messages the server receives.
    private func openingMessages(muted: Bool) async throws -> [WSMessage] {
        let server = try LocalWebSocketServer()
        defer { server.stop() }
        let port = try await server.start()
        let socket = VoiceSocket(endpoint: DaemonEndpoint(host: "127.0.0.1", port: Int(port)))
        socket.muted = muted
        socket.client = Self.client
        socket.connect()
        defer { socket.disconnect() }
        let texts = try await server.texts(count: 2, timeout: 10)
        return try texts.map { try WSMessage.decode(Data($0.utf8)) }
    }
}

/// A one-shot WebSocket server on loopback. It greets each client with a
/// `status` (the socket reports "open" on the first message it receives,
/// as it does against the daemon) and records the first text frame.
final class LocalWebSocketServer: @unchecked Sendable {
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

    /// Sends one text frame to every client.
    func send(_ text: String) {
        queue.async {
            for connection in self.connections {
                let metadata = NWProtocolWebSocket.Metadata(opcode: .text)
                let context = NWConnection.ContentContext(identifier: "text", metadata: [metadata])
                connection.send(content: Data(text.utf8), contentContext: context, isComplete: true, completion: .idempotent)
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
