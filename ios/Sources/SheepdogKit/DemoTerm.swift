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
        /// The daemon offers no phone shell (`term-ready.shell` false).
        case noShell
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
        /// A phone shell's folder; nil for an agent's window.
        var shellCwd: String?
        /// A shell's earlier lines (commands and the demo's answer).
        var history: [String] = []
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
            reply(#"{"type":"term-ready","protocol":1,"limits":{"channels":4},"shell":\#(behavior != .noShell)}"#)
        case .open:
            guard let ch = message.ch else { return }
            if let shell = message.target?.shell {
                openShell(ch, cwd: shell.cwd ?? "~/Documents", message: message)
                return
            }
            guard let pane = message.target?.paneID else { return }
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
            case 0x0d where c.shellCwd != nil:
                c.history += [Self.shellPrompt(c.shellCwd!) + c.typed]
                if !c.typed.isEmpty { c.history += Self.shellAnswer(c.typed) }
                c.typed = ""
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

    // MARK: - The phone shell

    private var shells = 0

    private func openShell(_ ch: Int, cwd: String, message: TermMessage) {
        guard behavior != .noShell else {
            reply(#"{"type":"term-closed","ch":\#(ch),"reason":"refused:shell_off"}"#)
            return
        }
        shells += 1
        let pane = "demo-shell-\(shells)"
        var c = Channel(paneID: pane, cols: message.cols ?? 58, rows: message.rows ?? 30, mode: .control)
        c.shellCwd = cwd
        c.history = ["Last login on the phone: demo shell in \(cwd)"]
        channels[ch] = c
        reply(#"{"type":"term-opened","ch":\#(ch),"pane_id":"\#(pane)","mode":"control","cols":\#(c.cols),"rows":\#(c.rows),"kind":"shell"}"#)
        sendFull(ch)
    }

    /// What the demo's shell prints for a command: a few scripted ones
    /// about the sample project, and a plain note for anything else.
    static func shellAnswer(_ command: String) -> [String] {
        let yellow = "\u{1b}[33m", green = "\u{1b}[32m", red = "\u{1b}[31m", reset = "\u{1b}[0m"
        switch command.split(separator: " ", omittingEmptySubsequences: true).joined(separator: " ") {
        case "git log --oneline -3", "git log --oneline":
            return ["\(yellow)4f2a9c1\(reset) Retry the payment webhook with backoff",
                    "\(yellow)9b13e70\(reset) Fix the login redirect loop",
                    "\(yellow)2d6f0b8\(reset) Add the users.plan migration"]
        case "git status":
            return ["On branch main", "Your branch is up to date with 'origin/main'.", "",
                    "Changes not staged for commit:", "  \(red)modified:   README.md\(reset)"]
        case "make test":
            return ["go test ./...", "ok   shop/internal/auth  0.42s", "ok   shop/internal/web   0.61s", "\(green)PASS\(reset)"]
        case "ls":
            return ["README.md  go.mod  internal  migrations  package.json  src  test"]
        case "pwd":
            return ["/Users/demo/code/shop"]
        default:
            return ["demo: only git log --oneline -3, git status, make test, ls and pwd answer here; on your computer this is a real shell."]
        }
    }

    static func shellPrompt(_ cwd: String) -> String {
        "\u{1b}[36m" + ((cwd as NSString).lastPathComponent) + "\u{1b}[0m % "
    }

    private func shellLines(_ c: Channel) -> [String] {
        let lines = c.history.flatMap { Self.wrap($0, cols: c.cols) } + [Self.shellPrompt(c.shellCwd ?? "~") + c.typed]
        return Array(lines.suffix(max(1, c.rows)))
    }

    /// A plain line cut at the window's width, so a long one does not run
    /// into the next row (lines with colour codes are short and kept whole).
    static func wrap(_ line: String, cols: Int) -> [String] {
        guard cols > 0, line.count > cols, !line.contains("\u{1b}") else { return [line] }
        var out: [String] = []
        var rest = Substring(line)
        while rest.count > cols {
            // At the last space that fits, or mid-word when there is none.
            let head = rest.prefix(cols + 1)
            let cut = head.lastIndex(of: " ").map { rest.distance(from: rest.startIndex, to: $0) } ?? 0
            let n = cut > 0 ? cut : cols
            out.append(String(rest.prefix(n)).trimmingCharacters(in: .whitespaces))
            rest = rest.dropFirst(n).drop { $0 == " " }
        }
        out.append(String(rest))
        return out
    }

    // MARK: - Frames

    private func reply(_ json: String) {
        let onEvent = self.onEvent
        DispatchQueue.main.async { onEvent?(.text(json)) }
    }

    private func sendFull(_ ch: Int) {
        guard var c = channels[ch] else { return }
        let body: [String]
        if c.shellCwd != nil {
            body = shellLines(c)
        } else if let agent = screen(c.paneID) {
            body = DemoTerminalScreen.lines(for: agent, cols: c.cols, rows: c.rows, typed: c.typed, tick: Self.tickNow)
        } else {
            return
        }
        c.lastBody = body
        c.seq &+= 1
        channels[ch] = c
        var bytes = Array("\u{1b}[?2026h\u{1b}[?25l\u{1b}[0m\u{1b}[2J".utf8)
        for (i, line) in body.enumerated() {
            bytes += Array("\u{1b}[\(i + 1);1H".utf8) + Array(line.utf8)
        }
        if c.shellCwd != nil {
            // The cursor after the prompt, like a real shell.
            let last = body.count
            let column = (body.last.map { $0.replacingOccurrences(of: "\u{1b}[36m", with: "").replacingOccurrences(of: "\u{1b}[0m", with: "").count } ?? 0) + 1
            bytes += Array("\u{1b}[0m\u{1b}[\(last);\(column)H\u{1b}[?25h\u{1b}[?2026l".utf8)
        } else {
            bytes += Array("\u{1b}[0m\u{1b}[?2026l".utf8)
        }
        emit(TermFrame(ch: ch, seq: c.seq, cols: c.cols, rows: c.rows, full: true, bytes: bytes))
    }

    private static var tickNow: Int { Int(Date().timeIntervalSince1970) }

    /// A diff with the rows that changed (the spinner), or a full frame
    /// when the script moved on.
    private func tick() {
        for ch in channels.keys.sorted() {
            guard var c = channels[ch], c.shellCwd == nil, let agent = screen(c.paneID) else { continue }
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
