import Combine
import SheepdogKit
import SwiftUI
import UIKit

/// Pushed from an Agents card or the agent strip: what an agent's
/// navigation route carries. Only the pane id, so the window always shows
/// the agent's current row.
struct AgentWindowRoute: Hashable, Codable {
    let paneID: String
}

/// An agent's live terminal window (docs/dev/design/phone-terminal.md
/// section 5). The terminal is the screen, edge to edge; the chrome is a
/// slim header (name, state, ⋯), at most one notice line, the approval card
/// when the agent asks, a key bar and the prompt composer.
///
/// The ⋯ menu's Switch agent replaces the window with another agent's; the
/// `/term` connection stays (`TermHub`) and only the channel changes.
struct AgentScreenView: View {
    let agent: Agent
    /// On iPad: the window is a pane in the detail, with its own header.
    var pane: PaneChrome?
    @EnvironmentObject private var model: SessionViewModel
    @State private var paneID: String?

    var body: some View {
        let current = paneID.flatMap { id in model.flock.all.first { $0.paneID == id } } ?? agent
        AgentWindowView(agent: current, pane: pane) { id in
            if let switchTo = pane?.onSwitch { switchTo(id) } else { paneID = id }
        }
        .id(current.paneID)
    }
}

/// An agent window as an iPad pane (canvas "AgentPane"): its header is
/// drawn in the pane, not in a navigation bar, so two panes each have one.
struct PaneChrome {
    /// Before the title: Show sidebar and the mini capsule, when the
    /// sidebar is hidden.
    var leading: AnyView?
    /// The pane's ✕ (only when there are two).
    var onClose: (() -> Void)?
    /// "Show another agent beside this one": the agents it offers.
    var splitCandidates: [Agent] = []
    var onSplit: ((String) -> Void)?
    /// The focused pane of two: a 3 pt ink line on top.
    var focused = false
    /// The window ended (closed, gone): what Back does.
    var onGone: () -> Void
    /// ⋯ ▸ Switch agent shows it in this pane.
    var onSwitch: ((String) -> Void)?
    /// Extra ⋯ items (Open in New Window).
    var extraMenu: AnyView?
    /// A tap or a key in this pane focuses it.
    var onInteract: (() -> Void)?
}

private struct AgentWindowView: View {
    let agent: Agent
    let pane: PaneChrome?
    let onSwitch: (String) -> Void

    @EnvironmentObject private var model: SessionViewModel
    @StateObject private var window: AgentWindowController
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.openAgentWindow) private var openWindow
    @State private var prompt = ""
    @State private var showsMoreKeys = false
    @State private var action: AgentWindowAction?
    @State private var confirmTakeover = false
    @State private var showChanges = false
    @FocusState private var promptFocused: Bool

    init(agent: Agent, pane: PaneChrome?, onSwitch: @escaping (String) -> Void) {
        self.agent = agent
        self.pane = pane
        self.onSwitch = onSwitch
        _window = StateObject(wrappedValue: AgentWindowController(paneID: agent.paneID))
    }

    /// The agent as the app lists it now.
    private var live: Agent { model.flock.all.first { $0.paneID == agent.paneID } ?? agent }
    private var name: String { live.displayName }
    private var theme: TerminalTheme { colorScheme == .dark ? .dark : .light }
    private var background: Color { Color(theme.background) }
    /// The brand's code surface, a step off the background.
    private var noticeFill: Color { Color(TerminalRGB(hex: colorScheme == .dark ? "#1F2126" : "#F1EEE7")) }
    private var state: AgentWindowState { window.state }

    private var approval: ApprovalFlow.Card? {
        // Any open question of this agent, not only the one the voice is on.
        model.questionCard(for: agent.paneID)
    }

    var body: some View {
        if let pane {
            VStack(spacing: 0) {
                paneHeader(pane)
                content
            }
            .overlay(alignment: .top) {
                if pane.focused {
                    Rectangle().fill(Theme.ink).frame(height: 3)
                        .accessibilityHidden(true)
                }
            }
            .simultaneousGesture(TapGesture().onEnded { pane.onInteract?() })
            .toolbar(.hidden, for: .navigationBar)
        } else {
            content
                .navigationTitle(name)
                .navigationBarTitleDisplayMode(.inline)
                .toolbarBackground(background, for: .navigationBar)
                .toolbarBackground(.visible, for: .navigationBar)
                .toolbar {
                    ToolbarItem(placement: .principal) { header }
                    ToolbarItem(placement: .topBarTrailing) { menu }
                }
        }
    }

    /// Back: the iPhone pops the window; an iPad pane goes away.
    private func leave() {
        if let pane { pane.onGone() } else { dismiss() }
    }

    private var content: some View {
        VStack(spacing: 0) {
            if let notice = state.notice, !(state.phase == .ended && window.screenText.isEmpty) {
                NoticeBar(notice: notice, fill: noticeFill) { act(on: notice) }
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
            terminal
            if let approval {
                FloatingQuestions(cards: [approval], voiceQuestionID: model.approval?.pending.id) { id, index in model.answer(question: id, option: index) }
                    .padding(.horizontal, 12)
                    .padding(.top, 8)
                    .id(approval.pending.id)
            }
            controls
        }
        .background(background.ignoresSafeArea())
        .animation(Theme.ease, value: state.notice)
        .onAppear {
            window.surface.apply(theme: theme)
            window.attach(model: model)
            window.onActivity = pane?.onInteract
        }
        .onDisappear { window.detach() }
        .sheet(isPresented: $showChanges) {
            ChangesView(
                paneID: agent.paneID, name: name,
                load: { try await model.agentChanges(paneID: agent.paneID) },
                loadDiff: { try await model.agentDiff(paneID: agent.paneID, path: $0) }
            )
        }
        .onChange(of: colorScheme) { _, _ in window.surface.apply(theme: theme) }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .background: window.enteredBackground()
            case .active: window.becameActive()
            default: break
            }
        }
        .onChange(of: model.handshake) { _, _ in window.availabilityChanged() }
        .modifier(AgentActionDialogs(pending: $action, name: name, paneID: agent.paneID, window: window) { leave() })
        .sheet(isPresented: $showsMoreKeys) {
            MoreKeysSheet(window: window, interrupt: true) { snippet in
                if window.typing {
                    window.sendText(snippet)
                } else {
                    prompt = prompt.isEmpty ? snippet : prompt + " " + snippet
                    promptFocused = true
                }
            }
        }
        .onChange(of: window.state.takesRawInput) { _, live in
            if !live { window.setTyping(false) }
        }
        .confirmationDialog(
            "Take control of \(name) from the other device? It can only watch until it takes control back.",
            isPresented: $confirmTakeover,
            titleVisibility: .visible
        ) {
            Button("Take over") { window.takeOver() }
                .accessibilityIdentifier("agentScreen.takeoverConfirm")
            Button("Cancel", role: .cancel) {}
        }
    }

    // MARK: - Header

    /// "Work Mac · webapp" under the name, several computers only.
    private var computerLine: String? {
        guard model.showsComputerLabels else { return nil }
        return [model.currentComputerName, live.workspaceLabel?.trimmedOrNil].compactMap { $0 }.joined(separator: " · ")
    }

    private var header: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Text(name)
                    .font(.headline)
                    .lineLimit(1)
                    .truncationMode(.middle)
                StateChip(state: live.state)
            }
            if let computerLine {
                Text(computerLine)
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
                    .lineLimit(1)
                    .accessibilityIdentifier("computerLabel.window")
            }
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("agentScreen.header")
    }

    private var menu: some View {
        Menu {
            menuItems
        } label: {
            Image(systemName: "ellipsis.circle")
                .accessibilityLabel("More")
        }
        .accessibilityIdentifier("agentScreen.menu")
    }

    @ViewBuilder
    private var menuItems: some View {
            if let extra = pane?.extraMenu { extra }
            if state.phase == .observing {
                Button {
                    confirmTakeover = true
                } label: {
                    Label("Take over", systemImage: "hand.raised")
                }
                .accessibilityIdentifier("agentScreen.menu.takeover")
            }
            Button {
                UIPasteboard.general.string = window.surface.snapshotText()
                Haptics.tap()
            } label: {
                Label("Copy screen", systemImage: "doc.on.doc")
            }
            .accessibilityIdentifier("agentScreen.menu.copy")
            Button {
                showChanges = true
            } label: {
                Label("Changes", systemImage: "plusminus")
            }
            .accessibilityIdentifier("agentScreen.menu.changes")
            if model.shellEntryShown, let cwd = live.cwd, !cwd.isEmpty {
                Button {
                    openWindow(.shell(cwd: cwd))
                } label: {
                    Label("Open shell here", systemImage: "apple.terminal")
                }
                .accessibilityIdentifier("agentScreen.menu.shell")
            }
            Button {
                window.resetFontSize()
            } label: {
                Label("Reset text size", systemImage: "textformat.size")
            }
            .disabled(window.fontSize == SwiftTermSurface.defaultFontSize)
            .accessibilityIdentifier("agentScreen.menu.textSize")
            if model.agentActionsAvailable {
                AgentActionItems(working: live.state == .working) { action = $0 }
            }
            let others = model.flock.running.filter { $0.paneID != agent.paneID }
            if !others.isEmpty {
                Menu {
                    ForEach(others, id: \.paneID) { other in
                        Button {
                            onSwitch(other.paneID)
                        } label: {
                            Label(other.displayName, systemImage: other.state == .blocked ? "hand.raised.fill" : "terminal")
                        }
                    }
                } label: {
                    Label("Switch agent", systemImage: "rectangle.stack")
                }
            }
    }

    // MARK: - Pane header (iPad)

    /// Canvas "AgentPane": the dot and name over the space, centred; the
    /// sidebar button and mini capsule before; ✕, split and ⋯ after.
    private func paneHeader(_ pane: PaneChrome) -> some View {
        HStack(spacing: 0) {
            HStack(spacing: 6) {
                if let leading = pane.leading { leading }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            VStack(spacing: 0) {
                HStack(spacing: 6) {
                    Circle().fill(Theme.color(for: live.state)).frame(width: 8, height: 8)
                    Text(name)
                        .font(.headline)
                        .foregroundStyle(Theme.ink)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                if let place = computerLine ?? live.workspaceLabel?.trimmedOrNil {
                    Text(place)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(1)
                }
            }
            .layoutPriority(1)
            .accessibilityElement(children: .combine)
            .accessibilityLabel("\(name), \(Theme.word(for: live.state))")
            .accessibilityIdentifier("agentScreen.header")
            HStack(spacing: 2) {
                if let onClose = pane.onClose {
                    paneButton("xmark", label: "Close this pane", id: "pane.close", weight: .semibold, color: Theme.muted, action: onClose)
                }
                if let onSplit = pane.onSplit, !pane.splitCandidates.isEmpty {
                    Menu {
                        Section("Show beside \(name)") {
                            ForEach(pane.splitCandidates) { other in
                                Button {
                                    onSplit(other.paneID)
                                } label: {
                                    Label(other.alias, systemImage: other.state == .blocked ? "hand.raised" : "terminal")
                                }
                                .accessibilityIdentifier("pane.split.\(other.alias)")
                            }
                        }
                    } label: {
                        Image(systemName: "rectangle.split.2x1")
                            .font(.system(size: 17))
                            .foregroundStyle(Theme.ink)
                            .frame(width: 44, height: 44)
                            .contentShape(Circle())
                            .hoverEffect()
                    }
                    .accessibilityLabel("Show another agent beside this one")
                    .accessibilityIdentifier("pane.split")
                }
                Menu { menuItems } label: {
                    Image(systemName: "ellipsis")
                        .font(.system(size: 17, weight: .semibold))
                        .foregroundStyle(Theme.ink)
                        .frame(width: 44, height: 44)
                        .contentShape(Circle())
                        .hoverEffect()
                }
                .accessibilityLabel("Agent actions")
                .accessibilityIdentifier("agentScreen.menu")
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
        }
        .padding(.horizontal, 10)
        .frame(minHeight: 52)
        .background(background)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }

    private func paneButton(_ symbol: String, label: String, id: String, weight: Font.Weight = .regular, color: Color = Theme.ink, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 17, weight: weight))
                .foregroundStyle(color)
                .frame(width: 44, height: 44)
                .contentShape(Circle())
                .hoverEffect()
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    // MARK: - Terminal

    private var terminal: some View {
        ZStack(alignment: .bottomTrailing) {
            TerminalHost(surface: window.surface, accessibilityText: window.screenText)
                .padding(.horizontal, 6)
                .padding(.top, 4)
                .opacity(state.showsSkeleton ? 0 : 1)
            if state.showsSkeleton {
                TerminalSkeleton(name: name, reduceMotion: reduceMotion)
            } else if state.phase == .ended, window.screenText.isEmpty {
                // Closed before anything was drawn: say so in the middle,
                // not over an empty terminal.
                ContentUnavailableView {
                    Label(state.notice?.text ?? "This agent closed.", systemImage: "terminal")
                } description: {
                    Text("Its pane closed, or sheepdog no longer tracks it.")
                } actions: {
                    Button(pane == nil ? "Back to agents" : "Close") { leave() }
                        .accessibilityIdentifier("agentScreen.ended.back")
                }
                .background(background)
            }
            if state.showsLatest {
                Button {
                    window.latest()
                } label: {
                    Label("Latest", systemImage: "arrow.down")
                        .font(.footnote.weight(.semibold))
                        .padding(.horizontal, 12)
                        .padding(.vertical, 7)
                        .background(.regularMaterial, in: Capsule())
                }
                .buttonStyle(.plain)
                .padding(12)
                .accessibilityIdentifier("agentScreen.latest")
                .transition(.opacity)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(background)
        .animation(Theme.ease, value: state.showsLatest)
    }

    // MARK: - Controls

    private var controls: some View {
        VStack(alignment: .leading, spacing: 8) {
            KeyBar(window: window, onMore: { showsMoreKeys = true })
            if window.typing {
                Label("Typing goes straight to \(name).", systemImage: "keyboard")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .accessibilityIdentifier("agentScreen.typingHint")
            } else {
                TerminalComposer(
                    text: $prompt,
                    placeholder: state.phase == .observing ? "Take over to type" : "Ask \(name)…",
                    enabled: window.canOperate,
                    sending: window.sending,
                    canStop: live.state == .working,
                    focused: $promptFocused,
                    onSend: sendPrompt,
                    onStop: { window.stop(actions: model.agentActionsAvailable) },
                    leading: {
                        AttachButton(upload: { data, fileName, type in
                            try await model.uploadAttachment(paneID: agent.paneID, data: data, fileName: fileName, mimeType: type)
                        }, onAttached: { reply in
                            prompt = Attachments.inserting(reply.path, into: prompt)
                        })
                        .disabled(!window.canOperate)
                        .padding(.bottom, 2)
                    }
                )
            }
            if let error = window.inputError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("agentScreen.inputError")
            } else if let reply = window.lastReply {
                Text(reply)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("agentScreen.reply")
            }
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 6)
        .background(Color(.systemBackground).opacity(0.001))
        .overlay(alignment: .top) { Divider() }
    }

    private var canSendPrompt: Bool { window.canOperate && AgentScreenModel.canSend(prompt) }

    private func sendPrompt() {
        guard canSendPrompt else { return }
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            if await window.sendPrompt(text) { prompt = "" }
        }
    }

    private func act(on notice: AgentWindowState.Notice) {
        switch notice {
        case .otherDevice: confirmTakeover = true
        case .gone, .notTracked: leave()
        case .herdrDown, .tooMany: window.retry()
        case .snapshotOnly, .reconnecting: break
        case .shellOff, .shellFolder, .shellClosed: leave()
        }
    }
}

// MARK: - Pieces

/// The state dot and word.
private struct StateChip: View {
    let state: Agent.State

    var body: some View {
        HStack(spacing: 4) {
            Circle().fill(Theme.color(for: state)).frame(width: 7, height: 7)
            Text(Theme.word(for: state))
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.textColor(for: state))
        }
        .padding(.horizontal, 7)
        .padding(.vertical, 3)
        .background(Theme.color(for: state).opacity(0.14), in: Capsule())
        .accessibilityIdentifier("agentScreen.state")
    }
}

/// The one slim line under the header.
struct NoticeBar: View {
    let notice: AgentWindowState.Notice
    let fill: Color
    let action: () -> Void

    private var symbol: String {
        switch notice {
        case .otherDevice: return "eye"
        case .gone, .notTracked: return "xmark.circle"
        case .herdrDown: return "exclamationmark.triangle"
        case .snapshotOnly: return "camera.viewfinder"
        case .tooMany: return "square.stack.3d.up"
        case .reconnecting: return "arrow.triangle.2.circlepath"
        case .shellOff: return "lock"
        case .shellFolder: return "folder.badge.questionmark"
        case .shellClosed: return "xmark.circle"
        }
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            label
            Spacer(minLength: 4)
            button
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(fill)
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("agentScreen.notice")
    }

    private var label: some View {
        Label(notice.text, systemImage: symbol)
            .font(.footnote)
            .foregroundStyle(notice == .herdrDown ? Theme.blockedText : .primary)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("agentScreen.notice.text")
    }

    @ViewBuilder private var button: some View {
        if let title = notice.action {
            Button(title, action: action)
                .font(.footnote.weight(.semibold))
                .fixedSize()
                .foregroundStyle(Theme.primaryText)
                .accessibilityIdentifier("agentScreen.notice.action")
        }
    }
}

/// "Connecting…": a few placeholder rows where the terminal will be.
struct TerminalSkeleton: View {
    let name: String
    let reduceMotion: Bool
    var message: String?
    @State private var dim = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array([0.7, 0.45, 0.85, 0.3, 0.6].enumerated()), id: \.offset) { _, width in
                RoundedRectangle(cornerRadius: 4, style: .continuous)
                    .fill(Color(.tertiarySystemFill))
                    .frame(height: 10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .scaleEffect(x: width, y: 1, anchor: .leading)
            }
            Text(message ?? "Connecting to \(name)…")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .padding(.top, 6)
        }
        .padding(16)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .opacity(dim ? 0.5 : 1)
        .onAppear {
            guard !reduceMotion else { return }
            withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) { dim = true }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(message ?? "Connecting to \(name)")
        .accessibilityIdentifier("agentScreen.connecting")
    }
}

/// The renderer's view in SwiftUI.
struct TerminalHost: UIViewRepresentable {
    let surface: SwiftTermSurface
    let accessibilityText: String

    func makeUIView(context: Context) -> UIView {
        let view = surface.view
        view.accessibilityIdentifier = "agentScreen.terminal"
        return view
    }

    func updateUIView(_ view: UIView, context: Context) {
        view.accessibilityValue = accessibilityText
    }
}

extension Color {
    init(_ rgb: TerminalRGB) {
        self.init(red: Double(rgb.r) / 255, green: Double(rgb.g) / 255, blue: Double(rgb.b) / 255)
    }
}

// MARK: - The window's controller

/// Runs one window: holds the surface and the `AgentWindowState`, opens and
/// closes its `/term` channel on the shared `TermHub`, and draws the
/// `screen` snapshot in the same surface when the window cannot go live.
@MainActor
final class AgentWindowController: ObservableObject {
    static let fontSizeKey = "agentWindow.fontSize"

    let paneID: String
    /// A phone shell's window: opens `.newShell(cwd:)`, not an agent's pane.
    let isShell: Bool
    let shellCwd: String?
    /// The shell's pane, once the daemon opened it (for Close shell).
    @Published private(set) var shellPane: String?
    let surface: SwiftTermSurface
    @Published private(set) var state: AgentWindowState {
        didSet { surface.pasteEnabled = state.takesRawInput }
    }
    @Published private(set) var snapshot: AgentScreenModel
    /// The visible text, for VoiceOver's value.
    @Published private(set) var screenText = ""
    @Published private(set) var fontSize: CGFloat
    @Published var sending = false
    @Published var inputError: String?
    @Published var lastReply: String?
    /// The key bar's one-shot Ctrl and Alt.
    @Published var modifiers = KeyModifiers()
    /// Typing mode: the system keyboard types into the pane.
    @Published var typing = false

    weak var model: SessionViewModel?
    /// A tap, a key or a prompt in this window (iPad: focuses its pane).
    var onActivity: (() -> Void)?
    private var channel: Int?
    private var snapshotTask: Task<Void, Never>?
    private var pushes: AnyCancellable?

    init(paneID: String, shell: Bool = false, shellCwd: String? = nil) {
        self.paneID = paneID
        self.isShell = shell
        self.shellCwd = shellCwd
        let saved = UserDefaults.standard.double(forKey: Self.fontSizeKey)
        let size = saved > 0 ? CGFloat(saved) : SwiftTermSurface.defaultFontSize
        fontSize = size
        surface = TerminalRenderer.makeSurface(fontSize: size)
        // No cursor until herdr's first frame places one.
        surface.feed(Array("\u{1b}[?25l".utf8))
        state = AgentWindowState(paneID: paneID, isShell: shell)
        snapshot = AgentScreenModel(paneID: paneID)
        surface.onSizeChange = { [weak self] cols, rows in self?.sizeChanged(cols: cols, rows: rows) }
        surface.onScroll = { [weak self] lines in self?.dragged(lines: lines) }
        surface.onTap = { [weak self] col, row in self?.tapped(col: col, row: row) }
        surface.onInput = { [weak self] bytes in self?.typed(bytes) }
        surface.onFontSizeChange = { [weak self] size in self?.fontChanged(size) }
        surface.onContentChange = { [weak self] in self?.contentChanged() }
    }

    // MARK: Lifecycle

    func attach(model: SessionViewModel) {
        guard self.model == nil else { return }
        self.model = model
        pushes = model.$agentScreenPush.sink { [weak self] push in
            guard let self, let push, push.paneID == self.paneID, self.state.snapshotRunning else { return }
            switch push.event {
            case let .frame(frame): self.applySnapshot(frame)
            case let .error(message, _): self.snapshot.fail(message: message)
            }
        }
        // A size that arrived before the model did.
        // (Before its first layout the view still holds SwiftTerm's 80 × 24.)
        run(mutate { $0.start(model.termAvailability) })
        if surface.view.bounds.width > 0, surface.cols > 0, surface.rows > 0, state.cols == 0 {
            run(mutate { $0.sizeChanged(cols: surface.cols, rows: surface.rows) })
        }
    }

    func detach() {
        run(mutate { $0.disappeared() })
        pushes = nil
    }

    func enteredBackground() {
        run(mutate { $0.enteredBackground() })
        model?.termHub.disconnectIfIdle()
    }

    func becameActive() { run(mutate { $0.becameActive() }) }

    func availabilityChanged() {
        guard let model else { return }
        run(mutate { $0.availabilityChanged(model.termAvailability) })
    }

    func takeOver() { run(mutate { $0.takeOver() }) }
    func retry() { run(mutate { $0.retry() }) }
    func latest() { run(mutate { $0.latest() }) }

    // MARK: Input

    var canOperate: Bool {
        guard !sending else { return false }
        if state.phase == .snapshot { return snapshot.screen != nil && !snapshot.unsupported }
        return state.canOperate
    }

    /// A key-bar key: raw bytes on a live channel, else the daemon's
    /// allow-listed keys.
    func send(key: AgentKey) {
        onActivity?()
        if state.takesRawInput {
            sendRaw(key.bytes)
            Haptics.tap()
            return
        }
        guard canOperate, let model else { return }
        Task {
            beginSend()
            do {
                let reply = try await model.agentInput(paneID: paneID, keys: [key])
                finishSend(reply)
                Haptics.answered()
            } catch {
                finishSend(error)
            }
        }
    }

    /// The composer: always `POST /input` (it guarantees submission and
    /// tells the orchestrator). True when sent.
    func sendPrompt(_ text: String) async -> Bool {
        onActivity?()
        guard canOperate, let model else { return false }
        run(mutate { $0.willSendInput() })
        beginSend()
        do {
            let reply = try await model.agentInput(paneID: paneID, text: text)
            finishSend(reply)
            Haptics.answered()
            return true
        } catch {
            finishSend(error)
            return false
        }
    }

    func sendRaw(_ bytes: [UInt8]) {
        onActivity?()
        guard state.takesRawInput, let channel, let hub = model?.termHub else { return }
        run(mutate { $0.willSendInput() })
        hub.send(input: bytes, ch: channel)
    }

    private func beginSend() {
        sending = true
        inputError = nil
    }

    private func finishSend(_ reply: AgentInputReply) {
        sending = false
        lastReply = reply.spoken?.trimmedOrNil
        snapshot.finishSend(reply: reply)
    }

    private func finishSend(_ error: Error) {
        sending = false
        inputError = (error as? DaemonError)?.message ?? error.localizedDescription
        Haptics.answerFailed()
    }

    // MARK: Surface

    func resetFontSize() {
        surface.setFontSize(SwiftTermSurface.defaultFontSize)
        fontChanged(SwiftTermSurface.defaultFontSize)
    }

    private func fontChanged(_ size: CGFloat) {
        fontSize = size
        UserDefaults.standard.set(Double(size), forKey: Self.fontSizeKey)
    }

    private func sizeChanged(cols: Int, rows: Int) {
        // Before `attach`, which reads the surface's size itself.
        guard model != nil else { return }
        run(mutate { $0.sizeChanged(cols: cols, rows: rows) })
        if let channel { model?.termHub.resize(ch: channel, cols: cols, rows: rows) }
        if state.phase == .snapshot, !snapshot.text.isEmpty { drawSnapshot() }
    }

    private func dragged(lines: Int) { run(mutate { $0.dragged(lines: lines) }) }

    private func tapped(col: Int, row: Int) {
        onActivity?()
        guard state.takesRawInput, let channel else { return }
        model?.termHub.click(ch: channel, col: col, row: row)
    }

    private func contentChanged() {
        let text = surface.snapshotText()
        if text != screenText { screenText = text }
    }

    // MARK: Commands

    private func mutate(_ change: (inout AgentWindowState) -> [AgentWindowState.Command]) -> [AgentWindowState.Command] {
        var next = state
        let commands = change(&next)
        if next != state { state = next }
        return commands
    }

    private func run(_ commands: [AgentWindowState.Command]) {
        for command in commands {
            // A command's channel id is the one open when it was decided:
            // `.close` then `.open` replaces it in order.
            switch command {
            case let .open(mode, takeover):
                guard let hub = model?.termHub else { continue }
                let target: TermMessage.Target = isShell ? .newShell(cwd: shellCwd) : .pane(paneID)
                let ch = hub.open(target: target, mode: mode, cols: state.cols, rows: state.rows, takeover: takeover) { [weak self] event in
                    self?.handle(event)
                }
                if let ch {
                    channel = ch
                } else {
                    run(mutate { $0.closed(.refused("too_many")) })
                }
            case .close:
                if let channel { model?.termHub.close(ch: channel) }
                channel = nil
            case .startSnapshot:
                startSnapshot()
            case .stopSnapshot:
                stopSnapshot()
            case let .scroll(dir, lines):
                if let channel { model?.termHub.scroll(ch: channel, dir, lines: lines) }
            }
        }
    }

    private func handle(_ event: TermSocket.Event) {
        switch event {
        case let .state(socketState):
            if socketState == .reconnecting { run(mutate { $0.connectionLost() }) }
        case let .opened(_, opened):
            if isShell { shellPane = opened.paneID }
            _ = mutate { $0.opened(mode: opened.mode); return [] }
        case let .frame(frame):
            surface.feed(frame.bytes)
            run(mutate { $0.frame() })
        case let .closed(_, reason, canTakeOver):
            channel = nil
            run(mutate { $0.closed(reason, canTakeOver: canTakeOver) })
        case .free:
            run(mutate { $0.controlFreed() })
        case .error, .resync:
            break
        }
    }

    // MARK: Snapshot path

    /// Streams the `screen` window over `/ws` when the daemon can, else
    /// polls `GET /api/agents/{pane}/screen`, and draws it in the surface.
    private func startSnapshot() {
        guard snapshotTask == nil, let model else { return }
        let paneID = self.paneID
        snapshotTask = Task { [weak self] in
            model.openAgentScreen(paneID: paneID)
            defer { model.closeAgentScreen(paneID: paneID) }
            while !Task.isCancelled {
                if model.agentScreenStreaming(paneID: paneID) {
                    try? await Task.sleep(nanoseconds: 500_000_000)
                    continue
                }
                guard let self else { return }
                await self.refreshSnapshot()
                if self.snapshot.unsupported { return }
                try? await Task.sleep(nanoseconds: UInt64(AgentScreenModel.pollInterval * 1_000_000_000))
            }
        }
    }

    private func stopSnapshot() {
        snapshotTask?.cancel()
        snapshotTask = nil
    }

    private func refreshSnapshot() async {
        guard let model else { return }
        do {
            if let frame = try await model.agentScreen(paneID: paneID, hash: snapshot.hashForNextRead) {
                applySnapshot(frame)
            } else {
                snapshot.markUnsupported()
                feedMessage(AgentScreenModel.unsupportedMessage)
            }
        } catch is CancellationError {
            return
        } catch {
            snapshot.fail(error)
            if snapshot.text.isEmpty { feedMessage(snapshot.error ?? "No screen.") }
        }
    }

    private func applySnapshot(_ frame: AgentScreen) {
        let before = snapshot.revision
        snapshot.apply(frame)
        if snapshot.revision != before || screenText.isEmpty { drawSnapshot() }
    }

    private func drawSnapshot() {
        guard state.phase == .snapshot else { return }
        surface.feed(SnapshotScreen.bytes(text: snapshot.text, rows: surface.rows, ansi: snapshot.isANSI))
    }

    private func feedMessage(_ message: String) {
        guard state.phase == .snapshot else { return }
        surface.feed(SnapshotScreen.bytes(text: message, rows: surface.rows))
    }
}
