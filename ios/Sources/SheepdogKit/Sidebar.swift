import Foundation

/// The iPad sidebar's agent list (docs/dev/design/app-redesign.md section
/// 6): computer → space → agent. Pure, so the one-computer rule is
/// unit-tested: with one computer there is no computer row, and the spaces
/// sit at the top level.
///
/// Running agents only. Spaces are in name order and agents in alias
/// order inside them, so a row keeps its place (and its ⌘1…9) while
/// states change; the hand icon shows who waits.
public struct SidebarSections: Equatable, Sendable {
    public struct Space: Equatable, Sendable, Identifiable {
        /// herdr's space label; "" when the daemon sent none.
        public var name: String
        public var agents: [Agent]
        public var id: String { name }
    }

    public struct Computer: Equatable, Sendable, Identifiable {
        public var id: String
        public var name: String
        public var factoryProjects: [FactoryAgentsList.Item]
        public var finishedFactoryCount: Int
        public var spaces: [Space]

        public init(id: String, name: String, spaces: [Space],
                    factoryProjects: [FactoryAgentsList.Item] = [], finishedFactoryCount: Int = 0) {
            self.id = id
            self.name = name
            self.factoryProjects = factoryProjects
            self.finishedFactoryCount = finishedFactoryCount
            self.spaces = spaces
        }

        private var factoryCountSummary: String? {
            switch (factoryProjects.count, finishedFactoryCount) {
            case (let a, let d) where a > 0 && d > 0: return "\(a), and \(d) done"
            case (let a, _) where a > 0: return "\(a)"
            case (_, let d) where d > 0: return "\(d) done"
            default: return nil
            }
        }

        public var factorySectionDetail: String? { factoryCountSummary.map { "· \($0)" } }
        public var factorySectionAccessibilityLabel: String? { factoryCountSummary.map { "Factory projects, \($0)" } }
        public var hasContent: Bool { !spaces.isEmpty || !factoryProjects.isEmpty || finishedFactoryCount > 0 }
    }

    public var computers: [Computer]

    /// Computer rows show only once a second computer is paired.
    public var showsComputerHeaders: Bool { computers.count > 1 }

    /// Every agent in sidebar order: what ⌘1…9 jump to.
    public var agents: [Agent] { computers.flatMap { $0.spaces.flatMap(\.agents) } }

    public init(computers: [Computer]) {
        self.computers = computers
    }

    /// Every paired computer in the store's order (the order they were
    /// paired, or the user's), whichever one is current: Talk to never
    /// moves a section. The current computer shows its own agents, the
    /// others what they last reported (none while offline). A current
    /// computer with no snapshot (a loopback fixture, nothing paired) goes
    /// first.
    public init(snapshots: [ComputerSnapshot], currentID: String, currentName: String, currentAgents: [Agent],
                currentFactoryHeads: [FactoryProjectHead]? = nil, currentQuestions: [PendingAction]? = nil) {
        var computers = snapshots.map { s -> Computer in
            if s.id == currentID {
                return .computer(id: s.id, name: s.name, agents: currentAgents,
                                 factoryHeads: currentFactoryHeads ?? s.factoryHeads,
                                 questions: currentQuestions ?? s.questions)
            }
            guard s.reachability.isOnline else {
                return .computer(id: s.id, name: s.name, agents: [])
            }
            return .computer(id: s.id, name: s.name, agents: s.agents,
                             factoryHeads: s.factoryHeads, questions: s.questions)
        }
        if !snapshots.contains(where: { $0.id == currentID }) {
            let current = Computer.computer(id: currentID, name: currentName, agents: currentAgents,
                                            factoryHeads: currentFactoryHeads ?? [], questions: currentQuestions ?? [])
            computers.insert(current, at: 0)
        }
        self.computers = computers
    }
}

extension SidebarSections.Computer {
    /// One computer's active factory projects and ordinary agents grouped into spaces.
    public static func computer(id: String, name: String, agents: [Agent],
                                factoryHeads: [FactoryProjectHead] = [],
                                questions: [PendingAction] = []) -> SidebarSections.Computer {
        let list = FactoryAgentsList(agents: agents, heads: factoryHeads, questions: questions)
        let groups = Dictionary(grouping: list.agents.running) {
            $0.workspaceLabel?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        }
        let spaces = groups
            .map { name, agents in
                SidebarSections.Space(name: name, agents: agents.sorted { $0.alias.localizedCaseInsensitiveCompare($1.alias) == .orderedAscending })
            }
            .sorted { lhs, rhs in
                // Agents with no space last.
                if lhs.name.isEmpty != rhs.name.isEmpty { return !lhs.name.isEmpty }
                return lhs.name.localizedCaseInsensitiveCompare(rhs.name) == .orderedAscending
            }
        return SidebarSections.Computer(id: id, name: name, spaces: spaces,
                                        factoryProjects: list.items.filter { $0.head.state != .finished },
                                        finishedFactoryCount: list.finishedHeads.count)
    }
}
