import Foundation

// The `/term` WebSocket's messages (docs/dev/design/phone-terminal.md
// section 4, "Protocol: the terminal socket"). Text frames are JSON control
// messages with a `type`, the same convention as `/ws`; binary frames carry
// terminal output (daemon → app) and raw input (app → daemon). The daemon's
// side is `internal/termproto`; field names here follow it.

/// One JSON control message on `/term`.
///
/// Client → daemon: `term-hello`, `term-open`, `term-resize`, `term-scroll`,
/// `term-mouse`, `term-close`.
/// Daemon → client: `term-ready`, `term-opened`, `term-closed`, `term-error`.
public struct TermMessage: Codable, Equatable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case hello = "term-hello"
        case open = "term-open"
        case resize = "term-resize"
        case scroll = "term-scroll"
        case mouse = "term-mouse"
        case close = "term-close"
        case ready = "term-ready"
        case opened = "term-opened"
        case closed = "term-closed"
        case error = "term-error"
    }

    /// `control` sends input and sizes the pane; `observe` only watches, at
    /// its own size.
    public enum Mode: String, Codable, Sendable {
        case control, observe
    }

    /// What a channel opens: a tracked agent's pane (or a phone shell the
    /// daemon opened), or a new phone shell (phase 5).
    public struct Target: Codable, Equatable, Sendable {
        public struct Shell: Codable, Equatable, Sendable {
            public var cwd: String?
            public init(cwd: String? = nil) { self.cwd = cwd }
        }

        public var paneID: String?
        public var shell: Shell?

        public static func pane(_ id: String) -> Target { Target(paneID: id) }
        public static func newShell(cwd: String? = nil) -> Target { Target(shell: Shell(cwd: cwd)) }

        public init(paneID: String? = nil, shell: Shell? = nil) {
            self.paneID = paneID
            self.shell = shell
        }

        enum CodingKeys: String, CodingKey {
            case paneID = "pane_id"
            case shell
        }
    }

    /// `term-ready`'s limits.
    public struct Limits: Codable, Equatable, Sendable {
        public var channels: Int
        public init(channels: Int) { self.channels = channels }
    }

    public var type: Kind
    public var `protocol`: Int?
    public var client: WSMessage.ClientInfo?
    public var limits: Limits?
    /// `term-ready`: whether the daemon offers a plain shell to this client.
    public var shell: Bool?
    public var ch: Int?
    public var target: Target?
    public var mode: Mode?
    public var cols: Int?
    public var rows: Int?
    public var takeover: Bool?
    public var paneID: String?
    /// `term-opened`: `agent` or `shell`.
    public var kind: String?
    /// `term-closed`: `released`, `taken_over`, `gone`, `refused:<why>`, `herdr`.
    public var reason: String?
    /// `term-closed` with `refused:busy`: another device controls the pane.
    public var canTakeOver: Bool?
    public var message: String?
    /// `term-scroll`: `up` or `down`.
    public var dir: String?
    public var lines: Int?
    /// `term-mouse`: `down`, `up` or `drag`.
    public var action: String?
    public var button: String?
    public var col: Int?
    public var row: Int?

    public init(type: Kind) {
        self.type = type
    }

    enum CodingKeys: String, CodingKey {
        case type, `protocol`, client, limits, shell, ch, target, mode, cols, rows, takeover
        case paneID = "pane_id"
        case kind, reason
        case canTakeOver = "can_take_over"
        case message, dir, lines, action, button, col, row
    }

    public static func decode(_ data: Data) throws -> TermMessage {
        try JSONDecoder().decode(TermMessage.self, from: data)
    }

    public func encode() throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(self)
    }

    // MARK: Client → daemon

    /// The protocol version this app speaks on `/term`.
    public static let protocolVersion = 1

    public static func hello(client: WSMessage.ClientInfo) -> TermMessage {
        var m = TermMessage(type: .hello)
        m.protocol = protocolVersion
        m.client = client
        return m
    }

    public static func open(ch: Int, target: Target, mode: Mode, cols: Int, rows: Int, takeover: Bool = false) -> TermMessage {
        var m = TermMessage(type: .open)
        m.ch = ch
        m.target = target
        m.mode = mode
        m.cols = cols
        m.rows = rows
        if takeover { m.takeover = true }
        return m
    }

    public static func resize(ch: Int, cols: Int, rows: Int) -> TermMessage {
        var m = TermMessage(type: .resize)
        m.ch = ch
        m.cols = cols
        m.rows = rows
        return m
    }

    public enum ScrollDirection: String, Sendable { case up, down }

    public static func scroll(ch: Int, _ dir: ScrollDirection, lines: Int, col: Int? = nil, row: Int? = nil) -> TermMessage {
        var m = TermMessage(type: .scroll)
        m.ch = ch
        m.dir = dir.rawValue
        m.lines = lines
        m.col = col
        m.row = row
        return m
    }

    public enum MouseAction: String, Sendable { case down, up, drag }

    public static func mouse(ch: Int, _ action: MouseAction, button: String? = nil, col: Int, row: Int) -> TermMessage {
        var m = TermMessage(type: .mouse)
        m.ch = ch
        m.action = action.rawValue
        m.button = button
        m.col = col
        m.row = row
        return m
    }

    public static func close(ch: Int) -> TermMessage {
        var m = TermMessage(type: .close)
        m.ch = ch
        return m
    }
}

/// Why the daemon closed a channel (`term-closed.reason`).
public enum TermCloseReason: Equatable, Sendable {
    /// The app closed it (`term-close`).
    case released
    /// Another device took control.
    case takenOver
    /// The pane closed or is no longer tracked.
    case gone
    /// `refused:<why>`: `busy` (another device controls it; can be taken
    /// over), `too_many`, `shell_off`, `not_tracked`.
    case refused(String)
    /// The herdr bridge failed; the app falls back to `screen`.
    case herdr
    /// A reason this app does not know yet.
    case other(String)

    public init(_ raw: String) {
        switch raw {
        case "released": self = .released
        case "taken_over": self = .takenOver
        case "gone": self = .gone
        case "herdr": self = .herdr
        default:
            if raw.hasPrefix("refused:") {
                self = .refused(String(raw.dropFirst("refused:".count)))
            } else {
                self = .other(raw)
            }
        }
    }
}

/// A binary frame of terminal output, daemon → app. Big-endian:
/// `0x01 | ch u16 | seq u32 | cols u16 | rows u16 | flags u8 | ANSI bytes`,
/// flags bit 0 = full frame. `seq` is herdr's per-bridge sequence.
public struct TermFrame: Equatable, Sendable {
    public static let tag: UInt8 = 0x01
    public static let headerSize = 12

    public var ch: Int
    public var seq: UInt32
    public var cols: Int
    public var rows: Int
    public var full: Bool
    public var bytes: [UInt8]

    public init(ch: Int, seq: UInt32, cols: Int, rows: Int, full: Bool, bytes: [UInt8]) {
        self.ch = ch
        self.seq = seq
        self.cols = cols
        self.rows = rows
        self.full = full
        self.bytes = bytes
    }

    /// nil for anything that is not a well-formed output frame.
    public static func decode(_ data: Data) -> TermFrame? {
        let b = [UInt8](data)
        guard b.count >= headerSize, b[0] == tag else { return nil }
        func u16(_ i: Int) -> Int { Int(b[i]) << 8 | Int(b[i + 1]) }
        let seq = UInt32(b[3]) << 24 | UInt32(b[4]) << 16 | UInt32(b[5]) << 8 | UInt32(b[6])
        return TermFrame(ch: u16(1), seq: seq, cols: u16(7), rows: u16(9),
                         full: b[11] & 1 == 1, bytes: Array(b[headerSize...]))
    }

    /// The wire form (the daemon's side; used by tests and the demo).
    public func encode() -> Data {
        var d = Data([Self.tag, UInt8(ch >> 8 & 0xff), UInt8(ch & 0xff)])
        d.append(contentsOf: [UInt8(seq >> 24 & 0xff), UInt8(seq >> 16 & 0xff), UInt8(seq >> 8 & 0xff), UInt8(seq & 0xff)])
        d.append(contentsOf: [UInt8(cols >> 8 & 0xff), UInt8(cols & 0xff), UInt8(rows >> 8 & 0xff), UInt8(rows & 0xff)])
        d.append(full ? 1 : 0)
        d.append(contentsOf: bytes)
        return d
    }
}

/// Raw terminal input, app → daemon: `0x02 | ch u16 | bytes`. The bytes are
/// what a terminal would send (UTF-8 text, `\r`, `\u{1b}[A`, `\u{3}`); the
/// daemon forwards them without reading them.
public enum TermInput {
    public static let tag: UInt8 = 0x02
    /// The daemon's cap on one input frame.
    public static let maxFrameSize = 64 * 1024

    /// One or more frames, each at most `maxFrameSize` long; a long paste is
    /// split, in order.
    public static func frames(ch: Int, bytes: [UInt8]) -> [Data] {
        let header = [tag, UInt8(ch >> 8 & 0xff), UInt8(ch & 0xff)]
        let chunk = maxFrameSize - header.count
        guard !bytes.isEmpty else { return [] }
        return stride(from: 0, to: bytes.count, by: chunk).map { start in
            Data(header + bytes[start..<min(start + chunk, bytes.count)])
        }
    }

    /// The channel and bytes of an input frame (the daemon's side; tests).
    public static func decode(_ data: Data) -> (ch: Int, bytes: [UInt8])? {
        let b = [UInt8](data)
        guard b.count >= 3, b[0] == tag else { return nil }
        return (Int(b[1]) << 8 | Int(b[2]), Array(b[3...]))
    }
}
