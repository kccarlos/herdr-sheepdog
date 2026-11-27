import SheepdogKit
import SwiftUI

/// iPad ▸ All agents (canvas "iPad — All agents"): every running agent as
/// an `AgentCard` in two columns, the filters All / Waiting / Working, and
/// Expand all. Expanded cards also list up to 3 earlier messages. A
/// waiting card carries its question and answer buttons, as on iPhone.
/// With one computer there are no computer sections.
struct AllAgentsView: View {
    private enum CardItem: Identifiable {
        case ask(PendingAction)
        case head(FactoryAgentsList.Item)
        case agent(Agent)

        var id: String {
            switch self {
            case let .ask(a): return "ask:\(a.id)"
            case let .head(h): return "head:\(h.id)"
            case let .agent(a): return "agent:\(a.paneID)"
            }
        }
    }

    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var filter: AgentFilter = .all
    @State private var openDone: Set<String> = []

    private var factoryList: FactoryAgentsList { model.agentsModel.factoryList }
    private var paneState: FlockPaneState { model.flockPaneState(hasAgents: !factoryList.isEmpty) }
    private var shown: [Agent] { factoryList.agents.running.filter(filter.matches) }

    private var settledFactoryAsk: PendingAction? { factoryList.settledAsk(from: model.approval) }

    private var currentHasMatches: Bool {
        switch filter {
        case .all:
            return !factoryList.isEmpty || settledFactoryAsk != nil
        case .waiting:
            return !shown.isEmpty || !factoryList.needsYouHeads.isEmpty
                || !factoryList.factoryAsks.isEmpty || settledFactoryAsk != nil
        case .working:
            return !shown.isEmpty || !factoryList.workingHeads.isEmpty
        }
    }

    private var allExpanded: Bool {
        let open = shown.filter { model.questionCard(for: $0.paneID) == nil }
        return !open.isEmpty && open.allSatisfy { model.agentsModel.expandedAgents.contains($0.paneID) }
    }

    private var subtitle: String {
        let lists = [factoryList] + model.otherSnapshots.filter(\.reachability.isOnline).map(\.factoryList)
        return ComputerSnapshot.summaryLine(
            agents: lists.reduce(0) { $0 + $1.agents.running.count },
            projects: lists.reduce(0) { $0 + $1.items.count },
            waiting: lists.reduce(0) { $0 + $1.waitingCount }
        )
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            if model.notices.banner != nil {
                SessionBanners()
                    .padding(.top, 8)
            }
            if paneState != .content, model.otherSnapshots.isEmpty {
                FlockUnavailableView(state: paneState)
            } else if !currentHasMatches, model.otherSnapshots.isEmpty {
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
        .animation(Theme.ease, value: model.notices.banner)
        .animation(Theme.ease(reduceMotion: reduceMotion), value: filter)
        .onChange(of: model.navigation.focusedAgent, initial: true) { _, _ in
            ensureFocusedAgentVisible()
        }
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: 12) {
            if let leading { leading }
            VStack(alignment: .leading, spacing: 0) {
                Text("All agents")
                    .font(.headline)
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1)
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
                    .contentTransition(.numericText())
            }
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
            model.agentsModel.expandedAgents.subtract(ids)
        } else {
            model.agentsModel.expandedAgents.formUnion(ids)
        }
    }

    // MARK: Grid

    @ViewBuilder
    private var grid: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    let sections = model.sectionSnapshots
                    if sections.isEmpty {
                        sectioned(factoryList, computer: nil)
                    } else {
                        ForEach(sections) { computer in
                            let offline: Bool = { if case .offline = computer.reachability { return true }; return false }()
                            let marker = marker(for: computer)
                            ComputerSectionHeader(
                                id: computer.id,
                                name: computer.name,
                                detail: offline ? nil : computer.summary(),
                                marker: marker,
                                isExpanded: model.agentsModel.isComputerExpanded(computer.id),
                                toggleExpanded: {
                                    withAnimation(Theme.ease(reduceMotion: reduceMotion)) {
                                        model.agentsModel.toggleComputer(computer.id)
                                    }
                                },
                                moveVoice: { model.talk(to: computer.id) },
                                retry: { model.session(computer.id)?.retry() }
                            )
                            .padding(.top, computer.id == sections.first?.id ? 0 : 10)
                            if model.agentsModel.isComputerExpanded(computer.id) {
                                if computer.isCurrent {
                                    sectioned(factoryList, computer: nil)
                                } else if offline {
                                    OfflineComputerRow(name: computer.name, lastSeen: computer.lastSeen()) { model.session(computer.id)?.retry() }
                                } else {
                                    sectioned(computer.factoryList, computer: computer)
                                }
                            }
                        }
                    }
                }
                .padding(.horizontal, 24)
                .padding(.vertical, 14)
            }
            .accessibilityIdentifier("allAgents.grid")
            .onAppear { scrollToFocus(proxy) }
            .onChange(of: model.navigation.focusedAgent) { _, _ in scrollToFocus(proxy) }
            .onChange(of: renderedFocusToken) { _, _ in scrollToFocus(proxy) }
        }
    }

    private func marker(for computer: ComputerSnapshot) -> ComputerVoiceMarkerState {
        ComputerVoiceMarkerState.resolve(
            computerID: computer.id,
            currentComputerID: model.currentComputerID,
            reachability: computer.reachability,
            showsComputers: model.showsComputerLabels
        )
    }

    /// Agents and factory project heads under Needs you / Working / Stopped / Done.
    @ViewBuilder
    private func sectioned(_ list: FactoryAgentsList, computer: ComputerSnapshot?) -> some View {
        let agents = list.agents.running.filter(filter.matches)
        if computer != nil, !list.hasFactoryContent {
            columns(agents.map(CardItem.agent), computer: computer, firstAskID: nil)
        } else {
            let id = computer?.id ?? model.currentComputerID
            let settled = computer == nil ? settledFactoryAsk : nil
            let asks = filter == .working ? [] : list.factoryAsks + (settled.map { [$0] } ?? [])
            let needsHeads = filter == .working ? [] : list.needsYouHeads
            let workHeads = filter == .waiting ? [] : list.workingHeads
            let stopHeads = filter == .all ? list.stoppedHeads : []
            let doneHeads = filter == .all ? list.finishedHeads : []
            let groups: [(AgentGroup, [CardItem])] = [
                (.needsYou, asks.map(CardItem.ask) + needsHeads.map(CardItem.head)
                    + agents.filter { $0.group == .needsYou }.map(CardItem.agent)),
                (.working, workHeads.map(CardItem.head) + agents.filter { $0.group == .working }.map(CardItem.agent)),
                (.done, doneHeads.map(CardItem.head) + agents.filter { $0.group == .done }.map(CardItem.agent)),
            ]
            ForEach(groups, id: \.0) { group, items in
                if group == .done && !stopHeads.isEmpty {
                    AgentSectionHeader(group: .working, count: stopHeads.count, title: "Stopped")
                    columns(stopHeads.map(CardItem.head), computer: computer, firstAskID: asks.first?.id)
                }
                if !items.isEmpty {
                    let folded = group == .done && !doneHeads.isEmpty
                    if folded {
                        SectionFoldRow(title: "Done (\(items.count))", expanded: openDone.contains(id),
                                       identifier: "flock.section.done") { openDone.formSymmetricDifference([id]) }
                    } else {
                        AgentSectionHeader(group: group, count: items.count)
                    }
                    if !folded || openDone.contains(id) {
                        columns(items, computer: computer, firstAskID: asks.first?.id)
                    }
                }
            }
        }
    }

    /// Two columns of independent heights (no row gaps), one at the
    /// accessibility sizes.
    @ViewBuilder
    private func columns(_ items: [CardItem], computer: ComputerSnapshot?, firstAskID: String?) -> some View {
        if dynamicTypeSize.isAccessibilitySize {
            column(items, computer: computer, firstAskID: firstAskID)
        } else {
            let (left, right) = AgentFilter.columns(items)
            HStack(alignment: .top, spacing: 14) {
                column(left, computer: computer, firstAskID: firstAskID)
                column(right, computer: computer, firstAskID: firstAskID)
            }
        }
    }

    /// `computer` is another computer's snapshot, nil for the current one.
    private func column(_ items: [CardItem], computer: ComputerSnapshot?, firstAskID: String?) -> some View {
        let first = computer == nil && factoryList.factoryAsks.isEmpty && factoryList.needsYouHeads.isEmpty
            ? shown.first { model.questionCard(for: $0.paneID) != nil }?.paneID : nil
        let ownerID = computer?.id ?? model.currentComputerID
        return LazyVStack(spacing: 12) {
            ForEach(items) { item in
                switch item {
                case let .ask(ask):
                    let card = model.factoryAskCard(ask, computer: computer)
                    ApprovalCardView(card: card, primary: computer == nil && ask.id == firstAskID,
                                     idPrefix: "factoryAsk.\(ask.id)") {
                        model.answer(question: card.pending.id, option: $0, on: ownerID)
                    }
                case let .head(headItem):
                    let dialog = headItem.coordinatorDialog.map { model.factoryAskCard($0, computer: computer) }
                    FactoryHeadCard(item: headItem, computer: computer, dialog: dialog,
                                    answer: { model.answer(question: $0, option: $1, on: ownerID) },
                                    onOpen: {
                                        ui.detail = .factoryProject(computerID: ownerID, projectID: headItem.id)
                                        ui.detailPath = NavigationPath()
                                    })
                case let .agent(agent):
                    let question = computer.map { model.remoteCard(computer: $0.id, paneID: agent.paneID) }
                        ?? model.questionCard(for: agent.paneID)
                    let key = computer.map { "\($0.id)|\(agent.paneID)" } ?? agent.paneID
                    AgentCard(
                        agent: agent, question: question, primary: agent.paneID == first,
                        voiceQuestion: computer == nil && question?.pending.id == model.approval?.pending.id,
                        showsEarlier: true, computer: computer?.id,
                        expanded: Binding(
                            get: { model.agentsModel.expandedAgents.contains(key) },
                            set: { open in
                                if open { model.agentsModel.expandedAgents.insert(key) }
                                else { model.agentsModel.expandedAgents.remove(key) }
                            }
                        ),
                        answer: { model.answer(question: $0, option: $1, on: ownerID) }
                    )
                    .id(key)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .top)
    }

    private func ensureFocusedAgentVisible() {
        guard let target = model.navigation.focusedAgent, filter != .all else { return }
        let isOther = target.isOtherComputer(currentComputerID: model.currentComputerID)
        let matchingAgent: Agent?
        if isOther, let cid = target.computerID {
            guard model.showsComputerLabels else { return }
            let agents = model.sectionSnapshots.first(where: { $0.id == cid })?.factoryList.agents.running ?? []
            matchingAgent = agents.first(where: { $0.paneID == target.paneID })
        } else {
            matchingAgent = factoryList.agents.running.first(where: { $0.paneID == target.paneID })
        }
        if matchingAgent == nil || !filter.matches(matchingAgent!) {
            filter = .all
        }
    }

    private var renderedFocusToken: [String] {
        var ids = factoryList.agents.running.map(\.paneID)
        for computer in model.sectionSnapshots where !computer.isCurrent {
            if case .offline = computer.reachability { continue }
            ids.append(contentsOf: computer.factoryList.agents.running.map { "\(computer.id)|\($0.paneID)" })
        }
        return ids
    }

    private func scrollToFocus(_ proxy: ScrollViewProxy) {
        guard let target = model.navigation.focusedAgent else { return }
        ensureFocusedAgentVisible()
        let isOther = target.isOtherComputer(currentComputerID: model.currentComputerID)
        if isOther, let cid = target.computerID {
            guard model.showsComputerLabels else { return }
            model.agentsModel.expandComputer(cid)
        } else {
            model.agentsModel.expandComputer(model.currentComputerID)
        }
        guard target.isTargetPresent(
            currentComputerID: model.currentComputerID,
            sectioned: model.showsComputerLabels,
            currentAgents: factoryList.agents.running,
            otherComputerAgents: { cid in
                guard let comp = model.sectionSnapshots.first(where: { $0.id == cid }) else { return nil }
                if case .offline = comp.reachability { return nil }
                return comp.factoryList.agents.running
            }
        ) else { return }
        let scrollID: String
        if isOther, let cid = target.computerID {
            let key = "\(cid)|\(target.paneID)"
            if model.remoteCard(computer: cid, paneID: target.paneID) == nil {
                model.agentsModel.expandedAgents.insert(key)
            }
            scrollID = key
        } else {
            if model.questionCard(for: target.paneID) == nil {
                model.agentsModel.expandedAgents.insert(target.paneID)
            }
            scrollID = target.paneID
        }
        DispatchQueue.main.async {
            guard model.navigation.focusedAgent == target else { return }
            withAnimation(Theme.ease(reduceMotion: reduceMotion)) {
                proxy.scrollTo(scrollID, anchor: .top)
            }
            model.navigation.focusedAgent = nil
        }
    }
}
