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
        model.flockPaneState
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
            if paneState != .content, model.otherSnapshots.isEmpty {
                FlockUnavailableView(state: paneState)
            } else if shown.isEmpty, model.otherSnapshots.isEmpty {
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
                    .hoverEffect()
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
                        .hoverEffect(.highlight)
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
            VStack(alignment: .leading, spacing: 14) {
                let others = model.otherSnapshots
                if !others.isEmpty {
                    ComputerSectionHeader(id: "current", name: model.currentComputerName, detail: "You're talking here", talkTo: nil)
                }
                columns(shown, computer: nil)
                // Several computers: one section per other computer.
                ForEach(others) { computer in
                    let offline: Bool = { if case .offline = computer.reachability { return true }; return false }()
                    ComputerSectionHeader(id: computer.id, name: computer.name, detail: offline ? nil : computer.summary(),
                                          talkTo: offline ? nil : { model.talk(to: computer.id) })
                        .padding(.top, 10)
                    if offline {
                        OfflineComputerRow(name: computer.name, lastSeen: computer.lastSeen()) { model.session(computer.id)?.retry() }
                    } else {
                        columns(Flock(computer.agents).running.filter(filter.matches), computer: computer.id)
                    }
                }
            }
            .padding(.horizontal, 24)
            .padding(.vertical, 14)
        }
        .accessibilityIdentifier("allAgents.grid")
    }

    /// Two columns of independent heights (no row gaps), one at the
    /// accessibility sizes.
    @ViewBuilder
    private func columns(_ agents: [Agent], computer: String?) -> some View {
        if dynamicTypeSize.isAccessibilitySize {
            column(agents, computer: computer)
        } else {
            let (left, right) = AgentFilter.columns(agents)
            HStack(alignment: .top, spacing: 14) {
                column(left, computer: computer)
                column(right, computer: computer)
            }
        }
    }

    /// `computer` is another computer's id, nil for the current one's.
    private func column(_ agents: [Agent], computer: String?) -> some View {
        let first = computer == nil ? shown.first { model.questionCard(for: $0.paneID) != nil }?.paneID : nil
        return LazyVStack(spacing: 12) {
            ForEach(agents) { agent in
                let question = computer.map { model.remoteCard(computer: $0, paneID: agent.paneID) } ?? model.questionCard(for: agent.paneID)
                let key = computer.map { "\($0)|\(agent.paneID)" } ?? agent.paneID
                AgentCard(
                    agent: agent,
                    question: question,
                    primary: agent.paneID == first,
                    voiceQuestion: computer == nil && question?.pending.id == model.approval?.pending.id,
                    showsEarlier: true,
                    showsWindowButton: computer == nil,
                    expanded: Binding(
                        get: { model.expandedAgents.contains(key) },
                        set: { open in
                            if open { model.expandedAgents.insert(key) } else { model.expandedAgents.remove(key) }
                        }
                    ),
                    answer: { model.answer(question: $0, option: $1) }
                )
            }
        }
        .frame(maxWidth: .infinity, alignment: .top)
    }
}
