import Foundation

// What the phone writes for unread, pinned and muted (docs/dev/ws-contract.md
// "Attention"): the requests, the mute times, the optimistic overlay and the
// rule for when an agent counts as looked at.

/// How long a space mute lasts, as the mute menu offers it. Product defaults
/// (design question Q4); the daemon's `attention.MuteForHour` and
/// `attention.UntilTomorrowMorning` are the same two rules, and each lives
/// here in one place.
public enum MuteChoice: String, CaseIterable, Identifiable, Sendable {
    case hour, tomorrowMorning, untilOff

    public var id: String { rawValue }

    /// The local hour "Until tomorrow morning" ends.
    public static let morningHour = 8
    /// "For 1 hour".
    public static let hourLength: TimeInterval = 3600

    public var title: String {
        switch self {
        case .hour: return "For 1 hour"
        case .tomorrowMorning: return "Until tomorrow morning"
        case .untilOff: return "Until I turn them back on"
        }
    }

    /// When the mute ends, or nil for "until I turn them back on".
    /// "Tomorrow morning" is the next 08:00 strictly after `now` in
    /// `calendar`'s time zone: asked at 07:00 it ends in an hour, asked at
    /// 09:00 the next day. It follows the wall clock, so a day with a
    /// daylight-saving change is 23 or 25 hours long, and it is never
    /// `now + 86400`.
    public func until(from now: Date, calendar: Calendar = .current) -> Date? {
        switch self {
        case .hour:
            return now.addingTimeInterval(MuteChoice.hourLength)
        case .tomorrowMorning:
            let morning = calendar.date(bySettingHour: MuteChoice.morningHour, minute: 0, second: 0, of: now, matchingPolicy: .nextTime)
            if let morning, morning > now { return morning }
            let tomorrow = morning.flatMap { calendar.date(byAdding: .day, value: 1, to: $0) }
            return tomorrow.flatMap { calendar.date(bySettingHour: MuteChoice.morningHour, minute: 0, second: 0, of: $0, matchingPolicy: .nextTime) }
        case .untilOff:
            return nil
        }
    }

    /// The instant the API takes: RFC 3339, whole seconds, UTC.
    public static func rfc3339(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.string(from: date)
    }

    /// "Muted until 8:00 AM", "Muted until Wed 8:00 AM": the line under a
    /// muted space.
    public static func status(until: Date?, now: Date, calendar: Calendar = .current, locale: Locale = .current) -> String {
        guard let until else { return "Muted until you turn it back on" }
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.calendar = calendar
        formatter.timeZone = calendar.timeZone
        formatter.setLocalizedDateFormatFromTemplate(calendar.isDate(until, inSameDayAs: now) ? "jm" : "EEEjm")
        return "Muted until \(formatter.string(from: until))"
    }
}

/// The reply of `seen` and `pin`: the agent's marks as the daemon has them.
public struct AttentionReply: Decodable, Equatable, Sendable {
    public var attention: AgentAttention?
}

extension DaemonClient {
    /// `POST /api/agents/{pane}/seen {"turn": n}`: the phone showed this
    /// turn. The daemon raises the mark only (never down, never above the
    /// current turn).
    @discardableResult
    public func markSeen(paneID: String, turn: Int) async throws -> AgentAttention? {
        try await postAttention(DaemonClient.agentPath(paneID: paneID, suffix: "seen"), ["turn": turn])
    }

    /// `POST /api/agents/{pane}/seen {"unread": true}`: Mark as unread.
    @discardableResult
    public func markUnread(paneID: String) async throws -> AgentAttention? {
        try await postAttention(DaemonClient.agentPath(paneID: paneID, suffix: "seen"), ["unread": true])
    }

    /// `POST /api/agents/{pane}/pin {"pinned": bool}`.
    @discardableResult
    public func setPinned(paneID: String, _ pinned: Bool) async throws -> AgentAttention? {
        try await postAttention(DaemonClient.agentPath(paneID: paneID, suffix: "pin"), ["pinned": pinned])
    }

    /// `POST /api/spaces/{id}/mute`. `until` (the instant it ends) goes with
    /// `muted: true` only; nil there means until the user unmutes.
    public func setSpaceMute(spaceID: String, muted: Bool, until: Date? = nil) async throws {
        var body: [String: Any] = ["muted": muted]
        if muted, let until { body["until"] = MuteChoice.rfc3339(until) }
        _ = try await postAttention(DaemonClient.spacePath(spaceID, "mute"), body)
    }

    private func postAttention(_ path: String, _ body: [String: Any]) async throws -> AgentAttention? {
        let data = try JSONSerialization.data(withJSONObject: body)
        let request = try request(encodedPath: path, method: "POST", body: data)
        let (reply, response) = try await perform(request)
        try checkSpace(data: reply, response: response)
        return (try? DaemonClient.decoder.decode(AttentionReply.self, from: reply))?.attention
    }
}

extension DaemonError {
    /// The daemon does not keep these marks or mutes: an older one (no such
    /// route) or one without the feature (`503`). Not a failure to show.
    public var meansFeatureAbsent: Bool {
        if case let .rejected(status, _) = self { return status == 503 || status == 404 }
        return false
    }
}

/// What the screen shows between a tap and the daemon's state catching up:
/// the pin, seen, unread or mute the user just set. An entry goes when
/// `/api/state` shows it, when 5 s pass, or when its write fails. Two
/// phones converge on the daemon's answer.
public struct AttentionOverlay: Equatable, Sendable {
    public static let lifetime: TimeInterval = 5

    public enum Field: Sendable { case pinned, seen, unread, muted }

    private struct Entry: Equatable, Sendable {
        var pinned: Bool?
        var pinnedAt = Date.distantPast
        var seen: Int?
        var seenAt = Date.distantPast
        var unreadAt: Date?
        var muted: Bool?
        var mutedAt = Date.distantPast

        var isEmpty: Bool { pinned == nil && seen == nil && unreadAt == nil && muted == nil }
    }

    private var entries: [String: Entry] = [:]

    public init() {}

    public var isEmpty: Bool { entries.isEmpty }

    public mutating func setPinned(_ paneID: String, _ value: Bool, at now: Date) {
        entries[paneID, default: Entry()].pinned = value
        entries[paneID]?.pinnedAt = now
    }

    public mutating func markSeen(_ paneID: String, turn: Int, at now: Date) {
        entries[paneID, default: Entry()].seen = turn
        entries[paneID]?.seenAt = now
        entries[paneID]?.unreadAt = nil
    }

    public mutating func markUnread(_ paneID: String, at now: Date) {
        entries[paneID, default: Entry()].unreadAt = now
        entries[paneID]?.seen = nil
    }

    public mutating func setMuted(_ paneIDs: [String], _ value: Bool, at now: Date) {
        for paneID in paneIDs {
            entries[paneID, default: Entry()].muted = value
            entries[paneID]?.mutedAt = now
        }
    }

    /// The write failed: forget the change so the row goes back.
    public mutating func rollback(_ paneID: String, _ field: Field) {
        switch field {
        case .pinned: entries[paneID]?.pinned = nil
        case .seen: entries[paneID]?.seen = nil
        case .unread: entries[paneID]?.unreadAt = nil
        case .muted: entries[paneID]?.muted = nil
        }
        if entries[paneID]?.isEmpty == true { entries[paneID] = nil }
    }

    private func live(_ at: Date, _ now: Date) -> Bool { now.timeIntervalSince(at) < Self.lifetime }

    /// The agents as the user just left them, over the daemon's state.
    /// An agent without `attention` (an older daemon) is left alone.
    public func applying(to agents: [Agent], now: Date) -> [Agent] {
        guard !entries.isEmpty else { return agents }
        return agents.map { agent in
            guard var attention = agent.attention, let entry = entries[agent.paneID] else { return agent }
            if let pinned = entry.pinned, live(entry.pinnedAt, now) { attention.pinned = pinned }
            if let muted = entry.muted, live(entry.mutedAt, now) { attention.muted = muted }
            if let turn = entry.seen, live(entry.seenAt, now) {
                attention.seen = max(attention.seen ?? turn, turn)
                if attention.group == .done, let current = attention.turn, (attention.seen ?? 0) >= current { attention.unread = false }
            }
            if let at = entry.unreadAt, live(at, now), attention.group == .done, let current = attention.turn, current > 0 {
                attention.unread = true
                attention.seen = min(attention.seen ?? current, current - 1)
            }
            var copy = agent
            copy.attention = attention
            return copy
        }
    }

    /// Drops what the daemon's state now shows, what has run its 5 s, and
    /// what belongs to an agent that is gone.
    public mutating func settle(with agents: [Agent], now: Date) {
        let byPane = Dictionary(agents.map { ($0.paneID, $0) }, uniquingKeysWith: { first, _ in first })
        for (paneID, var entry) in entries {
            guard let attention = byPane[paneID]?.attention else { entries[paneID] = nil; continue }
            if let pinned = entry.pinned, pinned == attention.pinned || !live(entry.pinnedAt, now) { entry.pinned = nil }
            if let muted = entry.muted, muted == attention.muted || !live(entry.mutedAt, now) { entry.muted = nil }
            if let seen = entry.seen, (attention.seen ?? -1) >= seen || !live(entry.seenAt, now) { entry.seen = nil }
            if let at = entry.unreadAt, attention.unread || !live(at, now) { entry.unreadAt = nil }
            entries[paneID] = entry.isEmpty ? nil : entry
        }
    }
}

/// When an agent counts as looked at, and what to write for it: the app is
/// in front and the agent's window is open or its card expanded. It writes
/// once per finished turn, so a window left open marks each new turn seen
/// as it ends, and nothing is written from the background.
public struct SeenTracker: Equatable, Sendable {
    /// The newest turn written (or being written) for each pane.
    private var written: [String: Int] = [:]

    public init() {}

    public struct Write: Equatable, Sendable {
        public var paneID: String
        public var turn: Int
    }

    /// The writes to make now for `visible` agents (those on screen).
    public mutating func writes(visible agents: [Agent], appActive: Bool) -> [Write] {
        guard appActive else { return [] }
        var out: [Write] = []
        for agent in agents {
            guard let attention = agent.attention, attention.hasUnseenTurn, let turn = attention.turn else { continue }
            if let done = written[agent.paneID], done >= turn { continue }
            written[agent.paneID] = turn
            out.append(Write(paneID: agent.paneID, turn: turn))
        }
        return out
    }

    /// The write failed: try again on the next look.
    public mutating func failed(_ write: Write) {
        if written[write.paneID] == write.turn { written[write.paneID] = nil }
    }

    /// The user marked the agent unread: the next look writes again.
    public mutating func forget(_ paneID: String) {
        written[paneID] = nil
    }
}

/// How the live events concern attention.
public enum AttentionEvents {
    /// `agent.attention` and `space.muted` mean the state moved: clients
    /// re-read `/api/state` (debounced, as for `herdr.transition`).
    public static func refreshesState(_ type: String) -> Bool {
        type == "agent.attention" || type == "space.muted"
    }

    /// A question or finish in a muted space does not buzz the phone
    /// (`muted: true` on `pending.created` and `herdr.transition`).
    public static func isMuted(_ event: DaemonEvent) -> Bool {
        event.bool("muted") == true
    }
}
