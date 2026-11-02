import CoreGraphics
import Foundation

// The live agent window's decisions, without SwiftUI or a socket
// (docs/dev/design/phone-terminal.md section 5): which path draws the
// window (a `/term` channel or the older `screen` snapshot), what each
// `term-closed` reason turns into, what to do on background and
// foreground, and the key bar's bytes. `AgentWindowController` (the app)
// feeds it events and runs the commands it returns. `AgentWindowTests`
// pins the behaviour.

/// Whether the daemon serves `/term` for this app.
public enum TermAvailability: Equatable, Sendable {
    /// `bot-ready` lists `term` (or the demo).
    case yes
    /// `bot-ready` came without `term`, or the daemon predates the
    /// handshake: the snapshot path.
    case no
    /// No `bot-ready` yet (the voice session is not connected): try
    /// `/term`, and fall back to the snapshot if it does not answer.
    case unknown

    /// The `bot-ready` feature.
    public static let feature = "term"

    /// From the `/ws` handshake, or the features the last `bot-ready` from
    /// this daemon listed when there is no connection now.
    public static func from(handshake: ProtocolHandshake.State, remembered: [String]?) -> TermAvailability {
        switch handshake {
        case let .ready(_, _, features):
            return features.contains(feature) ? .yes : .no
        case .legacy:
            return .no
        case .idle, .waiting, .appTooOld, .daemonTooOld:
            guard let remembered else { return .unknown }
            return remembered.contains(feature) ? .yes : .unknown
        }
    }
}

public struct AgentWindowState: Equatable, Sendable {
    public enum Phase: Equatable, Sendable {
        /// Opening a channel; nothing to draw yet (the skeleton).
        case connecting
        /// A `control` channel: frames, input, resize.
        case live
        /// An `observe` channel: another device has control.
        case observing
        /// The `screen` snapshot path (polled, or pushed over `/ws`).
        case snapshot
        /// The pane closed or is no longer an agent; nothing more comes.
        case ended
    }

    /// The slim line under the header, at most one.
    public enum Notice: Equatable, Sendable {
        /// "Another device has control · Take over".
        case otherDevice
        /// "This agent closed."
        case gone
        /// sheepdog no longer tracks the pane.
        case notTracked
        /// "herdr is not running: start herdr on your computer."
        case herdrDown
        /// The daemon has no live window: a snapshot.
        case snapshotOnly
        /// Four windows are open on this connection.
        case tooMany
        /// The live connection dropped; trying again (the last screen stays).
        case reconnecting
        /// A phone shell: the daemon does not offer it on this network.
        case shellOff
        /// A phone shell: the folder is outside the project folders.
        case shellFolder
        /// A phone shell's pane closed.
        case shellClosed

        public var text: String {
            switch self {
            case .otherDevice: return "Another device has control."
            case .gone: return "This agent closed."
            case .notTracked: return "sheepdog no longer tracks this agent."
            case .herdrDown: return "herdr is not running: start herdr on your computer."
            case .snapshotOnly: return "Snapshot only. The live window is unavailable on this computer."
            case .tooMany: return "Too many live windows are open. Close one, then try again."
            case .reconnecting: return "Reconnecting…"
            case .shellOff: return "The shell is off on this network."
            case .shellFolder: return "This folder is outside your project folders."
            case .shellClosed: return "This shell closed."
            }
        }

        /// The banner's one action, if any.
        public var action: String? {
            switch self {
            case .otherDevice: return "Take over"
            case .gone, .notTracked: return "Back to agents"
            case .herdrDown, .tooMany: return "Try again"
            case .snapshotOnly, .reconnecting: return nil
            case .shellOff, .shellFolder, .shellClosed: return "Back"
            }
        }
    }

    public enum Command: Equatable, Sendable {
        /// Open a channel at the current size.
        case open(mode: TermMessage.Mode, takeover: Bool)
        /// Close (release) the channel.
        case close
        /// Start drawing the `screen` snapshot (poll or `/ws` stream).
        case startSnapshot
        case stopSnapshot
        case scroll(TermMessage.ScrollDirection, lines: Int)
    }

    public let paneID: String
    /// A phone shell's window (no snapshot path: a shell is no agent).
    public let isShell: Bool
    public private(set) var availability: TermAvailability = .unknown
    public private(set) var phase: Phase = .connecting
    public private(set) var notice: Notice?
    /// A channel is open or opening.
    public private(set) var channelOpen = false
    public private(set) var mode: TermMessage.Mode = .control
    /// A frame arrived on the current channel.
    public private(set) var hasFrame = false
    /// Lines scrolled up from the bottom, as far as the app knows: herdr
    /// owns the scrollback and never says, so this counts the app's own
    /// scrolls and resets on Latest and on input. The daemon turns each line
    /// into one wheel notch, so the same count of lines scrolled back down
    /// undoes it, for the scrollback and for a full-screen app alike.
    public private(set) var scrolledUp = 0
    /// The grid the surface reports.
    public private(set) var cols = 0
    public private(set) var rows = 0
    public private(set) var foreground = true
    /// The snapshot path is drawing.
    public private(set) var snapshotRunning = false
    /// `/api/hello`'s `term_unavailable`: the daemon's own reason for not
    /// offering a live terminal.
    public private(set) var termUnavailableReason: String?

    public init(paneID: String, isShell: Bool = false) {
        self.paneID = paneID
        self.isShell = isShell
    }

    // MARK: - Derived

    /// Raw bytes go on the channel (the key bar, paste); else the key bar
    /// posts keys through the daemon's allow-list.
    public var takesRawInput: Bool { phase == .live && channelOpen && mode == .control }
    /// The composer and key bar work: a live control channel, or the
    /// snapshot path (which posts to the daemon).
    public var canOperate: Bool {
        switch phase {
        case .live: return takesRawInput
        case .snapshot: return true
        case .connecting, .observing, .ended: return false
        }
    }
    /// The "↓ Latest" pill.
    public var showsLatest: Bool { scrolledUp > 0 && (phase == .live || phase == .observing) }
    /// The "Connecting…" skeleton over the terminal.
    public var showsSkeleton: Bool { phase == .connecting && !hasFrame }
    /// Drags and taps go to herdr.
    public var acceptsPointer: Bool { (phase == .live || phase == .observing) && channelOpen }

    // MARK: - Events

    /// The window appeared. `.no` goes straight to the snapshot; otherwise
    /// the channel opens once the surface reports its size.
    public mutating func start(_ availability: TermAvailability, termUnavailable: String? = nil) -> [Command] {
        self.availability = availability
        termUnavailableReason = termUnavailable
        guard availability != .no else { return fallBack(.snapshotOnly) }
        phase = .connecting
        return openIfReady(mode: .control, takeover: false)
    }

    /// The handshake changed while the window is open: a daemon that now
    /// lists `term` upgrades a snapshot window.
    public mutating func availabilityChanged(_ availability: TermAvailability, termUnavailable: String? = nil) -> [Command] {
        termUnavailableReason = termUnavailable
        let was = self.availability
        self.availability = availability
        guard availability == .yes, was != .yes, phase == .snapshot, !channelOpen else { return [] }
        phase = .connecting
        return openIfReady(mode: .control, takeover: false)
    }

    /// The surface's grid. The first size opens the channel; later ones are
    /// `term-resize`s the controller sends itself.
    public mutating func sizeChanged(cols: Int, rows: Int) -> [Command] {
        self.cols = cols
        self.rows = rows
        guard phase == .connecting || phase == .observing, !channelOpen else { return [] }
        return openIfReady(mode: notice == .otherDevice ? .observe : .control, takeover: false)
    }

    public mutating func opened(mode: TermMessage.Mode) {
        self.mode = mode
        if mode == .observe { phase = .observing }
    }

    /// A frame for this window was drawn.
    public mutating func frame() -> [Command] {
        hasFrame = true
        var commands: [Command] = []
        if snapshotRunning {
            snapshotRunning = false
            commands.append(.stopSnapshot)
        }
        if mode == .observe {
            phase = .observing
            // Frames again: the connection is back.
            if notice == .reconnecting { notice = nil }
        } else {
            phase = .live
            notice = nil
        }
        return commands
    }

    /// `term-closed`. The channel is gone on the daemon's side.
    public mutating func closed(_ reason: TermCloseReason, canTakeOver: Bool = false) -> [Command] {
        channelOpen = false
        hasFrame = false
        scrolledUp = 0
        switch reason {
        case .released:
            return []
        case .takenOver, .refused("busy"):
            // Watch meanwhile; the banner offers to take it back.
            notice = .otherDevice
            phase = .observing
            return openIfReady(mode: .observe, takeover: false)
        case .gone:
            return end(isShell ? .shellClosed : .gone)
        case .refused("shell_off"):
            return end(.shellOff)
        case .refused("outside_projects"):
            return end(.shellFolder)
        case .refused("not_tracked"):
            return end(.notTracked)
        case .herdr:
            return fallBack(.herdrDown)
        case .refused("too_many"):
            return fallBack(.tooMany)
        case .refused, .other:
            return fallBack(.snapshotOnly)
        }
    }

    /// The `/term` connection failed or dropped. The socket keeps trying
    /// and reopens the channel; meanwhile a window with nothing on it
    /// shows the snapshot, and a live one keeps its last screen.
    public mutating func connectionLost() -> [Command] {
        guard channelOpen, phase != .ended else { return [] }
        hasFrame = false
        if phase == .live || phase == .observing || isShell {
            // A shell has no snapshot: wait for the socket to reopen it.
            if notice == nil { notice = .reconnecting }
            return []
        }
        guard !snapshotRunning else { return [] }
        // Nothing drawn yet: a daemon that said it has `term` but cannot
        // reach herdr answers /term with 503.
        notice = availability == .yes ? .herdrDown : .snapshotOnly
        phase = .snapshot
        snapshotRunning = true
        return [.startSnapshot]
    }

    /// `term-free`: the device that had control let go. An observing
    /// window takes control (no takeover: if a third device got there
    /// first, the refusal puts it back to observing).
    public mutating func controlFreed() -> [Command] {
        guard phase == .observing, channelOpen, mode == .observe else { return [] }
        channelOpen = false
        hasFrame = false
        scrolledUp = 0
        notice = nil
        phase = .connecting
        return [.close] + openIfReady(mode: .control, takeover: false)
    }

    /// The user confirmed Take over.
    public mutating func takeOver() -> [Command] {
        guard phase != .ended else { return [] }
        var commands: [Command] = []
        if channelOpen { commands.append(.close) }
        channelOpen = false
        hasFrame = false
        notice = nil
        phase = .connecting
        commands += openIfReady(mode: .control, takeover: true)
        return commands
    }

    /// The banner's Try again (herdr down, too many windows).
    public mutating func retry() -> [Command] {
        guard phase == .snapshot, !channelOpen, availability != .no else { return [] }
        phase = .connecting
        notice = nil
        return openIfReady(mode: .control, takeover: false)
    }

    /// The app went to the background: release the pane so the computer
    /// gets its layout back, and stop polling.
    public mutating func enteredBackground() -> [Command] {
        guard foreground else { return [] }
        foreground = false
        var commands: [Command] = []
        if channelOpen {
            commands.append(.close)
            channelOpen = false
            hasFrame = false
            scrolledUp = 0
        }
        if snapshotRunning {
            commands.append(.stopSnapshot)
            snapshotRunning = false
        }
        return commands
    }

    /// Back in the foreground: reattach (observing again if another device
    /// had control), or resume the snapshot.
    public mutating func becameActive() -> [Command] {
        guard !foreground else { return [] }
        foreground = true
        switch phase {
        case .ended:
            return []
        case .snapshot where isShell:
            phase = .connecting
            return openIfReady(mode: .control, takeover: false)
        case .snapshot where availability == .no || notice == .tooMany:
            snapshotRunning = true
            return [.startSnapshot]
        case .observing:
            return openIfReady(mode: .observe, takeover: false)
        case .snapshot, .connecting, .live:
            if notice == .reconnecting || notice == .herdrDown { notice = nil }
            phase = .connecting
            return openIfReady(mode: .control, takeover: false)
        }
    }

    /// The window is closing.
    public mutating func disappeared() -> [Command] {
        var commands: [Command] = []
        if channelOpen { commands.append(.close) }
        if snapshotRunning { commands.append(.stopSnapshot) }
        channelOpen = false
        snapshotRunning = false
        return commands
    }

    /// A drag of `lines` whole lines: positive is the finger moving down,
    /// which shows older output (`dir: up`).
    public mutating func dragged(lines: Int) -> [Command] {
        guard acceptsPointer, lines != 0 else { return [] }
        if lines > 0 {
            scrolledUp += lines
            return [.scroll(.up, lines: lines)]
        }
        let down = -lines
        scrolledUp = max(0, scrolledUp - down)
        return [.scroll(.down, lines: down)]
    }

    /// "↓ Latest": back to the bottom.
    public mutating func latest() -> [Command] {
        guard scrolledUp > 0 else { return [] }
        let lines = scrolledUp
        scrolledUp = 0
        return acceptsPointer ? [.scroll(.down, lines: lines)] : []
    }

    /// Before raw input: the input lands at the bottom, so go there.
    public mutating func willSendInput() -> [Command] {
        latest()
    }

    /// The notice's line. Snapshot-only uses the daemon's own reason when
    /// it provided one.
    public var noticeText: String? {
        guard notice == .snapshotOnly else { return notice?.text }
        let reason = termUnavailableReason?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !reason.isEmpty else { return notice?.text }
        let sentence = reason.prefix(1).uppercased() + reason.dropFirst()
        return "Snapshot only. \(sentence)"
    }

    // MARK: - Helpers

    private mutating func openIfReady(mode: TermMessage.Mode, takeover: Bool) -> [Command] {
        guard foreground, !channelOpen, cols > 0, rows > 0 else { return [] }
        channelOpen = true
        hasFrame = false
        self.mode = mode
        return [.open(mode: mode, takeover: takeover)]
    }

    private mutating func fallBack(_ notice: Notice) -> [Command] {
        self.notice = notice
        phase = .snapshot
        guard foreground, !snapshotRunning, !isShell else { return [] }
        snapshotRunning = true
        return [.startSnapshot]
    }

    private mutating func end(_ notice: Notice) -> [Command] {
        self.notice = notice
        phase = .ended
        var commands: [Command] = []
        if snapshotRunning {
            snapshotRunning = false
            commands.append(.stopSnapshot)
        }
        return commands
    }
}

// MARK: - The key bar

public extension AgentKey {
    /// The key bar, left to right (digits and more keys are phase 3's
    /// more-keys sheet).
    static let bar: [AgentKey] = [.esc, .tab, .up, .down, .ctrlC, .enter]

    /// The bytes a terminal sends for the key, for a live channel.
    var bytes: [UInt8] {
        switch self {
        case .enter: return [0x0d]
        case .esc: return [0x1b]
        case .ctrlC: return [0x03]
        case .tab: return [0x09]
        case .up: return Array("\u{1b}[A".utf8)
        case .down: return Array("\u{1b}[B".utf8)
        case .one, .two, .three: return Array(rawValue.utf8)
        }
    }

    /// The key bar's glyph.
    var barLabel: String {
        switch self {
        case .esc: return "esc"
        case .tab: return "⇥"
        case .ctrlC: return "⌃C"
        case .enter: return "⏎"
        default: return label
        }
    }
}

// MARK: - Snapshot text in the terminal

public enum SnapshotScreen {
    /// A `screen` snapshot (plain text at the pane's width) as bytes for
    /// the terminal surface: cursor hidden, clear, home, autowrap off so a wider line is
    /// clipped rather than wrapped, and the last `rows` lines with `\r\n`
    /// between them (a bare `\n` is only a line feed in a terminal).
    ///
    /// With `ansi` (a `screen-ansi` frame) ESC stays, so SGR colours draw;
    /// each line ends with an SGR reset, so a colour never bleeds into the
    /// next line's margin.
    public static func bytes(text: String, rows: Int, ansi: Bool = false) -> [UInt8] {
        var lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        while lines.last?.isEmpty == true { lines.removeLast() }
        if rows > 0, lines.count > rows { lines = Array(lines.suffix(rows)) }
        let body = lines.map { line in
            let kept = String(line.unicodeScalars.filter { $0.value >= 0x20 || $0 == "\t" || (ansi && $0 == "\u{1b}") }.map(Character.init))
            return ansi ? kept + "\u{1b}[0m" : kept
        }.joined(separator: "\r\n")
        return Array("\u{1b}[?25l\u{1b}[?7l\u{1b}[0m\u{1b}[2J\u{1b}[H".utf8) + Array(body.utf8)
    }
}
