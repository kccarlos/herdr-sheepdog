import SheepdogKit
import SwiftUI

/// One screen: the agents across the top, the conversation (or the Agents pane
/// pane) in the middle, an optional text field, a status line and the
/// compact voice bar (Mute, orb, End) at the bottom — everything in thumb
/// reach.
struct MainView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var started = false
    @State private var confirmNewConversation = false
    @State private var typing = false
    @Environment(\.scenePhase) private var scenePhase

    private var paneState: FlockPaneState {
        FlockPaneState.resolve(chip: model.connectionChip.status, hasAgents: !model.flock.isEmpty)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if model.isDemo {
                    DemoHeader()
                        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                }
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

                PaneSwitcher(pane: $model.pane, waiting: model.flock.waitingCount)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 8)

                if model.pane == .conversation, !model.flock.running.isEmpty || paneState == .empty {
                    AgentStrip(agents: model.flock.running, pending: model.pendingAction)
                        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
                        .padding(.bottom, 10)
                }

                Divider()

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
                        FlockView(flock: model.flock, approval: model.approval, focusedPaneID: $model.focusedAgentPaneID) { index in
                            model.answer(option: index)
                        }
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

                // On the Agents pane the question sits on its agent's card.
                if model.pane != .flock, let card = model.approval {
                    PinnedApprovalCard(card: card) { index in
                        model.answer(option: index)
                    }
                    .padding(.horizontal, 12)
                    .padding(.top, 8)
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
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                    .onAppear {
                        AccessibilityNotification.Announcement(VoiceBarModel.coachMark(voiceName: model.voiceName)).post()
                    }
                }

                if typing {
                    TypeBar(
                        voiceName: model.voiceName,
                        sending: model.sendingTyped,
                        error: model.typedError,
                        send: { await model.sendTyped($0) },
                        close: {
                            typing = false
                            model.typedError = nil
                        }
                    )
                    .padding(.horizontal, 12)
                    .padding(.top, 8)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                }

                VoiceBarView(
                    state: model.state,
                    meter: model.meter,
                    muted: model.isMuted,
                    voiceName: model.voiceName,
                    talkMode: model.talkMode,
                    holding: model.holding,
                    noSignal: model.noMicSignal,
                    // The one-time Mute caption takes the status line's
                    // place rather than a row of its own over the chat.
                    statusText: model.showMuteHint ? model.muteCaption : model.statusLine,
                    statusTint: model.noMicSignal || model.state == .noMicrophone ? Theme.blockedText : .secondary,
                    typing: typing,
                    canType: model.isConfigured,
                    onConnect: { model.connect() },
                    onStop: { model.stop() },
                    onMute: {
                        model.dismissCoachMark()
                        model.toggleMute()
                    },
                    onEnd: { model.disconnect() },
                    onResume: { model.resumeAudio() },
                    onPress: { model.pressToTalk() },
                    onRelease: { model.releaseToTalk() },
                    onType: { withAnimation(.easeInOut(duration: 0.2)) { typing.toggle() } }
                )
                .dynamicTypeSize(...DynamicTypeSize.accessibility2)
            }
            .animation(.easeInOut(duration: 0.2), value: model.banner)
            .animation(.spring(duration: 0.3), value: model.approval)
            .animation(.easeInOut(duration: 0.2), value: model.showMuteHint)
            .animation(.spring(duration: 0.35), value: model.showCoachMark)
            .animation(.easeInOut(duration: 0.2), value: model.pane)
            .navigationTitle(model.voiceName)
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(for: AgentWindowRoute.self) { route in
                if let agent = model.flock.all.first(where: { $0.paneID == route.paneID }) {
                    AgentScreenView(agent: agent)
                } else {
                    AgentGoneView()
                }
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    ConnectionChipView()
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        if model.conversationResetSupported {
                            Button {
                                confirmNewConversation = true
                            } label: {
                                Label("New conversation", systemImage: "arrow.counterclockwise")
                            }
                            .accessibilityIdentifier("toolbar.newConversation")
                        }
                        // The orb stops the voice while it talks; this is the
                        // same Stop, reachable whatever the orb is doing.
                        Button {
                            model.stop()
                        } label: {
                            Label("Stop \(model.voiceName)", systemImage: "stop.circle")
                        }
                        .disabled(!model.wantsConnection)
                        .accessibilityIdentifier("toolbar.stop")
                        Button {
                            withAnimation(.easeInOut(duration: 0.2)) { typing = true }
                        } label: {
                            Label("Type instead", systemImage: "keyboard")
                        }
                        .disabled(!model.isConfigured)
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                    .accessibilityLabel("More")
                    .accessibilityIdentifier("toolbar.more")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        model.showSettings = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel("Settings")
                    .accessibilityIdentifier("toolbar.settings")
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
        }
    }
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

/// Type instead of talking: one line to the daemon, as if spoken
/// (`POST /api/inject/utterance`). What was typed shows up in the
/// conversation like anything else the user said.
struct TypeBar: View {
    let voiceName: String
    let sending: Bool
    let error: String?
    let send: (String) async -> Bool
    let close: () -> Void

    @State private var text = ""
    @FocusState private var focused: Bool

    private var canSend: Bool { !sending && !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                TextField("Say something to \(voiceName)…", text: $text, axis: .vertical)
                    .lineLimit(1 ... 4)
                    .textInputAutocapitalization(.sentences)
                    .submitLabel(.send)
                    .focused($focused)
                    .onSubmit(submit)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .accessibilityIdentifier("type.field")
                Button(action: submit) {
                    if sending {
                        ProgressView().frame(width: 32, height: 32)
                    } else {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.system(size: 30))
                            .foregroundStyle(canSend ? Color.accentColor : Color(.tertiaryLabel))
                    }
                }
                .disabled(!canSend)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("type.send")
                Button(action: close) {
                    Image(systemName: "xmark")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(.secondary)
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Close the text field")
            }
            if let error {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Color(.systemRed))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("type.error")
            }
        }
        .onAppear { focused = true }
    }

    private func submit() {
        guard canSend else { return }
        let sent = text
        Task {
            if await send(sent) {
                text = ""
                // Keyboard down, so the answer has the room to show.
                focused = false
            }
        }
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
