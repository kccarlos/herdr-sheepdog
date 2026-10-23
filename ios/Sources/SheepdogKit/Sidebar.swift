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
        public var spaces: [Space]
    }

    public var computers: [Computer]

    /// Computer rows show only once a second computer is paired.
    public var showsComputerHeaders: Bool { computers.count > 1 }

    /// Every agent in sidebar order: what ⌘1…9 jump to.
    public var agents: [Agent] { computers.flatMap { $0.spaces.flatMap(\.agents) } }

    public init(computers: [Computer]) {
        self.computers = computers
    }
}

extension SidebarSections.Computer {
    /// One computer's agents, grouped into spaces.
    public static func computer(id: String, name: String, agents: [Agent]) -> SidebarSections.Computer {
        let running = agents.filter { $0.state.isRunning }
        let groups = Dictionary(grouping: running) { $0.workspaceLabel?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "" }
        let spaces = groups
            .map { name, agents in
                SidebarSections.Space(name: name, agents: agents.sorted { $0.alias.localizedCaseInsensitiveCompare($1.alias) == .orderedAscending })
            }
            .sorted { lhs, rhs in
                // Agents with no space last.
                if lhs.name.isEmpty != rhs.name.isEmpty { return !lhs.name.isEmpty }
                return lhs.name.localizedCaseInsensitiveCompare(rhs.name) == .orderedAscending
            }
        return SidebarSections.Computer(id: id, name: name, spaces: spaces)
    }
}
