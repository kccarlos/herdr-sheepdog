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
///   mute state (muted *or not*: the app owns it), a closed gate and the
///   speech detector's state are sent on every open — the last one makes
///   `speaking` authoritative for the connection from its first message;
/// * the ping goes every 5 s (the daemon's `web.client.stats` interval) and
///   waits for its pong before the next one, like `/voice`: a ping still
///   unanswered after 10 s drops the socket.
@MainActor
public final class VoiceSocket: VoiceTransport {
    /// The transport events (`VoiceTransportEvent`), under the name this
    /// socket has always used for them.
    public typealias Event = VoiceTransportEvent

    /// Browser constants (`web/voice/app.js`: `PING_EVERY_MS`,
    /// `PONG_TIMEOUT_MS`, the reconnect backoff).
    public static let pingInterval: TimeInterval = 5
    public static let pongTimeout: TimeInterval = 10
    public static let retryMinimum: TimeInterval = 1
    public static let retryMaximum: TimeInterval = 10

    /// The socket's own connection state — internal bookkeeping (and a
    /// `VoiceSocketTests` observation point) since PR #11's `ReconnectPolicy`
    /// moved reconnect *decisions* to the view model, which now tracks its
    /// own life from `onEvent` rather than reading this. Backing off after a
    /// drop is `.reconnecting`, the same case `SessionState` and
    /// `ConnectionChip` use for it.
    private(set) var life: SocketLife = .idle

    /// What the caller wants the daemon to believe on every (re)connection.
    public var muted = false
    public var gateClosed = false
    /// The app's speech detector is open (it hears the shepherd).
    public var speaking = false
    /// Counters copied onto the `ping`.
    public var gatedFrames = 0
    public var inputPeak = 0
    /// The playback buffer's counters, also copied onto the `ping`:
    /// audible gaps, times the player ran dry, audio buffered now (ms),
    /// the current time-stretch rate and total stretched time (ms).
    public var underruns = 0
    public var drains = 0
    public var bufferMs = 0
    public var rate = 1.0
    public var stretchedMs = 0
    /// The audio engine's state, copied onto the `ping` (`audio_state`).
    public var audioState: String?

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
            onEvent?(.failed(.malformed("the address \(endpoint.display) is not a URL"), retrying: false))
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
        retryDelay = VoiceSocket.retryMinimum
        // A reconnecting client starts unmuted and ungated on the daemon,
        // or with whatever mute it last had: say which, every time.
        send(.mute(muted))
        if gateClosed { send(.silence(true)) }
        send(.speaking(speaking))
        startPinging()
        onEvent?(.opened)
    }

    private func handle(text: String) {
        guard let message = try? WSMessage.decode(Data(text.utf8)) else { return }
        if message.type == .pong {
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
                onEvent?(.failed(.unreachable(endpoint: endpoint.display, detail: DaemonClient.describe(urlError)), retrying: true))
            }
        }
        onEvent?(.closed(reason: everOpened ? "connection lost" : "could not connect", retrying: true))
        scheduleRetry()
    }

    private func scheduleRetry() {
        let delay = retryDelay
        retryDelay = min(retryDelay * 2, VoiceSocket.retryMaximum)
        life = .reconnecting
        retryTask?.cancel()
        retryTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled, !self.userClosed else { return }
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

    /// One ping period. A ping still out waits for its pong rather than
    /// being replaced (a new one would restart the clock, and at 5 s the
    /// 10 s timeout could never fire); past the timeout the socket drops.
    func pingTick(now: Date = Date()) {
        if let sent = pingSentAt {
            guard PingSchedule.timedOut(sentAt: sent, now: now) else { return }
            // The daemon stopped answering: drop the socket so the normal
            // reconnect path takes over.
            onEvent?(.closed(reason: "no answer from the daemon", retrying: true))
            teardown(code: .abnormalClosure, reason: "pong timeout")
            scheduleRetry()
            return
        }
        pingSentAt = now
        if !send(.ping(
            gatedFrames: gatedFrames,
            peak: inputPeak,
            underruns: underruns,
            drains: drains,
            bufferMs: bufferMs,
            rate: (rate * 1000).rounded() / 1000,
            stretchedMs: stretchedMs,
            audioState: audioState
        )) {
            pingSentAt = nil
        }
    }
}

/// The ping's timing rule, apart from the socket so it is testable: send
/// only when no ping is outstanding, and give up on one after
/// `VoiceSocket.pongTimeout`.
public enum PingSchedule {
    /// The outstanding ping sent at `sentAt` has waited too long.
    public static func timedOut(sentAt: Date, now: Date) -> Bool {
        now.timeIntervalSince(sentAt) >= VoiceSocket.pongTimeout
    }
}
