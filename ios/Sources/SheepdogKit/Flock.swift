import Foundation

extension Agent.State {
    /// The agent's pane is alive in herdr: it is working, waiting on you,
    /// sitting idle or finished. `unknown` and `exited` are panes herdr no
    /// longer reports on, or that closed — not running.
    public var isRunning: Bool {
        switch self {
        case .idle, .working, .blocked, .done: return true
        case .unknown, .exited: return false
        }
    }

    /// Where the agent sorts: the ones waiting on a person first, then the
    /// busy ones, then the quiet ones, then what is not running.
    public var flockRank: Int {
        switch self {
        case .blocked: return 0
        case .working: return 1
        case .idle, .done: return 2
        case .unknown, .exited: return 3
        }
    }
}

/// The flock as the screen shows it: the running agents in the order that
/// matters (blocked, working, then idle and done; by alias within each),
/// and the rest folded away. A blocked agent is always running, so it can
/// never be folded.
public struct Flock: Equatable, Sendable {
    public private(set) var running: [Agent]
    public private(set) var notRunning: [Agent]

    public init(_ agents: [Agent]) {
        let sorted = Flock.sorted(agents)
        running = sorted.filter { $0.state.isRunning }
        notRunning = sorted.filter { !$0.state.isRunning }
    }

    /// Every agent, running ones first, in display order.
    public var all: [Agent] { running + notRunning }

    /// Agents waiting on the shepherd: what the Flock tab's badge counts.
    public var waitingCount: Int { running.filter { $0.state == .blocked }.count }

    public var isEmpty: Bool { running.isEmpty && notRunning.isEmpty }

    public static func sorted(_ agents: [Agent]) -> [Agent] {
        agents.sorted { lhs, rhs in
            if lhs.state.flockRank != rhs.state.flockRank { return lhs.state.flockRank < rhs.state.flockRank }
            return lhs.alias.localizedCaseInsensitiveCompare(rhs.alias) == .orderedAscending
        }
    }
}

extension Agent {
    /// The chip's one line. A blocked agent leads with what it needs, in a
    /// short verb, so the cut-off part is the detail and not the point:
    /// "Needs approval · git push origin main", "Needs you · which branch?"
    /// (review B2). Anyone else: `subtitle(pendingQuestion:)`.
    public func chipSubtitle(pending: PendingAction?) -> String? {
        let mine = pending.flatMap { $0.paneID == paneID ? $0 : nil }
        guard state == .blocked else { return subtitle(pendingQuestion: mine?.question ?? mine?.spokenSummary) }
        if let mine, mine.isAnswerPrompt {
            let detail = [mine.command, mine.question, mine.summary].compactMap { $0?.trimmed }.first { !$0.isEmpty }
            return "Needs approval" + (detail.map { " · \($0)" } ?? "")
        }
        let question = [mine?.question, mine?.spokenSummary, lastBlock?.answered == true ? nil : lastBlock?.question]
            .compactMap { $0?.trimmed }.first { !$0.isEmpty }
        return "Needs you" + (question.map { " · \($0)" } ?? "")
    }
}
