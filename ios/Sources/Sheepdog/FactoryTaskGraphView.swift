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

    @ScaledMetric(relativeTo: .body) private var scaledNodeWidth: CGFloat = 136
    @State private var hoveredRef: String?

    private var nodeWidth: CGFloat { min(scaledNodeWidth, 250) }

    var body: some View {
        let focus = graph.highlighted(focusing: hoveredRef)
        let cardShape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
        VStack(alignment: .leading, spacing: 0) {
            Text("Graph")
                .font(.caption.weight(.semibold)).foregroundStyle(Theme.muted)
                .padding(.horizontal, 14).padding(.top, 10)
                .accessibilityHidden(true)
            ScrollView([.horizontal, .vertical]) {
                HStack(alignment: .top, spacing: 28) {
                    ForEach(graph.layers.indices, id: \.self) { index in
                        VStack(alignment: .leading, spacing: 14) {
                            ForEach(graph.layers[index]) { node in
                                nodeButton(node, focus: focus)
                                    .anchorPreference(key: GraphNodeBoundsKey.self, value: .bounds) {
                                        [node.ref: $0]
                                    }
                            }
                        }
                    }
                }
                .padding(14)
                .backgroundPreferenceValue(GraphNodeBoundsKey.self) { anchors in
                    GeometryReader { proxy in
                        Canvas { context, _ in
                            for edge in graph.edges {
                                guard let fromA = anchors[edge.from], let toA = anchors[edge.to] else { continue }
                                let from = proxy[fromA]
                                let to = proxy[toA]
                                let start = CGPoint(x: from.maxX, y: from.midY)
                                let end = CGPoint(x: to.minX, y: to.midY)
                                let midX = (start.x + end.x) / 2
                                var path = Path()
                                path.move(to: start)
                                path.addCurve(to: end, control1: CGPoint(x: midX, y: start.y),
                                              control2: CGPoint(x: midX, y: end.y))
                                let hot = focus.edges.contains(edge)
                                let dim = hoveredRef != nil && !hot
                                let color: Color = hot ? Theme.ink : Theme.chevron.opacity(dim ? 0.35 : 1)
                                context.stroke(path, with: .color(color), lineWidth: hot ? 2 : 1.5)
                            }
                        }
                        .allowsHitTesting(false).accessibilityHidden(true)
                    }
                }
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: cardShape).overlay(cardShape.strokeBorder(Theme.line))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(graph.accessibilityLabel).accessibilityIdentifier("factoryProject.graph")
    }

    private func nodeButton(_ node: FactoryTaskGraph.Node,
                            focus: (nodes: Set<String>, edges: Set<FactoryTaskGraph.Edge>)) -> some View {
        let shape = RoundedRectangle(cornerRadius: 10, style: .continuous)
        let dim = hoveredRef != nil && !focus.nodes.contains(node.ref)
        return Button { onSelect(node.task) } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 5) {
                    Circle().fill(toneColor(node.tone)).frame(width: 7, height: 7).accessibilityHidden(true)
                    Text(node.ref).font(.caption.weight(.bold).monospaced()).foregroundStyle(Theme.ink)
                }
                Text(node.statusWord).font(.caption).foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 8).padding(.vertical, 6)
            .frame(minWidth: nodeWidth, maxWidth: nodeWidth, minHeight: 46, alignment: .leading)
            .background(Theme.card, in: shape).overlay(nodeBorder(node, shape: shape))
            .opacity(dim ? 0.45 : 1).contentShape(shape).hoverEffect(.highlight)
        }
        .buttonStyle(.plain)
        .onHover { over in
            if over { hoveredRef = node.ref } else if hoveredRef == node.ref { hoveredRef = nil }
        }
        .accessibilityLabel(node.accessibilityLabel).accessibilityIdentifier("factoryProject.graph.node.\(node.ref)")
    }

    @ViewBuilder
    private func nodeBorder(_ node: FactoryTaskGraph.Node, shape: RoundedRectangle) -> some View {
        if hoveredRef == node.ref {
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
}
