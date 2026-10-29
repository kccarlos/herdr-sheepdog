import SheepdogKit
import SwiftUI

/// What the iPad detail shows (canvas "iPad — …" boards).
enum IPadDetail: Hashable {
    case conversation
    case allAgents
    case agent(String)
    /// Two agents side by side (stacked in portrait).
    case pair(String, String)

    /// Whether the floating voice capsule is over this detail. An agent's
    /// terminal has its own composer and key bar at the foot, and the
    /// capsule must never cover them: as on iPhone, where the terminal is a
    /// screen of its own without one (a terminal's voice controls are a
    /// later task).
    var showsCapsule: Bool {
        switch self {
        case .conversation, .allAgents: return true
        case .agent, .pair: return false
        }
    }

    /// The agents on screen, in pane order.
    var panes: [String] {
        switch self {
        case let .agent(id): return [id]
        case let .pair(a, b): return [a, b]
        case .conversation, .allAgents: return []
        }
    }
}

/// The iPad layout at regular width (docs/dev/design/app-redesign.md
/// section 6): a `NavigationSplitView` with the sidebar (Conversation, All
/// agents, the agents by space) and the detail (an agent's window, the
/// Conversation in a 720 pt column, or All agents). The voice capsule
/// floats at the foot of the detail (`IPadDock`), centred in what the
/// sidebar leaves, as on iPhone.
struct IPadRootView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID

    var body: some View {
        GeometryReader { proxy in
            let portrait = proxy.isPortrait
            NavigationSplitView(columnVisibility: Bindable(ui).columns) {
                IPadSidebar(portrait: portrait)
                    .navigationSplitViewColumnWidth(390)
                    .toolbar(removing: .sidebarToggle)
                    .toolbar(.hidden, for: .navigationBar)
            } detail: {
                NavigationStack(path: Bindable(ui).detailPath) {
                    IPadDetailView(sidebarHidden: ui.columns == .detailOnly || (ui.columns == .automatic && portrait), portrait: portrait)
                        .toolbar(.hidden, for: .navigationBar)
                        .navigationDestination(for: ShellRoute.self) { route in
                            ShellWindowView(route: route)
                        }
                }
            }
            .modifier(SplitStyle(portrait: portrait))
            .onChange(of: portrait, initial: true) { _, portrait in
                // Portrait opens on the detail; landscape shows both. (Set
                // after the first layout: the split view ignores it before.)
                DispatchQueue.main.async {
                    ui.columns = portrait ? .detailOnly : .all
                }
            }
        }
        .background(Theme.surface)
    }
}

extension GeometryProxy {
    /// Whether the area is taller than wide, measured with the safe area
    /// added back: the keyboard is a bottom safe-area inset and shrinks
    /// `size.height`, which on a portrait iPad made the layout look like
    /// landscape the moment the keyboard came up (the sidebar returned and
    /// the keyboard went away again).
    var isPortrait: Bool {
        size.height + safeAreaInsets.top + safeAreaInsets.bottom
            > size.width + safeAreaInsets.leading + safeAreaInsets.trailing
    }
}

/// Landscape: the sidebar beside the detail. Portrait: the detail keeps
/// the whole width and the sidebar slides over it.
private struct SplitStyle: ViewModifier {
    let portrait: Bool

    func body(content: Content) -> some View {
        if portrait {
            content.navigationSplitViewStyle(.prominentDetail)
        } else {
            content.navigationSplitViewStyle(.balanced)
        }
    }
}

// MARK: - Sidebar

/// Canvas "IPadSidebar": the title with `+` and `⋯`, Conversation and All
/// agents, then the agents under their spaces. With one computer there is
/// no computer row. The voice capsule is not here: it floats over the
/// detail.
struct IPadSidebar: View {
    let portrait: Bool
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID

    private var menus: SessionMenus { SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: sceneID) }

    /// Every computer in the store's order, the current one in its place
    /// (several computers only; with one there is no computer level).
    private var sections: SidebarSections {
        SidebarSections(snapshots: model.computerSnapshots, currentID: model.currentComputerID,
                        currentName: model.currentComputerName, currentAgents: model.flock.all)
    }

    private var paneState: FlockPaneState {
        model.flockPaneState
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 1) {
                    navRow(.conversation, title: "Conversation", symbol: "bubble.left", meta: nil, id: "sidebar.conversation")
                    navRow(.allAgents, title: "All agents", symbol: "square.grid.2x2",
                           meta: model.waitingEverywhere > 0 ? "\(model.waitingEverywhere) waiting" : nil, id: "sidebar.allAgents")
                    agentRows
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        // The sidebar is a 390 pt column: it grows up to the largest
        // non-accessibility size, as the iPhone's chrome does; the detail
        // keeps scaling.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .background(Theme.fill.ignoresSafeArea())
    }

    private var header: some View {
        HStack(spacing: 2) {
            Text(model.isDemo ? "Demo" : "Sheepdog")
                .font(.title2.weight(.bold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .frame(maxWidth: .infinity, alignment: .leading)
            // From values only (`StableMenu`): an open menu stays still
            // while the session re-renders.
            stableMenu(menus.barValue, probe: "new", content: { menus.newItems($0) }) { _ in headerGlyph("plus") }
                .accessibilityLabel("New")
                .accessibilityIdentifier("toolbar.new")
            stableMenu(menus.barValue, probe: "more", content: { menus.moreItems($0) }) { _ in headerGlyph("ellipsis") }
                .accessibilityLabel("More")
                .accessibilityIdentifier("toolbar.more")
            Button {
                withAnimation(Theme.ease) { ui.columns = .detailOnly }
            } label: {
                headerGlyph("sidebar.left", weight: .regular)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Hide sidebar")
            .accessibilityIdentifier("sidebar.hide")
        }
        .padding(.leading, 20)
        .padding(.trailing, 10)
        .frame(minHeight: 52)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }

    private func headerGlyph(_ symbol: String, weight: Font.Weight = .semibold) -> some View {
        Image(systemName: symbol)
            .font(.system(size: 18, weight: weight))
            .foregroundStyle(Theme.ink)
            .frame(width: 44, height: 44)
            .contentShape(Circle())
            .hoverEffect()
    }

    // MARK: Rows

    private func navRow(_ detail: IPadDetail, title: String, symbol: String, meta: String?, id: String) -> some View {
        let selected = ui.detail == detail
        return Button {
            select(detail)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: symbol)
                    .font(.system(size: 17))
                    .frame(width: 22)
                Text(title)
                    .font(.body.weight(selected ? .semibold : .medium))
                    .frame(maxWidth: .infinity, alignment: .leading)
                if let meta {
                    Text(meta)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.blockedText)
                        .contentTransition(.numericText())
                }
            }
            .foregroundStyle(Theme.ink)
            .padding(.horizontal, 10)
            .frame(minHeight: 44)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(selected ? Theme.line : .clear))
            .contentShape(.hoverEffect, RoundedRectangle(cornerRadius: 10, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .hoverEffect(.highlight)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier(id)
    }

    @ViewBuilder
    private var agentRows: some View {
        let sections = self.sections
        if sections.computers.first?.spaces.isEmpty ?? true, !sections.showsComputerHeaders {
            emptyRow
                .padding(.top, 16)
        }
        ForEach(sections.computers) { computer in
            let remote = computer.id != model.currentComputerID
            if sections.showsComputerHeaders {
                computerHeader(computer.id, name: computer.name)
            }
            if model.agentsModel.isComputerExpanded(computer.id) {
                ForEach(computer.spaces) { space in
                    Text(space.name.isEmpty ? "Other" : space.name)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.muted)
                        .padding(.horizontal, 10)
                        .padding(.top, 12)
                        .padding(.bottom, 2)
                        .accessibilityAddTraits(.isHeader)
                    ForEach(space.agents) { agent in
                        if remote {
                            agentRow(agent, remote: true)
                        } else {
                            agentRow(agent)
                                // Drag it beside the current pane, or out of the
                                // app for a window of its own.
                                .draggable(AgentWindowRoute(paneID: agent.paneID)) {
                                    Label(agent.alias, systemImage: "terminal")
                                        .font(.body.weight(.semibold))
                                        .padding(.horizontal, 14)
                                        .padding(.vertical, 10)
                                        .background(Capsule().fill(Theme.card))
                                }
                        }
                    }
                }
            }
        }
    }

    /// A computer's row (several computers only), collapsed or expanded by
    /// its name and moved or retried by its voice marker.
    private func computerHeader(_ id: String, name: String) -> some View {
        let snapshot = model.computerSnapshots.first { $0.id == id }
        let isCurrent = id == model.currentComputerID
        let marker = ComputerVoiceMarkerState.resolve(
            computerID: id,
            currentComputerID: model.currentComputerID,
            reachability: snapshot?.reachability ?? .connecting,
            showsComputers: model.showsComputerLabels
        )
        return ComputerSectionHeader(
            id: id,
            name: name,
            detail: isCurrent ? nil : snapshot?.summary(),
            marker: marker,
            style: .sidebar,
            isExpanded: model.agentsModel.isComputerExpanded(id),
            toggleExpanded: {
                withAnimation(Theme.ease) {
                    model.agentsModel.toggleComputer(id)
                }
            },
            moveVoice: { model.talk(to: id) },
            retry: { model.session(id)?.retry() }
        )
    }

    private func agentRow(_ agent: Agent, remote: Bool = false) -> some View {
        let selected = !remote && ui.detail.panes.contains(agent.paneID)
        let blocked = agent.state == .blocked
        return Button {
            withAnimation(Theme.ease) {
                if remote {
                    // Another computer's agent: All agents shows it, with Talk to.
                    ui.detail = .allAgents
                } else {
                    ui.show(agent.paneID)
                }
                ui.detailPath = NavigationPath()
                if portrait { ui.columns = .detailOnly }
            }
        } label: {
            HStack(spacing: 10) {
                Circle().fill(Theme.color(for: agent.state)).frame(width: 8, height: 8)
                    .padding(.horizontal, 7)
                Text(agent.alias)
                    .font(.body.weight(selected ? .semibold : .regular))
                    .foregroundStyle(Theme.ink)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if blocked {
                    Image(systemName: "hand.raised")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(Theme.blockedText)
                    Text("waiting")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.blockedText)
                } else {
                    TimelineView(.periodic(from: .now, by: 30)) { context in
                        Text(agent.timeInState(at: DemoClock.shifted(context.date)) ?? Theme.word(for: agent.state))
                            .font(.caption)
                            .foregroundStyle(Theme.muted)
                            .contentTransition(.numericText())
                    }
                }
            }
            .padding(.horizontal, 10)
            .frame(minHeight: 44)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(selected ? Theme.line : .clear))
            .contentShape(.hoverEffect, RoundedRectangle(cornerRadius: 10, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .hoverEffect(.highlight)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(agent.alias), \(blocked ? "waiting on you" : Theme.word(for: agent.state))")
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier(remote ? "computerLabel.sidebarAgent.\(agent.alias)" : "sidebar.agent.\(agent.alias)")
    }

    /// No agent to list: why, in one line, with the one action that helps.
    @ViewBuilder
    private var emptyRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            switch paneState {
            case .loading, .reconnecting:
                Text(paneState == .loading ? "Connecting…" : "Reconnecting…")
            case .empty, .content:
                Text("No agents running.")
                if model.agentActionsAvailable {
                    Button("New agent") {
                        scenes.request(from: sceneID)
                        model.presentation.present(.newAgent)
                    }
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.primaryText)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("sidebar.newAgent")
                }
            case .notConnected:
                Text(model.isConfigured ? "Not connected." : "Pair your computer to see its agents.")
                Button(model.isConfigured ? "Connect" : "Pair") {
                    if model.isConfigured {
                        model.connect()
                    } else {
                        scenes.request(from: sceneID)
                        model.presentation.present(.settings)
                    }
                }
                .fontWeight(.semibold)
                .foregroundStyle(Theme.primaryText)
                .frame(minHeight: 44)
            case .tokenRefused:
                Text((model.notices.pairingProblem ?? .revoked).title)
                Button("Scan again") { model.needsNewScan(model.notices.pairingProblem ?? .revoked) }
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.primaryText)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("sidebar.scan")
            case .unreachable, .noNetwork, .untrusted, .updateNeeded:
                // The link's own words, the same as the chip's sheet.
                Text(model.linkModel.presentation.inline?.message ?? "Can't reach the computer.")
                if paneState != .noNetwork {
                    Button("Try again") { model.retryLink() }
                        .fontWeight(.semibold)
                        .foregroundStyle(Theme.primaryText)
                        .frame(minHeight: 44)
                        .accessibilityIdentifier("sidebar.retry")
                }
            }
        }
        .font(.subheadline)
        .foregroundStyle(Theme.muted)
        .padding(.horizontal, 10)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("sidebar.empty")
    }

    private func select(_ detail: IPadDetail) {
        withAnimation(Theme.ease) {
            ui.detail = detail
            ui.detailPath = NavigationPath()
            // The overlay sidebar (portrait) gets out of the way.
            if portrait { ui.columns = .detailOnly }
        }
    }
}

// MARK: - Detail

struct IPadDetailView: View {
    /// The sidebar is not beside the detail: the header shows Show
    /// sidebar.
    let sidebarHidden: Bool
    let portrait: Bool
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// The detail's height above the keyboard (`FloatingQuestions.area`).
    @State private var questionArea: CGFloat = 0

    private var menus: SessionMenus { SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: sceneID) }

    var body: some View {
        VStack(spacing: 0) {
            if model.isDemo {
                DemoHeader()
                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            }
            switch ui.detail {
            case .conversation:
                IPadConversationDetail(leading: leading)
            case .allAgents:
                AllAgentsView(leading: leading)
            case .agent, .pair:
                AgentPanes(panes: ui.detail.panes, leading: leading)
            }
        }
        .background(Theme.surface)
        // The question, the coach mark and the capsule float over the
        // detail, which scrolls under them.
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if ui.detail.showsCapsule {
                IPadDock(
                    menus: menus,
                    questionArea: questionArea,
                    showsQuestions: ui.detail == .conversation,
                    // In portrait the sidebar slides over the detail
                    // instead of beside it.
                    sidebarOverlays: portrait && ui.columns != .detailOnly
                )
            }
        }
        // The detail above the keyboard, outside the floating stack: what
        // the questions decide inline or compact against.
        .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded() } action: { questionArea = $0 }
        .animation(Theme.ease(reduceMotion: reduceMotion), value: model.floatingQuestions.map(\.pending.id))
    }

    /// Show sidebar, when the sidebar is not beside the detail.
    private var leading: AnyView? {
        guard sidebarHidden else { return nil }
        return AnyView(HStack(spacing: 6) {
            Button {
                withAnimation(Theme.ease) { ui.columns = .all }
            } label: {
                Image(systemName: "sidebar.left")
                    .font(.system(size: 18))
                    .foregroundStyle(Theme.ink)
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Show sidebar")
            .accessibilityIdentifier("sidebar.show")
        })
    }
}

/// The detail's 52 pt header: something before, a centred title (and a
/// line under it), something after.
struct DetailHeader<Leading: View, Trailing: View>: View {
    let title: String
    var subtitle: String?
    @ViewBuilder let leading: () -> Leading
    @ViewBuilder let trailing: () -> Trailing

    var body: some View {
        HStack(spacing: 0) {
            HStack(spacing: 6) { leading() }
                .frame(maxWidth: .infinity, alignment: .leading)
            VStack(spacing: 0) {
                Text(title)
                    .font(.headline)
                    .foregroundStyle(Theme.ink)
                if let subtitle {
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                }
            }
            .lineLimit(1)
            .layoutPriority(1)
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            HStack(spacing: 2) { trailing() }
                .frame(maxWidth: .infinity, alignment: .trailing)
        }
        .padding(.horizontal, 10)
        .frame(minHeight: 52)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }
}

/// Canvas "iPad — Conversation": the conversation in a 720 pt centred
/// column. The open questions and the composer are the floating dock's
/// (`IPadDock`), as on iPhone.
struct IPadConversationDetail: View {
    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui

    static let column: CGFloat = 720

    var body: some View {
        VStack(spacing: 0) {
            DetailHeader(title: "Conversation") {
                if let leading { leading }
            } trailing: {
                if model.conversationResetSupported {
                    Button {
                        ui.confirmNewConversation = true
                    } label: {
                        Image(systemName: "arrow.counterclockwise")
                            .font(.system(size: 17, weight: .medium))
                            .foregroundStyle(Theme.ink)
                            .frame(width: 44, height: 44)
                            .contentShape(Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("New conversation")
                    .accessibilityIdentifier("conversation.new")
                }
            }
            SessionBanners()
                .frame(maxWidth: Self.column)
                .padding(.top, 8)
            ConversationPane()
                .frame(maxWidth: Self.column)
                .frame(maxWidth: .infinity)
        }
        .animation(Theme.ease, value: model.notices.banner)
    }
}

/// The selected agent is gone (its pane closed).
struct IPadAgentGone: View {
    let leading: AnyView?
    let back: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            DetailHeader(title: "") {
                if let leading { leading }
            } trailing: {
                EmptyView()
            }
            ContentUnavailableView {
                Label("That agent is gone", systemImage: "terminal")
            } description: {
                Text("Its pane closed or herdr no longer reports it.")
            } actions: {
                Button("Close") { back() }
                    .accessibilityIdentifier("agentGone.back")
            }
            .frame(maxHeight: .infinity)
        }
    }
}
