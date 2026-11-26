import Foundation

public struct FactoryAgentsList: Equatable, Sendable {
    public struct Item: Equatable, Identifiable, Sendable {
        public let head: FactoryProjectHead
        public let coordinatorDialog: PendingAction?
        public let coordinatorBlocked: Bool

        public var id: String { head.id }
        public var kindWord: String { "factory" }
        public var stateWord: String { head.state.rawValue }
        public var waitingOnYouCount: Int { head.counts["owner_answer", default: 0] }
        public var countsLine: String {
            let total = head.counts.values.reduce(0, +)
            guard total > 0 else { return "no tasks yet" }
            let working = head.counts["working", default: 0]
            return [
                working > 0 ? "\(working) working" : nil,
                waitingOnYouCount > 0 ? "\(waitingOnYouCount) waiting on you" : nil,
                "\(head.counts["done", default: 0]) of \(total) done"
            ].compactMap { $0 }.joined(separator: " · ")
        }

        public var alertLine: String? {
            guard let alert = head.alert else { return nil }
            return alert.kind == "agents_down" ? "The \(head.name) coordinator is not running: \(alert.cause)" : alert.cause
        }
        public var alertFixLine: String? { head.alert?.fix.map { "Fix: \($0)" } }
        public var stuckHeading: String { "The \(head.name) coordinator is stuck on a prompt" }
        public var stuckNoHint: String {
            "To say what to do instead, use the box on the \(head.name) page. The coordinator gets it when it is next free."
        }
        public var accessibilityLabel: String {
            ["\(head.name) \(kindWord)", head.goal, stateWord, countsLine, alertLine, alertFixLine,
             coordinatorBlocked ? "the coordinator is stuck on a prompt" : nil]
                .compactMap { $0 }.joined(separator: ", ")
        }
    }

    public let agents: Flock
    private let items: [Item]
    public let factoryAsks: [PendingAction]

    public init(agents: [Agent], heads: [FactoryProjectHead], questions: [PendingAction]) {
        let coordinators = Set(heads.compactMap(\.coordinatorPane))
        let blocked = Set(agents.filter { $0.state == .blocked }.map(\.paneID))
        self.agents = Flock(agents.filter { !$0.isFactoryAgent && !coordinators.contains($0.paneID) })
        factoryAsks = questions.filter { $0.factory != nil }
        items = heads.map { head in
            let pane = head.coordinatorPane
            let dialog = questions.first { $0.paneID == pane && $0.factory == nil }
            let stuck = head.state == .running && (dialog != nil || pane.map(blocked.contains) == true)
            return Item(head: head, coordinatorDialog: stuck ? dialog : nil, coordinatorBlocked: stuck)
        }
    }

    public var needsYouHeads: [Item] { items.filter(\.coordinatorBlocked) }
    public var workingHeads: [Item] {
        items.filter { ($0.head.state == .running || $0.head.state == .unknown) && !$0.coordinatorBlocked }
    }
    public var stoppedHeads: [Item] { items.filter { $0.head.state == .stopped } }
    public var finishedHeads: [Item] { items.filter { $0.head.state == .finished } }
    public var hasStoppedSection: Bool { !stoppedHeads.isEmpty }
    public var waitingCount: Int { agents.waitingCount + needsYouHeads.count + factoryAsks.count }
    public var hasFactoryContent: Bool { !items.isEmpty || !factoryAsks.isEmpty }
    public var isEmpty: Bool { agents.isEmpty && !hasFactoryContent }
}

public struct FactoryAskCardPresentation: Equatable, Sendable {
    public let headerLine: String
    public let overrideFooter: String?
    public let accessibilityLabel: String

    public init?(_ pending: PendingAction) {
        guard let factory = pending.factory else { return nil }
        let name = pending.agent ?? factory.project
        var heading = [name] + [factory.task].compactMap { $0 }
        var spoken = heading
        switch factory.kind {
        case "delete", "release": heading.append("the coordinator asks")
        case "rule": heading += ["setting", "the engine asks"]; spoken.append("a setting")
        case "override": heading.append("override"); spoken.append("override")
        default: break
        }
        headerLine = heading.joined(separator: " · ")
        accessibilityLabel = spoken.count == 1 ? "\(name) needs your answer" : (spoken + ["needs your answer"]).joined(separator: ", ")
        overrideFooter = factory.kind == "override"
            ? "Only you can answer this. A conflict, or a commit newer than this card, is not merged."
            : nil
    }
}
