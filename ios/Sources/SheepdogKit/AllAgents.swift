import Foundation

/// iPad ▸ All agents: the filter over the running agents.
public enum AgentFilter: String, CaseIterable, Identifiable, Sendable {
    case all, waiting, working

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .all: return "All"
        case .waiting: return "Waiting"
        case .working: return "Working"
        }
    }

    public func matches(_ agent: Agent) -> Bool {
        switch self {
        case .all: return true
        case .waiting: return agent.state == .blocked
        case .working: return agent.state == .working
        }
    }

    /// What the page says when the filter leaves nothing.
    public var emptyText: String {
        switch self {
        case .all: return "No agents running."
        case .waiting: return "No agent is waiting on you."
        case .working: return "No agent is working right now."
        }
    }

    /// The cards in two columns, left to right then down, so the order
    /// reads the same as the list.
    public static func columns<T>(_ items: [T]) -> ([T], [T]) {
        var left: [T] = []
        var right: [T] = []
        for (index, item) in items.enumerated() {
            if index.isMultiple(of: 2) { left.append(item) } else { right.append(item) }
        }
        return (left, right)
    }
}

extension Agent {
    /// The worker's messages before the newest one (`lastWorkerMessage`),
    /// newest first: the expanded card's "Earlier" list on iPad.
    public func earlierWorkerMessages(limit: Int = 3) -> [JournalEntry] {
        let written = (journal ?? []).filter { !($0.lastAssistant?.trimmed.isEmpty ?? true) }
        guard written.count > 1 else { return [] }
        return Array(written.dropLast().reversed().prefix(limit))
    }
}
