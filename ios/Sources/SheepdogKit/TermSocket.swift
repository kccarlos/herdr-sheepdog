import Foundation

/// The `/term` connection: terminal windows on herdr panes, several per
/// connection (docs/dev/design/phone-terminal.md section 4).
///
/// - Same address and token as `/ws` (`Authorization: Bearer` on the
///   upgrade), path `/term`.
/// - `term-hello` goes first on every connection; the connection is ready
///   on `term-ready`, and channels opened before that wait for it.
/// - Each channel is one window on one pane. Frames are diffs, so a missed
///   one corrupts the screen: on a `seq` gap, or a diff before any full
///   frame, the channel drops diffs and asks for a repaint (a `term-resize`
///   to its current size) until a full frame arrives.
/// - `resize(ch:cols:rows:)` is debounced (150 ms); a resize to the same
///   size is sent too, since it asks for a repaint.
/// - A dropped connection reconnects with backoff (1 s doubling to 10 s)
///   and reopens every channel the daemon had not closed.
/// - A WebSocket ping goes every 20 s; one still unanswered at the next
///   tick drops the connection, which then reconnects.
///
/// Use it only when the daemon's `bot-ready` lists the `term` feature.
@MainActor
public final class TermSocket {
    /// `term-ready`.
    public struct Ready: Equatable, Sendable {
        public var protocolVersion: Int
        /// Channels this connection may have open at once.
        public var channels: Int
        /// Whether the daemon offers a plain shell to this client.
        public var shell: Bool
    }

    /// `term-opened`.
    public struct Opened: Equatable, Sendable {
        public var paneID: String
        public var mode: TermMessage.Mode
        public var cols: Int
        public var rows: Int
        /// `agent` or `shell`.
        public var kind: String
    }

    public enum State: Equatable, Sendable {
        case idle
        case connecting
        case ready(Ready)
        /// The connection dropped or could not be made; trying again.
        case reconnecting
    }

    public enum Event: Sendable {
        case state(State)
        case opened(ch: Int, Opened)
        /// A frame to draw, in order, gap-free since the last full frame.
        case frame(TermFrame)
        /// The daemon closed the channel; it will not be reopened.
        case closed(ch: Int, reason: TermCloseReason, canTakeOver: Bool)
        /// A problem that does not end the channel (`ch` nil: the connection).
        case error(ch: Int?, message: String)
        /// The channel lost a frame and asked for a repaint.
        case resync(ch: Int)
    }

    public static let resizeDebounce: TimeInterval = 0.15
    public static let retryMinimum: TimeInterval = 1
    public static let retryMaximum: TimeInterval = 10
    public static let pingInterval: TimeInterval = 20
    /// No `term-ready` this long after connecting: try again.
    public static let readyTimeout: TimeInterval = 10
    /// A repaint asked for and not answered this long: ask again.
    public static let resyncRetry: TimeInterval = 1
    /// `term-ready.limits.channels` until the daemon says.
    public static let defaultChannelLimit = 4

    public var onEvent: ((Event) -> Void)?
    /// Who the app says it is on `term-hello`.
    public var client = WSMessage.ClientInfo.app()

    public private(set) var state: State = .idle {
        didSet { if state != oldValue { onEvent?(.state(state)) } }
    }

    private struct Channel {
        var target: TermMessage.Target
        var mode: TermMessage.Mode
        var takeover: Bool
        var cols: Int
        var rows: Int
        /// Sent `term-open` on the current connection.
        var sentOpen = false
        var lastSeq: UInt32?
        /// Dropping diffs until a full frame arrives.
        var awaitingFull = false
        var resyncSentAt: Date?
    }

    private var endpoint: DaemonEndpoint
    private let makeTransport: (URLRequest) -> TermTransport
    private var transport: TermTransport?
    private var generation = 0
    private var userClosed = true
    private var retryDelay = TermSocket.retryMinimum
    private var retryTask: Task<Void, Never>?
    private var readyTimer: Task<Void, Never>?
    private var pingLoop: Task<Void, Never>?
    private var pingOutstanding = false
    private var channels: [Int: Channel] = [:]
    private var resizeTimers: [Int: Task<Void, Never>] = [:]
    private var nextChannel = 1
    private let now: () -> Date

    /// `makeTransport` is for tests; the app uses URLSession.
    public init(endpoint: DaemonEndpoint,
                makeTransport: ((URLRequest) -> TermTransport)? = nil,
                now: @escaping () -> Date = Date.init) {
        self.endpoint = endpoint
        self.now = now
        if let makeTransport {
            self.makeTransport = makeTransport
        } else {
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 60
            let session = URLSession(configuration: config)
            self.makeTransport = { URLSessionTermTransport(request: $0, session: session) }
        }
    }

    /// `ws(s)://host:port/term`.
    public static func url(for endpoint: DaemonEndpoint) -> URL? {
        guard let ws = endpoint.webSocketURL,
              var components = URLComponents(url: ws, resolvingAgainstBaseURL: false) else { return nil }
        components.path = "/term"
        return components.url
    }

    public func update(endpoint: DaemonEndpoint) {
        self.endpoint = endpoint
    }

    // MARK: - Connection

    public func connect() {
        userClosed = false
        retryDelay = Self.retryMinimum
        start()
    }

    /// Closes the connection and forgets every channel. The daemon releases
    /// the bridges when the socket goes.
    public func disconnect() {
        userClosed = true
        teardown()
        channels.removeAll()
        state = .idle
    }

    /// The channels open (or opening) now.
    public var openChannels: [Int] { channels.keys.sorted() }

    /// The channel limit: the daemon's once ready, 4 before.
    public var channelLimit: Int {
        if case let .ready(ready) = state { return ready.channels }
        return Self.defaultChannelLimit
    }

    private func start() {
        teardown()
        guard let url = Self.url(for: endpoint) else {
            onEvent?(.error(ch: nil, message: "the address \(endpoint.display) is not a URL"))
            userClosed = true
            state = .idle
            return
        }
        generation += 1
        let generation = self.generation
        var request = URLRequest(url: url)
        request.timeoutInterval = 15
        if !endpoint.token.isEmpty {
            request.setValue("Bearer \(endpoint.token)", forHTTPHeaderField: "Authorization")
        }
        let transport = makeTransport(request)
        self.transport = transport
        for key in channels.keys {
            channels[key]?.sentOpen = false
            channels[key]?.lastSeq = nil
            channels[key]?.awaitingFull = false
            channels[key]?.resyncSentAt = nil
        }
        state = .connecting
        transport.onEvent = { [weak self] event in
            guard let self, generation == self.generation else { return }
            self.handle(event)
        }
        transport.start()
        sendJSON(.hello(client: client))
        readyTimer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Self.readyTimeout * 1_000_000_000))
            guard let self, !Task.isCancelled, generation == self.generation else { return }
            if case .ready = self.state { return }
            self.connectionLost()
        }
    }

    private func teardown() {
        retryTask?.cancel()
        retryTask = nil
        readyTimer?.cancel()
        readyTimer = nil
        pingLoop?.cancel()
        pingLoop = nil
        pingOutstanding = false
        for timer in resizeTimers.values { timer.cancel() }
        resizeTimers.removeAll()
        generation += 1
        transport?.close()
        transport = nil
    }

    private func connectionLost() {
        teardown()
        guard !userClosed else {
            state = .idle
            return
        }
        state = .reconnecting
        let delay = retryDelay
        retryDelay = min(retryDelay * 2, Self.retryMaximum)
        retryTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled, !self.userClosed else { return }
            self.start()
        }
    }

    /// The delay before the next reconnect attempt (tests).
    var nextRetryDelay: TimeInterval { retryDelay }

    // MARK: - Channels

    /// Opens a window on a pane. Returns the channel id, or nil when this
    /// connection already has `channelLimit` channels. Before the connection
    /// is ready the open waits and goes out on `term-ready`.
    @discardableResult
    public func open(target: TermMessage.Target, mode: TermMessage.Mode, cols: Int, rows: Int, takeover: Bool = false) -> Int? {
        guard channels.count < channelLimit else { return nil }
        var ch = nextChannel
        while channels[ch] != nil { ch = ch % 65535 + 1 }
        nextChannel = ch % 65535 + 1
        channels[ch] = Channel(target: target, mode: mode, takeover: takeover, cols: cols, rows: rows)
        if case .ready = state { sendOpen(ch) }
        return ch
    }

    /// Releases the window; the pane keeps running.
    public func close(ch: Int) {
        guard channels.removeValue(forKey: ch) != nil else { return }
        resizeTimers.removeValue(forKey: ch)?.cancel()
        if case .ready = state { sendJSON(.close(ch: ch)) }
    }

    /// The window's grid changed. Sent after 150 ms without another change;
    /// the same size again asks for a repaint.
    public func resize(ch: Int, cols: Int, rows: Int) {
        guard channels[ch] != nil else { return }
        channels[ch]?.cols = cols
        channels[ch]?.rows = rows
        resizeTimers[ch]?.cancel()
        let generation = self.generation
        resizeTimers[ch] = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Self.resizeDebounce * 1_000_000_000))
            guard let self, !Task.isCancelled, generation == self.generation else { return }
            self.resizeTimers[ch] = nil
            self.sendResize(ch)
        }
    }

    /// Raw input bytes for a `control` channel (split into 64 KB frames).
    @discardableResult
    public func send(input: [UInt8], ch: Int) -> Bool {
        guard case .ready = state, let channel = channels[ch], channel.sentOpen, let transport else { return false }
        for frame in TermInput.frames(ch: ch, bytes: input) { transport.send(data: frame) }
        return true
    }

    public func scroll(ch: Int, _ dir: TermMessage.ScrollDirection, lines: Int, col: Int? = nil, row: Int? = nil) {
        guard channels[ch]?.sentOpen == true else { return }
        sendJSON(.scroll(ch: ch, dir, lines: lines, col: col, row: row))
    }

    public func mouse(ch: Int, _ action: TermMessage.MouseAction, button: String? = nil, col: Int, row: Int) {
        guard channels[ch]?.sentOpen == true else { return }
        sendJSON(.mouse(ch: ch, action, button: button, col: col, row: row))
    }

    private func sendOpen(_ ch: Int) {
        guard let c = channels[ch] else { return }
        channels[ch]?.sentOpen = true
        sendJSON(.open(ch: ch, target: c.target, mode: c.mode, cols: c.cols, rows: c.rows, takeover: c.takeover))
        // A takeover is a one-time decision; a reopen after a drop asks again.
        channels[ch]?.takeover = false
    }

    private func sendResize(_ ch: Int) {
        guard let c = channels[ch], c.sentOpen else { return }
        sendJSON(.resize(ch: ch, cols: c.cols, rows: c.rows))
    }

    @discardableResult
    private func sendJSON(_ message: TermMessage) -> Bool {
        guard let transport, let data = try? message.encode() else { return false }
        transport.send(text: String(decoding: data, as: UTF8.self))
        return true
    }

    // MARK: - Receiving

    private func handle(_ event: TermTransportEvent) {
        switch event {
        case let .text(text):
            guard let message = try? TermMessage.decode(Data(text.utf8)) else { return }
            handle(message)
        case let .data(data):
            guard let frame = TermFrame.decode(data) else { return }
            receive(frame)
        case .closed:
            connectionLost()
        }
    }

    private func handle(_ message: TermMessage) {
        switch message.type {
        case .ready:
            readyTimer?.cancel()
            readyTimer = nil
            retryDelay = Self.retryMinimum
            state = .ready(Ready(protocolVersion: message.protocol ?? 1,
                                 channels: message.limits?.channels ?? Self.defaultChannelLimit,
                                 shell: message.shell ?? false))
            startPinging()
            for ch in channels.keys.sorted() where channels[ch]?.sentOpen == false {
                sendOpen(ch)
            }
        case .opened:
            guard let ch = message.ch, channels[ch] != nil else { return }
            channels[ch]?.lastSeq = nil
            channels[ch]?.awaitingFull = false
            onEvent?(.opened(ch: ch, Opened(paneID: message.paneID ?? "",
                                            mode: message.mode ?? .control,
                                            cols: message.cols ?? 0,
                                            rows: message.rows ?? 0,
                                            kind: message.kind ?? "agent")))
        case .closed:
            guard let ch = message.ch, channels.removeValue(forKey: ch) != nil else { return }
            resizeTimers.removeValue(forKey: ch)?.cancel()
            onEvent?(.closed(ch: ch, reason: TermCloseReason(message.reason ?? ""),
                             canTakeOver: message.canTakeOver ?? false))
        case .error:
            onEvent?(.error(ch: message.ch, message: message.message ?? "terminal error"))
        default:
            break
        }
    }

    private func receive(_ frame: TermFrame) {
        guard var c = channels[frame.ch] else { return }
        defer { channels[frame.ch] = c }
        let inOrder = c.lastSeq.map { frame.seq == $0 &+ 1 } ?? false
        c.lastSeq = frame.seq
        if frame.full {
            c.awaitingFull = false
            c.resyncSentAt = nil
            onEvent?(.frame(frame))
            return
        }
        if inOrder && !c.awaitingFull {
            onEvent?(.frame(frame))
            return
        }
        // A gap, or a diff with no full frame under it: drop it and ask
        // for a repaint (again, if the last ask went unanswered).
        let t = now()
        if !c.awaitingFull || c.resyncSentAt.map({ t.timeIntervalSince($0) >= Self.resyncRetry }) ?? true {
            c.awaitingFull = true
            c.resyncSentAt = t
            resizeTimers.removeValue(forKey: frame.ch)?.cancel()
            sendJSON(.resize(ch: frame.ch, cols: c.cols, rows: c.rows))
            onEvent?(.resync(ch: frame.ch))
        }
    }

    // MARK: - Liveness

    private func startPinging() {
        pingLoop?.cancel()
        pingOutstanding = false
        let generation = self.generation
        pingLoop = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(Self.pingInterval * 1_000_000_000))
                guard let self, !Task.isCancelled, generation == self.generation else { return }
                self.pingTick()
            }
        }
    }

    /// One ping period: a ping still unanswered drops the connection.
    func pingTick() {
        guard let transport else { return }
        if pingOutstanding {
            connectionLost()
            return
        }
        pingOutstanding = true
        let generation = self.generation
        transport.ping { [weak self] ok in
            guard let self, generation == self.generation else { return }
            if ok { self.pingOutstanding = false } else { self.connectionLost() }
        }
    }
}

// MARK: - Transport

public enum TermTransportEvent: Sendable {
    case text(String)
    case data(Data)
    /// The connection ended (or never opened).
    case closed(String?)
}

/// The WebSocket under a `TermSocket`; a fake in tests.
@MainActor
public protocol TermTransport: AnyObject {
    var onEvent: ((TermTransportEvent) -> Void)? { get set }
    func start()
    func send(text: String)
    func send(data: Data)
    /// A WebSocket ping; `completion(true)` on its pong.
    func ping(_ completion: @escaping (Bool) -> Void)
    func close()
}

@MainActor
final class URLSessionTermTransport: TermTransport {
    var onEvent: ((TermTransportEvent) -> Void)?
    private let task: URLSessionWebSocketTask
    private var loop: Task<Void, Never>?
    private var closed = false

    init(request: URLRequest, session: URLSession) {
        task = session.webSocketTask(with: request)
        // A full frame is a few KB to a few tens of KB (it scales with the
        // screen, not the output rate).
        task.maximumMessageSize = 4 * 1024 * 1024
    }

    func start() {
        task.resume()
        loop = Task { [weak self] in
            guard let task = self?.task else { return }
            while !Task.isCancelled {
                do {
                    let message = try await task.receive()
                    guard let self, !self.closed else { return }
                    switch message {
                    case let .string(text): self.onEvent?(.text(text))
                    case let .data(data): self.onEvent?(.data(data))
                    @unknown default: break
                    }
                } catch {
                    guard let self, !self.closed else { return }
                    self.closed = true
                    self.onEvent?(.closed(error.localizedDescription))
                    return
                }
            }
        }
    }

    func send(text: String) {
        task.send(.string(text)) { _ in }
    }

    func send(data: Data) {
        task.send(.data(data)) { _ in }
    }

    func ping(_ completion: @escaping (Bool) -> Void) {
        task.sendPing { error in
            Task { @MainActor in completion(error == nil) }
        }
    }

    func close() {
        closed = true
        loop?.cancel()
        task.cancel(with: .goingAway, reason: nil)
    }
}
