import SheepdogKit
import SwiftUI

/// The app's one screen, per window. It picks the layout (the iPhone
/// layout; on iPad at regular width the split view) and wraps it in what
/// every layout shares: the session's sheets, its dialogs and its
/// lifecycle (`SessionChrome`).
struct MainView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var ui = SceneUI()
    @Environment(\.horizontalSizeClass) private var sizeClass
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    /// The question collapse, the window zoom and the capsule morph.
    @Namespace private var motion

    private var split: Bool { sizeClass == .regular }

    /// The Conversation is in front, nothing over it.
    private var voiceScreen: Bool {
        split
            ? ui.detail == .conversation && ui.detailPath.isEmpty
            : model.pane == .conversation && model.navigationPath.isEmpty
    }

    var body: some View {
        Group {
            if split {
                IPadRootView()
            } else {
                PhoneRootView()
            }
        }
            .modifier(SessionChrome(ui: ui, voiceScreen: voiceScreen))
            .onChange(of: model.windowRequest) { _, request in
                // The window that showed the sheet opens it, once.
                guard let request, scenes.isPresenter(sceneID) else { return }
                model.windowRequest = nil
                open(request)
            }
            .background { KeyboardShortcuts(commands: commands) }
            // Outside the chrome, so its sheets (New agent, Spaces) see them.
            .environment(ui)
            .environment(\.motionNamespace, motion)
            .environment(\.openAgentWindow, OpenWindowAction { open($0) })
    }

    /// ⌘N, ⌘⇧M and ⌘1…9 (SheepdogCommands) in this window.
    private var commands: SessionCommands {
        SessionCommands(
            newAgent: model.agentActionsAvailable ? {
                scenes.request(from: sceneID)
                model.presentation.present(.newAgent)
            } : nil,
            toggleMute: VoiceBarModel(state: model.state, talkMode: model.talkMode).muteAvailability == .enabled
                ? { model.toggleMute() } : nil,
            muted: model.isMuted,
            agents: SidebarSections(computers: [.computer(id: model.currentComputerID, name: model.currentComputerName, agents: model.flock.all)]).agents,
            openAgent: { open(.agent($0)) },
            talkToNext: model.showsComputerLabels ? { model.talkToNext() } : nil
        )
    }

    /// The iPhone layout pushes a window on the main stack; the iPad
    /// shows it in the detail.
    private func open(_ request: WindowRequest) {
        if split {
            switch request {
            case let .agent(paneID):
                ui.detailPath = NavigationPath()
                ui.show(paneID)
            case let .newAgent(paneID):
                // A new agent opens beside the current pane.
                ui.detailPath = NavigationPath()
                ui.beside(paneID)
            case let .shell(cwd):
                ui.detailPath.append(ShellRoute(cwd: cwd))
            }
            return
        }
        switch request {
        case let .agent(paneID), let .newAgent(paneID):
            model.navigationPath.append(AgentWindowRoute(paneID: paneID))
        case let .shell(cwd):
            model.navigationPath.append(ShellRoute(cwd: cwd))
        }
    }
}

/// The iPhone layout: the top bar (Conversation | Agents, + and ⋯), the
/// agent strip, the conversation or the Agents pane, and the floating voice
/// capsule over them, which turns into the composer — everything in thumb
/// reach. iPad uses it too at compact width (Slide Over, a narrow split).
struct PhoneRootView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.motionNamespace) private var motionNamespace
    @Namespace private var fallbackNamespace

    private var motion: Namespace.ID { motionNamespace ?? fallbackNamespace }

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(link: model.link, hasAgents: !model.flock.isEmpty)
    }

    private var menus: SessionMenus { SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: sceneID) }

    var body: some View {
        NavigationStack(path: $model.navigationPath) {
            VStack(spacing: 0) {
                if model.isDemo {
                    DemoHeader()
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                }

                TopBar(
                    pane: $model.pane,
                    waiting: model.waitingEverywhere,
                    menus: menus
                )

                SessionBanners()

                if model.pane == .conversation, !model.flock.running.isEmpty || paneState == .empty || !model.blockedElsewhere.isEmpty {
                    // Another computer's chip names it (several computers only).
                    AgentStrip(agents: model.flock.running, pending: model.pendingAction, computers: model.computerCount,
                               elsewhere: model.blockedElsewhere, showElsewhere: { model.pane = .flock })
                        // Chrome: the chips grow up to the first
                        // accessibility size, then the strip stays one row.
                        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
                        .padding(.bottom, 2)
                }

                Rectangle().fill(Theme.line).frame(height: 1)

                Group {
                    switch model.pane {
                    case .conversation:
                        ConversationPane()
                    case .flock where paneState != .content && model.otherSnapshots.isEmpty:
                        FlockUnavailableView(state: paneState)
                    case .flock:
                        FlockView(
                            flock: model.flock,
                            question: { model.questionCard(for: $0) },
                            answer: { model.answer(question: $0, option: $1) },
                            expanded: $model.expandedAgents,
                            focusedPaneID: $model.focusedAgentPaneID,
                            onNewAgent: model.agentActionsAvailable ? { model.presentation.present(.newAgent) } : nil,
                            voiceQuestionID: model.approval?.pending.id,
                            others: model.otherSnapshots,
                            currentName: model.currentComputerName,
                            remoteCard: { model.remoteCard(computer: $0, paneID: $1) },
                            talkTo: { model.talk(to: $0) },
                            retry: { model.session($0)?.retry() }
                        )
                    }
                }
                .frame(maxHeight: .infinity)
            }
            .background(Theme.surface)
            // The question, the coach mark and the capsule float over the
            // content, which scrolls under them.
            .safeAreaInset(edge: .bottom, spacing: 0) { bottomStack }
            .animation(Theme.ease, value: model.banner)
            .animation(Theme.ease(reduceMotion: reduceMotion), value: model.floatingQuestions.map(\.pending.id))
            .animation(Theme.ease, value: model.showMuteHint)
            .animation(Theme.ease, value: model.showCoachMark)
            .animation(Theme.ease, value: model.pane)
            .toolbar(.hidden, for: .navigationBar)
            .navigationTitle(model.voiceName)
            .navigationDestination(for: ShellRoute.self) { route in
                ShellWindowView(route: route)
            }
            .navigationDestination(for: AgentWindowRoute.self) { route in
                Group {
                    if let agent = model.flock.all.first(where: { $0.paneID == route.paneID }) {
                        AgentScreenView(agent: agent)
                    } else {
                        AgentGoneView()
                    }
                }
                // The window zooms out of the chip or card it came from (a
                // plain push under Reduce Motion, or with no source).
                .modifier(WindowZoom(paneID: route.paneID, namespace: motion, reduceMotion: reduceMotion))
            }
        }
    }

    // MARK: - Bottom: the question, the coach mark, the capsule

    private var bottomStack: some View {
        VStack(spacing: 8) {
            // Every open question floats above the capsule; on the Agents
            // pane each sits on its agent's card instead.
            let cards = model.pane != .flock ? model.floatingQuestions : []
            if !cards.isEmpty {
                FloatingQuestions(
                    cards: cards,
                    voiceQuestionID: model.approval?.pending.id,
                    time: { question in
                        model.flock.all.first { $0.paneID == question.paneID }?.timeInState(at: Date())
                    },
                    compact: ui.typing,
                    remote: { model.remoteQuestion($0) },
                    talkTo: { model.talk(to: $0) }
                ) { id, index in
                    model.answer(question: id, option: index)
                }
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
            }

            if scenes.isOwner(sceneID) {
                if model.showCoachMark {
                    CoachMarkView(text: VoiceBarModel.coachMark(voiceName: model.voiceName)) {
                        model.dismissCoachMark()
                    }
                    .padding(.horizontal, 4)
                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                    .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                    .onAppear {
                        AccessibilityNotification.Announcement(VoiceBarModel.coachMark(voiceName: model.voiceName)).post()
                    }
                }

                ZStack {
                    if ui.typing {
                        CapsuleComposer(
                            voiceName: model.voiceName,
                            sending: model.sendingTyped,
                            error: model.typedError,
                            send: { await model.sendTyped($0) },
                            close: {
                                withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.typing = false }
                                model.typedError = nil
                            }
                        )
                        .modifier(MorphSurface(namespace: motion, reduceMotion: reduceMotion))
                        .transition(morph)
                    } else {
                        SessionCapsule(menus: menus)
                            .modifier(MorphSurface(namespace: motion, reduceMotion: reduceMotion))
                            .transition(morph)
                    }
                }
                // Grows with the text at every size; the capsule caps its own
                // height and scrolls inside past it.
            } else {
                // Another window owns the voice: this one gets the mini
                // capsule.
                MiniCapsule(menus: menus)
            }
        }
        .padding(.horizontal, 12)
        .padding(.bottom, 4)
    }

    /// The capsule turns into the composer and back: the shared frame
    /// morphs (`MorphSurface`) while the two cross-fade.
    private var morph: AnyTransition { .opacity }
}

/// The Conversation with the feedback flag and its notice: the same on
/// iPhone and in the iPad detail.
struct ConversationPane: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Rows that fold to a pointer while their question floats.
    private var pointerIDs: Set<String> {
        let open = model.openQuestionIDs
        return QuestionOrder.pointerRowIDs(rows: model.rows, open: model.questions.filter { open.contains($0.id) })
    }

    var body: some View {
        ConversationView(
            rows: model.rows,
            voiceName: model.voiceName,
            canLoadOlder: model.canLoadOlder,
            loadingOlder: model.loadingOlder,
            openPendingIDs: model.openQuestionIDs,
            pointerIDs: pointerIDs,
            loadOlder: { await model.loadOlder() },
            followToken: model.typedSent,
            ask: model.isConfigured ? { text in Task { _ = await model.sendTyped(text) } } : nil
        )
        // Mark this moment for debugging ("that paused"); only with a
        // daemon that takes marks.
        .overlay(alignment: .topTrailing) {
            if model.feedbackSupported {
                FeedbackButton { tag, note, at in
                    model.markFeedback(tag, note: note, at: at)
                }
                .padding(.top, 8)
                .padding(.trailing, 10)
            }
        }
        .overlay(alignment: .top) {
            if let notice = model.feedbackNotice {
                FeedbackNoticeView(text: notice)
                    .padding(.top, 8)
                    .transition(reduceMotion ? .opacity : .move(edge: .top).combined(with: .opacity))
            }
        }
        .animation(Theme.ease, value: model.feedbackNotice)
    }
}

/// The error banner or the update notice, under the top bar.
struct SessionBanners: View {
    @EnvironmentObject private var model: SessionViewModel
    /// The update notice the user put off ("Later"): hidden until the
    /// daemon reports a different one. Settings keeps showing it.
    @AppStorage("update.dismissedNotice") private var dismissedUpdate = ""
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if let banner = model.banner {
            BannerView(banner: banner, retry: { model.bannerAction() }, dismiss: { model.banner = nil })
                // Chrome stops growing at the largest non-accessibility
                // size: at AX5 a banner alone filled the screen. The
                // conversation keeps scaling.
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .padding(.horizontal, 16)
                .padding(.bottom, 8)
                .transition(reduceMotion ? .opacity : .move(edge: .top).combined(with: .opacity))
        } else if let notice = model.update?.notice, notice.text != dismissedUpdate {
            UpdateBanner(notice: notice) {
                withAnimation(Theme.ease(reduceMotion: reduceMotion)) { dismissedUpdate = notice.text }
            }
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .padding(.horizontal, 16)
            .padding(.bottom, 8)
            .transition(reduceMotion ? .opacity : .move(edge: .top).combined(with: .opacity))
        }
    }
}

/// The full voice capsule, wired to the session.
struct SessionCapsule: View {
    let menus: SessionMenus
    /// The keyboard button: the iPhone morphs the capsule into the
    /// composer; the iPad focuses the Conversation's composer.
    var onType: (() -> Void)?
    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        VoiceCapsule(
            state: model.state,
            meter: model.meter,
            muted: model.isMuted,
            name: model.capsuleName,
            talkMode: model.talkMode,
            holding: model.holding,
            noSignal: model.noMicSignal,
            // One accent per screen: any open question quiets the orb.
            questionOpen: !model.openQuestionIDs.isEmpty,
            // The one-time Mute caption takes the status line's place.
            statusText: model.showMuteHint ? model.muteCaption : model.statusLine,
            canType: model.isConfigured,
            onConnect: { model.connect() },
            onStop: { model.stop() },
            onMute: {
                model.dismissCoachMark()
                model.toggleMute()
            },
            onResume: { model.resumeAudio() },
            onPress: { model.pressToTalk() },
            onRelease: { model.releaseToTalk() },
            onType: {
                if let onType {
                    onType()
                } else {
                    withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.typing = true }
                }
            },
            menuValue: menus.voiceValue,
            menu: { menus.voiceItems($0) }
        )
    }
}

// MARK: - Menus

/// The session's menus, the same in every layout: the capsule's voice
/// menu, `+` and `⋯`. A menu item that opens a session sheet asks for it
/// in this window (`SceneCoordinator.request`).
@MainActor
struct SessionMenus {
    let model: SessionViewModel
    let ui: SceneUI
    let scenes: SceneCoordinator
    let sceneID: UUID?

    private func here(_ open: () -> Void) {
        scenes.request(from: sceneID)
        open()
    }

    /// What the capsule's menu shows. With one computer: this computer's
    /// connection, Manage computers… and End voice session. Talk to (the
    /// other computers) arrives with several computers.
    struct VoiceValue: Equatable {
        struct Computer: Equatable, Identifiable {
            var id: String
            var name: String
            var isCurrent: Bool
            var offline: Bool
            var summary: String
        }

        var connectionWord: String
        var endpoint: String
        var connectionSymbol: String
        /// Empty with one computer.
        var computers: [Computer]
        var wantsConnection: Bool
        var isConfigured: Bool
    }

    var voiceValue: VoiceValue {
        VoiceValue(
            connectionWord: model.linkPresentation.chip,
            endpoint: model.endpointDisplay,
            connectionSymbol: connectionSymbol,
            computers: model.showsComputerLabels ? model.computerSnapshots.map { computer in
                // Not offline yet (the quiet period still reads
                // "Connecting…"), but backing off: not picked either.
                .init(id: computer.id, name: computer.name, isCurrent: computer.isCurrent, offline: !computer.canTalkTo, summary: computer.summary())
            } : [],
            wantsConnection: model.wantsConnection,
            isConfigured: model.isConfigured
        )
    }

    /// The capsule's menu items, drawn from `voiceValue` only, so an open
    /// menu stays still while the session re-renders (`StableMenu`).
    @ViewBuilder
    func voiceItems(_ value: VoiceValue) -> some View {
        Button {
            here { model.presentation.present(.connection) }
        } label: {
            Label {
                Text(value.connectionWord)
                Text(value.endpoint)
            } icon: {
                Image(systemName: value.connectionSymbol)
            }
        }
        .accessibilityLabel("Connection: \(value.connectionWord)")
        .accessibilityIdentifier("chip.connection")
        if !value.computers.isEmpty {
            // Talk to: every computer, the current one checked; one that
            // does not answer is disabled with its last-seen time.
            Divider()
            Group {
                ForEach(value.computers) { computer in
                    Button {
                        model.talk(to: computer.id)
                    } label: {
                        Label {
                            Text(computer.isCurrent ? computer.name : "Talk to \(computer.name)")
                            Text(computer.isCurrent ? "Talking here · \(computer.summary)" : computer.summary)
                        } icon: {
                            Image(systemName: computer.isCurrent ? "checkmark" : (computer.offline ? "wifi.slash" : "desktopcomputer"))
                        }
                    }
                    .disabled(computer.offline && !computer.isCurrent)
                    .accessibilityLabel(computer.isCurrent ? "\(computer.name), talking here, \(computer.summary)" : "Talk to \(computer.name), \(computer.summary)")
                    .accessibilityIdentifier("voice.talkTo.\(computer.id)")
                }
            }
        }
        Button {
            here { model.presentation.present(.settings) }
        } label: {
            Label("Manage computers…", systemImage: "desktopcomputer")
        }
        .accessibilityIdentifier("voice.manageComputers")
        Divider()
        if value.wantsConnection {
            Button {
                model.disconnect()
            } label: {
                Label {
                    Text("End voice session")
                    Text("Your agents keep working.")
                } icon: {
                    Image(systemName: "phone.down")
                }
            }
            .accessibilityIdentifier("controls.end")
        } else {
            Button {
                model.connect()
            } label: {
                Label("Start voice session", systemImage: "waveform")
            }
            .disabled(!value.isConfigured)
            .accessibilityIdentifier("voice.start")
        }
    }

    private var connectionSymbol: String { model.linkPresentation.tint.symbol }

    /// What `+` and `⋯` show.
    struct BarValue: Equatable {
        var actionsAvailable: Bool
        var shellShown: Bool
        var resetSupported: Bool
        var voiceName: String
        var wantsConnection: Bool
    }

    var barValue: BarValue {
        BarValue(
            actionsAvailable: model.agentActionsAvailable,
            shellShown: model.shellEntryShown,
            resetSupported: model.conversationResetSupported,
            voiceName: model.voiceName,
            wantsConnection: model.wantsConnection
        )
    }

    /// `+`: make something on this computer.
    @ViewBuilder
    func newItems(_ value: BarValue) -> some View {
        Button {
            here { model.presentation.present(.newAgent) }
        } label: {
            Label("New agent", systemImage: "plus.square")
        }
        .disabled(!value.actionsAvailable)
        .accessibilityIdentifier("new.agent")
        NewShellButton(shown: value.shellShown)
    }

    /// `⋯`: the session's rarer actions, then Settings.
    @ViewBuilder
    func moreItems(_ value: BarValue) -> some View {
        if value.resetSupported {
            Button {
                ui.confirmNewConversation = true
            } label: {
                Label("New conversation", systemImage: "arrow.counterclockwise")
            }
            .accessibilityIdentifier("toolbar.newConversation")
        }
        // The orb stops the voice while it talks; this is the same
        // Stop, reachable whatever the orb is doing.
        Button {
            model.stop()
        } label: {
            Label("Stop \(value.voiceName)", systemImage: "stop.circle")
        }
        .disabled(!value.wantsConnection)
        .accessibilityIdentifier("toolbar.stop")
        Button {
            model.disconnect()
        } label: {
            Label("End voice session", systemImage: "phone.down")
        }
        .disabled(!value.wantsConnection)
        .accessibilityIdentifier("toolbar.end")
        Divider()
        Button {
            here { model.presentation.present(.spaces) }
        } label: {
            Label("Spaces", systemImage: "square.stack")
        }
        .disabled(!value.actionsAvailable)
        .accessibilityIdentifier("toolbar.spaces")
        Button {
            here { model.presentation.present(.settings) }
        } label: {
            Label("Settings", systemImage: "gearshape")
        }
        .accessibilityIdentifier("toolbar.settings")
    }
}

/// `+` ▸ New shell: opens in this layout's way (`openAgentWindow`).
private struct NewShellButton: View {
    /// The daemon offers the shell (`shellEntryShown`).
    let shown: Bool
    @Environment(\.openAgentWindow) private var openWindow

    var body: some View {
        Button {
            openWindow(.shell(cwd: nil))
        } label: {
            if shown {
                Label("New shell", systemImage: "apple.terminal")
            } else {
                Label {
                    Text("New shell")
                    Text("The shell is off on this computer.")
                } icon: {
                    Image(systemName: "apple.terminal")
                }
            }
        }
        .disabled(!shown)
        .accessibilityIdentifier("new.shell")
    }
}

// MARK: - What every layout shares

/// The session's sheets, dialogs and lifecycle, around either layout.
/// Session sheets show in one window only (`SceneCoordinator.presenter`);
/// the audio owner reports the screen and runs the session's foreground
/// and background.
struct SessionChrome: ViewModifier {
    let ui: SceneUI
    /// The layout shows the Conversation with nothing over it.
    let voiceScreen: Bool

    @EnvironmentObject private var model: SessionViewModel
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    @Environment(\.appPhase) private var appPhase
    @Environment(\.splashShowing) private var splashShowing

    private var presents: Bool { scenes.isPresenter(sceneID) }
    private var owns: Bool { scenes.isOwner(sceneID) }

    /// What the daemon is told for cards (`visibility`): the app is in
    /// the foreground, and the voice screen is in front — the
    /// conversation, nothing pushed, no sheet or cover over it. A card's
    /// own sheet counts as the voice screen, so the next card can replace
    /// it.
    private var screenReport: ScreenReport {
        let shown = voiceScreen
            && !model.showSettings
            && !model.demoActive
            && model.screenCovers.isEmpty
        return ScreenReport(visible: appPhase == .active, voiceScreen: shown)
    }

    private var presentation: PresentationCoordinator { model.presentation }

    /// The coordinator's item for this window: only the presenter shows
    /// it, and Settings waits for the splash to fade instead of sliding
    /// over it. A swipe or an in-sheet Done is a dismissal intent.
    private func binding(covers: Bool) -> Binding<Presentation?> {
        Binding(
            get: {
                guard presents, let item = presentation.current, item.isCover == covers else { return nil }
                if item == .settings, splashShowing { return nil }
                return item
            },
            set: { value in
                if value == nil, let item = presentation.current, item.isCover == covers {
                    presentation.dismiss(item)
                }
            }
        )
    }

    @ViewBuilder
    private func presented(_ item: Presentation) -> some View {
        switch item {
        case .settings:
            SettingsView(meter: model.meter)
                .environmentObject(model)
                // A form sheet on iPad; the iPhone ignores it.
                .presentationSizing(.form)
        case let .pairing(stage):
            // A pairing code from outside the app (asked about first), or
            // "Please scan again".
            PairingFlowView(stage: stage)
                .environmentObject(model)
        case let .card(card):
            CardSheet(card: card)
                .environmentObject(model)
        case .newAgent:
            NewAgentSheet().presentationSizing(.form)
        case .spaces:
            SpacesSheet().presentationSizing(.form)
        case .connection:
            ConnectionDetailView(
                showSettings: {
                    scenes.request(from: sceneID)
                    presentation.presentInstead(.settings)
                },
                showModels: {
                    scenes.request(from: sceneID)
                    model.settingsRoute = .models
                    presentation.presentInstead(.settings)
                }
            )
            .environmentObject(model)
            .coversVoiceScreen("connection")
            // One detent: a multi-detent sheet over a Form hung the
            // main thread ("onChange(of: Bool) … multiple times per
            // frame") during manual testing.
            .presentationDetents([.large])
        case let .primer(primer):
            PermissionPrimerView(primer: primer) { model.answerPrimer($0) }
        case .consent:
            AIConsentView(model: model)
        case .demo:
            // The offline demo: a second view model on a scripted daemon
            // inside the app, over the real one.
            DemoShell(close: { model.demoClosed() })
        }
    }

    func body(content: Content) -> some View {
        content
            .confirmationDialog(
                "Start a new conversation?",
                isPresented: Bindable(ui).confirmNewConversation,
                titleVisibility: .visible
            ) {
                Button("Start fresh") { model.startNewConversation() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Sheepdog and the orchestrator forget this conversation; your agents, open questions and 'tell me when…' reminders stay.")
            }
            // Every session sheet and cover, one at a time
            // (`PresentationCoordinator`): the next one shows once this
            // one has finished closing.
            .sheet(item: binding(covers: false), onDismiss: { presentation.didDismiss() }) { item in
                presented(item)
                    .onAppear { presentation.appeared(item) }
            }
            .fullScreenCover(item: binding(covers: true), onDismiss: { presentation.didDismiss() }) { item in
                presented(item)
                    .onAppear { presentation.appeared(item) }
            }
            .onChange(of: screenReport, initial: true) { _, report in
                guard owns else { return }
                model.setScreen(visible: report.visible, voiceScreen: report.voiceScreen)
            }
            .onAppear { model.start() }
            .onChange(of: appPhase) { _, phase in
                guard owns else { return }
                switch phase {
                case .active: model.becameActive()
                case .background: model.enteredBackground()
                default: break
                }
            }
            .onChange(of: model.pane) { _, _ in model.warmUpTerm() }
            .environment(\.openCard) { card in model.openCard(card) }
            #if DEBUG
            // Over the layout and any pushed window (the demo's too).
            .overlay(alignment: .topLeading) { RenderProbeReadout() }
            #endif
            .environment(\.reportScreenCover) { name, covering in model.setScreenCover(name, covering) }
    }
}

/// The top bar (canvas "TopBar"): `Conversation | Agents` with the waiting
/// badge, and the `+` and `⋯` menus in one pill. No title, no connection
/// chip: the connection lives in the capsule.
struct TopBar: View {
    @Binding var pane: MainPane
    let waiting: Int
    let menus: SessionMenus

    var body: some View {
        HStack(spacing: 8) {
            PaneSwitcher(pane: $pane, waiting: waiting)
                .fixedSize(horizontal: true, vertical: false)
            Spacer(minLength: 0)
            HStack(spacing: 0) {
                // From values only (`StableMenu`): an open menu stays
                // still while the session re-renders.
                stableMenu(menus.barValue, probe: "new", content: { menus.newItems($0) }) { _ in
                    Image(systemName: "plus")
                        .font(.system(size: 17, weight: .semibold))
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("New")
                .accessibilityIdentifier("toolbar.new")
                stableMenu(menus.barValue, probe: "more", content: { menus.moreItems($0) }) { _ in
                    Image(systemName: "ellipsis")
                        .font(.system(size: 17, weight: .semibold))
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("More")
                .accessibilityIdentifier("toolbar.more")
            }
            .foregroundStyle(Theme.ink)
            .background(Capsule().fill(Theme.card))
            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
        }
        // Chrome stops at the largest non-accessibility size.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .padding(.leading, 16)
        .padding(.trailing, 12)
        .padding(.vertical, 6)
    }
}

/// The screen as `visibility` reports it.
private struct ScreenReport: Equatable {
    var visible: Bool
    var voiceScreen: Bool
}

/// The first-run coach mark: a bubble pointing down at the mic button.
struct CoachMarkView: View {
    let text: String
    let dismiss: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "waveform")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(.white)
                    .padding(.top, 1)
                Text(text)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.white)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
                Button(action: dismiss) {
                    Text("Got it")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(.white)
                        .frame(minWidth: 44, minHeight: 44)
                        .contentShape(Rectangle())
                }
                    .buttonStyle(.plain)
                    .padding(.vertical, -10)
                    .accessibilityIdentifier("controls.coachMark.dismiss")
            }
            .padding(12)
            .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Theme.done))
            // The pointer, over the mic button (the first slot of the bar).
            Triangle()
                .fill(Theme.done)
                .frame(width: 18, height: 9)
                .padding(.leading, 17)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("controls.coachMark")
        .onTapGesture(perform: dismiss)
    }
}

private struct Triangle: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        p.move(to: CGPoint(x: rect.minX, y: rect.minY))
        p.addLine(to: CGPoint(x: rect.maxX, y: rect.minY))
        p.addLine(to: CGPoint(x: rect.midX, y: rect.maxY))
        p.closeSubpath()
        return p
    }
}

/// The offline demo's label, its way out, and the questions it understands
/// (also by typing, and by voice where on-device recognition is available).
struct DemoHeader: View {
    @EnvironmentObject private var model: SessionViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Label("Demo", systemImage: "play.circle.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.primaryText)
                Text("Scripted agents, on this device only")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: 4)
                Button("Leave") { model.leaveDemo() }
                    .font(.subheadline.weight(.medium))
                    .tint(Theme.primaryText)
                    .accessibilityIdentifier("demo.leave")
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    // While a worker asks, the way to answer it aloud: what
                    // on-device recognition would hand the demo when you say it.
                    if let card = model.approval, !card.isSettled {
                        Button(DemoWorld.sayYes) { model.askDemo(DemoWorld.sayYes) }
                            .font(.footnote.weight(.medium))
                            .buttonStyle(.borderedProminent)
                            .buttonBorderShape(.capsule)
                            .tint(Theme.primaryText)
                            .disabled(model.sendingTyped)
                            .accessibilityIdentifier("demo.sayYes")
                    }
                    ForEach(Array(DemoWorld.suggestedQuestions.enumerated()), id: \.offset) { index, question in
                        Button(question) { model.askDemo(question) }
                            .font(.footnote.weight(.medium))
                            .buttonStyle(.bordered)
                            .buttonBorderShape(.capsule)
                            .tint(Theme.primaryText)
                            .disabled(model.sendingTyped)
                            .accessibilityIdentifier("demo.question.\(index)")
                    }
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(Color.accentColor.opacity(0.08))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("demo.header")
    }
}

/// The capsule and the composer share one frame, so one morphs into the
/// other (its size and corners) while their contents cross-fade. A fade
/// only under Reduce Motion.
private struct MorphSurface: ViewModifier {
    let namespace: Namespace.ID
    let reduceMotion: Bool

    func body(content: Content) -> some View {
        if reduceMotion {
            content
        } else {
            content.matchedGeometryEffect(id: "capsule", in: namespace)
        }
    }
}

/// The agent window zooms out of its source (a plain push under Reduce
/// Motion).
private struct WindowZoom: ViewModifier {
    let paneID: String
    let namespace: Namespace.ID
    let reduceMotion: Bool

    func body(content: Content) -> some View {
        if reduceMotion {
            content
        } else {
            content.navigationTransition(.zoom(sourceID: "window." + paneID, in: namespace))
        }
    }
}
