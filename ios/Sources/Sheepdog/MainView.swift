import SheepdogKit
import SwiftUI

/// One screen: the top bar (Conversation | Agents, + and ⋯), the agent strip,
/// the conversation or the Agents pane, and the floating voice capsule over
/// them, which turns into the composer — everything in thumb
/// reach.
struct MainView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var started = false
    @State private var confirmNewConversation = false
    @State private var showsNewAgent = false
    @State private var typing = false
    @State private var showsConnection = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.scenePhase) private var scenePhase

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(chip: model.connectionChip.status, hasAgents: !model.flock.isEmpty)
    }

    /// What the daemon is told for cards (`visibility`): the app is in
    /// the foreground, and the voice screen is in front — the conversation
    /// pane, nothing pushed, no sheet or cover over it. A card's own sheet
    /// counts as the voice screen, so the next card can replace it.
    private var screenReport: ScreenReport {
        let voiceScreen = model.pane == .conversation
            && model.navigationPath.isEmpty
            && !model.showSettings
            && !model.demoActive
            && model.screenCovers.isEmpty
        return ScreenReport(visible: scenePhase == .active, voiceScreen: voiceScreen)
    }

    var body: some View {
        NavigationStack(path: $model.navigationPath) {
            VStack(spacing: 0) {
                if model.isDemo {
                    DemoHeader()
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                }

                TopBar(
                    pane: $model.pane,
                    waiting: model.flock.waitingCount,
                    newMenu: { newMenu },
                    moreMenu: { moreMenu }
                )

                if let banner = model.banner {
                    BannerView(banner: banner, retry: { model.bannerAction() }, dismiss: { model.banner = nil })
                        // Chrome stops growing at the largest non-
                        // accessibility size: at AX5 a banner alone filled
                        // the screen. The conversation keeps scaling.
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                        .padding(.horizontal, 16)
                        .padding(.bottom, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }

                if model.pane == .conversation, !model.flock.running.isEmpty || paneState == .empty {
                    AgentStrip(agents: model.flock.running, pending: model.pendingAction)
                        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
                        .padding(.bottom, 8)
                }

                Rectangle().fill(Theme.line).frame(height: 1)

                Group {
                    switch model.pane {
                    case .conversation:
                        ConversationView(
                            rows: model.rows,
                            voiceName: model.voiceName,
                            canLoadOlder: model.canLoadOlder,
                            loadingOlder: model.loadingOlder,
                            activePendingID: model.approval?.pending.id,
                            pointerIDs: QuestionFocus.pointerRowIDs(rows: model.rows, pending: model.approval.flatMap { $0.isSettled ? nil : $0.pending }),
                            loadOlder: { await model.loadOlder() },
                            followToken: model.typedSent
                        )
                    case .flock where paneState != .content:
                        FlockUnavailableView(state: paneState)
                    case .flock:
                        FlockView(flock: model.flock, approval: model.approval, focusedPaneID: $model.focusedAgentPaneID,
                                  answer: { model.answer(option: $0) })
                    }
                }
                .frame(maxHeight: .infinity)
                // Mark this moment for debugging ("that paused"); only
                // with a daemon that takes marks.
                .overlay(alignment: .topTrailing) {
                    if model.pane == .conversation, model.feedbackSupported {
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
                            .transition(.move(edge: .top).combined(with: .opacity))
                    }
                }
                .animation(.easeInOut(duration: 0.2), value: model.feedbackNotice)
            }
            .background(Theme.surface)
            // The question, the coach mark and the capsule float over the
            // content, which scrolls under them.
            .safeAreaInset(edge: .bottom, spacing: 0) { bottomStack }
            .animation(.easeInOut(duration: 0.2), value: model.banner)
            .animation(.spring(duration: 0.3), value: model.approval)
            .animation(.easeInOut(duration: 0.2), value: model.showMuteHint)
            .animation(.spring(duration: 0.35), value: model.showCoachMark)
            .animation(.easeInOut(duration: 0.2), value: model.pane)
            .toolbar(.hidden, for: .navigationBar)
            .navigationTitle(model.voiceName)
            .navigationDestination(for: ShellRoute.self) { route in
                ShellWindowView(route: route)
            }
            .navigationDestination(for: AgentWindowRoute.self) { route in
                if let agent = model.flock.all.first(where: { $0.paneID == route.paneID }) {
                    AgentScreenView(agent: agent)
                } else {
                    AgentGoneView()
                }
            }
            .confirmationDialog(
                "Start a new conversation?",
                isPresented: $confirmNewConversation,
                titleVisibility: .visible
            ) {
                Button("Start fresh") { model.startNewConversation() }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Sheepdog and the orchestrator forget this conversation; your agents, open questions and 'tell me when…' reminders stay.")
            }
            // sheepdog://pair from the Camera app: ask before applying.
            .alert(
                model.pendingPairing?.confirmation(currentAddress: model.settings.address).title ?? "",
                isPresented: Binding(
                    get: { model.pendingPairing != nil },
                    set: { if !$0, model.pendingPairing != nil { model.cancelPairing() } }
                ),
                presenting: model.pendingPairing
            ) { _ in
                Button("Pair") { model.confirmPairing() }
                    .accessibilityIdentifier("pairing.confirm")
                Button("Cancel", role: .cancel) { model.cancelPairing() }
            } message: { link in
                Text(link.confirmation(currentAddress: model.settings.address).message)
            }
            .sheet(isPresented: $model.showSettings) {
                SettingsView(meter: model.meter)
                    .environmentObject(model)
            }
            .sheet(isPresented: $showsNewAgent) { NewAgentSheet() }
            .sheet(isPresented: $showsConnection) {
                ConnectionDetailView(
                    showSettings: {
                        showsConnection = false
                        model.showSettings = true
                    },
                    showModels: {
                        showsConnection = false
                        model.openModels()
                    }
                )
                .environmentObject(model)
                .coversVoiceScreen("connection")
                // One detent: a multi-detent sheet over a Form hung the
                // main thread ("onChange(of: Bool) … multiple times per
                // frame") during manual testing.
                .presentationDetents([.large])
            }
            .sheet(item: $model.presentedCard) { card in
                CardSheet(card: card)
                    .environmentObject(model)
            }
            .onChange(of: screenReport, initial: true) { _, report in
                model.setScreen(visible: report.visible, voiceScreen: report.voiceScreen)
            }
            .onAppear {
                guard !started else { return }
                started = true
                model.start()
            }
            .onChange(of: scenePhase) { _, phase in
                switch phase {
                case .active where started: model.becameActive()
                case .background: model.enteredBackground()
                default: break
                }
            }
            .onChange(of: model.pane) { _, _ in model.warmUpTerm() }
        }
        .environment(\.openCard) { card in model.openCard(card) }
        .environment(\.reportScreenCover) { name, covering in model.setScreenCover(name, covering) }
    }

    // MARK: - Bottom: the question, the coach mark, the capsule

    private var bottomStack: some View {
        VStack(spacing: 8) {
            // On the Agents pane the question sits on its agent's card.
            if model.pane != .flock, let card = model.approval {
                PinnedApprovalCard(card: card) { index in
                    model.answer(option: index)
                }
                .transition(.move(edge: .bottom).combined(with: .opacity))
                .id(card.pending.id)
                .onAppear {
                    AccessibilityNotification.Announcement("\(card.pending.agent ?? "An agent") needs your answer. \(card.pending.displaySummary)").post()
                }
            }

            if model.showCoachMark {
                CoachMarkView(text: VoiceBarModel.coachMark(voiceName: model.voiceName)) {
                    model.dismissCoachMark()
                }
                .padding(.horizontal, 4)
                .transition(.move(edge: .bottom).combined(with: .opacity))
                .onAppear {
                    AccessibilityNotification.Announcement(VoiceBarModel.coachMark(voiceName: model.voiceName)).post()
                }
            }

            ZStack {
                if typing {
                    CapsuleComposer(
                        voiceName: model.voiceName,
                        sending: model.sendingTyped,
                        error: model.typedError,
                        send: { await model.sendTyped($0) },
                        close: {
                            withAnimation(Theme.ease(reduceMotion: reduceMotion)) { typing = false }
                            model.typedError = nil
                        }
                    )
                    .transition(morph)
                } else {
                    capsule
                        .transition(morph)
                }
            }
            .dynamicTypeSize(...DynamicTypeSize.accessibility3)
        }
        .padding(.horizontal, 12)
        .padding(.bottom, 4)
    }

    /// The capsule turns into the composer and back: a short scale and
    /// fade, only a fade under Reduce Motion.
    private var morph: AnyTransition {
        reduceMotion ? .opacity : .scale(scale: 0.96, anchor: .bottom).combined(with: .opacity)
    }

    private var capsule: some View {
        VoiceCapsule(
            state: model.state,
            meter: model.meter,
            muted: model.isMuted,
            name: model.voiceName,
            talkMode: model.talkMode,
            holding: model.holding,
            noSignal: model.noMicSignal,
            questionOpen: model.approval.map { !$0.isSettled } ?? false,
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
            onType: { withAnimation(Theme.ease(reduceMotion: reduceMotion)) { typing = true } },
            menu: { voiceMenu }
        )
    }

    // MARK: - Menus

    /// The capsule's menu. With one computer: this computer's connection,
    /// Manage computers…, and End voice session. Talk to (the other
    /// computers) arrives with several computers.
    @ViewBuilder
    private var voiceMenu: some View {
        Section {
            Button {
                showsConnection = true
            } label: {
                Label {
                    Text(model.connectionChip.word)
                    Text(model.endpointDisplay)
                } icon: {
                    Image(systemName: connectionSymbol)
                }
            }
            .accessibilityLabel("Connection: \(model.connectionChip.word)")
            .accessibilityIdentifier("chip.connection")
            Button {
                model.showSettings = true
            } label: {
                Label("Manage computers…", systemImage: "desktopcomputer")
            }
            .accessibilityIdentifier("voice.manageComputers")
        }
        Section {
            if model.wantsConnection {
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
                .disabled(!model.isConfigured)
                .accessibilityIdentifier("voice.start")
            }
        }
    }

    private var connectionSymbol: String {
        switch model.connectionChip.tint {
        case .green: return "checkmark.circle"
        case .amber: return "arrow.triangle.2.circlepath"
        case .red: return "wifi.slash"
        case .grey: return "circle.dashed"
        }
    }

    /// `+`: make something on this computer.
    @ViewBuilder
    private var newMenu: some View {
        Button {
            showsNewAgent = true
        } label: {
            Label("New agent", systemImage: "plus.square")
        }
        .disabled(!model.agentActionsAvailable)
        .accessibilityIdentifier("new.agent")
        Button {
            model.navigationPath.append(ShellRoute(cwd: nil))
        } label: {
            if model.shellEntryShown {
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
        .disabled(!model.shellEntryShown)
        .accessibilityIdentifier("new.shell")
    }

    /// `⋯`: the session's rarer actions, then Settings.
    @ViewBuilder
    private var moreMenu: some View {
        Section {
            if model.conversationResetSupported {
                Button {
                    confirmNewConversation = true
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
                Label("Stop \(model.voiceName)", systemImage: "stop.circle")
            }
            .disabled(!model.wantsConnection)
            .accessibilityIdentifier("toolbar.stop")
            Button {
                model.disconnect()
            } label: {
                Label("End voice session", systemImage: "phone.down")
            }
            .disabled(!model.wantsConnection)
            .accessibilityIdentifier("toolbar.end")
        }
        Section {
            Button {
                model.showSettings = true
            } label: {
                Label("Settings", systemImage: "gearshape")
            }
            .accessibilityIdentifier("toolbar.settings")
        }
    }
}

/// The top bar (canvas "TopBar"): `Conversation | Agents` with the waiting
/// badge, and the `+` and `⋯` menus in one pill. No title, no connection
/// chip: the connection lives in the capsule.
struct TopBar<NewMenu: View, MoreMenu: View>: View {
    @Binding var pane: MainPane
    let waiting: Int
    @ViewBuilder let newMenu: () -> NewMenu
    @ViewBuilder let moreMenu: () -> MoreMenu

    var body: some View {
        HStack(spacing: 8) {
            PaneSwitcher(pane: $pane, waiting: waiting)
                .fixedSize(horizontal: true, vertical: false)
            Spacer(minLength: 0)
            HStack(spacing: 0) {
                Menu(content: newMenu) {
                    Image(systemName: "plus")
                        .font(.system(size: 17, weight: .semibold))
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("New")
                .accessibilityIdentifier("toolbar.new")
                Menu(content: moreMenu) {
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
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        }
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

/// Errors in plain words, with the one button that helps.
struct BannerView: View {
    let banner: SessionViewModel.Banner
    let retry: () -> Void
    let dismiss: () -> Void

    private var tint: Color {
        switch banner.tone {
        case .error: return Color(.systemRed)
        case .warning: return Theme.blocked
        case .info: return Theme.working
        }
    }

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: banner.tone == .info ? "info.circle.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(tint)
                .padding(.top, 1)
            VStack(alignment: .leading, spacing: 3) {
                Text(banner.message)
                    .font(.subheadline.weight(.medium))
                    .fixedSize(horizontal: false, vertical: true)
                if let hint = banner.hint {
                    Text(LocalizedStringKey(hint))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                HStack(spacing: 12) {
                    if let retryTitle = banner.retryTitle {
                        Button(retryTitle, action: retry)
                            .font(.caption.weight(.semibold))
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                            .tint(Theme.primaryText)
                    }
                    if let help = banner.help {
                        Link("How to fix this", destination: help)
                            .font(.caption.weight(.semibold))
                            .tint(Theme.primaryText)
                            .accessibilityIdentifier("banner.help")
                    }
                }
                .padding(.top, 3)
            }
            Spacer(minLength: 0)
            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.secondary)
                    .frame(width: 44, height: 44)
                    .contentShape(Rectangle())
            }
            .padding(-12)
            .accessibilityLabel("Dismiss")
        }
        .padding(12)
        .background(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .fill(tint.opacity(0.10))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(tint.opacity(0.35), lineWidth: 1)
        )
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("banner")
    }
}

/// The approval card pinned above the controls: its whole height when it
/// fits in half the screen, else half the screen with the card scrolling
/// inside — never squeezed by the conversation into one-line options.
struct PinnedApprovalCard: View {
    let card: ApprovalFlow.Card
    let answer: (Int) -> Void

    /// The card's own height, measured.
    @State private var contentHeight: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    /// Half the screen; a third at the accessibility text sizes, where the
    /// header, the agent strip and the controls are tall too and a half-
    /// screen card pushed the demo header up under the navigation bar.
    private var maxHeight: CGFloat {
        let screen = UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.screen.bounds.height }.first ?? 800
        return screen * (dynamicTypeSize.isAccessibilitySize ? 0.3 : 0.5)
    }

    var body: some View {
        ScrollView {
            ApprovalCardView(card: card, answer: answer)
                .padding(.vertical, 4)
                .background(GeometryReader { proxy in
                    Color.clear.preference(key: CardHeightKey.self, value: proxy.size.height)
                })
        }
        .scrollBounceBehavior(.basedOnSize)
        .scrollIndicators(contentHeight > maxHeight ? .visible : .hidden)
        .frame(height: min(max(contentHeight, 1), maxHeight))
        .onPreferenceChange(CardHeightKey.self) { height in
            // Whole points only: a sub-point wobble must not relayout.
            let rounded = height.rounded(.up)
            if rounded != contentHeight { contentHeight = rounded }
        }
    }
}

private struct CardHeightKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
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
                Text("Scripted agents, on this phone only")
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
