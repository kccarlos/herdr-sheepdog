import Foundation

/// The app's one `/term` connection, shared by every open agent window
/// (docs/dev/design/phone-terminal.md section 4, "Channels").
///
/// Each window opens its own channel and gets only its own channel's
/// events, plus the connection's state. The connection opens with the
/// first channel and closes `linger` seconds after the last one, so moving
/// from one agent's window to another reuses it. `TermSocket` refuses a
/// channel past the daemon's limit (4); `open` then returns nil.
@MainActor
public final class TermHub {
    /// How long the connection stays up with no channel open.
    public static let linger: TimeInterval = 20

    public let socket: TermSocket
    /// Every `term-ready` (the app keeps `shell` to know whether to offer
    /// the phone shell).
    public var onReady: ((TermSocket.Ready) -> Void)?
    private var handlers: [Int: (TermSocket.Event) -> Void] = [:]
    private var lingerTask: Task<Void, Never>?
    private let linger: TimeInterval

    public init(socket: TermSocket, linger: TimeInterval = TermHub.linger) {
        self.socket = socket
        self.linger = linger
        socket.onEvent = { [weak self] event in self?.route(event) }
    }

    public convenience init(endpoint: DaemonEndpoint, makeTransport: ((URLRequest) -> TermTransport)? = nil) {
        self.init(socket: TermSocket(endpoint: endpoint, makeTransport: makeTransport))
    }

    public var state: TermSocket.State { socket.state }
    /// `term-ready.shell` of the live connection: whether the daemon offers
    /// the phone shell to this app. nil while not connected.
    public var shellOffered: Bool? {
        if case let .ready(ready) = socket.state { return ready.shell }
        return nil
    }
    /// Channels open now, across all windows.
    public var openChannels: [Int] { socket.openChannels }

    public func update(endpoint: DaemonEndpoint) {
        let wasOpen = !handlers.isEmpty
        socket.disconnect()
        handlers.removeAll()
        socket.update(endpoint: endpoint)
        if wasOpen { socket.connect() }
    }

    /// Opens a channel for one window; `handler` gets that channel's events
    /// and the connection's `.state` changes. nil past the channel limit.
    public func open(paneID: String, mode: TermMessage.Mode, cols: Int, rows: Int, takeover: Bool = false,
                     handler: @escaping (TermSocket.Event) -> Void) -> Int? {
        open(target: .pane(paneID), mode: mode, cols: cols, rows: rows, takeover: takeover, handler: handler)
    }

    /// Opens a channel on any target: an agent's pane, or the phone shell
    /// in a folder (`.newShell(cwd:)`, which the daemon reuses per folder).
    public func open(target: TermMessage.Target, mode: TermMessage.Mode, cols: Int, rows: Int, takeover: Bool = false,
                     handler: @escaping (TermSocket.Event) -> Void) -> Int? {
        lingerTask?.cancel()
        lingerTask = nil
        if socket.state == .idle { socket.connect() }
        guard let ch = socket.open(target: target, mode: mode, cols: cols, rows: rows, takeover: takeover) else {
            scheduleLinger()
            return nil
        }
        handlers[ch] = handler
        return ch
    }

    /// Connects without a channel, to learn `term-ready` (whether the shell
    /// is offered) before any window opens; closes after `linger` unless a
    /// window opens meanwhile.
    public func warmUp() {
        guard socket.state == .idle else { return }
        socket.connect()
        scheduleLinger()
    }

    /// Releases the channel; the pane keeps running.
    public func close(ch: Int) {
        handlers.removeValue(forKey: ch)
        socket.close(ch: ch)
        scheduleLinger()
    }

    public func resize(ch: Int, cols: Int, rows: Int) { socket.resize(ch: ch, cols: cols, rows: rows) }

    @discardableResult
    public func send(input: [UInt8], ch: Int) -> Bool { socket.send(input: input, ch: ch) }

    public func scroll(ch: Int, _ dir: TermMessage.ScrollDirection, lines: Int) { socket.scroll(ch: ch, dir, lines: lines) }

    public func click(ch: Int, col: Int, row: Int) {
        socket.mouse(ch: ch, .down, button: "left", col: col, row: row)
        socket.mouse(ch: ch, .up, button: "left", col: col, row: row)
    }

    /// Closes the connection at once (the app went to the background and
    /// every window released its channel).
    public func disconnectIfIdle() {
        guard handlers.isEmpty else { return }
        lingerTask?.cancel()
        lingerTask = nil
        socket.disconnect()
    }

    private func scheduleLinger() {
        guard handlers.isEmpty else { return }
        lingerTask?.cancel()
        let linger = self.linger
        lingerTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(linger * 1_000_000_000))
            guard let self, !Task.isCancelled, self.handlers.isEmpty else { return }
            self.socket.disconnect()
        }
    }

    private func route(_ event: TermSocket.Event) {
        switch event {
        case let .state(state):
            if case let .ready(ready) = state { onReady?(ready) }
            for handler in handlers.values { handler(event) }
        case let .opened(ch, _), let .closed(ch, _, _), let .resync(ch), let .free(ch):
            handlers[ch]?(event)
            if case .closed = event {
                handlers.removeValue(forKey: ch)
                scheduleLinger()
            }
        case let .frame(frame):
            handlers[frame.ch]?(event)
        case let .error(ch, _):
            if let ch {
                handlers[ch]?(event)
            } else {
                for handler in handlers.values { handler(event) }
            }
        }
    }
}
