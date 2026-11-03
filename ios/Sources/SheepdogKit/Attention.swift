import Foundation

// Unread, pinned and muted (docs/dev/ws-contract.md "Attention", features
// `attention` and `space-mute`; design in docs/dev/design/unread-pinned-mute.md).
// The daemon computes each agent's group, unread and pinned once; the phone
// only orders them. A daemon without the feature sends none of it, and the
// phone then groups by state alone and offers no pin, unread or mute.

/// The list group of an agent, in the order the sections appear.
public enum AgentGroup: String, Codable, CaseIterable, Equatable, Sendable {
    case needsYou = "needs_you"
    case working
    case done
    case notRunning = "not_running"

    /// An unrecognised group from a newer daemon reads as `notRunning`, like
    /// an unknown state.
    public init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = AgentGroup(rawValue: raw) ?? .notRunning
    }

    /// The section header. Not-running agents are folded, not a section.
    public var title: String {
        switch self {
        case .needsYou: return "Needs you"
        case .working: return "Working"
        case .done: return "Done"
        case .notRunning: return "Not running"
        }
    }

    /// The group of an agent from a daemon that sends no `attention`: by
    /// state alone.
    public init(state: Agent.State) {
        switch state {
        case .blocked: self = .needsYou
        case .working: self = .working
        case .idle, .done: self = .done
        case .unknown, .exited: self = .notRunning
        }
    }
}

/// `agents[].attention` of `GET /api/state`.
public struct AgentAttention: Codable, Equatable, Sendable {
    public var group: AgentGroup
    /// A `done` agent that finished a turn nobody looked at.
    public var unread: Bool
    public var pinned: Bool
    /// The backend's count of finished turns, and the last one a person saw
    /// (absent when there is no record).
    public var turn: Int?
    public var seen: Int?
    /// The agent's space is muted.
    public var muted: Bool
    /// The marks are shared with every Herdr client (hc labels). False on
    /// the original herdr, where they are kept on that computer only.
    public var shared: Bool

    public init(group: AgentGroup, unread: Bool = false, pinned: Bool = false, turn: Int? = nil, seen: Int? = nil, muted: Bool = false, shared: Bool = true) {
        self.group = group
        self.unread = unread
        self.pinned = pinned
        self.turn = turn
        self.seen = seen
        self.muted = muted
        self.shared = shared
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        group = try c.decodeIfPresent(AgentGroup.self, forKey: .group) ?? .notRunning
        unread = try c.decodeIfPresent(Bool.self, forKey: .unread) ?? false
        pinned = try c.decodeIfPresent(Bool.self, forKey: .pinned) ?? false
        turn = try c.decodeIfPresent(Int.self, forKey: .turn)
        seen = try c.decodeIfPresent(Int.self, forKey: .seen)
        muted = try c.decodeIfPresent(Bool.self, forKey: .muted) ?? false
        shared = try c.decodeIfPresent(Bool.self, forKey: .shared) ?? true
    }

    /// The daemon's `attention.Classify`, for the offline demo, which is its
    /// own daemon. A real daemon computes this; the app only reads it.
    /// `word` is the backend's own word (`done` or `idle`), which decides
    /// unread when there is no `seen` record; `error` is hc's status word
    /// `error`. Checked against the shared fixture's `classify` cases.
    public static func classify(state: Agent.State, turn: Int, seen: Int?, word: String, error: Bool = false,
                                pinned: Bool = false, muted: Bool = false) -> AgentAttention {
        var group = AgentGroup.notRunning
        var unread = false
        if state == .blocked || error {
            group = .needsYou
        } else if state == .working {
            group = .working
        } else if state == .idle || state == .done {
            group = .done
            if let seen { unread = turn > seen } else { unread = word == "done" }
        }
        return AgentAttention(group: group, unread: unread, pinned: pinned, turn: turn, seen: seen, muted: muted)
    }

    /// A turn nobody looked at is finished: the phone has something to mark
    /// seen. True also with no record, so the first look writes one.
    public var hasUnseenTurn: Bool {
        guard let turn else { return false }
        guard let seen else { return true }
        return turn > seen
    }
}

/// `attention_counts` of `GET /api/state`. The badges count `needsYou`
/// only: unread done agents show as a dot (product default, design Q3).
public struct AttentionCounts: Codable, Equatable, Sendable {
    public var needsYou: Int
    public var working: Int
    public var doneUnread: Int

    enum CodingKeys: String, CodingKey {
        case working
        case needsYou = "needs_you"
        case doneUnread = "done_unread"
    }

    public init(needsYou: Int = 0, working: Int = 0, doneUnread: Int = 0) {
        self.needsYou = needsYou
        self.working = working
        self.doneUnread = doneUnread
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        needsYou = try c.decodeIfPresent(Int.self, forKey: .needsYou) ?? 0
        working = try c.decodeIfPresent(Int.self, forKey: .working) ?? 0
        doneUnread = try c.decodeIfPresent(Int.self, forKey: .doneUnread) ?? 0
    }
}

/// One entry of `space_mutes` in `GET /api/state`: a muted space folder.
public struct SpaceMute: Codable, Equatable, Sendable {
    public var path: String
    public var name: String?
    /// When it ends; nil means until the user unmutes.
    public var until: Date?

    public init(path: String, name: String? = nil, until: Date? = nil) {
        self.path = path
        self.name = name
        self.until = until
    }
}

extension Agent {
    /// The group the list puts the agent in: the daemon's, else by state.
    public var group: AgentGroup { attention?.group ?? AgentGroup(state: state) }
    public var isUnread: Bool { attention?.unread ?? false }
    public var isPinned: Bool { attention?.pinned ?? false }
    public var isMuted: Bool { attention?.muted ?? false }
    /// This daemon keeps seen and pinned marks: the pin and unread controls
    /// are offered only then.
    public var hasAttention: Bool { attention != nil }
}

/// The agent list's order: groups in order, pinned first within a group,
/// unread first within Done, then by spoken name without regard to case.
/// There is no time sort, so rows never jump while a person looks at them.
/// The same rule as the daemon's `attention.Compare`, tested against the
/// shared `Tests/Fixtures/agent-attention.json`.
public enum AgentOrder {
    public static func sorted(_ agents: [Agent]) -> [Agent] {
        agents.sorted { areInIncreasingOrder($0, $1) }
    }

    public static func areInIncreasingOrder(_ a: Agent, _ b: Agent) -> Bool {
        let ga = a.group, gb = b.group
        if ga != gb { return rank(ga) < rank(gb) }
        if a.isPinned != b.isPinned { return a.isPinned }
        if ga == .done, a.isUnread != b.isUnread { return a.isUnread }
        let na = key(a.displayName), nb = key(b.displayName)
        if na != nb { return na.lexicographicallyPrecedes(nb) }
        // Rows that tie on everything keep a stable place.
        return a.paneID < b.paneID
    }

    private static func rank(_ group: AgentGroup) -> Int {
        AgentGroup.allCases.firstIndex(of: group) ?? AgentGroup.allCases.count
    }

    /// Bytes of the lowercased name, which is how the daemon compares.
    private static func key(_ name: String) -> [UInt8] { Array(name.lowercased().utf8) }
}

/// One section of the list: a group and its agents in order.
public struct FlockSection: Equatable, Identifiable, Sendable {
    public var group: AgentGroup
    public var agents: [Agent]
    public var id: AgentGroup { group }
    public var title: String { group.title }
}
