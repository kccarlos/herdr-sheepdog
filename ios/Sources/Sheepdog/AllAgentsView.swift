import SheepdogKit
import SwiftUI

/// iPad ▸ All agents (canvas "iPad — All agents"): every running agent as
/// an `AgentCard` in two columns, the filters All / Waiting / Working, and
/// Expand all. Expanded cards also list up to 3 earlier messages. A
/// waiting card carries its question and answer buttons, as on iPhone.
/// With one computer there are no computer sections.
struct AllAgentsView: View {
    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var filter: AgentFilter = .all

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(chip: model.connectionChip.status, hasAgents: !model.flock.isEmpty)
    }

    private var shown: [Agent] { model.flock.running.filter(filter.matches) }

    private var allExpanded: Bool {
        let open = shown.filter { model.questionCard(for: $0.paneID) == nil }
        return !open.isEmpty && open.allSatisfy { model.expandedAgents.contains($0.paneID) }
    }

    private var subtitle: String {
        let count = model.flock.running.count
        var text = count == 1 ? "1 agent" : "\(count) agents"
        let waiting = model.flock.waitingCount
        if waiting > 0 { text += " · \(waiting) waiting" }
        return text
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            if paneState != .content {
                FlockUnavailableView(state: paneState)
            } else if shown.isEmpty {
                ContentUnavailableView {
                    Label(filter.emptyText, systemImage: filter == .waiting ? "hand.raised" : "square.grid.2x2")
                } actions: {
                    if filter != .all {
                        Button("Show all") { filter = .all }
                            .accessibilityIdentifier("allAgents.showAll")
                    }
                }
                .frame(maxHeight: .infinity)
                // A container, so Show all keeps its own identifier.
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("allAgents.empty")
            } else {
                grid
            }
        }
        .animation(Theme.ease(reduceMotion: reduceMotion), value: filter)
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 12) {
            if let leading { leading }
            VStack(alignment: .leading, spacing: 0) {
                Text("All agents")
                    .font(.headline)
                    .foregroundStyle(Theme.ink)
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .contentTransition(.numericText())
            }
            .lineLimit(1)
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("allAgents.title")
            Spacer(minLength: 8)
            filterControl
            Button {
                withAnimation(Theme.ease(reduceMotion: reduceMotion)) { toggleAll() }
            } label: {
                Text(allExpanded ? "Collapse all" : "Expand all")
                    .font(.footnote.weight(.medium))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 12)
                    .frame(minHeight: 34)
                    .background(Capsule().fill(Theme.card))
                    .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
                    .frame(minHeight: 44)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("allAgents.expandAll")
        }
        .padding(.leading, leading == nil ? 24 : 10)
        .padding(.trailing, 12)
        .frame(minHeight: 52)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
        .dynamicTypeSize(...DynamicTypeSize.xxLarge)
    }

    private var filterControl: some View {
        HStack(spacing: 2) {
            ForEach(AgentFilter.allCases) { option in
                let on = option == filter
                Button {
                    filter = option
                } label: {
                    Text(option.title)
                        .font(.footnote.weight(on ? .semibold : .regular))
                        .foregroundStyle(Theme.ink)
                        .padding(.horizontal, 12)
                        .frame(minHeight: 28)
                        .background(RoundedRectangle(cornerRadius: 8, style: .continuous).fill(on ? Theme.selected : .clear))
                        .shadow(color: .black.opacity(on ? 0.1 : 0), radius: 1, y: 1)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
                .accessibilityIdentifier("allAgents.filter.\(option.rawValue)")
            }
        }
        .padding(3)
        .background(RoundedRectangle(cornerRadius: 11, style: .continuous).fill(Theme.track))
        .frame(minHeight: 44)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Filter")
    }

    private func toggleAll() {
        let ids = shown.filter { model.questionCard(for: $0.paneID) == nil }.map(\.paneID)
        if allExpanded {
            model.expandedAgents.subtract(ids)
        } else {
            model.expandedAgents.formUnion(ids)
        }
    }

    // MARK: Grid

    @ViewBuilder
    private var grid: some View {
        ScrollView {
            // Two columns of independent heights (no row gaps), one at the
            // accessibility sizes.
            if dynamicTypeSize.isAccessibilitySize {
                column(shown)
                    .padding(.horizontal, 24)
                    .padding(.vertical, 14)
            } else {
                let (left, right) = AgentFilter.columns(shown)
                HStack(alignment: .top, spacing: 14) {
                    column(left)
                    column(right)
                }
                .padding(.horizontal, 24)
                .padding(.vertical, 14)
            }
        }
        .accessibilityIdentifier("allAgents.grid")
    }

    private func column(_ agents: [Agent]) -> some View {
        let first = shown.first { model.questionCard(for: $0.paneID) != nil }?.paneID
        return LazyVStack(spacing: 12) {
            ForEach(agents) { agent in
                let question = model.questionCard(for: agent.paneID)
                AgentCard(
                    agent: agent,
                    question: question,
                    primary: agent.paneID == first,
                    voiceQuestion: question?.pending.id == model.approval?.pending.id,
                    showsEarlier: true,
                    expanded: Binding(
                        get: { model.expandedAgents.contains(agent.paneID) },
                        set: { open in
                            if open { model.expandedAgents.insert(agent.paneID) } else { model.expandedAgents.remove(agent.paneID) }
                        }
                    ),
                    answer: { model.answer(question: $0, option: $1) }
                )
            }
        }
        .frame(maxWidth: .infinity, alignment: .top)
    }
}
