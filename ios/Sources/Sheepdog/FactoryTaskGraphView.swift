import SheepdogKit
import SwiftUI

private struct GraphNodeBoundsKey: PreferenceKey {
    static let defaultValue: [String: Anchor<CGRect>] = [:]
    static func reduce(value: inout [String: Anchor<CGRect>], nextValue: () -> [String: Anchor<CGRect>]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

/// Interactive layered task graph for the iPad factory board (`FactoryIPadBoard.dc.html`).
struct FactoryTaskGraphView: View {
    let graph: FactoryTaskGraph
    let onSelect: (FactoryTask) -> Void

    @ScaledMetric(relativeTo: .body) private var scaledNodeWidth: CGFloat = 118
    @State private var hoveredRef: String?
    @FocusState private var focusedRef: String?

    private var nodeWidth: CGFloat { min(scaledNodeWidth, 250) }
    private var activeRef: String? { hoveredRef ?? focusedRef }
    private var verticalPadding: CGFloat {
        let layerOf = Dictionary(uniqueKeysWithValues: graph.nodes.map { ($0.ref, $0.layerIndex) })
        let skips = graph.edges.filter { (layerOf[$0.to] ?? 0) - (layerOf[$0.from] ?? 0) > 1 }.count
        return skips > 0 ? 14 + CGFloat(min(skips, 5)) * 8 : 12
    }

    var body: some View {
        let focus = graph.highlighted(focusing: activeRef)
        let cardShape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        VStack(alignment: .leading, spacing: 0) {
            Text("Graph")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.muted)
                .padding(.horizontal, 12)
                .padding(.top, 10)
                .accessibilityHidden(true)
            ScrollView(.horizontal) {
                HStack(alignment: .top, spacing: 20) {
                    ForEach(graph.layers.indices, id: \.self) { index in
                        VStack(alignment: .leading, spacing: 14) {
                            ForEach(graph.layers[index]) { node in
                                nodeButton(node, focus: focus)
                                    .anchorPreference(key: GraphNodeBoundsKey.self, value: .bounds) {
                                        [node.ref: $0]
                                    }
                            }
                        }
                        .accessibilityElement(children: .contain)
                        .accessibilityIdentifier("factoryProject.graph.layer.\(index)")
                    }
                }
                .padding(.horizontal, 12)
                .padding(.vertical, verticalPadding)
                .backgroundPreferenceValue(GraphNodeBoundsKey.self) { anchors in
                    GeometryReader { proxy in
                        Canvas { context, _ in
                            let paths = Self.edgePaths(graph: graph, rects: anchors.mapValues { proxy[$0] })
                            let ordered = graph.edges.filter { !focus.edges.contains($0) }
                                + graph.edges.filter { focus.edges.contains($0) }
                            for edge in ordered {
                                guard let path = paths[edge] else { continue }
                                let hot = focus.edges.contains(edge)
                                let dim = activeRef != nil && !hot
                                let color: Color = hot ? Theme.ink : Theme.chevron.opacity(dim ? 0.35 : 1)
                                context.stroke(path, with: .color(color), lineWidth: hot ? 2 : 1.5)
                            }
                        }
                        .allowsHitTesting(false)
                        .accessibilityHidden(true)
                    }
                }
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: cardShape)
        .overlay(cardShape.strokeBorder(Theme.line))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(graph.accessibilityLabel)
        .accessibilityIdentifier("factoryProject.graph")
    }

    private func nodeButton(_ node: FactoryTaskGraph.Node,
                            focus: (nodes: Set<String>, edges: Set<FactoryTaskGraph.Edge>)) -> some View {
        let shape = RoundedRectangle(cornerRadius: 10, style: .continuous)
        let dim = activeRef != nil && !focus.nodes.contains(node.ref)
        return Button { onSelect(node.task) } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 5) {
                    Circle()
                        .fill(toneColor(node.tone))
                        .frame(width: 7, height: 7)
                        .accessibilityHidden(true)
                    Text(node.ref)
                        .font(.caption.weight(.bold).monospaced())
                        .foregroundStyle(Theme.ink)
                }
                Text(node.statusWord)
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
            }
            .multilineTextAlignment(.leading)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 8)
            .padding(.vertical, 6)
            .frame(width: nodeWidth, alignment: .leading)
            .frame(minHeight: 46, alignment: .leading)
            .background(Theme.card, in: shape)
            .overlay(nodeBorder(node, shape: shape))
            .opacity(dim ? 0.45 : 1)
            .contentShape(shape)
            .hoverEffect(.highlight)
        }
        .buttonStyle(.plain)
        .focused($focusedRef, equals: node.ref)
        .onHover { over in
            if over {
                hoveredRef = node.ref
            } else if hoveredRef == node.ref {
                hoveredRef = nil
            }
        }
        .accessibilityLabel(node.accessibilityLabel)
        .accessibilityIdentifier("factoryProject.graph.node.\(node.ref)")
    }

    @ViewBuilder
    private func nodeBorder(_ node: FactoryTaskGraph.Node, shape: RoundedRectangle) -> some View {
        if activeRef == node.ref {
            shape.strokeBorder(Theme.working, lineWidth: 2)
        } else if node.tone == .blocked {
            shape.strokeBorder(Theme.blocked, lineWidth: 2)
        } else if node.isDashed {
            shape.strokeBorder(Theme.dash, style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
        } else {
            shape.strokeBorder(Theme.line, lineWidth: 1)
        }
    }

    private func toneColor(_ tone: FactoryTaskGraph.Tone) -> Color {
        switch tone {
        case .done: return Theme.done
        case .working: return Theme.working
        case .blocked: return Theme.blocked
        case .idle: return Theme.idle
        }
    }

    static func edgePaths(graph: FactoryTaskGraph, rects: [String: CGRect]) -> [FactoryTaskGraph.Edge: Path] {
        let nodeByRef = Dictionary(uniqueKeysWithValues: graph.nodes.map { ($0.ref, $0) })
        var paths: [FactoryTaskGraph.Edge: Path] = [:]
        var topLanes = 0, bottomLanes = 0
        for edge in graph.edges {
            guard let fromNode = nodeByRef[edge.from], let toNode = nodeByRef[edge.to],
                  let from = rects[edge.from], let to = rects[edge.to] else { continue }
            let start = CGPoint(x: from.maxX, y: from.midY), end = CGPoint(x: to.minX, y: to.midY)
            let inter = graph.nodes.compactMap {
                $0.layerIndex > fromNode.layerIndex && $0.layerIndex < toNode.layerIndex ? rects[$0.ref] : nil
            }
            let loY = min(start.y, end.y) - 4, hiY = max(start.y, end.y) + 4
            var path = Path()
            path.move(to: start)
            if inter.contains(where: { $0.minY <= hiY && $0.maxY >= loY }),
               let minY = inter.map(\.minY).min(), let maxY = inter.map(\.maxY).max() {
                let avgY = (start.y + end.y) / 2
                let above = (fromNode.slotIndex == 0 && toNode.slotIndex == 0) || (avgY - minY <= maxY - avgY)
                let lane = min(above ? topLanes : bottomLanes, 4)
                if above { topLanes += 1 } else { bottomLanes += 1 }
                let laneY = above ? minY - 12 - CGFloat(lane) * 8 : maxY + 12 + CGFloat(lane) * 8
                let xL = from.maxX + 10, xR = to.minX - 10
                let rL = min(8, abs(laneY - start.y) / 2), rR = min(8, abs(laneY - end.y) / 2)
                if rL > 0.5 {
                    path.addArc(tangent1End: CGPoint(x: xL, y: start.y), tangent2End: CGPoint(x: xL, y: laneY), radius: rL)
                    path.addArc(tangent1End: CGPoint(x: xL, y: laneY), tangent2End: CGPoint(x: xR, y: laneY), radius: rL)
                } else { path.addLine(to: CGPoint(x: xL, y: laneY)) }
                if rR > 0.5 {
                    path.addArc(tangent1End: CGPoint(x: xR, y: laneY), tangent2End: CGPoint(x: xR, y: end.y), radius: rR)
                    path.addArc(tangent1End: CGPoint(x: xR, y: end.y), tangent2End: end, radius: rR)
                } else { path.addLine(to: CGPoint(x: xR, y: laneY)) }
                path.addLine(to: end)
            } else {
                let midX = (start.x + end.x) / 2
                path.addCurve(to: end, control1: CGPoint(x: midX, y: start.y), control2: CGPoint(x: midX, y: end.y))
            }
            paths[edge] = path
        }
        return paths
    }
}
