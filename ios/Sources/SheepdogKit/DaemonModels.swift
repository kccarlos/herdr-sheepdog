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

    enum CodingKeys: String, CodingKey {
        case name, version, auth, ws, audio
        case voiceName = "voice_name"
    }

    public var needsToken: Bool { auth == "token" }
    public var displayName: String { voiceName?.isEmpty == false ? voiceName! : "Sheepdog" }

    public init(name: String, version: String, voiceName: String? = nil, auth: String, ws: String, audio: Audio) {
        self.name = name
        self.version = version
        self.voiceName = voiceName
        self.auth = auth
        self.ws = ws
        self.audio = audio
    }
}

/// One agent of the flock, from `GET /api/state`.
///
/// A lenient mirror of the daemon's `state.Agent`: only the fields the app
/// shows are decoded, so a daemon that grows a field does not break us.
public struct Agent: Codable, Equatable, Identifiable, Sendable {
    public struct Block: Codable, Equatable, Sendable {
        public var question: String?
        public var options: [Option]?
        public var at: Date?

        public struct Option: Codable, Equatable, Sendable {
            public var label: String
            public init(label: String) { self.label = label }
            enum CodingKeys: String, CodingKey { case label }
        }

        public init(question: String? = nil, options: [Option]? = nil, at: Date? = nil) {
            self.question = question
            self.options = options
            self.at = at
        }
    }

    public struct Result: Codable, Equatable, Sendable {
        public var summary: String?
        public var at: Date?
        public init(summary: String? = nil, at: Date? = nil) {
            self.summary = summary
            self.at = at
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

    public var id: String { paneID }

    enum CodingKeys: String, CodingKey {
        case alias, kind, name, state
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
        lastResult: Result? = nil
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
    }

    /// The one-line subtitle on a chip: the pending question wins, then the
    /// last result, then the current task.
    public func subtitle(pendingQuestion: String?) -> String? {
        if let q = pendingQuestion?.trimmed, !q.isEmpty { return q }
        if state == .blocked, let q = lastBlock?.question?.trimmed, !q.isEmpty { return q }
        if let r = lastResult?.summary?.trimmed, !r.isEmpty { return r }
        if let t = currentTask?.trimmed, !t.isEmpty { return t }
        return nil
    }
}

/// The single-slot pending action from `GET /api/state`.
public struct PendingAction: Codable, Equatable, Sendable {
    public var id: String
    public var paneID: String
    public var kind: String
    public var spokenSummary: String?
    public var question: String?
    public var options: [Agent.Block.Option]?

    enum CodingKeys: String, CodingKey {
        case id, kind, question, options
        case paneID = "pane_id"
        case spokenSummary = "spoken_summary"
    }

    public init(id: String, paneID: String, kind: String, spokenSummary: String? = nil, question: String? = nil, options: [Agent.Block.Option]? = nil) {
        self.id = id
        self.paneID = paneID
        self.kind = kind
        self.spokenSummary = spokenSummary
        self.question = question
        self.options = options
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

    enum CodingKeys: String, CodingKey {
        case agents, voice, herdr, version
        case pendingAction = "pending_action"
    }

    public init(agents: [Agent], pendingAction: PendingAction? = nil, voice: VoiceInfo, herdr: String? = nil, version: String? = nil) {
        self.agents = agents
        self.pendingAction = pendingAction
        self.voice = voice
        self.herdr = herdr
        self.version = version
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

    public var stringValue: String? {
        if case let .string(s) = self { return s }
        return nil
    }
}

extension String {
    var trimmed: String { trimmingCharacters(in: .whitespacesAndNewlines) }
}
