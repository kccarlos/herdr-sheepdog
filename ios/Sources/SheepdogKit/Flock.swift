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
}

/// The agents as the screen shows it: the running agents in the order that
/// matters (needs you, working, then done; pinned first within a group and
/// unread first within Done; see `AgentOrder`), and the rest folded away.
/// A blocked agent is always running, so it can never be folded.
public struct Flock: Equatable, Sendable {
    public private(set) var running: [Agent]
    public private(set) var notRunning: [Agent]

    public init(_ agents: [Agent]) {
        let sorted = Flock.sorted(agents)
        running = sorted.filter { $0.group != .notRunning }
        notRunning = sorted.filter { $0.group == .notRunning }
    }

    /// Every agent, running ones first, in display order.
    public var all: [Agent] { running + notRunning }

    /// The running agents under their headers: Needs you, Working, Done.
    /// An empty section is left out.
    public var sections: [FlockSection] {
        [AgentGroup.needsYou, .working, .done].compactMap { group in
            let members = running.filter { $0.group == group }
            return members.isEmpty ? nil : FlockSection(group: group, agents: members)
        }
    }

    /// Agents waiting on the user: what the Agents pane tab's badge counts.
    /// Blocked only: unread done agents show as a dot, not in the badge.
    public var waitingCount: Int { running.filter { $0.state == .blocked }.count }

    public var isEmpty: Bool { running.isEmpty && notRunning.isEmpty }

    public static func sorted(_ agents: [Agent]) -> [Agent] {
        AgentOrder.sorted(agents)
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

/// Whether an agent strip chip names a computer (app-redesign.md 1.1):
/// only a blocked agent on another computer, and only once a second
/// computer is paired. With one computer the strip looks as it always did.
public enum AgentStripLabel {
    public static func computer(state: Agent.State, agentComputer: String?, pairedComputers: Int) -> String? {
        guard pairedComputers > 1, state == .blocked,
              let name = agentComputer?.trimmingCharacters(in: .whitespacesAndNewlines), !name.isEmpty
        else { return nil }
        return name
    }
}
