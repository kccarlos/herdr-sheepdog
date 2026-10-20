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
        lingerTask?.cancel()
        lingerTask = nil
        if socket.state == .idle { socket.connect() }
        guard let ch = socket.open(target: .pane(paneID), mode: mode, cols: cols, rows: rows, takeover: takeover) else {
            scheduleLinger()
            return nil
        }
        handlers[ch] = handler
        return ch
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
        case .state:
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
