import Foundation

/// The `/ws` connection: binary PCM frames both ways, JSON control frames,
/// a liveness ping, and reconnect with backoff.
///
/// Everything here mirrors the browser page (`web/voice/app.js`) and
/// docs/ws-contract.md:
///
/// * the `ping` is the contract's **JSON** message (it carries the gate
///   counters the daemon logs, plus the jitter buffer's `underruns` and
///   `buffer_ms`), not `URLSessionWebSocketTask.sendPing`;
/// * `status: closed` with `reason: replaced` means another client took the
///   session — reconnecting would start a fight, so the socket stays down
///   and the user decides;
/// * a reconnecting client starts unmuted and ungated on the daemon, so the
///   mute and gate state are re-sent on every open.
@MainActor
public final class VoiceSocket {
    public enum Event: Sendable {
        case opened
        case control(WSMessage)
        case audio(Data)
        /// The socket went away. `retrying` is false when the app will not
        /// reconnect by itself (the user disconnected, or we were replaced).
        case closed(reason: String, retrying: Bool)
        case failed(DaemonError)
    }

    public enum Life: Equatable, Sendable {
        case idle, connecting, open, waitingToRetry
    }

    /// Browser constants (`web/voice/app.js`).
    public static let pingInterval: TimeInterval = 15
    public static let pongTimeout: TimeInterval = 10
    public static let retryMinimum: TimeInterval = 1
    public static let retryMaximum: TimeInterval = 10

    public private(set) var life: Life = .idle
    public private(set) var reconnects = 0
    public private(set) var lastRoundTrip: TimeInterval?

    /// What the caller wants the daemon to believe on every (re)connection.
    public var muted = false
    public var gateClosed = false
    /// Counters copied onto the `ping`.
    public var gatedFrames = 0
    public var inputPeak = 0
    /// The adaptive jitter buffer's audible-gap count and current target
    /// (ms), also copied onto the `ping`.
    public var underruns = 0
    public var bufferMs = 0

    public var onEvent: ((Event) -> Void)?

    private var endpoint: DaemonEndpoint
    private let session: URLSession
    private var task: URLSessionWebSocketTask?
    private var receiveLoop: Task<Void, Never>?
    private var pingLoop: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    private var retryDelay: TimeInterval = VoiceSocket.retryMinimum
    private var userClosed = true
    private var pingSentAt: Date?
    private var generation = 0

    public init(endpoint: DaemonEndpoint, session: URLSession? = nil) {
        self.endpoint = endpoint
        if let session {
            self.session = session
        } else {
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 60
            config.shouldUseExtendedBackgroundIdleMode = true
            self.session = URLSession(configuration: config)
        }
    }

    public func update(endpoint: DaemonEndpoint) {
        self.endpoint = endpoint
    }

    public var isOpen: Bool { life == .open }

    // MARK: - Lifecycle

    public func connect() {
        userClosed = false
        retryDelay = VoiceSocket.retryMinimum
        open()
    }

    public func disconnect(reason: String = "user disconnect") {
        userClosed = true
        teardown(code: .goingAway, reason: reason)
        life = .idle
    }

    private func open() {
        guard let url = endpoint.webSocketURL else {
            onEvent?(.failed(.malformed("the address \(endpoint.display) is not a URL")))
            return
        }
        teardown(code: .goingAway, reason: "reopening")
        generation += 1
        let generation = self.generation
        var request = URLRequest(url: url)
        request.timeoutInterval = 15
        if !endpoint.token.isEmpty {
            // URLSession can set a header on the upgrade request, which a
            // browser cannot — so the app never needs `?token=`.
            request.setValue("Bearer \(endpoint.token)", forHTTPHeaderField: "Authorization")
        }
        let task = session.webSocketTask(with: request)
        // Model chunks can be ~700 ms of 24 kHz PCM16; leave plenty of room.
        task.maximumMessageSize = 8 * 1024 * 1024
        self.task = task
        life = .connecting
        task.resume()
        receiveLoop = Task { [weak self] in
            await self?.receive(on: task, generation: generation)
        }
    }

    private func teardown(code: URLSessionWebSocketTask.CloseCode = .goingAway, reason: String) {
        retryTask?.cancel()
        retryTask = nil
        pingLoop?.cancel()
        pingLoop = nil
        receiveLoop?.cancel()
        receiveLoop = nil
        pingSentAt = nil
        task?.cancel(with: code, reason: Data(reason.utf8))
        task = nil
    }

    // MARK: - Receiving

    private func receive(on task: URLSessionWebSocketTask, generation: Int) async {
        var opened = false
        while !Task.isCancelled {
            do {
                let message = try await task.receive()
                guard generation == self.generation else { return }
                if !opened {
                    opened = true
                    didOpen()
                }
                switch message {
                case let .data(data):
                    onEvent?(.audio(data))
                case let .string(text):
                    handle(text: text)
                @unknown default:
                    break
                }
            } catch {
                guard generation == self.generation else { return }
                didClose(error: error, everOpened: opened)
                return
            }
        }
    }

    private func didOpen() {
        life = .open
        reconnects = 0
        retryDelay = VoiceSocket.retryMinimum
        // A reconnecting client starts unmuted and ungated on the daemon.
        if muted { send(.mute(true)) }
        if gateClosed { send(.silence(true)) }
        startPinging()
        onEvent?(.opened)
    }

    private func handle(text: String) {
        guard let message = try? WSMessage.decode(Data(text.utf8)) else { return }
        if message.type == .pong {
            if let sent = pingSentAt { lastRoundTrip = Date().timeIntervalSince(sent) }
            pingSentAt = nil
        }
        if message.type == .status, message.voice == .closed, message.reason == "replaced" {
            // Another client owns the session now. Reconnecting would make
            // the two of us fight over it.
            onEvent?(.control(message))
            userClosed = true
            teardown(reason: "replaced")
            life = .idle
            onEvent?(.closed(reason: "replaced", retrying: false))
            return
        }
        onEvent?(.control(message))
    }

    private func didClose(error: Error, everOpened: Bool) {
        pingLoop?.cancel()
        pingLoop = nil
        task = nil
        if userClosed {
            life = .idle
            return
        }
        if let urlError = error as? URLError {
            if urlError.code == .cancelled {
                life = .idle
                return
            }
            if !everOpened {
                // Never reached the daemon: say why, and keep retrying so a
                // daemon that comes back is picked up.
                onEvent?(.failed(.unreachable(endpoint: endpoint.display, detail: DaemonClient.describe(urlError))))
            }
        }
        onEvent?(.closed(reason: everOpened ? "connection lost" : "could not connect", retrying: true))
        scheduleRetry()
    }

    private func scheduleRetry() {
        let delay = retryDelay
        retryDelay = min(retryDelay * 2, VoiceSocket.retryMaximum)
        life = .waitingToRetry
        retryTask?.cancel()
        retryTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled, !self.userClosed else { return }
            self.reconnects += 1
            self.open()
        }
    }

    // MARK: - Sending

    @discardableResult
    public func send(_ message: WSMessage) -> Bool {
        guard let task, life == .open, let data = try? message.encode() else { return false }
        task.send(.string(String(decoding: data, as: UTF8.self))) { _ in }
        return true
    }

    /// One binary frame of PCM16 LE mono 16 kHz.
    @discardableResult
    public func send(frame: Data) -> Bool {
        guard let task, life == .open else { return false }
        task.send(.data(frame)) { _ in }
        return true
    }

    private func startPinging() {
        pingLoop?.cancel()
        pingSentAt = nil
        pingLoop = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(VoiceSocket.pingInterval * 1_000_000_000))
                guard let self, !Task.isCancelled else { return }
                self.pingTick()
            }
        }
    }

    private func pingTick() {
        if let sent = pingSentAt, Date().timeIntervalSince(sent) > VoiceSocket.pongTimeout {
            // The daemon stopped answering: drop the socket so the normal
            // reconnect path takes over.
            onEvent?(.closed(reason: "no answer from the daemon", retrying: true))
            teardown(code: .abnormalClosure, reason: "pong timeout")
            scheduleRetry()
            return
        }
        pingSentAt = Date()
        if !send(.ping(gatedFrames: gatedFrames, peak: inputPeak, underruns: underruns, bufferMs: bufferMs)) {
            pingSentAt = nil
        }
    }
}
