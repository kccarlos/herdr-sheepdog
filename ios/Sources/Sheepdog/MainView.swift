import SheepdogKit
import SwiftUI

/// One screen: the flock across the top, the conversation in the middle,
/// the orb and three buttons at the bottom — everything in thumb reach.
struct MainView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var selectedAgent: Agent?
    @State private var started = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if let banner = model.banner {
                    BannerView(banner: banner, retry: { model.retry() }, dismiss: { model.banner = nil })
                        .padding(.horizontal, 16)
                        .padding(.bottom, 8)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }

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

                Divider()

                ConversationView(
                    rows: model.rows,
                    voiceName: model.voiceName,
                    canLoadOlder: model.canLoadOlder,
                    loadingOlder: model.loadingOlder,
                    activePendingID: model.approval?.pending.id,
                    loadOlder: { await model.loadOlder() }
                )
                .frame(maxHeight: .infinity)

                if let card = model.approval {
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
                } else {
                    PresenceOrb(state: model.state, level: model.micLevel, voiceName: model.voiceName)
                        .padding(.top, 4)
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

                ControlBar(
                    connected: model.wantsConnection,
                    muted: model.isMuted,
                    canStop: model.state.capturing || model.state == .speaking,
                    onConnect: { model.toggleConnection() },
                    onMute: { model.toggleMute() },
                    onStop: { model.stop() }
                )
            }
            .animation(.easeInOut(duration: 0.2), value: model.banner)
            .animation(.spring(duration: 0.3), value: model.approval)
            .animation(.easeInOut(duration: 0.2), value: model.showMuteHint)
            .navigationTitle(model.voiceName)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    HStack(spacing: 6) {
                        Circle()
                            .fill(Theme.color(for: model.state))
                            .frame(width: 8, height: 8)
                        Text(model.endpointDisplay)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    .accessibilityLabel("Daemon \(model.endpointDisplay), \(model.state.title)")
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
            .sheet(isPresented: $model.showSettings) {
                SettingsView()
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
                case .background: model.willResignActive()
                default: break
                }
            }
        }
    }
}

/// Connect is the primary action; Mute and Stop sit either side of it, all
/// with 56-point targets.
struct ControlBar: View {
    let connected: Bool
    let muted: Bool
    let canStop: Bool
    let onConnect: () -> Void
    let onMute: () -> Void
    let onStop: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            CircleButton(
                systemImage: muted ? "mic.slash.fill" : "mic.fill",
                title: muted ? "Unmute" : "Mute",
                hint: SessionViewModel.muteHint,
                tint: muted ? Color(.systemRed) : Color(.label),
                filled: muted,
                enabled: connected,
                action: onMute
            )

            Button(action: onConnect) {
                Label(connected ? "Disconnect" : "Connect", systemImage: connected ? "stop.circle" : "waveform.circle.fill")
                    .font(.headline)
                    .frame(maxWidth: .infinity, minHeight: 56)
            }
            .buttonStyle(.borderedProminent)
            .accessibilityIdentifier("controls.connect")
            .tint(connected ? Color(.systemGray) : .accentColor)
            .accessibilityHint(connected ? "Closes the connection to the daemon" : "Opens the voice session")

            CircleButton(
                systemImage: "hand.raised.fill",
                title: "Stop",
                tint: Color(.systemRed),
                filled: false,
                enabled: connected && canStop,
                action: onStop
            )
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 8)
        .background(.bar)
    }
}

struct CircleButton: View {
    let systemImage: String
    let title: String
    var hint: String? = nil
    let tint: Color
    let filled: Bool
    let enabled: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 20, weight: .medium))
                .frame(width: 56, height: 56)
                .background(
                    Circle().fill(filled ? tint.opacity(0.18) : Color(.secondarySystemBackground))
                )
                .overlay(Circle().strokeBorder(filled ? tint.opacity(0.6) : .clear, lineWidth: 1.5))
                .foregroundStyle(enabled ? tint : Color(.tertiaryLabel))
        }
        .disabled(!enabled)
        .accessibilityLabel(title)
        .accessibilityHint(hint ?? "")
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
    }
}
