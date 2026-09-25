import SheepdogKit
import SwiftUI

/// One screen: the flock across the top, the conversation (or the Flock
/// pane) in the middle, an optional text field, a status line and the
/// compact voice bar (Mute, orb, End) at the bottom — everything in thumb
/// reach.
struct MainView: View {
    enum Pane: String, CaseIterable, Identifiable {
        case conversation = "Conversation"
        case flock = "Flock"
        case health = "Health"
        var id: String { rawValue }
    }

    @EnvironmentObject private var model: SessionViewModel
    @State private var selectedAgent: Agent?
    @State private var started = false
    @State private var confirmNewConversation = false
    @State private var pane: Pane = .conversation
    @State private var typing = false
    @Environment(\.scenePhase) private var scenePhase

    /// "Health · 2": problems in the last 15 minutes, so a glance at the
    /// switch says whether something went wrong.
    private var healthLabel: String {
        let recent = model.health.problems(since: Date().addingTimeInterval(-15 * 60)).filter { $0.severity != .info }.count
        return recent == 0 ? "Health" : "Health · \(recent)"
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if let banner = model.banner {
                    BannerView(banner: banner, retry: { model.bannerAction() }, dismiss: { model.banner = nil })
                        .padding(.horizontal, 16)
                        .padding(.bottom, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }

                Picker("Show", selection: $pane) {
                    Text("Conversation").tag(Pane.conversation)
                    Text(model.agents.isEmpty ? "Flock" : "Flock · \(model.agents.count)").tag(Pane.flock)
                    Text(healthLabel).tag(Pane.health)
                }
                .pickerStyle(.segmented)
                .padding(.horizontal, 16)
                .padding(.bottom, 8)
                .accessibilityIdentifier("main.pane")

                if pane == .conversation {
                    AgentStrip(agents: model.agents, question: { model.pendingQuestion(for: $0) }) { agent in
                        selectedAgent = agent
                    }
                    .padding(.bottom, 10)
                    // Two `.sheet` modifiers on the same view fight each other in
                    // SwiftUI and only the last one presents, so the agent sheet
                    // hangs off the strip and Settings off the navigation stack.
                    .sheet(item: $selectedAgent) { agent in
                        AgentSheet(agent: agent, question: model.pendingQuestion(for: agent))
                    }
                }

                Divider()

                Group {
                    switch pane {
                    case .conversation:
                        ConversationView(
                            rows: model.rows,
                            voiceName: model.voiceName,
                            canLoadOlder: model.canLoadOlder,
                            loadingOlder: model.loadingOlder,
                            activePendingID: model.approval?.pending.id,
                            loadOlder: { await model.loadOlder() },
                            followToken: model.typedSent
                        )
                    case .flock:
                        FlockView(agents: model.agents, approval: model.approval) { index in
                            model.answer(option: index)
                        }
                    case .health:
                        HealthView(meter: model.meter)
                    }
                }
                .frame(maxHeight: .infinity)

                // On the Flock pane the question sits on its agent's card.
                if pane != .flock, let card = model.approval {
                    ApprovalCardView(card: card) { index in
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

                if model.showMuteHint {
                    Text(SessionViewModel.muteHint)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 24)
                        .padding(.top, 6)
                        .transition(.opacity)
                        .onTapGesture { model.dismissMuteHint() }
                        .accessibilityIdentifier("controls.muteHint")
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

                // Fixed height regardless of which state string is showing:
                // a status line that grows or shrinks the VStack every time
                // the state changes (e.g. wrapping to two lines) resizes
                // the ConversationView above it on every transition, which
                // thrashes its "am I scrolled to the bottom" tracking badly
                // enough to lock up the main thread (a genuine SwiftUI
                // "onChange … multiple times per frame" fault, reproduced
                // while testing this bar).
                ZStack(alignment: .trailing) {
                    Text(model.statusLine)
                        .font(.footnote.weight(.medium))
                        .foregroundStyle(model.noMicSignal ? Theme.blocked : (model.isMuted ? Color(.systemRed) : .secondary))
                        .lineLimit(1)
                        .minimumScaleFactor(0.7)
                        .frame(height: 16)
                        .padding(.horizontal, 48)
                        .frame(maxWidth: .infinity)
                        .accessibilityIdentifier("controls.statusLine")
                    // Type instead of talking: a quiet room, or a test that
                    // needs the exact words.
                    Button {
                        withAnimation(.easeInOut(duration: 0.2)) { typing.toggle() }
                    } label: {
                        Image(systemName: typing ? "keyboard.chevron.compact.down" : "keyboard")
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(typing ? Color.accentColor : .secondary)
                            .frame(width: 44, height: 28)
                    }
                    .disabled(!model.isConfigured)
                    .accessibilityLabel(typing ? "Hide the text field" : "Type instead")
                    .accessibilityIdentifier("controls.type")
                    .padding(.trailing, 8)
                }
                .frame(height: 28)
                .padding(.top, 2)

                VoiceBarView(
                    state: model.state,
                    meter: model.meter,
                    muted: model.isMuted,
                    voiceName: model.voiceName,
                    talkMode: model.talkMode,
                    holding: model.holding,
                    noSignal: model.noMicSignal,
                    onConnect: { model.connect() },
                    onStop: { model.stop() },
                    onMute: {
                        model.dismissCoachMark()
                        model.toggleMute()
                    },
                    onEnd: { model.disconnect() },
                    onResume: { model.resumeAudio() },
                    onPress: { model.pressToTalk() },
                    onRelease: { model.releaseToTalk() }
                )
            }
            .animation(.easeInOut(duration: 0.2), value: model.banner)
            .animation(.spring(duration: 0.3), value: model.approval)
            .animation(.easeInOut(duration: 0.2), value: model.showMuteHint)
            .animation(.spring(duration: 0.35), value: model.showCoachMark)
            .animation(.easeInOut(duration: 0.2), value: pane)
            .navigationTitle(model.voiceName)
            .navigationBarTitleDisplayMode(.inline)
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
                        // The orb stops the dog while it talks; this is the
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
                Text("Sheepdog and the brain forget this conversation; your agents, open questions and 'tell me when…' reminders stay.")
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
                Button("Got it", action: dismiss)
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white)
                    .buttonStyle(.plain)
                    .padding(.top, 2)
                    .accessibilityIdentifier("controls.coachMark.dismiss")
            }
            .padding(12)
            .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Theme.done))
            // The pointer, over the mic button (the first slot of the bar).
            Triangle()
                .fill(Theme.done)
                .frame(width: 18, height: 9)
                .padding(.leading, 27)
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
                    Text(hint)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let retryTitle = banner.retryTitle {
                    Button(retryTitle, action: retry)
                        .font(.caption.weight(.semibold))
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .padding(.top, 3)
                }
            }
            Spacer(minLength: 0)
            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.secondary)
            }
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
/// conversation like anything else the shepherd said.
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
                        .frame(width: 28, height: 32)
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
