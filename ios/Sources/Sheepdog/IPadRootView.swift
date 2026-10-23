import SheepdogKit
import SwiftUI

/// What the iPad detail shows (canvas "iPad — …" boards).
enum IPadDetail: Hashable {
    case conversation
    case allAgents
    case agent(String)
    /// Two agents side by side (stacked in portrait).
    case pair(String, String)

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
/// agents, the agents by space, the capsule at the foot) and the detail
/// (an agent's window, the Conversation in a 720 pt column, or All agents).
/// In portrait the sidebar hides and the detail's header carries the mini
/// capsule.
struct IPadRootView: View {
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @ObservedObject private var scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID

    var body: some View {
        GeometryReader { proxy in
            let portrait = proxy.size.height > proxy.size.width
            NavigationSplitView(columnVisibility: $ui.columns) {
                IPadSidebar(portrait: portrait)
                    .navigationSplitViewColumnWidth(390)
                    .toolbar(removing: .sidebarToggle)
                    .toolbar(.hidden, for: .navigationBar)
            } detail: {
                NavigationStack(path: $ui.detailPath) {
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
/// agents, then the agents under their spaces, and the voice capsule at
/// the foot. With one computer there is no computer row.
struct IPadSidebar: View {
    let portrait: Bool
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @ObservedObject private var scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID

    private var menus: SessionMenus { SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: sceneID) }

    private var sections: SidebarSections {
        // One computer for now: its name is the voice's.
        SidebarSections(computers: [.computer(id: "current", name: model.voiceName, agents: model.flock.all)])
    }

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(chip: model.connectionChip.status, hasAgents: !model.flock.isEmpty)
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 1) {
                    navRow(.conversation, title: "Conversation", symbol: "bubble.left", meta: nil, id: "sidebar.conversation")
                    navRow(.allAgents, title: "All agents", symbol: "square.grid.2x2",
                           meta: model.flock.waitingCount > 0 ? "\(model.flock.waitingCount) waiting" : nil, id: "sidebar.allAgents")
                    agentRows
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 4)
            }
            .scrollBounceBehavior(.basedOnSize)
            foot
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
            Menu { menus.newMenu } label: { headerGlyph("plus") }
                .accessibilityLabel("New")
                .accessibilityIdentifier("toolbar.new")
            Menu { menus.moreMenu } label: { headerGlyph("ellipsis") }
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
        if sections.agents.isEmpty {
            emptyRow
                .padding(.top, 16)
        }
        ForEach(sections.computers) { computer in
            if sections.showsComputerHeaders {
                Label(computer.name, systemImage: "desktopcomputer")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 10)
                    .padding(.top, 16)
            }
            ForEach(computer.spaces) { space in
                Text(space.name.isEmpty ? "Other" : space.name)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                    .padding(.horizontal, 10)
                    .padding(.top, 12)
                    .padding(.bottom, 2)
                    .accessibilityAddTraits(.isHeader)
                ForEach(space.agents) { agent in
                    agentRow(agent)
                }
            }
        }
    }

    private func agentRow(_ agent: Agent) -> some View {
        let selected = ui.detail.panes.contains(agent.paneID)
        let blocked = agent.state == .blocked
        return Button {
            withAnimation(Theme.ease) {
                ui.show(agent.paneID)
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
                        Text(agent.timeInState(at: context.date) ?? Theme.word(for: agent.state))
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
        .accessibilityIdentifier("sidebar.agent.\(agent.alias)")
        // Drag it beside the current pane, or out of the app for a window
        // of its own.
        .draggable(AgentWindowRoute(paneID: agent.paneID)) {
            Label(agent.alias, systemImage: "terminal")
                .font(.body.weight(.semibold))
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(Capsule().fill(Theme.card))
        }
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
                        model.showsNewAgent = true
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
                        model.showSettings = true
                    }
                }
                .fontWeight(.semibold)
                .foregroundStyle(Theme.primaryText)
                .frame(minHeight: 44)
            case .unreachable, .tokenRefused, .updateNeeded:
                Text("Can't reach the computer.")
                Button("Retry") { model.reconnect() }
                    .fontWeight(.semibold)
                    .foregroundStyle(Theme.primaryText)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("sidebar.retry")
            }
        }
        .font(.subheadline)
        .foregroundStyle(Theme.muted)
        .padding(.horizontal, 10)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("sidebar.empty")
    }

    // MARK: Foot

    @ViewBuilder
    private var foot: some View {
        Group {
            if scenes.isOwner(sceneID) {
                SessionCapsule(menus: menus, onType: {
                    select(.conversation)
                    ui.focusComposer += 1
                })
            } else {
                MiniCapsule(menus: menus)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 14)
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
    /// sidebar and the mini capsule.
    let sidebarHidden: Bool
    let portrait: Bool
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @ObservedObject private var scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID

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
    }

    /// Show sidebar and the mini capsule, when the sidebar is not beside
    /// the detail. Only one capsule is ever on screen: when the sidebar's
    /// own is hidden, this one stands in.
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
            MiniCapsule(menus: menus)
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
/// column, the open questions pinned above a composer.
struct IPadConversationDetail: View {
    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @ObservedObject private var scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    static let column: CGFloat = 720

    private var menus: SessionMenus { SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: sceneID) }

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
                .safeAreaInset(edge: .bottom, spacing: 0) { bottom }
        }
        .animation(Theme.ease(reduceMotion: reduceMotion), value: model.floatingQuestions.map(\.pending.id))
        .animation(Theme.ease, value: model.banner)
    }

    private var bottom: some View {
        VStack(spacing: 10) {
            let cards = model.floatingQuestions
            if !cards.isEmpty {
                FloatingQuestions(
                    cards: cards,
                    voiceQuestionID: model.approval?.pending.id,
                    time: { question in
                        model.flock.all.first { $0.paneID == question.paneID }?.timeInState(at: Date())
                    },
                    compact: false
                ) { id, index in
                    model.answer(question: id, option: index)
                }
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
            }
            IPadComposer()
        }
        .frame(maxWidth: Self.column)
        .padding(.horizontal, 24)
        .padding(.top, 10)
        .padding(.bottom, 16)
        .frame(maxWidth: .infinity)
    }
}

/// The Conversation's composer on iPad: a field and the ink send button.
/// The capsule's keyboard button focuses it.
struct IPadComposer: View {
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @State private var text = ""
    @FocusState private var focused: Bool

    private var canSend: Bool { model.isConfigured && !model.sendingTyped && !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let error = model.typedError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("type.error")
            }
            HStack(spacing: 8) {
                TextField("Type to \(model.voiceName), or just talk", text: $text, axis: .vertical)
                    .font(.body)
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1 ... 5)
                    .submitLabel(.send)
                    .focused($focused)
                    .onSubmit(submit)
                    .disabled(!model.isConfigured)
                    .accessibilityLabel("Message to \(model.voiceName)")
                    .accessibilityIdentifier("type.field")
                Button(action: submit) {
                    ZStack {
                        Circle().fill(canSend ? Theme.ink : Theme.line)
                        if model.sendingTyped {
                            ProgressView().tint(Theme.surface)
                        } else {
                            Image(systemName: "arrow.up")
                                .font(.system(size: 15, weight: .bold))
                                .foregroundStyle(Theme.surface)
                        }
                    }
                    .frame(width: 36, height: 36)
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
                }
                .buttonStyle(.plain)
                .disabled(!canSend)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("type.send")
            }
            .padding(.leading, 18)
            .padding(.trailing, 4)
            .padding(.vertical, 2)
            .frame(minHeight: 48)
            .background(RoundedRectangle(cornerRadius: 24, style: .continuous).fill(Theme.card))
            .overlay(RoundedRectangle(cornerRadius: 24, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
        }
        .onChange(of: ui.focusComposer) { _, _ in focused = true }
    }

    private func submit() {
        guard canSend else { return }
        let sent = text
        Task {
            if await model.sendTyped(sent) { text = "" }
        }
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
