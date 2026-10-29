import Foundation

/// How the app reaches one computer that is not the current one: its
/// `/api/state`, polled and nudged by its `/events` (app-redesign.md
/// section 4, option A).
public enum Reachability: Equatable, Sendable {
    /// Not heard from yet.
    case connecting
    case online
    /// It stopped answering; `since` is when it last did (nil: never).
    case offline(since: Date?)

    public var isOnline: Bool { self == .online }

    /// A computer's, read from its link: the one source that knows. A blip
    /// inside the quiet period is still "connecting"; past it, and for
    /// anything a retry does not fix by itself, it is offline since the
    /// last time anything succeeded.
    public init(_ link: ComputerLink) {
        switch link.state {
        case .online, .degraded:
            self = .online
        case .idle, .waiting:
            self = .connecting
        case let .connecting(_, outage):
            self = outage?.long == true ? .offline(since: link.lastSeen) : .connecting
        case .offline, .noNetwork, .untrustedAnswer, .refused, .incompatible:
            self = .offline(since: link.lastSeen)
        }
    }
}

/// What a computer section's voice marker shows.
public enum ComputerVoiceMarkerState: Equatable, Sendable {
    /// With one computer there is no computer section or marker.
    case hidden
    /// The voice is on this computer.
    case voice
    /// Tapping moves the voice to this computer.
    case available
    /// Tapping retries this computer.
    case offline

    /// The marker is a single choice: a computer that does not answer is
    /// Retry (the current one too, since the voice cannot reach it), else
    /// the current computer holds the voice and the rest can take it.
    public static func resolve(
        computerID: String,
        currentComputerID: String,
        reachability: Reachability,
        showsComputers: Bool
    ) -> Self {
        guard showsComputers else { return .hidden }
        if case .offline = reachability { return .offline }
        return computerID == currentComputerID ? .voice : .available
    }
}

/// One paired computer as the several-computers UI shows it: the Talk to
/// rows, the Agents page's sections, the iPad sidebar and Settings ▸
/// Computers. Each comes from its computer's `ComputerModel`; the current
/// one's link from the session.
public struct ComputerSnapshot: Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var isCurrent: Bool
    public var reachability: Reachability
    public var agents: [Agent]
    public var questions: [PendingAction]
    /// Talk to may switch here now (`ComputerModel.canTalkTo`): a computer
    /// that is backing off is not switched to, even inside the quiet period
    /// that still reads "Connecting…".
    public var canTalkTo: Bool

    public init(id: String, name: String, isCurrent: Bool, reachability: Reachability, agents: [Agent] = [], questions: [PendingAction] = [], canTalkTo: Bool? = nil) {
        self.id = id
        self.name = name
        self.isCurrent = isCurrent
        self.reachability = reachability
        self.agents = agents
        self.questions = questions
        if let canTalkTo {
            self.canTalkTo = canTalkTo
        } else if case .offline = reachability {
            self.canTalkTo = false
        } else {
            self.canTalkTo = true
        }
    }

    public var running: [Agent] { agents.filter { $0.state.isRunning } }
    public var waitingCount: Int { running.filter { $0.state == .blocked }.count }

    /// "4 agents · 1 waiting", "1 agent", "No agents", "Offline since 9:12",
    /// "Connecting…".
    public func summary(now: Date = Date(), calendar: Calendar = .current) -> String {
        switch reachability {
        case .connecting:
            return "Connecting…"
        case let .offline(since):
            guard let since else { return "Offline" }
            return "Offline since \(Self.time(since, now: now, calendar: calendar))"
        case .online:
            let n = running.count
            guard n > 0 else { return "No agents" }
            let agents = n == 1 ? "1 agent" : "\(n) agents"
            return waitingCount > 0 ? "\(agents) · \(waitingCount) waiting" : agents
        }
    }

    /// "Last seen 9:12." for an offline computer that answered before, nil
    /// otherwise.
    public func lastSeen(now: Date = Date(), calendar: Calendar = .current) -> String? {
        guard case let .offline(since?) = reachability else { return nil }
        return "Last seen \(Self.time(since, now: now, calendar: calendar))."
    }

    /// "9:12" today, else "Mon 9:12".
    static func time(_ date: Date, now: Date, calendar: Calendar) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.calendar = calendar
        f.timeZone = calendar.timeZone
        f.dateFormat = calendar.isDate(date, inSameDayAs: now) ? "H:mm" : "EEE H:mm"
        return f.string(from: date)
    }
}

/// A question from another computer, as the floating stack and the
/// Agents page carry it: the computer, the agent's space, and an id that
/// cannot collide with the current computer's (question ids are only
/// unique within one daemon).
public struct RemoteQuestion: Equatable, Sendable {
    public var computerID: String
    public var computerName: String
    /// The agent's space ("blog"), else the agent's name.
    public var place: String
    public var pending: PendingAction

    public init(computerID: String, computerName: String, place: String, pending: PendingAction) {
        self.computerID = computerID
        self.computerName = computerName
        self.place = place
        self.pending = pending
    }

    /// "Home Mac · blog".
    public var label: String { place.isEmpty ? computerName : "\(computerName) · \(place)" }

    /// The question with its id made unique across computers.
    public var namespaced: PendingAction {
        var p = pending
        p.id = Self.id(computer: computerID, question: pending.id)
        return p
    }

    static let prefix = "remote:"

    public static func id(computer: String, question: String) -> String { "\(prefix)\(computer)|\(question)" }

    /// The computer and the daemon's own id of a namespaced question id;
    /// nil for the current computer's.
    public static func parse(_ id: String) -> (computer: String, question: String)? {
        guard id.hasPrefix(prefix) else { return nil }
        let rest = id.dropFirst(prefix.count)
        guard let bar = rest.firstIndex(of: "|") else { return nil }
        return (String(rest[rest.startIndex ..< bar]), String(rest[rest.index(after: bar)...]))
    }
}

extension QuestionOrder {
    /// The other computers' questions after the current one's, oldest
    /// first across all of them (a question without a time goes last, in
    /// the order it came).
    public static func others(_ questions: [RemoteQuestion]) -> [RemoteQuestion] {
        questions.enumerated().sorted { a, b in
            switch (a.element.pending.createdAt, b.element.pending.createdAt) {
            case let (x?, y?) where x != y: return x < y
            case (_?, nil): return true
            case (nil, _?): return false
            default: return a.offset < b.offset
            }
        }.map(\.element)
    }
}
