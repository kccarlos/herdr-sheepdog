import Foundation

/// The offline demo's `/term`: a `TermTransport` that answers like the
/// daemon (term-ready, term-opened, full frames at the window's size, a
/// diff a second for a working agent's status line) from `DemoDaemon`'s
/// scripted screens. The agent window runs its real code path on it:
/// `TermSocket`, `TermHub`, the surface, resize, the key bar.
@MainActor
public final class DemoTermTransport: TermTransport {
    /// What `term-open` gets, for screenshots and UI tests of the other
    /// states (`-sheepdogDemoTerm busy|gone|herdr`).
    public enum Behavior: String, Sendable {
        /// Opens; the session plays.
        case normal
        /// Another device controls every pane: `refused:busy` until a
        /// takeover.
        case busy
        /// The pane closed.
        case gone
        /// herdr is not running.
        case herdr
    }

    public var onEvent: ((TermTransportEvent) -> Void)?
    private let behavior: Behavior
    private let screen: (String) -> AgentScreen?
    private var channels: [Int: Channel] = [:]
    private var ticker: Task<Void, Never>?
    private var closed = false

    private struct Channel {
        var paneID: String
        var cols: Int
        var rows: Int
        var mode: TermMessage.Mode
        var seq: UInt32 = 0
        var typed = ""
        var lastBody: [String] = []
    }

    /// `screen` reads an agent's scripted screen (`DemoDaemon.agentScreen`).
    public init(behavior: Behavior = .normal, screen: @escaping (String) -> AgentScreen?) {
        self.behavior = behavior
        self.screen = screen
    }

    public func start() {
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                self?.tick()
            }
        }
    }

    public func send(text: String) {
        guard !closed, let message = try? TermMessage.decode(Data(text.utf8)) else { return }
        switch message.type {
        case .hello:
            reply(#"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":false}"#)
        case .open:
            guard let ch = message.ch, let pane = message.target?.paneID else { return }
            let mode = message.mode ?? .control
            switch behavior {
            case .gone:
                reply(#"{"type":"term-closed","ch":\#(ch),"reason":"gone"}"#)
                return
            case .herdr:
                reply(#"{"type":"term-closed","ch":\#(ch),"reason":"herdr"}"#)
                return
            case .busy where mode == .control && message.takeover != true:
                reply(#"{"type":"term-closed","ch":\#(ch),"reason":"refused:busy","can_take_over":true}"#)
                return
            default:
                break
            }
            guard screen(pane) != nil else {
                reply(#"{"type":"term-closed","ch":\#(ch),"reason":"refused:not_tracked"}"#)
                return
            }
            let c = Channel(paneID: pane, cols: message.cols ?? 58, rows: message.rows ?? 30, mode: mode)
            channels[ch] = c
            reply(#"{"type":"term-opened","ch":\#(ch),"pane_id":"\#(pane)","mode":"\#(mode.rawValue)","cols":\#(c.cols),"rows":\#(c.rows),"kind":"agent"}"#)
            sendFull(ch)
        case .resize:
            guard let ch = message.ch, channels[ch] != nil else { return }
            channels[ch]?.cols = message.cols ?? channels[ch]!.cols
            channels[ch]?.rows = message.rows ?? channels[ch]!.rows
            sendFull(ch)
        case .close:
            if let ch = message.ch { channels[ch] = nil }
        default:
            break
        }
    }

    public func send(data: Data) {
        guard let (ch, bytes) = TermInput.decode(data), var c = channels[ch], c.mode == .control else { return }
        for byte in bytes {
            switch byte {
            case 0x0d: c.typed = ""
            case 0x7f, 0x08: if !c.typed.isEmpty { c.typed.removeLast() }
            case 0x03, 0x1b: c.typed = ""
            case 0x20 ..< 0x7f: c.typed.append(Character(UnicodeScalar(byte)))
            default: break
            }
        }
        channels[ch] = c
        sendFull(ch)
    }

    public func ping(_ completion: @escaping (Bool) -> Void) { completion(!closed) }

    public func close() {
        closed = true
        ticker?.cancel()
        channels.removeAll()
    }

    // MARK: - Frames

    private func reply(_ json: String) {
        let onEvent = self.onEvent
        DispatchQueue.main.async { onEvent?(.text(json)) }
    }

    private func sendFull(_ ch: Int) {
        guard var c = channels[ch], let agent = screen(c.paneID) else { return }
        let body = DemoTerminalScreen.lines(for: agent, cols: c.cols, rows: c.rows, typed: c.typed, tick: Self.tickNow)
        c.lastBody = body
        c.seq &+= 1
        channels[ch] = c
        var bytes = Array("\u{1b}[?2026h\u{1b}[?25l\u{1b}[0m\u{1b}[2J".utf8)
        for (i, line) in body.enumerated() {
            bytes += Array("\u{1b}[\(i + 1);1H".utf8) + Array(line.utf8)
        }
        bytes += Array("\u{1b}[0m\u{1b}[?2026l".utf8)
        emit(TermFrame(ch: ch, seq: c.seq, cols: c.cols, rows: c.rows, full: true, bytes: bytes))
    }

    private static var tickNow: Int { Int(Date().timeIntervalSince1970) }

    /// A diff with the rows that changed (the spinner), or a full frame
    /// when the script moved on.
    private func tick() {
        for ch in channels.keys.sorted() {
            guard var c = channels[ch], let agent = screen(c.paneID) else { continue }
            let body = DemoTerminalScreen.lines(for: agent, cols: c.cols, rows: c.rows, typed: c.typed, tick: Self.tickNow)
            let changed = body.indices.filter { $0 >= c.lastBody.count || body[$0] != c.lastBody[$0] }
            guard !changed.isEmpty else { continue }
            if changed.count > 2 || body.count != c.lastBody.count {
                sendFull(ch)
                continue
            }
            c.lastBody = body
            c.seq &+= 1
            channels[ch] = c
            var bytes = Array("\u{1b}[?2026h".utf8)
            for i in changed {
                bytes += Array("\u{1b}[\(i + 1);1H\u{1b}[0m\u{1b}[2K".utf8) + Array(body[i].utf8)
            }
            bytes += Array("\u{1b}[0m\u{1b}[?2026l".utf8)
            emit(TermFrame(ch: ch, seq: c.seq, cols: c.cols, rows: c.rows, full: false, bytes: bytes))
        }
    }

    private func emit(_ frame: TermFrame) {
        let onEvent = self.onEvent
        let data = frame.encode()
        DispatchQueue.main.async { onEvent?(.data(data)) }
    }
}

/// A coding agent's screen as ANSI rows, drawn from the demo's scripted
/// screen text: the task, the work, a status line with a spinner while
/// working, and a prompt box at the bottom. Rows fit `cols` (long lines
/// wrap at spaces) and there are at most `rows` of them.
public enum DemoTerminalScreen {
    static let reset = "\u{1b}[0m"
    static let dim = "\u{1b}[2m"
    static let bold = "\u{1b}[1m"
    static func fg(_ n: Int) -> String { "\u{1b}[\(30 + n)m" }

    public static func lines(for screen: AgentScreen, cols: Int, rows: Int, typed: String, tick: Int) -> [String] {
        let width = max(10, cols)
        var out: [String] = []
        let text = screen.text ?? ""
        for raw in text.components(separatedBy: "\n") where !raw.hasPrefix("(Demo:") {
            for piece in wrap(raw, width: width) {
                out.append(style(piece))
            }
        }
        while out.last == "" { out.removeLast() }

        var footer: [String] = []
        if screen.state == .working {
            let spinner = ["✻", "✶", "✳", "✢", "·", "✢", "✳", "✶"][tick % 8]
            let seconds = 12 + tick % 50
            footer.append("")
            footer.append(fg(3) + spinner + " Working… " + reset + dim + "(\(seconds)s · esc to interrupt)" + reset)
        }
        footer.append("")
        let inner = max(4, width - 2)
        footer.append(dim + "╭" + String(repeating: "─", count: inner) + "╮" + reset)
        let shown = String(("> " + typed).suffix(inner - 1))
        footer.append(dim + "│" + reset + shown + String(repeating: " ", count: max(0, inner - shown.count)) + dim + "│" + reset)
        footer.append(dim + "╰" + String(repeating: "─", count: inner) + "╯" + reset)
        footer.append(dim + "  demo · a scripted session" + reset)

        let room = max(0, rows - footer.count)
        if out.count > room { out = Array(out.suffix(room)) }
        // Keep the prompt box at the bottom, like the real thing.
        let gap = max(0, rows - out.count - footer.count)
        return Array((out + Array(repeating: "", count: gap) + footer).prefix(max(1, rows)))
    }

    static func style(_ line: String) -> String {
        if line.hasPrefix("> ") { return bold + line + reset }
        if line.hasPrefix("● ") { return fg(2) + "●" + reset + String(line.dropFirst()) }
        if line.hasPrefix("$ ") || line.hasPrefix("  $ ") { return fg(6) + line + reset }
        if line.hasPrefix("ERROR") { return fg(1) + line + reset }
        if line.hasPrefix("› ") { return fg(3) + bold + line + reset }
        if line.hasPrefix("Would you like") { return bold + line + reset }
        return line
    }

    /// Wraps at spaces to `width` columns (an unbroken run is cut).
    static func wrap(_ line: String, width: Int) -> [String] {
        guard line.count > width else { return [line] }
        var result: [String] = []
        var current = ""
        for word in line.split(separator: " ", omittingEmptySubsequences: false) {
            let candidate = current.isEmpty ? String(word) : current + " " + word
            if candidate.count <= width {
                current = candidate
            } else {
                if !current.isEmpty { result.append(current) }
                var rest = String(word)
                while rest.count > width {
                    result.append(String(rest.prefix(width)))
                    rest = String(rest.dropFirst(width))
                }
                current = rest
            }
        }
        if !current.isEmpty { result.append(current) }
        return result
    }
}
