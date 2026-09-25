import Foundation

/// `GET /api/hello` — public, so the app can confirm it found a sheepdog
/// daemon and whether it needs a token before it opens anything else.
public struct Hello: Codable, Equatable, Sendable {
    public struct Audio: Codable, Equatable, Sendable {
        public var inHz: Int
        public var outHz: Int
        public var format: String

        enum CodingKeys: String, CodingKey {
            case inHz = "in_hz"
            case outHz = "out_hz"
            case format
        }

        public init(inHz: Int, outHz: Int, format: String) {
            self.inHz = inHz
            self.outHz = outHz
            self.format = format
        }
    }

    public var name: String
    public var version: String
    /// What the dog calls itself (the daemon's `voice.name`).
    public var voiceName: String?
    /// `"token"` or `"none"`.
    public var auth: String
    public var ws: String
    public var audio: Audio
    /// The live session's provider and model (`gemini`, `gemini-3.8-live`),
    /// on daemons that can switch provider. Absent on older ones.
    public var voiceProvider: String?
    public var voiceModel: String?

    enum CodingKeys: String, CodingKey {
        case name, version, auth, ws, audio
        case voiceName = "voice_name"
        case voiceProvider = "voice_provider"
        case voiceModel = "voice_model"
    }

    public var needsToken: Bool { auth == "token" }
    public var displayName: String { voiceName?.isEmpty == false ? voiceName! : "Sheepdog" }

    public init(name: String, version: String, voiceName: String? = nil, auth: String, ws: String, audio: Audio, voiceProvider: String? = nil, voiceModel: String? = nil) {
        self.name = name
        self.version = version
        self.voiceName = voiceName
        self.auth = auth
        self.ws = ws
        self.audio = audio
        self.voiceProvider = voiceProvider
        self.voiceModel = voiceModel
    }
}

/// One agent of the flock, from `GET /api/state` (the same list as
/// `GET /api/agents`).
///
/// A lenient mirror of the daemon's `state.Agent`: only the fields the app
/// shows are decoded, so a daemon that grows a field does not break us.
public struct Agent: Codable, Equatable, Identifiable, Sendable {
    /// `state.Block`: the last time the agent was blocked.
    public struct Block: Codable, Equatable, Sendable {
        public var question: String?
        public var seenAt: Date?
        /// The question was answered (by tap, voice or the brain).
        public var answered: Bool?

        enum CodingKeys: String, CodingKey {
            case question, answered
            case seenAt = "seen_at"
        }

        public init(question: String? = nil, seenAt: Date? = nil, answered: Bool? = nil) {
            self.question = question
            self.seenAt = seenAt
            self.answered = answered
        }
    }

    /// `state.Result`: what the agent last finished, as the shepherd heard
    /// it — the announcement (`spoken`) and the brain's detail for it.
    public struct Result: Codable, Equatable, Sendable {
        public var spoken: String?
        public var detail: String?
        public var at: Date?

        public init(spoken: String? = nil, detail: String? = nil, at: Date? = nil) {
            self.spoken = spoken
            self.detail = detail
            self.at = at
        }
    }

    /// `state.JournalEntry`: one stop of the worker, read from its own
    /// transcript — what it wrote for a human, the instruction it was on,
    /// the files it touched and its tool counts.
    public struct JournalEntry: Codable, Equatable, Sendable {
        public var at: Date?
        /// `done`, `idle` or `blocked`.
        public var event: String?
        /// The worker's last message, at most 600 characters.
        public var lastAssistant: String?
        public var instruction: String?
        public var filesTouched: [String]?
        public var tools: [String: Int]?

        enum CodingKeys: String, CodingKey {
            case at, event, instruction, tools
            case lastAssistant = "last_assistant"
            case filesTouched = "files_touched"
        }

        public init(at: Date? = nil, event: String? = nil, lastAssistant: String? = nil, instruction: String? = nil, filesTouched: [String]? = nil, tools: [String: Int]? = nil) {
            self.at = at
            self.event = event
            self.lastAssistant = lastAssistant
            self.instruction = instruction
            self.filesTouched = filesTouched
            self.tools = tools
        }
    }

    /// The daemon's herdr states.
    public enum State: String, Codable, Equatable, Sendable {
        case idle, working, blocked, done, unknown, exited

        /// Lenient decode: an unrecognised state reads as `.unknown` rather
        /// than failing the whole `/api/state` response.
        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = State(rawValue: raw) ?? .unknown
        }
    }

    public var paneID: String
    public var alias: String
    public var kind: String?
    public var workspaceLabel: String?
    public var name: String?
    public var state: State
    public var stateSince: Date?
    public var currentTask: String?
    public var lastTask: String?
    public var lastBlock: Block?
    public var lastResult: Result?
    /// The work journal, oldest first (at most 20 entries).
    public var journal: [JournalEntry]?
    /// Injected by the debug page rather than seen through herdr.
    public var synthetic: Bool?

    public var id: String { paneID }

    enum CodingKeys: String, CodingKey {
        case alias, kind, name, state, journal, synthetic
        case paneID = "pane_id"
        case workspaceLabel = "workspace_label"
        case stateSince = "state_since"
        case currentTask = "current_task"
        case lastTask = "last_task"
        case lastBlock = "last_block"
        case lastResult = "last_result"
    }

    public init(
        paneID: String,
        alias: String,
        kind: String? = nil,
        workspaceLabel: String? = nil,
        name: String? = nil,
        state: State,
        stateSince: Date? = nil,
        currentTask: String? = nil,
        lastTask: String? = nil,
        lastBlock: Block? = nil,
        lastResult: Result? = nil,
        journal: [JournalEntry]? = nil,
        synthetic: Bool? = nil
    ) {
        self.paneID = paneID
        self.alias = alias
        self.kind = kind
        self.workspaceLabel = workspaceLabel
        self.name = name
        self.state = state
        self.stateSince = stateSince
        self.currentTask = currentTask
        self.lastTask = lastTask
        self.lastBlock = lastBlock
        self.lastResult = lastResult
        self.journal = journal
        self.synthetic = synthetic
    }

    /// The one-line subtitle on a chip: the pending question wins, then an
    /// unanswered block, then the last result, then the current task.
    public func subtitle(pendingQuestion: String?) -> String? {
        if let q = pendingQuestion?.trimmed, !q.isEmpty { return q }
        if state == .blocked, lastBlock?.answered != true, let q = lastBlock?.question?.trimmed, !q.isEmpty { return q }
        if let r = lastResult?.spoken?.trimmed, !r.isEmpty { return r }
        if let t = currentTask?.trimmed, !t.isEmpty { return t }
        return nil
    }

    /// The newest journal entry that carries what the worker wrote for a
    /// human, if the daemon has read one from its transcript.
    public var lastWorkerMessage: JournalEntry? {
        journal?.last { !($0.lastAssistant?.trimmed.isEmpty ?? true) }
    }

    /// "4 min", "2 h 5 min", "just now": how long the agent has been in its
    /// state at `now`. Nil without a `state_since`.
    public func timeInState(at now: Date) -> String? {
        guard let since = stateSince else { return nil }
        return Agent.duration(now.timeIntervalSince(since))
    }

    /// A short duration for people: under a minute is "just now".
    public static func duration(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds))
        if s < 60 { return "just now" }
        let minutes = s / 60
        if minutes < 60 { return "\(minutes) min" }
        let hours = minutes / 60
        if hours < 48 {
            let rest = minutes % 60
            return rest == 0 ? "\(hours) h" : "\(hours) h \(rest) min"
        }
        return "\(hours / 24) days"
    }
}

/// One choice of an approval prompt: `options[]` of `/api/state`'s pending
/// action, of the `pending.created` event and of an `approval` item.
/// `index` is 1-based and is what `POST /api/pending/answer` takes.
public struct PendingOption: Codable, Equatable, Hashable, Sendable {
    public var index: Int
    public var label: String
    public var keys: [String]
    /// The option the prompt's cursor is on (what enter selects).
    public var isDefault: Bool

    enum CodingKeys: String, CodingKey {
        case index, label, keys
        case isDefault = "default"
    }

    public init(index: Int, label: String, keys: [String] = [], isDefault: Bool = false) {
        self.index = index
        self.label = label
        self.keys = keys
        self.isDefault = isDefault
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        index = try c.decodeIfPresent(Int.self, forKey: .index) ?? 0
        label = try c.decodeIfPresent(String.self, forKey: .label) ?? ""
        keys = try c.decodeIfPresent([String].self, forKey: .keys) ?? []
        isDefault = try c.decodeIfPresent(Bool.self, forKey: .isDefault) ?? false
    }

    /// "No, and tell Codex what to do differently" — the one to style as
    /// destructive.
    public var isNegative: Bool {
        let l = label.trimmed.lowercased()
        return l == "no" || l.hasPrefix("no,") || l.hasPrefix("no ") || l.hasPrefix("no.") || l.hasPrefix("deny") || l.hasPrefix("reject")
    }

    /// What VoiceOver reads on the button: "Approve: Yes, proceed".
    public var accessibilityLabel: String {
        "\(isNegative ? "Decline" : "Approve"): \(label)"
    }

    /// Older daemons (and older log lines) send options without an index:
    /// number them by position, 1-based, the way the daemon does.
    static func normalised(_ options: [PendingOption]?) -> [PendingOption]? {
        guard let options else { return nil }
        return options.enumerated().map { offset, option in
            var o = option
            if o.index <= 0 { o.index = offset + 1 }
            return o
        }
    }
}

/// The single-slot pending action from `GET /api/state` (and the payload
/// of a `pending.created` event).
public struct PendingAction: Codable, Equatable, Sendable {
    public var id: String
    public var paneID: String
    public var kind: String
    public var spokenSummary: String?
    public var question: String?
    public var options: [PendingOption]?
    /// The agent's alias (`/api/state` and `pending.created` only).
    public var agent: String?
    /// The plain-words summary (`summary`; `/api/state` also sends it as
    /// `spoken_summary`).
    public var summary: String?
    /// The command the prompt is about, when it shows one.
    public var command: String?

    enum CodingKeys: String, CodingKey {
        case id, kind, question, options, agent, summary, command
        case paneID = "pane_id"
        case spokenSummary = "spoken_summary"
    }

    public init(
        id: String,
        paneID: String,
        kind: String,
        spokenSummary: String? = nil,
        question: String? = nil,
        options: [PendingOption]? = nil,
        agent: String? = nil,
        summary: String? = nil,
        command: String? = nil
    ) {
        self.id = id
        self.paneID = paneID
        self.kind = kind
        self.spokenSummary = spokenSummary
        self.question = question
        self.options = PendingOption.normalised(options)
        self.agent = agent
        self.summary = summary
        self.command = command
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        paneID = try c.decodeIfPresent(String.self, forKey: .paneID) ?? ""
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        spokenSummary = try c.decodeIfPresent(String.self, forKey: .spokenSummary)
        question = try c.decodeIfPresent(String.self, forKey: .question)
        // Older lines carry a count here, not the options.
        options = PendingOption.normalised(try? c.decodeIfPresent([PendingOption].self, forKey: .options))
        agent = try c.decodeIfPresent(String.self, forKey: .agent)
        summary = try c.decodeIfPresent(String.self, forKey: .summary)
        command = try c.decodeIfPresent(String.self, forKey: .command)
    }

    /// A worker's approval prompt the shepherd can answer by tap.
    public var isAnswerPrompt: Bool { kind == "answer_prompt" && !(options ?? []).isEmpty }

    /// What the card says, in plain words.
    public var displaySummary: String {
        for candidate in [summary, spokenSummary, question] {
            if let s = candidate?.trimmed, !s.isEmpty { return s }
        }
        return "An agent is waiting for your answer."
    }

    /// Builds the action from a `pending.created` event (its payload is the
    /// pending view; the pane comes from the event).
    public init?(event: DaemonEvent) {
        guard event.type == "pending.created",
              let data = try? JSONEncoder().encode(event.payload),
              var action = try? JSONDecoder().decode(PendingAction.self, from: data)
        else { return nil }
        if action.paneID.isEmpty { action.paneID = event.paneID ?? "" }
        self = action
    }
}

/// The supervisor's model, from `/api/state`'s `brain`, when the daemon
/// sends one. Optional throughout: an older daemon simply omits the key.
public struct BrainInfo: Codable, Equatable, Sendable {
    public var provider: String?
    public var model: String?

    public init(provider: String? = nil, model: String? = nil) {
        self.provider = provider
        self.model = model
    }

    /// "gemini · gemini-3.5-flash", or just whichever half is there.
    public var display: String? {
        let parts = [provider, model].compactMap { $0?.trimmed }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// `POST /api/conversation/reset {"via": "app"}` — forgets the supervisor's
/// and the voice model's short-term conversation; the roster, open
/// approvals, promises and notes, and the scrollback itself, are kept (see
/// docs/architecture.md "New conversation"). `DaemonClient.resetConversation`
/// answers `nil` for a 404, the same convention as `voiceProviders()`: an
/// older daemon simply does not have the route yet.
public struct ConversationReset: Codable, Equatable, Sendable {
    public var ok: Bool
    public var resetID: String
    public var at: Date

    enum CodingKeys: String, CodingKey {
        case ok
        case resetID = "reset_id"
        case at
    }

    public init(ok: Bool, resetID: String, at: Date) {
        self.ok = ok
        self.resetID = resetID
        self.at = at
    }
}

/// Whether the "New conversation" action is worth offering, learned from
/// the daemon's own answer rather than assumed. `nil` (a 404) is the only
/// thing that hides it; any real response — success or a thrown
/// `DaemonError` — leaves the previous answer alone, since neither tells
/// the app the route does not exist.
public enum ConversationResetAvailability: Equatable, Sendable {
    case supported
    case unsupported

    public init(response: ConversationReset?) {
        self = response == nil ? .unsupported : .supported
    }

    public var showsAction: Bool { self == .supported }
}

/// The reply to `POST /api/inject/utterance`: `{"ok":true,"via":"voice"}`
/// with a voice session (the answer comes as speech), or the text router's
/// tool response (`tool`, `spoken`, `via: "text_router"`) with
/// `--voice=off`.
public struct UtteranceReply: Decodable, Equatable, Sendable {
    public var via: String?
    public var tool: String?
    public var spoken: String?

    public init(via: String? = nil, tool: String? = nil, spoken: String? = nil) {
        self.via = via
        self.tool = tool
        self.spoken = spoken
    }
}

/// `GET /api/state`, trimmed to what the app shows.
public struct DaemonState: Codable, Equatable, Sendable {
    public struct VoiceInfo: Codable, Equatable, Sendable {
        /// `gemini` or `off`.
        public var mode: String
        public var name: String?
        public var status: String
        public var reason: String?
        public var muted: Bool
        public var stalled: Bool
        public var clientConnected: Bool

        enum CodingKeys: String, CodingKey {
            case mode, name, status, reason, muted, stalled
            case clientConnected = "client_connected"
        }

        public var isOff: Bool { mode == "off" }

        public init(mode: String, name: String? = nil, status: String, reason: String? = nil, muted: Bool = false, stalled: Bool = false, clientConnected: Bool = false) {
            self.mode = mode
            self.name = name
            self.status = status
            self.reason = reason
            self.muted = muted
            self.stalled = stalled
            self.clientConnected = clientConnected
        }
    }

    public var agents: [Agent]
    public var pendingAction: PendingAction?
    public var voice: VoiceInfo
    public var herdr: String?
    public var version: String?
    /// The supervisor's current model, when the daemon reports one.
    public var brain: BrainInfo?
    /// The live voice session's provider and model, when the daemon can
    /// switch provider (`GET /api/voice/providers`).
    public var voiceProvider: String?
    public var voiceModel: String?
    /// When the current conversation started — the last reset, or the
    /// daemon's own start if there has not been one. Absent on a daemon
    /// that predates "New conversation".
    public var conversationStartedAt: Date?

    enum CodingKeys: String, CodingKey {
        case agents, voice, herdr, version, brain
        case pendingAction = "pending_action"
        case voiceProvider = "voice_provider"
        case voiceModel = "voice_model"
        case conversationStartedAt = "conversation_started_at"
    }

    public init(agents: [Agent], pendingAction: PendingAction? = nil, voice: VoiceInfo, herdr: String? = nil, version: String? = nil, brain: BrainInfo? = nil, voiceProvider: String? = nil, voiceModel: String? = nil, conversationStartedAt: Date? = nil) {
        self.agents = agents
        self.pendingAction = pendingAction
        self.voice = voice
        self.herdr = herdr
        self.version = version
        self.brain = brain
        self.voiceProvider = voiceProvider
        self.voiceModel = voiceModel
        self.conversationStartedAt = conversationStartedAt
    }

    /// The question waiting on a given pane, if the pending action is one.
    public func pendingQuestion(for paneID: String) -> String? {
        guard let p = pendingAction, p.paneID == paneID else { return nil }
        return p.question ?? p.spokenSummary
    }
}

/// One line of the daemon's event log, streamed by `GET /events`.
public struct DaemonEvent: Equatable, Sendable {
    public var seq: UInt64
    public var ts: Date?
    public var source: String
    public var type: String
    public var paneID: String?
    /// The raw `payload` object, kept as JSON so the app can pick fields out
    /// without mirroring every event type.
    public var payload: [String: JSONValue]

    public init(seq: UInt64, ts: Date? = nil, source: String, type: String, paneID: String? = nil, payload: [String: JSONValue] = [:]) {
        self.seq = seq
        self.ts = ts
        self.source = source
        self.type = type
        self.paneID = paneID
        self.payload = payload
    }

    public func string(_ key: String) -> String? {
        if case let .string(s)? = payload[key] { return s }
        return nil
    }

    public func bool(_ key: String) -> Bool? {
        if case let .bool(b)? = payload[key] { return b }
        return nil
    }
}

/// A minimal JSON value, so event payloads survive without a type per event.
public enum JSONValue: Codable, Equatable, Sendable {
    case string(String)
    case number(Double)
    case bool(Bool)
    case object([String: JSONValue])
    case array([JSONValue])
    case null

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let d = try? c.decode(Double.self) { self = .number(d); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        if let o = try? c.decode([String: JSONValue].self) { self = .object(o); return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "unsupported JSON value")
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case let .string(s): try c.encode(s)
        case let .number(d): try c.encode(d)
        case let .bool(b): try c.encode(b)
        case let .object(o): try c.encode(o)
        case let .array(a): try c.encode(a)
        case .null: try c.encodeNil()
        }
    }

}

extension String {
    var trimmed: String { trimmingCharacters(in: .whitespacesAndNewlines) }
}
