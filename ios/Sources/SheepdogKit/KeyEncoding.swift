import Foundation

// The bytes a terminal sends for each key (docs/dev/term-contract.md
// "Binary frames": the daemon forwards them unchanged), the key bar's
// one-shot Ctrl and Alt, and the snippets the more-keys sheet keeps.
// `KeyEncodingTests` pins every sequence.

/// A key the key bar or the more-keys sheet sends.
public enum TermKey: Hashable, Sendable, Identifiable {
    case esc, tab, shiftTab, enter, backspace
    case up, down, left, right
    case home, end, pageUp, pageDown
    /// F1–F12.
    case function(Int)
    /// A control chord, `ctrl(“c”)` = ⌃C.
    case ctrl(Character)
    /// Plain text (y, n, a digit).
    case text(String)

    public var id: String { label }

    /// xterm's sequences (normal cursor-key mode: herdr's renderer
    /// resolved the app's modes already).
    public var bytes: [UInt8] {
        switch self {
        case .esc: return [0x1b]
        case .tab: return [0x09]
        case .shiftTab: return Array("\u{1b}[Z".utf8)
        case .enter: return [0x0d]
        case .backspace: return [0x7f]
        case .up: return Array("\u{1b}[A".utf8)
        case .down: return Array("\u{1b}[B".utf8)
        case .right: return Array("\u{1b}[C".utf8)
        case .left: return Array("\u{1b}[D".utf8)
        case .home: return Array("\u{1b}[H".utf8)
        case .end: return Array("\u{1b}[F".utf8)
        case .pageUp: return Array("\u{1b}[5~".utf8)
        case .pageDown: return Array("\u{1b}[6~".utf8)
        case let .function(n):
            switch n {
            case 1 ... 4: return [0x1b, 0x4f, UInt8(0x50 + n - 1)]
            case 5 ... 12:
                let codes = [5: 15, 6: 17, 7: 18, 8: 19, 9: 20, 10: 21, 11: 23, 12: 24]
                return Array("\u{1b}[\(codes[n]!)~".utf8)
            default: return []
            }
        case let .ctrl(c): return KeyEncoding.control(c).map { [$0] } ?? []
        case let .text(s): return Array(s.utf8)
        }
    }

    /// The button's glyph.
    public var label: String {
        switch self {
        case .esc: return "esc"
        case .tab: return "⇥"
        case .shiftTab: return "⇧⇥"
        case .enter: return "⏎"
        case .backspace: return "⌫"
        case .up: return "↑"
        case .down: return "↓"
        case .left: return "←"
        case .right: return "→"
        case .home: return "Home"
        case .end: return "End"
        case .pageUp: return "PgUp"
        case .pageDown: return "PgDn"
        case let .function(n): return "F\(n)"
        case let .ctrl(c): return "⌃" + String(c).uppercased()
        case let .text(s): return s
        }
    }

    /// What VoiceOver says.
    public var accessibilityLabel: String {
        switch self {
        case .esc: return "Escape"
        case .tab: return "Tab"
        case .shiftTab: return "Shift Tab"
        case .enter: return "Return"
        case .backspace: return "Delete"
        case .up: return "Up arrow"
        case .down: return "Down arrow"
        case .left: return "Left arrow"
        case .right: return "Right arrow"
        case .home: return "Home"
        case .end: return "End"
        case .pageUp: return "Page up"
        case .pageDown: return "Page down"
        case let .function(n): return "F\(n)"
        case let .ctrl(c): return "Control " + String(c).uppercased()
        case let .text(s): return s
        }
    }

    /// The daemon's allow-listed name (`POST /input {keys}`), for the
    /// snapshot window, which has no raw channel; nil when the key is
    /// live-only.
    public var allowListed: AgentKey? {
        switch self {
        case .esc: return .esc
        case .tab: return .tab
        case .enter: return .enter
        case .up: return .up
        case .down: return .down
        case .ctrl("c"): return .ctrlC
        case let .text(s): return AgentKey(rawValue: s)
        default: return nil
        }
    }

    /// Ctrl-C can end the agent's process: the window asks first (outside
    /// typing mode). Esc goes at once, so Esc Esc (rewind) works.
    public var needsConfirmation: Bool { self == .ctrl("c") }
}

/// One-shot modifiers: the next key or typed character gets them, then
/// they clear.
public struct KeyModifiers: Equatable, Sendable {
    public var ctrl = false
    public var alt = false

    public init(ctrl: Bool = false, alt: Bool = false) {
        self.ctrl = ctrl
        self.alt = alt
    }

    public var isEmpty: Bool { !ctrl && !alt }

    /// Applies the latched modifiers to `bytes` (a key or typed text) and
    /// clears them. Ctrl turns a single character into its control code
    /// (⌃ + letter, `@ [ \ ] ^ _ ?` and space); Alt prefixes ESC.
    public mutating func apply(to bytes: [UInt8]) -> [UInt8] {
        defer { self = KeyModifiers() }
        var out = bytes
        if ctrl, out.count == 1, let code = KeyEncoding.control(Character(UnicodeScalar(out[0]))) {
            out = [code]
        }
        if alt, !out.isEmpty { out = [0x1b] + out }
        return out
    }
}

public enum KeyEncoding {
    /// The control code for ⌃ + `c`, or nil for a character without one.
    public static func control(_ c: Character) -> UInt8? {
        guard let a = c.asciiValue else { return nil }
        switch a {
        case 0x61 ... 0x7a: return a - 0x60 // a–z
        case 0x40 ... 0x5f: return a - 0x40 // @ A–Z [ \ ] ^ _
        case 0x20: return 0 // space = NUL
        case 0x3f: return 0x7f // ? = DEL
        default: return nil
        }
    }

    /// The more-keys sheet's groups.
    public static let navigation: [TermKey] = [.left, .right, .up, .down, .home, .end, .pageUp, .pageDown]
    public static let keys: [TermKey] = [.esc, .tab, .shiftTab, .backspace, .ctrl("c"), .ctrl("d"), .ctrl("z"), .ctrl("l"), .ctrl("r")]
    public static let answers: [TermKey] = [.text("y"), .text("n")] + (1 ... 9).map { .text(String($0)) }
    public static let functions: [TermKey] = (1 ... 12).map { .function($0) }
}

/// The user's saved snippets (more keys ▸ Snippets): short texts that go
/// into the composer, or straight to the pane in typing mode. Kept per
/// device in `UserDefaults`.
public struct SnippetStore: Equatable, Sendable {
    public static let key = "agentWindow.snippets"
    public static let maxCount = 20
    public static let maxLength = 2000

    public private(set) var items: [String]

    public init(items: [String] = []) {
        self.items = items
    }

    public static func load(_ defaults: UserDefaults = .standard) -> SnippetStore {
        SnippetStore(items: defaults.stringArray(forKey: key) ?? [])
    }

    public func save(_ defaults: UserDefaults = .standard) {
        defaults.set(items, forKey: Self.key)
    }

    /// Adds `text` (trimmed) at the top; a duplicate moves up. False when
    /// it is empty or too long.
    @discardableResult
    public mutating func add(_ text: String) -> Bool {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, t.count <= Self.maxLength else { return false }
        items.removeAll { $0 == t }
        items.insert(t, at: 0)
        if items.count > Self.maxCount { items.removeLast(items.count - Self.maxCount) }
        return true
    }

    public mutating func remove(atOffsets offsets: IndexSet) {
        for i in offsets.sorted(by: >) where items.indices.contains(i) { items.remove(at: i) }
    }
}
