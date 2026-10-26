import Foundation

/// The `/ws` connection: binary PCM frames both ways, JSON control frames
/// and a liveness ping. It is a channel of the current computer's link
/// (`LinkChannelClient`): it never retries by itself. A drop is reported to
/// the link, which probes once and says when to open it again
/// (docs/dev/design/ios-state-architecture.md 3.2 "One schedule per
/// computer").
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
/// * `client-ready` is the first frame on every open (the protocol
///   handshake, `ProtocolHandshake`); nothing waits for `bot-ready`, and a
///   protocol mismatch closes the socket without reconnecting, like
///   `replaced`;
/// * the ping goes every 5 s (the daemon's `web.client.stats` interval) and
///   waits for its pong before the next one, like `/voice`: a ping still
///   unanswered after 10 s drops the socket.
///
/// It opens only while it is both wanted (`connect()` until
/// `disconnect()`) and allowed by the link (`linkAllows`).
@MainActor
public final class VoiceSocket: LinkChannelClient {
    public enum Event: Sendable {
        case opened
        case control(WSMessage)
        case audio(Data)
        /// The socket went away. `retrying` is true when the link will open
        /// it again (it was lost), false when nothing will (we were
        /// replaced, or the protocol does not match).
        case closed(reason: String, retrying: Bool)
        /// An attempt failed. `retrying` is true when the link will try
        /// again (the daemon was unreachable), false when nothing will (the
        /// address is not a URL).
        case failed(DaemonError, retrying: Bool)
        /// The link closed the socket (offline, no network, the
        /// background): it opens again when the link allows it. Not a
        /// loss, so the link is not told.
        case paused
        /// The protocol handshake moved (`ProtocolHandshake.State`).
        case handshake(ProtocolHandshake.State)
    }

    /// Browser constants (`web/voice/app.js`: `PING_EVERY_MS`,
    /// `PONG_TIMEOUT_MS`).
    public static let pingInterval: TimeInterval = 5
    public static let pongTimeout: TimeInterval = 10

    /// The socket's own connection state: internal bookkeeping (and a
    /// `VoiceSocketTests` observation point); the view model tracks its own
    /// life from `onEvent`. Waiting for the link after a drop is
    /// `.reconnecting`, the same case `SessionState` uses for it.
    private(set) var life: SocketLife = .idle
    /// The link allows the socket (`linkAllows`). True until a link says
    /// otherwise, so a socket with no link (the tests) opens on `connect`.
    public private(set) var allowed = true
    /// What to tell the link, as of the last `linkAllows`/`linkReopens`.
    private var reporter: LinkChannelReporter?
    /// The reporter the open socket was opened with: its reports go to the
    /// computer and epoch it belongs to, never to a later one.
    private var openReporter: LinkChannelReporter?

    /// What the caller wants the daemon to believe on every (re)connection.
    public var muted = false
    public var gateClosed = false
    /// The app's speech detector is open (it hears the user).
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

    /// Who the app says it is on `client-ready`.
    public var client = WSMessage.ClientInfo.app()
    /// The handshake of the current (or last) connection.
    public private(set) var handshake = ProtocolHandshake()

    private var endpoint: DaemonEndpoint
    private let session: URLSession
    private var task: URLSessionWebSocketTask?
    private var receiveLoop: Task<Void, Never>?
    private var pingLoop: Task<Void, Never>?
    private var handshakeTimer: Task<Void, Never>?
    private var userClosed = true
    private var pingSentAt: Date?
    private var generation = 0

    /// The pins of the endpoint's computer, followed across `update`.
    private let trust: EndpointTrust

    public init(endpoint: DaemonEndpoint, session: URLSession? = nil) {
        self.endpoint = endpoint
        trust = EndpointTrust(endpoint.credentials)
        if let session {
            self.session = session
        } else {
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 60
            config.shouldUseExtendedBackgroundIdleMode = true
            self.session = trust.session(configuration: config)
        }
    }

    public func update(endpoint: DaemonEndpoint) {
        self.endpoint = endpoint
        trust.follow(endpoint.credentials)
    }

    // MARK: - Lifecycle

    /// The voice wants the socket: it opens now if the link allows it, else
    /// as soon as it does.
    public func connect() {
        userClosed = false
        if allowed {
            open()
        } else {
            life = .connecting
        }
    }

    public func disconnect(reason: String = "user disconnect") {
        userClosed = true
        teardown(code: .goingAway, reason: reason)
        life = .idle
    }

    /// Whether the voice wants the socket (from `connect` until
    /// `disconnect`, a replacement or a protocol mismatch).
    public var wanted: Bool { !userClosed }

    // MARK: - The link

    public func linkAllows(_ allowed: Bool, reporter: LinkChannelReporter) {
        self.reporter = reporter
        self.allowed = allowed
        if allowed {
            if wanted, task == nil { open() }
        } else if task != nil || life == .connecting {
            teardown(reason: "paused")
            guard wanted else {
                life = .idle
                return
            }
            life = .reconnecting
            onEvent?(.paused)
        }
    }

    /// Opens it again: after a loss, or (with a socket still up) after the
    /// interface changed, when the old socket is dead before its ping says
    /// so. The new socket replaces it.
    public func linkReopens(reporter: LinkChannelReporter) {
        self.reporter = reporter
        guard wanted, allowed else { return }
        open()
    }

    private func open() {
        guard let url = endpoint.webSocketURL else {
            let error: DaemonError = endpoint.allowsConnection
                ? .malformed("the address \(endpoint.display) is not a URL")
                : .pairing(.notPaired)
            onEvent?(.failed(error, retrying: false))
            return
        }
        teardown(code: .goingAway, reason: "reopening")
        openReporter = reporter
        let generation = self.generation
        var request = URLRequest(url: url)
        request.timeoutInterval = 15
        // The session ticket on the upgrade (docs/dev/ws-contract.md "Auth").
        endpoint.authorize(&request)
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

    /// Closes the socket; nothing from it arrives after this.
    private func teardown(code: URLSessionWebSocketTask.CloseCode = .goingAway, reason: String) {
        generation += 1
        pingLoop?.cancel()
        pingLoop = nil
        handshakeTimer?.cancel()
        handshakeTimer = nil
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
        openReporter?.opened()
        // The handshake first, before anything else on the connection.
        // An older daemon answers it with an "unknown type" error, which
        // the handshake swallows.
        handshake.start()
        send(.clientReady(client: client))
        startHandshakeTimer()
        onEvent?(.handshake(handshake.state))
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
        let before = handshake.state
        let action = handshake.receive(message)
        if handshake.state != before { onEvent?(.handshake(handshake.state)) }
        switch action {
        case .forward:
            break
        case .swallow:
            return
        case .close:
            // A version mismatch does not fix itself: no reconnect loop.
            userClosed = true
            teardown(reason: ProtocolHandshake.closeReason)
            life = .idle
            onEvent?(.closed(reason: ProtocolHandshake.closeReason, retrying: false))
            return
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
        let reporter = openReporter
        teardown(reason: "closed")
        if userClosed {
            life = .idle
            return
        }
        var lost = LinkError(error)
        if let urlError = error as? URLError {
            if urlError.code == .cancelled {
                // Not ours (ours bump the generation first): the system
                // gave up on it, which is a loss like any other.
                lost = .transport
            }
            if !everOpened {
                // Never reached the daemon: say why. The link probes and
                // opens it again once the computer answers.
                onEvent?(.failed(.unreachable(endpoint: endpoint.display, detail: DaemonClient.describe(urlError)), retrying: true))
            }
        }
        lostConnection(reason: everOpened ? "connection lost" : "could not connect", error: lost, reporter: reporter)
    }

    /// The socket dropped, not by the app: wait for the link, which probes
    /// once and says when to open it again.
    private func lostConnection(reason: String, error: LinkError, reporter: LinkChannelReporter?) {
        life = .reconnecting
        onEvent?(.closed(reason: reason, retrying: true))
        reporter?.lost(error == .cancelled ? .transport : error)
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

    private func startHandshakeTimer() {
        handshakeTimer?.cancel()
        let generation = self.generation
        handshakeTimer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(ProtocolHandshake.timeout * 1_000_000_000))
            guard let self, !Task.isCancelled, generation == self.generation else { return }
            self.handshakeTimedOut()
        }
    }

    /// No `bot-ready` within `ProtocolHandshake.timeout`: an older daemon.
    func handshakeTimedOut() {
        let before = handshake.state
        handshake.timedOut()
        if handshake.state != before { onEvent?(.handshake(handshake.state)) }
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
            // The daemon stopped answering: drop the socket and tell the
            // link, which decides when to open it again.
            let reporter = openReporter
            teardown(code: .abnormalClosure, reason: "pong timeout")
            lostConnection(reason: "no answer from the daemon", error: .transport, reporter: reporter)
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
