import Foundation

/// Layered task dependency graph for the iPad factory board (`web/factory/graph.js`,
/// `FactoryIPadBoard.dc.html`, `slice14-wording.md` `gr.node`).
public struct FactoryTaskGraph: Equatable, Sendable {
    public enum Tone: String, Equatable, Sendable {
        case done, working, blocked, idle
    }

    public struct Node: Equatable, Identifiable, Sendable {
        public let task: FactoryTask
        public let layerIndex, slotIndex: Int
        public let statusWord: String
        public let tone: Tone
        public let isDashed: Bool
        public let accessibilityLabel: String

        public var id: String { task.ref }
        public var ref: String { task.ref }
        public var title: String { task.title }
    }

    public struct Edge: Equatable, Hashable, Sendable {
        public let from, to: String

        public init(from: String, to: String) {
            self.from = from
            self.to = to
        }
    }

    /// Side-by-side vs stacked board layout rule for regular-width iPad windows.
    public struct BoardLayout: Equatable, Sendable {
        public static let minSideBySideWidth: CGFloat = 680
        public let sideBySide: Bool
        public let listWidth: CGFloat?

        public init(isRegular: Bool, isAccessibilitySize: Bool, availableWidth: CGFloat) {
            sideBySide = isRegular && !isAccessibilitySize && availableWidth >= Self.minSideBySideWidth
            listWidth = sideBySide ? min(340, max(280, ((availableWidth - 48) * 0.42).rounded(.down))) : nil
        }
    }

    public let layers: [[Node]]
    public var nodes: [Node] { layers.flatMap { $0 } }
    public let edges: [Edge]
    public var isEmpty: Bool { layers.isEmpty }

    public var accessibilityLabel: String {
        let n = nodes.count, l = layers.count
        return "task graph: \(n) \(n == 1 ? "task" : "tasks") in \(l) \(l == 1 ? "layer" : "layers")"
    }

    public init(tasks: [FactoryTask]) {
        let grouped = Dictionary(grouping: tasks.enumerated().map { (i: $0.offset, t: $0.element) }, by: \.t.layer)
        var placedCoord: [String: Double] = [:]
        var placedLayer: [String: Int] = [:]
        var builtLayers: [[Node]] = []
        for (layerIndex, key) in grouped.keys.sorted().enumerated() {
            var row = grouped[key] ?? []
            if layerIndex > 0 {
                row.sort { lhs, rhs in
                    let lb = Self.barycenter(for: lhs.t, placed: placedCoord)
                    let rb = Self.barycenter(for: rhs.t, placed: placedCoord)
                    return lb != rb ? lb < rb : lhs.i < rhs.i
                }
            }
            let layerNodes = row.enumerated().map { slotIndex, item -> Node in
                placedCoord[item.t.ref] = Double(slotIndex)
                placedLayer[item.t.ref] = layerIndex
                let info = Self.statusInfo(for: item.t)
                let base = [item.t.ref, item.t.title.trimmed, info.word]
                    .filter { !$0.isEmpty }
                    .joined(separator: ", ")
                let after = item.t.after.isEmpty ? "" : ", after \(item.t.after.joined(separator: " and "))"
                return Node(task: item.t, layerIndex: layerIndex, slotIndex: slotIndex,
                            statusWord: info.word, tone: info.tone, isDashed: info.dashed,
                            accessibilityLabel: "\(base)\(after)")
            }
            builtLayers.append(layerNodes)
        }
        var builtEdges: [Edge] = []
        for node in builtLayers.flatMap({ $0 }) {
            var seen: Set<String> = []
            for dep in node.task.after where seen.insert(dep).inserted {
                if let fromLayer = placedLayer[dep], fromLayer < node.layerIndex {
                    builtEdges.append(Edge(from: dep, to: node.ref))
                }
            }
        }
        self.layers = builtLayers
        self.edges = builtEdges
    }

    public func highlighted(focusing ref: String?) -> (nodes: Set<String>, edges: Set<Edge>) {
        guard let ref, nodes.contains(where: { $0.ref == ref }) else { return ([], []) }
        let hit = Set(edges.filter { $0.from == ref || $0.to == ref })
        return (Set([ref] + hit.flatMap { [$0.from, $0.to] }), hit)
    }

    private static func barycenter(for task: FactoryTask, placed: [String: Double]) -> Double {
        let coords = Set(task.after).compactMap { placed[$0] }
        guard !coords.isEmpty else { return .infinity }
        return coords.reduce(0, +) / Double(coords.count)
    }

    private static func statusInfo(for task: FactoryTask) -> (word: String, tone: Tone, dashed: Bool) {
        if let ended = task.steps.first(where: { $0.state == "ended" }) {
            return (ended.name.trimmed.nonEmpty ?? "ended", .idle, true)
        }
        let word = FactoryProjectBoard.stageLabel(for: task.stage).trimmed.nonEmpty ?? "unknown"
        switch task.stage {
        case "done": return (word, .done, false)
        case "working", "ci": return (word, .working, false)
        case "owner_answer", "owner_gate", "blocked_or_failed": return (word, .blocked, false)
        case "queued": return (word, .idle, true)
        default: return (word, .idle, false)
        }
    }
}
