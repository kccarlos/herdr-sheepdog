import Foundation

/// What the voice session's Live Activity shows on the lock screen: the
/// voice's phase, the current computer (only with several), up to three
/// agents waiting for an answer with their questions, and how many agents
/// are working and done. It is the activity's `ContentState` itself
/// (`Sources/SheepdogActivity`), so the widget decodes exactly this and
/// draws it with the words below. Pure: the mapping from the session and
/// the agents is tested here.
///
/// The encoded form is the wire format of a Live Activity update, local
/// or pushed, and ActivityKit drops any update over 4 KB: questions are
/// cut to `questionLimit` and names to `nameLimit`.
public struct LiveActivityContent: Codable, Hashable, Sendable {
    /// What the voice is doing. Raw values cross the process boundary.
    public enum Phase: String, Codable, CaseIterable, Sendable {
        case connecting
        case reconnecting
        case listening
        case speaking
        case thinking
        /// The microphone is off; the voice can still talk.
        case muted
        /// Keep listening with the screen locked is off and the app went to
        /// the background, so the audio stopped.
        case locked
        /// iOS paused the audio (a call, Siri).
        case paused
        case noMicrophone = "no_microphone"
        /// The daemon has no voice session.
        case voiceOff = "voice_off"
    }

    /// One agent waiting for an answer. Tapping it opens the agent's
    /// window (`url`).
    public struct Row: Codable, Hashable, Sendable, Identifiable {
        /// The agent's herdr pane id.
        public var pane: String
        /// The agent's name.
        public var agent: String
        /// Its question, in one line.
        public var question: String
        /// The computer it runs on, only when that is not the current one.
        public var computer: String?
        /// That computer's id, for the link.
        public var computerID: String?

        public init(pane: String, agent: String, question: String, computer: String? = nil, computerID: String? = nil) {
            self.pane = pane
            self.agent = agent
            self.question = question
            self.computer = computer
            self.computerID = computerID
        }

        public var id: String { (computerID ?? "") + "/" + pane }

        /// `sheepdog://window/<pane>`: the agent's window.
        public var url: URL { AppRoute.window(pane, computer: computerID).url }

        /// "auth-fix asks: Run npm test in webapp?" (", on Home Mac").
        public var accessibilityLabel: String {
            var label = "\(agent) asks: \(question)"
            if let computer { label += ", on \(computer)" }
            return label
        }

        enum CodingKeys: String, CodingKey {
            case pane, agent, question, computer
            case computerID = "computer_id"
        }
    }

    public var phase: Phase
    /// The microphone is muted (labels the Mute button; speaking and
    /// thinking outrank `muted` as the phase).
    public var muted: Bool
    /// The current computer's name, only when several are paired.
    public var computer: String?
    /// Up to `maxRows` waiting agents: the current computer's first, then
    /// by how long they have waited.
    public var rows: [Row]
    /// Every agent waiting for an answer (at least `rows.count`).
    public var waiting: Int
    public var working: Int
    public var done: Int

    public init(phase: Phase, muted: Bool = false, computer: String? = nil, rows: [Row] = [], waiting: Int? = nil, working: Int = 0, done: Int = 0) {
        self.phase = phase
        self.muted = muted
        self.computer = computer
        self.rows = Array(rows.prefix(Self.maxRows))
        self.waiting = max(waiting ?? rows.count, self.rows.count)
        self.working = max(0, working)
        self.done = max(0, done)
    }

    public static let maxRows = 3
    /// A question longer than this is cut, with an ellipsis.
    public static let questionLimit = 90
    public static let nameLimit = 32

    /// How long an update stays fresh before the system dims it as stale.
    /// The app refreshes well inside it (`refreshInterval`) while the
    /// session runs, so only an activity a killed app left goes stale.
    public static let staleAfter: TimeInterval = 15 * 60
    public static let refreshInterval: TimeInterval = 5 * 60
}

// MARK: - Building it

extension LiveActivityContent {
    /// The activity for the session as it is now, or nil when there is no
    /// session to show (not connected): the activity ends.
    ///
    /// - `agents`, `questions`: the current computer's agents and open
    ///   questions (`/api/state`, oldest question first).
    /// - `computer`: the current computer's name, only when several are
    ///   paired; nil with one, so one computer looks as simple as ever.
    /// - `suspendedInBackground`: Keep listening with the screen locked is
    ///   off and the app went to the background, so the audio stopped.
    public static func make(
        state: SessionState,
        muted: Bool,
        agents: [Agent] = [],
        questions: [PendingAction] = [],
        computer: String? = nil,
        suspendedInBackground: Bool = false
    ) -> LiveActivityContent? {
        guard let phase = phase(state: state, suspendedInBackground: suspendedInBackground) else { return nil }
        let running = agents.filter { $0.state.isRunning }
        let rows = waitingRows(agents: running, questions: questions)
        return LiveActivityContent(
            phase: phase,
            muted: muted,
            computer: computer.flatMap { clip($0, to: nameLimit) },
            rows: rows,
            waiting: running.filter { $0.state == .blocked }.count,
            working: running.filter { $0.state == .working }.count,
            done: running.filter { $0.state == .done }.count
        )
    }

    public static func phase(state: SessionState, suspendedInBackground: Bool) -> Phase? {
        switch state {
        case .disconnected: return nil
        case .connecting: return .connecting
        case .reconnecting: return .reconnecting
        case _ where suspendedInBackground: return .locked
        case .voiceOff: return .voiceOff
        case .audioPaused: return .paused
        case .noMicrophone: return .noMicrophone
        case .speaking: return .speaking
        case .thinking, .stalled: return .thinking
        case .muted: return .muted
        case .listening, .silent, .idle: return .listening
        }
    }

    /// The blocked agents as rows: those with an open question in the
    /// questions' order (oldest first), then the rest by how long they
    /// have been blocked.
    static func waitingRows(agents: [Agent], questions: [PendingAction]) -> [Row] {
        let blocked = agents.filter { $0.state == .blocked }
        var order: [String: Int] = [:]
        for (i, q) in questions.enumerated() where order[q.paneID] == nil { order[q.paneID] = i }
        let sorted = blocked.sorted { a, b in
            switch (order[a.paneID], order[b.paneID]) {
            case let (x?, y?): return x < y
            case (_?, nil): return true
            case (nil, _?): return false
            case (nil, nil):
                let x = a.stateSince ?? .distantFuture, y = b.stateSince ?? .distantFuture
                return x != y ? x < y : a.alias < b.alias
            }
        }
        return sorted.prefix(maxRows).map { agent in
            let open = questions.first { $0.paneID == agent.paneID }
            let candidates = [open?.question, open?.spokenSummary, open?.summary, open?.command,
                              agent.lastBlock?.answered == true ? nil : agent.lastBlock?.question]
            let question = candidates.compactMap { $0.flatMap { clip($0, to: questionLimit) } }.first ?? "Needs you"
            return Row(pane: agent.paneID, agent: clip(agent.alias, to: nameLimit) ?? agent.paneID, question: question)
        }
    }

    /// One line, whitespace folded, cut to `limit` characters with an
    /// ellipsis; nil when nothing is left.
    static func clip(_ text: String, to limit: Int) -> String? {
        let line = text.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        guard !line.isEmpty else { return nil }
        guard line.count > limit else { return line }
        let cut = line.prefix(limit - 1).trimmingCharacters(in: .whitespaces)
        return cut + "…"
    }
}

// MARK: - Words

extension LiveActivityContent {
    /// The headline: "Listening", "Muted", "Reconnecting…".
    public var title: String {
        switch phase {
        case .connecting: return "Connecting…"
        case .reconnecting: return "Reconnecting…"
        case .listening: return "Listening"
        case .speaking: return "Speaking"
        case .thinking: return "Thinking"
        case .muted: return "Muted"
        case .locked: return "Paused while locked"
        case .paused: return "Paused by iOS"
        case .noMicrophone: return "No microphone"
        case .voiceOff: return "Voice is off"
        }
    }

    /// "2 working · 1 done", or nil when neither is.
    public var countsText: String? {
        var parts: [String] = []
        if working > 0 { parts.append("\(working) working") }
        if done > 0 { parts.append("\(done) done") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// The line under the headline: the computer (several only) and the
    /// counts, "Work Mac · 2 working · 1 done"; the voice's name when
    /// there is nothing else to say.
    public func subtitle(voiceName: String) -> String {
        let parts = [computer, countsText].compactMap { $0 }
        return parts.isEmpty ? voiceName : parts.joined(separator: " · ")
    }

    /// The rows that fit `slots` lines, and how many waiting agents are
    /// left over for the "N more waiting" line. When not every waiting
    /// agent fits, the last slot says how many more there are.
    public func visibleRows(slots: Int) -> (rows: [Row], more: Int) {
        let slots = max(1, slots)
        if waiting <= slots, rows.count >= waiting {
            return (Array(rows.prefix(slots)), 0)
        }
        let shown = Array(rows.prefix(slots - 1))
        return (shown, waiting - shown.count)
    }

    /// "3 more waiting", or "3 agents waiting" when no row is shown.
    public static func moreText(_ more: Int, afterRows: Bool) -> String {
        if afterRows { return "\(more) more waiting" }
        return more == 1 ? "1 agent waiting" : "\(more) agents waiting"
    }

    /// "1 agent waiting for an answer" / "3 agents waiting for an answer".
    public static func waitingText(_ count: Int) -> String? {
        switch count {
        case ..<1: return nil
        case 1: return "1 agent waiting for an answer"
        default: return "\(count) agents waiting for an answer"
        }
    }

    /// The headline's VoiceOver sentence: the phase, the computer, the
    /// mute state and the counts. Each row reads on its own.
    public func accessibilityLabel(voiceName: String) -> String {
        var sentence: String
        switch phase {
        case .connecting: sentence = "\(voiceName) is connecting"
        case .reconnecting: sentence = "\(voiceName) is reconnecting"
        case .listening: sentence = "\(voiceName) is listening"
        case .speaking: sentence = "\(voiceName) is speaking"
        case .thinking: sentence = "\(voiceName) is thinking"
        case .muted: sentence = "Microphone muted. \(voiceName) can still talk"
        case .locked: sentence = "Paused while the phone is locked. Open Sheepdog to talk again"
        case .paused: sentence = "iOS paused the audio. Open Sheepdog to resume"
        case .noMicrophone: sentence = "No microphone. \(voiceName) cannot hear you"
        case .voiceOff: sentence = "The computer has no voice session"
        }
        if let computer { sentence += ", on \(computer)" }
        if muted, phase == .speaking || phase == .thinking { sentence += ". Microphone muted" }
        if let waitingText = Self.waitingText(waiting) { sentence += ". \(waitingText)" }
        if working > 0 || done > 0 { sentence += ". \(working) working, \(done) done" }
        return sentence
    }

    /// The short label for the compact and minimal Dynamic Island.
    public var compactAccessibilityLabel: String {
        var label = phase == .muted || !muted ? title : "\(title), muted"
        if waiting > 0 { label += ", \(waiting) waiting" }
        return label
    }

    public static let staleTitle = "May have ended"
    public static func staleSubtitle(voiceName: String) -> String { "Open \(voiceName) to check" }
    public static let staleAccessibilityLabel = "The voice session may have ended. Open Sheepdog to check."
}

// MARK: - Decoding

extension LiveActivityContent {
    enum CodingKeys: String, CodingKey {
        case phase, muted, computer, rows, waiting, working, done
    }

    /// Lenient where it costs nothing: ActivityKit drops a whole update
    /// that fails to decode, so an unknown phase reads as listening and a
    /// missing list or count as empty.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let raw = try c.decodeIfPresent(String.self, forKey: .phase) ?? ""
        self.init(
            phase: Phase(rawValue: raw) ?? .listening,
            muted: try c.decodeIfPresent(Bool.self, forKey: .muted) ?? false,
            computer: try c.decodeIfPresent(String.self, forKey: .computer),
            rows: try c.decodeIfPresent([Row].self, forKey: .rows) ?? [],
            waiting: try c.decodeIfPresent(Int.self, forKey: .waiting),
            working: try c.decodeIfPresent(Int.self, forKey: .working) ?? 0,
            done: try c.decodeIfPresent(Int.self, forKey: .done) ?? 0
        )
    }
}
