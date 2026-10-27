import SheepdogKit
import SwiftUI

/// The small voice capsule (canvas "iPad portrait — sidebar hidden"): a
/// 30 pt orb and "Sheepdog ⌄ / Listening". It sits in a window's toolbar
/// where the full capsule is not: iPad portrait with the sidebar hidden,
/// and every window that does not own the voice (`SceneCoordinator`).
/// The orb mutes and unmutes while the voice listens, and otherwise does
/// what the big orb does (connect, stop, resume). The name opens the voice
/// menu.
struct MiniCapsule: View {
    let menus: SessionMenus
    @EnvironmentObject private var model: SessionViewModel

    private var mode: CapsuleMode {
        CapsuleMode.resolve(state: model.voice.state, talkMode: model.voice.talkMode, holding: model.voice.holding, questionOpen: !model.openQuestionIDs.isEmpty)
    }

    private var bar: VoiceBarModel { VoiceBarModel(state: model.voice.state, talkMode: model.voice.talkMode) }

    /// Listening (or muted): the orb is Mute. Otherwise the big orb's own
    /// action.
    private var orbMutes: Bool {
        switch mode {
        case .live, .quiet, .muted: return bar.muteAvailability == .enabled
        default: return false
        }
    }

    var body: some View {
        HStack(spacing: 6) {
            Button(action: tapOrb) {
                MeteredOrb(meter: model.voice.meter, mode: mode, size: 30)
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
            }
            .buttonStyle(UndimmedButtonStyle())
            .disabled(!orbMutes && bar.orbAction == .none)
            .padding(.vertical, -3)
            .accessibilityLabel(orbLabel)
            .accessibilityIdentifier("mini.orb")
            stableMenu(
                MiniMenuLabel(items: menus.voiceValue, name: model.capsuleName, status: model.statusLine),
                probe: "mini",
                content: { menus.voiceItems($0.items) }
            ) { label in
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: 3) {
                        Text(label.name)
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                        Image(systemName: "chevron.down")
                            .font(.system(size: 9, weight: .bold))
                            .foregroundStyle(Theme.ink)
                    }
                    Text(label.status)
                        .font(.caption2)
                        .foregroundStyle(Theme.muted)
                }
                .lineLimit(1)
                .padding(.trailing, 10)
                .frame(minHeight: 44)
                .contentShape(Rectangle())
            }
            .menuOrder(.fixed)
            .accessibilityLabel("Voice: \(model.voiceName), \(model.statusLine)")
            .accessibilityHint("Shows the connection and End voice session")
            .accessibilityIdentifier("mini.menu")
        }
        .padding(.leading, 1)
        .frame(height: 38)
        .background(Capsule().fill(Theme.card.opacity(0.8)))
        .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
        // Toolbar chrome: it stays one line.
        .dynamicTypeSize(...DynamicTypeSize.xxLarge)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("mini.capsule")
    }

    private func tapOrb() {
        if orbMutes {
            model.voice.toggleMute()
            return
        }
        switch bar.orbAction {
        case .connect: model.connect()
        case .stop: model.voice.stop()
        case .resume: model.resumeAudio()
        case .holdToTalk, .none: break
        }
    }

    private var orbLabel: String {
        if orbMutes {
            return model.voice.isMuted ? "\(model.voiceName) is muted. Unmute" : "\(model.voiceName) is listening. Mute"
        }
        switch mode {
        case .offline: return "Connect"
        case .speaking, .thinking, .stalled: return "Stop \(model.voiceName)"
        case .paused: return "Resume audio"
        default: return model.voice.state.accessibilityLabel(voiceName: model.voiceName)
        }
    }
}

/// The mini capsule's menu button: what it shows, with the menu's value
/// (`StableMenu`).
struct MiniMenuLabel: Equatable {
    var items: SessionMenus.VoiceValue
    var name: String
    var status: String
}
