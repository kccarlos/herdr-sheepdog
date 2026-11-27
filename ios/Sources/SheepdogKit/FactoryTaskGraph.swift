import Foundation

/// Layered task dependency graph for the iPad factory board (`web/factory/graph.js`,
/// `FactoryIPadBoard.dc.html`, `slice14-wording.md` `gr.node`).
public struct FactoryTaskGraph: Equatable, Sendable {
    public enum Tone: String, Equatable, Sendable {
        case done, working, blocked, idle
    }

    public struct Node: Equatable, Identifiable, Sendable {
        public let task: FactoryTask
        public let layerIndex: Int
        public let slotIndex: Int
        public let statusWord: String
        public let tone: Tone
        public let isDashed: Bool
        public let accessibilityLabel: String

        public var id: String { task.ref }
        public var ref: String { task.ref }
        public var title: String { task.title }
    }

    public struct Edge: Equatable, Hashable, Sendable {
        public let from: String
        public let to: String

        public init(from: String, to: String) {
            self.from = from
            self.to = to
        }
    }

    public let layers: [[Node]]
    public var nodes: [Node] { layers.flatMap { $0 } }
    public let edges: [Edge]
    public var isEmpty: Bool { layers.isEmpty }

    public var accessibilityLabel: String {
        let n = nodes.count
        let l = layers.count
        return "task graph: \(n) \(n == 1 ? "task" : "tasks") in \(l) \(l == 1 ? "layer" : "layers")"
    }

    public init(tasks: [FactoryTask]) {
        guard !tasks.isEmpty else {
            self.layers = []
            self.edges = []
            return
        }
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
            let half = Double(row.count - 1) / 2.0
            let layerNodes = row.enumerated().map { slotIndex, item -> Node in
                placedCoord[item.t.ref] = Double(slotIndex) - half
                placedLayer[item.t.ref] = layerIndex
                let info = Self.statusInfo(for: item.t)
                let after = item.t.after.isEmpty ? "" : ", after \(item.t.after.joined(separator: " and "))"
                let aria = "\(item.t.ref), \(item.t.title), \(info.word)\(after)"
                return Node(task: item.t, layerIndex: layerIndex, slotIndex: slotIndex,
                            statusWord: info.word, tone: info.tone, isDashed: info.dashed, accessibilityLabel: aria)
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
        let hit = edges.filter { $0.from == ref || $0.to == ref }
        var related: Set<String> = [ref]
        for edge in hit {
            related.insert(edge.from)
            related.insert(edge.to)
        }
        return (related, Set(hit))
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
        switch task.stage {
        case "done": return ("done", .done, false)
        case "working": return ("working", .working, false)
        case "ci": return ("waiting on CI", .working, false)
        case "owner_answer": return ("waiting on you", .blocked, false)
        case "owner_gate": return ("needs your approval", .blocked, false)
        case "blocked_or_failed": return ("blocked", .blocked, false)
        case "waiting_on_dep": return ("waiting on others", .idle, false)
        case "queued": return ("queued", .idle, true)
        default: return (task.stage.replacingOccurrences(of: "_", with: " "), .idle, false)
        }
    }
}
